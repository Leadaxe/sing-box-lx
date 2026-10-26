//go:build with_lx_command

package lxtrace

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

func attrOf(snapshot StageSnapshot, key string) (string, bool) {
	for _, attr := range snapshot.Attrs {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	return "", false
}

func stageByID(summary Summary, id uint32) StageSnapshot {
	for _, s := range summary.Stages {
		if s.ID == id {
			return s
		}
	}
	return StageSnapshot{}
}

func TestNoTraceIsNoop(t *testing.T) {
	ctx := context.Background()
	childCtx, s := Begin(ctx, "dial", "x")
	if s != nil || childCtx != ctx {
		t.Fatal("Begin without a trace must return (ctx, nil)")
	}
	lookupCtx, ls, err := BeginLookup(ctx, "r", "d")
	if ls != nil || err != nil || lookupCtx != ctx {
		t.Fatal("BeginLookup without a trace must return (ctx, nil, nil)")
	}
	if From(ctx) != nil || Current(ctx) != nil || Fresh(ctx) || IsProbe(ctx) || DepthOf(ctx) != DepthHTTP {
		t.Fatal("accessors without a trace must be zero")
	}
	if Routed(ctx, &adapter.InboundContext{}, nil, nil) {
		t.Fatal("Routed without a trace must return false")
	}
	// nil-safe methods
	var nilStage *Stage
	nilStage.Attr("k", "v")
	nilStage.End(nil)
	nilStage.End(errors.New("x"))
	nilStage.Mark(Reused)
	nilStage.LostRace(nil)
	if nilStage.ID() != 0 {
		t.Fatal("nil stage id must be 0")
	}
}

func TestNestingFollowsContext(t *testing.T) {
	trace := New(Options{Fresh: true, Probe: true, Depth: DepthConnect})
	ctx := With(context.Background(), trace)
	if !Fresh(ctx) || !IsProbe(ctx) || DepthOf(ctx) != DepthConnect || From(ctx) != trace {
		t.Fatal("accessors must read the trace options")
	}
	ctx1, s1 := Begin(ctx, "dial", "node")
	ctx2, s2 := Begin(ctx1, "detour", "awg")
	_, s3 := Begin(ctx2, "lookup", "example.net")
	_, s1b := Begin(ctx1, "tcp_connect", "1.2.3.4:443")
	if Current(ctx2) != s2 || Current(ctx1) != s1 || Current(ctx) != nil {
		t.Fatal("Current must return the innermost stage of each ctx")
	}
	s3.End(nil)
	s2.End(nil)
	s1b.End(nil)
	s1.End(nil)
	summary := trace.Finish(nil)
	if len(summary.Stages) != 4 {
		t.Fatalf("expected 4 stages, got %d", len(summary.Stages))
	}
	if stageByID(summary, 1).Parent != 0 || stageByID(summary, 2).Parent != 1 || stageByID(summary, 3).Parent != 2 || stageByID(summary, 4).Parent != 1 {
		t.Fatalf("wrong parents: %+v", summary.Stages)
	}
	for _, s := range summary.Stages {
		if s.Result != OK {
			t.Fatalf("stage %d should be OK, got %v", s.ID, s.Result)
		}
	}
	if summary.RootCause != 0 || summary.Truncated {
		t.Fatalf("clean trace: root %d truncated %v", summary.RootCause, summary.Truncated)
	}
}

func TestStageCapTruncates(t *testing.T) {
	trace := New(Options{MaxStages: 3})
	ctx := With(context.Background(), trace)
	var stages []*Stage
	for i := 0; i < 5; i++ {
		_, s := Begin(ctx, "dial", strconv.Itoa(i))
		stages = append(stages, s)
	}
	if stages[0] == nil || stages[1] == nil || stages[2] == nil {
		t.Fatal("stages under the cap must be recorded")
	}
	if stages[3] != nil || stages[4] != nil {
		t.Fatal("stages past the cap must be nil")
	}
	summary := trace.Finish(nil)
	if !summary.Truncated || len(summary.Stages) != 3 {
		t.Fatalf("truncated=%v stages=%d", summary.Truncated, len(summary.Stages))
	}
}

func TestEmitOrderingAndSameID(t *testing.T) {
	var (
		access    sync.Mutex
		snapshots []StageSnapshot
	)
	trace := New(Options{Emit: func(s StageSnapshot) {
		access.Lock()
		snapshots = append(snapshots, s)
		access.Unlock()
	}})
	ctx := With(context.Background(), trace)
	_, s := Begin(ctx, "tls", "example.com")
	s.Attr("sni", "example.com")
	s.End(nil)
	trace.Finish(nil)
	access.Lock()
	defer access.Unlock()
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 emits, got %d", len(snapshots))
	}
	if snapshots[0].ID != 1 || snapshots[0].Result != Running || snapshots[0].DurationMs != 0 {
		t.Fatalf("first emit must be RUNNING with the stage id: %+v", snapshots[0])
	}
	if snapshots[1].ID != 1 || snapshots[1].Result != OK {
		t.Fatalf("second emit must be the end with the same id: %+v", snapshots[1])
	}
	if sni, ok := attrOf(snapshots[1], "sni"); !ok || sni != "example.com" {
		t.Fatalf("end snapshot must carry attrs: %+v", snapshots[1])
	}
	if len(snapshots[0].Attrs) != 0 {
		t.Fatal("the RUNNING snapshot was taken before the attr and must not share the slice")
	}
}

func TestEmitOverflowDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	var received int
	trace := New(Options{MaxStages: 10000, Emit: func(StageSnapshot) {
		<-release // the consumer is stuck: the queue must fill and drop, never block
		received++
	}})
	ctx := With(context.Background(), trace)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < emitQueueSize*3; i++ {
			_, s := Begin(ctx, "dial", "x")
			s.End(nil)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Begin/End blocked on a full emit queue")
	}
	close(release)
	summary := trace.Finish(nil)
	if len(summary.Stages) != emitQueueSize*3 {
		t.Fatalf("every stage must still be in the summary: %d", len(summary.Stages))
	}
	if received == 0 || received > emitQueueSize+1 {
		t.Fatalf("unexpected number of delivered emits: %d", received)
	}
}

func TestBeginLookupDetectsCycle(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	ctx1, l1, err := BeginLookup(ctx, "dns-remote", "nl.example.net")
	if err != nil || l1 == nil {
		t.Fatal("first lookup must open")
	}
	ctx2, d, _ := BeginLookup(ctx1, "dns-boot", "dns.example.org") // a different pair nests fine
	if d == nil {
		t.Fatal("different pair must open")
	}
	ctx3, l2, err := BeginLookup(ctx2, "dns-remote", "nl.example.net")
	if !errors.Is(err, ErrResolveCycle) {
		t.Fatalf("expected ErrResolveCycle, got %v", err)
	}
	if l2 == nil {
		t.Fatal("the cycle stage must still be recorded")
	}
	if ctx3 != ctx2 {
		t.Fatal("a cycle must not return a child ctx")
	}
	d.End(nil)
	l1.End(err)
	summary := trace.Finish(nil)
	cycle := stageByID(summary, l2.ID())
	if cycle.Result != Failed || cycle.Error != ErrResolveCycle.Error() {
		t.Fatalf("cycle stage: %+v", cycle)
	}
	if with, ok := attrOf(cycle, "cycle_with"); !ok || with != strconv.Itoa(int(l1.ID())) {
		t.Fatalf("cycle_with must point at the ancestor: %+v", cycle)
	}
	if resolver, _ := attrOf(cycle, "resolver"); resolver != "dns-remote" {
		t.Fatalf("resolver attr: %+v", cycle)
	}
	if summary.RootCause != l2.ID() {
		t.Fatalf("root must be the cycle stage %d, got %d", l2.ID(), summary.RootCause)
	}
}

func TestRootCauseNestedFailure(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	ctx1, dial := Begin(ctx, "dial", "vless-nl")
	ctx2, detour := Begin(ctx1, "detour", "awg-de")
	ctx3, lookup := Begin(ctx2, "lookup", "nl.example.net")
	_, tcp := Begin(ctx3, "tcp_connect", "203.0.113.50:8388")
	tcp.End(errors.New("connection refused"))
	lookup.End(errors.New("lookup failed"))
	detour.End(errors.New("detour failed"))
	dial.End(errors.New("dial failed"))
	_, http := Begin(ctx, "http", "")
	http.Mark(Skipped)
	summary := trace.Finish(nil)
	if summary.RootCause != tcp.ID() {
		t.Fatalf("root must be the deepest failure %d, got %d", tcp.ID(), summary.RootCause)
	}
}

