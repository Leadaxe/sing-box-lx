//go:build with_lx_command

package lxd

// clientinfo_test.go covers the client identity directory (SPEC 066): the
// merge order that makes a later provider refine an earlier one, the operator
// labels and their key validation, the cache, and the endpoint's behaviour
// with no core running. Providers that shell out are exercised through the
// clientInfo seams rather than against the host's real tables — a test must
// not depend on whose Wi-Fi the machine is on.

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixedClientInfo builds a directory whose providers are all stubs, so a test
// states exactly which sources spoke.
func fixedClientInfo(t *testing.T, stateStore *store) *clientInfo {
	t.Helper()
	labels, err := newLabelStore(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	info := newClientInfo(labels, []string{filepath.Join(t.TempDir(), "absent.leases")})
	info.loadLeases = func([]string) (map[netip.Addr]net.HardwareAddr, map[netip.Addr]string, map[string]string) {
		return nil, nil, nil
	}
	info.enrichARP = func(map[string]*clientEntry) bool { return false }
	info.enrichBridge = func(map[string]*clientEntry) bool { return false }
	info.enrichWiFi = func(map[string]*clientEntry) bool { return false }
	return info
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }

func mustMAC(t *testing.T, text string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(text)
	if err != nil {
		t.Fatal(err)
	}
	return mac
}

func mustAddr(t *testing.T, text string) netip.Addr {
	t.Helper()
	address, err := netip.ParseAddr(text)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func TestClientInfoMergesProvidersInPriorityOrder(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	// A lease file that exists, so the lease provider runs.
	leases := filepath.Join(t.TempDir(), "dhcp.leases")
	if err = os.WriteFile(leases, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info.leaseFiles = []string{leases}
	info.loadLeases = func([]string) (map[netip.Addr]net.HardwareAddr, map[netip.Addr]string, map[string]string) {
		return map[netip.Addr]net.HardwareAddr{
				mustAddr(t, "192.168.20.238"): mustMAC(t, "be:ab:bd:ec:70:40"),
			}, map[netip.Addr]string{
				mustAddr(t, "192.168.20.238"): "iPhone-Vasya",
			}, nil
	}
	// ARP reports the bridge...
	info.enrichARP = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.238")
		entry.Mac = "be:ab:bd:ec:70:40"
		entry.Iface = "br-lan"
		entry.addSource("arp")
		return true
	}
	// ...and wireless refines it to the actual AP. This ordering is the point:
	// the more precise source must win.
	info.enrichWiFi = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.238")
		entry.SSID = "LexVPN2G"
		entry.Iface = "phy0-ap1"
		entry.addSource("wireless")
		return true
	}

	snapshot := info.Snapshot()
	entry := snapshot.Clients["192.168.20.238"]
	if entry == nil {
		t.Fatalf("client missing, snapshot: %+v", snapshot)
	}
	if entry.Name != "iPhone-Vasya" {
		t.Fatalf("name must come from the lease, got %q", entry.Name)
	}
	if entry.Iface != "phy0-ap1" {
		t.Fatalf("wireless must overwrite the bridge ARP reported, got %q", entry.Iface)
	}
	if entry.SSID != "LexVPN2G" {
		t.Fatalf("ssid must come from wireless, got %q", entry.SSID)
	}
	// Provenance must name every contributor, in call order.
	if entry.Source != "lease+arp+wireless" {
		t.Fatalf("source must record all providers in order, got %q", entry.Source)
	}
}

func TestClientInfoBridgePortDoesNotOverwriteIface(t *testing.T) {
	// iface and port are different levels — which segment vs which socket —
	// and an operator wants both.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	info.enrichARP = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.51")
		entry.Mac = "dc:a6:32:de:ad:be"
		entry.Iface = "br-lan"
		entry.addSource("arp")
		return true
	}
	info.enrichBridge = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.51")
		entry.Port = "lan2"
		entry.addSource("bridge")
		return true
	}

	entry := info.Snapshot().Clients["192.168.20.51"]
	if entry == nil {
		t.Fatal("wired client missing")
	}
	if entry.Iface != "br-lan" {
		t.Fatalf("bridge must leave iface alone, got %q", entry.Iface)
	}
	if entry.Port != "lan2" {
		t.Fatalf("port must come from the bridge provider, got %q", entry.Port)
	}
	// A wired client has no SSID, and that empty field is a valid state.
	if entry.SSID != "" {
		t.Fatalf("a wired client must have no ssid, got %q", entry.SSID)
	}
}

