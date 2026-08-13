//go:build with_lxd

package lxd

// hostinfo_test.go covers host telemetry (SPEC 068): the delta arithmetic that
// turns monotonic counters into percentages and rates, the 32-bit counter
// extension darwin needs, the read-only filter on the disk summary, and the
// endpoints' behaviour with no core running. Platform readers are swapped
// through the cache's seams so a test never depends on the machine it runs on.

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedHostCache builds a cache whose readers are all stubs, so a test states
// exactly what the platform reported.
func fixedHostCache(now *time.Time) *hostCache {
	cache := newHostCache("/state")
	cache.now = func() time.Time { return *now }
	cache.readStatic = func() staticInfo { return staticInfo{Arch: "arm64"} }
	cache.readCPUTicks = func() ([]cpuTicks, bool) { return nil, false }
	cache.readMemory = func() memoryInfo { return memoryInfo{} }
	cache.readThermal = func() *thermalInfo { return nil }
	cache.readMounts = func() []mountInfo { return nil }
	cache.readFD = func() fdInfo { return fdInfo{} }
	cache.readInterfaces = func() []rawInterface { return nil }
	return cache
}

func TestCPUPercentNeedsTwoSamples(t *testing.T) {
	// The first sample has nothing to compare against. Reporting 0 would read
	// as "idle", which is a different and wrong statement — so it is null.
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	sample := []cpuTicks{{total: 1000, idle: 900}, {total: 500, idle: 450}}
	cache.readCPUTicks = func() ([]cpuTicks, bool) { return sample, true }

	first := cache.Host().CPU
	if first.UsagePercent != nil {
		t.Fatalf("the first sample must report no percentage, got %v", *first.UsagePercent)
	}
	if first.PerCorePercent != nil {
		t.Fatalf("per-core must be absent too, got %v", first.PerCorePercent)
	}
	if first.IntervalSeconds != 0 {
		t.Fatalf("no percentage means no window, got %v", first.IntervalSeconds)
	}

	// Second sample: 100 ticks passed, 50 of them idle → 50% busy.
	now = now.Add(10 * time.Second)
	sample = []cpuTicks{{total: 1100, idle: 950}, {total: 600, idle: 500}}
	second := cache.Host().CPU
	if second.UsagePercent == nil {
		t.Fatal("the second sample must produce a percentage")
	}
	if *second.UsagePercent != 50 {
		t.Fatalf("100 ticks with 50 idle is 50%%, got %v", *second.UsagePercent)
	}
	if second.IntervalSeconds != 10 {
		t.Fatalf("interval must be the gap between samples, got %v", second.IntervalSeconds)
	}
	if len(second.PerCorePercent) != 1 {
		t.Fatalf("one core row beyond the aggregate, got %v", second.PerCorePercent)
	}
	if second.PerCorePercent[0] != 50 {
		t.Fatalf("core 0: 100 ticks with 50 idle is 50%%, got %v", second.PerCorePercent[0])
	}
}

func TestCPUPercentPerCoreIsNotFlattened(t *testing.T) {
	// One core pinned while the others idle is a diagnosis the average hides —
	// the reason per-core exists at all.
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	sample := []cpuTicks{
		{total: 400, idle: 300}, // aggregate
		{total: 100, idle: 100}, // idle core
		{total: 100, idle: 0},   // pinned core
		{total: 100, idle: 100},
		{total: 100, idle: 100},
	}
	cache.readCPUTicks = func() ([]cpuTicks, bool) { return sample, true }
	cache.Host()

	// Past the TTL, or the cache serves the first snapshot back.
	now = now.Add(hostInfoTTL + time.Second)
	sample = []cpuTicks{
		{total: 800, idle: 600},
		{total: 200, idle: 200}, // still idle
		{total: 200, idle: 0},   // still pinned
		{total: 200, idle: 200},
		{total: 200, idle: 200},
	}
	cpu := cache.Host().CPU
	if len(cpu.PerCorePercent) != 4 {
		t.Fatalf("expected four cores, got %v", cpu.PerCorePercent)
	}
	if cpu.PerCorePercent[1] != 100 {
		t.Fatalf("the pinned core must read 100%%, got %v", cpu.PerCorePercent[1])
	}
	if cpu.PerCorePercent[0] != 0 {
		t.Fatalf("an idle core must read 0%%, got %v", cpu.PerCorePercent[0])
	}
	// The aggregate is the sum of the cores: 400 ticks passed, 300 of them
	// idle, so one pinned core out of four reads as 25% overall — exactly the
	// number that hides the diagnosis the per-core array exposes.
	if *cpu.UsagePercent != 25 {
		t.Fatalf("one busy core in four is 25%% overall, got %v", *cpu.UsagePercent)
	}
}

