package libbox

const (
	CommandLog int32 = iota
	CommandStatus
	CommandGroup
	CommandClashMode
	CommandConnections
	CommandOutbounds
	// lx:begin dns
	CommandDNS // SPEC 018 — DNS query stream as a multiplexed command (like CommandConnections)
	// lx:end dns
)