func TestClientInfoStaticIPGetsMACWithoutLease(t *testing.T) {
	// The static-IP hole the ARP provider closes: no DHCP name, but a mac and
	// an iface — enough for an operator label to attach to.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	info.enrichARP = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.99")
		entry.Mac = "aa:bb:cc:dd:ee:ff"
		entry.Iface = "br-lan"
		entry.addSource("arp")
		return true
	}

	entry := info.Snapshot().Clients["192.168.20.99"]
	if entry == nil {
		t.Fatal("static client missing")
	}
	if entry.Name != "" {
		t.Fatalf("a client absent from the leases has no DHCP name, got %q", entry.Name)
	}
	if entry.Mac == "" || entry.Iface == "" {
		t.Fatalf("arp must still supply mac and iface, got %+v", entry)
	}

	// A MAC label now gives it a name.
	if err = info.SetLabel("aa:bb:cc:dd:ee:ff", "Принтер"); err != nil {
		t.Fatal(err)
	}
	labeled := info.Snapshot().Clients["192.168.20.99"]
	if labeled.Name != "Принтер" {
		t.Fatalf("a MAC label must name the client, got %q", labeled.Name)
	}
	if labeled.Source != "arp+label" {
		t.Fatalf("source must record the label, got %q", labeled.Source)
	}
}

func TestClientInfoLabelOverridesLeaseName(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	info.enrichARP = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.7")
		entry.Mac = "11:22:33:44:55:66"
		entry.addSource("arp")
		return true
	}
	if err = info.SetLabel("192.168.20.7", "Телевизор"); err != nil {
		t.Fatal(err)
	}
	entry := info.Snapshot().Clients["192.168.20.7"]
	if entry.Name != "Телевизор" {
		t.Fatalf("the operator's word is final, got %q", entry.Name)
	}
}

