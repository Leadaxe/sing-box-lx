package masque

import (
	"testing"
	"time"
)

// lx: SPEC 139 — the tunnel's age and HTTP version reported to a connection test.
func TestTunnelInfoLx(t *testing.T) {
	t.Parallel()
	client := &Client{}
	if _, _, ok := client.TunnelInfoLx(); ok {
		t.Fatal("no session yet → nothing to report")
	}
	client.lxTunnel.opened(true)
	age, vhttp, ok := client.TunnelInfoLx()
	if !ok || vhttp != "h3" || age < 0 || age > time.Minute {
		t.Fatalf("datagram session: age %v vhttp %q ok %v", age, vhttp, ok)
	}
	client.lxTunnel.opened(false)
	if _, vhttp, _ = client.TunnelInfoLx(); vhttp != "h2" {
		t.Fatalf("stream session: vhttp %q, want h2", vhttp)
	}
}