func TestCPUTicksGoingBackwardsGiveNoVerdict(t *testing.T) {
	// A counter that decreased means a reset or a wrap; inventing a percentage
	// from it would be worse than reporting none.
	previous := cpuTicks{total: 1000, idle: 900}
	if _, ok := tickUsage(previous, cpuTicks{total: 500, idle: 400}); ok {
		t.Fatal("a backwards total must not produce a percentage")
	}
	if _, ok := tickUsage(previous, previous); ok {
		t.Fatal("a zero delta must not produce a percentage")
	}
	if _, ok := tickUsage(previous, cpuTicks{total: 1100, idle: 1099}); ok {
		t.Fatal("idle growing faster than total is nonsense, not 0%")
	}
}

func TestCPUCoreCountChangeResetsTheWindow(t *testing.T) {
	// CPU hotplug changes the row count; pairing row N with a different core's
	// previous value would produce a confident wrong number.
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	sample := []cpuTicks{{total: 100, idle: 50}, {total: 100, idle: 50}}
	cache.readCPUTicks = func() ([]cpuTicks, bool) { return sample, true }
	cache.Host()

	now = now.Add(hostInfoTTL + time.Second)
	sample = []cpuTicks{{total: 200, idle: 100}, {total: 100, idle: 50}, {total: 100, idle: 50}}
	if cpu := cache.Host().CPU; cpu.UsagePercent != nil {
		t.Fatalf("a changed core count must void the window, got %v", *cpu.UsagePercent)
	}
}

func TestLoadAverageSurvivesMissingTickSource(t *testing.T) {
	// macOS has no pure-Go tick source, but load averages still answer "is it
	// busy" — they must not be dropped along with the percentages.
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	cache.readCPUTicks = func() ([]cpuTicks, bool) { return nil, false }

	cpu := cache.Host().CPU
	if cpu.UsagePercent != nil {
		t.Fatal("no tick source means no percentage")
	}
	if cpu.Cores <= 0 {
		t.Fatalf("core count comes from the runtime and is always known, got %d", cpu.Cores)
	}
}

func TestExtendCounterCarriesAcrossA32BitWrap(t *testing.T) {
	// darwin's if_data64 byte counters wrap at 4 GB despite the name; a client
	// graphing them would otherwise see a drop to zero every few hours.
	const wrap = uint64(1) << 32

	// First sighting: the raw value is the total, nothing to carry.
	if got := extendCounter(0, 0, 1000, false); got != 1000 {
		t.Fatalf("first sample must pass through, got %d", got)
	}
	// Normal forward movement.
	if got := extendCounter(1000, 1000, 1500, true); got != 1500 {
		t.Fatalf("forward delta must add, got %d", got)
	}
	// The wrap: raw went from just under 2^32 back to 100.
	previousRaw := wrap - 50
	got := extendCounter(previousRaw, previousRaw, 100, true)
	if got != previousRaw+150 {
		t.Fatalf("a wrap must carry, expected %d, got %d", previousRaw+150, got)
	}
	// A second wrap keeps accumulating rather than resetting.
	got = extendCounter(got, 100, 200, true)
	if got != previousRaw+250 {
		t.Fatalf("the total must keep growing, got %d", got)
	}
}

