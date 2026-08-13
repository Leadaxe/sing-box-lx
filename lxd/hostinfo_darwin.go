//go:build with_lxd && darwin

package lxd

import (
	"encoding/binary"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func numCPU() int { return runtime.NumCPU() }

func readStaticInfo() staticInfo {
	return staticInfo{
		Arch:   runtime.GOARCH,
		Model:  sysctlString("hw.model"),
		Kernel: sysctlString("kern.osrelease"),
		OS:     darwinOSName(),
		// A constant rather than null: there is no /etc/os-release here, but
		// the platform IS known, and a filled field spares every client a
		// separate branch. OSLike stays empty — macOS is like nothing else.
		OSID: "macos",
	}
}

// darwinOSName builds "macOS 15.3" from the kernel's product version.
func darwinOSName() string {
	version := sysctlString("kern.osproductversion")
	if version == "" {
		return "macOS"
	}
	return "macOS " + version
}

func sysctlString(name string) string {
	value, err := unix.Sysctl(name)
	if err != nil {
		return ""
	}
	return strings.TrimRight(value, "\x00")
}

func sysctlUint64(name string) (uint64, bool) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, false
	}
	switch len(raw) {
	case 8:
		return binary.LittleEndian.Uint64(raw), true
	case 4:
		return uint64(binary.LittleEndian.Uint32(raw)), true
	}
	return 0, false
}

// readUptimeSeconds derives uptime from the boot timestamp: darwin has no
// /proc/uptime equivalent.
func readUptimeSeconds() int64 {
	raw, err := unix.SysctlRaw("kern.boottime")
	if err != nil || len(raw) < 8 {
		return 0
	}
	// struct timeval { int64 sec; int32 usec; } on 64-bit darwin.
	boot := int64(binary.LittleEndian.Uint64(raw[0:8]))
	if boot <= 0 {
		return 0
	}
	uptime := time.Now().Unix() - boot
	if uptime < 0 {
		return 0
	}
	return uptime
}

// readLoadAverage reads vm.loadavg: struct loadavg { fixpt_t ldavg[3]; long
// fscale; } — three 32-bit scaled integers followed by the scale itself.
// Sizes verified against the SDK on this machine: the struct is 24 bytes with
// fscale at offset 16 (ldavg[3] is 12 bytes plus 4 of padding before the
// 8-byte-aligned long).
const loadAvgSize, loadAvgScaleOffset = 24, 16

func readLoadAverage() (*float64, *float64, *float64) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(raw) < loadAvgSize {
		return nil, nil, nil
	}
	scale := float64(binary.LittleEndian.Uint64(raw[loadAvgScaleOffset:]))
	if scale == 0 {
		return nil, nil, nil
	}
	values := make([]*float64, 3)
	for index := 0; index < 3; index++ {
		offset := index * 4
		value := roundTo(float64(binary.LittleEndian.Uint32(raw[offset:offset+4]))/scale, 2)
		values[index] = &value
	}
	return values[0], values[1], values[2]
}

// readCPUTicks has no pure-Go source on darwin: per-CPU tick counters live
// behind host_processor_info(), a Mach call that needs CGO. Reporting no
// source is honest — the load averages above still answer "is it busy".
func readCPUTicks() ([]cpuTicks, bool) { return nil, false }

// readMemory combines hw.memsize (exact) with vm_stat's page counters. The
// Mach page states do not map onto Linux's Buffers/Cached, so those two stay
// null rather than being filled with a lookalike.
func readMemory() memoryInfo {
	total, ok := sysctlUint64("hw.memsize")
	if !ok {
		return memoryInfo{}
	}
	info := memoryInfo{TotalBytes: total}
	if free, available, got := darwinMemoryPages(); got {
		info.FreeBytes = free
		info.AvailableBytes = available
		percent := usedPercentOf(total, available)
		info.UsedPercent = &percent
	}
	if swapTotal, swapFree, got := darwinSwap(); got {
		info.SwapTotalBytes = swapTotal
		info.SwapFreeBytes = swapFree
	}
	return info
}

