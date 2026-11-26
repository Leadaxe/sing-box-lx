package chain

// lx: SPEC 165 — тесты цепочки как перезаписи `detour` на фейковых узлах.
// Имена тестов несут номер правила ALGORITHM.md. Сеть не нужна.

import (
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/lxdatagram"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/block"
	"github.com/sagernet/sing-box/protocol/group"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

// ---- фейковые узлы ----------------------------------------------------------

const (
	typeFake   = "fake"
	typeNoDial = "nodial"
)

type fakeOptions struct {
	option.DialerOptions
	Name   string                     `json:"name,omitempty"`
	Lookup string                     `json:"lookup,omitempty"`
	TLS    *option.OutboundTLSOptions `json:"tls,omitempty"`
}

type noDialOptions struct {
	Name string `json:"name,omitempty"`
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

type fakeOutbound struct {
	outbound.Adapter
	manager adapter.OutboundManager
	detour  string
	// lookup is an outbound the node dials first with the same ctx, as a
	// node resolving its server through a DNS server with a detour does.
	lookup string
}

func (f *fakeOutbound) label() string {
	return f.Tag() + "[" + f.detour + "]"
}

func (f *fakeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if t := traceOf(ctx); t != nil {
		t.add(f.label())
	}
	if f.lookup != "" {
		resolver, _ := f.manager.Outbound(f.lookup)
		conn, err := resolver.DialContext(ctx, network, M.ParseSocksaddr("dns.example:53"))
		if err != nil {
			return nil, err
		}
		conn.Close()
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
	adapter.RecordCarrier(ctx, next) // as the detour dialer does
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

// fakeGroup — urltest, выбирающий на каждом соединении первого члена, как
// round_robin: проверка Х8 без Now().
type fakeGroup struct {
	outbound.Adapter
	manager adapter.OutboundManager
	tags    []string
}

func (g *fakeGroup) All() []string { return g.tags }
func (g *fakeGroup) Selected(string) adapter.Outbound {
	selected, _ := g.manager.Outbound(g.tags[0])
	return selected
}
func (g *fakeGroup) AttachConnection(io.Closer) func() { return func() {} }

func (g *fakeGroup) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	picked, _ := g.manager.Outbound(g.tags[0])
	ctx, picked, err := adapter.ResolveChainLeaf(ctx, picked)
	if err != nil {
		return nil, err
	}
	return picked.DialContext(ctx, network, destination)
}

func (g *fakeGroup) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	picked, _ := g.manager.Outbound(g.tags[0])
	ctx, picked, err := adapter.ResolveChainLeaf(ctx, picked)
	if err != nil {
		return nil, err
	}
	return picked.ListenPacket(ctx, destination)
}

// fakeDirectDialer replaces the direct dialer of a disabled entry (Х5).
type fakeDirectDialer struct{}

func (fakeDirectDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if t := traceOf(ctx); t != nil {
		t.add("direct-fallback")
	}
	client, server := net.Pipe()
	go server.Close()
	return client, nil
}

func (fakeDirectDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if t := traceOf(ctx); t != nil {
		t.add("udp:direct-fallback")
	}
	return net.ListenPacket("udp", "127.0.0.1:0")
}

// fakeDisabledStore — in-memory ChainDisabledStore over a nil CacheFile.
type fakeDisabledStore struct {
	adapter.CacheFile
	mu   sync.Mutex
	data map[string][]string
}

func (f *fakeDisabledStore) LoadChainDisabled(chainTag string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.data[chainTag])
}

func (f *fakeDisabledStore) StoreChainDisabled(chainTag string, disabledTags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[chainTag] = slices.Clone(disabledTags)
	return nil
}

// ---- стенд ------------------------------------------------------------------

type stand struct {
	t        *testing.T
	ctx      context.Context
	outbound *outbound.Manager
	endpoint *endpoint.Manager
	scope    *adapter.Scope
	registry *outbound.Registry
	options  option.Options
	changes  map[string][]option.LXConfigChange
	created  map[string]int
	createMu sync.Mutex
}

func newStand(t *testing.T) *stand {
	t.Helper()
	s := &stand{t: t, created: make(map[string]int)}
	registry := outbound.NewRegistry()
	epRegistry := endpoint.NewRegistry()
	newFake := func(typeName string) func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.DialerOptions) (adapter.Outbound, error) {
		return func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.DialerOptions) (adapter.Outbound, error) {
			s.createMu.Lock()
			s.created[tag]++
			s.createMu.Unlock()
			return &fakeOutbound{
				Adapter: outbound.NewAdapterWithDialerOptions(typeName, tag, []string{N.NetworkTCP, N.NetworkUDP}, options),
				manager: service.FromContext[adapter.OutboundManager](ctx),
				detour:  options.Detour,
			}, nil
		}
	}
	fake := newFake(typeFake)
	outbound.Register[fakeOptions](registry, typeFake, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options fakeOptions) (adapter.Outbound, error) {
		created, err := fake(ctx, router, logger, tag, options.DialerOptions)
		if err == nil {
			created.(*fakeOutbound).lookup = options.Lookup
		}
		return created, err
	})
	wireGuard := newFake(C.TypeWireGuard)
	outbound.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardEndpointOptions) (adapter.Outbound, error) {
		return wireGuard(ctx, router, logger, tag, options.DialerOptions)
	})
	outbound.Register[noDialOptions](registry, typeNoDial, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options noDialOptions) (adapter.Outbound, error) {
		return &fakeOutbound{Adapter: outbound.NewAdapter(typeNoDial, tag, []string{N.NetworkTCP}, nil), manager: service.FromContext[adapter.OutboundManager](ctx)}, nil
	})
	outbound.Register[option.URLTestOutboundOptions](registry, C.TypeURLTest, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.URLTestOutboundOptions) (adapter.Outbound, error) {
		return &fakeGroup{Adapter: outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds), manager: service.FromContext[adapter.OutboundManager](ctx), tags: options.Outbounds}, nil
	})
	// direct под фейковым конструктором: настоящему нужен DNS-менеджер.
	direct := newFake(C.TypeDirect)
	outbound.Register[option.DirectOutboundOptions](registry, C.TypeDirect, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.DirectOutboundOptions) (adapter.Outbound, error) {
		return direct(ctx, router, logger, tag, option.DialerOptions{})
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
	manager := outbound.NewManager(registry, epManager, "")
	ctx = service.ContextWith[adapter.OutboundManager](ctx, manager)
	ctx = service.ContextWith[adapter.EndpointManager](ctx, epManager)
	s.ctx, s.outbound, s.endpoint, s.registry = ctx, manager, epManager, registry
	s.scope = adapter.NewScope(ctx, logFactory.Logger())
	return s
}

