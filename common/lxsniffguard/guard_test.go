package lxsniffguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

var errRefused = errors.New("connection refused")

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type memCache struct {
	entries map[string]CacheEntry
	stored  map[string]CacheEntry
}

func (c *memCache) LoadSniffGuard() map[string]CacheEntry { return c.entries }

func (c *memCache) StoreSniffGuard(key string, entry CacheEntry) error {
	if c.stored == nil {
		c.stored = make(map[string]CacheEntry)
	}
	c.stored[key] = entry
	return nil
}

func testDetourTypes() map[string]string {
	return map[string]string{
		option.LXSniffGuardDirect:   option.LXSniffGuardClean,
		"ssh":                       option.LXSniffGuardClean,
		"socks":                     option.LXSniffGuardClean,
		option.LXSniffGuardWildcard: option.LXSniffGuardSuspect,
	}
}

func testPolicy() option.LXSniffGuardResolved {
	quicTypes := testDetourTypes()
	quicTypes["socks"] = option.LXSniffGuardNever
	return option.LXSniffGuardResolved{
		TLS: option.LXSniffGuardSectionResolved{
			Method:      option.LXSniffGuardTLSRecordFragment,
			Delay:       300 * time.Millisecond,
			Persist:     true,
			DetourTypes: testDetourTypes(),
		},
		QUIC: option.LXSniffGuardSectionResolved{
			Method:      option.LXSniffGuardQUICDecoy,
			Delay:       300 * time.Millisecond,
			Persist:     true,
			DetourTypes: quicTypes,
		},
	}
}

var (
	pathDirect = Path{Direct: true}
	pathRelay  = Path{CarrierType: "vless", CarrierTag: "relay"}
	pathSocks  = Path{CarrierType: "socks", CarrierTag: "socks-out"}
)

func testCarriers() map[string]CarrierInfo {
	return map[string]CarrierInfo{
		"relay":     {Type: "vless", Fingerprint: "vless|relay.example|443", Encrypted: true},
		"socks-out": {Type: "socks", Fingerprint: "socks|10.0.0.1|1080"},
	}
}

func newTestGuard(t *testing.T, cache Cache) (*Guard, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	config := Config{
		Policy: testPolicy(),
		Nodes: map[string]NodeInfo{
			"cover":  {Type: "vless", SNIMismatch: true},
			"cover2": {Type: "vless", SNIMismatch: true},
			"own":    {Type: "vless"},
		},
		Carriers: testCarriers(),
		Now:      clock.Now,
	}
	if cache != nil {
		config.Cache = cache
	}
	g := New(config)
	require.NotNil(t, g)
	return g, clock
}

// dial runs one Node.Dial whose handshake marks the attempt on path and
// whose outcome is outcome(protect). It returns the decisions of the
// attempts in order.
func dial(node *Node, proto Proto, path Path, outcome func(protect bool) error) ([]bool, error) {
	var decisions []bool
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		decision := AttemptFromContext(ctx).Decide(proto, path, true)
		decisions = append(decisions, decision.Protect)
		return outcome(decision.Protect)
	})
	return decisions, err
}

func sniffedFast(protect bool) error {
	if protect {
		return nil
	}
	return errRefused
}

func sniffedSilent(protect bool) error {
	if protect {
		return nil
	}
	return timeoutError{}
}

// brokenByProtection is a path where protection itself fails (DPI on the
// split records).
func brokenByProtection(protect bool) error {
	if protect {
		return errRefused
	}
	return nil
}

func deadFast(bool) error   { return errRefused }
func deadSilent(bool) error { return timeoutError{} }
func alive(bool) error      { return nil }

func stateOf(g *Guard, proto Proto, path Path) string {
	g.access.Lock()
	defer g.access.Unlock()
	key, start, ok := g.resolve(proto, path)
	if !ok {
		return option.LXSniffGuardNever
	}
	return g.label(key, start).state
}

func TestNilSafe(t *testing.T) {
	var g *Guard
	require.Nil(t, g.Node("cover"))
	g.NetworkChanged()
	require.False(t, g.Override())
	var n *Node
	calls := 0
	require.ErrorIs(t, n.Dial(context.Background(), func(ctx context.Context) error {
		calls++
		require.Nil(t, AttemptFromContext(ctx))
		return errRefused
	}), errRefused)
	require.Equal(t, 1, calls)
	require.Equal(t, Decision{}, n.Peek(ProtoTLS, pathDirect))
	n.Report(ProtoTLS, pathDirect, false, false)
	var a *Attempt
	require.Equal(t, Decision{}, a.Decide(ProtoTLS, pathDirect, true))
	require.Nil(t, New(Config{}))
	require.Nil(t, FromContext(context.Background()))
}

