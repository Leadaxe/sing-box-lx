//go:build with_lx_command

package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
)

// lx: SPECS/TASKS/139-CONNECTION_TEST — regression tests for the review
// findings on the handler: a timeout through a node is FAILED/timeout on the
// dial (not a misfiled lost_race), proxy_header is synthesized at DEPTH_HTTP
// only, and attributes synthesized on an ended stage reach the stream.

// typedProbeOutbound is the probe node with a protocol type of its own, so
// the handler's per-type synthesis (proxy_header, class) can be exercised.
type typedProbeOutbound struct {
	*probeOutbound
	typ string
}

func (o *typedProbeOutbound) Type() string { return o.typ }

func newTypedProbeService(dialer *probeDialer, typ string) *StartedService {
	return &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager: &probeOutboundManager{byTag: map[string]adapter.Outbound{
				"node": &typedProbeOutbound{probeOutbound: &probeOutbound{dialer: dialer, tag: "node"}, typ: typ},
			}},
			endpointManager: &probeEndpointManager{byTag: map[string]adapter.Endpoint{}},
		},
	}
}

func stageWithParent(stages []*ConnectionTestStage, kind string, parent uint32) *ConnectionTestStage {
	for _, stage := range stages {
		if stage.Kind == kind && stage.Parent == parent {
			return stage
		}
	}
	return nil
}

// A node whose dial never returns: the overall timeout ends the dial stage
// FAILED with the deadline error, it is the root cause, and nothing is
// classified as a lost race (§5.11).
func TestStartConnectionTest_ViaNode_Timeout_LX(t *testing.T) {
	dialer := &probeDialer{block: make(chan struct{})}
	service := newProbeService(dialer)
	stream := newRecordingStream(context.Background())
	err := service.StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid/", OutboundTag: "node", Timeout: 300}, stream)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	result := stream.result()
	if result == nil || result.Error != "" {
		t.Fatalf("result = %v, want a result without call error", result)
	}
	stages := stream.finalStages()
	dial := findStage(stages, stageDial)
	if dial == nil {
		t.Fatalf("no dial stage in %v", kindsOf(stages))
	}
	// Either the dial's own ctx error or Finish's "timeout" (§5.11) may win
	// the race to close the stage; both are a FAILED timeout.
	if dial.Result != ConnectionTestStageResult_STAGE_FAILED || !(strings.Contains(dial.Error, "deadline") || dial.Error == "timeout") {
		t.Fatalf("dial = %v, want FAILED with a timeout cause", dial)
	}
	if attrOf(dial, "lost_race") != "" {
		t.Fatalf("a timed-out dial must not be filed as a lost race: %v", dial)
	}
	if result.RootCause != dial.Id {
		t.Fatalf("rootCause = %d, want the dial %d", result.RootCause, dial.Id)
	}
	for _, stage := range stages {
		if stage.Kind != stageDial && stage.Result != ConnectionTestStageResult_STAGE_SKIPPED {
			t.Fatalf("stage %s = %v, want SKIPPED after the timed-out dial", stage.Kind, stage.Result)
		}
	}
}

// proxy_header: synthesized under the dial as NO_ACK at DEPTH_HTTP, absent at
// DEPTH_CONNECT where no payload (and so no header) is ever sent (§7).
func TestStartConnectionTest_ProxyHeaderDepthGate_LX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Run("DEPTH_HTTP", func(t *testing.T) {
		service := newTypedProbeService(&probeDialer{}, C.TypeVLESS)
		stream := newRecordingStream(context.Background())
		if err := service.StartConnectionTest(&ConnectionTestRequest{Link: server.URL, OutboundTag: "node"}, stream); err != nil {
			t.Fatalf("transport error: %v", err)
		}
		result := stream.result()
		if result == nil || result.HttpStatus != http.StatusNoContent || result.RootCause != 0 {
			t.Fatalf("result = %v, want 204 and no root cause", result)
		}
		stages := stream.finalStages()
		dial := findStage(stages, stageDial)
		header := stageWithParent(stages, stageProxyHeader, dial.Id)
		if header == nil {
			t.Fatalf("no proxy_header under dial %d in %v", dial.Id, kindsOf(stages))
		}
		if header.Result != ConnectionTestStageResult_STAGE_NO_ACK || attrOf(header, "protocol") != C.TypeVLESS {
			t.Fatalf("proxy_header = %v, want NO_ACK protocol=vless", header)
		}
	})

	t.Run("DEPTH_CONNECT", func(t *testing.T) {
		service := newTypedProbeService(&probeDialer{}, C.TypeVLESS)
		stream := newRecordingStream(context.Background())
		if err := service.StartConnectionTest(&ConnectionTestRequest{Link: server.URL, OutboundTag: "node", Depth: ConnectionTestDepth_DEPTH_CONNECT}, stream); err != nil {
			t.Fatalf("transport error: %v", err)
		}
		result := stream.result()
		if result == nil || result.Error != "" || result.RootCause != 0 || result.HttpStatus != 0 {
			t.Fatalf("result = %v, want a clean result without HTTP fields", result)
		}
		kinds := strings.Join(kindsOf(stream.finalStages()), ",")
		if kinds != "dial" {
			t.Fatalf("stages = %s, want only dial at DEPTH_CONNECT", kinds)
		}
	})
}

// The class of a failed hysteria2/tuic/naive dial is synthesized after the
// stage ended; it must still be on the snapshot the client receives.
func TestStartConnectionTest_ClassAttrReachesStream_LX(t *testing.T) {
	service := newTypedProbeService(&probeDialer{fail: errors.New("authentication failed, status code: 401")}, C.TypeHysteria2)
	stream := newRecordingStream(context.Background())
	if err := service.StartConnectionTest(&ConnectionTestRequest{Link: "http://example.invalid/", OutboundTag: "node"}, stream); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	dial := findStage(stream.finalStages(), stageDial)
	if dial == nil || dial.Result != ConnectionTestStageResult_STAGE_FAILED {
		t.Fatalf("dial = %v, want FAILED", dial)
	}
	if attrOf(dial, "class") != "auth" {
		t.Fatalf("dial attrs = %v, want class=auth on the streamed snapshot", dial.Attrs)
	}
	if result := stream.result(); result == nil || result.RootCause != dial.Id {
		t.Fatalf("result = %v, want rootCause = dial %d", result, dial.Id)
	}
}