func (s *stand) add(tag, typeName string, options any) {
	s.options.Outbounds = append(s.options.Outbounds, option.Outbound{Type: typeName, Tag: tag, Options: options})
}

func (s *stand) fake(tag string, detour ...string) *fakeOptions {
	options := &fakeOptions{}
	if len(detour) > 0 {
		options.Detour = detour[0]
	}
	s.add(tag, typeFake, options)
	return options
}

func (s *stand) selector(tag string, members ...string) {
	s.add(tag, C.TypeSelector, &option.SelectorOutboundOptions{Outbounds: members})
}

func (s *stand) chain(tag string, positions ...string) *option.ChainOutboundOptions {
	options := &option.ChainOutboundOptions{Outbounds: positions}
	s.add(tag, C.TypeChain, options)
	return options
}

// rewrite runs the SPEC 165 rewrite on the stand's config.
func (s *stand) rewrite() error {
	changes, err := Rewrite(s.ctx, &s.options)
	s.changes = changes
	return err
}

func (s *stand) mustRewrite() {
	s.t.Helper()
	if err := s.rewrite(); err != nil {
		s.t.Fatalf("rewrite: %v", err)
	}
}

// buildErr rewrites and creates the outbounds; it returns the error of the
// rewrite or of the manager's start-order check (Initialize).
func (s *stand) buildErr() error {
	if err := s.rewrite(); err != nil {
		return err
	}
	logFactory := log.NewNOPFactory()
	for _, item := range s.options.Outbounds {
		if err := s.outbound.Create(s.ctx, nil, logFactory.NewLogger(item.Tag), item.Tag, item.Type, item.Options); err != nil {
			return err
		}
	}
	return s.outbound.Initialize(func() (adapter.Outbound, error) {
		return s.registry.CreateOutbound(s.ctx, nil, logFactory.NewLogger("default"), "default-direct", C.TypeDirect, &option.DirectOutboundOptions{})
	})
}

