package lxdatagram

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type writeRecordingConn struct {
	*framedConn
	access sync.Mutex
	writes [][]byte
}

func (c *writeRecordingConn) Write(p []byte) (int, error) {
	c.access.Lock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	c.access.Unlock()
	return len(p), nil
}

func (c *writeRecordingConn) decoyFirst() bool {
	c.access.Lock()
	defer c.access.Unlock()
	return len(c.writes) > 0 && string(c.writes[0]) == string(sniffDecoy)
}

// carriedDialer stands for a detour to an outbound of carrierType: it records
// the carrier the way the detour dialer does and hands out a framed conn.
type carriedDialer struct {
	carrierType string
	conns       []*writeRecordingConn
}

func (d *carriedDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	if d.carrierType != "" {
		adapter.RecordCarrier(ctx, stubOutbound{d.carrierType})
	}
	conn := &writeRecordingConn{framedConn: newFramedConn()}
	d.conns = append(d.conns, conn)
	return conn, nil
}

func (d *carriedDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

type stubOutbound struct{ outboundType string }

func (s stubOutbound) Type() string           { return s.outboundType }
func (s stubOutbound) Tag() string            { return s.outboundType + "-node" }
func (s stubOutbound) Network() []string      { return nil }
func (s stubOutbound) Dependencies() []string { return nil }
func (s stubOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, os.ErrInvalid
}

func (s stubOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

// testGuard is a guard whose quic section uses method, with vless relays
// starting suspect and socks relays clean; it covers the node "node".
func testGuard(t *testing.T, method string) (context.Context, *lxsniffguard.Guard) {
	t.Helper()
	g := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{QUIC: option.LXSniffGuardSectionResolved{
			Method: method,
			Delay:  50 * time.Millisecond,
			DetourTypes: map[string]string{
				option.LXSniffGuardDirect:   option.LXSniffGuardClean,
				"socks":                     option.LXSniffGuardClean,
				"vless":                     option.LXSniffGuardSuspect,
				option.LXSniffGuardWildcard: option.LXSniffGuardNever,
			},
		}},
		Nodes: map[string]lxsniffguard.NodeInfo{"node": {Type: "hysteria2", SNIMismatch: true}},
		Carriers: map[string]lxsniffguard.CarrierInfo{
			"vless-node": {Type: "vless", Encrypted: true},
			"socks-node": {Type: "socks"},
		},
	})
	require.NotNil(t, g)
	return lxsniffguard.ContextWith(context.Background(), g), g
}

var (
	errHandshake = errors.New("remote endpoint has a different public key than pinned")
	pathVLESS    = lxsniffguard.Path{CarrierType: "vless", CarrierTag: "vless-node"}
	pathSOCKS    = lxsniffguard.Path{CarrierType: "socks", CarrierTag: "socks-node"}
)

// guardedDial dials once through carrier inside the node's Node.Dial and
// returns the conn of each attempt; handshake is the outcome of an attempt.
func guardedDial(t *testing.T, node *lxsniffguard.Node, carrier *carriedDialer, write func(net.Conn), handshake func(attempt int) error) ([]*writeRecordingConn, error) {
	t.Helper()
	var conns []*writeRecordingConn
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		ctx, record := adapter.ContextWithCarrierRecord(ctx)
		conn, err := carrier.DialContext(ctx, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
		require.NoError(t, err)
		recording := conn.(*writeRecordingConn)
		conns = append(conns, recording)
		handshakeConn, guard := GuardQUIC(lxsniffguard.AttemptFromContext(ctx), recording, record, recording, nil)
		if write != nil {
			write(handshakeConn)
		}
		guard.Done()
		return handshake(len(conns))
	})
	return conns, err
}

