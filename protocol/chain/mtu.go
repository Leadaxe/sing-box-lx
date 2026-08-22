package chain

import (
	stdjson "encoding/json"
	"math"
	"net/netip"
	"strconv"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// MTU звеньев-туннелей. Контракт: mtu в конфиге узла = «как самостоятельного»;
// цепочка только ПОНИЖАЕТ его на точные накладные IP-туннелей под звеном.
//
//	capacity(i−1) = узел i−1: IP-туннель → его (уже подогнанный) MTU;
//	                          поток / direct / датаграммный прокси → ∞
//	                группа i−1: min по всем достижимым узлам
//	clone.mtu     = min(orig.mtu, capacity(i−1) − overhead(тип звена))
//
// Смотрим только на непосредственно нижний узел: глубже либо учтено в его MTU,
// либо сброшено потоком. Min по группе — чтобы не пересоздавать звено при
// переключении группы ниже.
const (
	wgOverheadV4   = 60 // IPv4 20 + UDP 8 + WG data header 32
	wgOverheadV6   = 80 // IPv6 40 + UDP 8 + WG data header 32
	masqueOverhead = 90 // UDP/IP + QUIC short header + h3 datagram framing (оценка)

	defaultWGMTU     = 1408
	defaultMasqueMTU = 1280

	unlimited = math.MaxInt
)

func isTunnelType(typeName string) bool {
	switch typeName {
	case C.TypeWireGuard, C.TypeMASQUE, C.TypeOpenVPNClient, C.TypeOpenConnect:
		return true
	}
	return false
}

func tunnelDefaultMTU(typeName string) uint32 {
	switch typeName {
	case C.TypeWireGuard:
		return defaultWGMTU
	case C.TypeMASQUE:
		return defaultMasqueMTU
	}
	return 0
}

func mtuFromMap(m map[string]any, typeName string) uint32 {
	if value := numberFromMap(m["mtu"]); value > 0 {
		return uint32(value)
	}
	return tunnelDefaultMTU(typeName)
}

func numberFromMap(value any) int64 {
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

// overheadOf — накладные звена данного типа внутри IP-туннеля; −1 = неизвестны
// (openvpn/openconnect как верхний туннель не подгоняются).
func overheadOf(typeName string, m map[string]any) int {
	switch typeName {
	case C.TypeWireGuard:
		if peers, ok := m["peers"].([]any); ok && len(peers) > 0 {
			if peer, ok := peers[0].(map[string]any); ok {
				if address, ok := peer["address"].(string); ok {
					if addr, err := netip.ParseAddr(address); err == nil && addr.Is4() {
						return wgOverheadV4
					}
				}
			}
		}
		return wgOverheadV6
	case C.TypeMASQUE:
		return masqueOverhead
	}
	return -1
}

// applyMTU — подгонка mtu туннельного звена на позиции position.
func (c *Chain) applyMTU(position int, typeName string, m map[string]any, info *cloneInfo) {
	configured := mtuFromMap(m, typeName)
	info.mtuConfigured = configured
	info.mtuEffective = configured
	overhead := overheadOf(typeName, m)
	if overhead < 0 {
		info.mtuReason = "kept (overhead of " + typeName + " unknown)"
		return
	}
	capacity, reason := c.capacityBelow(position)
	if capacity == unlimited {
		if reason != "" {
			info.mtuReason = reason
		}
		return
	}
	limit := capacity - overhead
	if limit <= 0 {
		info.mtuReason = "kept (capacity below too small: " + reason + ")"
		return
	}
	if uint32(limit) < configured {
		info.mtuEffective = uint32(limit)
		info.mtuReason = reason + " −" + strconv.Itoa(overhead)
		m["mtu"] = info.mtuEffective
		return
	}
	info.mtuReason = "fits (" + reason + ")"
}

// capacityBelow — сколько байт IP-пакета пронесёт позиция position−1:
// min по её достижимым узлам.
func (c *Chain) capacityBelow(position int) (int, string) {
	below := position - 1
	leaves, err := c.leavesOf(c.targets[below])
	if err != nil || len(leaves) == 0 {
		return unlimited, ""
	}
	capacity := unlimited
	reason := ""
	warning := ""
	for _, leaf := range leaves {
		leafCapacity, leafReason := c.leafCapacity(below, leaf)
		if leafCapacity < capacity {
			capacity = leafCapacity
			reason = leafReason
		}
		if leafReason != "" && leafCapacity == unlimited && warning == "" {
			warning = leafReason
		}
	}
	if capacity == unlimited {
		return unlimited, warning
	}
	return capacity, reason
}

// leafCapacity — ёмкость одного узла на позиции position: IP-туннель → его
// эффективный MTU (позиция 0: как в конфиге; ≥ 1: как у его звена);
// прочее → ∞ (датаграммный tuic в native-режиме — ∞ с предупреждением).
func (c *Chain) leafCapacity(position int, leaf adapter.Outbound) (int, string) {
	typeName := leaf.Type()
	if !isTunnelType(typeName) {
		if typeName == C.TypeTUIC {
			if _, m, _, err := c.leafOptionsMap(position, leaf); err == nil {
				if mode, _ := m["udp_relay_mode"].(string); mode != "quic" {
					return unlimited, "warning: tuic " + leaf.Tag() + " in native udp_relay_mode below a tunnel may drop oversize datagrams; use quic"
				}
			}
		}
		return unlimited, ""
	}
	_, m, _, err := c.leafOptionsMap(position, leaf)
	if err != nil {
		return unlimited, ""
	}
	mtu := mtuFromMap(m, typeName)
	if mtu == 0 {
		return unlimited, ""
	}
	if position > 0 {
		var info cloneInfo
		c.applyMTU(position, typeName, m, &info)
		mtu = info.mtuEffective
	}
	return int(mtu), "limited by " + leaf.Tag() + "(" + typeName + ") mtu " + strconv.Itoa(int(mtu))
}
