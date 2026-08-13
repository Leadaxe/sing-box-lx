//go:build with_lxd && linux

package lxd

// The parsing halves of the Linux readers, exercised on captured /proc output.
// The router is the platform that matters for this feature, and its files
// cannot be read from a macOS dev machine — these close that gap.

import (
	"strings"
	"testing"
)

func TestParseCPUTicksSplitsAggregateFromCores(t *testing.T) {
	// Real /proc/stat shape: the bare "cpu" row is the sum, "cpu0…" follow,
	// and the section ends at the first non-cpu line.
	content := strings.Join([]string{
		"cpu  100 20 30 400 50 0 10 0 0 0",
		"cpu0 50 10 15 200 25 0 5 0 0 0",
		"cpu1 50 10 15 200 25 0 5 0 0 0",
		"intr 12345 0 0",
		"ctxt 987654",
	}, "\n")

	ticks, ok := parseCPUTicks(strings.NewReader(content))
	if !ok {
		t.Fatal("a well-formed /proc/stat must parse")
	}
	if len(ticks) != 3 {
		t.Fatalf("expected the aggregate plus two cores, got %d", len(ticks))
	}
	// total is every column summed: 100+20+30+400+50+0+10 = 610.
	if ticks[0].total != 610 {
		t.Fatalf("aggregate total must sum all columns, got %d", ticks[0].total)
	}
	// idle is columns 4 and 5 — idle plus iowait: 400+50.
	if ticks[0].idle != 450 {
		t.Fatalf("idle must be idle+iowait, got %d", ticks[0].idle)
	}
	if ticks[1].total != 305 || ticks[1].idle != 225 {
		t.Fatalf("per-core row parsed wrong: %+v", ticks[1])
	}
}

func TestParseCPUTicksRejectsEmpty(t *testing.T) {
	if _, ok := parseCPUTicks(strings.NewReader("intr 1 2 3\n")); ok {
		t.Fatal("a file with no cpu rows must report no source")
	}
}

func TestParseMemInfoUsesAvailableNotFree(t *testing.T) {
	// A router's RAM sits in page cache: MemFree is tiny while MemAvailable
	// is large. A Free-based percentage would scream "full" at 54% used.
	content := strings.Join([]string{
		"MemTotal:         262144 kB",
		"MemFree:           37376 kB",
		"MemAvailable:     118784 kB",
		"Buffers:            8000 kB",
		"Cached:            73408 kB",
		"SwapTotal:             0 kB",
		"SwapFree:              0 kB",
	}, "\n")

	info := parseMemInfo(strings.NewReader(content))
	if info.TotalBytes != 262144*1024 {
		t.Fatalf("kB must be scaled to bytes, got %d", info.TotalBytes)
	}
	if info.AvailableBytes != 118784*1024 {
		t.Fatalf("available parsed wrong, got %d", info.AvailableBytes)
	}
	if info.UsedPercent == nil {
		t.Fatal("a known total must yield a percentage")
	}
	// (262144-118784)/262144 = 54.7%, not the 85.7% MemFree would suggest.
	if *info.UsedPercent != 54.7 {
		t.Fatalf("expected 54.7%% from available, got %v", *info.UsedPercent)
	}
	if info.BuffersBytes == nil || info.CachedBytes == nil {
		t.Fatal("buffers and cached are present on Linux and must not be null")
	}
}

func TestParseMemInfoFallsBackWithoutMemAvailable(t *testing.T) {
	// MemAvailable predates every kernel we target, but a synthetic /proc
	// (some containers) can omit it; the crude estimate beats reporting zero.
	content := strings.Join([]string{
		"MemTotal:         262144 kB",
		"MemFree:           37376 kB",
		"Buffers:            8000 kB",
		"Cached:            73408 kB",
	}, "\n")

	info := parseMemInfo(strings.NewReader(content))
	expected := uint64(37376+8000+73408) * 1024
	if info.AvailableBytes != expected {
		t.Fatalf("fallback must be free+buffers+cached (%d), got %d", expected, info.AvailableBytes)
	}
}

func TestParseLoadAverageReadsThreeNumbers(t *testing.T) {
	one, five, fifteen := parseLoadAverage("0.42 0.38 0.31 1/234 5678\n")
	if one == nil || five == nil || fifteen == nil {
		t.Fatal("all three averages must parse")
	}
	if *one != 0.42 || *five != 0.38 || *fifteen != 0.31 {
		t.Fatalf("parsed wrong: %v %v %v", *one, *five, *fifteen)
	}
	// Junk must yield nothing rather than a confident zero.
	if one, _, _ := parseLoadAverage("garbage"); one != nil {
		t.Fatal("unparsable content must report no load average")
	}
}

