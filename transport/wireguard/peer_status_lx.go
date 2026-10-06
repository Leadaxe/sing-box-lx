package wireguard

// lx: SPEC 114 — per-peer status (last handshake, endpoint, transfer) for
// GetOutbounds, parsed from the device's UAPI dump.

import (
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// PeerStatuses returns one entry per configured peer, in config order, or nil
// while there is no device (never built, torn down, closed). The read runs
// under pauseOpAccess — the mutex Close and Teardown release the device under —
// so it never touches a device that is being shut down. Off the hot path: it
// runs only on an explicit observability pull.
func (e *Endpoint) PeerStatuses() []adapter.PeerStatus {
	e.pauseOpAccess.Lock()
	defer e.pauseOpAccess.Unlock()
	if e.device == nil {
		return nil
	}
	ipc, err := e.device.IpcGet()
	if err != nil {
		return nil
	}
	return orderPeerStatuses(parsePeerStatuses(ipc), e.peers)
}

// parsePeerStatuses extracts the per-peer block of a UAPI "get" dump, keyed by
// the hex public key. Device-level lines (private_key, listen_port, AWG knobs)
// precede the first public_key and are never read; preshared_key is skipped,
// so no secret leaves this function.
func parsePeerStatuses(ipc string) map[string]*adapter.PeerStatus {
	statuses := make(map[string]*adapter.PeerStatus)
	var (
		current       *adapter.PeerStatus
		handshakeSec  int64
		handshakeNsec int64
	)
	flush := func() {
		if current != nil && (handshakeSec != 0 || handshakeNsec != 0) {
			current.LastHandshake = time.Unix(handshakeSec, handshakeNsec)
		}
	}
	for _, line := range strings.Split(ipc, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if key == "public_key" {
			flush()
			handshakeSec, handshakeNsec = 0, 0
			current = &adapter.PeerStatus{PublicKey: hexKeyToBase64(value)}
			statuses[value] = current
			continue
		}
		if current == nil {
			continue
		}
		switch key {
		case "endpoint":
			current.Endpoint = value
		case "last_handshake_time_sec":
			handshakeSec, _ = strconv.ParseInt(value, 10, 64)
		case "last_handshake_time_nsec":
			handshakeNsec, _ = strconv.ParseInt(value, 10, 64)
		case "rx_bytes":
			current.RxBytes, _ = strconv.ParseUint(value, 10, 64)
		case "tx_bytes":
			current.TxBytes, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	flush()
	return statuses
}

// orderPeerStatuses lays the parsed statuses out in config order (the UAPI dump
// iterates a map, so its order is random); a peer the device knows but the
// config does not is appended after, which should not happen.
func orderPeerStatuses(statuses map[string]*adapter.PeerStatus, peers []peerConfig) []adapter.PeerStatus {
	result := make([]adapter.PeerStatus, 0, len(statuses))
	for _, peer := range peers {
		if status, loaded := statuses[peer.publicKeyHex]; loaded {
			result = append(result, *status)
			delete(statuses, peer.publicKeyHex)
		}
	}
	for _, status := range statuses {
		result = append(result, *status)
	}
	return result
}

func hexKeyToBase64(hexKey string) string {
	keyBytes, err := hex.DecodeString(hexKey)
	if err != nil {
		return hexKey
	}
	return base64.StdEncoding.EncodeToString(keyBytes)
}
