//go:build with_lx_command

package daemon

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	E "github.com/sagernet/sing/common/exceptions"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 164 — the impair commands are a bridge to the outbound's control
// methods: lookup by tag, status codes, the status JSON in every answer.

type controlledImpair struct {
	listedOutbound
	entry, entries, start, repeat string
	err                           error
}

func (o *controlledImpair) ImpairSet(entry []byte) error {
	o.entry = string(entry)
	return o.err
}

func (o *controlledImpair) ImpairSchedule(entries []byte, start string, repeat string) error {
	o.entries, o.start, o.repeat = string(entries), start, repeat
	return o.err
}

func (o *controlledImpair) ImpairStatus() ([]byte, error) {
	return []byte(`{"tag":"` + o.tag + `"}`), nil
}

type taggedOutboundManager struct {
	listingOutboundManager
}

func (m *taggedOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	for _, outbound := range m.outbounds {
		if outbound.Tag() == tag {
			return outbound, true
		}
	}
	return nil, false
}

func newImpairService(outbounds ...adapter.Outbound) *StartedService {
	return &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager:       &taggedOutboundManager{listingOutboundManager{outbounds: outbounds}},
		},
	}
}

func TestImpairCommands_LX(t *testing.T) {
	impair := &controlledImpair{listedOutbound: listedOutbound{tag: "slow"}}
	service := newImpairService(impair, &listedOutbound{tag: "proxy"})
	ctx := context.Background()

	response, err := service.SetImpair(ctx, &SetImpairRequest{OutboundTag: "slow", Entry: `{"outage":true}`})
	if err != nil || impair.entry != `{"outage":true}` || response.Json != `{"tag":"slow"}` {
		t.Fatalf("set: %v %v %q", response, err, impair.entry)
	}
	response, err = service.ScheduleImpair(ctx, &ScheduleImpairRequest{OutboundTag: "slow", Entries: `[]`, Start: "first_use", Repeat: "60s"})
	if err != nil || impair.entries != `[]` || impair.start != "first_use" || impair.repeat != "60s" || response.Json != `{"tag":"slow"}` {
		t.Fatalf("schedule: %v %v", response, err)
	}
	if response, err = service.GetImpairStatus(ctx, &ImpairStatusRequest{OutboundTag: "slow"}); err != nil || response.Json != `{"tag":"slow"}` {
		t.Fatalf("status: %v %v", response, err)
	}

	impair.err = E.New("bad entry")
	cases := []struct {
		name string
		call func() error
		code codes.Code
	}{
		{"unknown tag", func() error {
			_, err := service.GetImpairStatus(ctx, &ImpairStatusRequest{OutboundTag: "nope"})
			return err
		}, codes.NotFound},
		{"not impair", func() error {
			_, err := service.SetImpair(ctx, &SetImpairRequest{OutboundTag: "proxy", Entry: "{}"})
			return err
		}, codes.InvalidArgument},
		{"rejected entry", func() error {
			_, err := service.SetImpair(ctx, &SetImpairRequest{OutboundTag: "slow", Entry: "{}"})
			return err
		}, codes.InvalidArgument},
		{"rejected schedule", func() error {
			_, err := service.ScheduleImpair(ctx, &ScheduleImpairRequest{OutboundTag: "slow", Entries: "[]"})
			return err
		}, codes.InvalidArgument},
	}
	for _, c := range cases {
		if code := status.Code(c.call()); code != c.code {
			t.Errorf("%s: code %v, want %v", c.name, code, c.code)
		}
	}

	stopped := newImpairService(impair)
	stopped.serviceStatus.Status = ServiceStatus_IDLE
	if _, err = stopped.GetImpairStatus(ctx, &ImpairStatusRequest{OutboundTag: "slow"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("not started: %v", err)
	}
}