// darwinMemoryPages runs vm_stat and derives an "available" figure comparable
// to Linux's MemAvailable: free plus the pages the kernel can reclaim without
// swapping (inactive and purgeable, plus the file-backed part of speculative).
func darwinMemoryPages() (free uint64, available uint64, ok bool) {
	output, spawned := runProvider("vm_stat")
	if !spawned {
		return 0, 0, false
	}
	pageSize := uint64(4096)
	values := make(map[string]uint64, 8)
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			// The header carries the page size: "(page size of 16384 bytes)".
			if _, rest, found := strings.Cut(line, "page size of "); found {
				if size, err := strconv.ParseUint(strings.Fields(rest)[0], 10, 64); err == nil && size > 0 {
					pageSize = size
				}
			}
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		digits := strings.TrimRight(strings.TrimSpace(value), ".")
		parsed, err := strconv.ParseUint(digits, 10, 64)
		if err != nil {
			continue
		}
		values[strings.TrimSpace(key)] = parsed
	}
	freePages, hasFree := values["Pages free"]
	if !hasFree {
		return 0, 0, false
	}
	reclaimable := freePages + values["Pages inactive"] + values["Pages purgeable"] + values["Pages speculative"]
	return freePages * pageSize, reclaimable * pageSize, true
}

func darwinSwap() (total uint64, free uint64, ok bool) {
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil || len(raw) < 24 {
		return 0, 0, false
	}
	// struct xsw_usage { uint64 xsu_total, xsu_avail, xsu_used; ... }
	return binary.LittleEndian.Uint64(raw[0:8]), binary.LittleEndian.Uint64(raw[8:16]), true
}

// readThermal returns nil: darwin exposes temperatures only through SMC/IOKit,
// which needs CGO. Adding a C dependency for a number on a dev workstation is
// not worth it — the router is where thermals matter.
func readThermal() *thermalInfo { return nil }

func readMounts() []mountInfo {
	count, err := unix.Getfsstat(nil, 1 /* MNT_WAIT */)
	if err != nil || count <= 0 {
		return nil
	}
	stats := make([]unix.Statfs_t, count)
	count, err = unix.Getfsstat(stats, 1)
	if err != nil {
		return nil
	}
	var mounts []mountInfo
	for index := 0; index < count && index < len(stats); index++ {
		stat := stats[index]
		blockSize := uint64(stat.Bsize)
		total := stat.Blocks * blockSize
		if total == 0 {
			continue
		}
		fstype := cstring(stat.Fstypename[:])
		if pseudoFilesystems[fstype] {
			continue
		}
		available := stat.Bavail * blockSize
		mounts = append(mounts, mountInfo{
			Path:           cstring(stat.Mntonname[:]),
			FSType:         fstype,
			TotalBytes:     total,
			AvailableBytes: available,
			UsedPercent:    usedPercentOf(total, available),
			ReadOnly:       stat.Flags&unix.MNT_RDONLY != 0,
		})
	}
	return mounts
}

// markStateDirMount matches by device id, not path prefix — same reasoning as
// on Linux (a symlinked state dir must still find its filesystem).
func markStateDirMount(mounts []mountInfo, stateDir string) {
	if stateDir == "" {
		return
	}
	var target unix.Stat_t
	if unix.Stat(stateDir, &target) != nil {
		return
	}
	best := -1
	for index := range mounts {
		var candidate unix.Stat_t
		if unix.Stat(mounts[index].Path, &candidate) != nil {
			continue
		}
		if candidate.Dev != target.Dev {
			continue
		}
		if best < 0 || len(mounts[index].Path) > len(mounts[best].Path) {
			best = index
		}
	}
	if best >= 0 {
		mounts[best].HoldsStateDir = true
	}
}

func readFD() fdInfo {
	var info fdInfo
	// /dev/fd lists the calling process's descriptors. Opening the directory
	// consumes one itself, so discount it.
	if file, err := os.Open("/dev/fd"); err == nil {
		names, readErr := file.Readdirnames(-1)
		file.Close()
		if readErr == nil {
			open := len(names) - 1
			if open < 0 {
				open = 0
			}
			info.Open = &open
		}
	}
	var limit unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &limit) == nil {
		value := int(limit.Cur)
		info.Limit = &value
	}
	if used, ok := sysctlUint64("kern.num_files"); ok {
		value := int(used)
		info.SystemOpen = &value
	}
	if maximum, ok := sysctlUint64("kern.maxfiles"); ok {
		value := int(maximum)
		info.SystemLimit = &value
	}
	return info
}

