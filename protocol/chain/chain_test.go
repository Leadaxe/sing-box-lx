package chain

// lx: SPEC 073 — тесты цепочки на фейковых узлах: проверяют порядок хопов,
// звенья (создание/прогрев/эвикшн), прозрачный direct, блок, strip/rewrite/MTU,
// ошибки с позициями и внутренние теги хопов. Сеть не нужна.

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/block"
	"github.com/sagernet/sing-box/protocol/group"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// ---- фейковый узел ---------------------------------------------------------

const typeFake = "fake"

type fakeOptions struct {
	option.DialerOptions
	Name      string                           `json:"name,omitempty"`
	TLS       *option.OutboundTLSOptions       `json:"tls,omitempty"`
	Multiplex *option.OutboundMultiplexOptions `json:"multiplex,omitempty"`
	Transport *option.V2RayTransportOptions    `json:"transport,omitempty"`
}

type traceKey struct{}

type trace struct {
	mu    sync.Mutex
	items []string
}

func (t *trace) add(item string) {
	t.mu.Lock()
	t.items = append(t.items, item)
	t.mu.Unlock()
}

func withTrace(ctx context.Context) (context.Context, *trace) {
	t := &trace{}
	return context.WithValue(ctx, traceKey{}, t), t
}

func traceOf(ctx context.Context) *trace {
	t, _ := ctx.Value(traceKey{}).(*trace)
	return t
}

type fakeRegistry struct {
	mu       sync.Mutex
	created  []string // "tag@detour"
	options  map[string][]fakeOptions
	closed   map[string]int
	closedMu sync.Mutex
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{options: make(map[string][]fakeOptions), closed: make(map[string]int)}
}

func (r *fakeRegistry) record(tag string, options fakeOptions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, tag+"@"+options.Detour)
	r.options[tag] = append(r.options[tag], options)
}

func (r *fakeRegistry) createdList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.created)
}

func (r *fakeRegistry) lastOptions(tag string) fakeOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.options[tag]
	return list[len(list)-1]
}

type fakeOutbound struct {
	outbound.Adapter
	registry *fakeRegistry
	manager  adapter.OutboundManager
	detour   string
	name     string
}

func (f *fakeOutbound) label() string {
	return f.Tag() + "[" + f.detour + "]"
}

func (f *fakeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if t := traceOf(ctx); t != nil {
		t.add(f.label())
	}
	if f.detour == "" {
		client, server := net.Pipe()
		go func() {
			_, _ = server.Write([]byte("ok"))
			server.Close()
		}()
		return client, nil
	}
	next, loaded := f.manager.Outbound(f.detour)
	if !loaded {
		return nil, E.New("detour not found: ", f.detour)
	}
	return next.DialContext(ctx, network, M.ParseSocksaddr(f.Tag()+".server:1"))
}

func (f *fakeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if t := traceOf(ctx); t != nil {
		t.add("udp:" + f.label())
	}
	if f.detour == "" {
		return net.ListenPacket("udp", "127.0.0.1:0")
	}
	next, loaded := f.manager.Outbound(f.detour)
	if !loaded {
		return nil, E.New("detour not found: ", f.detour)
	}
	return next.ListenPacket(ctx, M.ParseSocksaddr(f.Tag()+".server:1"))
}

func (f *fakeOutbound) Close() error {
	f.registry.closedMu.Lock()
	f.registry.closed[f.label()]++
	f.registry.closedMu.Unlock()
	return nil
}

// typeNoDial — тип без dial-полей (должен отвергаться на позициях ≥ 1).
const typeNoDial = "nodial"

type noDialOptions struct {
	Name string `json:"name,omitempty"`
}

// fakeGroup — группа типа urltest (для прогрев-детерминизма) с хуком как у настоящих.
type fakeGroupOptions struct {
	Outbounds []string `json:"outbounds"`
}

