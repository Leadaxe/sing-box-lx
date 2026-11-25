package impair

import (
	"strconv"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// The JSON schema lives in package option (option.ImpairOutboundOptions);
// the short names below keep this package readable.
type (
	Options       = option.ImpairOutboundOptions
	LinkOptions   = option.ImpairLinkOptions
	VaryOptions   = option.ImpairVaryOptions
	StallOptions  = option.ImpairStallOptions
	ScheduleEntry = option.ImpairScheduleEntry
)

// ---- resolved form ----

const (
	dirUp   = 0
	dirDown = 1
)

var dirNames = [2]string{"up", "down"}

type varySpec struct {
	min, max float64
	every    time.Duration
}

// params is the running state of one direction's bottleneck.
type params struct {
	rate        float64 // bit/s; 0 = unlimited
	queueTime   time.Duration
	queueBytes  int // when > 0 the queue is this many bytes, else queueTime at the rate
	delay       time.Duration
	jitter      time.Duration
	reorder     bool
	loss        float64
	burst       float64 // mean loss burst length in packets; <= 1: independent losses
	streamStall time.Duration
	vary        *varySpec
	drawNow     bool // vary set without a rate: draw the first rate at once
}

const defaultQueue = 50 * time.Millisecond

func defaultParams() params {
	return params{queueTime: defaultQueue}
}

type stallSpec struct {
	every, duration time.Duration
}

type event struct {
	at     time.Duration
	dirs   [2]bool
	link   *LinkOptions // nil: no link change
	outage *bool
	reset  bool
}

const (
	startProcess  = "process"
	startFirstUse = "first_use"
)

type config struct {
	dirs     [2]params
	stall    *stallSpec
	schedule []event
	firstUse bool
	seed     int64
	seeded   bool
	profile  string
}

func hasLink(o *LinkOptions) bool {
	return o != nil && (o.Rate != "" || o.Queue != "" || o.Delay != "" || o.Jitter != "" || o.Reorder != nil ||
		o.Loss != "" || o.LossBurst != nil || o.StreamStall != "" || o.Vary != nil)
}

// applyLink applies the keys set in o to p. key prefixes the error text.
func applyLink(key string, o *LinkOptions, p *params) error {
	if o == nil {
		return nil
	}
	join := func(name string) string {
		if key == "" {
			return name
		}
		return key + "." + name
	}
	if o.Rate != "" {
		rate, err := parseRate(join("rate"), o.Rate)
		if err != nil {
			return err
		}
		p.rate = rate
		p.drawNow = false
		if o.Vary == nil {
			p.vary = nil // a fixed rate replaces a variation from a lower layer
		}
	}
	if o.Vary != nil {
		vkey := join("vary")
		if o.Vary.Min == "" || o.Vary.Max == "" || o.Vary.Every == "" {
			return E.New(vkey, ": min, max and every are required")
		}
		minRate, err := parseRate(vkey+".min", o.Vary.Min)
		if err != nil {
			return err
		}
		maxRate, err := parseRate(vkey+".max", o.Vary.Max)
		if err != nil {
			return err
		}
		if minRate == 0 || maxRate == 0 {
			return E.New(vkey, `: min and max must be rates, not "unlimited"`)
		}
		if minRate > maxRate {
			return E.New(vkey, ": min ", o.Vary.Min, " is above max ", o.Vary.Max)
		}
		every, err := parseDuration(vkey+".every", o.Vary.Every, 10*time.Millisecond, time.Hour)
		if err != nil {
			return err
		}
		p.vary = &varySpec{min: minRate, max: maxRate, every: every}
		p.drawNow = o.Rate == ""
	}
	if o.Queue != "" {
		queueTime, queueBytes, err := parseQueue(join("queue"), o.Queue)
		if err != nil {
			return err
		}
		p.queueTime, p.queueBytes = queueTime, queueBytes
	}
	if o.Delay != "" {
		delay, err := parseDuration(join("delay"), o.Delay, 0, 10*time.Second)
		if err != nil {
			return err
		}
		p.delay = delay
	}
	if o.Jitter != "" {
		jitter, err := parseDuration(join("jitter"), o.Jitter, 0, 10*time.Second)
		if err != nil {
			return err
		}
		p.jitter = jitter
	}
	if o.Reorder != nil {
		p.reorder = *o.Reorder
	}
	if o.Loss != "" {
		loss, err := parsePercent(join("loss"), o.Loss)
		if err != nil {
			return err
		}
		p.loss = loss
	}
	if o.LossBurst != nil {
		burst := *o.LossBurst
		if burst < 1 || burst > 1000 {
			return E.New(join("loss_burst"), ": ", strconv.FormatFloat(burst, 'g', -1, 64), " must be within 1..1000 (mean lost packets in a row; 1 = independent losses)")
		}
		p.burst = burst
	}
	if o.StreamStall != "" {
		stall, err := parseDuration(join("stream_stall"), o.StreamStall, time.Millisecond, 60*time.Second)
		if err != nil {
			return err
		}
		p.streamStall = stall
	}
	return nil
}

// profiles are named starting points; every key set explicitly overrides
// them. They are described key by key in docs-lx/lx-impair.md.
type profile struct {
	both, up, down LinkOptions
	stall          *StallOptions
}

func ptrFloat(v float64) *float64 { return &v }

var profiles = map[string]profile{
	// A steady asymmetric wired line: no variation, no loss, a deep buffer.
	"dsl": {
		both: LinkOptions{Queue: "100ms", Delay: "12ms", Jitter: "1ms"},
		up:   LinkOptions{Rate: "2mbit"},
		down: LinkOptions{Rate: "16mbit"},
	},
	// A mobile carrier while walking: the rate jumps every second, a deep
	// buffer, bursty loss and a short stall now and then.
	"lte-walk": {
		both:  LinkOptions{Queue: "120ms", Delay: "35ms", Jitter: "10ms", Loss: "0.3%", LossBurst: ptrFloat(3)},
		up:    LinkOptions{Rate: "6mbit", Vary: &VaryOptions{Min: "2mbit", Max: "12mbit", Every: "1s"}},
		down:  LinkOptions{Rate: "15mbit", Vary: &VaryOptions{Min: "4mbit", Max: "30mbit", Every: "1s"}},
		stall: &StallOptions{Every: "20s", Duration: "400ms"},
	},
	// A congested Wi-Fi hop: short delay, large jitter, frequent small rate
	// swings and a lot of loss in short bursts.
	"wifi-lossy": {
		both: LinkOptions{
			Rate: "25mbit", Vary: &VaryOptions{Min: "8mbit", Max: "40mbit", Every: "500ms"},
			Queue: "40ms", Delay: "4ms", Jitter: "6ms", Loss: "2%", LossBurst: ptrFloat(2),
		},
	},
}

// resolve validates every key and returns the resolved config.
func resolve(options Options) (*config, error) {
	cfg := &config{dirs: [2]params{defaultParams(), defaultParams()}}
	if options.Profile != "" {
		prof, ok := profiles[options.Profile]
		if !ok {
			return nil, E.New("profile: unknown profile ", strconv.Quote(options.Profile), ` (known: "dsl", "lte-walk", "wifi-lossy")`)
		}
		cfg.profile = options.Profile
		for dir, own := range [2]LinkOptions{prof.up, prof.down} {
			if err := applyLink("profile", &prof.both, &cfg.dirs[dir]); err != nil {
				return nil, err
			}
			if err := applyLink("profile", &own, &cfg.dirs[dir]); err != nil {
				return nil, err
			}
		}
		if prof.stall != nil {
			stall, err := resolveStall(prof.stall)
			if err != nil {
				return nil, err
			}
			cfg.stall = stall
		}
	}
	for dir := range cfg.dirs {
		if err := applyLink("", &options.ImpairLinkOptions, &cfg.dirs[dir]); err != nil {
			return nil, err
		}
	}
	if err := applyLink("up", options.Up, &cfg.dirs[dirUp]); err != nil {
		return nil, err
	}
	if err := applyLink("down", options.Down, &cfg.dirs[dirDown]); err != nil {
		return nil, err
	}
	if options.Stall != nil {
		stall, err := resolveStall(options.Stall)
		if err != nil {
			return nil, err
		}
		cfg.stall = stall
	}
	switch options.Start {
	case "", startProcess:
	case startFirstUse:
		cfg.firstUse = true
	default:
		return nil, E.New("start: ", strconv.Quote(options.Start), ` must be "process" or "first_use"`)
	}
	if options.Seed != nil {
		cfg.seed, cfg.seeded = *options.Seed, true
	}
	schedule, err := resolveSchedule("schedule", options.Schedule, cfg.dirs[dirUp])
	if err != nil {
		return nil, err
	}
	cfg.schedule = schedule
	return cfg, nil
}

// resolveSchedule checks a schedule (from the config, or from the
// ScheduleImpair command, under the key prefix) and resolves it. base is
// the link the keys are validated against.
func resolveSchedule(prefix string, entries []ScheduleEntry, base params) ([]event, error) {
	var schedule []event
	var last time.Duration
	for index := range entries {
		entry := &entries[index]
		key := prefix + "[" + strconv.Itoa(index) + "]"
		if entry.At == "" {
			return nil, E.New(key, ".at is required")
		}
		at, err := parseDuration(key+".at", entry.At, 0, 7*24*time.Hour)
		if err != nil {
			return nil, err
		}
		if at < last {
			return nil, E.New(key, ".at ", entry.At, " is before the previous entry: list the entries in time order")
		}
		last = at
		ev, err := resolveEntry(key, entry, base)
		if err != nil {
			return nil, err
		}
		ev.at = at
		schedule = append(schedule, ev)
	}
	return schedule, nil
}

// resolveEntry checks one change of the link (a schedule entry, or the
// SetImpair command) apart from its time.
func resolveEntry(key string, entry *ScheduleEntry, base params) (event, error) {
	ev := event{outage: entry.Outage, reset: entry.Reset}
	switch entry.Direction {
	case "":
		ev.dirs = [2]bool{true, true}
	case "up":
		ev.dirs[dirUp] = true
	case "down":
		ev.dirs[dirDown] = true
	default:
		return ev, E.New(key, ".direction: ", strconv.Quote(entry.Direction), ` must be "up" or "down" (absent: both)`)
	}
	if entry.Reset && (entry.Outage == nil || !*entry.Outage) {
		return ev, E.New(key, `.reset needs "outage": true in the same entry`)
	}
	if hasLink(&entry.ImpairLinkOptions) {
		// validate against a scratch copy; the entry is applied as is at its time
		scratch := base
		if err := applyLink(key, &entry.ImpairLinkOptions, &scratch); err != nil {
			return ev, err
		}
		link := entry.ImpairLinkOptions
		ev.link = &link
	}
	if ev.link == nil && ev.outage == nil {
		return ev, E.New(key, ": changes nothing (set a link key or outage)")
	}
	return ev, nil
}

func resolveStall(o *StallOptions) (*stallSpec, error) {
	if o.Every == "" || o.Duration == "" {
		return nil, E.New("stall: every and duration are required")
	}
	every, err := parseDuration("stall.every", o.Every, 100*time.Millisecond, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	duration, err := parseDuration("stall.duration", o.Duration, time.Millisecond, time.Hour)
	if err != nil {
		return nil, err
	}
	return &stallSpec{every: every, duration: duration}, nil
}
