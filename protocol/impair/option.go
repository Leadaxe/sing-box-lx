package impair

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// The JSON schema lives in package option (option.ImpairOutboundOptions);
// the short names below keep this package readable.
type (
	Options       = option.ImpairOutboundOptions
	LinkParams    = option.ImpairLinkParams
	LinkOptions   = option.ImpairLinkOptions
	VaryOptions   = option.ImpairVaryOptions
	StallOptions  = option.ImpairStallOptions
	SublinkOption = option.ImpairSublink
	MatchOptions  = option.ImpairMatch
	ScheduleEntry = option.ImpairScheduleEntry
	ProfileOption = option.ImpairProfile
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

// params is the running state of one direction of a line.
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
	outage      bool
}

const defaultQueue = 50 * time.Millisecond

func defaultParams() params {
	return params{queueTime: defaultQueue}
}

type stallSpec struct {
	every, duration time.Duration
}

// lineValues are the parameters of a whole line: both directions, the
// random stalls and the template it was built from.
type lineValues struct {
	dirs    [2]params
	stall   *stallSpec
	profile string
}

func neutralLine() lineValues {
	return lineValues{dirs: [2]params{defaultParams(), defaultParams()}}
}

// change is a checked link_params, applied to a running line at its time.
type change struct {
	lp *LinkParams
}

// sublinkSpec is a checked sublink: its matcher and its line from neutral.
type sublinkSpec struct {
	option SublinkOption
	match  matcher
	line   lineValues
	reset  bool
}

// event is a schedule entry or a SetImpair command.
type event struct {
	at       time.Duration
	link     *change
	sublinks *[]sublinkSpec // nil: unchanged
}

const (
	startProcess  = "process"
	startFirstUse = "first_use"
)

type config struct {
	line     lineValues
	sublinks []sublinkSpec
	schedule []event
	repeat   time.Duration
	firstUse bool
	seed     int64
	seeded   bool
	profiles *profileSet
}

func hasLink(o *LinkOptions) bool {
	return o != nil && (o.Rate != "" || o.Queue != "" || o.Delay != "" || o.Jitter != "" || o.Reorder != nil ||
		o.Loss != "" || o.LossBurst != nil || o.StreamStall != "" || o.Vary != nil || o.Outage != nil)
}

func varyOff(o *VaryOptions) bool { return o.Off }

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
	if o.Vary != nil && varyOff(o.Vary) {
		p.vary, p.drawNow = nil, false
	} else if o.Vary != nil {
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
	if o.Outage != nil {
		p.outage = *o.Outage
	}
	return nil
}

// ---- profiles ----

func ptrFloat(v float64) *float64 { return &v }

// builtinProfiles are the templates every config has; lx.impair_profiles
// may override them by name. They are described in docs-lx/lx-impair.md.
var builtinProfiles = map[string]*LinkParams{
	// A steady asymmetric wired line: no variation, no loss, a deep buffer.
	"dsl": {
		ImpairLinkOptions: LinkOptions{Queue: "100ms", Delay: "12ms", Jitter: "1ms"},
		Up:                &LinkOptions{Rate: "2mbit"},
		Down:              &LinkOptions{Rate: "16mbit"},
	},
	// A mobile carrier while walking: the rate jumps every second, a deep
	// buffer, bursty loss and a short stall now and then.
	"lte-walk": {
		ImpairLinkOptions: LinkOptions{Queue: "120ms", Delay: "35ms", Jitter: "10ms", Loss: "0.3%", LossBurst: ptrFloat(3)},
		Up:                &LinkOptions{Rate: "6mbit", Vary: &VaryOptions{Min: "2mbit", Max: "12mbit", Every: "1s"}},
		Down:              &LinkOptions{Rate: "15mbit", Vary: &VaryOptions{Min: "4mbit", Max: "30mbit", Every: "1s"}},
		Stall:             &StallOptions{Every: "20s", Duration: "400ms"},
	},
	// A congested Wi-Fi hop: short delay, large jitter, frequent small rate
	// swings and a lot of loss in short bursts.
	"wifi-lossy": {
		ImpairLinkOptions: LinkOptions{
			Rate: "25mbit", Vary: &VaryOptions{Min: "8mbit", Max: "40mbit", Every: "500ms"},
			Queue: "40ms", Delay: "4ms", Jitter: "6ms", Loss: "2%", LossBurst: ptrFloat(2),
		},
	},
}