func TestRootCauseEarliestAmongIndependentFailures(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	_, a := Begin(ctx, "app_dns", "A")
	_, b := Begin(ctx, "app_dns", "AAAA")
	b.End(errors.New("first to fail"))
	time.Sleep(2 * time.Millisecond)
	a.End(errors.New("second to fail"))
	summary := trace.Finish(nil)
	if summary.RootCause != b.ID() {
		t.Fatalf("root must be the failure that finished first (%d), got %d", b.ID(), summary.RootCause)
	}
}

func TestLostRaceExcludedFromRoot(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	ctx1, dial := Begin(ctx, "dial", "node")
	_, loser := Begin(ctx1, "tcp_connect", "[2001:db8::1]:443")
	_, winner := Begin(ctx1, "tcp_connect", "192.0.2.1:443")
	loser.LostRace(winner)
	winner.End(nil)
	_, tls := Begin(ctx1, "tls", "example.com")
	tls.End(errors.New("handshake failed"))
	dial.End(errors.New("dial failed"))
	summary := trace.Finish(nil)
	if summary.RootCause != tls.ID() {
		t.Fatalf("root must be the tls failure %d, got %d", tls.ID(), summary.RootCause)
	}
	lost := stageByID(summary, loser.ID())
	if lost.Result != Skipped {
		t.Fatalf("loser must be Skipped: %+v", lost)
	}
	if id, ok := attrOf(lost, "lost_race"); !ok || id != strconv.Itoa(int(winner.ID())) {
		t.Fatalf("lost_race must name the winner: %+v", lost)
	}
	// a trace whose only "failure" is a lost race has no root cause
	trace2 := New(Options{})
	ctx = With(context.Background(), trace2)
	_, l := Begin(ctx, "tcp_connect", "a")
	_, w := Begin(ctx, "tcp_connect", "b")
	l.LostRace(w)
	w.End(nil)
	if root := trace2.Finish(nil).RootCause; root != 0 {
		t.Fatalf("lost race alone must not be a root cause, got %d", root)
	}
}

func TestFinishClosesRunningStages(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	ctx1, dial := Begin(ctx, "dial", "node")
	_, tcp := Begin(ctx1, "tcp_connect", "x")
	summary := trace.Finish(errors.New("timeout"))
	for _, id := range []uint32{dial.ID(), tcp.ID()} {
		s := stageByID(summary, id)
		if s.Result != Failed || s.Error != "timeout" {
			t.Fatalf("running stage %d must end Failed/timeout: %+v", id, s)
		}
	}
	if summary.RootCause != tcp.ID() {
		t.Fatalf("root after timeout must be the deepest unfinished stage %d, got %d", tcp.ID(), summary.RootCause)
	}
	// after Finish: Begin is a no-op, End on old stages is ignored, Finish is idempotent
	if _, s := Begin(ctx, "dial", "late"); s != nil {
		t.Fatal("Begin after Finish must return nil")
	}
	tcp.End(nil)
	again := trace.Finish(nil)
	if len(again.Stages) != 2 || again.RootCause != tcp.ID() || stageByID(again, tcp.ID()).Result != Failed {
		t.Fatalf("Finish must be idempotent: %+v", again)
	}
	// nil cause → "canceled"
	trace3 := New(Options{})
	_, s := Begin(With(context.Background(), trace3), "dial", "x")
	if got := stageByID(trace3.Finish(nil), s.ID()).Error; got != "canceled" {
		t.Fatalf("nil cause must read canceled, got %q", got)
	}
}

func TestMarkAndEndAreIdempotent(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	_, s := Begin(ctx, "dns", "dns-remote")
	s.Mark(Reused)
	s.End(errors.New("late error"))
	s.Mark(NoAck)
	snapshot := stageByID(trace.Finish(nil), s.ID())
	if snapshot.Result != Reused || snapshot.Error != "" {
		t.Fatalf("first close must win: %+v", snapshot)
	}
}

// --- Routed -----------------------------------------------------------------

type testRule struct {
	text   string
	action testAction
}

func (r *testRule) Match(*adapter.InboundContext) bool { return true }
func (r *testRule) String() string                     { return r.text }
func (r *testRule) Start() error                       { return nil }
func (r *testRule) Close() error                       { return nil }
func (r *testRule) Type() string                       { return C.RuleTypeDefault }
func (r *testRule) Action() adapter.RuleAction         { return r.action }

type testAction string

func (a testAction) Type() string   { return string(a) }
func (a testAction) String() string { return string(a) }

type testOutbound struct {
	tag, typ string
	now      string
}

func (o *testOutbound) Type() string           { return o.typ }
func (o *testOutbound) Tag() string            { return o.tag }
func (o *testOutbound) Network() []string      { return []string{"tcp"} }
func (o *testOutbound) Dependencies() []string { return nil }
func (o *testOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("not dialable")
}

