package daemon

import "github.com/sagernet/sing-box/common/trafficcontrol"

// refreshActualLx re-announces a live connection once its SPEC 138 view
// changes: the NEW event went out when the router created the entry, before a
// per-connection group (round_robin) picked the node, and UPDATE events carry
// only traffic deltas. A repeated NEW replaces the client's entry as a whole
// (libbox Connections.ApplyEvents), totals included, and stands in for this
// tick's UPDATE: it carries the tick's delta both as the rate of the entry
// (Connection.Uplink/Downlink) and as the event delta, computed from the very
// totals the entry carries, so a client that takes totals from the entry and
// one that accumulates the deltas agree. Connections without such a group
// never get a view and are never re-announced.
func refreshActualLx(metadata *trafficcontrol.TrackerMetadata, snapshot *connectionSnapshot) *ConnectionEvent {
	if snapshot.actualLx == metadata || !metadata.IsActualViewLx() {
		return nil
	}
	snapshot.actualLx = metadata
	connection := buildConnectionProto(metadata)
	uplinkDelta := connection.UplinkTotal - snapshot.uplink
	downlinkDelta := connection.DownlinkTotal - snapshot.downlink
	if uplinkDelta < 0 || downlinkDelta < 0 {
		uplinkDelta, downlinkDelta = 0, 0
	}
	connection.Uplink = uplinkDelta
	connection.Downlink = downlinkDelta
	snapshot.uplink = connection.UplinkTotal
	snapshot.downlink = connection.DownlinkTotal
	snapshot.hadTraffic = uplinkDelta > 0 || downlinkDelta > 0
	return &ConnectionEvent{
		Type:          ConnectionEventType_CONNECTION_EVENT_NEW,
		Id:            metadata.ID.String(),
		Connection:    connection,
		UplinkDelta:   uplinkDelta,
		DownlinkDelta: downlinkDelta,
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