// build rewrites, creates every outbound and starts the managers.
func (s *stand) build() {
	s.t.Helper()
	s.mustRewrite()
	logFactory := log.NewNOPFactory()
	for _, item := range s.options.Outbounds {
		if err := s.outbound.Create(s.ctx, nil, logFactory.NewLogger(item.Tag), item.Tag, item.Type, item.Options); err != nil {
			s.t.Fatalf("create %s: %v", item.Tag, err)
		}
	}
	// Initialize sorts the outbounds that exist now (upstream 9e0a6a393).
	err := s.outbound.Initialize(func() (adapter.Outbound, error) {
		return s.registry.CreateOutbound(s.ctx, nil, logFactory.NewLogger("default"), "default-direct", C.TypeDirect, &option.DirectOutboundOptions{})
	})
	if err != nil {
		s.t.Fatalf("initialize: %v", err)
	}
	for _, stage := range adapter.ListStartStages {
		if err := s.scope.Start("endpoint", s.endpoint, stage); err != nil {
			s.t.Fatalf("start: %v", err)
		}
		if err := s.scope.Start("outbound", s.outbound, stage); err != nil {
			s.t.Fatalf("start: %v", err)
		}
	}
	s.t.Cleanup(func() { s.scope.Close() })
}

func (s *stand) chainOf(tag string) *Chain {
	s.t.Helper()
	ob, loaded := s.outbound.Outbound(tag)
	if !loaded {
		s.t.Fatalf("chain %s not found", tag)
	}
	return ob.(*Chain)
}

func (s *stand) dialCtx(ctx context.Context, tag string) ([]string, error) {
	ctx, tr := withTrace(ctx)
	ob, _ := s.outbound.Outbound(tag)
	conn, err := ob.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("target.example:443"))
	if err != nil {
		return tr.items, err
	}
	conn.Close()
	return tr.items, nil
}

func (s *stand) dial(tag string) []string {
	s.t.Helper()
	items, err := s.dialCtx(s.ctx, tag)
	if err != nil {
		s.t.Fatalf("dial %s: %v", tag, err)
	}
	return items
}

func (s *stand) detourOf(tag string) string {
	s.t.Helper()
	for _, item := range s.options.Outbounds {
		if item.Tag == tag {
			if wrapper, ok := item.Options.(option.DialerOptionsWrapper); ok {
				return wrapper.TakeDialerOptions().Detour
			}
			return ""
		}
	}
	s.t.Fatalf("no outbound %s", tag)
	return ""
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

func requireError(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with %q", parts)
	}
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err, part)
		}
	}
}

// ---- 4.1 перезапись ---------------------------------------------------------

func TestRewriteП1П6Hops(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", "in", "mid", "exit")
	s.mustRewrite()
	var hops []string
	for _, item := range s.options.Outbounds {
		if item.Type == C.TypeChainHop {
			hop := item.Options.(*option.ChainHopOutboundOptions)
			hops = append(hops, item.Tag+"="+hop.Outbound+"/"+hop.Below)
		}
	}
	if got := strings.Join(hops, " "); got != "virt#0=in/ virt#1=mid/virt#0 virt#2=exit/virt#1" {
		t.Fatalf("hops %s", got)
	}
	s.options.Outbounds = nil
	s2 := newStand(t)
	s2.fake("in")
	s2.fake("exit")
	s2.fake("virt#1")
	s2.chain("virt", "in", "exit")
	requireError(t, s2.rewrite(), "virt#1", "reserved")
}

func TestRewriteП2П3Detour(t *testing.T) {
	s := newStand(t)
	s.fake("relay")
	s.fake("in", "relay")
	s.fake("exit", "relay")
	original := s.options.Outbounds[2].Options
	s.chain("virt", "in", "exit")
	s.mustRewrite()
	if s.detourOf("in") != "relay" {
		t.Fatal("П3: the entry keeps its own detour")
	}
	if s.detourOf("exit") != "virt#0" {
		t.Fatalf("П2: exit detour %s", s.detourOf("exit"))
	}
	if original.(*fakeOptions).Detour != "relay" {
		t.Fatal("the caller's options struct must not be written through")
	}
	changes := s.changes["exit"]
	if len(changes) != 1 || changes[0].Field != "detour" || changes[0].From != "relay" || changes[0].To != "virt#0" ||
		changes[0].By != (option.LXConfigChangeBy{Type: option.LXChangeByChain, Chain: "virt", Position: 1}) {
		t.Fatalf("changes %+v", changes)
	}
	if len(s.changes["in"]) != 0 {
		t.Fatal("П3: no change on the entry")
	}
}

