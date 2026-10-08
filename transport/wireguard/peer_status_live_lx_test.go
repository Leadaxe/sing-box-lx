//go:build with_gvisor

package wireguard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/crypto/curve25519"
)

// lx: SPEC 114 — live per-peer status on a real client/server pair over
// loopback. The server peer has no address (the owner's server config): the
// server learns the client's address from its handshake, and both sides report
// the handshake time.

func newPeerStatusKey(t *testing.T) (private string, public string) {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64
	pub, err := curve25519.X25519(key[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key[:]), base64.StdEncoding.EncodeToString(pub)
}

func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return uint16(conn.LocalAddr().(*net.UDPAddr).Port)
}

func startPeerStatusEndpoint(t *testing.T, options EndpointOptions) *Endpoint {
	t.Helper()
	ctx := context.Background()
	outboundDialer, err := dialer.NewDefault(ctx, option.DialerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	options.Context = ctx
	options.Logger = logger.NOP()
	options.Dialer = outboundDialer
	options.MTU = 1408
	endpoint, err := NewEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	for _, postStart := range []bool{false, true} {
		if err = endpoint.Start(postStart); err != nil {
			t.Fatal(err)
		}
	}
	return endpoint
}

func TestPeerStatuses_serverLearnsEndpoint(t *testing.T) {
	serverPrivate, serverPublic := newPeerStatusKey(t)
	clientPrivate, clientPublic := newPeerStatusKey(t)
	serverPort := freeUDPPort(t)

	server := startPeerStatusEndpoint(t, EndpointOptions{
		Address:    []netip.Prefix{netip.MustParsePrefix("10.99.2.1/32")},
		PrivateKey: serverPrivate,
		ListenPort: serverPort,
		Peers: []PeerOptions{{
			PublicKey:  clientPublic,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.2.2/32")},
		}},
	})
	serverAddress := "127.0.0.1:" + strconv.Itoa(int(serverPort))
	client := startPeerStatusEndpoint(t, EndpointOptions{
		Address:    []netip.Prefix{netip.MustParsePrefix("10.99.2.2/32")},
		PrivateKey: clientPrivate,
		Peers: []PeerOptions{{
			Endpoint:   M.ParseSocksaddr(serverAddress),
			PublicKey:  serverPublic,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.2.1/32")},
		}},
	})

	before := server.PeerStatuses()
	if len(before) != 1 || before[0].PublicKey != clientPublic {
		t.Fatalf("server must list the configured peer by its base64 key: %+v", before)
	}
	if before[0].Endpoint != "" || !before[0].LastHandshake.IsZero() {
		t.Fatalf("before any packet the server knows neither address nor handshake: %+v", before[0])
	}

	packetConn, err := client.ListenPacket(context.Background(), M.ParseSocksaddr("10.99.2.1:9"))
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	target := M.ParseSocksaddr("10.99.2.1:9").UDPAddr()

	deadline := time.Now().Add(15 * time.Second)
	for {
		_, _ = packetConn.WriteTo([]byte("ping"), target)
		statuses := server.PeerStatuses()
		if len(statuses) == 1 && !statuses[0].LastHandshake.IsZero() && statuses[0].Endpoint != "" {
			if !strings.HasPrefix(statuses[0].Endpoint, "127.0.0.1:") {
				t.Fatalf("server learned an unexpected endpoint: %q", statuses[0].Endpoint)
			}
			if statuses[0].RxBytes == 0 {
				t.Fatalf("handshake counted no received bytes: %+v", statuses[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no handshake seen by the server: %+v", statuses)
		}
		time.Sleep(100 * time.Millisecond)
	}

	clientStatuses := client.PeerStatuses()
	if len(clientStatuses) != 1 || clientStatuses[0].PublicKey != serverPublic ||
		clientStatuses[0].Endpoint != serverAddress || clientStatuses[0].LastHandshake.IsZero() {
		t.Fatalf("client status: %+v", clientStatuses)
	}

	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	if statuses := server.PeerStatuses(); statuses != nil {
		t.Fatalf("a closed endpoint reports no peers: %+v", statuses)
	}
}
