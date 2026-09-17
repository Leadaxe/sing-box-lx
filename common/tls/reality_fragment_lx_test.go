//go:build with_utls

// lx: SPEC 060 — REALITY must pass its transport connection through the same
// ClientHello fragmentation layer as an ordinary uTLS client.
package tls

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func TestRealityClientHandshakeAppliesDetourRecordFragment(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewClientWithOptions(ClientOptions{
		Context:             context.Background(),
		Logger:              log.NewNOPFactory().NewLogger("test"),
		ServerAddress:       "www.example.com",
		DialedThroughDetour: true,
		Options: option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: "www.example.com",
			Insecure:   true,
			UTLS: &option.OutboundUTLSOptions{
				Enabled:     true,
				Fingerprint: "chrome",
			},
			Reality: &option.OutboundRealityOptions{
				Enabled:   true,
				PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	realityConfig, ok := config.(*RealityClientConfig)
	if !ok {
		t.Fatalf("config type = %T, want *RealityClientConfig", config)
	}
	if !realityConfig.uClient.recordFragment {
		t.Fatal("detour default did not reach the REALITY uTLS config")
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	handshakeDone := make(chan error, 1)
	go func() {
		_, handshakeErr := realityConfig.ClientHandshake(ctx, clientConn)
		handshakeDone <- handshakeErr
	}()

	if err = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	clientHello := make([]byte, 64*1024)
	n, err := serverConn.Read(clientHello)
	if err != nil {
		t.Fatalf("read ClientHello: %v", err)
	}
	clientHello = clientHello[:n]

	recordCount := 0
	for len(clientHello) > 0 {
		if len(clientHello) < 5 {
			t.Fatalf("truncated TLS record header after %d records", recordCount)
		}
		if clientHello[0] != 22 {
			t.Fatalf("TLS record %d type = %d, want handshake (22)", recordCount, clientHello[0])
		}
		recordLength := int(binary.BigEndian.Uint16(clientHello[3:5]))
		if len(clientHello) < 5+recordLength {
			t.Fatalf("TLS record %d length = %d, only %d bytes remain", recordCount, recordLength, len(clientHello)-5)
		}
		recordCount++
		clientHello = clientHello[5+recordLength:]
	}
	if recordCount < 2 {
		t.Fatalf("REALITY ClientHello used %d TLS record; record_fragment was bypassed", recordCount)
	}

	_ = serverConn.Close()
	select {
	case <-handshakeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("REALITY handshake did not stop after closing the peer")
	}
}
