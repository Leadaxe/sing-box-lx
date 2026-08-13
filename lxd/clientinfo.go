//go:build with_lx_command

package lxd

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/route"
	E "github.com/sagernet/sing/common/exceptions"
)

// clientInfoTTL caches the whole map. Leases are read from disk and the
// wireless/bridge providers spawn a process — each cheap, none free, and a UI
// polling a connection inspector will ask often. The data itself changes on a
// scale of hours, so a minute of staleness costs nothing. Same shape as
// memorySnapshotTTL (SPEC 065), only longer.
const clientInfoTTL = 60 * time.Second

// clientEntry is one row of the IP → device directory. Every field is a plain
// string and an empty one is a VALID state — "this source said nothing about
// the client" — not an error. A consumer renders what it got.
type clientEntry struct {
	// Name is the human label: the DHCP hostname the device announced (option
	// 12) or an operator label. Empty for a static-IP client with no label.
	Name string `json:"name"`
	Mac  string `json:"mac"`
	// SSID is Wi-Fi only; a wired client legitimately has none.
	SSID string `json:"ssid"`
	// Iface is the interface the client is attached to — phy0-ap1 for Wi-Fi,
	// br-lan for wired. On a router with VLANs or a guest network this answers
	// "which segment did this come from".
	Iface string `json:"iface"`
	// Port is the physical bridge port (lan2) of a wired client: Iface says
	// which segment, Port says which socket it is plugged into.
	Port string `json:"port"`
	// Source lists the providers that contributed, joined with "+". Mandatory:
	// when a device loses its name the first question is always which source
	// went quiet — source: "label" alone says the DHCP lease expired.
	Source string `json:"source"`
}

// addSource appends a provider to the entry's provenance, keeping call order
// (which is priority order) and never duplicating.
func (e *clientEntry) addSource(name string) {
	for _, existing := range strings.Split(e.Source, "+") {
		if existing == name {
			return
		}
	}
	if e.Source == "" {
		e.Source = name
		return
	}
	e.Source += "+" + name
}

// clientInfoSnapshot is the response of GET /admin/clients-info.
type clientInfoSnapshot struct {
	Clients map[string]*clientEntry `json:"clients"`
	// Sources names the providers that actually produced data, so an operator
	// can tell "no Wi-Fi clients" from "ubus is missing on this box".
	Sources     []string `json:"sources"`
	UpdatedUnix int64    `json:"updated_unix"`
}

// clientInfo builds and caches the directory. Providers are plain functions
// selected by build tag (clientinfo_linux.go / clientinfo_stub.go) rather than
// an interface — the same shape as currentRSS()/peakRSS() in SPEC 065, and the
// interfaces in this package exist for testability, not for platforms.
type clientInfo struct {
	labels     *labelStore
	leaseFiles []string

	// Test seams. The enrich* functions are package-level and platform-bound;
	// swapping them here keeps unit tests off the host's real ARP table.
	now          func() time.Time
	loadLeases   func(files []string) (map[netip.Addr]net.HardwareAddr, map[netip.Addr]string, map[string]string)
	enrichARP    func(map[string]*clientEntry) bool
	enrichBridge func(map[string]*clientEntry) bool
	enrichWiFi   func(map[string]*clientEntry) bool

	access  sync.Mutex
	cached  clientInfoSnapshot
	takenAt time.Time
	valid   bool
}

func newClientInfo(labels *labelStore, leaseFiles []string) *clientInfo {
	if len(leaseFiles) == 0 {
		leaseFiles = defaultLeaseFiles()
	}
	return &clientInfo{
		labels:       labels,
		leaseFiles:   leaseFiles,
		now:          time.Now,
		loadLeases:   route.ReloadLeaseFiles,
		enrichARP:    enrichARP,
		enrichBridge: enrichBridge,
		enrichWiFi:   enrichWireless,
	}
}

// Snapshot returns the directory, rebuilding it at most once per clientInfoTTL.
func (c *clientInfo) Snapshot() clientInfoSnapshot {
	c.access.Lock()
	defer c.access.Unlock()
	if c.valid && c.now().Sub(c.takenAt) < clientInfoTTL {
		return c.cached
	}
	c.cached = c.build()
	c.takenAt = c.now()
	c.valid = true
	return c.cached
}

