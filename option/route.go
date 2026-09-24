package option

import "github.com/sagernet/sing/common/json/badoption"

type RouteOptions struct {
	GeoIP                      *GeoIPOptions                     `json:"geoip,omitempty" schema:"omit"`
	Geosite                    *GeositeOptions                   `json:"geosite,omitempty" schema:"omit"`
	Rules                      []Rule                            `json:"rules,omitempty"`
	RuleSet                    []RuleSet                         `json:"rule_set,omitempty"`
	Final                      string                            `json:"final,omitempty" reference:"outbound"`
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
	// Deprecated aliases of the SPEC 020 idle keys, which moved to the root `lx`
	// block (SPEC 098): accepted for one release with a warning per key, folded
	// into lx.wg.* by option.ResolveLX and cleared from the resolved options. A
	// value set both here and in lx.wg that differs is a start error.
	//
	// Deprecated: use lx.wg.idle_suspend.
	LXIdleSuspend badoption.Duration `json:"lx_idle_suspend,omitempty"`
	// Deprecated: use lx.wg.idle_suspend_reachable.
	LXIdleSuspendReachable badoption.Duration `json:"lx_idle_suspend_reachable,omitempty"`
	// Deprecated: use lx.wg.idle_teardown. A pointer, like its replacement, so
	// that an explicit "0" (teardown disabled) survives as a value.
	LXIdleTeardown *badoption.Duration `json:"lx_idle_teardown,omitempty"`
	// lx:end idle-suspend
}

type GeoIPOptions struct {
	Path           string `json:"path,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty" reference:"outbound"`
}

type GeositeOptions struct {
	Path           string `json:"path,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty" reference:"outbound"`
}
