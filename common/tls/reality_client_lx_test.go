//go:build with_utls

package tls

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net"
	"testing"
	"time"

	tf "github.com/sagernet/sing-box/common/tlsfragment"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 088 / SPEC 089 / SPEC 090 guards for the REALITY client (and, for 090,
// the REALITY server).
//
// SPEC 088: `fragment` / `record_fragment` (and the SPEC 060 detour default) are
// applied to the REALITY ClientHello. Upstream builds the UConn on the bare conn,
// so the flags were written to the config and ignored — the test that catches a
// merge putting that back is TestLxRealityClientHelloGoesThroughFragmentConn.
//
// SPEC 089: `reality.key_share` — "" keeps what the fingerprint carries (the
// SPEC 083 contract), "classical" strips X25519MLKEM768 from key_share and
// supported_groups, "hybrid" fails early when the fingerprint has no hybrid share.
//
// SPEC 090: `short_id` longer than 16 hex characters must be a config error, not
// a panic — upstream decodes into a [8]byte without checking the length first,
// and hex.Decode writes len(src)/2 bytes regardless of the destination's size.

func lxRealityOptions(fingerprint, keyShare string, fragment, recordFragment bool) option.OutboundTLSOptions {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return option.OutboundTLSOptions{
		Enabled:        true,
		ServerName:     "www.example.com",
		Fragment:       fragment,
		RecordFragment: recordFragment,
		UTLS:           &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fingerprint},
		Reality: &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
			ShortID:   "0123abcd",
			KeyShare:  keyShare,
		},
	}
}

func lxNewRealityClient(t *testing.T, fingerprint, keyShare string, fragment, recordFragment bool) *RealityClientConfig {
	t.Helper()
	config, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", lxRealityOptions(fingerprint, keyShare, fragment, recordFragment))
	require.NoError(t, err)
	reality, isReality := config.(*RealityClientConfig)
	require.True(t, isReality, "expected *RealityClientConfig, got %T", config)
	return reality
}

// lxRealityPrepare builds the ClientHello the client would send on a pipe and
// returns the UConn for inspection — nothing is written.
func lxRealityPrepare(t *testing.T, client *RealityClientConfig) *utls.UConn {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() {
		conn.Close()
		peer.Close()
	})
	uConn, verifier, err := client.prepareClientHello(conn)
	require.NoError(t, err)
	require.NotNil(t, verifier)
	require.NotNil(t, verifier.authKey, "AuthKey must be derived before the hello is sent")
	return uConn
}

// lxTLSRecordCount splits a byte stream into TLS records and returns how many
// handshake records it holds; a broken stream yields -1.
func lxTLSRecordCount(t *testing.T, stream []byte) int {
	t.Helper()
	count := 0
	for len(stream) > 0 {
		if len(stream) < 5 {
			return -1
		}
		if stream[0] != 0x16 {
			return -1
		}
		length := int(stream[3])<<8 | int(stream[4])
		if len(stream) < 5+length {
			return -1
		}
		stream = stream[5+length:]
		count++
	}
	return count
}

