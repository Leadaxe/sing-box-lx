package impair

import (
	"strconv"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// Runtime control (SPEC 164): the StartedService commands SetImpair,
// ScheduleImpair and GetImpairStatus reach the outbound through these three
// methods. Requests and the status are JSON in the config's own schema: a
// SetImpair request is a schedule entry without "at", a ScheduleImpair list
// is a "schedule" list.

// ImpairSet applies a change now, on top of the running schedule: a
// schedule entry without "at" (link_params, sublinks, or both).
func (h *Outbound) ImpairSet(entry []byte) error {
	var options ScheduleEntry
	if err := json.UnmarshalDisallowUnknownFields(entry, &options); err != nil {
		return E.Cause(err, "impair: entry")
	}
	if options.At != "" {
		return E.New(`impair: entry: "at" has no place in a change applied now`)
	}
	ev, err := h.cfg.profiles.resolveEntry("entry", &options)
	if err != nil {
		return E.Cause(err, "impair")
	}
	h.shaper.startEvents()
	h.shaper.handleReset(h.shaper.model.set(&ev))
	return nil
}

// ImpairSchedule replaces the schedule: the line and the sublinks go back
// to the config, then entries (a JSON list of schedule entries) run from
// now, or from the next traffic with start "first_use", and start over
// every repeat when it is set. An empty list only restores the config.
func (h *Outbound) ImpairSchedule(entries []byte, start string, repeat string) error {
	var options []ScheduleEntry
	if err := json.UnmarshalDisallowUnknownFields(entries, &options); err != nil {
		return E.Cause(err, "impair: schedule")
	}
	var firstUse bool
	switch start {
	case "", "now":
	case startFirstUse:
		firstUse = true
	default:
		return E.New("impair: start: ", strconv.Quote(start), ` must be "now" or "first_use"`)
	}
	var every time.Duration
	if repeat != "" {
		var err error
		if every, err = parseDuration("repeat", repeat, 100*time.Millisecond, 7*24*time.Hour); err != nil {
			return E.Cause(err, "impair")
		}
	}
	sched, err := h.cfg.profiles.resolveSchedule("schedule", options, every)
	if err != nil {
		return E.Cause(err, "impair")
	}
	h.shaper.startEvents()
	h.shaper.model.replaceSchedule(sched, firstUse, every)
	return nil
}

// ImpairStatus is the running state as JSON.
func (h *Outbound) ImpairStatus() ([]byte, error) {
	status := h.shaper.model.status()
	status.Tag = h.Tag()
	return json.Marshal(status)
}

// Status is the JSON of GetImpairStatus: the outbound's line, its
// sublinks, each with its template, its state now and its counters, and
// where the schedule is.
type Status struct {
	Tag        string          `json:"tag"`
	LinkParams LineStatus      `json:"link_params"`
	Counters   CountersStatus  `json:"counters"`
	Sublinks   []SublinkStatus `json:"sublinks"`
	Schedule   ScheduleStatus  `json:"schedule"`
}

// LineStatus is a line now: the template it was built from (empty: none),
// both directions, the random stalls.
type LineStatus struct {
	Profile string      `json:"profile,omitempty"`
	Up      LinkStatus  `json:"up"`
	Down    LinkStatus  `json:"down"`
	Stall   StallStatus `json:"stall"`
}

// LinkStatus is one direction of a line now.
type LinkStatus struct {
	Rate        string  `json:"rate"`
	Vary        string  `json:"vary,omitempty"`
	Queue       string  `json:"queue"`
	Delay       string  `json:"delay"`
	Jitter      string  `json:"jitter"`
	Reorder     bool    `json:"reorder"`
	Loss        string  `json:"loss"`
	LossBurst   float64 `json:"loss_burst"`
	StreamStall string  `json:"stream_stall"`
	Outage      bool    `json:"outage"`
	Reset       bool    `json:"reset"`
}

// StallStatus is the random stalls of a line: off when every is empty.
type StallStatus struct {
	Every    string `json:"every,omitempty"`
	Duration string `json:"duration,omitempty"`
	Now      bool   `json:"now"`
}

// CountersStatus is the counters of a line per direction.
type CountersStatus struct {
	Up   DirCounters `json:"up"`
	Down DirCounters `json:"down"`
}

// DirCounters are the counters of one direction.
type DirCounters struct {
	Units         uint64 `json:"units"`
	Bytes         uint64 `json:"bytes"`
	DroppedQueue  uint64 `json:"dropped_queue"`
	DroppedLoss   uint64 `json:"dropped_loss"`
	DroppedOutage uint64 `json:"dropped_outage"`
	StreamStalls  uint64 `json:"stream_stalls"`
}

// SublinkStatus is a sublink: its match as written, the connections that
// took it, its line now and its counters.
type SublinkStatus struct {
	Match      MatchOptions   `json:"match"`
	Matched    uint64         `json:"matched"`
	LinkParams LineStatus     `json:"link_params"`
	Counters   CountersStatus `json:"counters"`
}

// ScheduleStatus is where the schedule is: entries applied of the total in
// this round, the round (from 0) when it repeats, and, once it runs, the
// time since the round began and the time of the next entry (both relative
// to the round's start, like "at").
type ScheduleStatus struct {
	Entries           int    `json:"entries"`
	Applied           int    `json:"applied"`
	Repeat            string `json:"repeat,omitempty"`
	Round             int    `json:"round"`
	WaitingForTraffic bool   `json:"waiting_for_traffic"`
	Elapsed           string `json:"elapsed,omitempty"`
	NextAt            string `json:"next_at,omitempty"`
}

func (m *model) status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.advance(now)
	status := Status{LinkParams: lineStatus(m.line, now), Counters: countersStatus(m.line), Sublinks: []SublinkStatus{}}
	for _, sub := range m.sublinks {
		m.advanceSub(sub, now)
		status.Sublinks = append(status.Sublinks, SublinkStatus{
			Match: *sub.spec.option.Match, Matched: sub.matched,
			LinkParams: lineStatus(sub.line, now), Counters: countersStatus(sub.line),
		})
	}
	status.Schedule = ScheduleStatus{
		Entries: len(m.sched), Applied: m.nextEvent, Round: m.round,
		WaitingForTraffic: !m.schedStarted && len(m.sched) > 0,
	}
	if m.repeat > 0 {
		status.Schedule.Repeat = m.repeat.String()
	}
	if m.schedStarted && len(m.sched) > 0 {
		status.Schedule.Elapsed = time.Duration(now - m.schedBase).Round(time.Millisecond).String()
		switch {
		case m.nextEvent < len(m.sched):
			status.Schedule.NextAt = m.sched[m.nextEvent].at.String()
		case m.repeat > 0:
			status.Schedule.NextAt = (m.repeat + m.sched[0].at).String()
		}
	}
	return status
}

