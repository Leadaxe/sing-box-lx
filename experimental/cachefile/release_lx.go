package cachefile

// lx: SPEC 147 — cache.db takeover inside one process. bbolt holds an flock on
// its own open file description, so a Box that is still stuck in Close keeps
// the file locked against the next Box of the same process, and start() fails
// with "initialize cache-file: timeout" after ten one-second attempts. The
// cache-file cleanup is the last one Scope.Close reaches, so a hang in any
// other component keeps the lock forever.
//
// Every started CacheFile registers itself by path. A new instance releases
// every other registered instance with the same path before opening the
// database: the previous one flushes its pending FakeIP metadata, closes the
// database and from then on answers writes with os.ErrClosed and reads with
// zero values. The timeout loop in start() stays as the guard against a
// foreign process holding the file.

import (
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
)

var (
	cacheFileRegistryAccess sync.Mutex
	cacheFileRegistry       = make(map[string]map[*CacheFile]struct{})
)

type cacheFileReleaseState struct {
	access   sync.Mutex
	released atomic.Bool
}

func registerCacheFile(c *CacheFile) {
	cacheFileRegistryAccess.Lock()
	defer cacheFileRegistryAccess.Unlock()
	instances := cacheFileRegistry[c.path]
	if instances == nil {
		instances = make(map[*CacheFile]struct{})
		cacheFileRegistry[c.path] = instances
	}
	instances[c] = struct{}{}
}

func unregisterCacheFile(c *CacheFile) {
	cacheFileRegistryAccess.Lock()
	defer cacheFileRegistryAccess.Unlock()
	instances := cacheFileRegistry[c.path]
	if instances == nil {
		return
	}
	delete(instances, c)
	if len(instances) == 0 {
		delete(cacheFileRegistry, c.path)
	}
}

// takeOverPath releases every other open instance of this process that holds
// the same file. Called by start() before bbolt.Open.
func (c *CacheFile) takeOverPath() {
	cacheFileRegistryAccess.Lock()
	var previous []*CacheFile
	for instance := range cacheFileRegistry[c.path] {
		if instance != c {
			previous = append(previous, instance)
		}
	}
	cacheFileRegistryAccess.Unlock()
	for _, instance := range previous {
		c.logger.Warn("cache-file: taking over ", c.path, " from a previous instance still holding it")
		err := instance.Release()
		if err != nil {
			c.logger.Warn("cache-file: release previous instance: ", err)
		}
	}
}

func (c *CacheFile) isReleased() bool {
	return c.releaseState.released.Load()
}

// Release flushes the pending FakeIP metadata, closes the database and
// unregisters the instance. It is idempotent: later calls return nil. After
// Release, view, batch and update return os.ErrClosed and resetDB never
// reopens the file.
func (c *CacheFile) Release() error {
	c.releaseState.access.Lock()
	defer c.releaseState.access.Unlock()
	if c.isReleased() {
		return nil
	}
	flushErr := c.flushPendingFakeIPMetadata()
	c.dbAccess.Lock()
	c.releaseState.released.Store(true)
	db := c.DB
	c.dbAccess.Unlock()
	unregisterCacheFile(c)
	if db == nil {
		return flushErr
	}
	// Not under dbAccess: bbolt Close waits for in-flight transactions, and
	// view/batch/update take dbAccess only to read the pointer.
	closeErr := db.Close()
	return E.Errors(flushErr, closeErr)
}

func (c *CacheFile) flushPendingFakeIPMetadata() error {
	c.saveMetadataAccess.Lock()
	if c.saveMetadataTimer != nil {
		c.saveMetadataTimer.Stop()
	}
	metadata := c.saveMetadata
	c.saveMetadataAccess.Unlock()
	if metadata == nil {
		return nil
	}
	err := c.FakeIPSaveMetadata(metadata)
	if err != nil {
		return E.Cause(err, "save FakeIP metadata")
	}
	return nil
}

// seedFakeIPMetadata keeps the metadata FakeIPMetadata has just read and
// deleted as the pending value, so Release writes it back even when this
// session never allocates an address. Without it, a session released by a
// takeover (its FakeIP store never closes) loses the pointer and the next one
// resets the pool.
func (c *CacheFile) seedFakeIPMetadata(metadata *adapter.FakeIPMetadata) {
	c.saveMetadataAccess.Lock()
	defer c.saveMetadataAccess.Unlock()
	if c.saveMetadata == nil {
		seeded := *metadata
		c.saveMetadata = &seeded
	}
}

func (c *CacheFile) clearPendingFakeIPMetadata() {
	c.saveMetadataAccess.Lock()
	defer c.saveMetadataAccess.Unlock()
	c.saveMetadata = nil
}
