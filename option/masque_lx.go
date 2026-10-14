package option

import "github.com/sagernet/sing/common/json/badoption"

// MASQUEOutboundOptions configures a MASQUE (CONNECT-IP / RFC 9484) outbound,
// primarily for Cloudflare WARP. See SPEC 021.
//
// The HTTP version carrying the tunnel (h3/h2) is `vhttp`; the tcp/udp
// allow-list is `network_list`, as in every other outbound. TLS goes in the
// standard `tls` block.
//
// Legacy flat `sni` / `skip_cert_verify` / `fragment*` still work and report a
// deprecation (SPEC 062). The legacy `network` key for the HTTP version (it
// meant the opposite of everyone else's `network`) was removed in v1.14.3-lx.18:
// the strict decoder rejects it as an unknown field.
type MASQUEOutboundOptions struct {
	DialerOptions
	ServerOptions
	// lx: SPEC 062 — the standard `tls: {…}` block, same container every other
	// TLS outbound uses. The flat sni/skip_cert_verify/fragment* fields below
	// are kept as aliases for it until v1.14.0-lx.30.
	OutboundTLSOptionsContainer

	// Profile selects behaviour: "cloudflare" (default) or "standard" (RFC 9484).
	Profile string `json:"profile,omitempty"`
	// VHTTP selects the HTTP version carrying CONNECT-IP: "auto" (default) —
	// try h3 and fall back to h2 when the QUIC handshake does not complete
	// (lx: SPEC 074; the endpoint may ignore QUIC from this source address,
	// which looks like a hang rather than an error) — or a fixed "h3" (QUIC) /
	// "h2". On the standard profile there is no h2 leg, so the default quietly
	// means h3 there. The tcp/udp allow-list is NetworkList, as everywhere
	// else.
	//
	// Not named `transport`: that key is an object (V2RayTransportOptions) on
	// vless/trojan/vmess, and reusing it for a string here would repeat exactly
	// the confusion this migration removes. lx: SPEC 062.
	//
	// There is deliberately no `network` here. It used to carry the HTTP
	// version — the opposite of `network` in every other outbound — and was
	// removed in v1.14.3-lx.18; the key is kept free so it can later take its
	// usual tcp/udp meaning (today that is NetworkList).
	VHTTP string `json:"vhttp,omitempty" enum:"h3,h2,auto"`

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
	// Absent inherits lx.masque.idle_timeout (off when that is unset too); an
	// explicit "0" or a negative value keeps this node's tunnel up until Close
	// whatever the global default. A pointer so that "0" and absent stay
	// distinguishable. lx: SPEC 098.
	IdleTimeout *badoption.Duration `json:"idle_timeout,omitempty"`
	// KeepAlivePeriod is the QUIC (h3) keepalive interval. Empty = 30s. A
	// negative value disables keepalive.
	KeepAlivePeriod badoption.Duration `json:"keep_alive_period,omitempty"`

	// L4 protocols routed through the tunnel: tcp and/or udp. Empty = both.
	NetworkList NetworkList `json:"network_list,omitempty"`
}