func TestContext(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	require.Same(t, g, FromContext(ContextWith(context.Background(), g)))
	require.Nil(t, g.Node("unknown"))
}

func TestLabelTransitions(t *testing.T) {
	for _, test := range []struct {
		name      string
		path      Path
		outcome   func(bool) error
		decisions []bool
		ok        bool
		after     string
		next      []bool
	}{
		{"clean to suspect", pathDirect, sniffedFast, []bool{false, true}, true, option.LXSniffGuardSuspect, []bool{true}},
		{"suspect to clean", pathRelay, brokenByProtection, []bool{true, false}, true, option.LXSniffGuardClean, []bool{false}},
		{"dead on clean", pathDirect, deadFast, []bool{false, true}, false, option.LXSniffGuardClean, []bool{false, true}},
		{"dead on suspect", pathRelay, deadFast, []bool{true, false}, false, option.LXSniffGuardSuspect, []bool{true}},
		{"alive", pathRelay, alive, []bool{true}, true, option.LXSniffGuardSuspect, []bool{true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, clock := newTestGuard(t, nil)
			node := g.Node("cover")
			decisions, err := dial(node, ProtoTLS, test.path, test.outcome)
			require.Equal(t, test.decisions, decisions)
			require.Equal(t, test.ok, err == nil)
			require.Equal(t, test.after, stateOf(g, ProtoTLS, test.path))
			clock.Advance(time.Minute)
			decisions, _ = dial(node, ProtoTLS, test.path, test.outcome)
			require.Equal(t, test.next, decisions)
			require.Equal(t, test.after, stateOf(g, ProtoTLS, test.path))
		})
	}
}

func TestProtocolsIndependent(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	_, err := dial(g.Node("cover"), ProtoTLS, pathDirect, sniffedFast)
	require.NoError(t, err)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathDirect))
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoQUIC, pathDirect))
	decision := g.Node("cover2").Peek(ProtoQUIC, pathDirect)
	require.False(t, decision.Protect)
	decision = g.Node("cover2").Peek(ProtoTLS, pathDirect)
	require.Equal(t, Decision{Protect: true, Method: option.LXSniffGuardTLSRecordFragment, Delay: 300 * time.Millisecond}, decision)
}

func TestPendingPairOnTimeout(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	decisions, err := dial(node, ProtoTLS, pathDirect, sniffedSilent)
	require.Equal(t, []bool{false}, decisions)
	require.Error(t, err)
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathDirect))

	// Another node on the path goes by the label.
	decisions, _ = dial(g.Node("cover2"), ProtoTLS, pathDirect, alive)
	require.Equal(t, []bool{false}, decisions)

	clock.Advance(time.Minute)
	decisions, err = dial(node, ProtoTLS, pathDirect, sniffedSilent)
	require.Equal(t, []bool{true}, decisions)
	require.NoError(t, err)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathDirect))
}

func TestPendingPairLifetime(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	_, err := dial(node, ProtoTLS, pathDirect, sniffedSilent)
	require.Error(t, err)
	clock.Advance(pairLifetime)
	decisions, err := dial(node, ProtoTLS, pathDirect, alive)
	require.Equal(t, []bool{false}, decisions)
	require.NoError(t, err)
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathDirect))
}

func TestPendingPairOneTaker(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	_, err := dial(node, ProtoTLS, pathDirect, sniffedSilent)
	require.Error(t, err)
	clock.Advance(time.Second)

	var outer, inner []bool
	err = node.Dial(context.Background(), func(ctx context.Context) error {
		outer = append(outer, AttemptFromContext(ctx).Decide(ProtoTLS, pathDirect, true).Protect)
		// A parallel dial of the same node while the pair is taken.
		var innerErr error
		inner, innerErr = dial(node, ProtoTLS, pathDirect, sniffedSilent)
		require.Error(t, innerErr)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true}, outer)
	require.Equal(t, []bool{false}, inner)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathDirect))
	g.access.Lock()
	require.Nil(t, node.pending)
	g.access.Unlock()
}

