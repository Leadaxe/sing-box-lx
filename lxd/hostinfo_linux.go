//go:build with_lxd && linux

package lxd

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func numCPU() int { return runtime.NumCPU() }

// readStaticInfo fills the fields that cannot change while the daemon runs.
func readStaticInfo() staticInfo {
	info := staticInfo{Arch: runtime.GOARCH}
	info.Model = firstNonEmptyFile(
		// OpenWrt writes a human-readable board name here at boot; the
		// device-tree node is the generic fallback and is NUL-terminated.
		"/tmp/sysinfo/model",
		"/proc/device-tree/model",
	)
	info.Kernel = unameRelease()
	info.OS, info.OSID, info.OSLike = readOSRelease()
	return info
}

// readOSRelease fills the distribution fields from /etc/os-release, the
// systemd-era standard that OpenWrt also ships.
//
// os-release comes FIRST and openwrt_release is only a fallback, because the
// standard file carries machine-readable keys (ID, ID_LIKE) that the OpenWrt
// one does not, and its PRETTY_NAME is cleaner: on a RouteRich router
// PRETTY_NAME is "RouteRich 24.10.5" while DISTRIB_DESCRIPTION is
// "RouteRich 24.10.5 r29087-d9c5716d1d RR-3.9.0" — build revision and vendor
// suffix included.
func readOSRelease() (name string, id string, idLike []string) {
	if content, err := os.ReadFile("/etc/os-release"); err == nil {
		name, id, idLike = parseOSRelease(string(content))
	}
	if name == "" {
		// Older or trimmed images may ship only OpenWrt's own file. It has no
		// ID at all, so a distribution known only through this path gets a
		// name and nothing else — better than nothing, and honest.
		if content, err := os.ReadFile("/etc/openwrt_release"); err == nil {
			name, _, _ = parseOSRelease(string(content))
		}
	}
	return name, id, idLike
}

// parseOSRelease reads the KEY=value shape shared by /etc/os-release and
// /etc/openwrt_release. Split out so it can be tested on captured content.
func parseOSRelease(content string) (name string, id string, idLike []string) {
	for _, line := range strings.Split(content, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		value = strings.Trim(value, "\"' ")
		if value == "" {
			continue
		}
		switch key {
		case "PRETTY_NAME", "DISTRIB_DESCRIPTION":
			if name == "" {
				name = value
			}
		case "ID":
			id = value
		case "ID_LIKE":
			// The standard says space-separated; a slice spares every client
			// the same split.
			idLike = strings.Fields(value)
		}
	}
	return name, id, idLike
}

func unameRelease() string {
	var buffer syscall.Utsname
	if syscall.Uname(&buffer) != nil {
		return ""
	}
	return utsString(buffer.Release[:])
}

func firstNonEmptyFile(paths ...string) string {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// device-tree strings carry a trailing NUL.
		text := strings.TrimSpace(strings.TrimRight(string(content), "\x00"))
		if text != "" {
			return text
		}
	}
	return ""
}

func readUptimeSeconds() int64 {
	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(seconds)
}

func readLoadAverage() (*float64, *float64, *float64) {
	content, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, nil, nil
	}
	return parseLoadAverage(string(content))
}

func parseLoadAverage(content string) (*float64, *float64, *float64) {
	fields := strings.Fields(content)
	if len(fields) < 3 {
		return nil, nil, nil
	}
	values := make([]*float64, 3)
	for index := 0; index < 3; index++ {
		parsed, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return nil, nil, nil
		}
		value := parsed
		values[index] = &value
	}
	return values[0], values[1], values[2]
}

// readCPUTicks reads /proc/stat. Index 0 is the aggregate "cpu" row, the rest
// are per-core in file order.
func readCPUTicks() ([]cpuTicks, bool) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return nil, false
	}
	defer file.Close()
	return parseCPUTicks(file)
}

