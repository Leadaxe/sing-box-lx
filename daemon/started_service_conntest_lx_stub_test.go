//go:build !with_lx_command

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPECS/TASKS/139-CONNECTION_TEST — without with_lx_command the RPC
// answers Unimplemented like every other stubbed lx handler: a tag-less build
// stays behaviourally equivalent to upstream.

type stubConnectionTestStream struct {
	grpc.ServerStream
}

func (s *stubConnectionTestStream) Send(*ConnectionTestProgress) error { return nil }
func (s *stubConnectionTestStream) Context() context.Context           { return context.Background() }

func TestStartConnectionTestStub_LX(t *testing.T) {
	service := &StartedService{}
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: "https://example.invalid"}, &stubConnectionTestStream{})
	if err == nil {
		t.Fatal("expected Unimplemented from the stub")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", err)
	}
}
