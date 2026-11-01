package wireguard

// lx: SPEC 145 — per-peer session view of the status (SPEC 114).
//
// device.Down() zeroes the peers' keypairs and handshake state, but leaves
// lastHandshakeNano and the rx/tx counters alone, so after the next Up the
// UAPI dump still shows the destroyed session's handshake as if it were live.
// The session starts at the first Up after a Down (enable, wake from idle
// sleep, network wake after a pause): there the counters are snapshotted and
// the time stamped, and PeerStatuses reports the handshake of the current
// session only, plus the transfer since that snapshot next to the device
// totals. While the device stays down the previous session is still reported
// as it was. A device rebuild starts from zero counters, so Teardown drops the
// snapshot. wireguard-go itself is not touched.

import (
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

type peerSession struct {
	// down is set by every Down of a live device and consumed by the next Up,
	// so an Up of a device that is already up keeps the running session.
	down atomic.Bool
	base atomic.Pointer[peerSessionBase]
}

type peerSessionBase struct {
	since    time.Time
	counters map[string]peerCounters // hex public key
}

type peerCounters struct {
	rx uint64
	tx uint64
}

// markSessionDown records that the device has just gone down.
func (e *Endpoint) markSessionDown() {
	e.session.down.Store(true)
}

// markSessionUp starts a new session if the device was down. Called right
// before device.Up(): the counters do not move while the device is down, and
// no handshake can complete before Up, so stamping first loses nothing. A
// failed Up leaves the stamp in place, which is still true: no session.
func (e *Endpoint) markSessionUp() {
	if !e.session.down.Swap(false) {
		return
	}
	base := &peerSessionBase{since: time.Now()}
	if wgDevice := e.device; wgDevice != nil {
		if ipc, err := wgDevice.IpcGet(); err == nil {
			statuses := parsePeerStatuses(ipc)
			base.counters = make(map[string]peerCounters, len(statuses))
			for publicKeyHex, status := range statuses {
				base.counters[publicKeyHex] = peerCounters{rx: status.RxBytes, tx: status.TxBytes}
			}
		}
	}
	e.session.base.Store(base)
}

// resetSession forgets the session of a released device: the next one starts
// with zero counters.
func (e *Endpoint) resetSession() {
	e.session.down.Store(false)
	e.session.base.Store(nil)
}

// applySession rewrites the parsed statuses into the session view: a handshake
// older than the session start is dropped, and the session transfer is the
// device total minus the snapshot. Without a snapshot the session is the whole
// device lifetime. A total below its snapshot (cannot happen without a rebuild)
// counts as a fresh start rather than wrapping around.
func (e *Endpoint) applySession(statuses map[string]*adapter.PeerStatus) {
	base := e.session.base.Load()
	for publicKeyHex, status := range statuses {
		status.SessionRxBytes, status.SessionTxBytes = status.RxBytes, status.TxBytes
		if base == nil {
			continue
		}
		if status.LastHandshake.Before(base.since) {
			status.LastHandshake = time.Time{}
		}
		counters := base.counters[publicKeyHex]
		if status.RxBytes >= counters.rx {
			status.SessionRxBytes = status.RxBytes - counters.rx
		}
		if status.TxBytes >= counters.tx {
			status.SessionTxBytes = status.TxBytes - counters.tx
		}
	}
}
