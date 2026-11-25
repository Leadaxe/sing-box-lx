// lx:begin SPEC 054 penalty failover tests

package group

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// penaltyDialNode — outbound с управляемым исходом дайла: err != nil → ошибка,
// иначе соединение к addr.
type penaltyDialNode struct {
	adapter.Outbound
	tag   string
	addr  string
	err   error
	dials int
	typ   string // "" → direct
}

func (n *penaltyDialNode) Type() string {
	if n.typ != "" {
		return n.typ
	}
	return C.TypeDirect
}
func (n *penaltyDialNode) Tag() string            { return n.tag }
func (n *penaltyDialNode) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (n *penaltyDialNode) Dependencies() []string { return nil }
func (n *penaltyDialNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	n.dials++
	if n.err != nil {
		return nil, n.err
	}
	return net.Dial("tcp", n.addr)
}

func newPenaltyTestGroup(t *testing.T, nodes []*penaltyDialNode, delays map[string]uint16) *URLTestGroup {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	byTag := make(map[string]adapter.Outbound, len(nodes))
	outbounds := make([]adapter.Outbound, 0, len(nodes))
	for _, node := range nodes {
		if node.addr == "server" {
			node.addr = server.Listener.Addr().String()
		}
		byTag[node.tag] = node
		outbounds = append(outbounds, node)
	}
	g := &URLTestGroup{
		ctx:            context.Background(),
		outbound:       &fakeManager{byTag: byTag},
		logger:         logger.NOP(),
		outbounds:      outbounds,
		link:           server.URL,
		interval:       time.Minute,
		history:        urltest.NewHistoryStorage(),
		interruptGroup: interrupt.NewGroup(),
	}
	for tag, delay := range delays {
		g.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
	}
	return g
}

// TestIsPathDeadDialError pins the error classification: timeouts and
// unreachable count for any node; refused and reset count only for a node that
// does not go to the destination itself on open; caller cancellation never does.
func TestIsPathDeadDialError(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		proxy bool // want for a proxy leaf
		site  bool // want for a direct or endpoint leaf
	}{
		{"ctx deadline (SPEC 052)", context.DeadlineExceeded, true, true},
		{"wrapped ctx deadline", E.Cause(context.DeadlineExceeded, "dial"), true, true},
		{"os deadline", &net.OpError{Op: "connect", Err: syscall.ETIMEDOUT}, true, true},
		{"host unreachable", syscall.EHOSTUNREACH, true, true},
		{"refused", syscall.ECONNREFUSED, true, false},
		{"wrapped refused", E.Cause(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "dial server"), true, false},
		{"reset on open", syscall.ECONNRESET, true, false},
		{"canceled (caller)", context.Canceled, false, false},
		{"plain error", E.New("dead node"), false, false},
		{"nil", nil, false, false},
	}
	for _, c := range cases {
		if got := isPathDeadDialError(c.err, false); got != c.proxy {
			t.Fatalf("%s via proxy: got %v, want %v", c.name, got, c.proxy)
		}
		if got := isPathDeadDialError(c.err, true); got != c.site {
			t.Fatalf("%s via direct/endpoint: got %v, want %v", c.name, got, c.site)
		}
	}
}

// fakeEndpoints is an EndpointManager that knows the listed tags.
type fakeEndpoints struct {
	adapter.EndpointManager
	tags map[string]bool
}

func (m *fakeEndpoints) Get(tag string) (adapter.Endpoint, bool) {
	return nil, m.tags[tag]
}

// TestPenaltySiteOnOpen: direct and endpoint leaves go to the destination
// themselves on open; a proxy leaf does not; a group is resolved to its leaf.
func TestPenaltySiteOnOpen(t *testing.T) {
	direct := &penaltyDialNode{tag: "direct"}
	proxy := &penaltyDialNode{tag: "proxy", typ: C.TypeVLESS}
	wg := &penaltyDialNode{tag: "wg", typ: C.TypeWireGuard}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{direct, proxy, wg}, nil)
	g.ctx = service.ContextWith[adapter.EndpointManager](context.Background(), &fakeEndpoints{tags: map[string]bool{"wg": true}})
	for _, c := range []struct {
		node adapter.Outbound
		want bool
	}{
		{direct, true},
		{proxy, false},
		{wg, true},
		{&penaltyGroupNode{penaltyDialNode: penaltyDialNode{tag: "nested", typ: C.TypeURLTest}, selected: proxy}, false},
		{&penaltyGroupNode{penaltyDialNode: penaltyDialNode{tag: "nested", typ: C.TypeURLTest}, selected: direct}, true},
	} {
		if got := penaltySiteOnOpen(g.ctx, g.outbound, c.node); got != c.want {
			t.Fatalf("%s: got %v, want %v", c.node.Tag(), got, c.want)
		}
	}
}

// penaltyGroupNode is a nested group that has selected selected.
type penaltyGroupNode struct {
	penaltyDialNode
	selected adapter.Outbound
}