func TestPendingPairNotClosedByEarlierDial(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		require.False(t, AttemptFromContext(ctx).Decide(ProtoTLS, pathDirect, true).Protect)
		clock.Advance(time.Second)
		// A later dial times out and leaves a pair while this one is running.
		_, innerErr := dial(node, ProtoTLS, pathDirect, sniffedSilent)
		require.Error(t, innerErr)
		return nil
	})
	require.NoError(t, err)
	decisions, err := dial(node, ProtoTLS, pathDirect, sniffedSilent)
	require.Equal(t, []bool{true}, decisions)
	require.NoError(t, err)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathDirect))
}

func TestPendingPairClosedByOtherOutcome(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	_, err := dial(node, ProtoTLS, pathDirect, sniffedSilent)
	require.Error(t, err)
	clock.Advance(time.Second)
	// A marked attempt on another key closes the pair.
	_, err = dial(node, ProtoQUIC, pathDirect, alive)
	require.NoError(t, err)
	decisions, _ := dial(node, ProtoTLS, pathDirect, alive)
	require.Equal(t, []bool{false}, decisions)
}

func TestUnmarkedAttemptIgnored(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	node := g.Node("cover")
	for _, test := range []struct {
		name string
		do   func(ctx context.Context) error
	}{
		{"no handshake", func(ctx context.Context) error {
			return errRefused
		}},
		{"not applicable", func(ctx context.Context) error {
			require.Equal(t, Decision{}, AttemptFromContext(ctx).Decide(ProtoTLS, pathDirect, false))
			return errRefused
		}},
		{"timeout without handshake", func(ctx context.Context) error {
			return timeoutError{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := node.Dial(context.Background(), func(ctx context.Context) error {
				calls++
				return test.do(ctx)
			})
			require.Error(t, err)
			require.Equal(t, 1, calls)
			g.access.Lock()
			require.Nil(t, node.pending)
			g.access.Unlock()
		})
	}
	decisions, _ := dial(node, ProtoTLS, pathDirect, alive)
	require.Equal(t, []bool{false}, decisions)
}

func TestNeverAndUnknownPath(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	node := g.Node("cover")
	for _, test := range []struct {
		name  string
		proto Proto
		path  Path
	}{
		{"never", ProtoQUIC, pathSocks},
		{"unknown", ProtoTLS, Path{CarrierType: "vless"}},
		{"unknown quic", ProtoQUIC, Path{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := node.Dial(context.Background(), func(ctx context.Context) error {
				calls++
				require.Equal(t, Decision{}, AttemptFromContext(ctx).Decide(test.proto, test.path, true))
				return errRefused
			})
			require.Error(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, Decision{}, node.Peek(test.proto, test.path))
		})
	}
	g.access.Lock()
	require.Empty(t, g.labels)
	g.access.Unlock()
}

func TestSectionOff(t *testing.T) {
	policy := testPolicy()
	policy.QUIC = option.LXSniffGuardSectionResolved{}
	g := New(Config{Policy: policy, Nodes: map[string]NodeInfo{"cover": {SNIMismatch: true}}})
	require.NotNil(t, g)
	require.Equal(t, Decision{}, g.Node("cover").Peek(ProtoQUIC, pathRelay))
	require.True(t, g.Node("cover").Peek(ProtoTLS, pathRelay).Protect)
}

func TestOverrideInDecision(t *testing.T) {
	policy := testPolicy()
	policy.Override = true
	g := New(Config{Policy: policy, Nodes: map[string]NodeInfo{"cover": {SNIMismatch: true}}})
	require.True(t, g.Override())
	require.Equal(t, Decision{Override: true}, g.Node("cover").Peek(ProtoTLS, pathDirect))
	require.Equal(t, Decision{}, g.Node("cover").Peek(ProtoQUIC, pathSocks))
	err := g.Node("cover").Dial(context.Background(), func(ctx context.Context) error {
		require.True(t, AttemptFromContext(ctx).Override())
		return nil
	})
	require.NoError(t, err)
}

func TestDescentProbeWindow(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	decisions, _ := dial(node, ProtoTLS, pathRelay, deadFast)
	require.Equal(t, []bool{true, false}, decisions)

	// Within the window: neither a retry nor a pending pair, for any node.
	clock.Advance(5 * time.Minute)
	decisions, _ = dial(g.Node("cover2"), ProtoTLS, pathRelay, deadFast)
	require.Equal(t, []bool{true}, decisions)
	decisions, _ = dial(node, ProtoTLS, pathRelay, deadSilent)
	require.Equal(t, []bool{true}, decisions)
	decisions, _ = dial(node, ProtoTLS, pathRelay, deadFast)
	require.Equal(t, []bool{true}, decisions)

	clock.Advance(5 * time.Minute)
	decisions, _ = dial(node, ProtoTLS, pathRelay, deadFast)
	require.Equal(t, []bool{true, false}, decisions)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay))
}

