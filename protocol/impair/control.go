package impair

import (
	"strconv"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// Runtime control (SPEC 164): the StartedService commands SetImpair,
// ScheduleImpair and GetImpairStatus reach the outbound through these three
// methods. Requests and the status are JSON, so the commands keep one
// schema with the config: a SetImpair entry is a schedule entry without
// "at", a ScheduleImpair list is a "schedule" list.

// ImpairSet applies one change of the link now, on top of the running
// schedule: entry is a schedule entry without "at".
func (h *Outbound) ImpairSet(entry []byte) error {
	var options ScheduleEntry
	if err := json.UnmarshalDisallowUnknownFields(entry, &options); err != nil {
		return E.Cause(err, "impair: entry")
	}
	if options.At != "" {
		return E.New(`impair: entry: "at" has no place in a change applied now`)
	}
	ev, err := resolveEntry("entry", &options, h.cfg.dirs[dirUp])
	if err != nil {
		return E.Cause(err, "impair")
	}
	h.shaper.startEvents()
	h.shaper.handleReset(h.shaper.model.set(&ev))
	return nil
}

// ImpairSchedule replaces the schedule: the link goes back to the config,
// then entries (a JSON list of schedule entries) run from now, or from the
// next traffic with start "first_use". An empty list only restores the
// config's link.
func (h *Outbound) ImpairSchedule(entries []byte, start string) error {
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
	sched, err := resolveSchedule("schedule", options, h.cfg.dirs[dirUp])
	if err != nil {
		return E.Cause(err, "impair")
	}
	h.shaper.startEvents()
	h.shaper.model.replaceSchedule(sched, firstUse)
	return nil
}

// ImpairStatus is the running state as JSON: the link of each direction
// with its counters, whether a random stall is on, and where the schedule
// is.
func (h *Outbound) ImpairStatus() ([]byte, error) {
	status := h.shaper.model.status()
	status.Tag = h.Tag()
	return json.Marshal(status)
}

// Status is the JSON of GetImpairStatus.
type Status struct {
	Tag      string         `json:"tag"`
	Up       LinkStatus     `json:"up"`
	Down     LinkStatus     `json:"down"`
	Stall    bool           `json:"stall"`
	Schedule ScheduleStatus `json:"schedule"`
}

// LinkStatus is one direction: its parameters now and its counters.
type LinkStatus struct {
	Rate          string  `json:"rate"`
	Vary          string  `json:"vary,omitempty"`
	Queue         string  `json:"queue"`
	Delay         string  `json:"delay"`
	Jitter        string  `json:"jitter"`
	Reorder       bool    `json:"reorder"`
	Loss          string  `json:"loss"`
	LossBurst     float64 `json:"loss_burst"`
	StreamStall   string  `json:"stream_stall"`
	Outage        bool    `json:"outage"`
	Reset         bool    `json:"reset"`
	Units         uint64  `json:"units"`
	Bytes         uint64  `json:"bytes"`
	DroppedQueue  uint64  `json:"dropped_queue"`
	DroppedLoss   uint64  `json:"dropped_loss"`
	DroppedOutage uint64  `json:"dropped_outage"`
	StreamStalls  uint64  `json:"stream_stalls"`
}

// ScheduleStatus is where the schedule is: entries applied of the total,
// and, once it runs, the time since its start and the time of the next
// entry (both relative to the start, like "at").
type ScheduleStatus struct {
	Entries           int    `json:"entries"`
	Applied           int    `json:"applied"`
	WaitingForTraffic bool   `json:"waiting_for_traffic"`
	Elapsed           string `json:"elapsed,omitempty"`
	NextAt            string `json:"next_at,omitempty"`
}

func (m *model) status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.advance(now)
	var status Status
	for dir, out := range [2]*LinkStatus{&status.Up, &status.Down} {
		l := &m.links[dir]
		*out = linkStatus(&l.p)
		out.Outage = m.outageLocked(dir, now)
		out.Reset = l.resetOutage
		out.Units, out.Bytes = l.stats.Units, l.stats.Bytes
		out.DroppedQueue, out.DroppedLoss, out.DroppedOutage = l.stats.DropQueue, l.stats.DropLoss, l.stats.DropOutage
		out.StreamStalls = l.stats.Stalls
	}
	status.Stall = m.stalled(now)
	status.Schedule = ScheduleStatus{Entries: len(m.sched), Applied: m.nextEvent, WaitingForTraffic: !m.schedStarted && len(m.sched) > 0}
	if m.schedStarted && len(m.sched) > 0 {
		status.Schedule.Elapsed = time.Duration(now - m.schedBase).Round(time.Millisecond).String()
		if m.nextEvent < len(m.sched) {
			status.Schedule.NextAt = m.sched[m.nextEvent].at.String()
		}
	}
	return status
}

func linkStatus(p *params) LinkStatus {
	s := LinkStatus{
		Rate:        "unlimited",
		Delay:       p.delay.String(),
		Jitter:      p.jitter.String(),
		Reorder:     p.reorder,
		Loss:        strconv.FormatFloat(p.loss*100, 'g', 4, 64) + "%",
		LossBurst:   max(p.burst, 1),
		StreamStall: (&link{p: *p}).streamStall().String(),
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
