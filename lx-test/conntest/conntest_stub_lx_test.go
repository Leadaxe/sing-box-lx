//go:build !with_lx_command

// lx: SPECS/TASKS/139-CONNECTION_TEST §8.11 — without with_lx_command the RPC
// answers Unimplemented through the public daemon surface. The in-package
// twin is daemon/started_service_conntest_lx_stub_test.go; the 038 guard over
// the CommandClient surface is experimental/libbox/command_client_cgo_frame_lx_test.go.
package conntest

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/include"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubStream struct {
	grpc.ServerStream
}

func (s *stubStream) Send(*daemon.ConnectionTestProgress) error { return nil }
func (s *stubStream) Context() context.Context                  { return context.Background() }

func TestConnectionTestStub(t *testing.T) {
	service := daemon.NewStartedService(daemon.ServiceOptions{Context: include.Context(context.Background())})
	defer service.Close()
	err := service.StartConnectionTest(&daemon.ConnectionTestRequest{Link: "http://example.invalid/"}, &stubStream{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("StartConnectionTest without with_lx_command = %v, want codes.Unimplemented", err)
	}
}
