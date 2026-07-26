//go:build !with_lx_command

package daemon

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/option"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 037 — without with_lx_command no snapshot is captured (no per-instance
// memory cost, behavioural equivalence with upstream) and the RPC answers
// Unimplemented like every other stubbed lx handler.
func TestRunningConfigStub_LX(t *testing.T) {
	if captureRunningConfig(option.Options{}) != "" {
		t.Fatal("tag-less build must not capture a snapshot")
	}
	service := &StartedService{}
	_, err := service.GetRunningConfig(context.Background(), nil)
	if err == nil {
		t.Fatal("expected Unimplemented from the stub")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", err)
	}
}