func TestClientInfoLabelSurvivesReload(t *testing.T) {
	// The labels file is the operator's, not a cache: a restart must not lose
	// it. Written atomically, so a crash mid-write cannot tear it either.
	dir := t.TempDir()
	stateStore, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	if err = info.SetLabel("192.168.20.77", "Телевизор"); err != nil {
		t.Fatal(err)
	}

	reopened, err := newLabelStore(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if name := reopened.All()["192.168.20.77"]; name != "Телевизор" {
		t.Fatalf("label must survive a reload, got %q", name)
	}
	if _, err = os.Stat(filepath.Join(dir, "client-labels.json")); err != nil {
		t.Fatalf("labels file must exist: %v", err)
	}
}

func TestClientInfoLabelKeyValidation(t *testing.T) {
	valid := map[string]string{
		"192.168.20.7":        "192.168.20.7",
		"be:ab:bd:ec:70:40":   "be:ab:bd:ec:70:40",
		"BE:AB:BD:EC:70:40":   "be:ab:bd:ec:70:40", // canonicalised
		"fe80::1":             "fe80::1",
		" 192.168.20.7 ":      "192.168.20.7", // trimmed
		"be-ab-bd-ec-70-40":   "be:ab:bd:ec:70:40",
		"2001:db8::dead:beef": "2001:db8::dead:beef",
	}
	for input, want := range valid {
		got, ok := normalizeLabelKey(input)
		if !ok {
			t.Fatalf("%q must be a valid key", input)
		}
		if got != want {
			t.Fatalf("%q must normalise to %q, got %q", input, want, got)
		}
	}
	// Junk keys are refused: they never reach a filesystem path, but a key
	// nothing can ever match would only rot in the map.
	for _, input := range []string{"", "   ", "notanaddress", "../../etc/passwd", "192.168.20.7/24", "192.168.20"} {
		if _, ok := normalizeLabelKey(input); ok {
			t.Fatalf("%q must be refused as a label key", input)
		}
	}
}

func TestClientInfoCacheThrottlesAndLabelInvalidates(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	var calls int
	info.enrichARP = func(clients map[string]*clientEntry) bool {
		calls++
		entry := entryFor(clients, "192.168.20.5")
		entry.Mac = "11:22:33:44:55:66"
		entry.addSource("arp")
		return true
	}
	// A fixed clock, so the test asserts the throttle rather than racing it.
	now := time.Unix(1755087234, 0)
	info.now = func() time.Time { return now }

	info.Snapshot()
	info.Snapshot()
	if calls != 1 {
		t.Fatalf("a second read inside the TTL must be served from cache, providers ran %d times", calls)
	}
	now = now.Add(clientInfoTTL + time.Second)
	info.Snapshot()
	if calls != 2 {
		t.Fatalf("the cache must expire after the TTL, providers ran %d times", calls)
	}

	// A label write is an explicit act: waiting out the TTL to see it would
	// read as the write being lost.
	if err = info.SetLabel("192.168.20.5", "Ноут"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("SetLabel must not rebuild eagerly")
	}
	if name := info.Snapshot().Clients["192.168.20.5"].Name; name != "Ноут" {
		t.Fatalf("a label must be visible immediately, got %q", name)
	}
	if calls != 3 {
		t.Fatalf("a label write must invalidate the cache, providers ran %d times", calls)
	}
}

func TestClientInfoLabelOnlyClientAppears(t *testing.T) {
	// A device that no live source knows — a static-IP box that happens to be
	// silent — still belongs in the directory if the operator named it.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info := fixedClientInfo(t, stateStore)
	if err = info.SetLabel("192.168.20.77", "Телевизор"); err != nil {
		t.Fatal(err)
	}
	entry := info.Snapshot().Clients["192.168.20.77"]
	if entry == nil {
		t.Fatal("a labeled client must appear even with no other source")
	}
	if entry.Source != "label" {
		t.Fatalf("source must say the name is only a label, got %q", entry.Source)
	}
	if entry.Mac != "" || entry.Iface != "" {
		t.Fatalf("no live source spoke, so those fields stay empty: %+v", entry)
	}
}

func TestClientInfoEmptySnapshotIsAMapNotNull(t *testing.T) {
	// An empty directory must marshal as {} — a client iterating the map must
	// not have to special-case JSON null.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := newTestController(t, &fakeReloader{}, nil)
	control.clientInfo = fixedClientInfo(t, stateStore)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/clients-info", "")
	if status != http.StatusOK {
		t.Fatal("expected 200, got", status)
	}
	clients, ok := payload["clients"].(map[string]any)
	if !ok {
		t.Fatalf("clients must be an object even when empty, payload: %v", payload)
	}
	if len(clients) != 0 {
		t.Fatalf("expected no clients, got %v", clients)
	}
	if _, ok = payload["updated_unix"].(float64); !ok {
		t.Fatalf("updated_unix must be present, payload: %v", payload)
	}
}

func TestClientInfoServedWithNoCore(t *testing.T) {
	// The directory is built from the host's own tables, so it answers whether
	// or not a core is up — the state in which a client needs it most.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := newTestController(t, &fakeReloader{}, nil)
	control.clientInfo = fixedClientInfo(t, stateStore)
	control.clientInfo.enrichARP = func(clients map[string]*clientEntry) bool {
		entry := entryFor(clients, "192.168.20.5")
		entry.Mac = "11:22:33:44:55:66"
		entry.addSource("arp")
		return true
	}
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodGet, server.URL+"/admin/clients-info", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200 with no core, got %d %v", status, payload)
	}
	clients, _ := payload["clients"].(map[string]any)
	if _, ok := clients["192.168.20.5"]; !ok {
		t.Fatalf("the client must be listed with no core running, payload: %v", payload)
	}
}

