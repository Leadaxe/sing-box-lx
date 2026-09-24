//go:build with_lx_command

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
)

// lx: SPEC 097 — GetOutbounds reports the WG endpoint state and leaves it
// empty for every other outbound.

type listedOutbound struct {
	adapter.Outbound
	tag string
}

func (o *listedOutbound) Tag() string  { return o.tag }
func (o *listedOutbound) Type() string { return "vless" }

type listedEndpoint struct {
	adapter.Endpoint
	tag   string
	state adapter.IdleState
}

func (e *listedEndpoint) Tag() string                  { return e.tag }
func (e *listedEndpoint) Type() string                 { return "wireguard" }
func (e *listedEndpoint) IdleState() adapter.IdleState { return e.state }

type listingOutboundManager struct {
	adapter.OutboundManager
	outbounds []adapter.Outbound
}

func (m *listingOutboundManager) Outbounds() []adapter.Outbound { return m.outbounds }

func (m *listingOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	for _, outbound := range m.outbounds {
		if outbound.Tag() == tag {
			return outbound, true
		}
	}
	return nil, false
}

type listingEndpointManager struct {
	adapter.EndpointManager
	endpoints []adapter.Endpoint
}

func (m *listingEndpointManager) Endpoints() []adapter.Endpoint { return m.endpoints }

func TestGetOutbounds_endpointState_LX(t *testing.T) {
	service := &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager: &listingOutboundManager{outbounds: []adapter.Outbound{
				&listedOutbound{tag: "proxy"},
			}},
			endpointManager: &listingEndpointManager{endpoints: []adapter.Endpoint{
				&listedEndpoint{tag: "wg-cold", state: adapter.IdleState{State: adapter.EndpointStateNeverBuilt, IdleSince: 90 * time.Second}},
				&listedEndpoint{tag: "wg-up", state: adapter.IdleState{State: adapter.EndpointStateUp}},
			}},
		},
	}
	list, err := service.GetOutbounds(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byTag := make(map[string]*GroupItem)
	for _, item := range list.Outbounds {
		byTag[item.Tag] = item
	}
	if item := byTag["proxy"]; item == nil || item.EndpointState != "" || item.IdleSinceSeconds != 0 {
		t.Fatalf("a plain outbound carries no endpoint state: %+v", item)
	}
	if item := byTag["wg-cold"]; item == nil || item.EndpointState != "never_built" || item.IdleSinceSeconds != 90 {
		t.Fatalf("never-built endpoint: %+v", item)
	}
	if item := byTag["wg-up"]; item == nil || item.EndpointState != "up" || item.IdleSinceSeconds != 0 {
		t.Fatalf("up endpoint: %+v", item)
	}
}
