//go:build with_lx_command

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
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

// lx: the attached-service path (services: [{type: "api"}]) builds an Instance with a nil
// *box.Box, so GetRules must never reach for it — it used to call instance.Router() there
// and take the whole process down with a nil deref. Started-without-a-router degrades to
// Unavailable; with a router in place the rule table comes back from the context-resolved
// field, never from the box.
func TestGetRulesHandler_LX(t *testing.T) {
	service := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	if _, err := service.GetRules(context.Background(), nil); err == nil {
		t.Fatal("expected an error when not started")
	}

	// The attached-service shape: started, no *box.Box, no router.
	service = &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance:      &Instance{ctx: context.Background()},
	}
	_, err := service.GetRules(context.Background(), nil)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable without a router, got %v", err)
	}

	service.instance.router = &stubRouterLX{rules: []adapter.Rule{stubRuleLX{}}}
	response, err := service.GetRules(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected the rule table back, got error %v", err)
	}
	if len(response.Rules) != 1 {
		t.Fatalf("expected one route rule, got %d", len(response.Rules))
	}
	rule := response.Rules[0]
	if rule.Type != "default" || rule.Payload != "stub-rule" || rule.Action != "route" || rule.IsDNS {
		t.Fatalf("rule lost its identity in transit: %+v", rule)
	}
}

// stubRouterLX serves Rules() and panics on everything else — GetRules must touch nothing
// but the rule table, and a panic here is a louder failure than a nil return.
type stubRouterLX struct {
	adapter.Router
	rules []adapter.Rule
}

func (r *stubRouterLX) Rules() []adapter.Rule { return r.rules }

type stubRuleLX struct {
	adapter.Rule
}

func (stubRuleLX) Type() string               { return "default" }
func (stubRuleLX) String() string             { return "stub-rule" }
func (stubRuleLX) Action() adapter.RuleAction { return stubRuleActionLX{} }

type stubRuleActionLX struct {
	adapter.RuleAction
}

func (stubRuleActionLX) String() string { return "route" }
