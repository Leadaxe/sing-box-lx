//go:build with_lxd

package lxd

import (
	"runtime"
	"sync"
	"time"
)

// hostInfoTTL throttles the /proc (or sysctl) reads behind GET /admin/host.
// Shorter than the client directory's 60 s because these numbers change
// constantly, longer than /admin/memory's 200 ms because reading a dozen
// files is dearer than one ReadMemStats — and a client polling for a graph
// asks often.
const hostInfoTTL = 2 * time.Second

// hostSnapshot is GET /admin/host: the machine the daemon runs ON, as opposed
// to /admin/memory (SPEC 065), which describes the daemon process itself.
//
// Every platform returns this same shape; a field this platform cannot measure
// is JSON null. A client checks for null rather than branching on `os` — the
// owner's rule for this endpoint is "take what we can get, report the rest as
// absent".
type hostSnapshot struct {
	Model string `json:"model"`
	// OSFamily is the machine-readable platform: "linux", "darwin",
	// "windows". OS below is the human string the distribution reports
	// ("OpenWrt 23.05.5", "macOS 15.3") and differs per distro, so a client
	// deciding what a null field MEANS — this platform cannot measure it, or
	// the sensor is absent — must branch on this one, never parse that one.
	OSFamily      string `json:"os_family"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	UptimeSeconds int64  `json:"uptime_seconds"`

	CPU     cpuInfo      `json:"cpu"`
	Memory  memoryInfo   `json:"memory"`
	Thermal *thermalInfo `json:"thermal"`
	Disk    diskInfo     `json:"disk"`
	FD      fdInfo       `json:"fd"`

	UpdatedUnix int64 `json:"updated_unix"`
}

// cpuInfo carries both the derived percentages and the load average. The
// percentages come from deltas between two /proc/stat reads, so they are null
// until a second sample exists — a zero would read as "idle", which is a
// different and wrong statement.
type cpuInfo struct {
	Cores int `json:"cores"`
	// UsagePercent and PerCorePercent are nil on the first sample and on
	// platforms with no tick source.
	UsagePercent   *float64  `json:"usage_percent"`
	PerCorePercent []float64 `json:"per_core_percent"`
	// IntervalSeconds is the span the percentages describe. Without it 12.4%
	// over five seconds and over an hour look identical and mean different
	// things. Zero when no percentage was computed.
	IntervalSeconds float64 `json:"interval_seconds"`
	// Load averages are the one part of this struct every platform fills:
	// three numbers, already computed by the kernel, no delta needed.
	Load1  *float64 `json:"load_1"`
	Load5  *float64 `json:"load_5"`
	Load15 *float64 `json:"load_15"`
}

// memoryInfo is the HOST's memory. UsedPercent is computed from Available, not
// Free: a router keeps most of its RAM in page cache, so Free is nearly always
// small and a Free-based percentage screams "full" with 120 MB actually free.
type memoryInfo struct {
	TotalBytes     uint64   `json:"total_bytes"`
	AvailableBytes uint64   `json:"available_bytes"`
	FreeBytes      uint64   `json:"free_bytes"`
	BuffersBytes   *uint64  `json:"buffers_bytes"`
	CachedBytes    *uint64  `json:"cached_bytes"`
	UsedPercent    *float64 `json:"used_percent"`
	SwapTotalBytes uint64   `json:"swap_total_bytes"`
	SwapFreeBytes  uint64   `json:"swap_free_bytes"`
}

// thermalInfo is a whole-struct pointer in hostSnapshot: "this machine has no
// sensors" (a VM, a container, macOS without CGO) is reported as null rather
// than as an empty array, because an empty array reads as "sensors exist and
// said nothing".
type thermalInfo struct {
	Zones      []thermalZone `json:"zones"`
	MaxCelsius float64       `json:"max_celsius"`
}

type thermalZone struct {
	Name    string  `json:"name"`
	Celsius float64 `json:"celsius"`
}

type diskInfo struct {
	Mounts       []mountInfo `json:"mounts"`
	StateDirPath string      `json:"state_dir_path"`
	// MaxUsedPercent ignores read-only filesystems: OpenWrt's squashfs root is
	// permanently 100% full, and a summary that is always red is a summary
	// nobody looks at. The per-mount ReadOnly flag keeps the full picture.
	MaxUsedPercent *float64 `json:"max_used_percent"`
}

type mountInfo struct {
	Path           string  `json:"path"`
	FSType         string  `json:"fstype"`
	TotalBytes     uint64  `json:"total_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
	ReadOnly       bool    `json:"read_only"`
	// HoldsStateDir marks the filesystem the state directory lives on — the
	// one whose exhaustion breaks apply. Set on at most one mount; absent
	// entirely if the match failed (see markStateDirMount).
	HoldsStateDir bool `json:"holds_state_dir,omitempty"`
}

