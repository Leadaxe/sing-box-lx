//go:build !with_lx_command

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 106 — without with_lx_command SetEndpointEnabled answers
// Unimplemented.
func TestSetEndpointEnabledStub_LX(t *testing.T) {
	service := &StartedService{}
	_, err := service.SetEndpointEnabled(context.Background(), &SetEndpointEnabledRequest{Tag: "wg", Enabled: false})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", err)
	}
}
