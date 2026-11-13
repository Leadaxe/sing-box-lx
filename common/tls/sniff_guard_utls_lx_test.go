//go:build with_utls

package tls

// lx: SPEC 060 — the lx.sniff_guard shape reaches the uTLS and REALITY
// engines. uTLS runs a live handshake against the loopback crypto/tls server;
// REALITY is checked on its first flight (the loopback peer reads the
// ClientHello and hangs up, as in reality_client_lx_test.go).

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxsniffguard"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"

	"github.com/stretchr/testify/require"
)

func lxGuardUTLSConfig(t *testing.T, recordFragment *bool) Config {
	t.Helper()
	config, err := NewClientWithOptions(ClientOptions{
		Context:       context.Background(),
		Logger:        logger.NOP(),
		ServerAddress: "127.0.0.1",
		Options: option.OutboundTLSOptions{
			Enabled:        true,
			ServerName:     lxGuardServerName,
			Insecure:       true,
			RecordFragment: recordFragment,
			UTLS:           &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
		},
	})
	require.NoError(t, err)
	_, isUTLS := config.(*UTLSClientConfig)
	require.True(t, isUTLS, "got %T", config)
	return config
}

func TestLxSniffGuardUTLS(t *testing.T) {
	t.Parallel()
	address, results := lxGuardServer(t)
	for _, method := range []string{option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardDelay} {
		node := lxGuardNode(t, method, option.LXSniffGuardSuspect, false)
		wire := lxGuardDial(t, node, address, lxGuardUTLSConfig(t, nil))
		lxGuardServerAccepted(t, results)
		writes := wire.snapshot()
		records, complete := lxGuardClientHello(t, writes)
		switch method {
		case option.LXSniffGuardTLSRecordFragment:
			require.False(t, complete, "uTLS: first record must not be a complete ClientHello")
			require.GreaterOrEqual(t, records, 2)
		case option.LXSniffGuardDelay:
			require.Equal(t, 1, records)
			_, payloads := lxGuardRecords(writes[0].data)
			require.Empty(t, payloads, "uTLS: the first write is an incomplete record")
		}
	}

	// Override on a clean path drops the node's own record_fragment.
	node := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardClean, true)
	wire := lxGuardDial(t, node, address, lxGuardUTLSConfig(t, lxBoolPtr(true)))
	lxGuardServerAccepted(t, results)
	records, complete := lxGuardClientHello(t, wire.snapshot())
	require.Equal(t, 1, records)
	require.True(t, complete)
}

// lxGuardRealityFlight runs the REALITY handshake of client on a loopback
// socket (the direct path) inside node.Dial, or without an Attempt for a nil
// node, and returns the ClientHello flight the peer read.
func lxGuardRealityFlight(t *testing.T, node *lxsniffguard.Node, client *RealityClientConfig) []byte {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	flight := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			flight <- nil
			return
		}
		defer conn.Close()
		var stream []byte
		buffer := make([]byte, 64*1024)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for !lxGuardHelloComplete(stream) {
			n, err := conn.Read(buffer)
			if err != nil {
				break
			}
			stream = append(stream, buffer[:n]...)
		}
		flight <- stream
	}()
	raw, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	defer raw.Close()
	handshake := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := ClientHandshake(ctx, raw, client)
		return err
	}
	if node == nil {
		err = handshake(context.Background())
	} else {
		err = node.Dial(context.Background(), handshake)
	}
	require.Error(t, err, "the peer hangs up after the first flight")
	select {
	case stream := <-flight:
		require.True(t, lxGuardHelloComplete(stream), "peer did not get the whole ClientHello")
		return stream
	case <-time.After(10 * time.Second):
		t.Fatal("peer never saw the first flight")
		return nil
	}
}

func lxGuardHelloComplete(stream []byte) bool {
	types, payloads := lxGuardRecords(stream)
	if len(payloads) == 0 || types[0] != 22 || len(payloads[0]) < 4 {
		return false
	}
	helloLength := 4 + (int(payloads[0][1])<<16 | int(payloads[0][2])<<8 | int(payloads[0][3]))
	covered := 0
	for _, payload := range payloads {
		covered += len(payload)
	}
	return covered >= helloLength
}

func TestLxSniffGuardReality(t *testing.T) {
	t.Parallel()
	// No Attempt: one record, as upstream.
	flight := lxGuardRealityFlight(t, nil, lxNewRealityClient(t, "chrome", "", false, false))
	require.Equal(t, 1, lxTLSRecordCount(t, flight))

	// Suspect with record_fragment: the REALITY hello is split into records.
	suspect := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardSuspect, false)
	flight = lxGuardRealityFlight(t, suspect, lxNewRealityClient(t, "chrome", "", false, false))
	require.GreaterOrEqual(t, lxTLSRecordCount(t, flight), 2)

	// Override on a clean path drops the node's own record_fragment.
	clean := lxGuardNode(t, option.LXSniffGuardTLSRecordFragment, option.LXSniffGuardClean, true)
	flight = lxGuardRealityFlight(t, clean, lxNewRealityClient(t, "chrome", "", false, true))
	require.Equal(t, 1, lxTLSRecordCount(t, flight))
}

// The REALITY UConn sits on the guard's conn, not on a node tf.Conn stacked
// over it: the engine skipped its own wrapper.
func TestLxSniffGuardRealityEngineSkipsNodeWrapper(t *testing.T) {
	t.Parallel()
	client := lxNewRealityClient(t, "chrome", "", false, true)
	node := lxGuardNode(t, option.LXSniffGuardDelay, option.LXSniffGuardSuspect, true)
	shaped := lxGuardShapedConn(t, node, client)
	_, isShaped := shaped.(*sniffGuardConn)
	require.True(t, isShaped, "got %T", shaped)
	uConn, _, err := client.prepareClientHello(shaped)
	require.NoError(t, err)
	require.Same(t, shaped, uConn.NetConn())
}