// fdInfo is the one section here that describes the PROCESS as well as the
// host: Open/Limit are the daemon's, System* are the machine's. Both matter
// and they fail differently — a daemon at its own ceiling with the system
// half-empty is a different bug from the system running out.
type fdInfo struct {
	Open        *int `json:"open"`
	Limit       *int `json:"limit"`
	SystemOpen  *int `json:"system_open"`
	SystemLimit *int `json:"system_limit"`
}

// interfaceSnapshot is GET /admin/host/interfaces.
type interfaceSnapshot struct {
	Interfaces []interfaceInfo `json:"interfaces"`
	// IntervalSeconds spans the whole response: one delta window for every
	// interface, because they are all sampled in the same pass.
	IntervalSeconds float64 `json:"interval_seconds"`
	UpdatedUnix     int64   `json:"updated_unix"`
}

// interfaceInfo reports raw counters AND derived rates on purpose. A counter
// survives everything and is what a client should graph; a rate is convenient
// but lies across gaps. Reporting both lets the client choose.
type interfaceInfo struct {
	Name string `json:"name"`
	Up   bool   `json:"up"`
	// Mac is empty for a tunnel device, Addresses empty for an AP interface
	// enslaved to a bridge. Both are valid states, not errors.
	Mac       string   `json:"mac"`
	Addresses []string `json:"addresses"`
	MTU       int      `json:"mtu"`

	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	TxPackets uint64 `json:"tx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`

	RxBytesPerSecond *float64 `json:"rx_bytes_per_second"`
	TxBytesPerSecond *float64 `json:"tx_bytes_per_second"`
}

// cpuTicks is one /proc/stat row: the raw counters a percentage is derived
// from. Kept per-core plus a total.
type cpuTicks struct {
	total uint64 // all fields summed
	idle  uint64 // idle + iowait
}

// hostCache serves both endpoints and owns the delta state. The previous
// sample is what turns monotonic counters into percentages and rates, so it
// lives here rather than in the handler.
type hostCache struct {
	access sync.Mutex
	now    func() time.Time // test seam

	// Test seams for the platform readers, so unit tests never depend on the
	// machine they run on.
	readCPUTicks   func() ([]cpuTicks, bool)
	readStatic     func() staticInfo
	readMemory     func() memoryInfo
	readThermal    func() *thermalInfo
	readMounts     func() []mountInfo
	readFD         func() fdInfo
	readInterfaces func() []rawInterface

	stateDirPath string

	hostTakenAt time.Time
	hostCached  hostSnapshot
	hostValid   bool

	ifTakenAt time.Time
	ifCached  interfaceSnapshot
	ifValid   bool

	// prevTicks is the previous /proc/stat sample; index 0 is the aggregate.
	prevTicks   []cpuTicks
	prevTicksAt time.Time

	// prevIfCounters is keyed by interface name and holds the last raw
	// counters plus the extended byte totals (see extendCounter).
	prevIfCounters map[string]ifCounters
	prevIfAt       time.Time
}

// staticInfo is the part that never changes while the daemon runs; read once
// and remembered, because /proc/device-tree and uname are pointless to re-read
// every two seconds.
type staticInfo struct {
	Model  string
	OS     string
	Kernel string
	Arch   string
}

// rawInterface is what a platform reader produces, before rates and 64-bit
// extension are applied.
type rawInterface struct {
	Name      string
	Up        bool
	Mac       string
	Addresses []string
	MTU       int

	RxBytes   uint64
	TxBytes   uint64
	RxPackets uint64
	TxPackets uint64
	RxErrors  uint64
	TxErrors  uint64
	RxDropped uint64
	TxDropped uint64

	// BytesAre32Bit marks a platform whose byte counters wrap at 4 GB
	// (darwin's if_data64 does, despite the name). Set by the reader; the
	// cache then extends them to 64 bits from the deltas.
	BytesAre32Bit bool
}

// ifCounters is the remembered per-interface state: the raw values last seen
// and the extended totals derived from them.
type ifCounters struct {
	rawRx uint64
	rawTx uint64
	// extRx and extTx are the daemon-maintained 64-bit totals. On a platform
	// with honest 64-bit counters they simply track the raw values.
	extRx uint64
	extTx uint64
}