func TestInterfaceRatesNeedAPreviousSample(t *testing.T) {
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	counters := []rawInterface{{Name: "br-lan", RxBytes: 1000, TxBytes: 2000}}
	cache.readInterfaces = func() []rawInterface { return counters }

	first := cache.Interfaces()
	if first.IntervalSeconds != 0 {
		t.Fatalf("no previous sample means no window, got %v", first.IntervalSeconds)
	}
	if first.Interfaces[0].RxBytesPerSecond != nil {
		t.Fatal("the first sample must report no rate")
	}

	now = now.Add(10 * time.Second)
	counters = []rawInterface{{Name: "br-lan", RxBytes: 11000, TxBytes: 2000}}
	second := cache.Interfaces()
	entry := second.Interfaces[0]
	if entry.RxBytesPerSecond == nil {
		t.Fatal("the second sample must produce a rate")
	}
	if *entry.RxBytesPerSecond != 1000 {
		t.Fatalf("10000 bytes over 10 s is 1000 B/s, got %v", *entry.RxBytesPerSecond)
	}
	if *entry.TxBytesPerSecond != 0 {
		t.Fatalf("an idle direction is 0, not noise, got %v", *entry.TxBytesPerSecond)
	}
	// Raw counters ride along with the rates: a counter survives gaps, a rate
	// does not, so the client gets both.
	if entry.RxBytes != 11000 {
		t.Fatalf("the raw counter must be reported too, got %d", entry.RxBytes)
	}
}

func TestInterfaceEmptyFieldsAreValidStates(t *testing.T) {
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	cache.readInterfaces = func() []rawInterface {
		return []rawInterface{{Name: "wg0", Up: true}}
	}
	entry := cache.Interfaces().Interfaces[0]
	if entry.Mac != "" {
		t.Fatalf("a tunnel device has no MAC, got %q", entry.Mac)
	}
	// [] rather than null: a client iterating must not special-case absence.
	if entry.Addresses == nil {
		t.Fatal("addresses must marshal as an empty array, not null")
	}
	if len(entry.Addresses) != 0 {
		t.Fatalf("expected no addresses, got %v", entry.Addresses)
	}
}

func TestDiskSummarySkipsReadOnlyFilesystems(t *testing.T) {
	// OpenWrt's squashfs root is permanently 100% full. Counting it would make
	// the summary always red, and an always-red indicator is one nobody reads.
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	cache.readMounts = func() []mountInfo {
		return []mountInfo{
			{Path: "/", FSType: "squashfs", UsedPercent: 100, ReadOnly: true},
			{Path: "/overlay", FSType: "f2fs", UsedPercent: 60},
			{Path: "/tmp", FSType: "tmpfs", UsedPercent: 9.4},
		}
	}
	disk := cache.Host().Disk
	if disk.MaxUsedPercent == nil {
		t.Fatal("a writable mount exists, so there must be a summary")
	}
	if *disk.MaxUsedPercent != 60 {
		t.Fatalf("the summary must ignore the read-only root, got %v", *disk.MaxUsedPercent)
	}
	// The flag stays on the entry: the full picture is still available.
	if !disk.Mounts[0].ReadOnly {
		t.Fatal("the read-only flag must survive on the mount itself")
	}
}

func TestDiskSummaryAbsentWithNoWritableMounts(t *testing.T) {
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	cache.readMounts = func() []mountInfo {
		return []mountInfo{{Path: "/", FSType: "squashfs", UsedPercent: 100, ReadOnly: true}}
	}
	if summary := cache.Host().Disk.MaxUsedPercent; summary != nil {
		t.Fatalf("nothing writable means no summary, got %v", *summary)
	}
}

func TestUsedPercentComesFromAvailableNotFree(t *testing.T) {
	// A router keeps most of its RAM in page cache: a Free-based percentage
	// screams "full" with 120 MB actually free.
	total := uint64(268435456)
	available := uint64(121634816)
	if got := usedPercentOf(total, available); got != 54.7 {
		t.Fatalf("expected 54.7%%, got %v", got)
	}
	// Guards: a zero total must not divide, and available > total (possible
	// across a racing read) must not underflow into a giant number.
	if got := usedPercentOf(0, 0); got != 0 {
		t.Fatalf("a zero total is 0%%, got %v", got)
	}
	if got := usedPercentOf(100, 200); got != 0 {
		t.Fatalf("available beyond total must clamp to 0%%, got %v", got)
	}
}

func TestHostCacheThrottles(t *testing.T) {
	now := time.Unix(1786620000, 0)
	cache := fixedHostCache(&now)
	var reads int
	cache.readMemory = func() memoryInfo {
		reads++
		return memoryInfo{TotalBytes: 1000}
	}

	cache.Host()
	cache.Host()
	if reads != 1 {
		t.Fatalf("a second read inside the TTL must be cached, readers ran %d times", reads)
	}
	now = now.Add(hostInfoTTL + time.Second)
	cache.Host()
	if reads != 2 {
		t.Fatalf("the cache must expire after the TTL, readers ran %d times", reads)
	}
}