func parseCPUTicks(source interface{ Read([]byte) (int, error) }) ([]cpuTicks, bool) {
	scanner := bufio.NewScanner(source)
	var ticks []cpuTicks
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			// Rows are contiguous at the top of the file; the first non-cpu
			// line ends the section.
			if len(ticks) > 0 {
				break
			}
			continue
		}
		var entry cpuTicks
		for index := 1; index < len(fields); index++ {
			value, err := strconv.ParseUint(fields[index], 10, 64)
			if err != nil {
				continue
			}
			entry.total += value
			// Fields 4 and 5 are idle and iowait: time the CPU was not doing
			// work. Everything else counts as busy.
			if index == 4 || index == 5 {
				entry.idle += value
			}
		}
		ticks = append(ticks, entry)
	}
	if len(ticks) == 0 {
		return nil, false
	}
	return ticks, true
}

func readMemory() memoryInfo {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return memoryInfo{}
	}
	defer file.Close()
	return parseMemInfo(file)
}

func parseMemInfo(source interface{ Read([]byte) (int, error) }) memoryInfo {
	values := make(map[string]uint64, 8)
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		name, value, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		parsed, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo is in kB except for a few hugepage counters we do not
		// read.
		values[name] = parsed * 1024
	}

	info := memoryInfo{
		TotalBytes:     values["MemTotal"],
		AvailableBytes: values["MemAvailable"],
		FreeBytes:      values["MemFree"],
		SwapTotalBytes: values["SwapTotal"],
		SwapFreeBytes:  values["SwapFree"],
	}
	if buffers, ok := values["Buffers"]; ok {
		info.BuffersBytes = &buffers
	}
	if cached, ok := values["Cached"]; ok {
		info.CachedBytes = &cached
	}
	// MemAvailable has been in the kernel since 3.14; fall back to the crude
	// estimate only if it is genuinely missing.
	if info.AvailableBytes == 0 && info.TotalBytes > 0 {
		info.AvailableBytes = info.FreeBytes + values["Buffers"] + values["Cached"]
	}
	if info.TotalBytes > 0 {
		percent := usedPercentOf(info.TotalBytes, info.AvailableBytes)
		info.UsedPercent = &percent
	}
	return info
}

// readThermal walks /sys/class/thermal. Returns nil when the machine has no
// sensors at all — a VM or container, where an empty array would wrongly
// suggest sensors that went quiet.
func readThermal() *thermalInfo {
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return nil
	}
	var zones []thermalZone
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "thermal_zone") {
			continue
		}
		raw, err := os.ReadFile("/sys/class/thermal/" + name + "/temp")
		if err != nil {
			continue
		}
		milli, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			continue
		}
		zoneName := strings.TrimSpace(firstNonEmptyFile("/sys/class/thermal/" + name + "/type"))
		if zoneName == "" {
			// Some boards leave `type` useless or absent; the index at least
			// keeps zones distinguishable.
			zoneName = name
		}
		zones = append(zones, thermalZone{
			Name:    zoneName,
			Celsius: roundTo(float64(milli)/1000, 1),
		})
	}
	if len(zones) == 0 {
		return nil
	}
	maximum := zones[0].Celsius
	for _, zone := range zones[1:] {
		if zone.Celsius > maximum {
			maximum = zone.Celsius
		}
	}
	return &thermalInfo{Zones: zones, MaxCelsius: maximum}
}

func readMounts() []mountInfo {
	file, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer file.Close()

	var mounts []mountInfo
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		path, fstype, options := fields[1], fields[2], fields[3]
		if pseudoFilesystems[fstype] || seen[path] {
			continue
		}
		var stat syscall.Statfs_t
		if syscall.Statfs(path, &stat) != nil {
			continue
		}
		blockSize := uint64(stat.Bsize)
		total := stat.Blocks * blockSize
		if total == 0 {
			continue
		}
		seen[path] = true
		// Available (not Free) is the space unprivileged writes can use.
		available := stat.Bavail * blockSize
		mounts = append(mounts, mountInfo{
			Path:           path,
			FSType:         fstype,
			TotalBytes:     total,
			AvailableBytes: available,
			UsedPercent:    usedPercentOf(total, available),
			ReadOnly:       options == "ro" || strings.HasPrefix(options, "ro,"),
		})
	}
	return mounts
}

