package impair

import (
	"errors"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing/common/json"
)

// Tests of the v2 schema: profiles (built-in and lx.impair_profiles),
// sublinks as a second stage, schedule_repeat, the status.

func modelFromJSON(t *testing.T, options string, profiles string) (*model, *fakeClock) {
	t.Helper()
	var o Options
	if err := json.UnmarshalDisallowUnknownFields([]byte(options), &o); err != nil {
		t.Fatal(err)
	}
	if o.Seed == nil {
		o.Seed = seed(1)
	}
	var user map[string]ProfileOption
	if profiles != "" {
		if err := json.UnmarshalDisallowUnknownFields([]byte(profiles), &user); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := resolve(o, user)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{}
	return newModel(cfg, clock.now), clock
}

func resolveJSON(options string, profiles string) error {
	var o Options
	if err := json.UnmarshalDisallowUnknownFields([]byte(options), &o); err != nil {
		return err
	}
	var user map[string]ProfileOption
	if profiles != "" {
		if err := json.UnmarshalDisallowUnknownFields([]byte(profiles), &user); err != nil {
			return err
		}
	}
	_, err := resolve(o, user)
	return err
}

func setJSON(t *testing.T, m *model, entry string) bool {
	t.Helper()
	var options ScheduleEntry
	if err := json.UnmarshalDisallowUnknownFields([]byte(entry), &options); err != nil {
		t.Fatal(err)
	}
	ev, err := m.cfg.profiles.resolveEntry("entry", &options)
	if err != nil {
		t.Fatal(err)
	}
	return m.set(&ev)
}

const testProfiles = `{
  "home":   {"link_params": {"rate": "50mbit", "delay": "10ms"}},
  "metro":  {"link_params": {"profile": "lte-walk", "loss": "3%"}},
  "tunnel": {"link_params": {"outage": true}},
  "blink":  {"link_params": {"delay": "5ms", "stall": {"every": "40s", "duration": "20s"}}}
}`

// A profile is a template: the line starts from it and the other keys tune
// it; switching to another one drops everything the previous one set.
func TestProfileTemplateAndSwitch(t *testing.T) {
	m, clock := modelFromJSON(t, `{"link_params": {"profile": "metro", "delay": "50ms"}}`, testProfiles)
	st := m.status()
	if st.LinkParams.Profile != "metro" || st.LinkParams.Up.Loss != "3%" || st.LinkParams.Up.Delay != "50ms" ||
		st.LinkParams.Up.Vary == "" || st.LinkParams.Stall.Every != "20s" {
		t.Fatalf("metro tuned: %+v", st.LinkParams)
	}
	clock.set(time.Second)
	setJSON(t, m, `{"link_params": {"profile": "blink"}}`)
	st = m.status()
	if st.LinkParams.Profile != "blink" || st.LinkParams.Up.Loss != "0%" || st.LinkParams.Up.Vary != "" ||
		st.LinkParams.Up.Rate != "unlimited" || st.LinkParams.Up.Delay != "5ms" || st.LinkParams.Stall.Every != "40s" {
		t.Fatalf("switch to blink: %+v", st.LinkParams)
	}
	// a tuning without a profile keeps the template's name and the rest
	setJSON(t, m, `{"link_params": {"down": {"rate": "1mbit"}}}`)
	if st = m.status(); st.LinkParams.Profile != "blink" || st.LinkParams.Down.Rate != "1mbit" || st.LinkParams.Up.Delay != "5ms" {
		t.Fatalf("tuning: %+v", st.LinkParams)
	}
	// a switch drops an outage and the stalls
	setJSON(t, m, `{"link_params": {"profile": "tunnel", "reset": true}}`)
	if st = m.status(); !st.LinkParams.Up.Outage || !st.LinkParams.Down.Reset || st.LinkParams.Stall.Every != "" {
		t.Fatalf("tunnel: %+v", st.LinkParams)
	}
	setJSON(t, m, `{"link_params": {"profile": "home"}}`)
	if st = m.status(); st.LinkParams.Up.Outage || st.LinkParams.Up.Reset || st.LinkParams.Up.Rate != "50mbit" {
		t.Fatalf("home: %+v", st.LinkParams)
	}
	// {} turns vary and stall off
	setJSON(t, m, `{"link_params": {"profile": "metro"}}`)
	setJSON(t, m, `{"link_params": {"vary": false, "stall": false}}`)
	if st = m.status(); st.LinkParams.Up.Vary != "" || st.LinkParams.Stall.Every != "" || st.LinkParams.Up.Loss != "3%" {
		t.Fatalf("vary/stall off: %+v", st.LinkParams)
	}
}

func TestProfileErrors(t *testing.T) {
	for _, test := range []struct{ options, profiles, want string }{
		{`{"link_params": {"profile": "cafe"}}`, testProfiles, `unknown profile "cafe"`},
		{`{}`, `{"a": {"link_params": {"profile": "b"}}, "b": {"link_params": {"profile": "a"}}}`, "loop"},
		{`{}`, `{"a": {"link_params": {"outage": true, "reset": true}}}`, "reset"},
		{`{}`, `{"a": {"link_params": {"rate": "fast"}}}`, "lx.impair_profiles.a.link_params.rate"},
		{`{"schedule": [{"at": "1s", "link_params": {"profile": "home", "reset": true}}]}`, testProfiles, "reset needs"},
	} {
		err := resolveJSON(test.options, test.profiles)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s / %s: %v, want %q", test.options, test.profiles, err, test.want)
		}
	}
	// a user profile overrides a built-in one by name, and reset takes an
	// outage that comes from a profile
	if err := resolveJSON(`{"link_params": {"profile": "dsl"}, "schedule": [{"at": "1s", "link_params": {"profile": "tunnel", "reset": true}}]}`,
		`{"dsl": {"link_params": {"rate": "1mbit"}}, "tunnel": {"link_params": {"outage": true}}}`); err != nil {
		t.Fatal(err)
	}
}

