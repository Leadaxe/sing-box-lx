//go:build with_lx_command

package daemon

import "github.com/sagernet/sing-box/adapter"

// peerStatusesToGRPC — SPEC 114: GroupItem.peers of a WG/AWG endpoint. A zero
// handshake time maps to 0 ("no handshake yet"), not to a negative Unix value.
func peerStatusesToGRPC(statuses []adapter.PeerStatus) []*PeerStatus {
	if len(statuses) == 0 {
		return nil
	}
	peers := make([]*PeerStatus, 0, len(statuses))
	for _, status := range statuses {
		var lastHandshake int64
		if !status.LastHandshake.IsZero() {
			lastHandshake = status.LastHandshake.Unix()
		}
		peers = append(peers, &PeerStatus{
			PublicKey:         status.PublicKey,
			Endpoint:          status.Endpoint,
			LastHandshakeUnix: lastHandshake,
			RxBytes:           int64(status.RxBytes),
			TxBytes:           int64(status.TxBytes),
		})
	}
	return peers
}
