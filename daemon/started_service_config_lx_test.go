//go:build with_lx_command

package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 165 Н3–Н5 — the GetOutboundConfig document.
func TestOutboundConfigН3Н4Н5(t *testing.T) {
	running := `{"outbounds": [{"type": "vless", "tag": "n", "detour": "o-ru"}, {"type": "selector", "tag": "g", "outbounds": ["n"]}]}`
	effective := `{"outbounds": [{"type": "vless", "tag": "n", "detour": "c#0"}, {"type": "selector", "tag": "g", "outbounds": ["n"]},
		{"type": "chain-hop", "tag": "c#0", "chain": "c", "position": 0, "outbound": "o-direct"}]}`
	changes := map[string][]option.LXConfigChange{"n": {{Field: "detour", From: "o-ru", To: "c#0", By: option.LXConfigChangeBy{Type: "chain", Chain: "c", Position: 1}}}}
	changesOf := func(tag string) []option.LXConfigChange { return changes[tag] }

	decode := func(content string) map[string]any {
		var document map[string]any
		if err := json.Unmarshal([]byte(content), &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	content, err := outboundConfigJSON(running, effective, changesOf, "n")
	if err != nil {
		t.Fatal(err)
	}
	node := decode(content)
	if node["original"].(map[string]any)["detour"] != "o-ru" || node["effective"].(map[string]any)["detour"] != "c#0" {
		t.Fatalf("Н3: %s", content)
	}
	if !strings.Contains(content, `"by": {`) || !strings.Contains(content, `"type": "chain"`) {
		t.Fatalf("Н3: changes %s", content)
	}
	content, _ = outboundConfigJSON(running, effective, changesOf, "g")
	if changes := decode(content)["changes"].([]any); len(changes) != 0 {
		t.Fatal("a group without changes has an empty list")
	}
	content, _ = outboundConfigJSON(running, effective, changesOf, "c#0")
	hop := decode(content)
	if _, has := hop["original"]; has {
		t.Fatal("Н4: a hop has no original")
	}
	if _, has := hop["changes"]; has {
		t.Fatal("Н4: a hop has no changes")
	}
	if hop["effective"].(map[string]any)["outbound"] != "o-direct" {
		t.Fatalf("Т9: %s", content)
	}
	if _, err := outboundConfigJSON(running, effective, changesOf, "nope"); status.Code(err) != codes.NotFound {
		t.Fatalf("Н5: %v", err)
	}
}
