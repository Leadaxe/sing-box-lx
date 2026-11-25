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
	cfg, err := resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{}
	return newModel(cfg, clock.now), clock
}

func TestTokenBucketRate(t *testing.T) {
	m, _ := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Rate: "8mbit"}})
	for i := 1; i <= 10; i++ {
		due, v, _ := m.admitPacket(dirUp, 1000) // 1000 B at 1 MB/s = 1 ms
		if v != verdictPass || due != ms(float64(i)) {
			t.Fatalf("packet %d: due %v verdict %d, want %v", i, time.Duration(due), v, time.Duration(ms(float64(i))))
		}
	}
	// the other direction has its own bottleneck
	if due, _, _ := m.admitPacket(dirDown, 1000); due != ms(1) {
		t.Fatalf("down due %v", time.Duration(due))
	}
}

func TestQueueTailDrop(t *testing.T) {
	for _, test := range []struct {
		queue string
		pass  int
	}{{"5ms", 6}, {"3000B", 4}, {"50ms", 20}} {
		m, _ := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Rate: "8mbit", Queue: test.queue}})
		pass := 0
		for range 20 {
			if _, v, _ := m.admitPacket(dirUp, 1000); v == verdictPass {
				pass++
			}
		}
		if pass != test.pass || m.stats(dirUp).DropQueue != uint64(20-test.pass) {
			t.Errorf("queue %s: %d passed, %+v; want %d", test.queue, pass, m.stats(dirUp), test.pass)
		}
	}
	// the queue drains with time
	m, clock := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Rate: "8mbit", Queue: "5ms"}})
	for range 20 {
		m.admitPacket(dirUp, 1000)
	}
	clock.set(6 * time.Millisecond)
	if _, v, _ := m.admitPacket(dirUp, 1000); v != verdictPass {
		t.Fatal("queue did not drain")
	}
}

func TestDelayJitterOrder(t *testing.T) {
	for _, reorder := range []bool{false, true} {
		m, clock := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Delay: "20ms", Jitter: "5ms", Reorder: boolp(reorder)}})
		var last int64
		inversions := 0
		sum := 0.0
		const n = 10000
		for i := range n {
			clock.set(time.Duration(i) * time.Millisecond)
			due, v, _ := m.admitPacket(dirUp, 100)
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
	m, _ := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Loss: "10%"}})
	const n = 100000
	for range n {
		m.admitPacket(dirUp, 100)
	}
	share := float64(m.stats(dirUp).DropLoss) / n
	if !near(share, 0.10, 0.005) {
		t.Fatalf("loss share %.4f", share)
	}
}

func TestLossGilbertElliott(t *testing.T) {
	m, _ := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Loss: "5%", LossBurst: floatp(4)}})
	const n = 400000
	lost, bursts, run := 0, 0, 0
	for range n {
		_, v, _ := m.admitPacket(dirUp, 100)
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
	opts := Options{ImpairLinkOptions: LinkOptions{Rate: "10mbit", Vary: &VaryOptions{Min: "3mbit", Max: "12mbit", Every: "2s"}}, Seed: seed(42)}
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
	c, _ := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Vary: &VaryOptions{Min: "3mbit", Max: "4mbit", Every: "1s"}}})
	if r := c.rate(dirUp); r < 3e6 || r > 4e6 {
		t.Fatalf("first draw %v", r)
	}
}