func conn(network, domain, addr string, port uint16) connInfo {
	info := connInfo{network: network, domain: domain, port: port}
	if addr != "" {
		info.addr = netip.MustParseAddr(addr)
	}
	return info
}

// Sublinks pick connections by their fields; the first that matches wins;
// every and share choose among the matching ones.
func TestSublinkPick(t *testing.T) {
	m, _ := modelFromJSON(t, `{"sublinks": [
	  {"match": {"domain": ["site3.test"]}, "link_params": {"delay": "1s"}},
	  {"match": {"domain_suffix": ["sites.test"], "network": "tcp", "every": 3}, "link_params": {"delay": "2s"}},
	  {"match": {"ip_cidr": ["10.0.0.0/8"], "port": [443]}, "link_params": {"delay": "3s"}}
	]}`, "")
	if sub := m.pick(conn("tcp", "SITE3.test.", "", 443)); sub == nil || sub != m.sublinks[0] {
		t.Fatal("domain match")
	}
	var picked []bool
	for range 6 {
		picked = append(picked, m.pick(conn("tcp", "a.sites.test", "", 443)) == m.sublinks[1])
	}
	if got := bits(picked); got != "001001" {
		t.Fatalf("every 3: %s", got)
	}
	if m.pick(conn("udp", "a.sites.test", "", 443)) != nil {
		t.Fatal("network udp matched a tcp sublink")
	}
	if m.pick(conn("tcp", "", "10.1.2.3", 443)) != m.sublinks[2] || m.pick(conn("tcp", "", "10.1.2.3", 80)) != nil {
		t.Fatal("ip_cidr + port")
	}
	if st := m.status(); st.Sublinks[1].Matched != 2 || st.Sublinks[0].Matched != 1 {
		t.Fatalf("matched counters: %+v", st.Sublinks)
	}
}

