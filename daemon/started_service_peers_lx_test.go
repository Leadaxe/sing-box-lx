//go:build with_lx_command

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
)

// lx: SPEC 114 — GetOutbounds carries per-peer status of a WG/AWG endpoint.

type peerListedEndpoint struct {
	listedEndpoint
	peers []adapter.PeerStatus
}

func (e *peerListedEndpoint) PeerStatuses() []adapter.PeerStatus { return e.peers }

func TestGetOutbounds_peers_LX(t *testing.T) {
	handshake := time.Unix(1790000000, 999)
	service := &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager: &listingOutboundManager{outbounds: []adapter.Outbound{
				&listedOutbound{tag: "proxy"},
			}},
			endpointManager: &listingEndpointManager{endpoints: []adapter.Endpoint{
				&peerListedEndpoint{
					listedEndpoint: listedEndpoint{tag: "wg-server", state: adapter.IdleState{State: adapter.EndpointStateUp}},
					peers: []adapter.PeerStatus{
						{PublicKey: "client-a", Endpoint: "203.0.113.7:41022", LastHandshake: handshake, RxBytes: 1200, TxBytes: 340},
						{PublicKey: "client-b"},
					},
				},
				&peerListedEndpoint{
					listedEndpoint: listedEndpoint{tag: "wg-cold", state: adapter.IdleState{State: adapter.EndpointStateNeverBuilt}},
				},
			}},
		},
	}
	list, err := service.GetOutbounds(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byTag := make(map[string]*GroupItem)
	for _, item := range list.Outbounds {
		byTag[item.Tag] = item
	}
	if item := byTag["proxy"]; item == nil || len(item.Peers) != 0 {
		t.Fatalf("a plain outbound carries no peers: %+v", item)
	}
	if item := byTag["wg-cold"]; item == nil || len(item.Peers) != 0 || item.EndpointState != "never_built" {
		t.Fatalf("an endpoint without a device carries no peers: %+v", item)
	}
	server := byTag["wg-server"]
	if server == nil || len(server.Peers) != 2 {
		t.Fatalf("server endpoint peers: %+v", server)
	}
	a, b := server.Peers[0], server.Peers[1]
	if a.PublicKey != "client-a" || a.Endpoint != "203.0.113.7:41022" || a.LastHandshakeUnix != handshake.Unix() || a.RxBytes != 1200 || a.TxBytes != 340 {
		t.Fatalf("peer a: %+v", a)
	}
	if b.PublicKey != "client-b" || b.Endpoint != "" || b.LastHandshakeUnix != 0 {
		t.Fatalf("a peer without handshake reports 0, not a negative unix time: %+v", b)
	}
}
