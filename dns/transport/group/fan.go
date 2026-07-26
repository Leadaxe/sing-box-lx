package group

import (
	"context"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/dnstrack"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

type fanResult struct {
	member   *member
	response *mDNS.Msg
	err      error
	rtt      time.Duration
}

// fan is the single fan-out primitive serving three callers: the rescue fan
// (after the target failed), the fastest election and every parallel query.
// All participants run under the REQUEST context — no detach: a "late"
// answer is one arriving after the first success but before the deadline.
// The first success answers the query; the winner gets a WIN record
// (fastest only — no other mode reads wins) and becomes the sticky target
// (except parallel). Failures write error records ONLY while the request
// budget is alive: with a dead parent context the fan's instant failures
// say nothing about the servers, and recording them would poison the whole
// group because of one blackholed target.
func (t *Transport) fan(ctx context.Context, message *mDNS.Msg, participants []*member, gen int, election bool) (*mDNS.Msg, error) {
	dnstrack.MarkFanned(ctx)
	if t.logger != nil {
		tags := make([]string, 0, len(participants))
		for _, participant := range participants {
			tags = append(tags, participant.tag)
		}
		t.logger.DebugContext(ctx, "group[", t.Tag(), "]: fan to [", strings.Join(tags, " "), "]")
	}
	results := make(chan fanResult, len(participants))
	for _, participant := range participants {
		go func(current *member) {
			response, err, rtt := t.timedExchange(ctx, current, message.Copy())
			results <- fanResult{member: current, response: response, err: err, rtt: rtt}
		}(participant)
	}
	winnerCh := make(chan fanResult, 1)
	go t.collectFan(ctx, len(participants), results, winnerCh, gen, election)

	select {
	case result := <-winnerCh:
		if result.member != nil {
			t.recordEffective(ctx, result.member)
		}
		if result.response == nil && result.err == nil {
			// Degenerate guard: a fan must never yield (nil, nil) — the
			// client dereferences the response on a nil error.
			if cause := ctx.Err(); cause != nil {
				return nil, cause
			}
			return nil, E.New("group[", t.Tag(), "]: fan yielded no result")
		}
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// collectFan consumes every participant result. It always runs to the last
// participant (their exchanges terminate at the request deadline at the
// latest) and then releases the election flag, so a fan interrupted by the
// caller cannot leak the single-flight lock.
func (t *Transport) collectFan(ctx context.Context, count int, results chan fanResult, winnerCh chan fanResult, gen int, election bool) {
	var (
		errs      []error
		delivered bool
	)
	for i := 0; i < count; i++ {
		result := <-results
		if isFailure(result.response, result.err) {
			// Guard: a failure observed after the request context ended (the
			// answer was already delivered and the client cancelled, or the
			// budget died with a blackholed target) is an artifact of the
			// dead context, not evidence about the server — it goes neither
			// into the health records nor into the trace.
			if ctx.Err() != nil {
				continue
			}
			t.traceAttempt(ctx, result.member, result.response, result.err, result.rtt)
			t.noteError(result.member.tag, gen)
			t.logProbeFailure(ctx, result.member.tag, result.response, result.err)
			if result.err != nil {
				errs = append(errs, E.Cause(result.err, result.member.tag))
			} else {
				errs = append(errs, E.New(result.member.tag, ": SERVFAIL"))
			}
			continue
		}
		t.traceAttempt(ctx, result.member, result.response, result.err, result.rtt)
		// A success always erases the server's live errors — late answers
		// included (their answer is discarded, their health signal is not).
		t.noteSuccess(result.member.tag, result.rtt, gen)
		// A success observed after the request context ended belongs to a
		// FAILED query: it heals the server but must not mint a win, must
		// not re-elect the current and must not pretend to answer.
		if ctx.Err() != nil {
			continue
		}
		if !delivered {
			delivered = true
			if t.mode == ModeFastest {
				t.noteWin(result.member.tag, gen)
			}
			if t.mode != ModeParallel {
				if previous, changed := t.setCurrent(result.member.tag, gen); changed {
					t.logCurrentChange(ctx, previous, result.member.tag)
				}
			}
			t.logger.DebugContext(ctx, "group[", t.Tag(), "]: fan winner: ", result.member.tag)
			winnerCh <- result
		}
	}
	if election {
		t.access.Lock()
		t.election = false
		t.access.Unlock()
	}
	if !delivered {
		failure := E.Errors(errs...)
		if failure == nil {
			// Every failure was abandoned (guard above): the fan died with
			// the request context. E.Errors() of nothing is nil — never let
			// a fan resolve to (nil, nil).
			if cause := ctx.Err(); cause != nil {
				failure = E.Cause(cause, "group[", t.Tag(), "]: all fan probes abandoned")
			} else {
				failure = E.New("group[", t.Tag(), "]: fan yielded no result")
			}
		}
		winnerCh <- fanResult{err: failure}
	}
}