func (o *testOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("not dialable")
}
func (o *testOutbound) Now() string   { return o.now }
func (o *testOutbound) All() []string { return []string{o.now} }

type testOutboundManager struct {
	outbounds map[string]adapter.Outbound
}

func (m *testOutboundManager) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (m *testOutboundManager) Outbounds() []adapter.Outbound                  { return nil }
func (m *testOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	o, ok := m.outbounds[tag]
	return o, ok
}
func (m *testOutboundManager) Default() adapter.Outbound { return nil }
func (m *testOutboundManager) Create(context.Context, adapter.Router, log.ContextLogger, string, string, any) error {
	return nil
}

func TestRoutedAttrsAndDomainOrigin(t *testing.T) {
	cases := []struct {
		name     string
		metadata adapter.InboundContext
		origin   string
	}{
		{"fakeip", adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("www.youtube.com", 443), FakeIP: true, Domain: "www.youtube.com", Protocol: "tls"}, "fakeip"},
		{"sniff", adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("1.2.3.4", 443), Domain: "example.com", Protocol: "tls"}, "sniff"},
		{"reverse_mapping", adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("1.2.3.4", 443), Domain: "example.com"}, "reverse_mapping"},
		{"request", adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("example.com", 443)}, "request"},
		{"none", adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("1.2.3.4", 443)}, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := New(Options{})
			ctx := With(context.Background(), trace)
			if Routed(ctx, &tc.metadata, nil, nil) {
				t.Fatal("DepthHTTP must not stop")
			}
			summary := trace.Finish(nil)
			route := stageByID(summary, 1)
			if route.Kind != "route" || route.Result != OK {
				t.Fatalf("route stage: %+v", route)
			}
			if origin, _ := attrOf(route, "domain_origin"); origin != tc.origin {
				t.Fatalf("domain_origin=%q want %q", origin, tc.origin)
			}
			if rule, _ := attrOf(route, "rule"); rule != "final" {
				t.Fatalf("nil rule must read final: %+v", route)
			}
		})
	}
}

func TestRoutedFullAttrsAndSelectorUnfold(t *testing.T) {
	leaf := &testOutbound{tag: "vless-nl", typ: C.TypeVLESS}
	inner := &testOutbound{tag: "eu", typ: C.TypeSelector, now: "vless-nl"}
	outer := &testOutbound{tag: "proxy", typ: C.TypeSelector, now: "eu"}
	manager := &testOutboundManager{outbounds: map[string]adapter.Outbound{"vless-nl": leaf, "eu": inner, "proxy": outer}}
	trace := New(Options{Depth: DepthRoute})
	ctx := service.ContextWith[adapter.OutboundManager](With(context.Background(), trace), manager)
	metadata := &adapter.InboundContext{
		Inbound:              "tun-in",
		Network:              "tcp",
		Source:               M.SocksaddrFrom(netip.MustParseAddr("192.168.1.10"), 51000),
		Destination:          M.ParseSocksaddrHostPort("www.youtube.com", 443),
		OriginDestination:    M.ParseSocksaddrHostPort("198.18.0.42", 443),
		FakeIP:               true,
		Protocol:             "tls",
		Domain:               "www.youtube.com",
		Client:               "chrome",
		DestinationAddresses: []netip.Addr{netip.MustParseAddr("142.250.1.1"), netip.MustParseAddr("142.250.1.2")},
		ProcessInfo:          &adapter.ConnectionOwner{UserId: 10123, AndroidPackageNames: []string{"com.android.chrome"}},
	}
	rule := &testRule{text: "domain_suffix=youtube.com => route(proxy)", action: testAction(C.RuleActionTypeRoute)}
	if !Routed(ctx, metadata, rule, outer) {
		t.Fatal("DepthRoute must stop after the route stage")
	}
	summary := trace.Finish(nil)
	if len(summary.Stages) != 3 {
		t.Fatalf("expected route + 2 group stages, got %+v", summary.Stages)
	}
	route := summary.Stages[0]
	want := map[string]string{
		"network":              "tcp",
		"rule":                 rule.text,
		"action":               C.RuleActionTypeRoute,
		"outbound":             "proxy",
		"outbound_type":        C.TypeSelector,
		"domain_origin":        "fakeip",
		"protocol":             "tls",
		"domain":               "www.youtube.com",
		"client":               "chrome",
		"original_destination": "198.18.0.42:443",
		"addresses":            "142.250.1.1,142.250.1.2",
		"inbound":              "tun-in",
		"source":               "192.168.1.10:51000",
		"process":              "com.android.chrome",
		"user_id":              "10123",
	}
	for key, value := range want {
		if got, ok := attrOf(route, key); !ok || got != value {
			t.Errorf("attr %s=%q want %q", key, got, value)
		}
	}
	// attr order: the first keys follow the documented order
	order := []string{"network", "rule", "action", "outbound", "outbound_type", "domain_origin"}
	for i, key := range order {
		if route.Attrs[i].Key != key {
			t.Fatalf("attr %d is %s, want %s", i, route.Attrs[i].Key, key)
		}
	}
	g1, g2 := summary.Stages[1], summary.Stages[2]
	if g1.Kind != "group" || g1.Subject != "proxy" || g2.Kind != "group" || g2.Subject != "eu" {
		t.Fatalf("selector unfold: %+v %+v", g1, g2)
	}
	if sel, _ := attrOf(g1, "selected"); sel != "eu" {
		t.Fatalf("outer selected: %+v", g1)
	}
	if sel, _ := attrOf(g2, "selected"); sel != "vless-nl" {
		t.Fatalf("inner selected: %+v", g2)
	}
	if g1.Parent != 0 || g2.Parent != 0 {
		t.Fatalf("group stages are top-level like route: %+v %+v", g1, g2)
	}
}

