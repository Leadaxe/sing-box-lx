//go:build with_gvisor

package tailscale

import (
	"reflect"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/tailscale/ipn/ipnstate"
)

// lx: SPEC 115 — the path verdict follows magicsock's choice: direct address,
// then peer relay, then DERP once the node has ever sent, else none.
func TestFillTailscalePeerPath_LX(t *testing.T) {
	handshake := time.Unix(1790000000, 0)
	sent := time.Unix(1790000100, 0)
	cases := []struct {
		name string
		peer ipnstate.PeerStatus
		want adapter.TailscalePeer
	}{
		{"direct", ipnstate.PeerStatus{CurAddr: "203.0.113.7:41641", Relay: "fra", LastWrite: sent, LastHandshake: handshake},
			adapter.TailscalePeer{Path: "direct", Endpoint: "203.0.113.7:41641", DERPRegionCode: "fra", LastHandshake: handshake.Unix()}},
		{"direct chosen while idle", ipnstate.PeerStatus{CurAddr: "203.0.113.7:41641", Relay: "fra"},
			adapter.TailscalePeer{Path: "direct", Endpoint: "203.0.113.7:41641", DERPRegionCode: "fra"}},
		{"peer relay", ipnstate.PeerStatus{PeerRelay: "198.51.100.2:3340:7", Relay: "ams", LastWrite: sent},
			adapter.TailscalePeer{Path: "peer_relay", PeerRelay: "198.51.100.2:3340:7", DERPRegionCode: "ams"}},
		{"derp", ipnstate.PeerStatus{Relay: "fra", LastWrite: sent, LastHandshake: handshake},
			adapter.TailscalePeer{Path: "derp", DERPRegionCode: "fra", LastHandshake: handshake.Unix()}},
		{"never sent", ipnstate.PeerStatus{Relay: "fra"},
			adapter.TailscalePeer{Path: "", DERPRegionCode: "fra"}},
		{"region unknown", ipnstate.PeerStatus{},
			adapter.TailscalePeer{}},
	}
	for _, tc := range cases {
		var got adapter.TailscalePeer
		fillTailscalePeerPath(&got, &tc.peer)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// convertTailscaleStatus carries the backend health warnings through.
func TestConvertTailscaleStatus_health_LX(t *testing.T) {
	status := &ipnstate.Status{BackendState: "Running", Health: []string{"not connected to DERP"}}
	result := convertTailscaleStatus(status)
	if len(result.Health) != 1 || result.Health[0] != "not connected to DERP" {
		t.Fatalf("health: %+v", result.Health)
	}
}