func (n *penaltyGroupNode) All() []string                                     { return []string{n.selected.Tag()} }
func (n *penaltyGroupNode) Selected(network string) adapter.Outbound          { return n.selected }
func (n *penaltyGroupNode) AttachConnection(closer io.Closer) (detach func()) { return func() {} }

// TestPenaltyEmergencyRanking: аварийный режим включается порогом у
// лучшего-по-скорости и ранжирует двумя уровнями — штрафы ↑, затем задержка ↑
// (победители по штрафам соревнуются по скорости).
func TestPenaltyEmergencyRanking(t *testing.T) {
	a := &penaltyDialNode{tag: "a"}
	b := &penaltyDialNode{tag: "b"}
	c := &penaltyDialNode{tag: "c"}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{a, b, c}, map[string]uint16{"a": 10, "b": 50, "c": 100})

	if g.penaltyEmergency(N.NetworkTCP) {
		t.Fatal("no penalties → no emergency")
	}
	g.penaltyAdd("a")
	g.penaltyAdd("a")
	if g.penaltyEmergency(N.NetworkTCP) {
		t.Fatal("fastest below threshold → no emergency")
	}
	g.penaltyAdd("a") // a=3 → аварийный режим
	if !g.penaltyEmergency(N.NetworkTCP) {
		t.Fatal("fastest at threshold → emergency")
	}
	// b и c без штрафов — победители по штрафам; из них выбирается быстрый (b).
	best, _ := g.selectPenaltyAware(N.NetworkTCP)
	if best.Tag() != "b" {
		t.Fatalf("two-level ranking must pick b (0 penalties, fastest of them), got %s", best.Tag())
	}
	g.penaltyAdd("b") // b=1, c=0 → побеждает c несмотря на большую задержку
	best, _ = g.selectPenaltyAware(N.NetworkTCP)
	if best.Tag() != "c" {
		t.Fatalf("fewer penalties must beat delay, got %s", best.Tag())
	}
	// Успех b (доказательство жизни) сбрасывает его штраф → снова b.
	g.penaltyReset("b")
	best, _ = g.selectPenaltyAware(N.NetworkTCP)
	if best.Tag() != "b" {
		t.Fatalf("after reset b must win again, got %s", best.Tag())
	}
}

// TestPenaltyFailoverDial: «путь мёртв» → штраф отказавшему, fallback-дайл через
// следующего кандидата, успех переносит выбор группы на fallback без Interrupt.
func TestPenaltyFailoverDial(t *testing.T) {
	dead := &penaltyDialNode{tag: "dead", err: context.DeadlineExceeded}
	alive := &penaltyDialNode{tag: "alive", addr: "server"}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{dead, alive}, map[string]uint16{"dead": 10, "alive": 50})
	g.selectedOutboundTCP.Store(dead)

	conn, fallback, ok := g.penaltyFailoverDial(context.Background(), N.NetworkTCP, M.Socksaddr{}, dead, context.DeadlineExceeded)
	if !ok {
		t.Fatal("failover must succeed via the alive node")
	}
	defer conn.Close()
	if fallback.Tag() != "alive" {
		t.Fatalf("fallback must be the alive node, got %s", fallback.Tag())
	}
	if g.penaltyOf("dead") != 1 {
		t.Fatalf("failed node must get +1 penalty, got %d", g.penaltyOf("dead"))
	}
	if g.penaltyOf("alive") != 0 {
		t.Fatalf("successful fallback must hold 0 penalties, got %d", g.penaltyOf("alive"))
	}
	if g.selectedOutboundTCP.Load() != adapter.Outbound(alive) {
		t.Fatal("selection must move to the fallback")
	}
	if alive.dials != 1 {
		t.Fatalf("exactly one fallback attempt (cap 2 total), got %d", alive.dials)
	}
}

// TestPenaltyFailoverDial_notForDestinationErrors: refused/reset at a direct or
// endpoint leaf is the site's answer — no penalty, no fallback (через другой узел
// тот же отказ).
func TestPenaltyFailoverDial_notForDestinationErrors(t *testing.T) {
	for _, dialErr := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET} {
		for _, typ := range []string{C.TypeDirect, C.TypeWireGuard} {
			dead := &penaltyDialNode{tag: "dead", typ: typ, err: dialErr}
			alive := &penaltyDialNode{tag: "alive", addr: "server"}
			g := newPenaltyTestGroup(t, []*penaltyDialNode{dead, alive}, map[string]uint16{"dead": 10, "alive": 50})
			g.ctx = service.ContextWith[adapter.EndpointManager](context.Background(), &fakeEndpoints{tags: map[string]bool{"dead": typ == C.TypeWireGuard}})

			_, _, ok := g.penaltyFailoverDial(context.Background(), N.NetworkTCP, M.Socksaddr{}, dead, dialErr)
			if ok {
				t.Fatalf("%s %v: destination refusal must not trigger failover", typ, dialErr)
			}
			if g.penaltyOf("dead") != 0 {
				t.Fatalf("%s %v: destination refusal must not penalize, got %d", typ, dialErr, g.penaltyOf("dead"))
			}
			if alive.dials != 0 {
				t.Fatalf("%s %v: no fallback dial for destination errors", typ, dialErr)
			}
		}
	}
}