func TestSchedule(t *testing.T) {
	m, clock := newTestModel(t, Options{
		ImpairLinkOptions: LinkOptions{Rate: "8mbit"},
		Schedule: []ScheduleEntry{
			{At: "1s", ImpairLinkOptions: LinkOptions{Loss: "100%"}},
			{At: "2s", ImpairLinkOptions: LinkOptions{Loss: "0%", Rate: "16mbit"}},
			{At: "3s", Outage: boolp(true)},
			{At: "4s", Outage: boolp(false)},
			{At: "5s", Direction: "down", Outage: boolp(true), Reset: true},
		},
	})
	check := func(at time.Duration, dir int, want verdict) {
		t.Helper()
		clock.set(at)
		if _, v, _ := m.admitPacket(dir, 1000); v != want {
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
	if _, v, reset := m.admitPacket(dirUp, 1000); v != verdictPass || !reset {
		t.Fatalf("reset entry: verdict %d reset %v", v, reset)
	}
	check(5100*time.Millisecond, dirDown, verdictOutage)
	if resetting, _, _ := m.dialState(); !resetting {
		t.Fatal("dial allowed during a resetting outage")
	}
}

func TestScheduleFirstUse(t *testing.T) {
	m, clock := newTestModel(t, Options{Start: "first_use", Schedule: []ScheduleEntry{{At: "1s", Outage: boolp(true)}}})
	clock.set(10 * time.Second)
	if _, v, _ := m.admitPacket(dirUp, 100); v != verdictPass {
		t.Fatal("schedule ran before the first use")
	}
	m.firstUse()
	clock.set(10500 * time.Millisecond)
	if _, v, _ := m.admitPacket(dirUp, 100); v != verdictPass {
		t.Fatal("outage too early")
	}
	clock.set(11500 * time.Millisecond)
	if _, v, _ := m.admitPacket(dirUp, 100); v != verdictOutage {
		t.Fatal("no outage 1s after the first use")
	}
}

func TestRandomStalls(t *testing.T) {
	m, clock := newTestModel(t, Options{Stall: &StallOptions{Every: "1s", Duration: "100ms"}})
	down := 0
	const n = 200000
	for i := range n {
		clock.set(time.Duration(i) * time.Millisecond)
		if m.outage(dirUp) {
			down++
		}
	}
	share := float64(down) / n
	if !near(share, 0.1/1.1, 0.015) {
		t.Fatalf("outage share %.3f", share)
	}
}

func TestStreamBackpressureAndStall(t *testing.T) {
	m, clock := newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Rate: "8mbit", Queue: "10ms"}})
	var st streamState
	// 1 MB/s, segments of 1000 B: the 12th waits for the queue
	for i := 0; i < 11; i++ {
		if _, wait, _ := m.admitStream(dirUp, &st, 1000); wait != 0 {
			t.Fatalf("segment %d waits %v", i, wait)
		}
	}
	_, wait, _ := m.admitStream(dirUp, &st, 1000)
	if wait != time.Millisecond {
		t.Fatalf("wait %v, want 1ms", wait)
	}
	clock.add(wait)
	if _, wait, _ = m.admitStream(dirUp, &st, 1000); wait != 0 {
		t.Fatal("still waiting after the backlog drained")
	}

	// loss as stall: 1 % per MSS, stall 300ms
	m, clock = newTestModel(t, Options{ImpairLinkOptions: LinkOptions{Loss: "1%", StreamStall: "300ms"}})
	st = streamState{}
	const segments = 100000
	var last int64
	for i := range segments {
		clock.set(time.Duration(i) * time.Microsecond)
		due, _, _ := m.admitStream(dirDown, &st, mss)
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
	m, clock := newTestModel(t, Options{Schedule: []ScheduleEntry{{At: "1s", Outage: boolp(true)}, {At: "2s", Outage: boolp(false)}}})
	var st streamState
	clock.set(1500 * time.Millisecond)
	if _, wait, _ := m.admitStream(dirUp, &st, 100); wait == 0 {
		t.Fatal("stream admitted during an outage")
	}
	if resetting, wait, _ := m.dialState(); resetting || wait == 0 {
		t.Fatal("dial not held during a silent outage")
	}
	clock.set(2500 * time.Millisecond)
	if _, wait, _ := m.admitStream(dirUp, &st, 100); wait != 0 {
		t.Fatal("stream held after the outage")
	}
}

