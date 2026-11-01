package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
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

// lx: SPEC 145 — session view: handshake of the current session only, transfer
// since the session start next to the device totals.

func TestApplySession_noBase(t *testing.T) {
	endpoint := &Endpoint{}
	handshake := time.Unix(1790000000, 0)
	statuses := map[string]*adapter.PeerStatus{"aa": {LastHandshake: handshake, RxBytes: 100, TxBytes: 40}}
	endpoint.applySession(statuses)
	status := statuses["aa"]
	if !status.LastHandshake.Equal(handshake) || status.SessionRxBytes != 100 || status.SessionTxBytes != 40 {
		t.Fatalf("without a Down the session is the device lifetime: %+v", status)
	}
}

func TestApplySession_afterResume(t *testing.T) {
	endpoint := &Endpoint{}
	since := time.Unix(1790000100, 0)
	endpoint.session.base.Store(&peerSessionBase{
		since:    since,
		counters: map[string]peerCounters{"aa": {rx: 100, tx: 40}, "bb": {rx: 500, tx: 500}},
	})
	statuses := map[string]*adapter.PeerStatus{
		"aa": {LastHandshake: since.Add(-time.Second), RxBytes: 130, TxBytes: 40},
		"bb": {LastHandshake: since.Add(time.Second), RxBytes: 20, TxBytes: 10},
		"cc": {RxBytes: 7, TxBytes: 3},
	}
	endpoint.applySession(statuses)
	if a := statuses["aa"]; !a.LastHandshake.IsZero() || a.SessionRxBytes != 30 || a.SessionTxBytes != 0 || a.RxBytes != 130 {
		t.Fatalf("handshake before the session start is dropped, totals kept: %+v", a)
	}
	if b := statuses["bb"]; !b.LastHandshake.Equal(since.Add(time.Second)) || b.SessionRxBytes != 20 || b.SessionTxBytes != 10 {
		t.Fatalf("new handshake kept; total below the snapshot counts as a fresh start: %+v", b)
	}
	if c := statuses["cc"]; c.SessionRxBytes != 7 || c.SessionTxBytes != 3 {
		t.Fatalf("a peer missing from the snapshot counts from zero: %+v", c)
	}
}

func TestMarkSession(t *testing.T) {
	endpoint := &Endpoint{}
	endpoint.markSessionUp()
	if endpoint.session.base.Load() != nil {
		t.Fatal("an Up without a Down must keep the running session")
	}
	endpoint.markSessionDown()
	endpoint.markSessionUp()
	if endpoint.session.base.Load() == nil {
		t.Fatal("an Up after a Down starts a session")
	}
	endpoint.resetSession()
	if endpoint.session.base.Load() != nil || endpoint.session.down.Load() {
		t.Fatal("reset forgets the session")
	}
}
