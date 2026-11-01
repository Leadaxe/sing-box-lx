package wireguard

// lx: SPEC 114 — per-peer status for GetOutbounds.

import "github.com/sagernet/sing-box/adapter"

// PeerStatuses implements adapter.PeerStatusReporter. A build in progress
// assigns the device without the transport's shutdown mutex, so the snapshot
// is skipped for that window (the state reads "building"); an asleep device
// still answers with the session it had before Down (SPEC 145).
func (w *Endpoint) PeerStatuses() []adapter.PeerStatus {
	if w.endpoint == nil || w.building.Load() {
		return nil
	}
	return w.endpoint.PeerStatuses()
}
