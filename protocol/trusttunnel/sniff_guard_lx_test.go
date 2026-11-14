package trusttunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

type recordingConn struct {
	net.Conn
	writes [][]byte
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

type relayStub struct{}

func (relayStub) Type() string           { return "vless" }
func (relayStub) Tag() string            { return "relay" }
func (relayStub) Network() []string      { return nil }
func (relayStub) Dependencies() []string { return nil }
func (relayStub) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrInvalid
}

func (relayStub) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

// lx: SPEC 060 — the QUIC dial hook runs each attempt to open the session
// inside the node's Node.Dial with a fresh carrier record, puts the decoy
// ahead of the handshake on a protected path and redoes a fast failure with
// the opposite decision.
func TestSniffGuardHook(t *testing.T) {
	guard := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{QUIC: option.LXSniffGuardSectionResolved{
			Method: option.LXSniffGuardQUICDecoy,
			Delay:  300 * time.Millisecond,
			DetourTypes: map[string]string{
				option.LXSniffGuardDirect:   option.LXSniffGuardClean,
				option.LXSniffGuardWildcard: option.LXSniffGuardClean,
			},
		}},
		Nodes: map[string]lxsniffguard.NodeInfo{"tt": {Type: "trusttunnel", SNIMismatch: true}},
	})
	require.Nil(t, newSniffGuardHook(guard.Node("other"), nil))
	hook := newSniffGuardHook(guard.Node("tt"), nil)
	require.NotNil(t, hook)

	var (
		conns   []*recordingConn
		records []*adapter.CarrierRecord
	)
	err := hook.Dial(context.Background(), func(ctx context.Context) error {
		record := adapter.CarrierRecordFromContext(ctx)
		require.NotNil(t, record)
		records = append(records, record)
		adapter.RecordCarrier(ctx, relayStub{})
		conn := &recordingConn{}
		conns = append(conns, conn)
		_, done := hook.Handshake(ctx, conn)
		done()
		if len(conns) == 1 {
			return errors.New("remote endpoint has a different public key than pinned")
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, conns, 2, "a fast failure is redone in the same session open")
	require.NotSame(t, records[0], records[1], "each attempt has its own record")
	require.Empty(t, conns[0].writes, "clean path: no decoy")
	require.Equal(t, [][]byte{{0x00}}, conns[1].writes, "the redo carries the decoy")
	require.True(t, guard.Node("tt").Peek(lxsniffguard.ProtoQUIC, lxsniffguard.Path{CarrierType: "vless", CarrierTag: "relay"}).Protect)
}