// profileSet is the built-in templates plus lx.impair_profiles.
type profileSet struct {
	user map[string]ProfileOption
}

// lookup finds a template and the key its errors are reported under.
func (s *profileSet) lookup(name string) (*LinkParams, string, bool) {
	if s != nil {
		if p, ok := s.user[name]; ok {
			key := "lx.impair_profiles." + name + ".link_params"
			if p.LinkParams == nil {
				return &LinkParams{}, key, true
			}
			return p.LinkParams, key, true
		}
	}
	p, ok := builtinProfiles[name]
	return p, "profile " + strconv.Quote(name), ok
}

func (s *profileSet) names() string {
	set := map[string]bool{}
	for name := range builtinProfiles {
		set[name] = true
	}
	if s != nil {
		for name := range s.user {
			set[name] = true
		}
	}
	list := make([]string, 0, len(set))
	for name := range set {
		list = append(list, strconv.Quote(name))
	}
	sort.Strings(list)
	return strings.Join(list, ", ")
}

// check resolves every user profile once, so a broken one fails the load
// even when no line uses it yet.
func (s *profileSet) check() error {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.user))
	for name := range s.user {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := neutralLine()
		if _, err := s.apply("lx.impair_profiles."+name+".link_params", &LinkParams{Profile: name}, &v, 0); err != nil {
			return err
		}
	}
	return nil
}

const maxProfileDepth = 8

// apply applies link_params to a line. With a profile the line first goes
// back to neutral and takes the template. It reports which directions had
// their outage set by these params (the profile's own included).
func (s *profileSet) apply(key string, lp *LinkParams, v *lineValues, depth int) (outageSet [2]bool, err error) {
	if lp == nil {
		return outageSet, nil
	}
	if lp.Profile != "" {
		if depth >= maxProfileDepth {
			return outageSet, E.New(key, ".profile: ", strconv.Quote(lp.Profile), ": profiles refer to each other in a loop")
		}
		template, templateKey, ok := s.lookup(lp.Profile)
		if !ok {
			return outageSet, E.New(key, ".profile: unknown profile ", strconv.Quote(lp.Profile), " (known: ", s.names(), ")")
		}
		if template.Reset {
			return outageSet, E.New(templateKey, `.reset has no place in a profile`)
		}
		*v = neutralLine()
		inner, err := s.apply(templateKey, template, v, depth+1)
		if err != nil {
			return outageSet, err
		}
		outageSet = inner
		v.profile = lp.Profile
	}
	for dir := range v.dirs {
		if err = applyLink(key, &lp.ImpairLinkOptions, &v.dirs[dir]); err != nil {
			return outageSet, err
		}
		if lp.ImpairLinkOptions.Outage != nil {
			outageSet[dir] = true
		}
	}
	for dir, own := range [2]*LinkOptions{lp.Up, lp.Down} {
		if err = applyLink(key+"."+dirNames[dir], own, &v.dirs[dir]); err != nil {
			return outageSet, err
		}
		if own != nil && own.Outage != nil {
			outageSet[dir] = true
		}
	}
	if lp.Stall != nil {
		v.stall, err = resolveStall(key+".stall", lp.Stall)
		if err != nil {
			return outageSet, err
		}
	}
	return outageSet, nil
}

// where a link_params stands decides whether "reset" is allowed.
const (
	placeLine    = "line"    // the outbound's own link_params
	placeChange  = "change"  // a schedule entry or SetImpair
	placeSublink = "sublink" // a sublink: reset refuses the matched connections
)

// checkLinkParams validates link_params on a scratch line.
func (s *profileSet) checkLinkParams(key string, lp *LinkParams, base lineValues, place string) (lineValues, error) {
	v := base
	if lp == nil {
		return v, nil
	}
	if _, err := s.apply(key, lp, &v, 0); err != nil {
		return v, err
	}
	if lp.Reset {
		if place == placeLine {
			return v, E.New(key, `.reset: only a schedule entry, a SetImpair command or a sublink can reset`)
		}
		if !v.dirs[dirUp].outage && !v.dirs[dirDown].outage {
			return v, E.New(key, `.reset needs an outage in the same link_params ("outage": true, or a profile with one)`)
		}
	}
	return v, nil
}