// lxRealityFirstFlight runs the handshake against a pipe whose far end reads the
// first flight and hangs up, and returns the bytes that reached the wire.
func lxRealityFirstFlight(t *testing.T, client *RealityClientConfig) []byte {
	t.Helper()
	conn, peer := net.Pipe()
	firstFlight := make(chan []byte, 1)
	go func() {
		defer peer.Close()
		buffer := make([]byte, 64*1024)
		n, err := peer.Read(buffer)
		if err != nil {
			firstFlight <- nil
			return
		}
		firstFlight <- buffer[:n]
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.ClientHandshake(ctx, conn)
	require.Error(t, err, "peer hangs up after the first flight; the handshake must fail, not succeed")
	conn.Close()
	select {
	case flight := <-firstFlight:
		require.NotNil(t, flight, "peer read nothing")
		return flight
	case <-time.After(5 * time.Second):
		t.Fatal("peer never saw the first flight")
		return nil
	}
}

// --- SPEC 088 -----------------------------------------------------------------

// The REALITY client must hand the fragment wrappers the same conn the plain
// uTLS client would: with either flag set the UConn sits on a *tf.Conn, with
// neither it sits on the raw conn (no accidental wrapping cost).
func TestLxRealityClientHelloGoesThroughFragmentConn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                     string
		fragment, recordFragment bool
		wantWrapped              bool
	}{
		{"none", false, false, false},
		{"fragment", true, false, true},
		{"record_fragment", false, true, true},
		{"both", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uConn := lxRealityPrepare(t, lxNewRealityClient(t, "chrome", "", tc.fragment, tc.recordFragment))
			_, isTF := uConn.NetConn().(*tf.Conn)
			require.Equal(t, tc.wantWrapped, isTF, "UConn.NetConn() is %T", uConn.NetConn())
		})
	}
}

// End to end on the wire: with record_fragment the REALITY first flight is more
// than one TLS record (tlsfragment splits the first record around the SNI);
// without it, exactly one. Before SPEC 088 both cases sent one record.
func TestLxRealityRecordFragmentSplitsFirstFlight(t *testing.T) {
	t.Parallel()
	plain := lxRealityFirstFlight(t, lxNewRealityClient(t, "chrome", "", false, false))
	require.Equal(t, 1, lxTLSRecordCount(t, plain), "without fragmentation the ClientHello is one record")

	fragmented := lxRealityFirstFlight(t, lxNewRealityClient(t, "chrome", "", false, true))
	require.GreaterOrEqual(t, lxTLSRecordCount(t, fragmented), 2, "record_fragment must split the REALITY ClientHello into several TLS records")
}

// The SPEC 060 default — record_fragment switched on when the outbound dials
// through a detour — must reach the REALITY wire, not just the config struct.
// Built through NewClientWithOptions with DialedThroughDetour and no fragment
// flags of its own, so the only thing that can split the first flight is the
// default. Test idea by Alex01d (PR #23): checking the flag alone was
// false-green before SPEC 088, the bytes were not.
func TestLxRealityDetourDefaultFragmentsFirstFlight(t *testing.T) {
	t.Parallel()
	options := lxRealityOptions("chrome", "", false, false)
	config, err := NewClientWithOptions(ClientOptions{
		Context:             context.Background(),
		Logger:              logger.NOP(),
		ServerAddress:       "www.example.com",
		Options:             options,
		DialedThroughDetour: true,
	})
	require.NoError(t, err)
	client, isReality := config.(*RealityClientConfig)
	require.True(t, isReality, "expected *RealityClientConfig, got %T", config)
	require.True(t, client.uClient.recordFragment, "detour default must reach the REALITY uTLS config")

	flight := lxRealityFirstFlight(t, client)
	require.GreaterOrEqual(t, lxTLSRecordCount(t, flight), 2, "detour default must split the REALITY ClientHello on the wire")
}

// --- SPEC 089 -----------------------------------------------------------------

// Default (empty) key_share keeps SPEC 083: the fingerprint's hybrid share goes
// on the wire, ahead of X25519, for all three presets the fork stands on.
func TestLxRealityKeyShareDefaultKeepsHybrid(t *testing.T) {
	t.Parallel()
	for _, fingerprint := range []string{"chrome", "firefox", "safari", "random", "randomized"} { // lx: SPEC 123 — random/randomized
		t.Run(fingerprint, func(t *testing.T) {
			t.Parallel()
			uConn := lxRealityPrepare(t, lxNewRealityClient(t, fingerprint, C.RealityKeyShareDefault, false, false))
			hello := uConn.HandshakeState.Hello
			hybridIndex, hybridCount := lxIndexOfShare(hello.KeyShares, utls.X25519MLKEM768)
			classicalIndex, _ := lxIndexOfShare(hello.KeyShares, utls.X25519)
			require.NotEqual(t, -1, hybridIndex, "hybrid share missing from the wire hello")
			require.Equal(t, 1, hybridCount)
			require.Less(t, hybridIndex, classicalIndex, "X25519MLKEM768 must precede X25519")
			require.NotEqual(t, -1, lxIndexOfCurve(hello.SupportedCurves, utls.X25519MLKEM768))
		})
	}
}

