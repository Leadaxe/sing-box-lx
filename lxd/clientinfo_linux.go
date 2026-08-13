//go:build with_lx_command && linux

package lxd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// providerTimeout bounds every external process a provider spawns. The owner
// measured ubus on a live router: ten runs finished inside busybox's timer
// resolution, so this is a hang guard, not a budget. Same discipline as
// execSelfCheck in apply.go — the only other exec in this package.
const providerTimeout = 2 * time.Second

// defaultLeaseFiles mirrors the core's list (route/neighbor_resolver_linux.go),
// OpenWrt's path first. Duplicated rather than exported from route/ because
// the SPEC's boundary is "no kernel edits" — and a five-entry list of distro
// conventions is cheaper to copy than a new export is to maintain.
func defaultLeaseFiles() []string {
	return []string{
		"/tmp/dhcp.leases",
		"/var/lib/dhcp/dhcpd.leases",
		"/var/lib/dhcpd/dhcpd.leases",
		"/var/lib/kea/kea-leases4.csv",
		"/var/lib/kea/kea-leases6.csv",
	}
}

// runProvider executes a helper binary, returning ok=false when the binary is
// absent, fails, or times out. A missing tool is a STATE, not an error: the
// same single branch on the client covers "not this platform" and "not on this
// host", exactly like currentRSS() returning rssUnsupported.
func runProvider(name string, args ...string) (string, bool) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

// enrichARP reads /proc/net/arp — a file, not a process, the same class of
// operation as reading leases:
//
//	IP address     HW type  Flags  HW address         Mask  Device
//	192.168.20.51  0x1      0x2    dc:a6:32:de:ad:be  *     br-lan
//
// It also closes the static-IP hole: a client absent from the leases still
// gets a mac (the key an operator label can hang on) and an iface. The entry
// only lives while traffic flows, and only for neighbors in the same L2
// segment — a device silent for minutes drops out of this source and remains
// in lease/label alone.
func enrichARP(clients map[string]*clientEntry) bool {
	file, err := os.Open("/proc/net/arp")
	if err != nil {
		return false
	}
	defer file.Close()
	return parseARPTable(file, clients)
}

// parseARPTable is enrichARP's parsing half, split out so it can be tested on
// captured output instead of whatever neighbors the test host happens to have.
func parseARPTable(source io.Reader, clients map[string]*clientEntry) bool {
	scanner := bufio.NewScanner(source)
	var seenHeader bool
	var found bool
	for scanner.Scan() {
		if !seenHeader {
			seenHeader = true
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}
		// Flags 0x0 is an incomplete entry — an ARP request that got no reply,
		// with the MAC filled in as all zeroes. Those are not clients.
		flags, err := strconv.ParseUint(strings.TrimPrefix(fields[2], "0x"), 16, 32)
		if err != nil || flags == 0 {
			continue
		}
		address, err := netip.ParseAddr(fields[0])
		if err != nil {
			continue
		}
		mac, err := net.ParseMAC(fields[3])
		if err != nil {
			continue
		}
		entry := entryFor(clients, address.String())
		entry.Mac = mac.String()
		entry.Iface = fields[5]
		entry.addSource("arp")
		found = true
	}
	return found
}

// enrichBridge maps MAC → physical port via `bridge fdb show`:
//
//	dc:a6:32:de:ad:be dev lan2 master br-lan
//
// It writes Port only and never touches Iface: the bridge (br-lan, from ARP)
// and the port (lan2) describe different levels — which segment versus which
// socket — and an operator wants both.
func enrichBridge(clients map[string]*clientEntry) bool {
	output, ok := runProvider("bridge", "fdb", "show")
	if !ok {
		return false
	}
	ports := parseBridgeFDB(output)
	if len(ports) == 0 {
		return false
	}
	return applyBridgePorts(clients, ports)
}

// parseBridgeFDB turns `bridge fdb show` output into MAC → physical port.
func parseBridgeFDB(output string) map[string]string {
	ports := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mac, err := net.ParseMAC(fields[0])
		if err != nil {
			continue
		}
		// `permanent` and `self` entries are the bridge's own addresses, not
		// the addresses of clients behind it.
		if strings.Contains(line, " permanent") || strings.Contains(line, " self") {
			continue
		}
		var port string
		for index := 1; index < len(fields)-1; index++ {
			if fields[index] == "dev" {
				port = fields[index+1]
				break
			}
		}
		if port == "" {
			continue
		}
		ports[mac.String()] = port
	}
	return ports
}