func TestRewriteП4GroupsOfAnyDepth(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("a")
	s.fake("b")
	s.add("direct", C.TypeDirect, &option.DirectOutboundOptions{})
	s.add("block", C.TypeBlock, &option.StubOptions{})
	s.selector("inner", "b", "block")
	s.selector("outer", "a", "inner", "direct")
	s.chain("virt", "in", "outer")
	s.mustRewrite()
	if s.detourOf("a") != "virt#0" || s.detourOf("b") != "virt#0" {
		t.Fatal("П2: every leaf of the group is taken over")
	}
	for _, item := range s.options.Outbounds {
		if item.Tag == "outer" && !slices.Equal(item.Options.(*option.SelectorOutboundOptions).Outbounds, []string{"a", "inner", "direct"}) {
			t.Fatal("П4: the group is not changed")
		}
	}
	if _, has := s.changes["direct"]; has {
		t.Fatal("direct is not a reachable node")
	}
}

func TestRewriteП5Rewrite(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	exit := s.fake("exit")
	exit.Name = "old"
	exit.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: "a.example"}
	s.chain("virt", "in", "exit").Rewrite = option.ChainRewrite{typeFake: map[string]any{
		"name": nil,
		"tls":  map[string]any{"server_name": "b.example", "insecure": true},
	}}
	s.mustRewrite()
	var fields []string
	for _, change := range s.changes["exit"] {
		fields = append(fields, change.By.Type+":"+change.Field)
	}
	if got := strings.Join(fields, " "); got != "chain:detour chain_rewrite:name chain_rewrite:tls.insecure chain_rewrite:tls.server_name" {
		t.Fatalf("changes %s", got)
	}
	removed := s.changes["exit"][1]
	if removed.From != "old" || removed.To != nil || removed.By.NodeType != typeFake || removed.By.Position != 1 {
		t.Fatalf("removal %+v", removed)
	}
	for _, item := range s.options.Outbounds {
		if item.Tag == "exit" {
			options := item.Options.(*fakeOptions)
			if options.Name != "" || options.TLS.ServerName != "b.example" || !options.TLS.Insecure {
				t.Fatalf("rewritten %+v", options)
			}
		}
	}
	s2 := newStand(t)
	s2.fake("in")
	s2.fake("exit")
	s2.chain("virt", "in", "exit").Rewrite = option.ChainRewrite{typeFake: map[string]any{"detour": "x"}}
	requireError(t, s2.rewrite(), "cannot change detour")
}

func TestRewriteП7RemovedChainKeys(t *testing.T) {
	for key, text := range map[string]string{
		`"idle_timeout": "5m"`:   "no longer creates link instances",
		`"strip_evasion": false`: "lx.detour.strip",
		`"strip": {}`:            "lx.detour.strip",
		`"strip": null`:          "lx.detour.strip",
	} {
		var options option.ChainOutboundOptions
		err := json.Unmarshal([]byte(`{"outbounds": ["a", "b"], `+key+`}`), &options)
		requireError(t, err, text)
	}
}

// ---- 4.2 конфликты ----------------------------------------------------------

func TestRewriteК1TwoChains(t *testing.T) {
	s := newStand(t)
	s.fake("a")
	s.fake("b")
	s.fake("v1")
	s.fake("v2")
	s.selector("g", "v1", "v2")
	s.chain("X", "a", "g")
	s.chain("Y", "b", "g")
	requireError(t, s.rewrite(), "g is needed on chain X position 1 and on chain Y position 1")
}

func TestRewriteК2EntryAndPosition(t *testing.T) {
	s := newStand(t)
	s.fake("N")
	s.fake("A")
	s.fake("B")
	s.chain("X", "N", "B")
	s.chain("Y", "A", "N")
	requireError(t, s.rewrite(), "N is needed on chain X position 0 and on chain Y position 1")
}