func TestMatchingSNINode(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	own := g.Node("own")

	// clean: no retry, no pending pair, the label stays.
	decisions, err := dial(own, ProtoTLS, pathDirect, sniffedFast)
	require.Equal(t, []bool{false}, decisions)
	require.Error(t, err)
	decisions, _ = dial(own, ProtoTLS, pathDirect, sniffedSilent)
	require.Equal(t, []bool{false}, decisions)
	decisions, _ = dial(own, ProtoTLS, pathDirect, sniffedSilent)
	require.Equal(t, []bool{false}, decisions)
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathDirect))

	// suspect: failed with protection, succeeded without → personal opt-out.
	decisions, err = dial(own, ProtoTLS, pathRelay, brokenByProtection)
	require.Equal(t, []bool{true, false}, decisions)
	require.NoError(t, err)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay))
	decisions, _ = dial(own, ProtoTLS, pathRelay, alive)
	require.Equal(t, []bool{false}, decisions)
	require.False(t, own.Peek(ProtoTLS, pathRelay).Protect)
	decisions, _ = dial(g.Node("cover"), ProtoTLS, pathRelay, alive)
	require.Equal(t, []bool{true}, decisions)

	// The per-node window holds the reverse probe back.
	clock.Advance(5 * time.Minute)
	decisions, _ = dial(own, ProtoTLS, pathRelay, sniffedFast)
	require.Equal(t, []bool{false}, decisions)

	// After the window the reverse pair removes the opt-out.
	clock.Advance(5 * time.Minute)
	decisions, err = dial(own, ProtoTLS, pathRelay, sniffedFast)
	require.Equal(t, []bool{false, true}, decisions)
	require.NoError(t, err)
	decisions, _ = dial(own, ProtoTLS, pathRelay, alive)
	require.Equal(t, []bool{true}, decisions)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay))
}

func TestMatchingSNINodeTimeoutPair(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	own := g.Node("own")
	silentWithProtection := func(protect bool) error {
		if protect {
			return timeoutError{}
		}
		return nil
	}
	decisions, err := dial(own, ProtoTLS, pathRelay, silentWithProtection)
	require.Equal(t, []bool{true}, decisions)
	require.Error(t, err)
	clock.Advance(time.Second)
	decisions, err = dial(own, ProtoTLS, pathRelay, silentWithProtection)
	require.Equal(t, []bool{false}, decisions)
	require.NoError(t, err)
	decisions, _ = dial(own, ProtoTLS, pathRelay, alive)
	require.Equal(t, []bool{false}, decisions)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay))
}

func TestNetworkChanged(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	cover := g.Node("cover")
	own := g.Node("own")
	_, err := dial(cover, ProtoTLS, pathDirect, sniffedFast)
	require.NoError(t, err)
	_, err = dial(cover, ProtoTLS, pathSocks, sniffedFast)
	require.NoError(t, err)
	_, err = dial(cover, ProtoTLS, pathRelay, brokenByProtection)
	require.NoError(t, err)
	// A personal opt-out on the (now suspect) direct path and a pending pair
	// on the socks path.
	_, err = dial(own, ProtoTLS, pathDirect, brokenByProtection)
	require.NoError(t, err)
	clock.Advance(time.Second)
	_, err = dial(cover2(g), ProtoTLS, pathSocks, deadSilent)
	require.Error(t, err)

	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathDirect))
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathSocks))
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay))

	g.NetworkChanged()
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathDirect))
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathSocks))
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay))
	g.access.Lock()
	require.Empty(t, own.optOut)
	require.Nil(t, cover2(g).pending)
	g.access.Unlock()
}

func cover2(g *Guard) *Node { return g.Node("cover2") }

