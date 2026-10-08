// lx: SPEC 084 — issue #20: переключение вложенного selector'а глушит весь
// трафик. Сценарий репортёра на настоящих Selector'ах и настоящем
// route.ConnectionManager:
//
//	global-auto-out (A) → eu-auto-out (B) → узел;  dns detour = global-auto-out.
//
// Входящее из TUN проходит outer.NewConnection → inner.NewConnection и обёрнуто
// A, затем B (Close идёт B→A). DoH-соединение поднято через outer.DialContext и
// обёрнуто B, затем A (Close идёт A→B). Переключение inner (Clash API) зовёт
// Interrupt(B), HTTP/2-клиент в это же время закрывает DoH — до SPEC 084 это
// был ABBA-дедлок, после которого каждое новое соединение вставало в
// NewConn/NewSingPacketConn на том же замке.
//
// Детерминированность — «затвором»: первое соединение группы B блокирует свой
// Close до сигнала, чтобы обе стороны гарантированно встретились.

package group

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/route"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// gatedPipeConn — конец пайпа, чей Close сообщает о входе и ждёт отпускания.
type gatedPipeConn struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *gatedPipeConn) Close() error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.Conn.Close()
}

// newNestedSelectorsUnderTest поднимает outer → inner → {node-a, node-b};
// у обоих selector'ов interrupt_exist_connections=true, как в конфиге репортёра.
func newNestedSelectorsUnderTest(t *testing.T) (outer, inner *Selector, nodeA, nodeB *probeNode) {
	t.Helper()

	nodeA = &probeNode{tag: "node-a"}
	nodeB = &probeNode{tag: "node-b"}
	mgr := &stubOutboundManager{byTag: map[string]adapter.Outbound{
		"node-a": nodeA,
		"node-b": nodeB,
	}}

	ctx := context.Background()
	ctx = service.ContextWith[adapter.OutboundManager](ctx, mgr)
	ctx = service.ContextWith[adapter.ConnectionManager](ctx, route.NewConnectionManager(logger.NOP()))

	newSelector := func(tag string, tags []string) *Selector {
		return &Selector{
			Adapter:                      outbound.NewAdapter(C.TypeSelector, tag, nil, tags),
			ctx:                          ctx,
			outbound:                     mgr,
			connection:                   service.FromContext[adapter.ConnectionManager](ctx),
			logger:                       logger.NOP(),
			tags:                         tags,
			defaultTag:                   tags[0],
			outbounds:                    make(map[string]adapter.Outbound),
			interruptGroup:               interrupt.NewGroup(),
			interruptExternalConnections: true,
		}
	}
	inner = newSelector("eu-auto-out", []string{"node-a", "node-b"})
	mgr.byTag["eu-auto-out"] = inner
	outer = newSelector("global-auto-out", []string{"eu-auto-out"})

	if err := inner.Start(adapter.StartStateStart, adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())); err != nil {
		t.Fatalf("inner.Start: %v", err)
	}
	if err := outer.Start(adapter.StartStateStart, adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())); err != nil {
		t.Fatalf("outer.Start: %v", err)
	}
	return outer, inner, nodeA, nodeB
}

func TestLxNestedSelectorSwitchDuringDetourCloseNoDeadlock(t *testing.T) {
	outer, inner, nodeA, nodeB := newNestedSelectorsUnderTest(t)

	gateRemote, gateLocal := net.Pipe()
	defer gateRemote.Close()
	gate := &gatedPipeConn{Conn: gateLocal, entered: make(chan struct{}), release: make(chan struct{})}
	upstreamRemote, upstreamLocal := net.Pipe()
	dohRemote, dohLocal := net.Pipe()

	// Узел отдаёт соединения в порядке дозвонов: затвор, сокет для входящего, DoH.
	queue := make(chan net.Conn, 3)
	queue <- gate
	queue <- upstreamLocal
	queue <- dohLocal
	nodeA.makeConn = func() net.Conn { return <-queue }

	// 1. Старое соединение через inner: первая запись его группы — затвор.
	if _, err := inner.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443")); err != nil {
		t.Fatalf("inner.DialContext: %v", err)
	}

	// 2. Входящее из TUN: outer.NewConnection → inner.NewConnection → узел.
	inboundClient, inboundServer := net.Pipe()
	defer inboundClient.Close()
	metadata := adapter.InboundContext{
		Network:     N.NetworkTCP,
		Destination: M.ParseSocksaddr("example.com:80"),
	}
	go outer.NewConnection(context.Background(), inboundServer, metadata, func(it error) {})
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.dialed.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if nodeA.dialed.Load() < 2 {
		t.Fatal("узел не сдиалил для входящего — окружение теста нерабочее")
	}

	// 3. DoH через detour = outer → inner → узел.
	dohConn, err := outer.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("9.9.9.9:443"))
	if err != nil {
		t.Fatalf("outer.DialContext: %v", err)
	}

	// 4. Переключение inner (Clash API) — Interrupt группы B, упирается в затвор.
	switchDone := make(chan struct{})
	go func() { inner.SelectOutbound("node-b"); close(switchDone) }()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Interrupt не дошёл до затвора — окружение теста нерабочее")
	}

	// 5. HTTP/2-клиент закрывает DoH-соединение: Close идёт A→B.
	closeDone := make(chan struct{})
	go func() { dohConn.Close(); close(closeDone) }()
	time.Sleep(50 * time.Millisecond) // дать Close взять A (на старом коде — и встать на B)
	close(gate.release)

	for _, w := range []struct {
		name string
		ch   chan struct{}
	}{{"SelectOutbound", switchDone}, {"Close DoH", closeDone}} {
		select {
		case <-w.ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s не завершился за 3 с — ABBA-дедлок вложенных selector'ов (issue #20)", w.name)
		}
	}

	if now := inner.Now(); now != "node-b" {
		t.Errorf("inner.Now() = %q, ожидали node-b", now)
	}
	if connAlive(t, upstreamRemote) {
		t.Error("сокет узла пережил переключение — interrupt не сработал")
	}
	if connAlive(t, inboundClient) {
		t.Error("входящее соединение пережило переключение — interrupt не сработал")
	}
	if connAlive(t, dohRemote) {
		t.Error("DoH-соединение не закрыто")
	}

	// 6. Новое соединение после переключения (уже через node-b) не встаёт на замке группы.
	nodeB.makeConn = func() net.Conn { c, _ := net.Pipe(); return c }
	newDone := make(chan error, 1)
	go func() {
		_, err := outer.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.org:443"))
		newDone <- err
	}()
	select {
	case err := <-newDone:
		if err != nil {
			t.Errorf("новый dial после переключения: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("новый dial после переключения завис — замок группы не отпущен")
	}
}
