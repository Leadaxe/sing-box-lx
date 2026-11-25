package adapter

// lx:begin SPEC 054 / SPEC 138 an outbound that dials the destination itself

import "errors"

// DestinationDialerLx is implemented by an outbound that, when a connection opens,
// goes to the destination itself through a local or userspace network stack or a
// tunnel of its own (MASQUE CONNECT-IP over gVisor), rather than asking a proxy
// server to go there. Its refused or reset open is the site's answer, not a dead
// node: SPEC 054 gives no penalty for it, and the SPEC 138 self-heal puts the site
// under suspicion (a probe of that node alone), not the node. direct and the
// endpoints (WireGuard, AWG, Tailscale, OpenVPN, OpenConnect) are known without it.
type DestinationDialerLx interface {
	DialsDestinationLx() bool
}

// lx:end SPEC 054 / SPEC 138

// lx:begin SPEC 138 a proxy that refused the destination

// DestinationRefusedLx wraps the error of an open through a proxy whose server
// answered that the destination failed (a SOCKS reply with a failure code, an HTTP
// CONNECT status such as 502): the proxy is alive and reached, the destination is
// at fault. Every other failed open through a proxy says nothing about the
// destination: it happened between us and the proxy's server. Tag is the outbound
// that answered so: a group takes the mark only from the leaf of its own node — a
// proxy on the way to that leaf's server (a detour) refusing it is the node's path
// failing, not the destination.
type DestinationRefusedLx struct {
	Tag string
	Err error
}

func (e *DestinationRefusedLx) Error() string { return e.Err.Error() }
func (e *DestinationRefusedLx) Unwrap() error { return e.Err }

// IsDestinationRefusedLx reports whether err carries DestinationRefusedLx.
func IsDestinationRefusedLx(err error) bool {
	var refused *DestinationRefusedLx
	return errors.As(err, &refused)
}

// lx:end SPEC 138