func TestRewriteК3HopTypeInConfig(t *testing.T) {
	s := newStand(t)
	s.add("x#0", C.TypeChainHop, &option.ChainHopOutboundOptions{Chain: "x", Outbound: "a"})
	requireError(t, s.rewrite(), "chain-hop", "internal")
}

func TestRewriteК4NestedChain(t *testing.T) {
	s := newStand(t)
	s.fake("a")
	s.fake("b")
	s.fake("c")
	s.chain("inner", "a", "b")
	s.chain("outer", "c", "inner")
	requireError(t, s.rewrite(), "nested chain is only allowed on position 0")

	s = newStand(t)
	s.fake("a")
	s.fake("b")
	s.selector("g", "b", "self")
	s.chain("self", "a", "g")
	requireError(t, s.rewrite(), "contains the chain itself")

	s = newStand(t)
	s.fake("a")
	s.fake("b")
	s.fake("c")
	s.chain("inner", "a", "b")
	s.chain("outer", "inner", "c")
	s.mustRewrite()
	if s.detourOf("c") != "outer#0" {
		t.Fatal("a nested chain on position 0 is allowed")
	}
}

func TestRewriteК5NotDialable(t *testing.T) {
	s := newStand(t)
	s.fake("a")
	s.add("x", typeNoDial, &noDialOptions{})
	s.chain("virt", "a", "x")
	requireError(t, s.rewrite(), "x (nodial) cannot dial through detour")
}

func TestRewriteК5LocalPorts(t *testing.T) {
	port := uint16(41641)
	for name, node := range map[string]struct {
		typeName string
		options  any
		field    string
	}{
		"wireguard":        {C.TypeWireGuard, &option.WireGuardEndpointOptions{ListenPort: 51820}, "listen_port"},
		"tailscale":        {C.TypeTailscale, &option.TailscaleEndpointOptions{ListenPort: port}, "listen_port"},
		"tailscale relay":  {C.TypeTailscale, &option.TailscaleEndpointOptions{RelayServerPort: &port}, "relay_server_port"},
		"openconnect dtls": {C.TypeOpenConnect, &option.OpenConnectEndpointOptions{DTLSLocalPort: port}, "dtls_local_port"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStand(t)
			s.fake("a")
			s.add("n", node.typeName, node.options)
			s.chain("virt", "a", "n")
			requireError(t, s.rewrite(), "node n has "+node.field, "fixed local port")
		})
	}
}

// Т15: a ring through a hop is upstream's circular dependency.
func TestRewriteК1RingThroughOwnDetour(t *testing.T) {
	s := newStand(t)
	s.fake("A", "N")
	s.fake("N")
	s.chain("C", "A", "N")
	requireError(t, s.buildErr(), "circular outbound dependency", "C#0")
}

// ---- 4.3 хоп во время работы -------------------------------------------------

func TestHopХ1Х2NodesAndGroups(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("m1")
	s.fake("m2")
	s.selector("sel", "m1", "m2")
	s.fake("exit")
	s.chain("virt", "in", "sel", "exit")
	s.build()
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "m1[virt#0]", "in[]")
	s.selectIn("sel", "m2")
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "m2[virt#0]", "in[]")
	// И1: one instance per node, no copies.
	for tag, count := range s.created {
		if count != 1 {
			t.Fatalf("И1: %s created %d times", tag, count)
		}
	}
	// T5: a route straight to a taken-over node goes the chain's path.
	assertTrace(t, s.dial("m2"), "m2[virt#0]", "in[]")
}

func TestHopХ3Direct(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("m")
	s.add("direct", C.TypeDirect, &option.DirectOutboundOptions{})
	s.selector("sel", "direct", "m")
	s.fake("exit")
	s.chain("virt", "in", "sel", "exit")
	s.build()
	// Т2: the selector picks direct, nothing leaves past `in`.
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "in[]")

	s = newStand(t)
	s.fake("in")
	s.add("direct", C.TypeDirect, &option.DirectOutboundOptions{})
	s.fake("exit")
	s.chain("virt", "in", "direct", "exit")
	s.build()
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "in[]")
}

