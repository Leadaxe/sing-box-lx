//go:build !with_lx_command

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 035 — without with_lx_command the GetDNSGroups RPC must answer
// Unimplemented (behavioural equivalence with upstream), not panic or hang.
func TestGetDNSGroupsStub_LX(t *testing.T) {
	service := &StartedService{}
	_, err := service.GetDNSGroups(context.Background(), nil)
	if err == nil {
		t.Fatal("expected Unimplemented from the stub")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", err)
	}
}
