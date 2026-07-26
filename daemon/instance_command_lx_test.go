//go:build with_lx_command

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 037 — the snapshot must be a non-empty, well-formed JSON document that
// carries the outbounds the box was built from, keyed exactly like a config file
// (the client extracts per-node JSON from it by tag). Options are built
// programmatically: marshaling needs no registry (only unmarshal does), which is
// the same property FormatConfig relies on.
func TestCaptureRunningConfig_LX(t *testing.T) {
	options := option.Options{
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}},
		},
	}
	content := captureRunningConfig(options)
	if content == "" {
		t.Fatal("expected a non-empty snapshot")
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v", err)
	}
	outbounds, isList := document["outbounds"].([]any)
	if !isList || len(outbounds) != 1 {
		t.Fatalf("expected one outbound in the snapshot, got: %s", content)
	}
	outbound, isMap := outbounds[0].(map[string]any)
	if !isMap || outbound["tag"] != "direct-out" || outbound["type"] != "direct" {
		t.Fatalf("outbound lost its identity in the snapshot: %s", content)
	}
	if !strings.HasSuffix(strings.TrimRight(content, "\n"), "}") {
		t.Fatalf("snapshot is not a single JSON object: %q", content)
	}
}

// lx: SPEC 037 — handler state machine: not-started → FailedPrecondition; started
// without a captured snapshot (attached-service path) → Unavailable; started with a
// snapshot → the exact string back.
func TestGetRunningConfigHandler_LX(t *testing.T) {
	service := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	_, err := service.GetRunningConfig(context.Background(), nil)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition when not started, got %v", err)
	}

	service = &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance:      &Instance{},
	}
	_, err = service.GetRunningConfig(context.Background(), nil)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable without a captured snapshot, got %v", err)
	}

	service.instance.runningConfig = "{\n  \"log\": {}\n}\n"
	response, err := service.GetRunningConfig(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected the snapshot back, got error %v", err)
	}
	if response.Content != service.instance.runningConfig {
		t.Fatalf("snapshot mutated in transit: %q", response.Content)
	}
}