// netRTIfList2 is NET_RT_IFLIST2 (6): the only RIB flavour carrying
// if_msghdr2, whose ifm_data is an if_data64 with traffic counters.
// x/net/route's own RIBTypeInterface is NET_RT_IFLIST (3), which does not
// include them.
const netRTIfList2 route.RIBType = 6

// if_msghdr2 layout (net/if.h), offsets verified against the SDK headers with
// a C offsetof probe on this machine:
//
//	ifm_index at 12 (u_short), ifm_data at 32, struct size 160.
//
// Inside if_data64 (net/if_var.h): ipackets 24, ierrors 32, opackets 40,
// oerrors 48, ibytes 64, obytes 72, iqdrops 96.
const (
	rtmIfInfo2      = 0x12
	ifm2DataOffset  = 32
	ifm2IndexOffset = 12
	ifDataIPackets  = 24
	ifDataIErrors   = 32
	ifDataOPackets  = 40
	ifDataOErrors   = 48
	ifDataIBytes    = 64
	ifDataOBytes    = 72
	ifDataIQDrops   = 96
	ifDataMinLength = 104
)

// readInterfaces walks the routing information base for interface statistics.
//
// ⚠️ The byte counters here are effectively 32-bit and wrap every 4 GB, even
// though the struct is named if_data64 — verified on a live machine: the
// values agree with `netstat -ib` modulo 2^32, while packets and errors are
// honest 64-bit. BytesAre32Bit tells the cache to extend them (see
// extendCounter); packets and errors are passed through untouched.
func readInterfaces() []rawInterface {
	rib, err := route.FetchRIB(0, netRTIfList2, 0)
	if err != nil {
		return decorateInterfaces(nil)
	}
	return decorateInterfaces(parseIfList2(rib, interfaceNamesByIndex()))
}

func interfaceNamesByIndex() map[int]string {
	names := make(map[int]string)
	system, err := netInterfaces()
	if err != nil {
		return names
	}
	for _, item := range system {
		names[item.Index] = item.Name
	}
	return names
}

// parseIfList2 is the parsing half, split out so a test can feed it a captured
// buffer instead of the machine's live interfaces.
func parseIfList2(rib []byte, names map[int]string) []rawInterface {
	order := binary.LittleEndian
	var interfaces []rawInterface
	for offset := 0; offset+4 <= len(rib); {
		length := int(order.Uint16(rib[offset:]))
		if length < 4 || offset+length > len(rib) {
			break
		}
		message := rib[offset : offset+length]
		offset += length
		if message[3] != rtmIfInfo2 || length < ifm2DataOffset+ifDataMinLength {
			continue
		}
		index := int(order.Uint16(message[ifm2IndexOffset:]))
		name := names[index]
		if name == "" {
			// Without a name the entry cannot be joined to anything useful.
			continue
		}
		data := message[ifm2DataOffset:]
		interfaces = append(interfaces, rawInterface{
			Name:          name,
			RxBytes:       order.Uint64(data[ifDataIBytes:]),
			TxBytes:       order.Uint64(data[ifDataOBytes:]),
			RxPackets:     order.Uint64(data[ifDataIPackets:]),
			TxPackets:     order.Uint64(data[ifDataOPackets:]),
			RxErrors:      order.Uint64(data[ifDataIErrors:]),
			TxErrors:      order.Uint64(data[ifDataOErrors:]),
			RxDropped:     order.Uint64(data[ifDataIQDrops:]),
			BytesAre32Bit: true,
		})
	}
	return interfaces
}

// cstring trims a NUL-terminated fixed-size field to a Go string.
func cstring(values []byte) string {
	for index, value := range values {
		if value == 0 {
			return string(values[:index])
		}
	}
	return string(values)
}