type fakeGroup struct {
	outbound.Adapter
	manager adapter.OutboundManager
	tags    []string
}

func (g *fakeGroup) Now() string   { return g.tags[0] }
func (g *fakeGroup) All() []string { return g.tags }

func (g *fakeGroup) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	picked, _ := g.manager.Outbound(g.tags[0])
	picked, err := adapter.ResolveChainLeaf(ctx, picked)
	if err != nil {
		return nil, err
	}
	return picked.DialContext(ctx, network, destination)
}

func (g *fakeGroup) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	picked, _ := g.manager.Outbound(g.tags[0])
	picked, err := adapter.ResolveChainLeaf(ctx, picked)
	if err != nil {
		return nil, err
	}
	return picked.ListenPacket(ctx, destination)
}

// ---- стенд ------------------------------------------------------------------

type stand struct {
	t        *testing.T
	ctx      context.Context
	registry *fakeRegistry
	outbound *outbound.Manager
	endpoint *endpoint.Manager
}

func newStand(t *testing.T) *stand {
	t.Helper()
	fakes := newFakeRegistry()
	registry := outbound.NewRegistry()
	epRegistry := endpoint.NewRegistry()
	var manager *outbound.Manager
	outbound.Register[fakeOptions](registry, typeFake, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options fakeOptions) (adapter.Outbound, error) {
		fakes.record(tag, options)
		return &fakeOutbound{
			Adapter:  outbound.NewAdapterWithDialerOptions(typeFake, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
			registry: fakes,
			manager:  service.FromContext[adapter.OutboundManager](ctx),
			detour:   options.Detour,
			name:     options.Name,
		}, nil
	})
	// wireguard под фейковым конструктором: опции настоящие (для MTU-логики), узел фейковый.
	outbound.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardEndpointOptions) (adapter.Outbound, error) {
		fakes.record(tag, fakeOptions{DialerOptions: options.DialerOptions, Name: "mtu=" + itoa(int(options.MTU))})
		return &fakeOutbound{
			Adapter:  outbound.NewAdapterWithDialerOptions(C.TypeWireGuard, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
			registry: fakes,
			manager:  service.FromContext[adapter.OutboundManager](ctx),
			detour:   options.Detour,
		}, nil
	})
	outbound.Register[noDialOptions](registry, typeNoDial, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options noDialOptions) (adapter.Outbound, error) {
		return &fakeOutbound{Adapter: outbound.NewAdapter(typeNoDial, tag, []string{N.NetworkTCP}, nil), registry: fakes, manager: service.FromContext[adapter.OutboundManager](ctx)}, nil
	})
	outbound.Register[fakeGroupOptions](registry, C.TypeURLTest, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options fakeGroupOptions) (adapter.Outbound, error) {
		return &fakeGroup{Adapter: outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds), manager: service.FromContext[adapter.OutboundManager](ctx), tags: options.Outbounds}, nil
	})
	// direct под фейковым конструктором: настоящему нужен DNS-менеджер в ctx,
	// цепочка же смотрит только на Type().
	outbound.Register[option.DirectOutboundOptions](registry, C.TypeDirect, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.DirectOutboundOptions) (adapter.Outbound, error) {
		return &fakeOutbound{
			Adapter:  outbound.NewAdapter(C.TypeDirect, tag, []string{N.NetworkTCP, N.NetworkUDP}, nil),
			registry: fakes,
			manager:  service.FromContext[adapter.OutboundManager](ctx),
		}, nil
	})
	group.RegisterSelector(registry)
	block.RegisterOutbound(registry)
	RegisterOutbound(registry)

	logFactory := log.NewNOPFactory()
	ctx := context.Background()
	ctx = service.ContextWith[option.OutboundOptionsRegistry](ctx, registry)
	ctx = service.ContextWith[adapter.OutboundRegistry](ctx, registry)
	ctx = service.ContextWith[option.EndpointOptionsRegistry](ctx, epRegistry)
	ctx = service.ContextWith[adapter.EndpointRegistry](ctx, epRegistry)
	ctx = service.ContextWith[log.Factory](ctx, logFactory)
	epManager := endpoint.NewManager(logFactory.NewLogger("endpoint"), epRegistry)
	manager = outbound.NewManager(logFactory.NewLogger("outbound"), registry, epManager, "")
	ctx = service.ContextWith[adapter.OutboundManager](ctx, manager)
	ctx = service.ContextWith[adapter.EndpointManager](ctx, epManager)
	manager.Initialize(func() (adapter.Outbound, error) {
		return registry.CreateOutbound(ctx, nil, logFactory.NewLogger("direct"), "direct", C.TypeDirect, &option.DirectOutboundOptions{})
	})
	return &stand{t: t, ctx: ctx, registry: fakes, outbound: manager, endpoint: epManager}
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func (s *stand) add(tag, typeName string, options any) {
	s.t.Helper()
	if err := s.outbound.Create(s.ctx, nil, log.NewNOPFactory().NewLogger(tag), tag, typeName, options); err != nil {
		s.t.Fatalf("create %s: %v", tag, err)
	}
}

