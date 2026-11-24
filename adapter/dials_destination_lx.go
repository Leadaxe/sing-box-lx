package adapter

// lx:begin SPEC 054 / SPEC 138 an outbound that dials the destination itself

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