// ---- resolve ----

// resolve validates every key and returns the resolved config.
func resolve(options Options, userProfiles map[string]ProfileOption) (*config, error) {
	cfg := &config{profiles: &profileSet{user: userProfiles}}
	if err := cfg.profiles.check(); err != nil {
		return nil, err
	}
	line, err := cfg.profiles.checkLinkParams("link_params", options.LinkParams, neutralLine(), placeLine)
	if err != nil {
		return nil, err
	}
	cfg.line = line
	if cfg.sublinks, err = cfg.profiles.resolveSublinks("sublinks", options.Sublinks); err != nil {
		return nil, err
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
	if options.ScheduleRepeat != "" {
		if cfg.repeat, err = parseDuration("schedule_repeat", options.ScheduleRepeat, 100*time.Millisecond, 7*24*time.Hour); err != nil {
			return nil, err
		}
	}
	if cfg.schedule, err = cfg.profiles.resolveSchedule("schedule", options.Schedule, cfg.repeat); err != nil {
		return nil, err
	}
	return cfg, nil
}

// resolveSchedule checks a schedule (from the config, or from the
// ScheduleImpair command, under the key prefix) and resolves it. With a
// repeat every entry must fall inside one round.
func (s *profileSet) resolveSchedule(prefix string, entries []ScheduleEntry, repeat time.Duration) ([]event, error) {
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
		if repeat > 0 && at >= repeat {
			return nil, E.New(key, ".at ", entry.At, " is not inside one round of schedule_repeat ", repeat.String())
		}
		last = at
		ev, err := s.resolveEntry(key, entry)
		if err != nil {
			return nil, err
		}
		ev.at = at
		schedule = append(schedule, ev)
	}
	return schedule, nil
}

// resolveEntry checks one change (a schedule entry, or the SetImpair
// command) apart from its time. link_params is checked against a neutral
// line: a key's range does not depend on the line it lands on, and whether
// reset has an outage is decided by these params alone.
func (s *profileSet) resolveEntry(key string, entry *ScheduleEntry) (event, error) {
	var ev event
	if entry.LinkParams == nil && entry.Sublinks == nil {
		return ev, E.New(key, ": changes nothing (set link_params or sublinks)")
	}
	if entry.LinkParams != nil {
		if !s.hasEffect(entry.LinkParams) {
			return ev, E.New(key, ".link_params: changes nothing")
		}
		if _, err := s.checkLinkParams(key+".link_params", entry.LinkParams, neutralLine(), placeChange); err != nil {
			return ev, err
		}
		ev.link = &change{lp: entry.LinkParams}
	}
	if entry.Sublinks != nil {
		if !entry.Sublinks.Off && len(entry.Sublinks.List) == 0 {
			return ev, E.New(key, `.sublinks: empty: write false to remove the sublinks`)
		}
		sublinks, err := s.resolveSublinks(key+".sublinks", entry.Sublinks.List)
		if err != nil {
			return ev, err
		}
		ev.sublinks = &sublinks
	}
	return ev, nil
}

func (s *profileSet) hasEffect(lp *LinkParams) bool {
	return lp.Profile != "" || hasLink(&lp.ImpairLinkOptions) || hasLink(lp.Up) || hasLink(lp.Down) || lp.Stall != nil
}

func (s *profileSet) resolveSublinks(prefix string, list []SublinkOption) ([]sublinkSpec, error) {
	specs := make([]sublinkSpec, 0, len(list))
	for index := range list {
		item := list[index]
		key := prefix + "[" + strconv.Itoa(index) + "]"
		if item.Match == nil || emptyMatch(item.Match) {
			return nil, E.New(key, ".match is required, with at least one field")
		}
		if item.LinkParams == nil {
			return nil, E.New(key, ".link_params is required")
		}
		match, err := newMatcher(key+".match", item.Match)
		if err != nil {
			return nil, err
		}
		line, err := s.checkLinkParams(key+".link_params", item.LinkParams, neutralLine(), placeSublink)
		if err != nil {
			return nil, err
		}
		specs = append(specs, sublinkSpec{option: item, match: match, line: line, reset: item.LinkParams.Reset})
	}
	return specs, nil
}

