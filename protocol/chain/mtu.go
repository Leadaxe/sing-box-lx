package chain

import (
	"strconv"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxmtu"
	C "github.com/sagernet/sing-box/constant"
)

// MTU звеньев-туннелей и размер пакета QUIC-звеньев. Контракт: mtu в конфиге
// узла = «как самостоятельного»; цепочка только ПОНИЖАЕТ его на точные
// накладные IP-туннелей под звеном (lx: SPEC 073 §3.5; таблицы, режимы и
// правка карты — общий пакет common/lxmtu, SPEC 120).
//
//	capacity(i−1) = узел i−1: IP-туннель → его (уже подогнанный) MTU;
//	                          поток / direct / датаграммный прокси → ∞
//	                группа i−1: min по всем достижимым узлам
//	clone.mtu     = min(orig.mtu, capacity(i−1) − overhead(тип звена))
//	clone.initial_packet_size = capacity(i−1) − ip/udp(сервер), кламп QUIC
//
// Смотрим только на непосредственно нижний узел: глубже либо учтено в его MTU,
// либо сброшено потоком. Min по группе — чтобы не пересоздавать звено при
// переключении группы ниже.

// applyMTU — подгонка mtu / initial_packet_size звена tag на позиции position
// по политике lx.mtu_align (SPEC 120 §2.3).
func (c *Chain) applyMTU(position int, tag, typeName string, m map[string]any, info *cloneInfo) {
	capacity, reason := c.capacityBelow(position)
	decision := lxmtu.AlignMap(c.mtuPolicy, tag, typeName, m, capacity, reason)
	if decision == nil {
		return
	}
	if lxmtu.IsQUICType(typeName) {
		info.packetSizeEffective = decision.Effective
		info.packetSizeReason = decision.ChainReason()
		return
	}
	info.mtuConfigured = decision.Configured
	info.mtuEffective = decision.Effective
	info.mtuReason = decision.ChainReason()
}

// capacityBelow — сколько байт IP-пакета пронесёт позиция position−1:
// min по её достижимым узлам.
func (c *Chain) capacityBelow(position int) (int, string) {
	below := position - 1
	leaves, err := c.leavesOf(c.targets[below])
	if err != nil || len(leaves) == 0 {
		return lxmtu.Unlimited, ""
	}
	capacity := lxmtu.Unlimited
	reason := ""
	warning := ""
	for _, leaf := range leaves {
		leafCapacity, leafReason := c.leafCapacity(below, leaf)
		if leafCapacity < capacity {
			capacity = leafCapacity
			reason = leafReason
		}
		if leafReason != "" && leafCapacity == lxmtu.Unlimited && warning == "" {
			warning = leafReason
		}
	}
	if capacity == lxmtu.Unlimited {
		return lxmtu.Unlimited, warning
	}
	return capacity, reason
}

// leafCapacity — ёмкость одного узла на позиции position: IP-туннель → его
// эффективный MTU (позиция 0: как в конфиге; ≥ 1: как у его звена);
// прочее → ∞ (датаграммный tuic в native-режиме — ∞ с предупреждением).
func (c *Chain) leafCapacity(position int, leaf adapter.Outbound) (int, string) {
	typeName := leaf.Type()
	if !lxmtu.IsTunnelType(typeName) {
		if typeName == C.TypeTUIC {
			if _, m, _, err := c.leafOptionsMap(position, leaf); err == nil {
				if mode, _ := m["udp_relay_mode"].(string); mode != "quic" {
					return lxmtu.Unlimited, "warning: tuic " + leaf.Tag() + " in native udp_relay_mode below a tunnel may drop oversize datagrams; use quic"
				}
			}
		}
		return lxmtu.Unlimited, ""
	}
	_, m, _, err := c.leafOptionsMap(position, leaf)
	if err != nil {
		return lxmtu.Unlimited, ""
	}
	mtu := lxmtu.MTUFromMap(m, typeName)
	if mtu == 0 {
		return lxmtu.Unlimited, ""
	}
	if position > 0 {
		var info cloneInfo
		c.applyMTU(position, leaf.Tag(), typeName, m, &info)
		mtu = info.mtuEffective
	}
	return int(mtu), "limited by " + leaf.Tag() + "(" + typeName + ") mtu " + strconv.Itoa(int(mtu))
}
