package libbox

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 115 — CommandClient.GetTailscaleStatus forwards the tag; the peer
// path and health reach the gomobile object as strings / an iterator.

type tailscaleStatusClient struct {
	daemon.StartedServiceClient
	request  *daemon.TailscaleStatusRequest
	response *daemon.TailscaleEndpointStatus
	err      error
}

func (c *tailscaleStatusClient) GetTailscaleStatus(ctx context.Context, request *daemon.TailscaleStatusRequest, opts ...grpc.CallOption) (*daemon.TailscaleEndpointStatus, error) {
	c.request = request
	return c.response, c.err
}

func TestCommandClientGetTailscaleStatus_LX(t *testing.T) {
	fake := &tailscaleStatusClient{response: &daemon.TailscaleEndpointStatus{
		EndpointTag:  "ts",
		BackendState: "Running",
		Health:       []string{"not connected to DERP"},
		ExitNode: &daemon.TailscalePeer{
			StableID: "n1", ExitNode: true,
			Path: daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DERP, DerpRegionCode: "fra", LastHandshake: 1790000000,
		},
		UserGroups: []*daemon.TailscaleUserGroup{{Peers: []*daemon.TailscalePeer{
			{StableID: "n2", Path: daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_DIRECT, Endpoint: "203.0.113.7:41641", DerpRegionCode: "ams"},
			{StableID: "n3", Path: daemon.TailscalePeerPath_TAILSCALE_PEER_PATH_PEER_RELAY, PeerRelay: "198.51.100.2:3340:7"},
			{StableID: "n4"},
		}}},
	}}
	client := NewCommandClient(nil, &CommandClientOptions{})
	client.grpcClient = fake
	result, err := client.GetTailscaleStatus("ts")
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.EndpointTag != "ts" {
		t.Fatalf("request: %+v", fake.request)
	}
	if result.EndpointTag != "ts" || result.BackendState != "Running" {
		t.Fatalf("status: %+v", result)
	}
	health := result.Health()
	if warning := health.Next(); warning != "not connected to DERP" || health.HasNext() {
		t.Fatalf("health: %q", warning)
	}
	if exit := result.ExitNode; exit.Path != "derp" || exit.DERPRegionCode != "fra" || exit.LastHandshake != 1790000000 {
		t.Fatalf("exit node: %+v", exit)
	}
	peers := result.UserGroups().Next().Peers()
	if direct := peers.Next(); direct.Path != "direct" || direct.Endpoint != "203.0.113.7:41641" || direct.DERPRegionCode != "ams" {
		t.Fatalf("direct: %+v", direct)
	}
	if relay := peers.Next(); relay.Path != "peer_relay" || relay.PeerRelay != "198.51.100.2:3340:7" {
		t.Fatalf("peer relay: %+v", relay)
	}
	if none := peers.Next(); none.Path != "" || none.Endpoint != "" || peers.HasNext() {
		t.Fatalf("none: %+v", none)
	}

	fake.response, fake.err = nil, status.Error(codes.NotFound, "endpoint not found: ts")
	if result, err = client.GetTailscaleStatus("ts"); result != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("result %+v, err %v", result, err)
	}
}
