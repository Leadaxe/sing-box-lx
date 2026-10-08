package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// lx: SPEC 114 — UAPI dump parsing for per-peer status.

func TestParsePeerStatuses(t *testing.T) {
	keyA := strings.Repeat("ab", 32)
	keyB := strings.Repeat("cd", 32)
	dump := strings.Join([]string{
		"private_key=" + strings.Repeat("11", 32),
		"listen_port=51822",
		"jc=6",
		"public_key=" + keyB,
		"preshared_key=" + strings.Repeat("22", 32),
		"protocol_version=1",
		"last_handshake_time_sec=0",
		"last_handshake_time_nsec=0",
		"tx_bytes=0",
		"rx_bytes=0",
		"persistent_keepalive_interval=0",
		"allowed_ip=10.0.0.3/32",
		"public_key=" + keyA,
		"preshared_key=" + strings.Repeat("00", 32),
		"protocol_version=1",
		"endpoint=203.0.113.7:41022",
		"last_handshake_time_sec=1790000000",
		"last_handshake_time_nsec=500",
		"tx_bytes=340",
		"rx_bytes=1200",
		"persistent_keepalive_interval=25",
		"allowed_ip=10.0.0.2/32",
		"",
	}, "\n")
	statuses := parsePeerStatuses(dump)
	if len(statuses) != 2 {
		t.Fatalf("want 2 peers, got %d", len(statuses))
	}
	// Config order, not dump order.
	ordered := orderPeerStatuses(statuses, []peerConfig{{publicKeyHex: keyA}, {publicKeyHex: keyB}})
	if len(ordered) != 2 {
		t.Fatalf("want 2 ordered peers, got %d", len(ordered))
	}
	a, b := ordered[0], ordered[1]
	keyABytes, _ := hex.DecodeString(keyA)
	if a.PublicKey != base64.StdEncoding.EncodeToString(keyABytes) {
		t.Fatalf("public key not base64 of the hex key: %q", a.PublicKey)
	}
	if a.Endpoint != "203.0.113.7:41022" || a.RxBytes != 1200 || a.TxBytes != 340 {
		t.Fatalf("peer A fields: %+v", a)
	}
	if !a.LastHandshake.Equal(time.Unix(1790000000, 500)) {
		t.Fatalf("peer A handshake: %v", a.LastHandshake)
	}
	if b.Endpoint != "" || !b.LastHandshake.IsZero() {
		t.Fatalf("peer B without endpoint/handshake must stay empty/zero: %+v", b)
	}
	for _, status := range ordered {
		for _, secret := range []string{"11111111", "22222222"} {
			if strings.Contains(status.PublicKey+status.Endpoint, secret) {
				t.Fatalf("secret leaked into status: %+v", status)
			}
		}
	}
}

func TestParsePeerStatuses_noPeers(t *testing.T) {
	if statuses := parsePeerStatuses("private_key=" + strings.Repeat("11", 32) + "\nlisten_port=1\n"); len(statuses) != 0 {
		t.Fatalf("device lines must not produce peers: %+v", statuses)
	}
}
