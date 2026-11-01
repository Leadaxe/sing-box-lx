//go:build !with_lx_command

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 149 — without with_lx_command TailscaleLogin answers Unimplemented.
func TestTailscaleLoginStub_LX(t *testing.T) {
	service := &StartedService{}
	_, err := service.TailscaleLogin(context.Background(), &TailscaleLoginRequest{EndpointTag: "ts", AuthKey: "tskey-auth-test"})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", err)
	}
}
