//go:build !with_lx_command

package daemon

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPECS/TASKS/058-GET_URL_VIA_OUTBOUND — без with_lx_command вызов
// отвечает Unimplemented, как любой другой заглушенный lx-handler: сборка
// без тега остаётся поведенчески эквивалентна апстриму.
func TestGetURLViaOutboundStub_LX(t *testing.T) {
	service := &StartedService{}
	_, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
		OutboundTag: "node",
		Link:        "https://example.invalid",
	})
	if err == nil {
		t.Fatal("expected Unimplemented from the stub")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", err)
	}
}
