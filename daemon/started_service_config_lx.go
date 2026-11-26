//go:build with_lx_command

package daemon

import (
	"bytes"
	"context"
	stdjson "encoding/json"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// lx: SPEC 165 — the config after the lx pre-pass (ALGORITHM Н2–Н5). The box
// keeps the effective config and the changes per node in its registry; the
// original stays the snapshot of GetRunningConfig (Н1).

func (s *StartedService) effectiveConfig() (*option.LXEffectiveConfig, string, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return nil, "", status.Error(codes.FailedPrecondition, "service is not started")
	}
	instance := s.instance
	s.serviceAccess.RUnlock()
	effective := service.FromContext[*option.LXEffectiveConfig](instance.ctx)
	if effective == nil {
		return nil, "", status.Error(codes.Unavailable, "effective config was not captured for this service")
	}
	return effective, instance.runningConfig, nil
}

// GetEffectiveConfig returns the config the box was built from after the lx
// pre-pass, in the canonical form of GetRunningConfig (Н2).
func (s *StartedService) GetEffectiveConfig(ctx context.Context, empty *emptypb.Empty) (*RunningConfig, error) {
	effective, _, err := s.effectiveConfig()
	if err != nil {
		return nil, err
	}
	content, err := effective.Content()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &RunningConfig{Content: content}, nil
}

// GetOutboundConfig returns one node: original, effective and the changes
// between them (Н3–Н5).
func (s *StartedService) GetOutboundConfig(ctx context.Context, request *GetOutboundConfigRequest) (*OutboundConfig, error) {
	effective, running, err := s.effectiveConfig()
	if err != nil {
		return nil, err
	}
	content, err := effective.Content()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	result, err := outboundConfigJSON(running, content, effective.Changes, request.Tag)
	if err != nil {
		return nil, err
	}
	return &OutboundConfig{Content: result}, nil
}

// outboundConfigJSON builds the GetOutboundConfig document of tag from the
// two config documents. A hop has only effective (Н4); an unknown tag is
// NotFound (Н5).
func outboundConfigJSON(running, effective string, changesOf func(string) []option.LXConfigChange, tag string) (string, error) {
	effectiveNode, err := findConfigNode(effective, tag)
	if err != nil {
		return "", status.Error(codes.Internal, err.Error())
	}
	if effectiveNode == nil {
		return "", status.Error(codes.NotFound, "outbound not found: "+tag)
	}
	document := map[string]any{"tag": tag, "effective": effectiveNode}
	if effectiveNode["type"] != C.TypeChainHop {
		var originalNode map[string]any
		if running != "" {
			originalNode, err = findConfigNode(running, tag)
			if err != nil {
				return "", status.Error(codes.Internal, err.Error())
			}
		}
		if originalNode != nil {
			document["original"] = originalNode
		}
		changes := changesOf(tag)
		if changes == nil {
			changes = []option.LXConfigChange{}
		}
		document["changes"] = changes
	}
	var buffer bytes.Buffer
	encoder := stdjson.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return "", status.Error(codes.Internal, err.Error())
	}
	return buffer.String(), nil
}

// findConfigNode returns the outbound or endpoint tagged tag of a config
// document, nil when there is none.
func findConfigNode(content, tag string) (map[string]any, error) {
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
		Endpoints []map[string]any `json:"endpoints"`
	}
	decoder := stdjson.NewDecoder(bytes.NewReader([]byte(content)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, E.Cause(err, "decode config snapshot")
	}
	for _, list := range [][]map[string]any{document.Outbounds, document.Endpoints} {
		for _, node := range list {
			if node["tag"] == tag {
				return node, nil
			}
		}
	}
	return nil, nil
}
