package lxmtu

import (
	stdjson "encoding/json"
	"strconv"

	C "github.com/sagernet/sing-box/constant"
)

// AlignMap applies the same rules as Resolver to the JSON-map form of a
// node's options, which is what `chain` works with after strip/rewrite
// (SPEC 120 §3 "chain"). capacity and capacityReason are what the position
// below carries (Unlimited with a warning text in capacityReason when there
// is one). The map is mutated in place: `mtu` for tunnels,
// `initial_packet_size` + `disable_path_mtu_discovery` for QUIC proxies.
//
// A decision is returned for every tunnel / QUIC node, changed or not, so the
// caller can report configured/effective/reason; nil for other types.
func AlignMap(policy Policy, tag, typeName string, m map[string]any, capacity int, capacityReason string) *Decision {
	switch {
	case IsTunnelType(typeName):
		return alignMapTunnel(policy, tag, typeName, m, capacity, capacityReason)
	case IsQUICType(typeName):
		return alignMapQUIC(policy, tag, m, capacity, capacityReason)
	}
	return nil
}

func alignMapTunnel(policy Policy, tag, typeName string, m map[string]any, capacity int, capacityReason string) *Decision {
	configured := MTUFromMap(m, typeName)
	explicit := NumberFromMap(m["mtu"]) > 0
	decision := Decision{Tag: tag, Field: "mtu", Configured: configured, Effective: configured, Explicit: explicit}
	server := MapServer(typeName, m)
	overhead, known := TunnelOverhead(typeName, server)
	if !known {
		decision.Reason = "kept (overhead of " + typeName + " unknown)"
		if capacity != Unlimited {
			decision.Warning = tag + "[" + typeName + "] mtu not aligned: overhead of " + typeName + " is unknown (" + capacityReason + ")"
		}
		return &decision
	}
	if capacity == Unlimited {
		decision.Reason = capacityReason
		return &decision
	}
	limit := capacity - overhead
	if limit <= 0 {
		decision.Reason = "kept (capacity below too small: " + capacityReason + ")"
		return &decision
	}
	if configured == 0 {
		// A tunnel whose default we do not know: nothing to lower, nothing to
		// fill without risking a raise.
		decision.Reason = "kept (" + capacityReason + ")"
		return &decision
	}
	decision = policy.Apply(tag, "mtu", configured, explicit, uint32(limit),
		capacityReason+" − "+strconv.Itoa(overhead)+" "+Family(server))
	if decision.Changed {
		m["mtu"] = decision.Effective
	}
	return &decision
}

func alignMapQUIC(policy Policy, tag string, m map[string]any, capacity int, capacityReason string) *Decision {
	configured := uint32(NumberFromMap(m["initial_packet_size"]))
	decision := Decision{Tag: tag, Field: "initial_packet_size", Configured: configured, Effective: configured, Explicit: configured != 0}
	if capacity == Unlimited {
		decision.Reason = capacityReason
		return &decision
	}
	server, _ := m["server"].(string)
	overhead := IPUDP(server)
	limit := capacity - overhead
	size, raised := QUICClamp(limit)
	decision = policy.Apply(tag, "initial_packet_size", configured, configured != 0, uint32(size),
		capacityReason+" − "+strconv.Itoa(overhead)+" "+Family(server))
	if decision.Changed {
		m["initial_packet_size"] = decision.Effective
		m["disable_path_mtu_discovery"] = true
		decision.Reason += "; pmtud off"
	}
	if raised && (decision.Changed || decision.Warning != "") {
		decision.Warning = joinWarning(decision.Warning, tag+" initial_packet_size "+strconv.Itoa(size)+" will not fit: path limit "+strconv.Itoa(limit)+" is below the QUIC minimum "+strconv.Itoa(QUICMinPacketSize))
	}
	return &decision
}

// MapServer is the server string whose address family decides the overhead:
// `peers[0].address` for WireGuard, `server` otherwise.
func MapServer(typeName string, m map[string]any) string {
	if typeName == C.TypeWireGuard {
		if peers, ok := m["peers"].([]any); ok && len(peers) > 0 {
			if peer, ok := peers[0].(map[string]any); ok {
				address, _ := peer["address"].(string)
				return address
			}
		}
		return ""
	}
	server, _ := m["server"].(string)
	return server
}

// MTUFromMap is the tunnel's mtu with the type default applied; 0 when
// absent and the default is unknown.
func MTUFromMap(m map[string]any, typeName string) uint32 {
	if value := NumberFromMap(m["mtu"]); value > 0 {
		return uint32(value)
	}
	return TunnelDefaultMTU(typeName)
}

// NumberFromMap reads a JSON number in any of the forms a decoded or
// rewritten options map may hold.
func NumberFromMap(value any) int64 {
	switch v := value.(type) {
	case stdjson.Number:
		n, err := v.Int64()
		if err != nil {
			f, ferr := v.Float64()
			if ferr != nil {
				return 0
			}
			return int64(f)
		}
		return n
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case uint32:
		return int64(v)
	}
	return 0
}