func newHostCache(stateDirPath string) *hostCache {
	cache := &hostCache{
		now:            time.Now,
		stateDirPath:   stateDirPath,
		prevIfCounters: make(map[string]ifCounters),
	}
	cache.readCPUTicks = readCPUTicks
	cache.readStatic = readStaticInfo
	cache.readMemory = readMemory
	cache.readThermal = readThermal
	cache.readMounts = readMounts
	cache.readFD = readFD
	cache.readInterfaces = readInterfaces
	return cache
}

// Host returns the host snapshot, rebuilding at most once per hostInfoTTL.
func (h *hostCache) Host() hostSnapshot {
	h.access.Lock()
	defer h.access.Unlock()
	if h.hostValid && h.now().Sub(h.hostTakenAt) < hostInfoTTL {
		return h.hostCached
	}
	h.hostCached = h.buildHostLocked()
	h.hostTakenAt = h.now()
	h.hostValid = true
	return h.hostCached
}

// Interfaces returns the interface snapshot under the same TTL.
func (h *hostCache) Interfaces() interfaceSnapshot {
	h.access.Lock()
	defer h.access.Unlock()
	if h.ifValid && h.now().Sub(h.ifTakenAt) < hostInfoTTL {
		return h.ifCached
	}
	h.ifCached = h.buildInterfacesLocked()
	h.ifTakenAt = h.now()
	h.ifValid = true
	return h.ifCached
}

func (h *hostCache) buildHostLocked() hostSnapshot {
	static := h.readStatic()
	snapshot := hostSnapshot{
		Model: static.Model,
		// Filled here rather than in each platform reader: it is the same
		// build constant everywhere, and three copies would be three places
		// to get it wrong.
		OSFamily:      runtime.GOOS,
		OS:            static.OS,
		Kernel:        static.Kernel,
		Arch:          static.Arch,
		UptimeSeconds: readUptimeSeconds(),
		Memory:        h.readMemory(),
		Thermal:       h.readThermal(),
		FD:            h.readFD(),
		UpdatedUnix:   h.now().Unix(),
	}
	snapshot.CPU = h.buildCPULocked()
	snapshot.Disk = h.buildDisk()
	return snapshot
}

// buildCPULocked turns two /proc/stat samples into percentages. The first call
// has nothing to compare against and deliberately reports null rather than a
// zero that would read as "idle".
func (h *hostCache) buildCPULocked() cpuInfo {
	info := cpuInfo{Cores: numCPU()}
	info.Load1, info.Load5, info.Load15 = readLoadAverage()

	ticks, ok := h.readCPUTicks()
	if !ok || len(ticks) == 0 {
		// No tick source on this platform: load averages still stand.
		return info
	}
	now := h.now()
	previous, previousAt := h.prevTicks, h.prevTicksAt
	h.prevTicks, h.prevTicksAt = ticks, now

	if len(previous) != len(ticks) || previousAt.IsZero() {
		// First sample, or the core count changed under us (CPU hotplug).
		return info
	}
	interval := now.Sub(previousAt).Seconds()
	if interval <= 0 {
		return info
	}
	info.IntervalSeconds = interval

	if usage, computed := tickUsage(previous[0], ticks[0]); computed {
		info.UsagePercent = &usage
	}
	if len(ticks) > 1 {
		perCore := make([]float64, 0, len(ticks)-1)
		for index := 1; index < len(ticks); index++ {
			usage, computed := tickUsage(previous[index], ticks[index])
			if !computed {
				// One unreadable core must not void the whole array; report
				// it as zero rather than dropping a position and shifting
				// every later core's identity.
				usage = 0
			}
			perCore = append(perCore, usage)
		}
		info.PerCorePercent = perCore
	}
	return info
}

// tickUsage is the busy fraction between two samples of one CPU's counters.
func tickUsage(previous, current cpuTicks) (float64, bool) {
	totalDelta := current.total - previous.total
	idleDelta := current.idle - previous.idle
	// A counter that went backwards means a reset (or a wrap): no verdict.
	if current.total < previous.total || current.idle < previous.idle || totalDelta == 0 {
		return 0, false
	}
	if idleDelta > totalDelta {
		return 0, false
	}
	usage := float64(totalDelta-idleDelta) / float64(totalDelta) * 100
	return roundTo(usage, 1), true
}