func lineStatus(l *line, now int64) LineStatus {
	s := LineStatus{Profile: l.profile, Up: linkStatus(&l.links[dirUp]), Down: linkStatus(&l.links[dirDown])}
	if l.stall != nil {
		s.Stall = StallStatus{Every: l.stall.every.String(), Duration: l.stall.duration.String(), Now: l.stalled(now)}
	}
	return s
}

func linkStatus(k *link) LinkStatus {
	p := &k.p
	s := LinkStatus{
		Rate:        "unlimited",
		Delay:       p.delay.String(),
		Jitter:      p.jitter.String(),
		Reorder:     p.reorder,
		Loss:        strconv.FormatFloat(p.loss*100, 'g', 4, 64) + "%",
		LossBurst:   max(p.burst, 1),
		StreamStall: k.streamStall().String(),
		Outage:      p.outage,
		Reset:       k.resetOutage,
	}
	if p.rate > 0 {
		s.Rate = formatRate(p.rate)
	}
	if p.vary != nil {
		s.Vary = formatRate(p.vary.min) + ".." + formatRate(p.vary.max) + "/" + p.vary.every.String()
	}
	if p.queueBytes > 0 {
		s.Queue = strconv.Itoa(p.queueBytes) + "B"
	} else {
		s.Queue = p.queueTime.String()
	}
	return s
}

func countersStatus(l *line) CountersStatus {
	return CountersStatus{Up: dirCounters(l.links[dirUp].stats), Down: dirCounters(l.links[dirDown].stats)}
}

func dirCounters(s Stats) DirCounters {
	return DirCounters{
		Units: s.Units, Bytes: s.Bytes, DroppedQueue: s.DropQueue, DroppedLoss: s.DropLoss,
		DroppedOutage: s.DropOutage, StreamStalls: s.Stalls,
	}
}