// SetLabel records an operator label and invalidates the cache: a label write
// is an explicit act, and having to wait out the TTL to see it take effect
// would read as the write being lost.
func (c *clientInfo) SetLabel(key string, name string) error {
	if err := c.labels.Set(key, name); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

func (c *clientInfo) DeleteLabel(key string) error {
	if err := c.labels.Delete(key); err != nil {
		return err
	}
	c.invalidate()
	return nil
}

func (c *clientInfo) invalidate() {
	c.access.Lock()
	defer c.access.Unlock()
	c.valid = false
}

// build runs every provider over one shared accumulator map, mirroring how
// ReloadLeaseFiles merges several lease files: each provider fills the fields
// it knows, a missing source is skipped silently. Call order IS priority —
// later providers overwrite earlier ones for the fields they own.
func (c *clientInfo) build() clientInfoSnapshot {
	clients := make(map[string]*clientEntry)
	var sources []string

	// lease: name + mac, the only source of a DHCP hostname.
	if files := existingFiles(c.leaseFiles); len(files) > 0 {
		ipToMAC, ipToHostname, macToHostname := c.loadLeases(files)
		for address, mac := range ipToMAC {
			entry := entryFor(clients, address.String())
			entry.Mac = mac.String()
			entry.addSource("lease")
		}
		for address, hostname := range ipToHostname {
			entry := entryFor(clients, address.String())
			entry.Name = hostname
			entry.addSource("lease")
		}
		// A lease may carry the hostname keyed by MAC only (odhcpd's IPv6
		// lines); fill entries that got a MAC but no name.
		for _, entry := range clients {
			if entry.Name == "" && entry.Mac != "" {
				if hostname := macToHostname[entry.Mac]; hostname != "" {
					entry.Name = hostname
				}
			}
		}
		if len(clients) > 0 {
			sources = append(sources, "lease")
		}
	}

	// arp: mac + bridge-level iface. Runs after lease because it is fresher —
	// a lease can outlive the MAC that took it (device re-randomized its
	// address), while the neighbor table reflects who answers for this IP now.
	if c.enrichARP(clients) {
		sources = append(sources, "arp")
	}
	// bridge: physical port. Writes Port only, never Iface — the two are
	// different levels (segment vs socket) and both are useful.
	if c.enrichBridge(clients) {
		sources = append(sources, "bridge")
	}
	// wireless: ssid + the precise AP interface. Deliberately after arp: for a
	// Wi-Fi client ARP reports the bridge (br-lan) and ubus refines it to
	// phy0-ap1 — the more precise source overwrites the less precise one.
	if c.enrichWiFi(clients) {
		sources = append(sources, "wireless")
	}

	// label: the operator's word, last and final.
	if c.applyLabels(clients) {
		sources = append(sources, "label")
	}

	return clientInfoSnapshot{
		Clients:     clients,
		Sources:     sources,
		UpdatedUnix: c.now().Unix(),
	}
}

// applyLabels overrides names from the operator's file. A label keyed by MAC
// wins over one keyed by IP: the MAC follows the device across addresses. See
// the SPEC caveat — with MAC randomization on modern phones that stops being
// true, and an IP label plus a DHCP reservation is the stabler pairing.
func (c *clientInfo) applyLabels(clients map[string]*clientEntry) bool {
	labels := c.labels.All()
	if len(labels) == 0 {
		return false
	}
	var used bool
	for address, name := range labels {
		// A label may name a client no other source knows (a static-IP device
		// that is currently silent): it still belongs in the directory.
		if _, isIP := netip.ParseAddr(address); isIP == nil {
			entry := entryFor(clients, address)
			entry.Name = name
			entry.addSource("label")
			used = true
		}
	}
	for _, entry := range clients {
		if entry.Mac == "" {
			continue
		}
		if name, ok := labels[entry.Mac]; ok {
			entry.Name = name
			entry.addSource("label")
			used = true
		}
	}
	return used
}

// entryFor is the accumulator's get-or-create.
func entryFor(clients map[string]*clientEntry, address string) *clientEntry {
	entry, ok := clients[address]
	if !ok {
		entry = &clientEntry{}
		clients[address] = entry
	}
	return entry
}

// existingFiles drops lease paths that are absent or empty, so a default list
// naming five distros' locations costs nothing on a box that has one.
func existingFiles(paths []string) []string {
	var present []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Size() > 0 {
			present = append(present, path)
		}
	}
	return present
}

// labelStore is the operator's IP/MAC → name file. Small, read rarely, written
// through the same tmp+fsync+rename discipline as every other daemon state
// file, so a crash mid-write never leaves a torn map.
type labelStore struct {
	path   string
	writer *store

	access sync.RWMutex
	labels map[string]string
}

func newLabelStore(stateStore *store) (*labelStore, error) {
	labels := &labelStore{
		path:   filepath.Join(stateStore.dir, "client-labels.json"),
		writer: stateStore,
		labels: make(map[string]string),
	}
	content, err := os.ReadFile(labels.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return labels, nil
		}
		return nil, E.Cause(err, "read client labels")
	}
	if len(content) == 0 {
		return labels, nil
	}
	if err = json.Unmarshal(content, &labels.labels); err != nil {
		return nil, E.Cause(err, "parse client labels")
	}
	if labels.labels == nil {
		labels.labels = make(map[string]string)
	}
	return labels, nil
}

// emptyLabelStore is the fallback when the labels file cannot be read: the
// daemon serves the directory without labels, and the next write replaces the
// unreadable file wholesale.
func emptyLabelStore(stateStore *store) *labelStore {
	return &labelStore{
		path:   filepath.Join(stateStore.dir, "client-labels.json"),
		writer: stateStore,
		labels: make(map[string]string),
	}
}

func (l *labelStore) All() map[string]string {
	if l == nil {
		return nil
	}
	l.access.RLock()
	defer l.access.RUnlock()
	copied := make(map[string]string, len(l.labels))
	for key, value := range l.labels {
		copied[key] = value
	}
	return copied
}

func (l *labelStore) Set(key string, name string) error {
	l.access.Lock()
	defer l.access.Unlock()
	l.labels[key] = name
	return l.persistLocked()
}

func (l *labelStore) Delete(key string) error {
	l.access.Lock()
	defer l.access.Unlock()
	if _, ok := l.labels[key]; !ok {
		return nil
	}
	delete(l.labels, key)
	return l.persistLocked()
}

func (l *labelStore) persistLocked() error {
	encoded, err := json.MarshalIndent(l.labels, "", "  ")
	if err != nil {
		return err
	}
	return l.writer.writeAtomic(l.path, append(encoded, '\n'))
}

// normalizeLabelKey accepts an IP or a MAC and returns the canonical form used
// as the map key; anything else is rejected. The key never reaches a
// filesystem path, so this is not a traversal guard — it keeps junk keys from
// silently accumulating in a map nothing will ever match.
func normalizeLabelKey(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	if address, err := netip.ParseAddr(key); err == nil {
		return address.String(), true
	}
	if mac, err := net.ParseMAC(key); err == nil {
		return mac.String(), true
	}
	return "", false
}
