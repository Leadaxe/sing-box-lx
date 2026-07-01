package option

import "github.com/sagernet/sing/common/json/badoption"

// MASQUEOutboundOptions configures a MASQUE (CONNECT-IP / RFC 9484) outbound,
// primarily for Cloudflare WARP. See SPEC 021.
//
// NOTE on `network`: unlike every other outbound (where `network` selects the
// L4 protocol tcp/udp), here `network` selects the TRANSPORT (h3/h2) and the L4
// allow-list is `network_list`. Setting `"network": "tcp"` fails fast.
type MASQUEOutboundOptions struct {
	DialerOptions
	ServerOptions

	// Profile selects behaviour: "cloudflare" (default) or "standard" (RFC 9484).
	Profile string `json:"profile,omitempty"`
	// Network selects the TRANSPORT: "h3" (QUIC, default) or "h2" (HTTP/2).
	// This is NOT the tcp/udp L4 list — that is NetworkList below.
	Network string `json:"network,omitempty"`

	// Key material (required for the cloudflare profile). Base64-encoded DER:
	// PrivateKey via x509.ParseECPrivateKey, PublicKey via x509.ParsePKIXPublicKey.
	PrivateKey string `json:"private_key,omitempty"`
	PublicKey  string `json:"public_key,omitempty"`

	// Local tunnel addresses. At least one of IP/IPv6 is required. A bare
	// address without a mask is treated as /32 (v4) or /128 (v6).
	IP   string `json:"ip,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`

	// URI is the CONNECT-IP request URI template. Defaults per profile.
	URI string `json:"uri,omitempty"`
	// SNI overrides the TLS server name. Defaults per profile.
	SNI string `json:"sni,omitempty"`
	// MTU of the userspace stack. Defaults to 1280.
	MTU uint32 `json:"mtu,omitempty"`
	// SkipCertVerify disables public-key pinning (debug only).
	SkipCertVerify bool `json:"skip_cert_verify,omitempty"`

	// IdleTimeout suspends the tunnel after this long with no traffic (freeing
	// the userspace stack, pumps and QUIC keepalive); the next dial rebuilds it.
	// Empty = default (5m). A negative value disables idle-suspend.
	IdleTimeout badoption.Duration `json:"idle_timeout,omitempty"`
	// KeepAlivePeriod is the QUIC (h3) keepalive interval. Empty = 30s. A
	// negative value disables keepalive.
	KeepAlivePeriod badoption.Duration `json:"keep_alive_period,omitempty"`

	// L4 protocols routed through the tunnel: tcp and/or udp. Empty = both.
	NetworkList NetworkList `json:"network_list,omitempty"`
}