func TestProfiles(t *testing.T) {
	for name := range profiles {
		cfg, err := resolve(Options{Profile: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.dirs[dirUp].rate == 0 && cfg.dirs[dirUp].vary == nil {
			t.Fatalf("%s: no rate", name)
		}
	}
	// an explicit rate replaces the profile's variation
	cfg, err := resolve(Options{Profile: "lte-walk", ImpairLinkOptions: LinkOptions{Rate: "5mbit"}})
	if err != nil || cfg.dirs[dirDown].vary != nil || cfg.dirs[dirDown].rate != 5e6 || cfg.dirs[dirDown].delay != 35*time.Millisecond {
		t.Fatalf("override: %+v %v", cfg.dirs[dirDown], err)
	}
}

func TestResolveErrors(t *testing.T) {
	for _, test := range []struct {
		options Options
		want    string
	}{
		{Options{ImpairLinkOptions: LinkOptions{Rate: "10mbps"}}, "rate"},
		{Options{ImpairLinkOptions: LinkOptions{Rate: "1bit"}}, "8kbit..10gbit"},
		{Options{Up: &LinkOptions{Rate: "x"}}, "up.rate"},
		{Options{ImpairLinkOptions: LinkOptions{Queue: "10"}}, "queue"},
		{Options{ImpairLinkOptions: LinkOptions{Queue: "100B"}}, "1500B..64MB"},
		{Options{ImpairLinkOptions: LinkOptions{Delay: "20"}}, "delay"},
		{Options{ImpairLinkOptions: LinkOptions{Delay: "20s"}}, "delay"},
		{Options{ImpairLinkOptions: LinkOptions{Loss: "0.5"}}, "percentage"},
		{Options{ImpairLinkOptions: LinkOptions{Loss: "101%"}}, "loss"},
		{Options{ImpairLinkOptions: LinkOptions{LossBurst: floatp(0.5)}}, "loss_burst"},
		{Options{ImpairLinkOptions: LinkOptions{Vary: &VaryOptions{Min: "5mbit", Max: "3mbit", Every: "1s"}}}, "above max"},
		{Options{ImpairLinkOptions: LinkOptions{Vary: &VaryOptions{Min: "5mbit", Max: "6mbit"}}}, "vary: min, max and every"},
		{Options{ImpairLinkOptions: LinkOptions{Vary: &VaryOptions{Min: "unlimited", Max: "6mbit", Every: "1s"}}}, "not \"unlimited\""},
		{Options{Down: &LinkOptions{Vary: &VaryOptions{Min: "1mbit", Max: "6mbit", Every: "1ms"}}}, "down.vary.every"},
		{Options{Stall: &StallOptions{Every: "1s"}}, "stall"},
		{Options{Start: "later"}, "start"},
		{Options{Profile: "5g"}, "unknown profile"},
		{Options{Schedule: []ScheduleEntry{{Outage: boolp(true)}}}, "schedule[0].at is required"},
		{Options{Schedule: []ScheduleEntry{{At: "2s", Outage: boolp(true)}, {At: "1s", Outage: boolp(false)}}}, "time order"},
		{Options{Schedule: []ScheduleEntry{{At: "1s"}}}, "changes nothing"},
		{Options{Schedule: []ScheduleEntry{{At: "1s", Reset: true}}}, "reset needs"},
		{Options{Schedule: []ScheduleEntry{{At: "1s", Direction: "both", Outage: boolp(true)}}}, "direction"},
		{Options{Schedule: []ScheduleEntry{{At: "1s", ImpairLinkOptions: LinkOptions{Rate: "fast"}}}}, "schedule[0].rate"},
	} {
		_, err := resolve(test.options)
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
		{At: "1s", Outage: boolp(true), Reset: true},
		{At: "2s", Outage: boolp(false), Direction: "up", ImpairLinkOptions: LinkOptions{Rate: "3mbit"}},
	}})
	clock.set(1500 * time.Millisecond)
	reset, logs := m.tick()
	if !reset || len(logs) != 1 || logs[0].debug || logs[0].text != "schedule at 1s: up outage begins, connections reset; down outage begins, connections reset" {
		t.Fatalf("at 1.5s: reset %v, logs %+v", reset, logs)
	}
	if at, ok := m.nextWake(); !ok || at != int64(2*time.Second) {
		t.Fatalf("next wake %v %v, want 2s", at, ok)
	}
	clock.set(2 * time.Second)
	if _, logs = m.tick(); len(logs) != 1 || logs[0].text != "schedule at 2s: up rate 3mbit delay 0s outage ends" {
		t.Fatalf("at 2s: %+v", logs)
	}
	if _, ok := m.nextWake(); ok {
		t.Fatal("a wake after the last entry")
	}
}

func TestStallLogs(t *testing.T) {
	m, clock := newTestModel(t, Options{Stall: &StallOptions{Every: "10s", Duration: "1s"}})
	start, _ := m.nextWake()
	clock.set(time.Duration(start))
	if _, logs := m.tick(); len(logs) != 1 || !logs[0].debug || logs[0].text != "stall of the whole link for 1s" {
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
