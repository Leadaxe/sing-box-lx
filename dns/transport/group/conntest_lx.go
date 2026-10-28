package group

// lx: SPEC 139 — the `dns_member` stage of a connection test: one stage per
// member the group asks, subject = the member's tag, so the stages of its own
// dial (detour, TCP, TLS) sit under the member they belong to. A fan
// participant that ends after the winner answered lost the race (§5.13):
// SKIPPED with lost_race=<winner's member stage>. Without a trace in ctx the
// helpers cost one ctx.Value lookup.

import (
	"context"

	"github.com/sagernet/sing-box/common/lxtrace"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

func beginMemberLx(ctx context.Context, current *member) (context.Context, *lxtrace.Stage) {
	if lxtrace.From(ctx) == nil {
		return ctx, nil
	}
	ctx, stage := lxtrace.Begin(ctx, "dns_member", current.tag)
	stage.Attr("type", current.transport.Type())
	if outbound := current.transport.OutboundTag(); outbound != "" {
		stage.Attr("outbound", outbound)
	}
	return ctx, stage
}

// endMemberLx closes the stage of a member that answered, failed on its own,
// or was cut short with no winner to name: a failure on a cancelled ctx lost a
// race, a timeout stays Failed (lxtrace.Stage.EndRace).
func endMemberLx(ctx context.Context, stage *lxtrace.Stage, response *mDNS.Msg, err error) {
	if stage == nil {
		return
	}
	if !isFailure(response, err) {
		stage.End(nil)
		return
	}
	if err == nil {
		rcode := mDNS.RcodeToString[response.Rcode]
		stage.Attr("rcode", rcode)
		err = E.New(rcode)
	}
	stage.EndRace(ctx, err)
}

// endLoserLx closes the stage of a fan participant that ended after the
// request's ctx did: it lost to the winner when there is one; with none (the
// query failed or was abandoned) its own outcome stands.
func (t *Transport) endLoserLx(ctx context.Context, result fanResult, delivered bool, winner *lxtrace.Stage) {
	if delivered {
		result.stage.LostRace(winner)
		return
	}
	endMemberLx(ctx, result.stage, result.response, result.err)
}
