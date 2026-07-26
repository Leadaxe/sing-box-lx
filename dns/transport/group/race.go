package group

import (
	"context"
	"sync"
	"time"

	C "github.com/sagernet/sing-box/constant"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

// raceState is the health memory of race mode (SPEC 034). It is guarded by
// Transport.access. There are NO timers anywhere: a race is triggered lazily
// by the first query that finds the previous race older than `interval`
// (ENERGY invariant — no traffic, no probes).
type raceState struct {
	winner   string    // tag of the fastest member of the last race; "" before the first race
	ranking  []string  // arrival order of successful answers of the last race
	lastRace time.Time // zero = no race has happened yet
	running  bool      // a race is in flight; late candidates must not start another
	gen      int       // bumped by reset(); a finishing race from an older gen drops its state writes

	firstDone     chan struct{} // closed when the FIRST race settles (winner found or all failed)
	firstDoneOnce sync.Once
}

func (s *raceState) reset() {
	s.winner = ""
	s.ranking = nil
	s.lastRace = time.Time{}
	s.gen++
	// `running` is left as-is: an in-flight race still owns the flag and
	// clears it on completion; its state writes are dropped by the gen check.
}

type raceResult struct {
	tag      string
	response *mDNS.Msg
	err      error
}

// exchangeRace serves one query in race mode. The first query after the
// previous race aged past `interval` becomes the racer: it fans out to every
// member outside its down_time window and answers with the first success;
// stragglers keep running on a detached context and fill in the ranking in
// the background. Every other query goes to the current winner, falling back
// through the ranking and then the list order (failover rules).
func (t *Transport) exchangeRace(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.access.Lock()
	shouldRace := !t.race.running && (t.race.lastRace.IsZero() || time.Since(t.race.lastRace) >= t.interval)
	var racers []*member
	if shouldRace {
		racers = t.aliveMembersLocked()
		if len(racers) == 0 {
			// Everyone is down — no race to run; fall through to the
			// all-down rotation below. lastRace is NOT stamped: the next
			// query after somebody recovers should race again.
			shouldRace = false
		} else {
			t.race.running = true
			t.race.lastRace = time.Now()
		}
	}
	firstDone := t.race.firstDone
	winner := t.race.winner
	ranking := append([]string(nil), t.race.ranking...)
	gen := t.race.gen
	t.access.Unlock()

	if shouldRace {
		return t.runRace(ctx, message, racers, gen)
	}

	// Before the first race settles there is no winner yet — wait for it
	// instead of duplicating the fan-out.
	if winner == "" && firstDone != nil {
		select {
		case <-firstDone:
			t.access.Lock()
			winner = t.race.winner
			ranking = append([]string(nil), t.race.ranking...)
			t.access.Unlock()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return t.exchangeOrdered(ctx, message, t.rankedCandidates(ranking))
}

// runRace fans the query out and answers with the first success.
func (t *Transport) runRace(ctx context.Context, message *mDNS.Msg, racers []*member, gen int) (*mDNS.Msg, error) {
	results := make(chan raceResult, len(racers))
	for _, racer := range racers {
		go func(current *member) {
			// Detached from the request: the racer returns on the first
			// success, but stragglers must finish to build the ranking.
			// C.DNSTimeout bounds each attempt (the client-level default);
			// the group deliberately does not override member timeouts on
			// the failover path — this bound exists only because the
			// request context no longer applies here.
			memberCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), C.DNSTimeout)
			defer cancel()
			response, err := current.transport.Exchange(memberCtx, message.Copy())
			results <- raceResult{tag: current.tag, response: response, err: err}
		}(racer)
	}

	winnerCh := make(chan raceResult, 1)
	go t.collectRace(ctx, racers, results, winnerCh, gen)

	select {
	case result := <-winnerCh:
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// collectRace consumes every member result: the first success becomes the
// winner (delivered to the racer immediately), later successes extend the
// ranking, failures get down marks. It always runs to the last member and
// then releases the race flag.
func (t *Transport) collectRace(ctx context.Context, racers []*member, results chan raceResult, winnerCh chan raceResult, gen int) {
	var (
		errs      []error
		delivered bool
	)
	for range racers {
		result := <-results
		if isFailure(result.response, result.err) {
			t.markFailure(result.tag)
			t.logFailure(ctx, result.tag, result.response, result.err)
			if result.err != nil {
				errs = append(errs, result.err)
			} else {
				errs = append(errs, E.New(result.tag, ": SERVFAIL"))
			}
			continue
		}
		t.clearFailure(result.tag)
		t.access.Lock()
		if t.race.gen == gen {
			if t.race.winner == "" {
				t.race.winner = result.tag
			}
			t.race.ranking = append(t.race.ranking, result.tag)
		}
		t.access.Unlock()
		if !delivered {
			delivered = true
			winnerCh <- result
			t.signalFirstDone()
		}
	}
	t.access.Lock()
	t.race.running = false
	t.access.Unlock()
	if !delivered {
		winnerCh <- raceResult{err: E.Errors(errs...)}
		t.signalFirstDone()
	}
}

func (t *Transport) signalFirstDone() {
	t.race.firstDoneOnce.Do(func() {
		close(t.race.firstDone)
	})
}

// rankedCandidates orders members for a between-races query: the last race's
// ranking first (skipping down members), then the remaining alive members in
// list order. An empty result means everyone is down.
func (t *Transport) rankedCandidates(ranking []string) []*member {
	alive := t.aliveMembers()
	aliveByTag := make(map[string]*member, len(alive))
	for _, current := range alive {
		aliveByTag[current.tag] = current
	}
	candidates := make([]*member, 0, len(alive))
	for _, tag := range ranking {
		if current, isAlive := aliveByTag[tag]; isAlive {
			candidates = append(candidates, current)
			delete(aliveByTag, tag)
		}
	}
	for _, current := range alive {
		if _, remaining := aliveByTag[current.tag]; remaining {
			candidates = append(candidates, current)
		}
	}
	return candidates
}

// exchangeOrdered is the failover walk over an explicit candidate order.
func (t *Transport) exchangeOrdered(ctx context.Context, message *mDNS.Msg, candidates []*member) (*mDNS.Msg, error) {
	if len(candidates) == 0 {
		return t.exchangeLastResort(ctx, message)
	}
	var (
		lastResponse *mDNS.Msg
		lastErr      error
	)
	for _, current := range candidates {
		response, err := current.transport.Exchange(ctx, message)
		if !isFailure(response, err) {
			t.clearFailure(current.tag)
			return response, err
		}
		t.markFailure(current.tag)
		t.logFailure(ctx, current.tag, response, err)
		lastResponse, lastErr = response, err
		if ctx.Err() != nil {
			break
		}
	}
	return lastResponse, lastErr
}