func TestHopХ8DirectPickedPerConnection(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.add("direct", C.TypeDirect, &option.DirectOutboundOptions{})
	s.fake("m")
	s.add("pool", C.TypeURLTest, &option.URLTestOutboundOptions{Outbounds: []string{"direct", "m"}})
	s.fake("exit")
	s.chain("virt", "in", "pool", "exit")
	s.build()
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "in[]")
	// The entry hop clears the mark: a group on position 0 dials direct itself.
	s = newStand(t)
	s.add("direct", C.TypeDirect, &option.DirectOutboundOptions{})
	s.fake("m")
	s.add("pool", C.TypeURLTest, &option.URLTestOutboundOptions{Outbounds: []string{"direct", "m"}})
	s.fake("exit")
	s.chain("virt", "pool", "exit")
	s.build()
	assertTrace(t, s.dial("virt"), "exit[virt#0]", "direct[]")
}

func TestHopХ4Х5Disabled(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", "in", "mid", "exit")
	s.build()
	c := s.chainOf("virt")
	c.directDialer = fakeDirectDialer{}
	c.directOnce.Do(func() {})
	requireNoError(t, c.SetPositionEnabled(1, false))
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "in[]")
	requireNoError(t, c.SetPositionEnabled(0, false))
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "direct-fallback")
	if got := strings.Join(c.ChainPath(), ","); got != "exit" {
		t.Fatalf("path %s", got)
	}
	status := c.ChainStatus()
	if !status.Positions[0].Disabled || !status.Positions[1].Disabled || status.Positions[2].Disabled {
		t.Fatalf("status %+v", status)
	}
	requireError(t, c.SetPositionEnabled(3, false), "out of range")
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestHopХ6Block(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.add("block", C.TypeBlock, &option.StubOptions{})
	s.fake("exit")
	s.chain("virt", "in", "block", "exit")
	s.build()
	_, err := s.dialCtx(s.ctx, "virt")
	if err == nil {
		t.Fatal("Х6: block refuses")
	}
}

