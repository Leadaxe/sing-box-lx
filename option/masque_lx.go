package option

import (
	"net/netip"

	"github.com/sagernet/sing/common/json/badoption"
)

// MASQUEClientLXOptions is the fork's part of the masque-client endpoint:
// what Cloudflare WARP needs on top of RFC 9484 and what the config cannot
// express otherwise. Embedded into MASQUEClientEndpointOptions. lx: SPEC 110.
type MASQUEClientLXOptions struct {
	// Profile selects the server's dialect: "standard" (RFC 9484, default) or
	// "cloudflare" (WARP: `cf-connect-ip` protocol token, plain CONNECT on
	// HTTP/2 with the cf-connect-proto header, the legacy datagram setting,
	// and a bounded wait for the CONNECT response before falling back from
	// HTTP/3 — WARP ignores QUIC from some source addresses without an error).
	Profile string `json:"profile,omitempty" enum:",standard,cloudflare"`
	// Address is the tunnel's own addresses when the server does not assign
	// them in-band (WARP hands them out at device registration). When set the
	// client does not send ADDRESS_REQUEST and is ready as soon as the tunnel
	// is open; an ADDRESS_ASSIGN from the server is then logged and ignored.
	Address badoption.Listable[netip.Prefix] `json:"address,omitempty"`
	// PrivateKey is the WARP registration key (base64 of the DER EC private
	// key), profile cloudflare only. The endpoint presents a self-signed client
	// certificate issued from it, so the config carries the key the
	// registration returned instead of a PEM pair made elsewhere. Conflicts
	// with tls.client_certificate / tls.client_key.
	PrivateKey string `json:"private_key,omitempty"`
	// PublicKey is the WARP endpoint key (base64 of the DER PKIX public key),
	// profile cloudflare only. The endpoint is authenticated by this key, not
	// by the name in its certificate; it becomes
	// tls.certificate_public_key_sha256. Conflicts with a pin set there.
	PublicKey string `json:"public_key,omitempty"`
}

const (
	MASQUEProfileStandard   = "standard"
	MASQUEProfileCloudflare = "cloudflare"
)
