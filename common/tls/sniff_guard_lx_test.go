package tls

// lx: SPEC 060 — lx.sniff_guard on the TLS handshake, against a loopback
// crypto/tls server (no external network).

import (
	"context"
	stdtls "crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	tf "github.com/sagernet/sing-box/common/tlsfragment"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

const lxGuardServerName = "www.example.com"

// lxGuardNode returns the handle of a node with a cover SNI under a block
// whose TLS section uses method, with every path starting at start.
func lxGuardNode(t *testing.T, method, start string, override bool) *lxsniffguard.Node {
	t.Helper()
	guard := lxsniffguard.New(lxsniffguard.Config{
		Policy: option.LXSniffGuardResolved{
			Override: override,
			TLS: option.LXSniffGuardSectionResolved{
				Method: method,
				Delay:  50 * time.Millisecond,
				DetourTypes: map[string]string{
					option.LXSniffGuardDirect:   start,
					option.LXSniffGuardWildcard: start,
				},
			},
		},
		Nodes: map[string]lxsniffguard.NodeInfo{"node": {Type: "vless", SNIMismatch: true}},
	})
	require.NotNil(t, guard)
	node := guard.Node("node")
	require.NotNil(t, node)
	return node
}

func lxGuardSTDConfig(t *testing.T, fragment bool, recordFragment *bool) Config {
	t.Helper()
	config, err := NewClientWithOptions(ClientOptions{
		Context:       context.Background(),
		Logger:        logger.NOP(),
		ServerAddress: "127.0.0.1",
		Options: option.OutboundTLSOptions{
			Enabled:        true,
			ServerName:     lxGuardServerName,
			Insecure:       true,
			Fragment:       fragment,
			RecordFragment: recordFragment,
		},
	})
	require.NoError(t, err)
	return config
}

type lxGuardWrite struct {
	at   time.Time
	data []byte
}

// lxGuardWire records the writes that reach the socket. It is transparent
// to the kernel-socket check (replaceable reader with an upstream), so the
// dial still classifies as the direct path.
type lxGuardWire struct {
	net.Conn
	access sync.Mutex
	writes []lxGuardWrite
}

func (w *lxGuardWire) Write(b []byte) (int, error) {
	w.access.Lock()
	w.writes = append(w.writes, lxGuardWrite{time.Now(), append([]byte(nil), b...)})
	w.access.Unlock()
	return w.Conn.Write(b)
}

func (w *lxGuardWire) ReaderReplaceable() bool {
	return true
}

func (w *lxGuardWire) Upstream() any {
	return w.Conn
}

func (w *lxGuardWire) snapshot() []lxGuardWrite {
	w.access.Lock()
	defer w.access.Unlock()
	return append([]lxGuardWrite(nil), w.writes...)
}

// lxGuardServer is a loopback crypto/tls server; every accepted handshake
// reports its result on the returned channel.
func lxGuardServer(t *testing.T) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	certificate := lxGuardCert(t, lxGuardServerName)
	results := make(chan error, 16)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				server := stdtls.Server(conn, &stdtls.Config{Certificates: []stdtls.Certificate{certificate}})
				server.SetDeadline(time.Now().Add(10 * time.Second))
				results <- server.Handshake()
				buffer := make([]byte, 1)
				_, _ = server.Read(buffer)
			}()
		}
	}()
	return listener.Addr().String(), results
}

// lxGuardHandshake dials the server and runs tls.ClientHandshake in ctx.
func lxGuardHandshake(ctx context.Context, address string, config Config) (*lxGuardWire, error) {
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	wire := &lxGuardWire{Conn: raw}
	tlsConn, err := ClientHandshake(ctx, wire, config)
	if err != nil {
		raw.Close()
		return wire, err
	}
	tlsConn.Close()
	return wire, nil
}

// lxGuardDial runs one handshake through node.Dial, as the outbound wrapper
// does, and returns the wire of its (only) attempt.
func lxGuardDial(t *testing.T, node *lxsniffguard.Node, address string, config Config) *lxGuardWire {
	t.Helper()
	var wire *lxGuardWire
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		var err error
		wire, err = lxGuardHandshake(ctx, address, config)
		return err
	})
	require.NoError(t, err)
	return wire
}

func lxGuardServerAccepted(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		require.NoError(t, err, "a server without a sniffer must accept the handshake (P2)")
	case <-time.After(10 * time.Second):
		t.Fatal("server saw no handshake")
	}
}

// lxGuardRecords splits a byte stream into TLS records (type, payload);
// a trailing partial record is dropped.
func lxGuardRecords(stream []byte) (types []byte, payloads [][]byte) {
	for len(stream) >= 5 {
		length := int(binary.BigEndian.Uint16(stream[3:5]))
		if len(stream) < 5+length {
			break
		}
		types = append(types, stream[0])
		payloads = append(payloads, stream[5:5+length])
		stream = stream[5+length:]
	}
	return
}