func TestHopХ7Carrier(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("m1")
	s.selector("sel", "m1")
	s.chain("virt", "in", "sel")
	s.build()
	ctx, record := adapter.ContextWithCarrierRecord(s.ctx)
	if _, err := s.dialCtx(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	// m1 dials its detour virt#0: the hop is not the carrier, the node on its
	// position is.
	if record.Tag != "in" {
		t.Fatalf("Х7: carrier %+v", record)
	}
	ctx, record = adapter.ContextWithCarrierRecord(s.ctx)
	if _, err := s.dialCtx(ctx, "virt"); err != nil {
		t.Fatal(err)
	}
	if record.Tag != "m1" {
		t.Fatalf("Х7: the member the group picked carries the chain dial, got %+v", record)
	}
}

func TestHopUDPPath(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", "in", "mid", "exit")
	s.build()
	ctx, tr := withTrace(s.ctx)
	conn, err := s.chainOf("virt").ListenPacket(ctx, M.ParseSocksaddr("1.1.1.1:53"))
	requireNoError(t, err)
	conn.Close()
	assertTrace(t, tr.items, "udp:exit[virt#1]", "udp:mid[virt#0]", "udp:in[]")
}

func TestHopToggleInterrupt(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.fake("in2")
	s.fake("mid2")
	s.fake("exit2")
	s.chain("virt", "in", "mid", "exit")
	s.chain("virt-rip", "in2", "mid2", "exit2").InterruptExistConnections = true
	s.build()
	open := func(tag string, external bool) net.PacketConn {
		ctx := s.ctx
		if external {
			ctx = interrupt.ContextWithIsExternalConnection(ctx, true)
		}
		ob, _ := s.outbound.Outbound(tag)
		conn, err := ob.ListenPacket(ctx, M.ParseSocksaddr("1.1.1.1:53"))
		requireNoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	alive := func(conn net.PacketConn) bool {
		_, err := conn.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
		return err == nil
	}
	// A node above keeps its own socket through the hop (a WG session): it
	// is internal and goes on any toggle.
	nodeSocket := open("exit", false)
	external := open("virt", true)
	requireNoError(t, s.chainOf("virt").SetPositionEnabled(1, false))
	if alive(nodeSocket) {
		t.Fatal("the socket of a node above must be interrupted on a toggle")
	}
	if !alive(external) {
		t.Fatal("a user connection survives without interrupt_exist_connections")
	}
	externalRip := open("virt-rip", true)
	requireNoError(t, s.chainOf("virt-rip").SetPositionEnabled(1, false))
	if alive(externalRip) {
		t.Fatal("a user connection goes with interrupt_exist_connections")
	}
}

func TestHopTogglePersistence(t *testing.T) {
	store := &fakeDisabledStore{data: map[string][]string{"virt": {"mid", "gone"}}}
	s := newStand(t)
	s.ctx = service.ContextWith[adapter.CacheFile](s.ctx, store)
	s.fake("in")
	s.fake("mid")
	s.fake("exit")
	s.chain("virt", "in", "mid", "exit")
	s.build()
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "in[]")
	requireNoError(t, s.chainOf("virt").SetPositionEnabled(1, true))
	if len(store.LoadChainDisabled("virt")) != 0 {
		t.Fatal("enable persists")
	}
	assertTrace(t, s.dial("virt"), "exit[virt#1]", "mid[virt#0]", "in[]")
}

func TestChainErrorNamesPositions(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.add("x", typeFake, &fakeOptions{DialerOptions: option.DialerOptions{}})
	s.chain("virt", "in", "x")
	s.build()
	status := s.chainOf("virt").ChainStatus()
	if status.Positions[1].Now != "x" || status.Positions[0].IsGroup {
		t.Fatalf("status %+v", status)
	}
}

// С2 rests on this: the interrupt wrapper a hop puts on every conn keeps a
// kernel socket visible, so a node over direct is not taken for a tunnel.
func TestHopWrapperKeepsKernelSocketVisible(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	requireNoError(t, err)
	defer listener.Close()
	go listener.Accept()
	conn, err := net.Dial("tcp", listener.Addr().String())
	requireNoError(t, err)
	defer conn.Close()
	group := interrupt.NewGroup()
	if !lxdatagram.IsKernelSocket(group.NewConn(group.NewConn(conn, false), true)) {
		t.Fatal("tcp through two hop wrappers must stay a kernel socket")
	}
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	requireNoError(t, err)
	defer packetConn.Close()
	if !lxdatagram.IsKernelPacketConn(group.NewPacketConn(packetConn, false)) {
		t.Fatal("udp through a hop wrapper must stay a kernel socket")
	}
}

// Х8 is the chain's only: a node on a position resolving its server through
// a DNS server whose detour is a group that picked direct goes direct, not
// through the hop below (review 2026-11-26).
func TestHopХ8MarkDoesNotReachNodeDials(t *testing.T) {
	for name, position := range map[string]func(*stand){
		"node on the position":  func(s *stand) { s.chain("virt", "in", "n") },
		"node in a group there": func(s *stand) { s.selector("g", "n"); s.chain("virt", "in", "g") },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStand(t)
			s.add("direct", C.TypeDirect, &option.DirectOutboundOptions{})
			s.selector("dns-sel", "direct")
			s.fake("in")
			s.fake("n").Lookup = "dns-sel"
			position(s)
			s.build()
			assertTrace(t, s.dial("virt"), "n[virt#0]", "direct[]", "in[]")
		})
	}
}

// A dial before Start resolves the chain or fails; it never panics.
func TestChainDialBeforeStart(t *testing.T) {
	s := newStand(t)
	s.fake("in")
	s.fake("exit")
	s.chain("virt", "in", "exit")
	s.mustRewrite()
	for _, item := range s.options.Outbounds {
		if item.Type == C.TypeChain {
			chain, err := NewOutbound(s.ctx, nil, log.NewNOPFactory().NewLogger("virt"), item.Tag, *item.Options.(*option.ChainOutboundOptions))
			requireNoError(t, err)
			_, err = chain.DialContext(s.ctx, N.NetworkTCP, M.ParseSocksaddr("target.example:443"))
			requireError(t, err, "outbound not found")
			_, err = chain.ListenPacket(s.ctx, M.ParseSocksaddr("1.1.1.1:53"))
			requireError(t, err, "outbound not found")
		}
	}
}

