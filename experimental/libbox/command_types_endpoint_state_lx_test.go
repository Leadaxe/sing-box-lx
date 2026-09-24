package libbox

import (
	"testing"

	"github.com/sagernet/sing-box/daemon"
)

// lx: SPEC 097 — the endpoint state reaches OutboundGroupItem as fields of the
// object (gomobile: no bare string returns).
func TestOutboundGroupItemListFromGRPC_endpointState(t *testing.T) {
	iterator := outboundGroupItemListFromGRPC(&daemon.OutboundList{Outbounds: []*daemon.GroupItem{
		{Tag: "wg", Type: "wireguard", EndpointState: "torn_down", IdleSinceSeconds: 42},
		{Tag: "proxy", Type: "vless"},
	}})
	wg := iterator.Next()
	if wg.EndpointState != "torn_down" || wg.IdleSinceSeconds != 42 {
		t.Fatalf("endpoint item: %+v", wg)
	}
	proxy := iterator.Next()
	if proxy.EndpointState != "" || proxy.IdleSinceSeconds != 0 {
		t.Fatalf("plain item: %+v", proxy)
	}
}