// lxGuardClientHello returns the records that carry the ClientHello and
// whether the first of them holds the whole message.
func lxGuardClientHello(t *testing.T, writes []lxGuardWrite) (records int, firstComplete bool) {
	t.Helper()
	var stream []byte
	for _, write := range writes {
		stream = append(stream, write.data...)
	}
	types, payloads := lxGuardRecords(stream)
	require.NotEmpty(t, payloads)
	require.Equal(t, byte(22), types[0])
	require.GreaterOrEqual(t, len(payloads[0]), 4)
	require.Equal(t, byte(1), payloads[0][0], "first handshake message must be a ClientHello")
	helloLength := 4 + (int(payloads[0][1])<<16 | int(payloads[0][2])<<8 | int(payloads[0][3]))
	covered := 0
	for i, payload := range payloads {
		require.Equal(t, byte(22), types[i])
		covered += len(payload)
		records++
		if covered >= helloLength {
			break
		}
	}
	require.GreaterOrEqual(t, covered, helloLength, "ClientHello did not fully reach the wire")
	return records, len(payloads[0]) >= helloLength
}

func lxGuardCert(t *testing.T, name string) stdtls.Certificate {
	t.Helper()
	certificate, err := GenerateKeyPair(nil, nil, time.Now, name)
	require.NoError(t, err)
	return *certificate
}

// P1: without an Attempt the hook hands the conn through untouched, also
// for a dial that went through a detour (a carrier is recorded), and a config
// without fragment keys sends the ClientHello as one record in one write.
func TestLxSniffGuardNoAttemptIsUpstream(t *testing.T) {
	t.Parallel()
	config := lxGuardSTDConfig(t, false, nil)
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	ctx, record := adapter.ContextWithCarrierRecord(context.Background())
	record.Type, record.Tag = "vless", "relay"
	require.Same(t, conn, sniffGuardConnLx(ctx, conn, config))
	require.Same(t, conn, sniffGuardConnLx(context.Background(), conn, config))

	address, results := lxGuardServer(t)
	wire, err := lxGuardHandshake(ctx, address, config)
	require.NoError(t, err)
	lxGuardServerAccepted(t, results)
	records, complete := lxGuardClientHello(t, wire.snapshot())
	require.Equal(t, 1, records)
	require.True(t, complete)
	require.Equal(t, byte(22), wire.snapshot()[0].data[0])
	_, payloads := lxGuardRecords(wire.snapshot()[0].data)
	require.Len(t, payloads, 1, "the first write is exactly the ClientHello record")
}

// The removed SPEC 060 detour default must stay removed: a config built
// without fragment keys does not split, whatever the dialer options say.
func TestLxSniffGuardNoDetourDefault(t *testing.T) {
	t.Parallel()
	config := lxGuardSTDConfig(t, false, nil).(*STDClientConfig)
	require.False(t, config.recordFragment)
	require.False(t, config.fragment)
	require.False(t, config.fragmentChosen)
}

func TestLxSniffGuardRecordFragment(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, false)
	wire := lxGuardDial(t, node, address, lxGuardSTDConfig(t, false, nil))
	lxGuardServerAccepted(t, results)
	records, complete := lxGuardClientHello(t, wire.snapshot())
	require.False(t, complete, "the first record must not be a complete ClientHello")
	require.GreaterOrEqual(t, records, 2)
}

func TestLxSniffGuardFragment(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	node := lxGuardNode(t, option.LXSniffGuardTLSFragment, option.LXSniffGuardSuspect, false)
	lxGuardDial(t, node, address, lxGuardSTDConfig(t, false, nil))
	lxGuardServerAccepted(t, results)

	// TCP segments are not observable from here (on a kernel socket tlsfragment
	// writes past any wrapper); check the writer the decision installs.
	attemptConn := lxGuardShapedConn(t, node, lxGuardSTDConfig(t, false, nil))
	shaped, isShaped := attemptConn.(*sniffGuardConn)
	require.True(t, isShaped, "got %T", attemptConn)
	_, isTF := shaped.Conn.(*tf.Conn)
	require.True(t, isTF, "fragment must go through tlsfragment, got %T", shaped.Conn)
}

// lxGuardShapedConn returns the conn the hook hands the engine for one
// attempt of node on a loopback (direct) socket.
func lxGuardShapedConn(t *testing.T, node *lxsniffguard.Node, config Config) net.Conn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	raw, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	var shaped net.Conn
	_ = node.Dial(context.Background(), func(ctx context.Context) error {
		shaped = sniffGuardConnLx(ctx, raw, config)
		return nil
	})
	return shaped
}

