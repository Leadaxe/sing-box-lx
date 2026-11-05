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
// database: the previous one stops its flush loop, flushes its pending writes,
// closes the database and from then on answers writes with os.ErrClosed and
// reads with zero values. The timeout loop in start() stays as the guard
// against a foreign process holding the file.

import (
	"context"
	"sync"
	"sync/atomic"
)

var (
	cacheFileRegistryAccess sync.Mutex
	cacheFileRegistry       = make(map[string]map[*CacheFile]struct{})
)

type cacheFileReleaseState struct {
	access      sync.Mutex
	released    atomic.Bool
	cancelFlush context.CancelFunc
	flushGroup  sync.WaitGroup
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

// startFlushLoop runs the upstream flush loop under a context Release can
// cancel, so a released instance stops flushing before its database closes,
// also when its scope never closes.
func (c *CacheFile) startFlushLoop(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.releaseState.access.Lock()
	c.releaseState.cancelFlush = cancel
	c.releaseState.access.Unlock()
	c.releaseState.flushGroup.Go(func() {
		c.loopFlush(ctx)
	})
}

// Release stops the flush loop, flushes the pending writes, closes the
// database and unregisters the instance, in the order of the upstream scope
// closers it replaces. It is idempotent: later calls return nil. After
// Release, view, batch and update return os.ErrClosed and resetDB never
// reopens the file.
func (c *CacheFile) Release() error {
	c.releaseState.access.Lock()
	defer c.releaseState.access.Unlock()
	if c.isReleased() {
		return nil
	}
	if c.releaseState.cancelFlush != nil {
		c.releaseState.cancelFlush()
	}
	c.releaseState.flushGroup.Wait()
	c.flushTimer.Stop()
	c.Flush()
	c.dbAccess.Lock()
	c.releaseState.released.Store(true)
	db := c.DB
	c.dbAccess.Unlock()
	unregisterCacheFile(c)
	if db == nil {
		return nil
	}
	// Not under dbAccess: bbolt Close waits for in-flight transactions, and
	// view/batch/update take dbAccess only to read the pointer.
	return db.Close()
}