func resolveStall(key string, o *StallOptions) (*stallSpec, error) {
	if o.Off {
		return nil, nil
	}
	if o.Every == "" || o.Duration == "" {
		return nil, E.New(key, `: every and duration are required (false turns the stalls off)`)
	}
	every, err := parseDuration(key+".every", o.Every, 100*time.Millisecond, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	duration, err := parseDuration(key+".duration", o.Duration, time.Millisecond, time.Hour)
	if err != nil {
		return nil, err
	}
	return &stallSpec{every: every, duration: duration}, nil
}

// ---- matching ----

// matcher is a checked sublink match.
type matcher struct {
	domains  map[string]bool
	suffixes []string
	prefixes []netip.Prefix
	ports    map[uint16]bool
	networks map[string]bool
	every    int
	share    float64 // 0: every connection that matched the fields
}

func emptyMatch(o *MatchOptions) bool {
	return len(o.Domain) == 0 && len(o.DomainSuffix) == 0 && len(o.IPCIDR) == 0 && len(o.Port) == 0 &&
		len(o.Network) == 0 && o.Every == 0 && o.Share == ""
}

func newMatcher(key string, o *MatchOptions) (matcher, error) {
	var m matcher
	if len(o.Domain) > 0 {
		m.domains = make(map[string]bool)
		for _, domain := range o.Domain {
			m.domains[strings.ToLower(strings.TrimSuffix(domain, "."))] = true
		}
	}
	for _, suffix := range o.DomainSuffix {
		suffix = strings.ToLower(strings.TrimPrefix(strings.TrimSuffix(suffix, "."), "."))
		if suffix == "" {
			return m, E.New(key, ".domain_suffix: an empty suffix")
		}
		m.suffixes = append(m.suffixes, suffix)
	}
	for _, cidr := range o.IPCIDR {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			addr, addrErr := netip.ParseAddr(cidr)
			if addrErr != nil {
				return m, E.New(key, ".ip_cidr: ", strconv.Quote(cidr), " is neither an address nor a prefix")
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		m.prefixes = append(m.prefixes, prefix.Masked())
	}
	if len(o.Port) > 0 {
		m.ports = make(map[uint16]bool)
		for _, port := range o.Port {
			m.ports[port] = true
		}
	}
	if len(o.Network) > 0 {
		m.networks = make(map[string]bool)
		for _, network := range o.Network {
			if network != "tcp" && network != "udp" {
				return m, E.New(key, ".network: ", strconv.Quote(network), ` must be "tcp" or "udp"`)
			}
			m.networks[network] = true
		}
	}
	if o.Every < 0 || o.Every > 1000000 {
		return m, E.New(key, ".every: ", strconv.Itoa(o.Every), " must be within 1..1000000")
	}
	m.every = o.Every
	if o.Share != "" {
		share, err := parsePercent(key+".share", o.Share)
		if err != nil {
			return m, err
		}
		if share <= 0 {
			return m, E.New(key, ".share: 0% matches nothing")
		}
		m.share = share
	}
	return m, nil
}

// fields reports whether a connection matches the matcher's fields (every
// and share are decided by the caller, which keeps their state).
func (m *matcher) fields(network, domain string, addr netip.Addr, port uint16) bool {
	if m.networks != nil && !m.networks[network] {
		return false
	}
	if m.ports != nil && !m.ports[port] {
		return false
	}
	if m.domains != nil || m.suffixes != nil {
		domain = strings.ToLower(strings.TrimSuffix(domain, "."))
		hit := m.domains[domain]
		for _, suffix := range m.suffixes {
			if hit {
				break
			}
			hit = domain == suffix || strings.HasSuffix(domain, "."+suffix)
		}
		if !hit {
			return false
		}
	}
	if m.prefixes != nil {
		hit := false
		if addr.IsValid() {
			addr = addr.Unmap()
			for _, prefix := range m.prefixes {
				if prefix.Contains(addr) {
					hit = true
					break
				}
			}
		}
		if !hit {
			return false
		}
	}
	return true
}
