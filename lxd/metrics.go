//go:build with_lxd

package lxd

import (
	"runtime"
	"sync"
	"time"
)

// rssUnsupported is the value reported for an RSS field this platform cannot
// measure. A negative sentinel rather than 0 or a missing field: 0 would read
// as "the process uses no memory", and an absent key would make the client
// guess whether the daemon is old or the platform is limited.
const rssUnsupported = -1

// memorySnapshotTTL throttles ReadMemStats. The call stops the world — tens of
// microseconds on a healthy process, but not free, and NumGoroutine takes the
// scheduler lock on top. A client polling for a graph gets a cached answer
// instead of burning the daemon.
//
// The floor mirrors minSubscribeInterval in daemon/started_service.go, added
// after a launcher sent an interval in the wrong unit and spun a core at 100%
// CPU. Same class of bug, same defense — invisible to a well-behaved client.
const memorySnapshotTTL = 200 * time.Millisecond

// memorySnapshot is the process-wide memory view served by GET /admin/memory.
// Every size is raw bytes with the unit in the field name: the consumer is a
// program drawing a graph, not an operator reading a page, so formatted
// strings like "40.0 MiB" (what box's debug endpoint serves) would only have
// to be parsed back.
type memorySnapshot struct {
	HeapInuseBytes  uint64 `json:"heap_inuse_bytes"`
	HeapIdleBytes   uint64 `json:"heap_idle_bytes"`
	StackInuseBytes uint64 `json:"stack_inuse_bytes"`
	SysBytes        uint64 `json:"sys_bytes"`
	// InuseBytes is Stack+Heap+Idle-Released — the same formula libbox's OOM
	// killer watches, so a number seen here matches the one that triggers it.
	InuseBytes     uint64 `json:"inuse_bytes"`
	Goroutines     int    `json:"goroutines"`
	NumGC          uint32 `json:"num_gc"`
	GCPauseTotalNs uint64 `json:"gc_pause_total_ns"`
	// RSSCurrentBytes is what the process resides in RAM right now;
	// RSSPeakBytes is the high-water mark since start. Both are reported
	// because the peak alone (the only one box's debug endpoint exposes, under
	// the ambiguous name "rss") never decreases and hides leaks. Either may be
	// rssUnsupported on platforms without a pure-Go source.
	RSSCurrentBytes int64 `json:"rss_current_bytes"`
	RSSPeakBytes    int64 `json:"rss_peak_bytes"`
}

// memoryCache serves memorySnapshot under the TTL above.
type memoryCache struct {
	access  sync.Mutex
	now     func() time.Time // test seam
	takenAt time.Time
	cached  memorySnapshot
	valid   bool
}

func newMemoryCache() *memoryCache {
	return &memoryCache{now: time.Now}
}

// Snapshot is nil-safe: tests build a controller as a struct literal, and an
// uncached read is correct — just unthrottled.
func (m *memoryCache) Snapshot() memorySnapshot {
	if m == nil {
		return takeMemorySnapshot()
	}
	m.access.Lock()
	defer m.access.Unlock()
	if m.valid && m.now().Sub(m.takenAt) < memorySnapshotTTL {
		return m.cached
	}
	m.cached = takeMemorySnapshot()
	m.takenAt = m.now()
	m.valid = true
	return m.cached
}

func takeMemorySnapshot() memorySnapshot {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return memorySnapshot{
		HeapInuseBytes:  stats.HeapInuse,
		HeapIdleBytes:   stats.HeapIdle - stats.HeapReleased,
		StackInuseBytes: stats.StackInuse,
		SysBytes:        stats.Sys,
		InuseBytes:      stats.StackInuse + stats.HeapInuse + stats.HeapIdle - stats.HeapReleased,
		Goroutines:      runtime.NumGoroutine(),
		NumGC:           stats.NumGC,
		GCPauseTotalNs:  stats.PauseTotalNs,
		RSSCurrentBytes: currentRSS(),
		RSSPeakBytes:    peakRSS(),
	}
}

// statsSnapshot is GET /admin/stats: the core's traffic counters and uptime.
// Every field is a pointer so "no core" is reported as JSON null rather than
// as zero — an idle daemon has not transferred 0 bytes, it has no core to ask.
// The endpoint stays 200 in that case by design: it describes the daemon, and
// answering "there is no core" is exactly what a client polls it for.
type statsSnapshot struct {
	CoreUptimeSeconds *int64 `json:"core_uptime_seconds"`
	UplinkTotal       *int64 `json:"uplink_total"`
	DownlinkTotal     *int64 `json:"downlink_total"`
	Connections       *int   `json:"connections"`
}