func TestRoutedStopsForDepthDNSOnly(t *testing.T) {
	for _, depth := range []Depth{DepthDNS, DepthRoute} {
		trace := New(Options{Depth: depth})
		if !Routed(With(context.Background(), trace), &adapter.InboundContext{}, nil, nil) {
			t.Fatalf("depth %d must stop", depth)
		}
	}
	for _, depth := range []Depth{DepthHTTP, DepthConnect} {
		trace := New(Options{Depth: depth})
		if Routed(With(context.Background(), trace), &adapter.InboundContext{}, nil, nil) {
			t.Fatalf("depth %d must not stop", depth)
		}
	}
}

func TestConcurrentBeginEnd(t *testing.T) {
	var emitted int64
	var access sync.Mutex
	trace := New(Options{MaxStages: 100000, Emit: func(StageSnapshot) {
		access.Lock()
		emitted++
		access.Unlock()
	}})
	ctx := With(context.Background(), trace)
	ctx, parent := Begin(ctx, "dial", "root")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			childCtx, s := Begin(ctx, "tcp_connect", strconv.Itoa(i))
			s.Attr("i", strconv.Itoa(i))
			for j := 0; j < 20; j++ {
				_, inner := Begin(childCtx, "tls", "")
				if j%2 == 0 {
					inner.End(nil)
				} else {
					inner.Mark(Reused)
				}
			}
			if i%7 == 0 {
				s.End(errors.New("boom"))
			} else {
				s.End(nil)
			}
		}(i)
	}
	wg.Wait()
	parent.End(nil)
	summary := trace.Finish(nil)
	if len(summary.Stages) != 1+50+50*20 {
		t.Fatalf("stages: %d", len(summary.Stages))
	}
	seen := make(map[uint32]bool)
	for _, s := range summary.Stages {
		if seen[s.ID] {
			t.Fatalf("duplicate id %d", s.ID)
		}
		seen[s.ID] = true
		if s.Result == Running {
			t.Fatalf("stage %d still running", s.ID)
		}
	}
	if summary.RootCause == 0 {
		t.Fatal("a boom child must be the root cause")
	}
}

func TestWithStageAndStageLookup(t *testing.T) {
	trace := New(Options{})
	ctx := With(context.Background(), trace)
	_, parent := Begin(ctx, "dial", "node")
	parent.End(nil)
	if trace.Stage(parent.ID()) != parent {
		t.Fatalf("Stage(%d) must return the recorded stage", parent.ID())
	}
	if trace.Stage(0) != nil || trace.Stage(99) != nil {
		t.Fatal("unknown ids must return nil")
	}
	_, child := Begin(WithStage(ctx, parent), "proxy_header", "target")
	child.Mark(NoAck)
	summary := trace.Finish(nil)
	if summary.Stages[1].Parent != parent.ID() {
		t.Fatalf("child parent = %d, want %d", summary.Stages[1].Parent, parent.ID())
	}
	if WithStage(ctx, nil) != ctx {
		t.Fatal("WithStage(nil) must return ctx unchanged")
	}
}