func TestNetworkChangedDropsStaleOutcome(t *testing.T) {
	g, _ := newTestGuard(t, nil)
	node := g.Node("cover")
	calls := 0
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		calls++
		AttemptFromContext(ctx).Decide(ProtoTLS, pathDirect, true)
		g.NetworkChanged()
		return errRefused
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestReportAndLate(t *testing.T) {
	g, clock := newTestGuard(t, nil)
	node := g.Node("cover")
	hysteria := func(outcomeOK func(bool) bool) {
		var protect bool
		err := node.Dial(context.Background(), func(ctx context.Context) error {
			attempt := AttemptFromContext(ctx)
			protect = attempt.Decide(ProtoQUIC, pathDirect, true).Protect
			attempt.Late()
			return nil
		})
		require.NoError(t, err)
		node.Report(ProtoQUIC, pathDirect, protect, outcomeOK(protect))
	}
	sniffed := func(protect bool) bool { return protect }
	hysteria(sniffed) // fails without protection: pending pair
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoQUIC, pathDirect))
	clock.Advance(time.Second)
	hysteria(sniffed) // succeeds with it: the pair completes
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoQUIC, pathDirect))

	// Late with a fast error: no retry.
	calls := 0
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		calls++
		attempt := AttemptFromContext(ctx)
		attempt.Decide(ProtoQUIC, pathDirect, true)
		require.Same(t, node, attempt.Late())
		return errRefused
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestPersist(t *testing.T) {
	cache := &memCache{entries: map[string]CacheEntry{
		"relay|tls":      {Fingerprint: "vless|relay.example|443", State: option.LXSniffGuardClean},
		"relay|quic":     {Fingerprint: "vless|other.example|443", State: option.LXSniffGuardClean},
		"socks-out|tls":  {Fingerprint: "socks|10.0.0.1|1080", State: option.LXSniffGuardSuspect},
		"gone|tls":       {Fingerprint: "vless|gone|443", State: option.LXSniffGuardClean},
		"socks-enc|quic": {Fingerprint: "socks|tls|1080", State: option.LXSniffGuardSuspect},
		"|tls":           {State: option.LXSniffGuardSuspect},
	}}
	clock := &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	carriers := testCarriers()
	// An encrypted carrier of a type that is `never` for quic.
	carriers["socks-enc"] = CarrierInfo{Type: "socks", Fingerprint: "socks|tls|1080", Encrypted: true}
	g := New(Config{
		Policy:   testPolicy(),
		Nodes:    map[string]NodeInfo{"cover": {SNIMismatch: true}},
		Carriers: carriers,
		Cache:    cache,
		Now:      clock.Now,
	})
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay))
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoQUIC, pathRelay), "foreign fingerprint")
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathSocks), "unencrypted carrier")
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathDirect))
	g.access.Lock()
	_, loaded := g.labels[labelKey{tag: "socks-enc", proto: ProtoQUIC}]
	g.access.Unlock()
	require.False(t, loaded, "never path")

	node := g.Node("cover")
	_, err := dial(node, ProtoTLS, pathRelay, sniffedFast)
	require.NoError(t, err)
	_, err = dial(node, ProtoTLS, pathDirect, sniffedFast)
	require.NoError(t, err)
	_, err = dial(node, ProtoTLS, pathSocks, sniffedFast)
	require.NoError(t, err)
	require.Equal(t, map[string]CacheEntry{
		"relay|tls": {Fingerprint: "vless|relay.example|443", State: option.LXSniffGuardSuspect},
	}, cache.stored)

	// A new box reads it back.
	g2 := New(Config{Policy: testPolicy(), Carriers: carriers, Cache: &memCache{entries: cache.stored}})
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g2, ProtoTLS, pathRelay))
}

func TestPersistOff(t *testing.T) {
	policy := testPolicy()
	policy.TLS.Persist = false
	cache := &memCache{entries: map[string]CacheEntry{
		"relay|tls": {Fingerprint: "vless|relay.example|443", State: option.LXSniffGuardClean},
	}}
	g := New(Config{Policy: policy, Nodes: map[string]NodeInfo{"cover": {SNIMismatch: true}}, Carriers: testCarriers(), Cache: cache})
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay))
	_, err := dial(g.Node("cover"), ProtoTLS, pathRelay, brokenByProtection)
	require.NoError(t, err)
	require.Empty(t, cache.stored)
}

func TestSNIMismatch(t *testing.T) {
	for _, test := range []struct {
		server, sni string
		mismatch    bool
	}{
		{"example.com", "", false},
		{"example.com", "example.com", false},
		{"Example.COM", "example.com.", false},
		{"example.com.", "EXAMPLE.com", false},
		{"example.com", "www.example.com", true},
		{"example.com", "cover.example.org", true},
		{"1.2.3.4", "example.com", true},
		{"1.2.3.4", "", false},
		{"2001:db8::1", "example.com", true},
		{"[2001:db8::1]", "", false},
		{"[2001:db8::1]", "2001:db8:0::1", false},
	} {
		require.Equal(t, test.mismatch, SNIMismatch(test.server, test.sni), "%s / %s", test.server, test.sni)
	}
}
