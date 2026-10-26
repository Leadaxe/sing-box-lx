//go:build with_lx_command

package main

import (
	"strings"
	"testing"

	"github.com/sagernet/sing-box/daemon"
)

// lx: SPEC 139 — flag parsing and the stage tree rendering of api connection-test.

func TestConnectionTestFlags_LX(t *testing.T) {
	for flag, want := range map[string]daemon.ConnectionTestDepth{
		"": daemon.ConnectionTestDepth_DEPTH_HTTP, "http": daemon.ConnectionTestDepth_DEPTH_HTTP,
		"dns": daemon.ConnectionTestDepth_DEPTH_DNS, "Route": daemon.ConnectionTestDepth_DEPTH_ROUTE,
		"connect": daemon.ConnectionTestDepth_DEPTH_CONNECT,
	} {
		if got, err := connectionTestDepthFromFlag(flag); err != nil || got != want {
			t.Fatalf("depth %q: %v %v", flag, got, err)
		}
	}
	if _, err := connectionTestDepthFromFlag("tls"); err == nil {
		t.Fatal("unknown depth accepted")
	}
	headers, err := connectionTestHeadersFromFlag([]string{"User-Agent: curl/8", "X-Empty:"})
	if err != nil || len(headers) != 2 || headers[0].Key != "User-Agent" || headers[0].Value != "curl/8" || headers[1].Value != "" {
		t.Fatalf("headers %v, err %v", headers, err)
	}
	if _, err = connectionTestHeadersFromFlag([]string{"no-colon"}); err == nil {
		t.Fatal("header without colon accepted")
	}
}

func TestConnectionTestTree_LX(t *testing.T) {
	tree := newConnectionTestTree()
	lines := []string{
		tree.formatStage(&daemon.ConnectionTestStage{Id: 1, Kind: "dial", Subject: "vless-nl", Result: daemon.ConnectionTestStageResult_STAGE_RUNNING}),
		tree.formatStage(&daemon.ConnectionTestStage{Id: 2, Parent: 1, Kind: "lookup", Subject: "nl.example.net", StartMs: 2, DurationMs: 22, Result: daemon.ConnectionTestStageResult_STAGE_OK, Attrs: []*daemon.ConnectionTestAttr{{Key: "server", Value: "dns-remote"}}}),
		tree.formatStage(&daemon.ConnectionTestStage{Id: 3, Parent: 2, Kind: "proxy_header", StartMs: 24, Result: daemon.ConnectionTestStageResult_STAGE_NO_ACK}),
		tree.formatStage(&daemon.ConnectionTestStage{Id: 1, Kind: "dial", Subject: "vless-nl", DurationMs: 61, Result: daemon.ConnectionTestStageResult_STAGE_FAILED, Error: "refused"}),
	}
	want := []string{
		"    0  … dial vless-nl",
		"    2    ✓ lookup nl.example.net  server=dns-remote  22 ms",
		"   24      · proxy_header (no_ack)  0 ms",
		"    0  ✗ dial vless-nl  error: refused  61 ms",
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, lines[i], want[i])
		}
	}
	output := tree.formatResult(&daemon.ConnectionTestResult{
		RootCause: 1, HttpStatus: 502, ContentType: "text/html", Body: []byte("bad"), ElapsedMs: 61,
		Findings: []*daemon.ConnectionTestFinding{{Code: "failed_after_no_ack", Level: daemon.ConnectionTestFindingLevel_FINDING_PROBLEM, Stages: []uint32{3, 1}}},
	})
	for _, fragment := range []string{
		"root cause: stage 1 dial vless-nl: refused\n",
		"http: 502 text/html, 3 bytes\n",
		"  problem failed_after_no_ack stages=3,1\n",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("result output missing %q:\n%s", fragment, output)
		}
	}
}
