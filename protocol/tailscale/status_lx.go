//go:build with_gvisor

package tailscale

import (
	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/tailscale/ipn/ipnlocal"
	"github.com/sagernet/tailscale/ipn/ipnstate"
)

// lx: SPEC 115 — on-demand Tailscale status and the per-peer path verdict.

var _ adapter.TailscaleStatusProvider = (*Endpoint)(nil)

// TailscaleStatus returns a fresh backend snapshot: the same shape the status
// stream delivers, so a consumer needs no second source. Nothing is cached;
// each call runs LocalBackend.Status(), which walks the peer map and the
// wireguard-go UAPI dump — cheap at tailnet sizes, and only on request.
func (t *Endpoint) TailscaleStatus() (*adapter.TailscaleEndpointStatus, error) {
	if !t.started.Load() {
		return nil, E.New("tailscale endpoint is not started")
	}
	return t.collectTailscaleStatus(t.server.ExportLocalBackend()), nil
}

// collectTailscaleStatus is the single builder behind the stream and the
// on-demand getter.
func (t *Endpoint) collectTailscaleStatus(localBackend *ipnlocal.LocalBackend) *adapter.TailscaleEndpointStatus {
	result := convertTailscaleStatus(localBackend.Status())
	result.KeyAuth = t.keyAuth
	canShareFiles, taildropTargets := t.taildropTargets()
	result.CanShareFiles = canShareFiles
	result.WaitingFileCount = t.taildrop.waitingFileCount()
	result.ReceivingFileCount = t.taildrop.receivingFileCount()
	result.UnreadFileCount = t.taildrop.unreadFileCount()
	result.CertDomains = t.server.CertDomains()
	if len(taildropTargets) > 0 {
		for _, group := range result.UserGroups {
			for _, peer := range group.Peers {
				peer.CanReceiveFiles = taildropTargets[peer.StableID]
			}
		}
	}
	return result
}

// fillTailscalePeerPath applies magicsock's choice as reported in
// ipnstate.PeerStatus: CurAddr is set only when the next send would go direct,
// PeerRelay when it would go through a peer relay, and LastWrite is non-zero
// once the node has ever sent to the peer (then without a direct path the
// packets go through DERP). Relay is the peer's home DERP region, known even
// before any traffic, so it is reported for every path.
func fillTailscalePeerPath(result *adapter.TailscalePeer, peer *ipnstate.PeerStatus) {
	result.Endpoint = peer.CurAddr
	result.PeerRelay = peer.PeerRelay
	result.DERPRegionCode = peer.Relay
	if !peer.LastHandshake.IsZero() {
		result.LastHandshake = peer.LastHandshake.Unix()
	}
	switch {
	case peer.CurAddr != "":
		result.Path = adapter.TailscalePeerPathDirect
	case peer.PeerRelay != "":
		result.Path = adapter.TailscalePeerPathPeerRelay
	case !peer.LastWrite.IsZero():
		result.Path = adapter.TailscalePeerPathDERP
	default:
		result.Path = adapter.TailscalePeerPathNone
	}
}
