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
	// LXIdleSuspend is the idle threshold (XX) for SPEC 020 idle WG/AWG endpoint
	// suspend. A WG/AWG endpoint that is both unreachable from the active routing
	// tree and idle longer than this is brought Down (freeing its recv-worker
	// bufsArrs, cutting the multi-WG GC scan heat); the next dial through it wakes
	// it. 0 / absent disables the feature entirely (the idle tick never starts).
	LXIdleSuspend badoption.Duration `json:"lx_idle_suspend,omitempty"`
	// lx:end
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
