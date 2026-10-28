//go:build with_lx_command

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	C "github.com/sagernet/sing-box/constant"
)

// lx: SPEC 139 release 2 — reuse=uncontrolled on the dial of a node whose
// external module keeps its own QUIC, mux or session pool: only under fresh,
// for the pooled types and for an outbound with multiplex enabled.

func runReuseTestLx(t *testing.T, nodeType string, runningConfig string, fresh bool) *ConnectionTestStage {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	service := newTypedProbeService(&probeDialer{}, nodeType)
	service.instance.runningConfig = runningConfig
	stream := newRecordingStream(context.Background())
	if err := service.StartConnectionTest(&ConnectionTestRequest{Link: server.URL, OutboundTag: "node", Fresh: fresh}, stream); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if result := stream.result(); result == nil || result.HttpStatus != http.StatusNoContent {
		t.Fatalf("result = %v, want 204", result)
	}
	dial := findStage(stream.finalStages(), stageDial)
	if dial == nil {
		t.Fatal("no dial stage")
	}
	return dial
}

func TestStartConnectionTest_ReuseUncontrolled_LX(t *testing.T) {
	const muxConfig = `{"outbounds":[{"type":"vless","tag":"node","multiplex":{"enabled":true}}]}`
	const noMuxConfig = `{"outbounds":[{"type":"vless","tag":"node","multiplex":{"enabled":false}}]}`
	const grpcConfig = `{"outbounds":[{"type":"vless","tag":"node","transport":{"type":"grpc","service_name":"x"}}]}`
	const wsConfig = `{"outbounds":[{"type":"vless","tag":"node","transport":{"type":"ws","path":"/x"}}]}`
	cases := []struct {
		name          string
		nodeType      string
		runningConfig string
		fresh         bool
		want          string
	}{
		{"hysteria2 fresh", C.TypeHysteria2, "", true, "uncontrolled"},
		{"hysteria2 not fresh", C.TypeHysteria2, "", false, ""},
		{"tuic fresh", C.TypeTUIC, "", true, "uncontrolled"},
		{"naive fresh", C.TypeNaive, "", true, "uncontrolled"},
		{"vless mux fresh", C.TypeVLESS, muxConfig, true, "uncontrolled"},
		{"vless mux not fresh", C.TypeVLESS, muxConfig, false, ""},
		{"vless without mux fresh", C.TypeVLESS, noMuxConfig, true, ""},
		{"vless no config fresh", C.TypeVLESS, "", true, ""},
		{"vless grpc fresh", C.TypeVLESS, grpcConfig, true, "uncontrolled"},
		{"vless grpc not fresh", C.TypeVLESS, grpcConfig, false, ""},
		{"vless ws fresh", C.TypeVLESS, wsConfig, true, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dial := runReuseTestLx(t, testCase.nodeType, testCase.runningConfig, testCase.fresh)
			if got := attrOf(dial, "reuse"); got != testCase.want {
				t.Fatalf("dial attrs = %v, want reuse=%q", dial.Attrs, testCase.want)
			}
		})
	}
}
