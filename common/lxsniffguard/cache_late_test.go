package lxsniffguard

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// The cache file of a box opens after the guard is created: LoadCache
// attaches it later, keeps a label learnt in between, and transitions from
// then on are stored.
func TestLoadCacheLate(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	carriers := testCarriers()
	carriers["relay2"] = CarrierInfo{Type: "vless", Fingerprint: "vless|relay2.example|443", Encrypted: true}
	g := New(Config{
		Policy:   testPolicy(),
		Nodes:    map[string]NodeInfo{"cover": {SNIMismatch: true}},
		Carriers: carriers,
		Now:      clock.Now,
	})
	require.NotNil(t, g)
	pathRelay2 := Path{CarrierType: "vless", CarrierTag: "relay2"}

	// Learnt before the cache is there: suspect on relay, not stored anywhere.
	_, err := dial(g.Node("cover"), ProtoTLS, pathRelay, sniffedFast)
	require.NoError(t, err)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay))

	cache := &memCache{entries: map[string]CacheEntry{
		"relay|tls":  {Fingerprint: "vless|relay.example|443", State: option.LXSniffGuardClean},
		"relay2|tls": {Fingerprint: "vless|relay2.example|443", State: option.LXSniffGuardClean},
	}}
	g.LoadCache(cache)
	require.Equal(t, option.LXSniffGuardSuspect, stateOf(g, ProtoTLS, pathRelay), "a label learnt in this box wins")
	require.Equal(t, option.LXSniffGuardClean, stateOf(g, ProtoTLS, pathRelay2), "loaded late")

	// A transition after the load is written to the attached cache.
	_, err = dial(g.Node("cover"), ProtoTLS, pathRelay2, sniffedFast)
	require.NoError(t, err)
	require.Equal(t, map[string]CacheEntry{
		"relay2|tls": {Fingerprint: "vless|relay2.example|443", State: option.LXSniffGuardSuspect},
	}, cache.stored)

	var nilGuard *Guard
	nilGuard.LoadCache(cache)
	g.LoadCache(nil)
}
