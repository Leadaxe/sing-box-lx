package libbox

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 114 — CommandClient.GetWireGuardStatus forwards the tag and
// returns state + peers through an iterator getter (gomobile: no slice fields).

type wireGuardStatusClient struct {
	daemon.StartedServiceClient
	request  *daemon.WireGuardStatusRequest
	response *daemon.WireGuardEndpointStatus
	err      error
}

func (c *wireGuardStatusClient) GetWireGuardStatus(ctx context.Context, request *daemon.WireGuardStatusRequest, opts ...grpc.CallOption) (*daemon.WireGuardEndpointStatus, error) {
	c.request = request
	return c.response, c.err
}

func TestCommandClientGetWireGuardStatus_LX(t *testing.T) {
	fake := &wireGuardStatusClient{response: &daemon.WireGuardEndpointStatus{
		EndpointTag: "wg", EndpointState: "up", IdleSinceSeconds: 7,
		Peers: []*daemon.PeerStatus{
			{PublicKey: "a", Endpoint: "203.0.113.7:41022", LastHandshakeUnix: 1790000000, RxBytes: 1200, TxBytes: 340},
			{PublicKey: "b"},
		},
	}}
	client := NewCommandClient(nil, &CommandClientOptions{})
	client.grpcClient = fake
	result, err := client.GetWireGuardStatus("wg")
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.Tag != "wg" {
		t.Fatalf("request: %+v", fake.request)
	}
	if result.EndpointTag != "wg" || result.EndpointState != "up" || result.IdleSinceSeconds != 7 {
		t.Fatalf("status: %+v", result)
	}
	peers := result.Peers()
	a := peers.Next()
	if a.PublicKey != "a" || a.Endpoint != "203.0.113.7:41022" || a.LastHandshakeUnix != 1790000000 || a.RxBytes != 1200 || a.TxBytes != 340 {
		t.Fatalf("peer a: %+v", a)
	}
	if b := peers.Next(); b.PublicKey != "b" || b.LastHandshakeUnix != 0 || peers.HasNext() {
		t.Fatalf("peer b: %+v", b)
	}

	fake.response = &daemon.WireGuardEndpointStatus{EndpointTag: "wg", EndpointState: "never_built"}
	result, err = client.GetWireGuardStatus("wg")
	if err != nil || result.Peers().HasNext() {
		t.Fatalf("an endpoint without a device has no peers: %+v err=%v", result, err)
	}

	fake.response, fake.err = nil, status.Error(codes.InvalidArgument, "endpoint is not WireGuard: ts")
	if result, err = client.GetWireGuardStatus("ts"); result != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("result %+v, err %v", result, err)
	}
}