// К1, rings: the rewrite lets them through and upstream's start-order check
// stops the load (circular outbound dependency, with the hop tags).
func TestRewriteК1RingsStopAtUpstream(t *testing.T) {
	for name, build := range map[string]func(s *stand){
		"[A, Dir{A, X}]": func(s *stand) { s.fake("A"); s.fake("X"); s.selector("Dir", "A", "X"); s.chain("c", "A", "Dir") },
		"[Dir{A}, A]":    func(s *stand) { s.fake("A"); s.selector("Dir", "A"); s.chain("c", "Dir", "A") },
		"[Dir1{A, X}, B, Dir2{A, Y}]": func(s *stand) {
			s.fake("A")
			s.fake("B")
			s.fake("X")
			s.fake("Y")
			s.selector("Dir1", "A", "X")
			s.selector("Dir2", "A", "Y")
			s.chain("c", "Dir1", "B", "Dir2")
		},
		"nested groups": func(s *stand) {
			s.fake("A")
			s.fake("B")
			s.fake("X")
			s.selector("Inner", "A")
			s.selector("Mid", "Inner", "B")
			s.add("Pool", C.TypeURLTest, &option.URLTestOutboundOptions{Outbounds: []string{"X", "Inner"}})
			s.chain("c", "Mid", "Pool")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStand(t)
			build(s)
			requireNoError(t, s.rewrite())
			s = newStand(t)
			build(s)
			requireError(t, s.buildErr(), "circular outbound dependency", "c#")
		})
	}
}

// К1, conflicts: one node or group would need two paths; the rewrite fails
// with a short technical error.
func TestRewriteК1Conflicts(t *testing.T) {
	for name, build := range map[string]func(s *stand){
		"two chains, one node": func(s *stand) {
			s.fake("A")
			s.fake("B")
			s.fake("N")
			s.chain("c1", "A", "N")
			s.chain("c2", "B", "N")
		},
		"two chains, entry and position": func(s *stand) {
			s.fake("A")
			s.fake("N")
			s.fake("X")
			s.chain("c1", "N", "X")
			s.chain("c2", "A", "N")
		},
		"[B, Dir1{A}, Dir2{A}]": func(s *stand) {
			s.fake("A")
			s.fake("B")
			s.fake("X")
			s.selector("Dir1", "A")
			s.selector("Dir2", "A", "X")
			s.chain("c", "B", "Dir1", "Dir2")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStand(t)
			build(s)
			requireError(t, s.rewrite(), "is needed on chain", "one node or group has one path")
		})
	}
	for name, positions := range map[string][]string{"[A, B, A]": {"A", "B", "A"}, "[Dir, A, Dir]": {"Dir", "A", "Dir"}} {
		t.Run(name, func(t *testing.T) {
			s := newStand(t)
			s.fake("A")
			s.fake("B")
			s.fake("X")
			s.selector("Dir", "X")
			s.chain("c", positions...)
			requireError(t, s.rewrite(), "duplicate outbound", "one node or group has one path")
		})
	}
}

// К1: two chains sharing an entry are not a conflict — position 0 is not
// taken over, the entry keeps its one own path.
func TestRewriteК1SharedEntry(t *testing.T) {
	for name, build := range map[string]func(s *stand){
		"[A, B] + [A, C]": func(s *stand) {
			s.fake("A")
			s.fake("B")
			s.fake("C")
			s.chain("x", "A", "B")
			s.chain("y", "A", "C")
		},
		"[proxy, B] + [proxy, C]": func(s *stand) {
			s.fake("A1")
			s.fake("A2")
			s.fake("B")
			s.fake("C")
			s.selector("proxy", "A1", "A2")
			s.chain("x", "proxy", "B")
			s.chain("y", "proxy", "C")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStand(t)
			build(s)
			s.build()
			if s.detourOf("B") != "x#0" || s.detourOf("C") != "y#0" {
				t.Fatalf("B %s, C %s", s.detourOf("B"), s.detourOf("C"))
			}
			if len(s.dial("x")) != 2 || len(s.dial("y")) != 2 {
				t.Fatal("both chains dial through the shared entry")
			}
		})
	}
}

func TestRewriteК1WithoutRingsWorks(t *testing.T) {
	s := newStand(t)
	s.fake("A")
	s.fake("X")
	s.fake("Y")
	s.selector("Dir", "X", "Y")
	// One position reaching a node through two branches is one place.
	s.selector("Pair", "Dir", "X")
	s.chain("c", "A", "Pair")
	s.fake("B")
	s.fake("Z")
	s.chain("other", "B", "Z")
	s.build()
	assertTrace(t, s.dial("c"), "X[c#0]", "A[]")
	assertTrace(t, s.dial("other"), "Z[other#0]", "B[]")
}
