// lx: проверка того, доходит ли interrupt_exist_connections до реального
// трафика селектора.
//
// Утверждение, которое проверяется: соединение, пришедшее из inbound (то есть
// через Selector.NewConnection — именно так его отдаёт роутер, route/route.go:175),
// НЕ регистрируется в s.interruptGroup, потому что Selector.NewConnection
// передаёт в ConnectionManager `selected`, а не `s`, и собственный
// Selector.DialContext (единственное место регистрации, selector.go:154)
// не вызывается.
//
// Тест намеренно НЕ моделирует interrupt.Group отдельно: он поднимает
// настоящий Selector и настоящий route.ConnectionManager и смотрит на факт
// закрытия живого сокета при SelectOutbound.

package group

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/route"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// probeNode — узел, чей DialContext отдаёт заранее заготовленный конец пайпа.
// Второй конец остаётся у теста: по нему видно, закрыли сокет или нет.
type probeNode struct {
	adapter.Outbound
	tag      string
	dialed   atomic.Int32
	makeConn func() net.Conn
}

func (n *probeNode) Type() string           { return C.TypeDirect }
func (n *probeNode) Tag() string            { return n.tag }
func (n *probeNode) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (n *probeNode) Dependencies() []string { return nil }

func (n *probeNode) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	n.dialed.Add(1)
	return n.makeConn(), nil
}

func (n *probeNode) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

// stubOutboundManager — минимальный менеджер, отдающий узлы по тегу.
type stubOutboundManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *stubOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	ob, ok := m.byTag[tag]
	return ob, ok
}

func (m *stubOutboundManager) Outbounds() []adapter.Outbound {
	var list []adapter.Outbound
	for _, ob := range m.byTag {
		list = append(list, ob)
	}
	return list
}

func (m *stubOutboundManager) Default() adapter.Outbound { return nil }

// newSelectorUnderTest поднимает настоящий Selector над двумя узлами,
// с настоящим route.ConnectionManager в контексте.
func newSelectorUnderTest(t *testing.T, interruptExisting bool) (*Selector, *probeNode, *probeNode) {
	t.Helper()

	nodeA := &probeNode{tag: "node-a"}
	nodeB := &probeNode{tag: "node-b"}

	mgr := &stubOutboundManager{byTag: map[string]adapter.Outbound{
		"node-a": nodeA,
		"node-b": nodeB,
	}}

	ctx := context.Background()
	ctx = service.ContextWith[adapter.OutboundManager](ctx, mgr)
	ctx = service.ContextWith[adapter.ConnectionManager](ctx, route.NewConnectionManager(logger.NOP()))

	sel := &Selector{
		Adapter:                      outbound.NewAdapter(C.TypeSelector, "sel", nil, []string{"node-a", "node-b"}),
		ctx:                          ctx,
		outbound:                     mgr,
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger.NOP(),
		tags:                         []string{"node-a", "node-b"},
		defaultTag:                   "node-a",
		outbounds:                    map[string]adapter.Outbound{"node-a": nodeA, "node-b": nodeB},
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExisting,
	}
	if err := sel.Start(); err != nil {
		t.Fatalf("Selector.Start: %v", err)
	}
	return sel, nodeA, nodeB
}

// Путь 1 — селектор как ДИАЛЕР (его дёргают через DialContext).
// Здесь регистрация в interruptGroup по коду есть (selector.go:154),
// значит переключение обязано порвать соединение.
func TestLxSelectorInterruptViaDialContext(t *testing.T) {
	sel, nodeA, _ := newSelectorUnderTest(t, true)

	remote, local := net.Pipe() // local отдаём узлу, remote держим у себя
	nodeA.makeConn = func() net.Conn { return local }

	ctx := context.Background()
	conn, err := sel.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("example.com:80"))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn

	if !sel.SelectOutbound("node-b") {
		t.Fatal("SelectOutbound вернул false")
	}

	if alive := connAlive(t, remote); alive {
		t.Error("ПУТЬ ДИАЛЕРА: соединение пережило переключение — interrupt не сработал")
	} else {
		t.Log("ПУТЬ ДИАЛЕРА: соединение порвано, interrupt работает")
	}
}

// Путь 2 — селектор как HANDLER (роутер отдаёт соединение через NewConnection).
// Это путь всего inbound-трафика: route/route.go:175.
// Проверяем, рвётся ли соединение при переключении узла.
func TestLxSelectorInterruptViaNewConnection(t *testing.T) {
	sel, nodeA, _ := newSelectorUnderTest(t, true)

	// upstreamRemote/upstreamLocal — «сокет до узла», его и должен рвать interrupt.
	upstreamRemote, upstreamLocal := net.Pipe()
	nodeA.makeConn = func() net.Conn { return upstreamLocal }

	// inboundClient/inboundServer — «сокет от приложения», его отдаёт роутер.
	inboundClient, inboundServer := net.Pipe()
	defer inboundClient.Close()

	metadata := adapter.InboundContext{
		Network:     N.NetworkTCP,
		Destination: M.ParseSocksaddr("example.com:80"),
	}

	go sel.NewConnection(context.Background(), inboundServer, metadata, func(it error) {})

	// Ждём, пока узел реально сдиалит.
	deadline := time.Now().Add(2 * time.Second)
	for nodeA.dialed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if nodeA.dialed.Load() == 0 {
		t.Fatal("узел так и не сдиалил — тест не дошёл до проверки")
	}

	if !sel.SelectOutbound("node-b") {
		t.Fatal("SelectOutbound вернул false")
	}
	time.Sleep(100 * time.Millisecond)

	if alive := connAlive(t, upstreamRemote); alive {
		t.Error("ПУТЬ HANDLER: соединение пережило переключение — interrupt НЕ сработал (баг)")
	} else {
		t.Log("ПУТЬ HANDLER: соединение порвано, interrupt работает")
	}
}

