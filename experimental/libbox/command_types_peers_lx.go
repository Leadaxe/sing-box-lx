package libbox

// lx: SPEC 114 — per-peer status of a WG/AWG endpoint in GetOutbounds.

import "github.com/sagernet/sing-box/daemon"

// PeerStatus is one WireGuard peer of a WG/AWG endpoint. Raw facts, no
// "connected" verdict: derive liveness from LastHandshakeUnix (0 = none yet).
// Endpoint is the last known remote ip:port — configured, or learned from the
// peer's packets on the server side — and stays after the peer goes silent.
type PeerStatus struct {
	PublicKey         string
	Endpoint          string
	LastHandshakeUnix int64
	RxBytes           int64
	TxBytes           int64
}

type PeerStatusIterator interface {
	Next() *PeerStatus
	HasNext() bool
}

// Peers returns the endpoint's peers in config order; empty for every other
// outbound and while the endpoint has no device (see EndpointState).
func (i *OutboundGroupItem) Peers() PeerStatusIterator {
	return newIterator(i.peers)
}

func peerStatusesFromGRPC(peers []*daemon.PeerStatus) []*PeerStatus {
	if len(peers) == 0 {
		return nil
	}
	result := make([]*PeerStatus, 0, len(peers))
	for _, peer := range peers {
		result = append(result, &PeerStatus{
			PublicKey:         peer.PublicKey,
			Endpoint:          peer.Endpoint,
			LastHandshakeUnix: peer.LastHandshakeUnix,
			RxBytes:           peer.RxBytes,
			TxBytes:           peer.TxBytes,
		})
	}
	return result
}
