package daemon

import "github.com/sagernet/sing-box/common/trafficcontrol"

// refreshActualLx re-announces a live connection once its SPEC 138 view
// changes: the NEW event went out when the router created the entry, before a
// per-connection group (round_robin) picked the node, and UPDATE events carry
// only traffic deltas. A repeated NEW replaces the client's entry as a whole
// (libbox Connections.ApplyEvents), totals included; the snapshot restarts the
// deltas from those totals. Connections without such a group never get a view
// and are never re-announced.
func refreshActualLx(metadata *trafficcontrol.TrackerMetadata, snapshot *connectionSnapshot) *ConnectionEvent {
	if snapshot.actualLx == metadata || !metadata.IsActualViewLx() {
		return nil
	}
	snapshot.actualLx = metadata
	connection := buildConnectionProto(metadata)
	snapshot.uplink = connection.UplinkTotal
	snapshot.downlink = connection.DownlinkTotal
	snapshot.hadTraffic = false
	return &ConnectionEvent{
		Type:       ConnectionEventType_CONNECTION_EVENT_NEW,
		Id:         metadata.ID.String(),
		Connection: connection,
	}
}

// sentActualLx is the SPEC 138 view a NEW event built from metadata carries, for
// the snapshot that event starts: the view itself when metadata is the current
// view of its connection, nil otherwise. A connection announced with its picked
// node is then not announced again on the next tick, which keeps that tick's
// traffic delta.
func sentActualLx(metadata *trafficcontrol.TrackerMetadata) *trafficcontrol.TrackerMetadata {
	if metadata.IsActualViewLx() {
		return metadata
	}
	return nil
}