func (h *hostCache) buildDisk() diskInfo {
	mounts := h.readMounts()
	info := diskInfo{Mounts: mounts, StateDirPath: h.stateDirPath}
	markStateDirMount(info.Mounts, h.stateDirPath)

	// The summary deliberately skips read-only filesystems — see the comment
	// on diskInfo.MaxUsedPercent.
	var maximum float64
	var found bool
	for _, mount := range info.Mounts {
		if mount.ReadOnly {
			continue
		}
		if !found || mount.UsedPercent > maximum {
			maximum, found = mount.UsedPercent, true
		}
	}
	if found {
		info.MaxUsedPercent = &maximum
	}
	return info
}

func (h *hostCache) buildInterfacesLocked() interfaceSnapshot {
	raw := h.readInterfaces()
	now := h.now()
	previous, previousAt := h.prevIfCounters, h.prevIfAt
	current := make(map[string]ifCounters, len(raw))

	var interval float64
	if !previousAt.IsZero() {
		interval = now.Sub(previousAt).Seconds()
	}

	interfaces := make([]interfaceInfo, 0, len(raw))
	for _, item := range raw {
		last, seen := previous[item.Name]

		// Extend 32-bit byte counters to 64 bits where the platform needs it.
		extRx := item.RxBytes
		extTx := item.TxBytes
		if item.BytesAre32Bit {
			extRx = extendCounter(last.extRx, last.rawRx, item.RxBytes, seen)
			extTx = extendCounter(last.extTx, last.rawTx, item.TxBytes, seen)
		}
		current[item.Name] = ifCounters{
			rawRx: item.RxBytes, rawTx: item.TxBytes,
			extRx: extRx, extTx: extTx,
		}

		entry := interfaceInfo{
			Name: item.Name, Up: item.Up, Mac: item.Mac,
			Addresses: item.Addresses, MTU: item.MTU,
			RxBytes: extRx, TxBytes: extTx,
			RxPackets: item.RxPackets, TxPackets: item.TxPackets,
			RxErrors: item.RxErrors, TxErrors: item.TxErrors,
			RxDropped: item.RxDropped, TxDropped: item.TxDropped,
		}
		if entry.Addresses == nil {
			// [] rather than null: a client iterating the list must not have
			// to special-case the absence of addresses.
			entry.Addresses = []string{}
		}
		if seen && interval > 0 {
			if rate, ok := perSecond(last.extRx, extRx, interval); ok {
				entry.RxBytesPerSecond = &rate
			}
			if rate, ok := perSecond(last.extTx, extTx, interval); ok {
				entry.TxBytesPerSecond = &rate
			}
		}
		interfaces = append(interfaces, entry)
	}

	h.prevIfCounters, h.prevIfAt = current, now
	if len(previous) == 0 {
		// Nothing to compare against: report no window rather than a
		// meaningless one.
		interval = 0
	}
	return interfaceSnapshot{
		Interfaces:      interfaces,
		IntervalSeconds: interval,
		UpdatedUnix:     now.Unix(),
	}
}

// extendCounter grows a 32-bit counter into a 64-bit total, carrying the
// daemon's own running sum across wraps. Darwin's NET_RT_IFLIST2 reports byte
// counts that wrap every 4 GB — verified against netstat, which agrees modulo
// 2^32 — so a client graphing them would see a drop to zero every few hours.
//
// The honest limits: the total starts at zero when the daemon restarts (it is
// the daemon's counter, not the system's), and a wrap is invisible if more
// than 4 GB passed between two samples — at a 2 s TTL that is ~17 Gbit/s.
func extendCounter(previousTotal, previousRaw, currentRaw uint64, seen bool) uint64 {
	if !seen {
		return currentRaw
	}
	const wrap = uint64(1) << 32
	if currentRaw >= previousRaw {
		return previousTotal + (currentRaw - previousRaw)
	}
	// Went backwards: either a wrap or an interface reset. Treat it as a wrap
	// — the alternative (restarting from currentRaw) throws away the history
	// on every wrap, which is the exact failure being fixed here.
	return previousTotal + (wrap - previousRaw + currentRaw)
}

// perSecond is a rate between two samples of a monotonic counter.
func perSecond(previous, current uint64, interval float64) (float64, bool) {
	if current < previous || interval <= 0 {
		return 0, false
	}
	return roundTo(float64(current-previous)/interval, 1), true
}

// usedPercentOf is the shared "how full" formula, guarding a zero total.
func usedPercentOf(total, available uint64) float64 {
	if total == 0 {
		return 0
	}
	used := total - available
	if available > total {
		used = 0
	}
	return roundTo(float64(used)/float64(total)*100, 1)
}

func roundTo(value float64, decimals int) float64 {
	factor := 1.0
	for index := 0; index < decimals; index++ {
		factor *= 10
	}
	rounded := float64(int64(value*factor+0.5)) / factor
	return rounded
}
