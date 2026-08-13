//go:build with_lx_command && linux

package lxd

// The parsing halves of the Linux providers, exercised on captured output.
// This is how the wired-client acceptance items are closed: the owner's stand
// has no wired clients, so `bridge fdb show` cannot be verified in the field.

import (
	"strings"
	"testing"
)

func TestParseARPTableSkipsIncompleteEntries(t *testing.T) {
	table := strings.Join([]string{
		"IP address       HW type     Flags       HW address            Mask     Device",
		"192.168.20.51    0x1         0x2         dc:a6:32:de:ad:be     *        br-lan",
		"192.168.20.238   0x1         0x2         be:ab:bd:ec:70:40     *        br-lan",
		// Flags 0x0 — an ARP request that got no reply. The MAC is all zeroes
		// and the host is not actually there.
		"192.168.20.99    0x1         0x0         00:00:00:00:00:00     *        br-lan",
		"garbage line",
		"",
	}, "\n")

	clients := make(map[string]*clientEntry)
	if !parseARPTable(strings.NewReader(table), clients) {
		t.Fatal("a table with live entries must report the provider as having spoken")
	}
	if len(clients) != 2 {
		t.Fatalf("expected the two complete entries, got %d: %+v", len(clients), clients)
	}
	if _, present := clients["192.168.20.99"]; present {
		t.Fatal("an incomplete entry (Flags 0x0) must not enter the map")
	}
	entry := clients["192.168.20.51"]
	if entry.Mac != "dc:a6:32:de:ad:be" || entry.Iface != "br-lan" {
		t.Fatalf("mac and iface must come from the table, got %+v", entry)
	}
	if entry.Source != "arp" {
		t.Fatalf("source must name the provider, got %q", entry.Source)
	}
}

func TestParseARPTableEmpty(t *testing.T) {
	// Header only: no neighbors. That is a state, not a failure — the caller
	// reports the provider as silent and the other sources still stand.
	table := "IP address       HW type     Flags       HW address            Mask     Device\n"
	clients := make(map[string]*clientEntry)
	if parseARPTable(strings.NewReader(table), clients) {
		t.Fatal("an empty table must report the provider as silent")
	}
	if len(clients) != 0 {
		t.Fatalf("no entries expected, got %+v", clients)
	}
}

func TestParseBridgeFDBSkipsBridgeOwnAddresses(t *testing.T) {
	output := strings.Join([]string{
		"dc:a6:32:de:ad:be dev lan2 master br-lan",
		"be:ab:bd:ec:70:40 dev phy0-ap1 master br-lan",
		// The bridge's own addresses, not clients behind it.
		"33:33:00:00:00:01 dev lan1 self permanent",
		"aa:bb:cc:00:11:22 dev br-lan vlan 1 master br-lan permanent",
		"malformed",
		"",
	}, "\n")

	ports := parseBridgeFDB(output)
	if len(ports) != 2 {
		t.Fatalf("expected two client entries, got %d: %v", len(ports), ports)
	}
	if ports["dc:a6:32:de:ad:be"] != "lan2" {
		t.Fatalf("wired client must map to its physical port, got %q", ports["dc:a6:32:de:ad:be"])
	}
	if _, present := ports["33:33:00:00:00:01"]; present {
		t.Fatal("a `self` entry is the bridge's own address, not a client")
	}
	if _, present := ports["aa:bb:cc:00:11:22"]; present {
		t.Fatal("a `permanent` entry is the bridge's own address, not a client")
	}
}

func TestBridgePortsAppliedByMAC(t *testing.T) {
	// The fdb is keyed by MAC and the directory by IP: the join must go
	// through the MAC an earlier provider supplied, and a client without one
	// is simply skipped.
	clients := map[string]*clientEntry{
		"192.168.20.51": {Mac: "dc:a6:32:de:ad:be", Iface: "br-lan"},
		"192.168.20.77": {},
	}
	ports := map[string]string{"dc:a6:32:de:ad:be": "lan2"}
	if !applyBridgePorts(clients, ports) {
		t.Fatal("a matching MAC must count as the provider having spoken")
	}
	if clients["192.168.20.51"].Port != "lan2" {
		t.Fatalf("port must be filled, got %+v", clients["192.168.20.51"])
	}
	if clients["192.168.20.51"].Iface != "br-lan" {
		t.Fatal("the bridge provider must leave iface alone — segment and socket are different levels")
	}
	if clients["192.168.20.77"].Port != "" {
		t.Fatal("a client with no MAC cannot be joined to the fdb")
	}
}

func TestParseUbusClientsCollectsStations(t *testing.T) {
	stations := make(map[string]wirelessStation)
	parseUbusClients(`{"clients":{"BE:AB:BD:EC:70:40":{"assoc":true},"6a:b8:11:22:33:44":{}}}`,
		"phy0-ap1", "LexVPN2G", stations)
	if len(stations) != 2 {
		t.Fatalf("expected both stations, got %v", stations)
	}
	// Keys must be canonical MACs — the directory joins on that form.
	station, ok := stations["be:ab:bd:ec:70:40"]
	if !ok {
		t.Fatalf("MAC keys must be canonicalised to lower case, got %v", stations)
	}
	if station.ssid != "LexVPN2G" || station.iface != "phy0-ap1" {
		t.Fatalf("station must carry ssid and the precise AP interface, got %+v", station)
	}
}

func TestParseUbusClientsIgnoresGarbage(t *testing.T) {
	stations := make(map[string]wirelessStation)
	parseUbusClients("not json", "phy0-ap1", "LexVPN2G", stations)
	parseUbusClients(`{"clients":{"nonsense":{}}}`, "phy0-ap1", "LexVPN2G", stations)
	if len(stations) != 0 {
		t.Fatalf("unparsable output must add nothing, got %v", stations)
	}
}

func TestHostapdInterfacesFromUbusList(t *testing.T) {
	listing := strings.Join([]string{
		"dhcp",
		"hostapd.phy0-ap0",
		"hostapd.phy0-ap1",
		"hostapd", // the bare object is not an interface
		"network.interface.lan",
		"",
	}, "\n")
	interfaces := hostapdInterfaces(listing)
	if len(interfaces) != 2 {
		t.Fatalf("expected the two AP interfaces, got %v", interfaces)
	}
	if interfaces[0] != "phy0-ap0" || interfaces[1] != "phy0-ap1" {
		t.Fatalf("interface names must be the object suffix, got %v", interfaces)
	}
}
