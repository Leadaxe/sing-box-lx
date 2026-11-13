package cachefile

import (
	"path/filepath"
	"testing"

	"github.com/sagernet/sing-box/common/lxsniffguard"

	"github.com/stretchr/testify/require"
)

// lx: SPEC 060 — the sniff guard bucket survives a reopen of the file.
func TestSniffGuardBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c := startTestCacheFile(t, path, nil)
	require.Empty(t, c.LoadSniffGuard())
	require.NoError(t, c.StoreSniffGuard("relay|tls", lxsniffguard.CacheEntry{Fingerprint: "vless|relay.example|443", State: "suspect"}))
	require.NoError(t, c.StoreSniffGuard("relay|quic", lxsniffguard.CacheEntry{Fingerprint: "vless|relay.example|443", State: "clean"}))
	require.NoError(t, c.StoreSniffGuard("relay|tls", lxsniffguard.CacheEntry{Fingerprint: "vless|relay.example|443", State: "clean"}))
	require.NoError(t, c.Release())

	reopened := startTestCacheFile(t, path, nil)
	require.Equal(t, map[string]lxsniffguard.CacheEntry{
		"relay|tls":  {Fingerprint: "vless|relay.example|443", State: "clean"},
		"relay|quic": {Fingerprint: "vless|relay.example|443", State: "clean"},
	}, reopened.LoadSniffGuard())
}