func (s *stand) fake(tag string, opts ...func(*fakeOptions)) {
	options := fakeOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	s.add(tag, typeFake, &options)
}

func (s *stand) selector(tag string, members ...string) {
	s.add(tag, C.TypeSelector, &option.SelectorOutboundOptions{Outbounds: members})
}

func (s *stand) chain(tag string, positions []string, mutate ...func(*option.ChainOutboundOptions)) {
	options := option.ChainOutboundOptions{Outbounds: positions}
	for _, m := range mutate {
		m(&options)
	}
	s.add(tag, C.TypeChain, &options)
}

func (s *stand) start() error {
	for _, stage := range adapter.ListStartStages {
		if err := s.endpoint.Start(stage); err != nil {
			return err
		}
		if err := s.outbound.Start(stage); err != nil {
			return err
		}
	}
	return nil
}

func (s *stand) mustStart() {
	s.t.Helper()
	if err := s.start(); err != nil {
		s.t.Fatalf("start: %v", err)
	}
	s.t.Cleanup(func() { s.outbound.Close() })
}

func (s *stand) chainOf(tag string) *Chain {
	s.t.Helper()
	ob, loaded := s.outbound.Outbound(tag)
	if !loaded {
		s.t.Fatalf("chain %s not found", tag)
	}
	return ob.(*Chain)
}

func (s *stand) dial(tag string) ([]string, error) {
	ctx, tr := withTrace(s.ctx)
	ob, _ := s.outbound.Outbound(tag)
	conn, err := ob.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("target.example:443"))
	if err != nil {
		return tr.items, err
	}
	conn.Close()
	return tr.items, nil
}

func (s *stand) selectIn(groupTag, member string) {
	s.t.Helper()
	ob, _ := s.outbound.Outbound(groupTag)
	if !ob.(*group.Selector).SelectOutbound(member) {
		s.t.Fatalf("select %s in %s failed", member, groupTag)
	}
}

func assertTrace(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " > ") != strings.Join(want, " > ") {
		t.Fatalf("trace mismatch:\n got  %v\n want %v", got, want)
	}
}

func contains(list []string, item string) bool {
	for _, it := range list {
		if it == item {
			return true
		}
	}
	return false
}

// ---- тесты ------------------------------------------------------------------

