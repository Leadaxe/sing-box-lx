package cachefile

// lx: SPEC 060 — persisted path labels of lx.sniff_guard. One bucket entry
// per "<carrier tag>|<proto>" key holding the carrier fingerprint and the
// state; the guard drops an entry whose fingerprint no longer matches the
// carrier under that tag.

import (
	"encoding/json"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/common/lxsniffguard"
)

var bucketSniffGuard = []byte("sniff_guard_lx")

var _ lxsniffguard.Cache = (*CacheFile)(nil)

type sniffGuardEntry struct {
	Fingerprint string `json:"fingerprint"`
	State       string `json:"state"`
}

func (c *CacheFile) LoadSniffGuard() map[string]lxsniffguard.CacheEntry {
	entries := make(map[string]lxsniffguard.CacheEntry)
	c.view(func(t *bbolt.Tx) error {
		bucket := c.bucket(t, bucketSniffGuard)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(key, value []byte) error {
			var entry sniffGuardEntry
			if json.Unmarshal(value, &entry) == nil {
				entries[string(key)] = lxsniffguard.CacheEntry{Fingerprint: entry.Fingerprint, State: entry.State}
			}
			return nil
		})
	})
	return entries
}

func (c *CacheFile) StoreSniffGuard(key string, entry lxsniffguard.CacheEntry) error {
	data, err := json.Marshal(sniffGuardEntry{Fingerprint: entry.Fingerprint, State: entry.State})
	if err != nil {
		return err
	}
	return c.batch(func(t *bbolt.Tx) error {
		bucket, err := c.createBucket(t, bucketSniffGuard)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), data)
	})
}