func TestLxSniffGuardDelay(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	node := lxGuardNode(t, option.LXSniffGuardDelay, option.LXSniffGuardSuspect, false)
	wire := lxGuardDial(t, node, address, lxGuardSTDConfig(t, false, nil))
	lxGuardServerAccepted(t, results)
	writes := wire.snapshot()
	require.GreaterOrEqual(t, len(writes), 2)
	hello := append(append([]byte(nil), writes[0].data...), writes[1].data...)
	_, payloads := lxGuardRecords(hello)
	require.Len(t, payloads, 1, "the two writes must make exactly one TLS record")
	records, complete := lxGuardClientHello(t, writes[:2])
	require.Equal(t, 1, records)
	require.True(t, complete)
	serverName := tf.IndexTLSServerName(hello)
	require.NotNil(t, serverName)
	require.Equal(t, lxGuardServerName, serverName.ServerName)
	require.Greater(t, len(writes[0].data), serverName.Index, "first write must end inside the SNI")
	require.Less(t, len(writes[0].data), serverName.Index+serverName.Length, "first write must end inside the SNI")
	require.GreaterOrEqual(t, writes[1].at.Sub(writes[0].at), 45*time.Millisecond, "the pause between the chunks")
}

// Clean path: the decision is "no protection" and nothing changes.
func TestLxSniffGuardCleanPathUnsplit(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardClean, false)
	wire := lxGuardDial(t, node, address, lxGuardSTDConfig(t, false, nil))
	lxGuardServerAccepted(t, results)
	records, complete := lxGuardClientHello(t, wire.snapshot())
	require.Equal(t, 1, records)
	require.True(t, complete)
}

// lxGuardFastFailures runs node.Dial with a handshake that succeeds and a
// dial that then fails fast, and returns how many attempts ran: a counted
// (applicable) failure on a suspect path is retried once without
// protection, an uncounted one is not.
func lxGuardFastFailures(t *testing.T, node *lxsniffguard.Node, address string, config Config) (int, []*lxGuardWire) {
	t.Helper()
	var attempts atomic.Int32
	var wires []*lxGuardWire
	err := node.Dial(context.Background(), func(ctx context.Context) error {
		attempts.Add(1)
		wire, err := lxGuardHandshake(ctx, address, config)
		require.NoError(t, err)
		wires = append(wires, wire)
		return errors.New("protocol error after the handshake")
	})
	require.Error(t, err)
	return int(attempts.Load()), wires
}

// P6: an explicit `record_fragment: false` under a suspect label goes
// unsplit, and Decide got applicable=false (the failure is not counted,
// so there is no retry).
func TestLxSniffGuardExplicitFalseWins(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, false)
	attempts, wires := lxGuardFastFailures(t, node, address, lxGuardSTDConfig(t, false, lxBoolPtr(false)))
	lxGuardServerAccepted(t, results)
	require.Equal(t, 1, attempts, "a node with its own choice is not applicable: no retry")
	records, complete := lxGuardClientHello(t, wires[0].snapshot())
	require.Equal(t, 1, records)
	require.True(t, complete)

	// The control: the same node without its own choice is applicable, its
	// fast failure with protection is retried without it.
	control := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, false)
	attempts, wires = lxGuardFastFailures(t, control, address, lxGuardSTDConfig(t, false, nil))
	require.Equal(t, 2, attempts)
	_, complete = lxGuardClientHello(t, wires[0].snapshot())
	require.False(t, complete, "first attempt with protection")
	_, complete = lxGuardClientHello(t, wires[1].snapshot())
	require.True(t, complete, "retry without protection")
}

// Override: the block decides the whole shape.
func TestLxSniffGuardOverride(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)

	suspect := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, true)
	wire := lxGuardDial(t, suspect, address, lxGuardSTDConfig(t, false, lxBoolPtr(false)))
	lxGuardServerAccepted(t, results)
	_, complete := lxGuardClientHello(t, wire.snapshot())
	require.False(t, complete, "suspect + override splits despite the node's false")

	clean := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardClean, true)
	wire = lxGuardDial(t, clean, address, lxGuardSTDConfig(t, false, lxBoolPtr(true)))
	lxGuardServerAccepted(t, results)
	records, complete := lxGuardClientHello(t, wire.snapshot())
	require.Equal(t, 1, records, "clean + override goes unsplit despite the node's true")
	require.True(t, complete)

	// The block's method replaces the node's: delay instead of the node's
	// record_fragment, one record in two writes.
	method := lxGuardNode(t, option.LXSniffGuardDelay, option.LXSniffGuardSuspect, true)
	wire = lxGuardDial(t, method, address, lxGuardSTDConfig(t, false, lxBoolPtr(true)))
	lxGuardServerAccepted(t, results)
	writes := wire.snapshot()
	require.GreaterOrEqual(t, len(writes), 2)
	records, complete = lxGuardClientHello(t, writes[:2])
	require.Equal(t, 1, records)
	require.True(t, complete)
}