func TestChainAllNodes(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", []string{"in", "mid", "exit"})
	s.mustStart()

	// прогрев: звенья узловых позиций подняты до первого дозвона
	created := s.registry.createdList()
	if !contains(created, "mid@virt#0") || !contains(created, "exit@virt#1") {
		t.Fatalf("preload missing: %v", created)
	}
	tr, err := s.dial("virt")
	if err != nil {
		t.Fatal(err)
	}
	// порядок пакета: exit (звено через #1) → mid (звено через #0) → in (оригинал)
	assertTrace(t, tr, "exit[virt#1]", "mid[virt#0]", "in[]")
	c := s.chainOf("virt")
	if path := strings.Join(c.ChainPath(), ","); path != "in,mid,exit" {
		t.Fatalf("path %s", path)
	}
	// хопы видны по тегу, но не в списке
	if _, ok := s.outbound.Outbound("virt#1"); !ok {
		t.Fatal("hop tag not resolvable")
	}
	for _, ob := range s.outbound.Outbounds() {
		if ob.Tag() == "virt#1" {
			t.Fatal("hop leaked into Outbounds()")
		}
	}
	status := c.ChainStatus()
	if status.LiveClones != 2 || status.Dials != 1 || status.Positions[2].Clone == nil || status.Positions[2].Clone.State != "idle" {
		t.Fatalf("status %+v", status)
	}
	if status.Positions[0].Clone != nil {
		t.Fatal("position 0 must not have a clone")
	}
}

func TestChainAllSelectorsSwitchAndEvict(t *testing.T) {
	s := newStand(t)
	for _, tag := range []string{"in-a", "in-b", "mid-a", "mid-b", "exit-a", "exit-b"} {
		s.fake(tag)
	}
	s.selector("sel-in", "in-a", "in-b")
	s.selector("sel-mid", "mid-a", "mid-b")
	s.selector("sel-exit", "exit-a", "exit-b")
	s.chain("virt", []string{"sel-in", "sel-mid", "sel-exit"})
	s.mustStart()

	// прогрев детерминированных позиций (селекторы): mid-a@#0, exit-a@#1
	created := s.registry.createdList()
	if !contains(created, "mid-a@virt#0") || !contains(created, "exit-a@virt#1") {
		t.Fatalf("preload: %v", created)
	}
	tr, err := s.dial("virt")
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, tr, "exit-a[virt#1]", "mid-a[virt#0]", "in-a[]")

	// переключение середины: новое звено на первом дозвоне, старое остаётся
	s.selectIn("sel-mid", "mid-b")
	tr, err = s.dial("virt")
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, tr, "exit-a[virt#1]", "mid-b[virt#0]", "in-a[]")
	c := s.chainOf("virt")
	if c.ChainStatus().LiveClones != 3 {
		t.Fatalf("expected 3 live clones, got %d", c.ChainStatus().LiveClones)
	}
	// переключение входа: клоны не нужны, путь меняется
	s.selectIn("sel-in", "in-b")
	tr, _ = s.dial("virt")
	assertTrace(t, tr, "exit-a[virt#1]", "mid-b[virt#0]", "in-b[]")

	// эвикшн: mid-a не выбирали, соединений нет → удаляется; остальные свежие
	c.evictIdle(time.Now().Add(c.idleTimeout + time.Second))
	status := c.ChainStatus()
	if status.LiveClones != 0 || status.ClonesEvicted != 3 {
		// все три звена простаивают (трассовые соединения закрыты) → все удалены
		t.Fatalf("evict: %+v", status)
	}
	if s.registry.closed["mid-a[virt#0]"] != 1 {
		t.Fatalf("mid-a clone not closed: %v", s.registry.closed)
	}
	// следующий дозвон пересоздаёт
	tr, err = s.dial("virt")
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, tr, "exit-a[virt#1]", "mid-b[virt#0]", "in-b[]")
}

func TestChainLiveConnectionHoldsClone(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", []string{"in", "mid", "exit"})
	s.mustStart()
	c := s.chainOf("virt")
	ctx, _ := withTrace(s.ctx)
	conn, err := c.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("target:1"))
	if err != nil {
		t.Fatal(err)
	}
	c.evictIdle(time.Now().Add(time.Hour))
	if c.ChainStatus().LiveClones != 2 {
		t.Fatalf("live connection must hold clones: %+v", c.ChainStatus())
	}
	conn.Close()
	c.evictIdle(time.Now().Add(time.Hour))
	if c.ChainStatus().LiveClones != 0 {
		t.Fatalf("clones must be evicted after close: %+v", c.ChainStatus())
	}
}