// "classical" removes X25519MLKEM768 from both key_share and supported_groups —
// the pre-083 upstream ClientHello, ~1.2 KB smaller — and leaves the X25519
// share (with its private key, which REALITY derives AuthKey from) in place.
func TestLxRealityKeyShareClassicalStripsHybrid(t *testing.T) {
	t.Parallel()
	for _, fingerprint := range []string{"chrome", "firefox", "safari", "random", "randomized"} { // lx: SPEC 123 — random/randomized
		t.Run(fingerprint, func(t *testing.T) {
			t.Parallel()
			hybrid := lxRealityPrepare(t, lxNewRealityClient(t, fingerprint, C.RealityKeyShareDefault, false, false))
			classical := lxRealityPrepare(t, lxNewRealityClient(t, fingerprint, C.RealityKeyShareClassical, false, false))
			hello := classical.HandshakeState.Hello

			hybridIndex, _ := lxIndexOfShare(hello.KeyShares, utls.X25519MLKEM768)
			require.Equal(t, -1, hybridIndex, "X25519MLKEM768 must be gone from key_share")
			classicalIndex, classicalCount := lxIndexOfShare(hello.KeyShares, utls.X25519)
			require.NotEqual(t, -1, classicalIndex, "X25519 share must stay")
			require.Equal(t, 1, classicalCount)
			require.Len(t, hello.KeyShares[classicalIndex].Data, 32)
			require.Equal(t, -1, lxIndexOfCurve(hello.SupportedCurves, utls.X25519MLKEM768), "X25519MLKEM768 must be gone from supported_groups")
			require.NotEqual(t, -1, lxIndexOfCurve(hello.SupportedCurves, utls.X25519))

			// Wire size, deterministically: a hello that carries the 1184-byte ML-KEM-768
			// encapsulation key cannot be shorter than it, and one without it stays under
			// (Chrome's GREASE ECH payload is random-sized, so two independently built
			// hellos are not compared against each other).
			require.GreaterOrEqual(t, len(hybrid.HandshakeState.Hello.Raw), 1184+32, "hybrid hello must carry the ML-KEM share (%d bytes)", len(hybrid.HandshakeState.Hello.Raw))
			require.Less(t, len(hello.Raw), 1184, "classical hello must be shorter than an ML-KEM-768 encapsulation key alone (%d bytes)", len(hello.Raw))

			// AuthKey source (SPEC 083 contract): the X25519 private key is still there
			// and matches the share on the wire.
			keys := classical.HandshakeState.State13.KeyShareKeys
			require.NotNil(t, keys)
			require.NotNil(t, keys.Ecdhe, "Ecdhe must remain — REALITY derives AuthKey from it")
			require.Equal(t, hello.KeyShares[classicalIndex].Data, keys.Ecdhe.PublicKey().Bytes())
		})
	}
}

// "hybrid" is a check, not an action: on a fingerprint that carries the share it
// changes nothing; on one that does not (edge = HelloEdge_85, X25519 only) the
// handshake fails at once with a config-shaped error instead of the silent
// `reality verification failed` the server would have produced.
func TestLxRealityKeyShareHybridRequiresShare(t *testing.T) {
	t.Parallel()
	uConn := lxRealityPrepare(t, lxNewRealityClient(t, "chrome", C.RealityKeyShareHybrid, false, false))
	hybridIndex, _ := lxIndexOfShare(uConn.HandshakeState.Hello.KeyShares, utls.X25519MLKEM768)
	require.NotEqual(t, -1, hybridIndex)

	edge := lxNewRealityClient(t, "edge", C.RealityKeyShareHybrid, false, false)
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	_, _, err := edge.prepareClientHello(conn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "carries no X25519MLKEM768 key share")
	require.Contains(t, err.Error(), "Edge")
}

