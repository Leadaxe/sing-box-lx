package impair

import (
	"math"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t int64 }

func (c *fakeClock) now() int64          { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t += int64(d) }
func (c *fakeClock) set(d time.Duration) { c.t = int64(d) }
func ms(v float64) int64                 { return int64(v * float64(time.Millisecond)) }
func seed(v int64) *int64                { return &v }
func boolp(v bool) *bool                 { return &v }
func floatp(v float64) *float64          { return &v }
func near(got, want, tol float64) bool   { return math.Abs(got-want) <= tol }
func newTestModel(t *testing.T, o Options) (*model, *fakeClock) {
	t.Helper()
	if o.Seed == nil {
		o.Seed = seed(1)
	}
	cfg, err := resolve(o, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{}
	return newModel(cfg, clock.now), clock
}

func TestTokenBucketRate(t *testing.T) {
	m, _ := newTestModel(t, Options{LinkParams: lp(LinkOptions{Rate: "8mbit"})})
	for i := 1; i <= 10; i++ {
		due, v, _ := m.admitPacket(nil, dirUp, 1000) // 1000 B at 1 MB/s = 1 ms
		if v != verdictPass || due != ms(float64(i)) {
			t.Fatalf("packet %d: due %v verdict %d, want %v", i, time.Duration(due), v, time.Duration(ms(float64(i))))
		}
	}
	// the other direction has its own bottleneck
	if due, _, _ := m.admitPacket(nil, dirDown, 1000); due != ms(1) {
		t.Fatalf("down due %v", time.Duration(due))
	}
}

func TestQueueTailDrop(t *testing.T) {
	for _, test := range []struct {
		queue string
		pass  int
	}{{"5ms", 6}, {"3000B", 4}, {"50ms", 20}} {
		m, _ := newTestModel(t, Options{LinkParams: lp(LinkOptions{Rate: "8mbit", Queue: test.queue})})
		pass := 0
		for range 20 {
			if _, v, _ := m.admitPacket(nil, dirUp, 1000); v == verdictPass {
				pass++
			}
		}
		if pass != test.pass || m.stats(dirUp).DropQueue != uint64(20-test.pass) {
			t.Errorf("queue %s: %d passed, %+v; want %d", test.queue, pass, m.stats(dirUp), test.pass)
		}
	}
	// the queue drains with time
	m, clock := newTestModel(t, Options{LinkParams: lp(LinkOptions{Rate: "8mbit", Queue: "5ms"})})
	for range 20 {
		m.admitPacket(nil, dirUp, 1000)
	}
	clock.set(6 * time.Millisecond)
	if _, v, _ := m.admitPacket(nil, dirUp, 1000); v != verdictPass {
		t.Fatal("queue did not drain")
	}
}

func TestDelayJitterOrder(t *testing.T) {
	for _, reorder := range []bool{false, true} {
		m, clock := newTestModel(t, Options{LinkParams: lp(LinkOptions{Delay: "20ms", Jitter: "5ms", Reorder: boolp(reorder)})})
		var last int64
		inversions := 0
		sum := 0.0
		const n = 10000
		for i := range n {
			clock.set(time.Duration(i) * time.Millisecond)
			due, v, _ := m.admitPacket(nil, dirUp, 100)
			if v != verdictPass {
				t.Fatal("dropped")
			}
			d := due - clock.t
			if d < ms(15) || (reorder && d > ms(25)) {
				t.Fatalf("reorder=%v: delay %v out of range", reorder, time.Duration(d))
			}
			if due < last {
				inversions++
			}
			last = due
			sum += float64(d)
		}
		mean := time.Duration(sum / n)
		if !reorder && inversions != 0 {
			t.Fatalf("order not preserved: %d inversions", inversions)
		}
		if reorder && inversions == 0 {
			t.Fatal("reorder: no inversion")
		}
		if mean < 19*time.Millisecond || mean > 23*time.Millisecond {
			t.Fatalf("reorder=%v: mean delay %v", reorder, mean)
		}
	}
}

func TestLossIndependent(t *testing.T) {
	m, _ := newTestModel(t, Options{LinkParams: lp(LinkOptions{Loss: "10%"})})
	const n = 100000
	for range n {
		m.admitPacket(nil, dirUp, 100)
	}
	share := float64(m.stats(dirUp).DropLoss) / n
	if !near(share, 0.10, 0.005) {
		t.Fatalf("loss share %.4f", share)
	}
}

func TestLossGilbertElliott(t *testing.T) {
	m, _ := newTestModel(t, Options{LinkParams: lp(LinkOptions{Loss: "5%", LossBurst: floatp(4)})})
	const n = 400000
	lost, bursts, run := 0, 0, 0
	for range n {
		_, v, _ := m.admitPacket(nil, dirUp, 100)
		if v == verdictLoss {
			lost++
			run++
		} else if run > 0 {
			bursts++
			run = 0
		}
	}
	share := float64(lost) / n
	meanBurst := float64(lost) / float64(bursts)
	if !near(share, 0.05, 0.006) || !near(meanBurst, 4, 0.4) {
		t.Fatalf("share %.4f mean burst %.2f", share, meanBurst)
	}
}

func TestVaryAndSeed(t *testing.T) {
	opts := Options{LinkParams: lp(LinkOptions{Rate: "10mbit", Vary: &VaryOptions{Min: "3mbit", Max: "12mbit", Every: "2s"}}), Seed: seed(42)}
	a, clockA := newTestModel(t, opts)
	b, clockB := newTestModel(t, opts)
	if r := a.rate(dirUp); r != 10e6 {
		t.Fatalf("initial rate %v", r)
	}
	seen := map[float64]bool{}
	for i := 1; i <= 50; i++ {
		clockA.set(time.Duration(i) * 2 * time.Second)
		clockB.set(time.Duration(i) * 2 * time.Second)
		ra, rb := a.rate(dirUp), b.rate(dirUp)
		if ra != rb {
			t.Fatal("same seed, different rates")
		}
		if ra < 3e6 || ra > 12e6 {
			t.Fatalf("rate %v out of range", ra)
		}
		// constant within the period
		clockA.add(1999 * time.Millisecond)
		if a.rate(dirUp) != ra {
			t.Fatal("rate changed inside the period")
		}
		seen[ra] = true
	}
	if len(seen) < 45 {
		t.Fatalf("only %d distinct rates", len(seen))
	}
	if a.rate(dirDown) == a.rate(dirUp) {
		t.Fatal("directions draw the same rates")
	}
	// vary without a rate draws at once
	c, _ := newTestModel(t, Options{LinkParams: lp(LinkOptions{Vary: &VaryOptions{Min: "3mbit", Max: "4mbit", Every: "1s"}})})
	if r := c.rate(dirUp); r < 3e6 || r > 4e6 {
		t.Fatalf("first draw %v", r)
	}
}

func TestSchedule(t *testing.T) {
	m, clock := newTestModel(t, Options{
		LinkParams: lp(LinkOptions{Rate: "8mbit"}),
		Schedule: []ScheduleEntry{
			{At: "1s", LinkParams: lp(LinkOptions{Loss: "100%"})},
			{At: "2s", LinkParams: lp(LinkOptions{Loss: "0%", Rate: "16mbit"})},
			{At: "3s", LinkParams: lp(LinkOptions{Outage: boolp(true)})},
			{At: "4s", LinkParams: lp(LinkOptions{Outage: boolp(false)})},
			{At: "5s", LinkParams: &LinkParams{Down: &LinkOptions{Outage: boolp(true)}, Reset: true}},
		},
	})
	check := func(at time.Duration, dir int, want verdict) {
		t.Helper()
		clock.set(at)
		if _, v, _ := m.admitPacket(nil, dir, 1000); v != want {
			t.Fatalf("at %v dir %d: verdict %d, want %d", at, dir, v, want)
		}
	}
	check(500*time.Millisecond, dirUp, verdictPass)
	check(1500*time.Millisecond, dirUp, verdictLoss)
	check(2500*time.Millisecond, dirUp, verdictPass)
	if r := m.rate(dirUp); r != 16e6 {
		t.Fatalf("rate %v after the schedule", r)
	}
	check(3500*time.Millisecond, dirUp, verdictOutage)
	check(3600*time.Millisecond, dirDown, verdictOutage)
	check(4500*time.Millisecond, dirDown, verdictPass)
	clock.set(5 * time.Second)
	if _, v, reset := m.admitPacket(nil, dirUp, 1000); v != verdictPass || !reset {
		t.Fatalf("reset entry: verdict %d reset %v", v, reset)
	}
	check(5100*time.Millisecond, dirDown, verdictOutage)
	if refused, _, _ := m.dialState(nil); refused == nil {
		t.Fatal("dial allowed during a resetting outage")
	}
}

func TestScheduleFirstUse(t *testing.T) {
	m, clock := newTestModel(t, Options{Start: "first_use", Schedule: []ScheduleEntry{{At: "1s", LinkParams: lp(LinkOptions{Outage: boolp(true)})}}})
	clock.set(10 * time.Second)
	if _, v, _ := m.admitPacket(nil, dirUp, 100); v != verdictPass {
		t.Fatal("schedule ran before the first use")
	}
	m.firstUse()
	clock.set(10500 * time.Millisecond)
	if _, v, _ := m.admitPacket(nil, dirUp, 100); v != verdictPass {
		t.Fatal("outage too early")
	}
	clock.set(11500 * time.Millisecond)
	if _, v, _ := m.admitPacket(nil, dirUp, 100); v != verdictOutage {
		t.Fatal("no outage 1s after the first use")
	}
}

func TestRandomStalls(t *testing.T) {
	m, clock := newTestModel(t, Options{LinkParams: &LinkParams{Stall: &StallOptions{Every: "1s", Duration: "100ms"}}})
	down := 0
	const n = 200000
	for i := range n {
		clock.set(time.Duration(i) * time.Millisecond)
		if m.outage(nil, dirUp) {
			down++
		}
	}
	share := float64(down) / n
	if !near(share, 0.1/1.1, 0.015) {
		t.Fatalf("outage share %.3f", share)
	}
}

func TestStreamBackpressureAndStall(t *testing.T) {
	m, clock := newTestModel(t, Options{LinkParams: lp(LinkOptions{Rate: "8mbit", Queue: "10ms"})})
	var st streamStates
	// 1 MB/s, segments of 1000 B: the 12th waits for the queue
	for i := 0; i < 11; i++ {
		if _, wait, _ := m.admitStream(nil, dirUp, &st, 1000); wait != 0 {
			t.Fatalf("segment %d waits %v", i, wait)
		}
	}
	_, wait, _ := m.admitStream(nil, dirUp, &st, 1000)
	if wait != time.Millisecond {
		t.Fatalf("wait %v, want 1ms", wait)
	}
	clock.add(wait)
	if _, wait, _ = m.admitStream(nil, dirUp, &st, 1000); wait != 0 {
		t.Fatal("still waiting after the backlog drained")
	}

	// loss as stall: 1 % per MSS, stall 300ms
	m, clock = newTestModel(t, Options{LinkParams: lp(LinkOptions{Loss: "1%", StreamStall: "300ms"})})
	st = streamStates{}
	const segments = 100000
	var last int64
	for i := range segments {
		clock.set(time.Duration(i) * time.Microsecond)
		due, _, _ := m.admitStream(nil, dirDown, &st, mss)
		if due < last {
			t.Fatal("stream delivery out of order")
		}
		if due-last > ms(250) && last > 0 && due-clock.t < ms(299) {
			t.Fatal("stall shorter than stream_stall")
		}
		last = due
	}
	stalls := float64(m.stats(dirDown).Stalls)
	if !near(stalls, segments*0.01, segments*0.01*0.1) {
		t.Fatalf("%v stalls, want about %v", stalls, segments*0.01)
	}
	// the default stall: max(200ms, 2·delay + 4·jitter)
	l := link{p: params{delay: 150 * time.Millisecond, jitter: 10 * time.Millisecond}}
	if l.streamStall() != 340*time.Millisecond {
		t.Fatal(l.streamStall())
	}
}

func TestStreamOutageWaits(t *testing.T) {
	m, clock := newTestModel(t, Options{Schedule: []ScheduleEntry{{At: "1s", LinkParams: lp(LinkOptions{Outage: boolp(true)})}, {At: "2s", LinkParams: lp(LinkOptions{Outage: boolp(false)})}}})
	var st streamStates
	clock.set(1500 * time.Millisecond)
	if _, wait, _ := m.admitStream(nil, dirUp, &st, 100); wait == 0 {
		t.Fatal("stream admitted during an outage")
	}
	if refused, wait, _ := m.dialState(nil); refused != nil || wait == 0 {
		t.Fatal("dial not held during a silent outage")
	}
	clock.set(2500 * time.Millisecond)
	if _, wait, _ := m.admitStream(nil, dirUp, &st, 100); wait != 0 {
		t.Fatal("stream held after the outage")
	}
}

func TestProfiles(t *testing.T) {
	for name := range builtinProfiles {
		cfg, err := resolve(Options{LinkParams: &LinkParams{Profile: name}}, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.line.dirs[dirUp].rate == 0 && cfg.line.dirs[dirUp].vary == nil || cfg.line.profile != name {
			t.Fatalf("%s: %+v", name, cfg.line)
		}
	}
	// an explicit rate replaces the profile's variation
	cfg, err := resolve(Options{LinkParams: &LinkParams{Profile: "lte-walk", ImpairLinkOptions: LinkOptions{Rate: "5mbit"}}}, nil)
	if err != nil || cfg.line.dirs[dirDown].vary != nil || cfg.line.dirs[dirDown].rate != 5e6 || cfg.line.dirs[dirDown].delay != 35*time.Millisecond {
		t.Fatalf("override: %+v %v", cfg.line.dirs[dirDown], err)
	}
}

func TestResolveErrors(t *testing.T) {
	for _, test := range []struct {
		options Options
		want    string
	}{
		{Options{LinkParams: lp(LinkOptions{Rate: "10mbps"})}, "rate"},
		{Options{LinkParams: lp(LinkOptions{Rate: "1bit"})}, "8kbit..10gbit"},
		{Options{LinkParams: &LinkParams{Up: &LinkOptions{Rate: "x"}}}, "link_params.up.rate"},
		{Options{LinkParams: lp(LinkOptions{Queue: "10"})}, "queue"},
		{Options{LinkParams: lp(LinkOptions{Queue: "100B"})}, "1500B..64MB"},
		{Options{LinkParams: lp(LinkOptions{Delay: "20"})}, "delay"},
		{Options{LinkParams: lp(LinkOptions{Delay: "20s"})}, "delay"},
		{Options{LinkParams: lp(LinkOptions{Loss: "0.5"})}, "percentage"},
		{Options{LinkParams: lp(LinkOptions{Loss: "101%"})}, "loss"},
		{Options{LinkParams: lp(LinkOptions{LossBurst: floatp(0.5)})}, "loss_burst"},
		{Options{LinkParams: lp(LinkOptions{Vary: &VaryOptions{Min: "5mbit", Max: "3mbit", Every: "1s"}})}, "above max"},
		{Options{LinkParams: lp(LinkOptions{Vary: &VaryOptions{Min: "5mbit", Max: "6mbit"}})}, "vary: min, max and every"},
		{Options{LinkParams: lp(LinkOptions{Vary: &VaryOptions{Min: "unlimited", Max: "6mbit", Every: "1s"}})}, "not \"unlimited\""},
		{Options{LinkParams: &LinkParams{Down: &LinkOptions{Vary: &VaryOptions{Min: "1mbit", Max: "6mbit", Every: "1ms"}}}}, "link_params.down.vary.every"},
		{Options{LinkParams: &LinkParams{Stall: &StallOptions{Every: "1s"}}}, "stall"},
		{Options{Start: "later"}, "start"},
		{Options{LinkParams: &LinkParams{Profile: "5g"}}, "unknown profile"},
		{Options{LinkParams: &LinkParams{ImpairLinkOptions: LinkOptions{Outage: boolp(true)}, Reset: true}}, "only a schedule entry"},
		{Options{Schedule: []ScheduleEntry{{LinkParams: lp(LinkOptions{Outage: boolp(true)})}}}, "schedule[0].at is required"},
		{Options{Schedule: []ScheduleEntry{{At: "2s", LinkParams: lp(LinkOptions{Outage: boolp(true)})}, {At: "1s", LinkParams: lp(LinkOptions{Outage: boolp(false)})}}}, "time order"},
		{Options{Schedule: []ScheduleEntry{{At: "1s"}}}, "changes nothing"},
		{Options{Schedule: []ScheduleEntry{{At: "1s", LinkParams: &LinkParams{Reset: true}}}}, "changes nothing"},
		{Options{Schedule: []ScheduleEntry{{At: "1s", LinkParams: &LinkParams{ImpairLinkOptions: LinkOptions{Delay: "1ms"}, Reset: true}}}}, "reset needs"},
		{Options{Schedule: []ScheduleEntry{{At: "1s", LinkParams: lp(LinkOptions{Rate: "fast"})}}}, "schedule[0].link_params.rate"},
		{Options{ScheduleRepeat: "10s", Schedule: []ScheduleEntry{{At: "10s", LinkParams: lp(LinkOptions{Delay: "1ms"})}}}, "one round"},
		{Options{Sublinks: []SublinkOption{{LinkParams: lp(LinkOptions{Delay: "1ms"})}}}, "match is required"},
		{Options{Sublinks: []SublinkOption{{Match: &MatchOptions{Every: 2}}}}, "link_params is required"},
		{Options{Sublinks: []SublinkOption{{Match: &MatchOptions{Network: []string{"icmp"}}, LinkParams: lp(LinkOptions{Delay: "1ms"})}}}, "network"},
		{Options{Sublinks: []SublinkOption{{Match: &MatchOptions{IPCIDR: []string{"10.0.0.0/33"}}, LinkParams: lp(LinkOptions{Delay: "1ms"})}}}, "ip_cidr"},
	} {
		_, err := resolve(test.options, nil)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%+v: error %v, want %q", test.options, err, test.want)
		}
	}
}

func TestParsers(t *testing.T) {
	for in, want := range map[string]float64{"10mbit": 10e6, "1.5mbit": 1.5e6, "500kbit": 500e3, "1gbit": 1e9, "8000bit": 8000, "unlimited": 0, "10MBIT": 10e6} {
		if got, err := parseRate("rate", in); err != nil || got != want {
			t.Errorf("parseRate(%q) = %v, %v", in, got, err)
		}
	}
	if d, b, err := parseQueue("q", "64KB"); err != nil || d != 0 || b != 65536 {
		t.Errorf("64KB: %v %v %v", d, b, err)
	}
	if p, err := parsePercent("l", "0.5%"); err != nil || p != 0.005 {
		t.Errorf("0.5%%: %v %v", p, err)
	}
}

func TestEventLogs(t *testing.T) {
	m, clock := newTestModel(t, Options{Schedule: []ScheduleEntry{
		{At: "1s", LinkParams: &LinkParams{ImpairLinkOptions: LinkOptions{Outage: boolp(true)}, Reset: true}},
		{At: "2s", LinkParams: &LinkParams{Up: &LinkOptions{Outage: boolp(false), Rate: "3mbit"}}},
	}})
	clock.set(1500 * time.Millisecond)
	reset, logs := m.tick()
	if !reset || len(logs) != 1 || logs[0].debug || logs[0].text != "schedule at 1s: up rate unlimited delay 0s outage (reset); down rate unlimited delay 0s outage (reset)" {
		t.Fatalf("at 1.5s: reset %v, logs %+v", reset, logs)
	}
	if at, ok := m.nextWake(); !ok || at != int64(2*time.Second) {
		t.Fatalf("next wake %v %v, want 2s", at, ok)
	}
	clock.set(2 * time.Second)
	if _, logs = m.tick(); len(logs) != 1 || logs[0].text != "schedule at 2s: up rate 3mbit delay 0s; down rate unlimited delay 0s outage (reset)" {
		t.Fatalf("at 2s: %+v", logs)
	}
	if _, ok := m.nextWake(); ok {
		t.Fatal("a wake after the last entry")
	}
}

func TestStallLogs(t *testing.T) {
	m, clock := newTestModel(t, Options{LinkParams: &LinkParams{Stall: &StallOptions{Every: "10s", Duration: "1s"}}})
	start, _ := m.nextWake()
	clock.set(time.Duration(start))
	if _, logs := m.tick(); len(logs) != 1 || !logs[0].debug || logs[0].text != "stall of the whole line for 1s" {
		t.Fatalf("stall start: %+v", logs)
	}
	end, _ := m.nextWake()
	if end != start+int64(time.Second) {
		t.Fatalf("stall end wake %v, want %v", end, start+int64(time.Second))
	}
	clock.set(time.Duration(end))
	if _, logs := m.tick(); len(logs) != 1 || logs[0].text != "stall ends" {
		t.Fatalf("stall end: %+v", logs)
	}
}

func mustEvent(t *testing.T, entry ScheduleEntry) *event {
	t.Helper()
	ev, err := (&profileSet{}).resolveEntry("entry", &entry)
	if err != nil {
		t.Fatal(err)
	}
	return &ev
}

func mustSchedule(t *testing.T, entries ...ScheduleEntry) []event {
	t.Helper()
	sched, err := (&profileSet{}).resolveSchedule("schedule", entries, 0)
	if err != nil {
		t.Fatal(err)
	}
	return sched
}

// set changes the link now; the config's schedule keeps running on top.
func TestSetOnTopOfSchedule(t *testing.T) {
	m, clock := newTestModel(t, Options{Schedule: []ScheduleEntry{{At: "2s", LinkParams: lp(LinkOptions{Outage: boolp(false)})}}})
	clock.set(time.Second)
	if reset := m.set(mustEvent(t, ScheduleEntry{LinkParams: &LinkParams{ImpairLinkOptions: LinkOptions{Outage: boolp(true)}, Reset: true}})); !reset {
		t.Fatal("set with reset did not report it")
	}
	if _, v, _ := m.admitPacket(nil, dirUp, 100); v != verdictOutage {
		t.Fatal("no outage after set")
	}
	if st := m.status(); !st.LinkParams.Up.Outage || !st.LinkParams.Up.Reset || st.Schedule.Applied != 0 || st.Schedule.NextAt != "2s" {
		t.Fatalf("status after set: %+v", st)
	}
	clock.set(2 * time.Second)
	if _, v, _ := m.admitPacket(nil, dirUp, 100); v != verdictPass {
		t.Fatal("the schedule did not end the outage at 2s")
	}
	_, logs := m.tick()
	if len(logs) != 2 || logs[0].text != "set: up rate unlimited delay 0s outage (reset); down rate unlimited delay 0s outage (reset)" || logs[1].text != "schedule at 2s: up rate unlimited delay 0s; down rate unlimited delay 0s" {
		t.Fatalf("logs %+v", logs)
	}
}

// A new schedule starts from the config's link, whatever set or the old
// schedule left; an empty one only restores the link.
func TestReplaceScheduleRestoresConfig(t *testing.T) {
	m, clock := newTestModel(t, Options{LinkParams: lp(LinkOptions{Rate: "10mbit"}), Schedule: []ScheduleEntry{{At: "1s", LinkParams: lp(LinkOptions{Outage: boolp(true)})}}})
	m.set(mustEvent(t, ScheduleEntry{LinkParams: lp(LinkOptions{Rate: "1mbit", Loss: "5%"})}))
	clock.set(1500 * time.Millisecond)
	if !m.outage(nil, dirUp) {
		t.Fatal("config schedule did not run")
	}
	m.replaceSchedule(nil, false, 0)
	st := m.status()
	if st.LinkParams.Up.Outage || st.LinkParams.Up.Rate != "10mbit" || st.LinkParams.Up.Loss != "0%" || st.Schedule.Entries != 0 {
		t.Fatalf("after an empty schedule: %+v", st)
	}
	clock.set(10 * time.Second)
	if m.outage(nil, dirUp) {
		t.Fatal("the old schedule still runs")
	}
	m.replaceSchedule(mustSchedule(t, ScheduleEntry{At: "1s", LinkParams: lp(LinkOptions{Rate: "2mbit"})}), false, 0)
	clock.set(10500 * time.Millisecond)
	if st = m.status(); st.LinkParams.Up.Rate != "10mbit" || st.Schedule.Elapsed != "500ms" || st.Schedule.NextAt != "1s" {
		t.Fatalf("new schedule before its entry: %+v", st)
	}
	clock.set(11 * time.Second)
	if st = m.status(); st.LinkParams.Up.Rate != "2mbit" || st.Schedule.Applied != 1 || st.Schedule.NextAt != "" {
		t.Fatalf("new schedule after its entry: %+v", st)
	}
}

// first_use waits for traffic: a unit of data on a connection opened
// before counts, not only a new connection.
func TestReplaceScheduleFirstUseByData(t *testing.T) {
	m, clock := newTestModel(t, Options{})
	m.replaceSchedule(mustSchedule(t, ScheduleEntry{At: "1s", LinkParams: lp(LinkOptions{Outage: boolp(true)})}), true, 0)
	clock.set(5 * time.Second)
	if st := m.status(); !st.Schedule.WaitingForTraffic || st.LinkParams.Up.Outage {
		t.Fatalf("before traffic: %+v", st.Schedule)
	}
	var st streamStates
	if _, wait, _ := m.admitStream(nil, dirDown, &st, 1000); wait != 0 {
		t.Fatal("first segment waited")
	}
	clock.set(5900 * time.Millisecond)
	if m.outage(nil, dirUp) {
		t.Fatal("outage before 1s after the first data")
	}
	clock.set(6 * time.Second)
	if !m.outage(nil, dirUp) {
		t.Fatal("no outage 1s after the first data")
	}
}

// A resetting outage first seen by a caller that ignores the reset (a
// stream's outage check) still reaches the event loop's tick.
func TestResetNotLostByOutageCheck(t *testing.T) {
	m, clock := newTestModel(t, Options{Schedule: []ScheduleEntry{{At: "1s", LinkParams: &LinkParams{ImpairLinkOptions: LinkOptions{Outage: boolp(true)}, Reset: true}}}})
	clock.set(1500 * time.Millisecond)
	if !m.outage(nil, dirDown) {
		t.Fatal("no outage")
	}
	if reset, _ := m.tick(); !reset {
		t.Fatal("the reset was lost")
	}
	if reset, _ := m.tick(); reset {
		t.Fatal("the reset was reported twice")
	}
}

// A new schedule restarts the generators from the seed: the same scenario
// over the same traffic draws the same losses.
func TestReplaceScheduleReseeds(t *testing.T) {
	m, _ := newTestModel(t, Options{LinkParams: lp(LinkOptions{Loss: "30%"})})
	run := func() []verdict {
		m.replaceSchedule(nil, false, 0)
		var got []verdict
		for range 50 {
			_, v, _ := m.admitPacket(nil, dirUp, 100)
			got = append(got, v)
		}
		return got
	}
	a, b := run(), run()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("draw %d differs: %v vs %v", i, a[i], b[i])
		}
	}
}

func lp(o LinkOptions) *LinkParams { return &LinkParams{ImpairLinkOptions: o} }