func TestChainDirectTransparentAndBlock(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.add("direct-out", C.TypeDirect, &option.DirectOutboundOptions{})
	s.add("block-out", C.TypeBlock, &option.StubOptions{})
	s.selector("sel-mid", "mid", "direct-out", "block-out")
	s.selector("sel-exit", "exit", "direct-out")
	s.chain("virt", []string{"in", "sel-mid", "sel-exit"})
	s.mustStart()

	tr, _ := s.dial("virt")
	assertTrace(t, tr, "exit[virt#1]", "mid[virt#0]", "in[]")

	s.selectIn("sel-mid", "direct-out")
	tr, err := s.dial("virt")
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, tr, "exit[virt#1]", "in[]")
	c := s.chainOf("virt")
	if path := strings.Join(c.ChainPath(), ","); path != "in,exit" {
		t.Fatalf("path %s", path)
	}
	status := c.ChainStatus()
	if !status.Positions[1].Transparent || status.Positions[1].Clone != nil {
		t.Fatalf("position 1 must be transparent: %+v", status.Positions[1])
	}

	s.selectIn("sel-exit", "direct-out")
	tr, _ = s.dial("virt")
	assertTrace(t, tr, "in[]")

	s.selectIn("sel-mid", "block-out")
	s.selectIn("sel-exit", "exit")
	_, err = s.dial("virt")
	if err == nil {
		t.Fatal("block must reject")
	}
	if !strings.Contains(err.Error(), "chain[virt] #2 (exit) via #1 (block-out)") {
		t.Fatalf("error must name positions: %v", err)
	}
}

func TestChainNestedGroupsAndURLTestPreload(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid-a")
	s.fake("mid-b")
	s.fake("exit")
	s.add("ut-mid", C.TypeURLTest, &fakeGroupOptions{Outbounds: []string{"mid-a", "mid-b"}})
	s.selector("sel-mid", "ut-mid")
	s.chain("virt", []string{"in", "sel-mid", "exit"})
	s.mustStart()

	// urltest на пути выбора → позиция 1 не прогревается; exit прогревается
	created := s.registry.createdList()
	if contains(created, "mid-a@virt#0") {
		t.Fatalf("urltest position must stay lazy: %v", created)
	}
	if !contains(created, "exit@virt#1") {
		t.Fatalf("exit must preload: %v", created)
	}
	tr, err := s.dial("virt")
	if err != nil {
		t.Fatal(err)
	}
	assertTrace(t, tr, "exit[virt#1]", "mid-a[virt#0]", "in[]")
}

