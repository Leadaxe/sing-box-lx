package dialer

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

type funcOutbound struct {
	tag  string
	dial func(ctx context.Context) error
}

func (f *funcOutbound) Type() string           { return "vless" }
func (f *funcOutbound) Tag() string            { return f.tag }
func (f *funcOutbound) Network() []string      { return []string{"tcp"} }
func (f *funcOutbound) Dependencies() []string { return nil }

func (f *funcOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := f.dial(ctx); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	server.Close()
	return client, nil
}

func (f *funcOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, f.dial(ctx)
}

type mapOutboundManager struct {
	adapter.OutboundManager
	outbounds map[string]adapter.Outbound
}

func (m *mapOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	outbound, loaded := m.outbounds[tag]
	return outbound, loaded
}

// lx: SPEC 060 — the dial of the relay a node goes through runs without the
// node's attempt; the node's record still gets the nearest relay, also when
// the relay is a covered node with its own attempt and record.
func TestDetourHidesSniffGuardAttempt(t *testing.T) {
	section := option.LXSniffGuardSectionResolved{
		Method:      option.LXSniffGuardTLSRecordFragment,
		Delay:       300 * time.Millisecond,
		DetourTypes: map[string]string{option.LXSniffGuardWildcard: option.LXSniffGuardSuspect},
	}
	guard := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{TLS: section},
		Nodes: map[string]lxsniffguard.NodeInfo{
			"node":          {Type: "vless", SNIMismatch: true},
			"covered-relay": {Type: "vless", SNIMismatch: true},
		},
	})
	ctx := lxsniffguard.ContextWith(service.ContextWithDefaultRegistry(context.Background()), guard)
	registry := outbound.NewRegistry()
	var build func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Outbound, error)
	outbound.Register[option.StubOptions](registry, "vless", func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Outbound, error) {
		return build(ctx, router, logger, tag, options)
	})
	create := func(tag string, dial func(ctx context.Context) error) adapter.Outbound {
		build = func(context.Context, adapter.Router, log.ContextLogger, string, option.StubOptions) (adapter.Outbound, error) {
			return &funcOutbound{tag: tag, dial: dial}, nil
		}
		created, err := registry.CreateOutbound(ctx, nil, log.NewNOPFactory().NewLogger("test"), tag, "vless", nil)
		require.NoError(t, err)
		return created
	}
	manager := &mapOutboundManager{outbounds: make(map[string]adapter.Outbound)}
	hop := &funcOutbound{tag: "hop"}

	for _, relayTag := range []string{"covered-relay", "plain-relay"} {
		var (
			outerAttempt *lxsniffguard.Attempt
			outerRecord  *adapter.CarrierRecord
			relayAttempt *lxsniffguard.Attempt
			relayRecord  *adapter.CarrierRecord
		)
		manager.outbounds[relayTag] = create(relayTag, func(ctx context.Context) error {
			relayAttempt = lxsniffguard.AttemptFromContext(ctx)
			relayRecord = adapter.CarrierRecordFromContext(ctx)
			// The relay's own detour hop: must never land in the node's record.
			adapter.RecordCarrier(ctx, hop)
			return nil
		})
		detour := NewDetour(manager, relayTag, true)
		node := create("node", func(ctx context.Context) error {
			outerAttempt = lxsniffguard.AttemptFromContext(ctx)
			outerRecord = adapter.CarrierRecordFromContext(ctx)
			conn, err := detour.DialContext(ctx, "tcp", M.ParseSocksaddr("example.com:443"))
			if err != nil {
				return err
			}
			return conn.Close()
		})
		conn, err := node.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.com:443"))
		require.NoError(t, err)
		conn.Close()

		require.NotNil(t, outerAttempt, relayTag)
		require.NotNil(t, outerRecord, relayTag)
		require.Equal(t, relayTag, outerRecord.Tag, "the nearest relay is the node's carrier")
		if relayTag == "covered-relay" {
			require.NotNil(t, relayAttempt)
			require.NotSame(t, outerAttempt, relayAttempt, "the relay has its own attempt")
			require.NotSame(t, outerRecord, relayRecord, "and its own record")
			require.Equal(t, "hop", relayRecord.Tag)
		} else {
			require.Nil(t, relayAttempt, "the node's attempt does not leak into the relay dial")
			require.Same(t, outerRecord, relayRecord)
		}
	}
}

// lx: SPEC 060 — the DNS exchange that resolves a node's server sees neither
// the node's attempt nor its carrier record: a DoH transport's TLS handshake
// and its detour stay out of the node's label and path.
func TestLookupHidesSniffGuard(t *testing.T) {
	guard := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{TLS: option.LXSniffGuardSectionResolved{
			Method:      option.LXSniffGuardTLSRecordFragment,
			Delay:       300 * time.Millisecond,
			DetourTypes: map[string]string{option.LXSniffGuardWildcard: option.LXSniffGuardSuspect},
		}},
		Nodes: map[string]lxsniffguard.NodeInfo{"node": {Type: "vless", SNIMismatch: true}},
	})
	err := guard.Node("node").Dial(context.Background(), func(ctx context.Context) error {
		ctx, record := adapter.ContextWithCarrierRecord(ctx)
		lookup := withoutSniffGuardLx(ctx)
		require.Nil(t, lxsniffguard.AttemptFromContext(lookup))
		require.NotNil(t, adapter.CarrierRecordFromContext(lookup))
		adapter.RecordCarrier(lookup, &funcOutbound{tag: "dns-detour"})
		require.Empty(t, record.Tag, "the transport's detour is not the node's carrier")
		require.NotNil(t, lxsniffguard.AttemptFromContext(ctx))
		return nil
	})
	require.NoError(t, err)
	bare := context.Background()
	require.Equal(t, bare, withoutSniffGuardLx(bare))
}