// The byte goes out only when the store says protect with decoy_first_byte;
// a fast failure without it is redone at once with it, and the pair moves
// the label.
func TestGuardQUICDecoyByDecision(t *testing.T) {
	_, g := testGuard(t, option.LXSniffGuardQUICDecoy)
	node := g.Node("node")

	conns, err := guardedDial(t, node, &carriedDialer{carrierType: "vless"}, nil, func(int) error { return nil })
	require.NoError(t, err)
	require.Len(t, conns, 1)
	require.True(t, conns[0].decoyFirst(), "suspect relay: the byte goes first")

	sniffed := func(attempt int) error {
		if attempt == 1 {
			return errHandshake
		}
		return nil
	}
	conns, err = guardedDial(t, node, &carriedDialer{carrierType: "socks"}, nil, sniffed)
	require.NoError(t, err)
	require.Len(t, conns, 2, "a fast failure is redone in the same dial")
	require.False(t, conns[0].decoyFirst(), "clean path: no byte")
	require.True(t, conns[1].decoyFirst(), "the redo carries the byte")
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, pathSOCKS).Protect, "the pair moved the label")

	conns, err = guardedDial(t, node, &carriedDialer{}, nil, func(int) error { return errHandshake })
	require.Error(t, err)
	require.Len(t, conns, 1, "unknown path: no retry")
	require.Empty(t, conns[0].writes, "unknown path: no byte")

	conn := &writeRecordingConn{framedConn: newFramedConn()}
	handshakeConn, handshake := GuardQUIC(nil, conn, nil, conn, nil)
	require.Same(t, conn, handshakeConn)
	require.Nil(t, handshake)
	handshake.Done()
	handshake.Report(false)
	require.Empty(t, conn.writes)
}

func quicInitial(size int) []byte {
	packet := datagram(size, 0xaa)
	packet[0] = 0xc3 // long header, fixed bit, type Initial (v1)
	packet[1], packet[2], packet[3], packet[4] = 0, 0, 0, 1
	return packet
}

func quicHandshakePacket(size int) []byte {
	packet := quicInitial(size)
	packet[0] = 0xe3 // type Handshake (v1)
	return packet
}

func TestIsQUICInitial(t *testing.T) {
	require.True(t, isQUICInitial(quicInitial(1200)))
	require.False(t, isQUICInitial(quicHandshakePacket(1200)))
	v2 := quicInitial(1200)
	v2[0] = 0xd3 // type 1 is Initial in QUIC v2
	v2[1], v2[2], v2[3], v2[4] = 0x6b, 0x33, 0x43, 0xcf
	require.True(t, isQUICInitial(v2))
	require.False(t, isQUICInitial(sniffDecoy))
	require.False(t, isQUICInitial([]byte{0x43, 0, 0, 0, 1}), "short header")
}

// The second Initial of the first flight waits until hold has passed since
// the first; other packets and a retransmission after a read pass at once.
func TestInitialDelayConnHoldsSecondInitial(t *testing.T) {
	recording := &writeRecordingConn{framedConn: newFramedConn()}
	conn := NewInitialDelayConn(recording, 80*time.Millisecond, nil)
	start := time.Now()
	_, err := conn.Write(quicInitial(1200))
	require.NoError(t, err)
	_, err = conn.Write(quicInitial(1200))
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), 80*time.Millisecond)
	require.True(t, conn.SecondInitial())
	start = time.Now()
	_, err = conn.Write(quicInitial(1200))
	require.NoError(t, err)
	_, err = conn.Write(quicHandshakePacket(100))
	require.NoError(t, err)
	require.Less(t, time.Since(start), 40*time.Millisecond, "only the second Initial is held")
	require.Len(t, recording.writes, 4)

	// A ClientHello in one Initial: the next Initial comes after the server
	// answered (a retransmission or the client's next flight) and is not held.
	single := NewInitialDelayConn(&writeRecordingConn{framedConn: newFramedConn([]byte{1})}, time.Second, nil)
	_, err = single.Write(quicInitial(1200))
	require.NoError(t, err)
	_, err = single.Read(make([]byte, 16))
	require.NoError(t, err)
	start = time.Now()
	_, err = single.Write(quicInitial(1200))
	require.NoError(t, err)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.False(t, single.SecondInitial())

	// Close releases a held write.
	held := NewInitialDelayConn(&writeRecordingConn{framedConn: newFramedConn()}, time.Minute, nil)
	_, err = held.Write(quicInitial(1200))
	require.NoError(t, err)
	go func() {
		time.Sleep(20 * time.Millisecond)
		held.Close()
	}()
	_, err = held.Write(quicInitial(1200))
	require.ErrorIs(t, err, net.ErrClosed)
}