func TestClientLabelEndpointsRoundTrip(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := newTestController(t, &fakeReloader{}, nil)
	control.clientInfo = fixedClientInfo(t, stateStore)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, payload := adminRequest(t, http.MethodPut,
		server.URL+"/admin/clients-info/labels/192.168.20.77", `{"name":"Телевизор"}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d %v", status, payload)
	}
	_, payload = adminRequest(t, http.MethodGet, server.URL+"/admin/clients-info", "")
	clients, _ := payload["clients"].(map[string]any)
	entry, _ := clients["192.168.20.77"].(map[string]any)
	if entry == nil || entry["name"] != "Телевизор" {
		t.Fatalf("the label must show up in the directory, payload: %v", payload)
	}

	status, payload = adminRequest(t, http.MethodDelete,
		server.URL+"/admin/clients-info/labels/192.168.20.77", "")
	if status != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d %v", status, payload)
	}
	_, payload = adminRequest(t, http.MethodGet, server.URL+"/admin/clients-info", "")
	clients, _ = payload["clients"].(map[string]any)
	if _, ok := clients["192.168.20.77"]; ok {
		t.Fatalf("the client had no other source, so deleting its label removes it: %v", payload)
	}
}

func TestClientLabelEndpointRejectsBadInput(t *testing.T) {
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := newTestController(t, &fakeReloader{}, nil)
	control.clientInfo = fixedClientInfo(t, stateStore)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	status, _ := adminRequest(t, http.MethodPut,
		server.URL+"/admin/clients-info/labels/not-an-address", `{"name":"x"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a junk key must be refused, got %d", status)
	}
	// An empty name is a malformed write, not an implicit delete: the client's
	// intent must never be guessed.
	status, _ = adminRequest(t, http.MethodPut,
		server.URL+"/admin/clients-info/labels/192.168.20.77", `{"name":"  "}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an empty name must be refused, got %d", status)
	}
	status, _ = adminRequest(t, http.MethodPut,
		server.URL+"/admin/clients-info/labels/192.168.20.77", `not json`)
	if status != http.StatusBadRequest {
		t.Fatalf("a malformed body must be refused, got %d", status)
	}
}

func TestClientInfoRoutesAbsentWhenUnwired(t *testing.T) {
	// The routes are registered only when the directory is wired, mirroring
	// the resource plane: a 404 is honest about the feature being off.
	control := newTestController(t, &fakeReloader{}, nil)
	server := httptest.NewServer(control.adminHandler(""))
	defer server.Close()

	// rawRequest, not adminRequest: an unregistered route gets the mux's own
	// plain-text 404, which is not JSON.
	if status, _, _ := rawRequest(t, http.MethodGet, server.URL+"/admin/clients-info"); status != http.StatusNotFound {
		t.Fatalf("expected 404 with no directory wired, got %d", status)
	}
}

func TestClientInfoExpiredLeasesAreSkipped(t *testing.T) {
	// Uses the real route.ReloadLeaseFiles rather than a seam: the point is
	// that the upstream parser's expiry handling is what we inherit, so a
	// stale lease must not resurrect a device that left the network hours ago.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	labels, err := newLabelStore(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	leases := filepath.Join(t.TempDir(), "dhcp.leases")
	live := time.Now().Add(time.Hour).Unix()
	expired := time.Now().Add(-time.Hour).Unix()
	content := "" +
		itoa(live) + " aa:bb:cc:dd:ee:01 192.168.20.51 NAS *\n" +
		itoa(expired) + " aa:bb:cc:dd:ee:02 192.168.20.52 GhostLaptop *\n"
	if err = os.WriteFile(leases, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	info := newClientInfo(labels, []string{leases})
	info.enrichARP = func(map[string]*clientEntry) bool { return false }
	info.enrichBridge = func(map[string]*clientEntry) bool { return false }
	info.enrichWiFi = func(map[string]*clientEntry) bool { return false }

	snapshot := info.Snapshot()
	if entry := snapshot.Clients["192.168.20.51"]; entry == nil || entry.Name != "NAS" {
		t.Fatalf("the live lease must be listed, snapshot: %+v", snapshot.Clients)
	}
	if _, present := snapshot.Clients["192.168.20.52"]; present {
		t.Fatalf("an expired lease must not enter the map, snapshot: %+v", snapshot.Clients)
	}
}

func TestClientInfoNoLeaseFilesStillAnswers(t *testing.T) {
	// The daemon does not have to run on a router. With nothing to read the
	// answer is an empty map and 200 — not a 404 and not a 500.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	labels, err := newLabelStore(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	info := newClientInfo(labels, []string{filepath.Join(t.TempDir(), "nope.leases")})
	info.enrichARP = func(map[string]*clientEntry) bool { return false }
	info.enrichBridge = func(map[string]*clientEntry) bool { return false }
	info.enrichWiFi = func(map[string]*clientEntry) bool { return false }

	snapshot := info.Snapshot()
	if len(snapshot.Clients) != 0 {
		t.Fatalf("expected an empty directory, got %+v", snapshot.Clients)
	}
	if len(snapshot.Sources) != 0 {
		t.Fatalf("no provider spoke, so sources must be empty, got %v", snapshot.Sources)
	}
}

func TestClientInfoRoutesPinnedToTrustedClients(t *testing.T) {
	// The directory names the devices on the operator's LAN — exactly the kind
	// of inventory that must not be readable by anyone who can reach the port.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := newTestController(t, &fakeReloader{}, nil)
	control.clientInfo = fixedClientInfo(t, stateStore)
	control.clients = newTestRegistry(t)
	handler := control.adminHandler("")

	request := httptest.NewRequest(http.MethodGet, "/admin/clients-info", nil)
	status, payload := serveAdmin(t, handler, request)
	if status != http.StatusUnauthorized {
		t.Fatalf("an uncertified client must be refused, got %d", status)
	}
	if text, _ := payload["error"].(string); !strings.Contains(text, "client certificate not trusted") {
		t.Fatalf("expected the not-trusted error, payload: %v", payload)
	}
	for _, route := range []string{
		"/admin/clients-info/labels/192.168.20.7",
	} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			request = httptest.NewRequest(method, route, strings.NewReader(`{"name":"x"}`))
			if status, _ = serveAdmin(t, handler, request); status != http.StatusUnauthorized {
				t.Fatalf("%s %s must refuse an uncertified client, got %d", method, route, status)
			}
		}
	}
}

func TestClientInfoReachableRemotely(t *testing.T) {
	// Like the rest of the observability plane, this is for the launcher to
	// read from elsewhere — it must NOT be on the operator loopback-only path.
	stateStore, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := newTestController(t, &fakeReloader{}, nil)
	control.clientInfo = fixedClientInfo(t, stateStore)
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

	request := httptest.NewRequest(http.MethodGet, "/admin/clients-info", nil)
	request.RemoteAddr = "203.0.113.7:51000" // decidedly not loopback
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted}}
	if status, payload := serveAdmin(t, control.adminHandler(""), request); status != http.StatusOK {
		t.Fatalf("a trusted remote client must reach the directory, got %d %v", status, payload)
	}
}

func TestUnreadableLabelsDoNotBlockTheDaemon(t *testing.T) {
	// A corrupt labels file must not keep the control channel down — the whole
	// design principle of this daemon is that it comes up when the payload is
	// broken. newLabelStore reports the failure; the caller degrades.
	dir := t.TempDir()
	stateStore, err := newStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "client-labels.json"), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = newLabelStore(stateStore); err == nil {
		t.Fatal("a corrupt labels file must be reported, not silently ignored")
	}
	// The fallback serves an empty map and can still be written to.
	fallback := emptyLabelStore(stateStore)
	if len(fallback.All()) != 0 {
		t.Fatal("the fallback store must start empty")
	}
	if err = fallback.Set("192.168.20.7", "Ноут"); err != nil {
		t.Fatalf("a later write must replace the unreadable file: %v", err)
	}
	reopened, err := newLabelStore(stateStore)
	if err != nil {
		t.Fatalf("the file must be valid JSON after a write: %v", err)
	}
	if reopened.All()["192.168.20.7"] != "Ноут" {
		t.Fatal("the rewritten file must hold the new label")
	}
}
