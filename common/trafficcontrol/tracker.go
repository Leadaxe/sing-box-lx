package trafficcontrol

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
)

type TrackerMetadata struct {
	ID        uuid.UUID
	Metadata  adapter.InboundContext
	CreatedAt time.Time
	ClosedAt  time.Time
	Upload    *atomic.Int64
	Download  *atomic.Int64
	Chain     []string
	// lx:begin detour-chain (SPEC 017)
	// Detour carries the transport detour tail of the final outbound — the part the
	// upstream Chain omits by design (Chain = routing groups + final outbound only).
	// Resolved in newTrackerMetadata against the same atomic group snapshot, so a
	// detour that points at a group reflects that group's live Now(). Order is from
	// the final outbound outward (node → its detour → …).
	Detour []string
	// lx:end detour-chain
	Rule         adapter.Rule
	Outbound     string
	OutboundType string
}

type Tracker interface {
	Metadata() *TrackerMetadata
	Close() error
}

func (m *Manager) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	upload := new(atomic.Int64)
	download := new(atomic.Int64)
	tracker := &connTracker{
		ExtendedConn: bufio.NewCounterConn(conn, []N.CountFunc{func(n int64) {
			upload.Add(n)
			m.uploadTotal.Add(n)
		}}, []N.CountFunc{func(n int64) {
			download.Add(n)
			m.downloadTotal.Add(n)
		}}),
		metadata: m.newTrackerMetadata(metadata, matchedRule, matchOutbound, upload, download),
		manager:  m,
	}
	m.join(tracker)
	return tracker
}

func (m *Manager) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) N.PacketConn {
	upload := new(atomic.Int64)
	download := new(atomic.Int64)
	tracker := &packetConnTracker{
		PacketConn: bufio.NewCounterPacketConn(conn, []N.CountFunc{func(n int64) {
			upload.Add(n)
			m.uploadTotal.Add(n)
		}}, []N.CountFunc{func(n int64) {
			download.Add(n)
			m.downloadTotal.Add(n)
		}}),
		metadata: m.newTrackerMetadata(metadata, matchedRule, matchOutbound, upload, download),
		manager:  m,
	}
	m.join(tracker)
	return tracker
}

func (m *Manager) newTrackerMetadata(metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound, upload *atomic.Int64, download *atomic.Int64) TrackerMetadata {
	id, _ := uuid.NewV4()
	var (
		chain        []string
		next         string
		outbound     string
		outboundType string
	)
	if matchOutbound != nil {
		next = matchOutbound.Tag()
	} else {
		next = m.outbound.Default().Tag()
	}
	// lx:begin detour-chain (SPEC 017)
	// finalOutbound is the non-group outbound the upstream loop stops at; its detour
	// tail (omitted from Chain by design) is unwound below into a separate field.
	var finalOutbound adapter.Outbound
	seen := make(map[string]bool)
	// lx:end detour-chain
	for {
		detour, loaded := m.outbound.Outbound(next)
		if !loaded {
			break
		}
		chain = append(chain, next)
		outbound = detour.Tag()
		outboundType = detour.Type()
		seen[next] = true // lx: detour-chain — remember routing-chain tags to avoid revisiting
		outboundGroup, isGroup := detour.(adapter.OutboundGroup)
		if !isGroup {
			finalOutbound = detour // lx: detour-chain
			break
		}
		next = outboundGroup.Now()
	}
	// lx:begin detour-chain (SPEC 017)
	// Walk the detour tail of the final outbound. Dependencies()[0] of a non-group
	// outbound is exactly its detour (adapter/outbound/adapter.go); a detour that
	// points at a group is descended via Now() against this same snapshot. seen
	// guards against detour cycles. Order: final outbound → outward.
	var detourChain []string
	for cur := finalOutbound; cur != nil; {
		deps := cur.Dependencies()
		if len(deps) == 0 || seen[deps[0]] {
			break
		}
		step, loaded := m.outbound.Outbound(deps[0])
		if !loaded {
			break
		}
		detourChain = append(detourChain, deps[0])
		seen[deps[0]] = true
		if group, isGroup := step.(adapter.OutboundGroup); isGroup {
			now := group.Now()
			if now == "" || seen[now] {
				break
			}
			nowOutbound, loaded := m.outbound.Outbound(now)
			if !loaded {
				break
			}
			detourChain = append(detourChain, now)
			seen[now] = true
			cur = nowOutbound
			continue
		}
		cur = step
	}
	// lx:end detour-chain
	return TrackerMetadata{
		ID:           id,
		Metadata:     metadata,
		CreatedAt:    time.Now(),
		Upload:       upload,
		Download:     download,
		Chain:        common.Reverse(chain),
		Detour:       detourChain, // lx: detour-chain (SPEC 017)
		Rule:         matchedRule,
		Outbound:     outbound,
		OutboundType: outboundType,
	}
}

type connTracker struct {
	N.ExtendedConn
	metadata TrackerMetadata
	manager  *Manager
}

func (t *connTracker) Metadata() *TrackerMetadata {
	return &t.metadata
}

func (t *connTracker) Close() error {
	t.manager.leave(t)
	return t.ExtendedConn.Close()
}

func (t *connTracker) Upstream() any {
	return t.ExtendedConn
}

func (t *connTracker) ReaderReplaceable() bool {
	return true
}

func (t *connTracker) WriterReplaceable() bool {
	return true
}

type packetConnTracker struct {
	N.PacketConn
	metadata TrackerMetadata
	manager  *Manager
}

func (t *packetConnTracker) Metadata() *TrackerMetadata {
	return &t.metadata
}

func (t *packetConnTracker) Close() error {
	t.manager.leave(t)
	return t.PacketConn.Close()
}

func (t *packetConnTracker) Upstream() any {
	return t.PacketConn
}

func (t *packetConnTracker) ReaderReplaceable() bool {
	return true
}

func (t *packetConnTracker) WriterReplaceable() bool {
	return true
}
