package libbox

import (
	"context"

	"github.com/sagernet/sing-box/daemon"
	E "github.com/sagernet/sing/common/exceptions"
)

// SPEC 164 — runtime control of an `impair` test outbound, for the test
// hooks of a client (LxBox Debug API): the StartedService commands
// SetImpair, ScheduleImpair and GetImpairStatus. Requests and the status
// are JSON in the config's own schema (docs-lx/lx-impair.md, "Runtime
// control"). Without with_lx_impair in the core there is no impair outbound
// and the calls fail with InvalidArgument ("not an impair outbound").

// ImpairStatus is the state of an impair outbound after a call: the JSON of
// GetImpairStatus in Content. An object, not a bare string: a string
// return crosses gomobile as a packed frame that kills the process on
// Android (SPEC 037).
type ImpairStatus struct {
	Content string
}

// SetImpair applies a change to an impair outbound now, on top of its
// running schedule: entry is a schedule entry without "at", e.g.
// {"link_params": {"outage": true, "reset": true}} or {"sublinks": false}.
func (c *CommandClient) SetImpair(outboundTag string, entry string) (*ImpairStatus, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*ImpairStatus, error) {
		response, err := client.SetImpair(ctx, &daemon.SetImpairRequest{OutboundTag: outboundTag, Entry: entry})
		if err != nil {
			return nil, E.Cause(err, "set impair")
		}
		return &ImpairStatus{Content: response.Json}, nil
	})
}

// ScheduleImpair replaces the schedule of an impair outbound: the line and
// the sublinks go back to the config, then entries (a JSON "schedule" list;
// "[]" only restores the config) run from now (start "" or "now") or from
// the next traffic ("first_use"), starting over every repeat ("60s"; ""
// runs once).
func (c *CommandClient) ScheduleImpair(outboundTag string, entries string, start string, repeat string) (*ImpairStatus, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*ImpairStatus, error) {
		response, err := client.ScheduleImpair(ctx, &daemon.ScheduleImpairRequest{
			OutboundTag: outboundTag, Entries: entries, Start: start, Repeat: repeat,
		})
		if err != nil {
			return nil, E.Cause(err, "schedule impair")
		}
		return &ImpairStatus{Content: response.Json}, nil
	})
}

// GetImpairStatus reads the state of an impair outbound: its line and
// sublinks with their template, state and counters, and where the schedule
// is.
func (c *CommandClient) GetImpairStatus(outboundTag string) (*ImpairStatus, error) {
	return callWithResult(c, func(ctx context.Context, client daemon.StartedServiceClient) (*ImpairStatus, error) {
		response, err := client.GetImpairStatus(ctx, &daemon.ImpairStatusRequest{OutboundTag: outboundTag})
		if err != nil {
			return nil, E.Cause(err, "get impair status")
		}
		return &ImpairStatus{Content: response.Json}, nil
	})
}
