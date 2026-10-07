//go:build with_lx_command

package daemon

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 115 — GetTailscaleStatus resolves the endpoint by tag, takes a fresh
// snapshot from the provider and converts the peer path and health.

type tailscaleListedEndpoint struct {
	listedEndpoint
	status *adapter.TailscaleEndpointStatus
	err    error
}

func (e *tailscaleListedEndpoint) Type() string { return "tailscale" }

func (e *tailscaleListedEndpoint) TailscaleStatus() (*adapter.TailscaleEndpointStatus, error) {
	return e.status, e.err
}

func TestGetTailscaleStatus_LX(t *testing.T) {
	ts := &tailscaleListedEndpoint{
		listedEndpoint: listedEndpoint{tag: "ts"},
		status: &adapter.TailscaleEndpointStatus{
			BackendState: "Running",
			Health:       []string{"not connected to DERP"},
			ExitNode:     &adapter.TailscalePeer{StableID: "n1", ExitNode: true, Path: "derp", DERPRegionCode: "fra", LastHandshake: 1790000000},
			UserGroups: []*adapter.TailscaleUserGroup{{Peers: []*adapter.TailscalePeer{
				{StableID: "n2", Path: "direct", Endpoint: "203.0.113.7:41641", DERPRegionCode: "ams"},
				{StableID: "n3", Path: "peer_relay", PeerRelay: "198.51.100.2:3340:7"},
				{StableID: "n4"},
			}}},
		},
	}
	wg := &listedEndpoint{tag: "wg"}
	endpointManager := &gettingEndpointManager{listingEndpointManager{endpoints: []adapter.Endpoint{ts, wg}}}
	service := &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   service.ContextWith[adapter.EndpointManager](context.Background(), endpointManager),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			endpointManager:       endpointManager,
		},
	}

	response, err := service.GetTailscaleStatus(context.Background(), &TailscaleStatusRequest{EndpointTag: "ts"})
	if err != nil {
		t.Fatal(err)
	}
	if response.EndpointTag != "ts" || response.BackendState != "Running" || len(response.Health) != 1 {
		t.Fatalf("status: %+v", response)
	}
	if exit := response.ExitNode; exit.Path != TailscalePeerPath_TAILSCALE_PEER_PATH_DERP || exit.DerpRegionCode != "fra" || exit.LastHandshake != 1790000000 {
		t.Fatalf("exit node: %+v", exit)
	}
	peers := response.UserGroups[0].Peers
	if peers[0].Path != TailscalePeerPath_TAILSCALE_PEER_PATH_DIRECT || peers[0].Endpoint != "203.0.113.7:41641" || peers[0].DerpRegionCode != "ams" {
		t.Fatalf("direct: %+v", peers[0])
	}
	if peers[1].Path != TailscalePeerPath_TAILSCALE_PEER_PATH_PEER_RELAY || peers[1].PeerRelay != "198.51.100.2:3340:7" {
		t.Fatalf("peer relay: %+v", peers[1])
	}
	if peers[2].Path != TailscalePeerPath_TAILSCALE_PEER_PATH_NONE || peers[2].Endpoint != "" {
		t.Fatalf("none: %+v", peers[2])
	}

	cases := []struct {
		name string
		tag  string
		err  error
		code codes.Code
	}{
		{"unknown tag", "missing", nil, codes.NotFound},
		{"not tailscale", "wg", nil, codes.InvalidArgument},
		{"endpoint not started", "ts", E.New("tailscale endpoint is not started"), codes.FailedPrecondition},
	}
	for _, tc := range cases {
		ts.err = tc.err
		_, err := service.GetTailscaleStatus(context.Background(), &TailscaleStatusRequest{EndpointTag: tc.tag})
		if status.Code(err) != tc.code {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.code)
		}
	}

	stopped := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	if _, err := stopped.GetTailscaleStatus(context.Background(), &TailscaleStatusRequest{EndpointTag: "ts"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("not started: %v", err)
	}
}
