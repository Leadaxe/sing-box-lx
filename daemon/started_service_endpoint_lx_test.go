//go:build with_lx_command

package daemon

import (
	"context"
	"os"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 106 — SetEndpointEnabled is a bridge to adapter.EndpointToggle:
// status codes and the state in the response.

type toggleEndpoint struct {
	listedEndpoint
	enabled bool
	err     error
}

func (e *toggleEndpoint) Enabled() bool { return e.enabled }

func (e *toggleEndpoint) SetEnabled(enabled bool) error {
	if e.err != nil {
		return e.err
	}
	e.enabled = enabled
	if enabled {
		e.state.State = adapter.EndpointStateUp
	} else {
		e.state.State = adapter.EndpointStateDisabled
	}
	return nil
}

type gettingEndpointManager struct {
	listingEndpointManager
}

func (m *gettingEndpointManager) Get(tag string) (adapter.Endpoint, bool) {
	for _, endpoint := range m.endpoints {
		if endpoint.Tag() == tag {
			return endpoint, true
		}
	}
	return nil, false
}

func newToggleService(endpoints ...adapter.Endpoint) *StartedService {
	return &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			endpointManager:       &gettingEndpointManager{listingEndpointManager{endpoints: endpoints}},
		},
	}
}

func TestSetEndpointEnabled_LX(t *testing.T) {
	wg := &toggleEndpoint{listedEndpoint: listedEndpoint{tag: "wg", state: adapter.IdleState{State: adapter.EndpointStateUp}}, enabled: true}
	plain := &listedEndpoint{tag: "tailscale"}
	service := newToggleService(wg, plain)

	response, err := service.SetEndpointEnabled(context.Background(), &SetEndpointEnabledRequest{Tag: "wg", Enabled: false})
	if err != nil || wg.enabled || response.State != adapter.EndpointStateDisabled {
		t.Fatalf("disable: response=%v err=%v enabled=%v", response, err, wg.enabled)
	}
	response, err = service.SetEndpointEnabled(context.Background(), &SetEndpointEnabledRequest{Tag: "wg", Enabled: true})
	if err != nil || !wg.enabled || response.State != adapter.EndpointStateUp {
		t.Fatalf("enable: response=%v err=%v", response, err)
	}

	cases := []struct {
		name string
		tag  string
		err  error
		code codes.Code
	}{
		{"unknown tag", "missing", nil, codes.NotFound},
		{"not a toggle", "tailscale", nil, codes.InvalidArgument},
		{"closing", "wg", os.ErrClosed, codes.FailedPrecondition},
		{"wake failed", "wg", E.New("bind: operation not permitted"), codes.Unavailable},
	}
	for _, tc := range cases {
		wg.err = tc.err
		_, err := service.SetEndpointEnabled(context.Background(), &SetEndpointEnabledRequest{Tag: tc.tag, Enabled: true})
		if status.Code(err) != tc.code {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.code)
		}
	}

	stopped := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	if _, err := stopped.SetEndpointEnabled(context.Background(), &SetEndpointEnabledRequest{Tag: "wg"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("not started: got %v", err)
	}
}
