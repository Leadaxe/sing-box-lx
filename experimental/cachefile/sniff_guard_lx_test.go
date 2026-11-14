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

// lx: SPEC 060 — the guard sheds entries of carriers that left the config.
func TestSniffGuardBucketDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	c := startTestCacheFile(t, path, nil)
	require.NoError(t, c.DeleteSniffGuard("relay|tls"), "no bucket yet")
	require.NoError(t, c.StoreSniffGuard("relay|tls", lxsniffguard.CacheEntry{Fingerprint: "vless|relay.example|443", State: "suspect"}))
	require.NoError(t, c.StoreSniffGuard("gone|tls", lxsniffguard.CacheEntry{Fingerprint: "vless|gone.example|443", State: "suspect"}))
	require.NoError(t, c.StoreSniffGuard("gone|quic", lxsniffguard.CacheEntry{Fingerprint: "vless|gone.example|443", State: "clean"}))
	require.NoError(t, c.DeleteSniffGuard("gone|tls", "gone|quic", "absent|tls"))
	require.NoError(t, c.DeleteSniffGuard())
	require.NoError(t, c.Release())

	reopened := startTestCacheFile(t, path, nil)
	require.Equal(t, map[string]lxsniffguard.CacheEntry{
		"relay|tls": {Fingerprint: "vless|relay.example|443", State: "suspect"},
	}, reopened.LoadSniffGuard())
}
