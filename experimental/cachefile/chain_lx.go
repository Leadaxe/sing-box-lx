package cachefile

// lx: SPEC 075 — persistence of the chain position toggle. One bucket entry per
// chain tag holding a JSON array of DISABLED position tags. Tags, not indices:
// config edits that reorder positions keep the right hops disabled, and a tag
// that left the chain is simply ignored on load. Mirrors the selector
// `selected` model: no cache-file in the config → the toggle state is ephemeral.

import (
	"encoding/json"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/adapter"
)

var bucketChainDisabled = []byte("chain_disabled_lx")

var _ adapter.ChainDisabledStore = (*CacheFile)(nil)

func (c *CacheFile) LoadChainDisabled(chainTag string) []string {
	var disabled []string
	c.view(func(t *bbolt.Tx) error {
		bucket := c.bucket(t, bucketChainDisabled)
		if bucket == nil {
			return nil
		}
		data := bucket.Get([]byte(chainTag))
		if len(data) == 0 {
			return nil
		}
		return json.Unmarshal(data, &disabled)
	})
	return disabled
}

func (c *CacheFile) StoreChainDisabled(chainTag string, disabledTags []string) error {
	data, err := json.Marshal(disabledTags)
	if err != nil {
		return err
	}
	return c.batch(func(t *bbolt.Tx) error {
		bucket, err := c.createBucket(t, bucketChainDisabled)
		if err != nil {
			return err
		}
		if len(disabledTags) == 0 {
			return bucket.Delete([]byte(chainTag))
		}
		return bucket.Put([]byte(chainTag), data)
	})
}
