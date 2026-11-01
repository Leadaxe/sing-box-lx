package adapter

import "time"

// lx: SPEC 114 — per-peer status of a WG/AWG endpoint for observability.

// PeerStatus is a snapshot of one WireGuard peer, read from the device's UAPI
// dump. The core reports raw facts and makes no "connected" verdict: the
// liveness threshold depends on (possibly ranged) AWG rekey timings, so the
// consumer derives it.
type PeerStatus struct {
	// PublicKey is the peer's public key, standard base64 — the same string as
	// the peer's public_key in the config, so it doubles as the peer identity.
	PublicKey string
	// Endpoint is the peer's current remote address (ip:port). A peer without a
	// configured address (server side) learns it from the first authenticated
	// packet, and WireGuard roaming updates it whenever the peer moves. Empty
	// until known. It is the last known address, not a liveness signal.
	Endpoint string
	// LastHandshake is the time of the last completed handshake of the current
	// session; zero if none. A session starts when the device is built and
	// again at every Up after a Down (enable, wake from sleep, network wake):
	// Down destroys the keys, so an older handshake is not reported (SPEC 145).
	LastHandshake time.Time
	// RxBytes and TxBytes are the device totals since it was built, service
	// traffic (handshakes, keepalives) included.
	RxBytes uint64
	TxBytes uint64
	// SessionRxBytes and SessionTxBytes count from the start of the current
	// session (SPEC 145).
	SessionRxBytes uint64
	SessionTxBytes uint64
}

// PeerStatusReporter is implemented by WG/AWG endpoints. It returns nil while
// no device exists (never built, torn down, closed): the endpoint state
// (IdleStateReporter) explains why, and the call never builds or wakes one.
type PeerStatusReporter interface {
	PeerStatuses() []PeerStatus
}
