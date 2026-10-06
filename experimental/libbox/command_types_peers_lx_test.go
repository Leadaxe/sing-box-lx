package libbox

import (
	"testing"

	"github.com/sagernet/sing-box/daemon"
)

// lx: SPEC 114 — peers reach OutboundGroupItem through an iterator getter
// (gomobile: no slice fields).
func TestOutboundGroupItemListFromGRPC_peers(t *testing.T) {
	iterator := outboundGroupItemListFromGRPC(&daemon.OutboundList{Outbounds: []*daemon.GroupItem{
		{Tag: "wg", Type: "wireguard", EndpointState: "up", Peers: []*daemon.PeerStatus{
			{PublicKey: "a", Endpoint: "203.0.113.7:41022", LastHandshakeUnix: 1790000000, RxBytes: 1200, TxBytes: 340},
			{PublicKey: "b"},
		}},
		{Tag: "proxy", Type: "vless"},
	}})
	wg, proxy := iterator.Next(), iterator.Next()
	peers := wg.Peers()
	a := peers.Next()
	if a.PublicKey != "a" || a.Endpoint != "203.0.113.7:41022" || a.LastHandshakeUnix != 1790000000 || a.RxBytes != 1200 || a.TxBytes != 340 {
		t.Fatalf("peer a: %+v", a)
	}
	if b := peers.Next(); b.PublicKey != "b" || b.LastHandshakeUnix != 0 || peers.HasNext() {
		t.Fatalf("peer b: %+v", b)
	}
	if proxy.Peers().HasNext() {
		t.Fatal("a plain outbound has no peers")
	}
}