// quic.delay on a ClientHello in one Initial does nothing: the attempt is
// withdrawn, neither counted nor retried. With two Initials it is a handshake
// like any other.
func TestGuardQUICDelayNotApplicableOnSingleInitial(t *testing.T) {
	_, g := testGuard(t, option.LXSniffGuardDelay)
	node := g.Node("node")
	oneInitial := func(conn net.Conn) { conn.Write(quicInitial(1200)) }
	twoInitials := func(conn net.Conn) {
		conn.Write(quicInitial(1200))
		conn.Write(quicInitial(1200))
	}

	conns, err := guardedDial(t, node, &carriedDialer{carrierType: "socks"}, oneInitial, func(int) error { return errHandshake })
	require.Error(t, err)
	require.Len(t, conns, 1, "withdrawn: no retry")
	require.False(t, conns[0].decoyFirst())

	conns, err = guardedDial(t, node, &carriedDialer{carrierType: "socks"}, twoInitials, func(attempt int) error {
		if attempt == 1 {
			return errHandshake
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, conns, 2, "two Initials: the fast failure is redone with the delay")
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, pathSOCKS).Protect)
}

// The library seam (hysteria2, tuic): the dialer cannot see the handshake, so
// a conn that was not settled by its window timer or a port hop is reported
// to the store at the next new connection — closed within the window is a
// failed attempt, still open or long-lived is a success.
func TestDialerReportsThroughStore(t *testing.T) {
	manualTimers(t)
	buildCtx, g := testGuard(t, option.LXSniffGuardQUICDecoy)
	node := g.Node("node")
	carried := &carriedDialer{carrierType: "socks"}
	d := Dialer(buildCtx, "node", carried, nil)
	dial := func(ctx context.Context) net.Conn {
		conn, err := d.DialContext(ctx, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:443"))
		require.NoError(t, err)
		return conn
	}
	offer := func() net.Conn {
		ctx, cancel := context.WithCancel(buildCtx) // sing-quic's offer context
		defer cancel()
		return dial(ctx)
	}

	first := offer()
	require.False(t, carried.conns[0].decoyFirst(), "clean path: the first connection goes without the byte")
	first.Close() // young: a failure
	second := offer()
	require.True(t, carried.conns[1].decoyFirst(), "the next connection takes the pending pair")
	require.False(t, node.Peek(lxsniffguard.ProtoQUIC, pathSOCKS).Protect, "no pair yet")
	// The second conn lived long: move its dial time into the past.
	d.(*truncDialer).last.dialedAt = time.Now().Add(-youngConnWindow - time.Second)
	second.Close()
	offer()
	require.True(t, node.Peek(lxsniffguard.ProtoQUIC, pathSOCKS).Protect, "failure without, success with: suspect")
	require.True(t, carried.conns[2].decoyFirst())

	// A port hop dials with the client's lifetime context itself: no
	// handshake, no byte, and the connection that hopped counts as alive.
	hop := dial(buildCtx)
	require.Empty(t, carried.conns[3].writes)
	if _, isAttempt := hop.(*attemptConn); isAttempt {
		t.Fatal("a hop is not an attempt")
	}
	d.(*truncDialer).last.Close() // closed young, but after a hop
	offer()
	require.True(t, carried.conns[4].decoyFirst(), "a hopped connection is no failure: no descent pair")

	tcp, err := d.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("1.1.1.1:443"))
	require.NoError(t, err)
	if _, isAttempt := tcp.(*attemptConn); isAttempt {
		t.Fatal("tcp dials pass through")
	}
	off := Dialer(context.Background(), "node", &carriedDialer{carrierType: "socks"}, nil)
	require.Nil(t, off.(*truncDialer).node, "no guard: the dialer only truncates")
	uncovered := Dialer(buildCtx, "other", &carriedDialer{carrierType: "socks"}, nil)
	require.Nil(t, uncovered.(*truncDialer).node)
}

// The decoy must be something a QUIC sniffer rejects outright (not a long
// header, so Xray's sniffer drops out and sing-box's records an error) and a
// QUIC server discards unparsed (fixed bit clear, too short for a stateless
// reset).
func TestSniffDecoyIsNotAQUICPacket(t *testing.T) {
	if sniffDecoy[0]&0x80 != 0 {
		t.Fatal("decoy must not be a long-header packet")
	}
	if sniffDecoy[0]&0x40 != 0 {
		t.Fatal("decoy must have the QUIC fixed bit clear")
	}
	if len(sniffDecoy) >= 21 {
		t.Fatal("decoy must stay below the smallest packet that can draw a stateless reset")
	}
}

var (
	_ net.Conn         = (*writeRecordingConn)(nil)
	_ adapter.Outbound = stubOutbound{}
)