// applyBridgePorts joins the MAC-keyed fdb onto the IP-keyed directory through
// the MAC an earlier provider supplied; a client without one is skipped.
func applyBridgePorts(clients map[string]*clientEntry, ports map[string]string) bool {
	var found bool
	for _, entry := range clients {
		if entry.Mac == "" {
			continue
		}
		if port, ok := ports[entry.Mac]; ok {
			entry.Port = port
			entry.addSource("bridge")
			found = true
		}
	}
	return found
}

// ubusStatus is the slice of `ubus call hostapd.<iface> get_status` we need.
type ubusStatus struct {
	SSID string `json:"ssid"`
}

// ubusClients is the slice of `ubus call hostapd.<iface> get_clients` we need:
// the MAC list alone, keyed by address. Per-client fields (RSSI and friends)
// are deliberately unread — they live for seconds and this map is cached for a
// minute, so reporting them would only mislead.
type ubusClients struct {
	Clients map[string]json.RawMessage `json:"clients"`
}

// wirelessStation is what the Wi-Fi provider learns about one associated MAC.
type wirelessStation struct{ ssid, iface string }

// hostapdInterfaces picks the AP interfaces out of `ubus list`. Enumerated
// rather than hard-coded: the UCI section name (wireless.vpn_2g) is not the
// interface name (phy0-ap1), the two are bound when hostapd starts, and the
// config file shows what was asked for rather than what came up.
func hostapdInterfaces(listing string) []string {
	var interfaces []string
	for _, object := range strings.Split(listing, "\n") {
		object = strings.TrimSpace(object)
		if !strings.HasPrefix(object, "hostapd.") {
			continue
		}
		if iface := strings.TrimPrefix(object, "hostapd."); iface != "" {
			interfaces = append(interfaces, iface)
		}
	}
	return interfaces
}

// parseUbusClients folds one interface's get_clients output into the station
// map, keyed by canonical MAC — the form the directory joins on. Unparsable
// output contributes nothing and is not an error: an interface can be down.
func parseUbusClients(raw string, iface string, ssid string, stations map[string]wirelessStation) {
	var associated ubusClients
	if json.Unmarshal([]byte(raw), &associated) != nil {
		return
	}
	for address := range associated.Clients {
		mac, err := net.ParseMAC(address)
		if err != nil {
			continue
		}
		stations[mac.String()] = wirelessStation{ssid: ssid, iface: iface}
	}
}

// enrichWireless asks ubus which stations are associated with each hostapd
// interface, giving MAC → {ssid, iface}. Interfaces are enumerated from
// `ubus list` rather than hard-coded, because the UCI section name
// (wireless.vpn_2g) is not the interface name (phy0-ap1) — the two are bound
// when hostapd starts, and the config file shows what was asked for, not what
// came up.
//
// Chosen over iwinfo (SSID only, then a second lookup for stations, and text
// parsing instead of JSON) and over speaking the binary ubus protocol
// directly, which would mean a new dependency for two calls a minute.
func enrichWireless(clients map[string]*clientEntry) bool {
	listing, ok := runProvider("ubus", "list")
	if !ok {
		return false
	}

	// MAC → interface + SSID for every associated station.
	stations := make(map[string]wirelessStation)
	for _, iface := range hostapdInterfaces(listing) {
		object := "hostapd." + iface
		var ssid string
		if raw, statusOK := runProvider("ubus", "call", object, "get_status"); statusOK {
			var status ubusStatus
			if json.Unmarshal([]byte(raw), &status) == nil {
				ssid = status.SSID
			}
		}
		if raw, clientsOK := runProvider("ubus", "call", object, "get_clients"); clientsOK {
			parseUbusClients(raw, iface, ssid, stations)
		}
	}
	if len(stations) == 0 {
		return false
	}

	var found bool
	for _, entry := range clients {
		if entry.Mac == "" {
			continue
		}
		if associated, ok := stations[entry.Mac]; ok {
			entry.SSID = associated.ssid
			// Overwrites the bridge ARP reported: this is the precise AP.
			entry.Iface = associated.iface
			entry.addSource("wireless")
			found = true
		}
	}
	return found
}
