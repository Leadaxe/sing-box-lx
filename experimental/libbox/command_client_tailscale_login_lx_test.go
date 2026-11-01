package libbox

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// lx: SPEC 149 — CommandClient.TailscaleLogin forwards tag and key; the gRPC
// code reaches the caller.

type tailscaleLoginClient struct {
	daemon.StartedServiceClient
	request *daemon.TailscaleLoginRequest
	err     error
}

func (c *tailscaleLoginClient) TailscaleLogin(ctx context.Context, request *daemon.TailscaleLoginRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	c.request = request
	if c.err != nil {
		return nil, c.err
	}
	return &emptypb.Empty{}, nil
}

func TestCommandClientTailscaleLogin_LX(t *testing.T) {
	fake := &tailscaleLoginClient{}
	client := NewCommandClient(nil, &CommandClientOptions{})
	client.grpcClient = fake
	if err := client.TailscaleLogin("ts", "tskey-auth-test"); err != nil {
		t.Fatal(err)
	}
	if fake.request.EndpointTag != "ts" || fake.request.AuthKey != "tskey-auth-test" {
		t.Fatalf("request: %+v", fake.request)
	}

	fake.err = status.Error(codes.FailedPrecondition, "Tailscale is already logged in, log out first")
	if err := client.TailscaleLogin("ts", ""); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err %v", err)
	}
	if fake.request.AuthKey != "" {
		t.Fatalf("request: %+v", fake.request)
	}
}