func TestChainStripRewriteAndErrors(t *testing.T) {
	withEvasion := func(o *fakeOptions) {
		o.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: "x", Fragment: true, UTLS: &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"}}
		o.Multiplex = &option.OutboundMultiplexOptions{Enabled: true, Padding: true}
	}
	t.Run("default strip", func(t *testing.T) {
		s := newStand(t)
		s.fake("in", withEvasion)
		s.fake("exit", withEvasion)
		s.chain("virt", []string{"in", "exit"})
		s.mustStart()
		clone := s.registry.lastOptions("exit")
		if clone.Detour != "virt#0" || clone.TLS == nil || clone.TLS.Fragment || clone.Multiplex == nil || clone.Multiplex.Padding {
			t.Fatalf("strip not applied: %+v tls=%+v mux=%+v", clone, clone.TLS, clone.Multiplex)
		}
		if clone.TLS.UTLS == nil || !clone.TLS.UTLS.Enabled {
			t.Fatal("utls must be kept by default")
		}
		status := s.chainOf("virt").ChainStatus()
		if got := strings.Join(status.Positions[1].Clone.Stripped, ","); got != "tls.fragment,multiplex.padding" {
			t.Fatalf("stripped=%s", got)
		}
	})
	t.Run("strip map and rewrite", func(t *testing.T) {
		s := newStand(t)
		s.fake("in", withEvasion)
		s.fake("exit", withEvasion)
		s.chain("virt", []string{"in", "exit"}, func(o *option.ChainOutboundOptions) {
			o.Strip = map[string]bool{"tls.fragment": false, "tls.utls": true}
			o.Rewrite = map[string]any{typeFake: map[string]any{"name": "rewritten", "tls": map[string]any{"server_name": "patched"}}}
		})
		s.mustStart()
		clone := s.registry.lastOptions("exit")
		if !clone.TLS.Fragment || clone.TLS.UTLS != nil || clone.Multiplex.Padding || clone.Name != "rewritten" || clone.TLS.ServerName != "patched" {
			t.Fatalf("strip/rewrite: %+v tls=%+v", clone, clone.TLS)
		}
		if !s.chainOf("virt").ChainStatus().Positions[1].Clone.Rewritten {
			t.Fatal("rewritten flag")
		}
	})
	t.Run("strip_evasion false", func(t *testing.T) {
		s := newStand(t)
		s.fake("in", withEvasion)
		s.fake("exit", withEvasion)
		off := false
		s.chain("virt", []string{"in", "exit"}, func(o *option.ChainOutboundOptions) { o.StripEvasion = &off })
		s.mustStart()
		clone := s.registry.lastOptions("exit")
		if !clone.TLS.Fragment || !clone.Multiplex.Padding {
			t.Fatalf("nothing must be stripped: %+v", clone.TLS)
		}
	})
	t.Run("unknown strip key", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		s.fake("exit")
		err := s.outbound.Create(s.ctx, nil, log.NewNOPFactory().NewLogger("virt"), "virt", C.TypeChain, &option.ChainOutboundOptions{Outbounds: []string{"in", "exit"}, Strip: map[string]bool{"tls.bogus": true}})
		if err == nil || !strings.Contains(err.Error(), "unknown key tls.bogus") {
			t.Fatalf("expected unknown key error, got %v", err)
		}
	})
	t.Run("utls strip vs reality", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		s.fake("exit", func(o *fakeOptions) {
			o.TLS = &option.OutboundTLSOptions{Enabled: true, UTLS: &option.OutboundUTLSOptions{Enabled: true}, Reality: &option.OutboundRealityOptions{Enabled: true, PublicKey: "k"}}
		})
		s.chain("virt", []string{"in", "exit"}, func(o *option.ChainOutboundOptions) { o.Strip = map[string]bool{"tls.utls": true} })
		err := s.start()
		if err == nil || !strings.Contains(err.Error(), "requires utls") {
			t.Fatalf("expected reality/utls start error, got %v", err)
		}
	})
	t.Run("rewrite unknown field fails start", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		s.fake("exit")
		s.chain("virt", []string{"in", "exit"}, func(o *option.ChainOutboundOptions) {
			o.Rewrite = map[string]any{typeFake: map[string]any{"no_such_field": 1}}
		})
		err := s.start()
		if err == nil || !strings.Contains(err.Error(), "dry-run") {
			t.Fatalf("expected dry-run error, got %v", err)
		}
	})
}

