package libbox

// lx: SPEC 114 — status snapshot of a WG/AWG endpoint from GetWireGuardStatus.

import "github.com/sagernet/sing-box/daemon"

// WireGuardEndpointStatus is the answer of CommandClient.GetWireGuardStatus:
// device state, idle time and peers in one object, so an empty peer list is
// explained by EndpointState without a second call. Peers are reached through
// an iterator (gomobile: no slice fields).
type WireGuardEndpointStatus struct {
	EndpointTag string
	// never_built / building / up / asleep / torn_down / down / disabled — the
	// same value OutboundGroupItem.EndpointState shows in the node list.
	EndpointState    string
	IdleSinceSeconds int64
	peers            []*PeerStatus
}

// Peers returns the endpoint's peers in config order; empty while the
// endpoint has no device (see EndpointState).
func (s *WireGuardEndpointStatus) Peers() PeerStatusIterator {
	return newIterator(s.peers)
}

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

func wireGuardEndpointStatusFromGRPC(status *daemon.WireGuardEndpointStatus) *WireGuardEndpointStatus {
	return &WireGuardEndpointStatus{
		EndpointTag:      status.EndpointTag,
		EndpointState:    status.EndpointState,
		IdleSinceSeconds: status.IdleSinceSeconds,
		peers:            peerStatusesFromGRPC(status.Peers),
	}
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
