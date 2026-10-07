//go:build with_lx_command

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 114 — GetWireGuardStatus answers device state and per-peer status
// of one WG/AWG endpoint; GetOutbounds no longer carries peers.

type peerListedEndpoint struct {
	listedEndpoint
	peers []adapter.PeerStatus
}

func (e *peerListedEndpoint) PeerStatuses() []adapter.PeerStatus { return e.peers }

type typedEndpoint struct {
	listedEndpoint
	typ string
}

func (e *typedEndpoint) Type() string { return e.typ }

func TestGetWireGuardStatus_LX(t *testing.T) {
	handshake := time.Unix(1790000000, 999)
	server := &peerListedEndpoint{
		listedEndpoint: listedEndpoint{tag: "wg-server", state: adapter.IdleState{State: adapter.EndpointStateUp}},
		peers: []adapter.PeerStatus{
			{PublicKey: "client-a", Endpoint: "203.0.113.7:41022", LastHandshake: handshake, RxBytes: 1200, TxBytes: 340},
			{PublicKey: "client-b"},
		},
	}
	cold := &peerListedEndpoint{
		listedEndpoint: listedEndpoint{tag: "wg-cold", state: adapter.IdleState{State: adapter.EndpointStateNeverBuilt, IdleSince: 90 * time.Second}},
	}
	tailscale := &typedEndpoint{listedEndpoint: listedEndpoint{tag: "ts"}, typ: "tailscale"}
	service := newToggleService(server, cold, tailscale)

	response, err := service.GetWireGuardStatus(context.Background(), &WireGuardStatusRequest{Tag: "wg-server"})
	if err != nil {
		t.Fatal(err)
	}
	if response.EndpointTag != "wg-server" || response.EndpointState != "up" || response.IdleSinceSeconds != 0 || len(response.Peers) != 2 {
		t.Fatalf("server: %+v", response)
	}
	a, b := response.Peers[0], response.Peers[1]
	if a.PublicKey != "client-a" || a.Endpoint != "203.0.113.7:41022" || a.LastHandshakeUnix != handshake.Unix() || a.RxBytes != 1200 || a.TxBytes != 340 {
		t.Fatalf("peer a: %+v", a)
	}
	if b.PublicKey != "client-b" || b.Endpoint != "" || b.LastHandshakeUnix != 0 {
		t.Fatalf("a peer without handshake reports 0, not a negative unix time: %+v", b)
	}

	response, err = service.GetWireGuardStatus(context.Background(), &WireGuardStatusRequest{Tag: "wg-cold"})
	if err != nil || response.EndpointState != "never_built" || response.IdleSinceSeconds != 90 || len(response.Peers) != 0 {
		t.Fatalf("an endpoint without a device: %+v err=%v", response, err)
	}

	cases := []struct {
		name string
		tag  string
		code codes.Code
	}{
		{"unknown tag", "missing", codes.NotFound},
		{"not wireguard", "ts", codes.InvalidArgument},
	}
	for _, tc := range cases {
		_, err := service.GetWireGuardStatus(context.Background(), &WireGuardStatusRequest{Tag: tc.tag})
		if status.Code(err) != tc.code {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.code)
		}
	}

	stopped := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	if _, err := stopped.GetWireGuardStatus(context.Background(), &WireGuardStatusRequest{Tag: "wg-server"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("not started: %v", err)
	}
}
