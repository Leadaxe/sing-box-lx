package option

import "github.com/sagernet/sing/common/json/badoption"

type RouteOptions struct {
	GeoIP                      *GeoIPOptions                     `json:"geoip,omitempty"`
	Geosite                    *GeositeOptions                   `json:"geosite,omitempty"`
	Rules                      []Rule                            `json:"rules,omitempty"`
	RuleSet                    []RuleSet                         `json:"rule_set,omitempty"`
	Final                      string                            `json:"final,omitempty"`
	FindProcess                bool                              `json:"find_process,omitempty"`
	FindNeighbor               bool                              `json:"find_neighbor,omitempty"`
	DHCPLeaseFiles             badoption.Listable[string]        `json:"dhcp_lease_files,omitempty"`
	AutoDetectInterface        bool                              `json:"auto_detect_interface,omitempty"`
	OverrideAndroidVPN         bool                              `json:"override_android_vpn,omitempty"`
	DefaultInterface           string                            `json:"default_interface,omitempty"`
	DefaultMark                FwMark                            `json:"default_mark,omitempty"`
	DefaultDomainResolver      *DomainResolveOptions             `json:"default_domain_resolver,omitempty"`
	DefaultNetworkStrategy     *NetworkStrategy                  `json:"default_network_strategy,omitempty"`
	DefaultNetworkType         badoption.Listable[InterfaceType] `json:"default_network_type,omitempty"`
	DefaultFallbackNetworkType badoption.Listable[InterfaceType] `json:"default_fallback_network_type,omitempty"`
	DefaultFallbackDelay       badoption.Duration                `json:"default_fallback_delay,omitempty"`
	DefaultHTTPClient          string                            `json:"default_http_client,omitempty"`
	// lx:begin idle-suspend
	// LXIdleSuspend is the idle threshold for SPEC 020 idle WG/AWG endpoint
	// suspend. A WG/AWG endpoint that is both unreachable from the active routing
	// tree and idle longer than this is brought Down (freeing its recv-worker
	// bufsArrs, cutting the multi-WG GC scan heat); the next dial through it wakes
	// it. 0 / absent disables the feature entirely (the idle tick never starts).
	LXIdleSuspend badoption.Duration `json:"lx_idle_suspend,omitempty"`
	// LXIdleSuspendReachable is the OPTIONAL second, longer idle threshold for
	// endpoints that ARE reachable from the active routing tree (a urltest pool
	// member, the selected node, final). Such an endpoint is kept live by default
	// (reachable = never suspended); with this set, it too is brought Down after
	// this idle window and woken lazily by the next dial (first dial pays ~1
	// handshake RTT). Must be >= lx_idle_suspend and requires it to be set.
	// Recommended >= the urltest idle_timeout of any group above the endpoint, so
	// health-check probes have already gone quiet before the endpoint sleeps.
	// 0 / absent keeps the default semantics (reachable endpoints never suspend).
	LXIdleSuspendReachable badoption.Duration `json:"lx_idle_suspend_reachable,omitempty"`
	// LXIdleTeardown is the third level: how long an ALREADY-SLEEPING endpoint
	// (suspended by either threshold above) may stay asleep before it is torn
	// down completely — device Closed, gVisor netstack freed (~5.9 MB/endpoint),
	// nothing left but its config. Counted FROM the moment it fell asleep, not
	// from the last dial. The next dial rebuilds it from scratch (~0.5-1 s:
	// new device + netstack + peer resolve + handshake) instead of the ~1 RTT a
	// suspended endpoint pays. Absent → defaults to lx_idle_suspend_reachable;
	// "0" disables teardown (endpoints stay merely suspended). Requires
	// lx_idle_suspend.
	LXIdleTeardown badoption.Duration `json:"lx_idle_teardown,omitempty"`
	// lx:end idle-suspend
}

type GeoIPOptions struct {
	Path           string `json:"path,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty"`
}

type GeositeOptions struct {
	Path           string `json:"path,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty"`
}