func TestParseProcNetDevReadsBothDirections(t *testing.T) {
	// Two header lines precede the data, and the interface name is separated
	// by a colon that can be flush against the counters.
	content := strings.Join([]string{
		"Inter-|   Receive                                                |  Transmit",
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed",
		"    lo: 8192000   12000    0    0    0     0          0         0  8192000   12000    0    0    0     0       0          0",
		" br-lan: 48372615424 51203847    0   12    0     0          0         0 61093847552 48372615    0    0    0     0       0          0",
		"   junk line without colon",
	}, "\n")

	interfaces := parseProcNetDev(strings.NewReader(content))
	if len(interfaces) != 2 {
		t.Fatalf("expected two interfaces, got %d: %+v", len(interfaces), interfaces)
	}
	byName := make(map[string]rawInterface, len(interfaces))
	for _, entry := range interfaces {
		byName[entry.Name] = entry
	}
	bridge, ok := byName["br-lan"]
	if !ok {
		t.Fatalf("br-lan missing, got %v", byName)
	}
	// The name must be trimmed even when the file pads it.
	if bridge.RxBytes != 48372615424 || bridge.TxBytes != 61093847552 {
		t.Fatalf("byte counters parsed wrong: %+v", bridge)
	}
	if bridge.RxPackets != 51203847 || bridge.TxPackets != 48372615 {
		t.Fatalf("packet counters parsed wrong: %+v", bridge)
	}
	if bridge.RxDropped != 12 {
		t.Fatalf("rx_dropped must come from column 4, got %d", bridge.RxDropped)
	}
	// Linux counters are honest 64-bit: no extension, unlike darwin.
	if bridge.BytesAre32Bit {
		t.Fatal("/proc/net/dev is 64-bit and must not be flagged for extension")
	}
	// lo is reported like any other interface (owner decision: no filtering).
	if _, present := byName["lo"]; !present {
		t.Fatal("lo must be reported, not filtered out")
	}
}

func TestParseProcNetDevSkipsShortRows(t *testing.T) {
	content := strings.Join([]string{
		"header one",
		"header two",
		"  eth0: 1 2 3",
	}, "\n")
	if interfaces := parseProcNetDev(strings.NewReader(content)); len(interfaces) != 0 {
		t.Fatalf("a truncated row must be skipped, got %+v", interfaces)
	}
}

func TestParseOSReleaseOnAnOpenWrtFork(t *testing.T) {
	// Captured verbatim from the owner's RouteRich router (MT7981). The point
	// of the case: NAME says "RouteRich" but ID says "openwrt" — a client
	// matching on the human string would fail to recognise the platform,
	// while ID and ID_LIKE identify it correctly.
	content := strings.Join([]string{
		`NAME="RouteRich"`,
		`VERSION="24.10.5"`,
		`ID="openwrt"`,
		`ID_LIKE="lede openwrt"`,
		`PRETTY_NAME="RouteRich 24.10.5"`,
		`VERSION_ID="24.10.5"`,
		`OPENWRT_BOARD="mediatek/filogic"`,
		`OPENWRT_RELEASE="RouteRich 24.10.5 r29087-d9c5716d1d RR-3.9.0"`,
	}, "\n")

	name, id, idLike := parseOSRelease(content)
	if name != "RouteRich 24.10.5" {
		t.Fatalf("name must come from PRETTY_NAME, got %q", name)
	}
	if id != "openwrt" {
		t.Fatalf("a fork still reports its base id, got %q", id)
	}
	if len(idLike) != 2 || idLike[0] != "lede" || idLike[1] != "openwrt" {
		t.Fatalf("ID_LIKE must be split on spaces, got %v", idLike)
	}
}

func TestParseOSReleaseFallsBackToOpenWrtFile(t *testing.T) {
	// /etc/openwrt_release has no ID at all — only a description. Also from
	// the owner's router: note the build revision and vendor suffix, which is
	// why os-release's PRETTY_NAME is preferred when both exist.
	content := strings.Join([]string{
		"DISTRIB_ID='RouteRich'",
		"DISTRIB_RELEASE='24.10.5'",
		"DISTRIB_DESCRIPTION='RouteRich 24.10.5 r29087-d9c5716d1d RR-3.9.0'",
		"DISTRIB_TAINTS=''",
	}, "\n")

	name, id, idLike := parseOSRelease(content)
	if name != "RouteRich 24.10.5 r29087-d9c5716d1d RR-3.9.0" {
		t.Fatalf("DISTRIB_DESCRIPTION must be the fallback name, got %q", name)
	}
	if id != "" || len(idLike) != 0 {
		t.Fatalf("this file carries no machine-readable id, got %q %v", id, idLike)
	}
	// An empty value must not be mistaken for a present one.
	if strings.Contains(name, "TAINTS") {
		t.Fatal("empty values must be skipped")
	}
}

func TestParseOSReleaseStandardDistro(t *testing.T) {
	content := strings.Join([]string{
		`PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"`,
		`NAME="Debian GNU/Linux"`,
		`ID=debian`, // unquoted is legal
		`VERSION_ID="12"`,
	}, "\n")
	name, id, idLike := parseOSRelease(content)
	if name != "Debian GNU/Linux 12 (bookworm)" || id != "debian" {
		t.Fatalf("parsed wrong: %q %q", name, id)
	}
	if idLike != nil {
		t.Fatalf("no ID_LIKE means no list, got %v", idLike)
	}
}

func TestParseOSReleaseIgnoresJunk(t *testing.T) {
	name, id, _ := parseOSRelease("garbage\n\n=novalue\nKEY_WITHOUT_EQUALS\n")
	if name != "" || id != "" {
		t.Fatalf("junk must yield nothing, got %q %q", name, id)
	}
}
