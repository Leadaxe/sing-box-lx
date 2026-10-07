package adapter

// lx: SPEC 115 — on-demand status of a Tailscale endpoint and the path verdict
// per peer.

// TailscalePeer.Path values. The verdict is the core's, from magicsock's
// current choice: a direct address wins, then a peer relay, then DERP for a
// peer the node has ever sent to; a peer never sent to has no path.
const (
	TailscalePeerPathNone      = ""
	TailscalePeerPathDirect    = "direct"
	TailscalePeerPathPeerRelay = "peer_relay"
	TailscalePeerPathDERP      = "derp"
)

// TailscaleStatusProvider is implemented by the Tailscale endpoint. It
// returns the same snapshot SubscribeTailscaleStatus delivers, taken fresh
// from the backend at call time: peer paths, handshakes and health are
// current. Fails while the endpoint is not started.
type TailscaleStatusProvider interface {
	TailscaleStatus() (*TailscaleEndpointStatus, error)
}