func TestChainMTU(t *testing.T) {
	wg := func(tag string, mtu uint32, peer string) func(*stand) {
		return func(s *stand) {
			s.add(tag, C.TypeWireGuard, &option.WireGuardEndpointOptions{MTU: mtu, Peers: []option.WireGuardPeer{{Address: peer, Port: 51820}}})
		}
	}
	t.Run("wg over wg v4", func(t *testing.T) {
		s := newStand(t)
		wg("wg-in", 1408, "1.2.3.4")(s)
		wg("wg-exit", 1408, "5.6.7.8")(s)
		s.chain("virt", []string{"wg-in", "wg-exit"})
		s.mustStart()
		if got := s.registry.lastOptions("wg-exit").Name; got != "mtu=1348" {
			t.Fatalf("mtu %s", got)
		}
		st := s.chainOf("virt").ChainStatus().Positions[1].Clone
		if st.MTUConfigured != 1408 || st.MTUEffective != 1348 {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("wg over wg v6 domain", func(t *testing.T) {
		s := newStand(t)
		wg("wg-in", 1280, "1.2.3.4")(s)
		wg("wg-exit", 1408, "wg.example.com")(s)
		s.chain("virt", []string{"wg-in", "wg-exit"})
		s.mustStart()
		if got := s.registry.lastOptions("wg-exit").Name; got != "mtu=1200" {
			t.Fatalf("mtu %s", got)
		}
	})
	t.Run("wg over stream proxy keeps mtu", func(t *testing.T) {
		s := newStand(t)
		wg("wg-in", 1280, "1.2.3.4")(s)
		s.fake("vless")
		wg("wg-exit", 1408, "5.6.7.8")(s)
		s.chain("virt", []string{"wg-in", "vless", "wg-exit"})
		s.mustStart()
		if got := s.registry.lastOptions("wg-exit").Name; got != "mtu=1408" {
			t.Fatalf("mtu %s", got)
		}
	})
	t.Run("selector below takes worst case", func(t *testing.T) {
		s := newStand(t)
		s.fake("vless")
		wg("wg-mid", 1300, "1.2.3.4")(s)
		s.selector("sel-mid", "vless", "wg-mid")
		wg("wg-exit", 1408, "5.6.7.8")(s)
		s.chain("virt", []string{"vless", "sel-mid", "wg-exit"})
		s.mustStart()
		if got := s.registry.lastOptions("wg-exit").Name; got != "mtu=1240" {
			t.Fatalf("mtu %s", got)
		}
	})
	t.Run("rewrite mtu then lower", func(t *testing.T) {
		s := newStand(t)
		wg("wg-in", 1408, "1.2.3.4")(s)
		wg("wg-exit", 1408, "5.6.7.8")(s)
		s.chain("virt", []string{"wg-in", "wg-exit"}, func(o *option.ChainOutboundOptions) {
			o.Rewrite = map[string]any{C.TypeWireGuard: map[string]any{"mtu": 1300}}
		})
		s.mustStart()
		if got := s.registry.lastOptions("wg-exit").Name; got != "mtu=1300" {
			t.Fatalf("mtu %s", got)
		}
	})
}

func TestChainValidation(t *testing.T) {
	t.Run("too short", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		err := s.outbound.Create(s.ctx, nil, log.NewNOPFactory().NewLogger("virt"), "virt", C.TypeChain, &option.ChainOutboundOptions{Outbounds: []string{"in"}})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("no dial fields at position 1", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		s.add("nd", typeNoDial, &noDialOptions{})
		s.chain("virt", []string{"in", "nd"})
		err := s.start()
		if err == nil || !strings.Contains(err.Error(), "no dial fields") {
			t.Fatalf("expected no-dial error, got %v", err)
		}
	})
	t.Run("no dial fields at position 0 allowed", func(t *testing.T) {
		s := newStand(t)
		s.add("nd", typeNoDial, &noDialOptions{})
		s.fake("exit")
		s.chain("virt", []string{"nd", "exit"})
		s.mustStart()
	})
	t.Run("nested chain only at 0", func(t *testing.T) {
		s := newStand(t)
		s.fake("a")
		s.fake("b")
		s.fake("c")
		s.chain("inner", []string{"a", "b"})
		s.chain("outer-ok", []string{"inner", "c"})
		s.mustStart()
		if _, err := s.dial("outer-ok"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nested chain at 1 rejected", func(t *testing.T) {
		s := newStand(t)
		s.fake("a")
		s.fake("b")
		s.fake("c")
		s.chain("inner", []string{"a", "b"})
		s.chain("outer-bad", []string{"c", "inner"})
		err := s.start()
		if err == nil || !strings.Contains(err.Error(), "nested chain") {
			t.Fatalf("expected nested chain error, got %v", err)
		}
	})
	t.Run("hop tag collision", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		s.fake("exit")
		s.fake("virt#1")
		s.chain("virt", []string{"in", "exit"})
		err := s.start()
		if err == nil || !strings.Contains(err.Error(), "reserved for chain hop") {
			t.Fatalf("expected collision error, got %v", err)
		}
	})
	t.Run("cycle through group", func(t *testing.T) {
		s := newStand(t)
		s.fake("in")
		s.selector("sel", "virt")
		s.chain("virt", []string{"in", "sel"})
		err := s.start()
		if err == nil {
			t.Fatal("expected circular dependency error")
		}
	})
}

func TestChainCloseRemovesHopsAndClones(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("exit")
	s.chain("virt", []string{"in", "exit"})
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.outbound.Outbound("virt#0"); !ok {
		t.Fatal("hop missing")
	}
	s.outbound.Close()
	if _, ok := s.outbound.Outbound("virt#0"); ok {
		t.Fatal("hop must be removed on close")
	}
	if s.registry.closed["exit[virt#0]"] != 1 {
		t.Fatalf("clone not closed: %v", s.registry.closed)
	}
}

func TestResolveChainLeafNoop(t *testing.T) {
	s := newStand(t)
	s.fake("x")
	s.selector("sel", "x")
	s.mustStart()
	x, _ := s.outbound.Outbound("x")
	got, err := adapter.ResolveChainLeaf(context.Background(), x)
	if err != nil || got != x {
		t.Fatal("no binding must be a no-op")
	}
	sel, _ := s.outbound.Outbound("sel")
	ctx := adapter.ContextWithChainHop(context.Background(), &hop{})
	got, err = adapter.ResolveChainLeaf(ctx, sel)
	if err != nil || got != sel {
		t.Fatal("groups pass through the hook")
	}
}

func TestMergePatch(t *testing.T) {
	target := map[string]any{"a": 1, "b": map[string]any{"c": 2, "d": 3}, "e": 4}
	mergePatch(target, map[string]any{"a": 10, "b": map[string]any{"c": nil, "x": 5}, "e": nil, "f": map[string]any{"g": 6}})
	b := target["b"].(map[string]any)
	if target["a"] != 10 || b["c"] != nil || b["d"] != 3 || b["x"] != 5 || target["e"] != nil || target["f"].(map[string]any)["g"] != 6 {
		t.Fatalf("merge patch result: %v", target)
	}
}

func TestBuildStripSet(t *testing.T) {
	set, err := buildStripSet(true, nil)
	if err != nil || !set["tls.fragment"] || !set["multiplex.padding"] || !set["xhttp.padding"] || set["tls.utls"] {
		t.Fatalf("defaults: %v %v", set, err)
	}
	set, err = buildStripSet(false, map[string]bool{"tls.utls": true})
	if err != nil || set["tls.fragment"] || !set["tls.utls"] {
		t.Fatalf("patch: %v %v", set, err)
	}
	if _, err = buildStripSet(true, map[string]bool{"nope": true}); err == nil {
		t.Fatal("unknown key must error")
	}
}

func TestChainUDPPath(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", []string{"in", "mid", "exit"})
	s.mustStart()
	ctx, tr := withTrace(s.ctx)
	conn, err := s.chainOf("virt").ListenPacket(ctx, M.ParseSocksaddr("1.1.1.1:53"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	assertTrace(t, tr.items, "udp:exit[virt#1]", "udp:mid[virt#0]", "udp:in[]")
}