// TestPenaltyFailoverDial_proxyRefused: refused/reset at a proxy leaf comes from
// the node's server (its process stopped on a live host) — penalty, one fallback
// dial, the selection moves.
func TestPenaltyFailoverDial_proxyRefused(t *testing.T) {
	for _, dialErr := range []error{syscall.ECONNREFUSED, E.Cause(syscall.ECONNRESET, "read handshake")} {
		dead := &penaltyDialNode{tag: "dead", typ: C.TypeTrojan, err: dialErr}
		alive := &penaltyDialNode{tag: "alive", addr: "server"}
		g := newPenaltyTestGroup(t, []*penaltyDialNode{dead, alive}, map[string]uint16{"dead": 10, "alive": 50})
		g.selectedOutboundTCP.Store(dead)

		conn, fallback, ok := g.penaltyFailoverDial(context.Background(), N.NetworkTCP, M.Socksaddr{}, dead, dialErr)
		if !ok {
			t.Fatalf("%v: a refused proxy node must fail over", dialErr)
		}
		conn.Close()
		if fallback.Tag() != "alive" || alive.dials != 1 {
			t.Fatalf("%v: one fallback dial via alive, got %s/%d", dialErr, fallback.Tag(), alive.dials)
		}
		if g.penaltyOf("dead") != 1 {
			t.Fatalf("%v: the refused proxy node must get +1 penalty, got %d", dialErr, g.penaltyOf("dead"))
		}
		if g.selectedOutboundTCP.Load() != adapter.Outbound(alive) {
			t.Fatalf("%v: selection must move to the fallback", dialErr)
		}
	}
}

// TestMaybeForceRetest_levelTriggerWithGap: тотальные штрафы запускают force-прогон;
// повторный запуск не раньше penaltyForcedRetestGap от КОНЦА прошлого прогона;
// частичное здоровье (кандидат ниже порога) не запускает вовсе.
func TestMaybeForceRetest_levelTriggerWithGap(t *testing.T) {
	a := &penaltyDialNode{tag: "a"}
	b := &penaltyDialNode{tag: "b"}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{a, b}, map[string]uint16{"a": 10, "b": 50})
	// Глушим реальный прогон: checking=true → CheckOutbounds(force) — мгновенный no-op.
	g.checking.Store(true)

	for i := 0; i < 3; i++ {
		g.penaltyAdd("a")
	}
	g.maybeForceRetest()
	if !g.forcedRetestRunning.Load() {
		// b без штрафов → не тотально → не должен был стрелять.
	} else {
		t.Fatal("partial health (b at 0) must not force a retest")
	}

	for i := 0; i < 3; i++ {
		g.penaltyAdd("b")
	}
	g.maybeForceRetest() // теперь тотально → стреляет
	waitUntil(t, func() bool { return !g.forcedRetestRunning.Load() && !g.lastForcedRetest.Load().IsZero() })
	firstRun := g.lastForcedRetest.Load()

	g.maybeForceRetest() // сразу же — дельта-лимит должен удержать
	if g.forcedRetestRunning.Load() {
		t.Fatal("gap not elapsed → no second forced retest")
	}
	if !g.lastForcedRetest.Load().Equal(firstRun) {
		t.Fatal("gap not elapsed → timestamp must be unchanged")
	}

	// Отматываем «конец прошлого прогона» за пределы паузы → уровень-триггер стреляет снова.
	g.lastForcedRetest.Store(time.Now().Add(-penaltyForcedRetestGap - time.Second))
	g.maybeForceRetest()
	waitUntil(t, func() bool { return !g.forcedRetestRunning.Load() })
	if g.lastForcedRetest.Load().Equal(firstRun) || g.lastForcedRetest.Load().Add(penaltyForcedRetestGap).Before(time.Now()) {
		t.Fatal("after the gap the level trigger must fire again and restamp from run end")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// lx:end SPEC 054

// lx: SPEC 139 — the dial path picks and moves the selection while a selection
// update writes it; meaningful under -race.
func TestSelectedOutboundConcurrentPickAndMove(t *testing.T) {
	a := &penaltyDialNode{tag: "a", addr: "server"}
	b := &penaltyDialNode{tag: "b", addr: "server"}
	g := newPenaltyTestGroup(t, []*penaltyDialNode{a, b}, map[string]uint16{"a": 10, "b": 20})
	g.setSelectedTCPLx(a)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = g.pickForDialNode(N.NetworkTCP)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				g.moveSelection(N.NetworkTCP, b)
			} else {
				g.moveSelection(N.NetworkTCP, a)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			g.setSelectedTCPLx(a)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			g.performSelectionUpdate(false)
			_ = g.failoverHeld()
		}
	}()
	wg.Wait()
}