// Without override, a node's own split stays as upstream applies it.
func TestLxSniffGuardNodeKeysWithoutOverride(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardClean, false)
	wire := lxGuardDial(t, node, address, lxGuardSTDConfig(t, false, lxBoolPtr(true)))
	lxGuardServerAccepted(t, results)
	_, complete := lxGuardClientHello(t, wire.snapshot())
	require.False(t, complete, "node record_fragment applies on a clean path")
}

// The decision is per handshake: one Config, two concurrent handshakes, one
// with a protecting Attempt and one without.
func TestLxSniffGuardPerHandshake(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	config := lxGuardSTDConfig(t, false, nil)
	node := lxGuardNode(t, option.LXSniffGuardDelay, option.LXSniffGuardSuspect, false)
	var group sync.WaitGroup
	var protected, plain *lxGuardWire
	group.Add(2)
	go func() {
		defer group.Done()
		_ = node.Dial(context.Background(), func(ctx context.Context) error {
			var err error
			protected, err = lxGuardHandshake(ctx, address, config)
			return err
		})
	}()
	go func() {
		defer group.Done()
		plain, _ = lxGuardHandshake(context.Background(), address, config)
	}()
	group.Wait()
	lxGuardServerAccepted(t, results)
	lxGuardServerAccepted(t, results)
	require.GreaterOrEqual(t, len(protected.snapshot()), 2)
	_, payloads := lxGuardRecords(protected.snapshot()[0].data)
	require.Empty(t, payloads, "the protected first write is an incomplete record")
	_, payloads = lxGuardRecords(plain.snapshot()[0].data)
	require.Len(t, payloads, 1, "the plain first write is the whole ClientHello record")
}

// An unknown path (no kernel socket, no carrier) is left alone.
func TestLxSniffGuardUnknownPath(t *testing.T) {
	t.Parallel()
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, false)
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	config := lxGuardSTDConfig(t, false, nil)
	_ = node.Dial(context.Background(), func(ctx context.Context) error {
		require.Same(t, conn, sniffGuardConnLx(ctx, conn, config))
		return nil
	})
	// The same conn with a recorded carrier is that carrier's path.
	_ = node.Dial(context.Background(), func(ctx context.Context) error {
		ctx, record := adapter.ContextWithCarrierRecord(ctx)
		record.Type, record.Tag = "vless", "relay"
		_, shaped := sniffGuardConnLx(ctx, conn, config).(*sniffGuardConn)
		require.True(t, shaped)
		return nil
	})
}

func TestLxSniffGuardDelaySplit(t *testing.T) {
	t.Parallel()
	require.Zero(t, sniffGuardDelaySplit(nil))
	require.Zero(t, sniffGuardDelaySplit([]byte{23, 3, 3, 0, 1, 0}))
}

// lxGuardDNSRouter records the context of the ECH config exchange.
type lxGuardDNSRouter struct {
	adapter.DNSRouter
	exchangeCtx context.Context
}

func (r *lxGuardDNSRouter) Exchange(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions) (*mDNS.Msg, error) {
	r.exchangeCtx = ctx
	return nil, errors.New("no DNS in this test")
}

// The ECH config is fetched inside the node's handshake, but the DNS
// exchange is not the node's: a DoT or DoH transport dialled from it must see
// neither the node's attempt nor its carrier record.
func TestLxSniffGuardECHExchangeHidesAttempt(t *testing.T) {
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, false)
	echCapable, isECH := lxGuardSTDConfig(t, false, nil).(ECHCapableConfig)
	require.True(t, isECH)
	router := &lxGuardDNSRouter{}
	config := &ECHClientConfig{ECHCapableConfig: echCapable, dnsRouter: router}
	dialCtx, record := adapter.ContextWithCarrierRecord(context.Background())
	_ = node.Dial(dialCtx, func(ctx context.Context) error {
		require.NotNil(t, lxsniffguard.AttemptFromContext(ctx))
		_, err := config.fetchAndHandshake(ctx, nil)
		require.Error(t, err)
		return err
	})
	require.NotNil(t, router.exchangeCtx)
	require.Nil(t, lxsniffguard.AttemptFromContext(router.exchangeCtx))
	exchangeRecord := adapter.CarrierRecordFromContext(router.exchangeCtx)
	require.NotNil(t, exchangeRecord)
	require.NotSame(t, record, exchangeRecord, "the transport's detour is not the node's carrier")
}