func bits(values []bool) string {
	var b strings.Builder
	for _, v := range values {
		if v {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// share picks the same connections again after the sublinks are set anew.
func TestSublinkShareRepeats(t *testing.T) {
	m, _ := modelFromJSON(t, `{"sublinks": [{"match": {"share": "30%"}, "link_params": {"delay": "1s"}}]}`, "")
	run := func() string {
		var b strings.Builder
		for range 40 {
			if m.pick(conn("tcp", "x.test", "", 1)) != nil {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		return b.String()
	}
	a := run()
	m.replaceSchedule(nil, false, 0)
	if b := run(); a != b || !strings.Contains(a, "1") || !strings.Contains(a, "0") {
		t.Fatalf("share runs differ or degenerate: %s / %s", a, b)
	}
}

// A sublink is a second stage: a matched datagram leaves the outbound's
// line, then the sublink's; the outbound's outage stops it too.
func TestSublinkSecondStage(t *testing.T) {
	m, clock := modelFromJSON(t, `{"link_params": {"delay": "20ms"},
	  "sublinks": [{"match": {"domain": ["slow.test"]}, "link_params": {"delay": "100ms", "down": {"outage": true}}}]}`, "")
	sub := m.pick(conn("udp", "slow.test", "", 53))
	if due, v, _ := m.admitPacket(sub, dirUp, 100); v != verdictPass || due != ms(120) {
		t.Fatalf("second stage: due %v verdict %d", due, v)
	}
	if due, v, _ := m.admitPacket(nil, dirUp, 100); v != verdictPass || due != ms(20) {
		t.Fatalf("outbound line only: due %v verdict %d", due, v)
	}
	if _, v, _ := m.admitPacket(sub, dirDown, 100); v != verdictOutage {
		t.Fatal("the sublink's receive outage let a datagram in")
	}
	// a receive-only outage lets the connection open
	if refused, wait, _ := m.dialState(sub); refused != nil || wait != 0 {
		t.Fatal("a receive-only outage held the dial")
	}
	clock.set(time.Second)
	setJSON(t, m, `{"link_params": {"outage": true}}`)
	if _, v, _ := m.admitPacket(sub, dirUp, 100); v != verdictOutage {
		t.Fatal("the outbound's outage let a sublink datagram through")
	}
	if st := m.status(); st.Sublinks[0].Counters.Down.DroppedOutage != 1 || st.Counters.Up.DroppedOutage != 1 {
		t.Fatalf("counters: %+v / %+v", st.Counters, st.Sublinks[0].Counters)
	}
}

// reset in a sublink refuses its connections at once; the other ones dial.
func TestSublinkResetRefuses(t *testing.T) {
	m, _ := modelFromJSON(t, `{"sublinks": [{"match": {"domain": ["bad.test"]}, "link_params": {"outage": true, "reset": true}}]}`, "")
	refused, _, _ := m.dialState(m.pick(conn("tcp", "bad.test", "", 443)))
	if !errors.Is(refused, syscall.ECONNRESET) || !strings.Contains(refused.Error(), "link reset by sublink [domain bad.test]") {
		t.Fatalf("sublink reset: %v", refused)
	}
	if refused, wait, _ := m.dialState(m.pick(conn("tcp", "good.test", "", 443))); refused != nil || wait != 0 {
		t.Fatal("an unmatched connection was held")
	}
}

// A stream through a sublink gets both delays, and its backpressure.
func TestSublinkStream(t *testing.T) {
	m, _ := modelFromJSON(t, `{"link_params": {"delay": "10ms"},
	  "sublinks": [{"match": {"network": ["tcp"]}, "link_params": {"delay": "30ms", "rate": "8mbit", "queue": "5ms"}}]}`, "")
	sub := m.pick(conn("tcp", "x.test", "", 1))
	var st streamStates
	due, wait, _ := m.admitStream(sub, dirUp, &st, 1000)
	if wait != 0 || due != ms(10)+ms(1)+ms(30) {
		t.Fatalf("first segment: due %v wait %v", due, wait)
	}
	for range 5 {
		m.admitStream(sub, dirUp, &st, 1000)
	}
	if _, wait, _ = m.admitStream(sub, dirUp, &st, 1000); wait == 0 {
		t.Fatal("the sublink's full queue did not hold the stream")
	}
}

// schedule_repeat starts the schedule over every round.
func TestScheduleRepeat(t *testing.T) {
	m, clock := modelFromJSON(t, `{"schedule_repeat": "10s", "schedule": [
	  {"at": "0s", "link_params": {"outage": true}},
	  {"at": "4s", "link_params": {"outage": false}}
	]}`, "")
	for round := range 3 {
		base := time.Duration(round) * 10 * time.Second
		clock.set(base + time.Second)
		if !m.outage(nil, dirUp) {
			t.Fatalf("round %d: no outage at +1s", round)
		}
		clock.set(base + 5*time.Second)
		if m.outage(nil, dirUp) {
			t.Fatalf("round %d: outage at +5s", round)
		}
		if st := m.status(); st.Schedule.Round != round || st.Schedule.NextAt != "10s" || st.Schedule.Repeat != "10s" {
			t.Fatalf("round %d status: %+v", round, st.Schedule)
		}
	}
	if at, ok := m.nextWake(); !ok || at != int64(30*time.Second) {
		t.Fatalf("next wake %v %v, want 30s", at, ok)
	}
}

// A schedule entry can replace the sublinks; false removes them; a sublink
// change leaves the outbound's line alone.
func TestScheduleSublinks(t *testing.T) {
	m, clock := modelFromJSON(t, `{"link_params": {"delay": "20ms"}, "schedule": [
	  {"at": "1s", "sublinks": [{"match": {"domain": ["a.test"]}, "link_params": {"delay": "1s"}}]},
	  {"at": "2s", "sublinks": false}
	]}`, "")
	clock.set(1500 * time.Millisecond)
	m.advance(clock.t)
	if len(m.sublinks) != 1 || m.status().LinkParams.Up.Delay != "20ms" {
		t.Fatalf("at 1.5s: %d sublinks", len(m.sublinks))
	}
	clock.set(2500 * time.Millisecond)
	m.advance(clock.t)
	if len(m.sublinks) != 0 {
		t.Fatal(`false did not remove the sublinks`)
	}
}

// [] and {} are refused with a pointer to false: in a config file the
// decoder would drop them silently.
func TestEmptyRefused(t *testing.T) {
	for _, options := range []string{
		`{"link_params": {"vary": {}}}`,
		`{"link_params": {"stall": {}}}`,
		`{"schedule": [{"at": "1s", "sublinks": []}]}`,
		`{"sublinks": [{"match": {}, "link_params": {"delay": "1ms"}}]}`,
		`{"link_params": {"vary": true}}`,
		`{"link_params": {"stall": "off"}}`,
	} {
		err := resolveJSON(options, "")
		if err == nil {
			t.Errorf("%s: %v", options, err)
		}
	}
}

// The error of a reset names what began it, and the status has no profile
// key when the line has no template.
func TestResetCauseAndProfileOmitted(t *testing.T) {
	m, clock := modelFromJSON(t, `{"schedule": [{"at": "1s", "link_params": {"outage": true, "reset": true}}]}`, "")
	if content, _ := json.Marshal(m.status()); strings.Contains(string(content), `"profile"`) {
		t.Fatalf("empty profile in the status: %s", content)
	}
	clock.set(1500 * time.Millisecond)
	if refused, _, _ := m.dialState(nil); refused == nil || !strings.Contains(refused.Error(), "link reset by schedule at 1s") {
		t.Fatalf("schedule reset: %v", refused)
	}
	setJSON(t, m, `{"link_params": {"outage": true, "reset": true}}`)
	if refused, _, _ := m.dialState(nil); refused == nil || !strings.Contains(refused.Error(), "link reset by impair set") {
		t.Fatalf("set reset: %v", refused)
	}
	if cause := m.resetCause(); cause != "impair set" {
		t.Fatalf("reset cause %q", cause)
	}
}