// handlerNode — узел-ConnectionHandler: сам принимает соединение и держит его,
// как это делают вложенные группы и protocol/dns. В selected-ветке A такой узел
// получает conn напрямую, минуя ConnectionManager.
type handlerNode struct {
	probeNode
	held atomic.Pointer[net.Conn]
}

func (n *handlerNode) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	n.held.Store(&conn)
}

// Путь 3 — selected сам ConnectionHandler (вложенная группа / dns).
// v1-фикс (selected → s) эту ветку не покрывал; обёртка входящего — покрывает.
func TestLxSelectorInterruptHandlerBranch(t *testing.T) {
	nodeA := &handlerNode{probeNode: probeNode{tag: "node-a"}}
	nodeB := &probeNode{tag: "node-b"}

	mgr := &stubOutboundManager{byTag: map[string]adapter.Outbound{
		"node-a": nodeA,
		"node-b": nodeB,
	}}

	ctx := context.Background()
	ctx = service.ContextWith[adapter.OutboundManager](ctx, mgr)
	ctx = service.ContextWith[adapter.ConnectionManager](ctx, route.NewConnectionManager(logger.NOP()))

	sel := &Selector{
		Adapter:                      outbound.NewAdapter(C.TypeSelector, "sel", nil, []string{"node-a", "node-b"}),
		ctx:                          ctx,
		outbound:                     mgr,
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger.NOP(),
		tags:                         []string{"node-a", "node-b"},
		defaultTag:                   "node-a",
		outbounds:                    map[string]adapter.Outbound{"node-a": nodeA, "node-b": nodeB},
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: true,
	}
	if err := sel.Start(); err != nil {
		t.Fatalf("Selector.Start: %v", err)
	}

	inboundClient, inboundServer := net.Pipe()
	defer inboundClient.Close()

	metadata := adapter.InboundContext{
		Network:     N.NetworkTCP,
		Destination: M.ParseSocksaddr("example.com:80"),
	}
	sel.NewConnection(context.Background(), inboundServer, metadata, func(it error) {})

	if nodeA.held.Load() == nil {
		t.Fatal("handler-узел не получил соединение — окружение нерабочее")
	}

	if !sel.SelectOutbound("node-b") {
		t.Fatal("SelectOutbound вернул false")
	}
	time.Sleep(50 * time.Millisecond)

	if alive := connAlive(t, inboundClient); alive {
		t.Error("ВЕТКА HANDLER-selected: соединение пережило переключение — interrupt НЕ сработал")
	} else {
		t.Log("ВЕТКА HANDLER-selected: соединение порвано, interrupt работает")
	}
}

// connAlive проверяет, жив ли конец пайпа: на закрытом write вернёт ошибку.
func connAlive(t *testing.T, c net.Conn) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		c.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
		_, err := c.Write([]byte("ping"))
		// Таймаут = живой, но никто не читает. Прочие ошибки = порван.
		if err == nil {
			done <- true
			return
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			done <- true
			return
		}
		done <- false
	}()
	select {
	case alive := <-done:
		return alive
	case <-time.After(2 * time.Second):
		return true
	}
}

// Контроль: убеждаемся, что в HANDLER-пути узел действительно диалит и сокет
// живой ДО переключения. Иначе FAIL основного теста был бы артефактом.
func TestLxSelectorHandlerPathSanity(t *testing.T) {
	sel, nodeA, _ := newSelectorUnderTest(t, true)

	upstreamRemote, upstreamLocal := net.Pipe()
	nodeA.makeConn = func() net.Conn { return upstreamLocal }

	inboundClient, inboundServer := net.Pipe()
	defer inboundClient.Close()

	metadata := adapter.InboundContext{
		Network:     N.NetworkTCP,
		Destination: M.ParseSocksaddr("example.com:80"),
	}
	go sel.NewConnection(context.Background(), inboundServer, metadata, func(it error) {})

	deadline := time.Now().Add(2 * time.Second)
	for nodeA.dialed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if nodeA.dialed.Load() == 0 {
		t.Fatal("узел не сдиалил — окружение теста нерабочее")
	}
	t.Logf("узел сдиалил %d раз(а)", nodeA.dialed.Load())

	if !connAlive(t, upstreamRemote) {
		t.Fatal("сокет мёртв ЕЩЁ ДО переключения — FAIL основного теста был бы артефактом")
	}
	t.Log("до переключения сокет жив — окружение корректно")
}