func TestHostEndpointsServedWithNoCore(t *testing.T) {
	// These describe the machine, not the instance — the state in which an
	// operator needs them most is exactly the one where no core is up.
	now := time.Unix(1786620000, 0)
	control := newTestController(t, &fakeReloader{}, nil)
	control.host = fixedHostCache(&now)
	control.host.readMemory = func() memoryInfo { return memoryInfo{TotalBytes: 268435456} }
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/host", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200 with no core, got %d %v", status, payload)
	}
	memory, _ := payload["memory"].(map[string]any)
	if total, _ := memory["total_bytes"].(float64); total != 268435456 {
		t.Fatalf("memory must be reported, payload: %v", payload)
	}
	status, payload = adminRequest(t, http.MethodGet, server.URL+"/admin/host/interfaces", "")
	if status != http.StatusOK {
		t.Fatalf("interfaces must answer with no core, got %d %v", status, payload)
	}
}

func TestHostNullFieldsArePresentNotOmitted(t *testing.T) {
	// A platform that cannot measure something reports null, and the KEY must
	// still be there: a missing key makes a client guess whether the daemon is
	// old or the platform is limited.
	now := time.Unix(1786620000, 0)
	control := newTestController(t, &fakeReloader{}, nil)
	control.host = fixedHostCache(&now)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	_, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/host", "")
	if _, present := payload["thermal"]; !present {
		t.Fatalf("thermal must be present as null, payload: %v", payload)
	}
	if payload["thermal"] != nil {
		t.Fatalf("no sensors means null, got %v", payload["thermal"])
	}
	cpu, _ := payload["cpu"].(map[string]any)
	for _, field := range []string{"usage_percent", "per_core_percent", "load_1"} {
		if _, present := cpu[field]; !present {
			t.Fatalf("cpu.%s must be present even when null, payload: %v", cpu, field)
		}
	}
}

func TestHostRoutesPinnedToTrustedClients(t *testing.T) {
	// Host telemetry names the machine's disks, addresses and load — not
	// something to serve to anyone who can reach the port.
	now := time.Unix(1786620000, 0)
	control := newTestController(t, &fakeReloader{}, nil)
	control.host = fixedHostCache(&now)
	control.clients = newTestRegistry(t)
	handler := control.adminHandler("")

	for _, route := range []string{"/admin/host", "/admin/host/interfaces"} {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		status, payload := serveAdmin(t, handler, request)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s must refuse an uncertified client, got %d", route, status)
		}
		if text, _ := payload["error"].(string); !strings.Contains(text, "client certificate not trusted") {
			t.Fatalf("%s must give the not-trusted error, payload: %v", route, payload)
		}
	}
}

func TestHostRoutesReachableRemotely(t *testing.T) {
	// Like the rest of the observability plane, the point is reading these
	// from the launcher — they must NOT be on the operator loopback path.
	now := time.Unix(1786620000, 0)
	control := newTestController(t, &fakeReloader{}, nil)
	control.host = fixedHostCache(&now)
	control.clients = newTestRegistry(t)
	trustedDER, err := decodeCertPEM(testClientCertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	code, err := control.clients.mintCode("launcher")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = control.clients.enroll(code, "launcher", trustedDER); err != nil {
		t.Fatal(err)
	}
	trusted, err := x509.ParseCertificate(trustedDER)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/host", nil)
	request.RemoteAddr = "203.0.113.7:51000" // decidedly not loopback
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted}}
	if status, payload := serveAdmin(t, control.adminHandler(""), request); status != http.StatusOK {
		t.Fatalf("a trusted remote client must reach /admin/host, got %d %v", status, payload)
	}
}

func TestHostRoutesAbsentWhenUnwired(t *testing.T) {
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	// rawRequest, not adminRequest: an unregistered route gets the mux's own
	// plain-text 404.
	if status, _, _ := rawRequest(t, http.MethodGet, server.URL+"/admin/host"); status != http.StatusNotFound {
		t.Fatalf("expected 404 with no host cache wired, got %d", status)
	}
}
