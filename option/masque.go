package option

import "github.com/sagernet/sing/common/json/badoption"

// MASQUEOutboundOptions configures a MASQUE (CONNECT-IP / RFC 9484) outbound,
// primarily for Cloudflare WARP. See SPEC 021.
//
// The HTTP version carrying the tunnel (h3/h2) is `vhttp`; the tcp/udp
// allow-list is `network_list`, as in every other outbound. TLS goes in the
// standard `tls` block.
//
// Legacy shapes still work until v1.14.0-lx.30 and report a deprecation:
// `network` for the HTTP version (it meant the opposite of everyone else's
// `network`), and flat `sni` / `skip_cert_verify` / `fragment*` for their `tls`
// counterparts. See SPEC 062.
type MASQUEOutboundOptions struct {
	DialerOptions
	ServerOptions
	// lx: SPEC 062 — the standard `tls: {…}` block, same container every other
	// TLS outbound uses. The flat sni/skip_cert_verify/fragment* fields below
	// are kept as aliases for it until v1.14.0-lx.30.
	OutboundTLSOptionsContainer

	// Profile selects behaviour: "cloudflare" (default) or "standard" (RFC 9484).
	Profile string `json:"profile,omitempty"`
	// VHTTP selects the HTTP version carrying CONNECT-IP: "h3" (QUIC, default),
	// "h2", or "auto" — try h3 and fall back to h2 when the QUIC handshake does
	// not complete (lx: SPEC 074; the endpoint may ignore QUIC from this source
	// address, which looks like a hang rather than an error). The tcp/udp
	// allow-list is NetworkList, as everywhere else.
	//
	// Not named `transport`: that key is an object (V2RayTransportOptions) on
	// vless/trojan/vmess, and reusing it for a string here would repeat exactly
	// the confusion this migration removes. lx: SPEC 062.
	VHTTP string `json:"vhttp,omitempty" enum:"h3,h2,auto"`

	// Deprecated: use `vhttp`. Removed in v1.14.0-lx.30.
	//
	// This field means the HTTP version (h3/h2), not the tcp/udp list — the
	// opposite of `network` in every other outbound. That inversion is why it
	// is being retired; `network` is expected to take its usual meaning later.
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
	// MTU of the userspace stack. Defaults to 1280.
	MTU uint32 `json:"mtu,omitempty"`

	// The fields below moved into the `tls` block. Each is still honoured, and
	// using one reports a deprecation once per outbound. Removed in
	// v1.14.0-lx.30. lx: SPEC 062.
	//
	// Note on the bool ones: an unset field and an explicit `false` are
	// indistinguishable here, so only a legacy `true` can be carried over — see
	// resolveLegacyOptions.

	// Deprecated: use `tls.server_name`. Removed in v1.14.0-lx.30.
	SNI string `json:"sni,omitempty"`
	// Deprecated: use `tls.insecure`. Removed in v1.14.0-lx.30.
	SkipCertVerify bool `json:"skip_cert_verify,omitempty"`
	// Deprecated: use `tls.fragment`. Removed in v1.14.0-lx.30.
	Fragment bool `json:"fragment,omitempty"`
	// Deprecated: use `tls.fragment_fallback_delay`. Removed in v1.14.0-lx.30.
	FragmentFallbackDelay badoption.Duration `json:"fragment_fallback_delay,omitempty"`
	// Deprecated: use `tls.record_fragment`. Removed in v1.14.0-lx.30.
	RecordFragment bool `json:"record_fragment,omitempty"`

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