// markStateDirMount flags the filesystem the state directory lives on, matched
// by device id rather than path prefix: on OpenWrt /etc is a symlink into the
// overlay, so /etc/sing-box-lx/state does not textually start with /overlay
// even though that is where it physically lives.
func markStateDirMount(mounts []mountInfo, stateDir string) {
	if stateDir == "" {
		return
	}
	var target syscall.Stat_t
	if syscall.Stat(stateDir, &target) != nil {
		return
	}
	best := -1
	for index := range mounts {
		var candidate syscall.Stat_t
		if syscall.Stat(mounts[index].Path, &candidate) != nil {
			continue
		}
		if candidate.Dev != target.Dev {
			continue
		}
		// Several mounts can share a device (bind mounts); the longest path
		// is the most specific one.
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
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		// The ReadDir call itself holds one descriptor open; it is closed by
		// the time the caller sees this, so discount it.
		open := len(entries) - 1
		if open < 0 {
			open = 0
		}
		info.Open = &open
	}
	var limit syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit) == nil {
		value := int(limit.Cur)
		info.Limit = &value
	}
	// /proc/sys/fs/file-nr: allocated, unused, maximum. The daemon can be far
	// from its own ceiling while the machine is at its one — a different bug.
	if content, err := os.ReadFile("/proc/sys/fs/file-nr"); err == nil {
		fields := strings.Fields(string(content))
		if len(fields) >= 3 {
			if allocated, err := strconv.Atoi(fields[0]); err == nil {
				info.SystemOpen = &allocated
			}
			if maximum, err := strconv.Atoi(fields[2]); err == nil {
				info.SystemLimit = &maximum
			}
		}
	}
	return info
}

// readInterfaces reads /proc/net/dev, whose counters are honest 64-bit values
// — no extension needed, unlike darwin.
func readInterfaces() []rawInterface {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil
	}
	defer file.Close()
	counters := parseProcNetDev(file)
	return decorateInterfaces(counters)
}

// parseProcNetDev extracts the counters, split out so it can be tested on
// captured output rather than the host's real interfaces.
func parseProcNetDev(source interface{ Read([]byte) (int, error) }) []rawInterface {
	var interfaces []rawInterface
	scanner := bufio.NewScanner(source)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		// Two header lines precede the data.
		if lineNumber <= 2 {
			continue
		}
		name, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			continue
		}
		values := make([]uint64, 16)
		for index := 0; index < 16; index++ {
			values[index], _ = strconv.ParseUint(fields[index], 10, 64)
		}
		interfaces = append(interfaces, rawInterface{
			Name:      strings.TrimSpace(name),
			RxBytes:   values[0],
			RxPackets: values[1],
			RxErrors:  values[2],
			RxDropped: values[3],
			TxBytes:   values[8],
			TxPackets: values[9],
			TxErrors:  values[10],
			TxDropped: values[11],
		})
	}
	return interfaces
}

// utsString trims a NUL-terminated fixed-size uname field to a Go string.
//
// Generic over the element type on purpose: syscall.Utsname's arrays are
// []int8 on arm64 and amd64 but []uint8 on 32-bit arm, so a signature naming
// either one compiles on some Linux targets and not others. That asymmetry
// broke the linux-armv7 release build.
func utsString[T ~int8 | ~uint8](values []T) string {
	buffer := make([]byte, 0, len(values))
	for _, value := range values {
		if value == 0 {
			break
		}
		buffer = append(buffer, byte(value))
	}
	return string(buffer)
}