// Typos are rejected when the outbound is built, not turned into the default.
func TestLxRealityKeyShareUnknownValueRejected(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"classic", "mlkem", "Hybrid", "auto"} {
		_, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", lxRealityOptions("chrome", value, false, false))
		require.Error(t, err, value)
		require.Contains(t, err.Error(), "unknown reality key_share", value)
	}
}

// Clone() must carry the policy: outbounds clone the TLS config per dial.
func TestLxRealityKeyShareSurvivesClone(t *testing.T) {
	t.Parallel()
	client := lxNewRealityClient(t, "chrome", C.RealityKeyShareClassical, false, false)
	cloned, isReality := client.Clone().(*RealityClientConfig)
	require.True(t, isReality)
	require.Equal(t, C.RealityKeyShareClassical, cloned.keyShare)
	hybridIndex, _ := lxIndexOfShare(lxRealityPrepare(t, cloned).HandshakeState.Hello.KeyShares, utls.X25519MLKEM768)
	require.Equal(t, -1, hybridIndex)
}

// --- SPEC 090 -----------------------------------------------------------------

// lxRealityShortIDCases is the length matrix both constructors must agree on:
// legal values stay legal, an over-long one is a config error (and used to be a
// panic inside hex.Decode), malformed ones keep the upstream decode error.
var lxRealityShortIDCases = []struct {
	name     string
	shortID  string
	errorSub string // "" = must be accepted
}{
	{"empty", "", ""},
	{"eight", "0123abcd", ""},
	{"sixteen", "0123456789abcdef", ""},
	{"eighteen", "0123456789abcdef01", "invalid short_id"},
	{"odd", "abc", "decode short_id"},
	{"not-hex", "zz", "decode short_id"},
}

// Client side: 18 hex characters used to run off the end of the [8]byte inside
// hex.Decode and kill the process; the post-decode `decodedLen > 8` check never
// got a chance to run.
func TestLxRealityShortIDTooLongRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range lxRealityShortIDCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := lxRealityOptions("chrome", "", false, false)
			options.Reality.ShortID = tc.shortID
			_, err := NewRealityClient(context.Background(), logger.NOP(), "www.example.com", options)
			if tc.errorSub == "" {
				require.NoError(t, err, tc.shortID)
				return
			}
			require.Error(t, err, tc.shortID)
			require.ErrorContains(t, err, tc.errorSub)
		})
	}
}

// Server side: the same string in the `short_id[]` loop of NewRealityServer.
// The error names the index, like the decode error next to it.
func TestLxRealityServerShortIDTooLongRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range lxRealityShortIDCases {
		if tc.shortID == "" {
			continue // an empty list is the "zero short_id" branch, not a decode
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRealityServer(context.Background(), logger.NOP(), lxRealityServerOptions(tc.shortID))
			if tc.errorSub == "" {
				require.NoError(t, err, tc.shortID)
				return
			}
			require.Error(t, err, tc.shortID)
			require.ErrorContains(t, err, tc.errorSub)
			require.ErrorContains(t, err, "[0]")
		})
	}
}

// lxRealityServerOptions is the smallest inbound config NewRealityServer accepts:
// a server name, a 32-byte private key and a handshake destination. The
// destination is an IP literal on purpose — a domain would send the handshake
// dialer looking for a DNS transport manager that a bare context has not got.
func lxRealityServerOptions(shortID string) option.InboundTLSOptions {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return option.InboundTLSOptions{
		Enabled:    true,
		ServerName: "www.example.com",
		Reality: &option.InboundRealityOptions{
			Enabled: true,
			Handshake: option.InboundRealityHandshakeOptions{
				ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
			},
			PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey.Bytes()),
			ShortID:    badoption.Listable[string]{shortID},
		},
	}
}
