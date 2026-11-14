package lxsniffguard

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/sagernet/sing-box/option"
)

// Node is the guard's handle of one node. A nil *Node is a node the block
// does not cover: every method behaves as upstream.
type Node struct {
	guard *Guard
	tag   string
	info  NodeInfo

	// Guarded by guard.access.
	pending *pendingPair
	// optOut is the personal opt-out of a node whose SNI matches its server:
	// no protection on a suspect key. Never persisted.
	optOut map[labelKey]bool
	// probe is the last probe of a node whose SNI matches its server; one
	// window for both directions.
	probe time.Time
}

// pendingPair is the failure that waits for the node's next dial with a
// handshake on key, which then goes with the opposite decision.
type pendingPair struct {
	key           labelKey
	failedProtect bool
	at            time.Time
	// taken is set while one dial carries the opposite decision; other dials
	// of the node go by the label and do not touch the pair.
	taken bool
}

// Tag returns the node's tag.
func (n *Node) Tag() string {
	if n == nil {
		return ""
	}
	return n.tag
}

// Dial runs do with an Attempt in its context. When the handshake layer
// marked the attempt (Attempt.Decide with applicable=true) and the attempt
// failed fast, do runs once more with the opposite decision if the rules
// allow it; a timeout leaves a pending pair for the next dial instead. An
// attempt without the mark is neither counted nor retried.
func (n *Node) Dial(ctx context.Context, do func(ctx context.Context) error) error {
	if n == nil {
		return do(ctx)
	}
	attempt := n.newAttempt(nil)
	err := do(contextWithAttempt(ctx, attempt))
	forced := n.settle(ctx, attempt, err)
	if forced == nil {
		return err
	}
	n.guard.logger.Debug("sniff guard: ", forced.key.String(), ": node ", n.tag, " failed ", describe(!forced.protect), " (", err, "), retrying ", describe(forced.protect))
	retry := n.newAttempt(forced)
	err = do(contextWithAttempt(ctx, retry))
	n.settle(ctx, retry, err)
	return err
}

func (n *Node) newAttempt(forced *forcedDecision) *Attempt {
	return &Attempt{node: n, start: n.guard.now(), forced: forced}
}

// settle counts the outcome of a marked attempt and returns the opposite
// decision to retry with right now, if any.
func (n *Node) settle(ctx context.Context, attempt *Attempt, err error) *forcedDecision {
	g := n.guard
	g.access.Lock()
	m := attempt.mark
	attempt.closed = true
	if m == nil {
		g.access.Unlock()
		return nil
	}
	ok := err == nil
	fast := !ok && !isTimeout(err) && ctx.Err() == nil && attempt.forced == nil
	retry, notices := n.settleLocked(m, ok, fast)
	g.access.Unlock()
	g.runNotices(notices)
	if !retry {
		return nil
	}
	return &forcedDecision{key: m.key, protect: !m.protect}
}

// Begin returns an attempt outside Node.Dial, for a seam whose handshake
// runs under the client's own lifetime context, which no dial Attempt
// reaches (hysteria2 and tuic in sing-quic). Decide on it as usual; nothing
// settles it, so the seam reports the outcome through Attempt.Report, or
// calls Withdraw when the method did not apply.
func (n *Node) Begin() *Attempt {
	if n == nil {
		return nil
	}
	attempt := n.newAttempt(nil)
	attempt.late = true
	return attempt
}

// Peek returns the decision by label without a handshake mark and without
// an outcome, for a seam that decides once per socket and cannot retry
// (AmneziaWG).
func (n *Node) Peek(proto Proto, path Path) Decision {
	if n == nil {
		return Decision{}
	}
	g := n.guard
	g.access.Lock()
	defer g.access.Unlock()
	key, start, ok := g.resolve(proto, path)
	if !ok {
		return Decision{}
	}
	return g.decision(proto, n.byLabel(key, g.label(key, start)))
}

// byLabel is the decision of the node by the label of key.
func (n *Node) byLabel(key labelKey, l *label) bool {
	if l.state != option.LXSniffGuardSuspect {
		return false
	}
	return n.info.SNIMismatch || !n.optOut[key]
}

// livePending returns the node's pending pair, dropping it once expired.
func (n *Node) livePending(now time.Time) *pendingPair {
	if n.pending != nil && now.Sub(n.pending.at) >= pairLifetime {
		n.pending = nil
	}
	return n.pending
}

// settleLocked applies the outcome of a marked attempt. retry reports that
// the opposite attempt is allowed and fast was set: the caller retries now.
func (n *Node) settleLocked(m *mark, ok, fast bool) (retry bool, notices []notice) {
	g := n.guard
	if m.epoch != g.epoch && g.isLocal(m.key) {
		// The network changed under the dial: the label it read is gone.
		if m.pair != nil && n.pending == m.pair {
			n.pending = nil
		}
		return false, nil
	}
	now := g.now()
	l := g.label(m.key, m.start)
	if m.second {
		if m.pair != nil && n.pending == m.pair {
			n.pending = nil
		}
		if ok {
			notices = n.pairCompleted(m, l)
		}
		return false, notices
	}
	if pair := n.livePending(now); pair != nil && !pair.taken && !m.began.IsZero() && pair.at.Before(m.began) {
		// Any other outcome of a marked attempt closes the pair, unless the
		// dial began before the pair appeared.
		n.pending = nil
	}
	if ok {
		return false, nil
	}
	if !fast {
		if pair := n.pending; pair != nil && pair.taken {
			// One pair at a time: another dial carries the opposite decision.
			return false, nil
		}
	}
	if !n.mayProbe(m, l, now) {
		return false, nil
	}
	if fast {
		return true, nil
	}
	n.pending = &pendingPair{key: m.key, failedProtect: m.protect, at: now}
	return false, []notice{{debug: true, message: []any{"sniff guard: ", m.key.String(), ": node ", n.tag, " failed ", describe(m.protect), ", next dial goes ", describe(!m.protect)}}}
}

// mayProbe reports whether the opposite of a failed attempt is allowed, and
// takes the window when it is.
func (n *Node) mayProbe(m *mark, l *label, now time.Time) bool {
	if n.info.SNIMismatch {
		if l.state == option.LXSniffGuardSuspect && m.protect {
			// Descent probe: at most once per window per key.
			if !l.probe.IsZero() && now.Sub(l.probe) < probeWindow {
				return false
			}
			l.probe = now
		}
		return true
	}
	// A node whose SNI matches its server says nothing about the relay: it
	// probes only on a suspect key, in the direction of its personal opt-out,
	// at most once per window per node.
	if l.state != option.LXSniffGuardSuspect {
		return false
	}
	if n.optOut[m.key] == m.protect {
		return false
	}
	if !n.probe.IsZero() && now.Sub(n.probe) < probeWindow {
		return false
	}
	n.probe = now
	return true
}

// pairCompleted applies a pair: a failure with one decision, then a success
// with the opposite one (m).
func (n *Node) pairCompleted(m *mark, l *label) []notice {
	g := n.guard
	pair := "failed " + describe(!m.protect) + ", then succeeded " + describe(m.protect)
	if !n.info.SNIMismatch {
		if m.protect == !n.optOut[m.key] {
			return nil
		}
		if m.protect {
			delete(n.optOut, m.key)
			return []notice{{message: []any{"sniff guard: ", m.key.String(), ": node ", n.tag, " follows the path label again (", pair, ")"}}}
		}
		if n.optOut == nil {
			n.optOut = make(map[labelKey]bool)
		}
		n.optOut[m.key] = true
		return []notice{{message: []any{"sniff guard: ", m.key.String(), ": node ", n.tag, " opts out of protection (", pair, ")"}}}
	}
	next := option.LXSniffGuardClean
	if m.protect {
		next = option.LXSniffGuardSuspect
	}
	if l.state == next {
		return nil
	}
	previous := l.state
	l.state = next
	result := notice{message: []any{"sniff guard: ", m.key.String(), ": ", previous, " -> ", next, " (node ", n.tag, " ", pair, ")"}}
	if g.persistable(m.key) {
		result.cache = g.cache
		result.storeKey = m.key.cacheKey()
		result.store = CacheEntry{Fingerprint: g.carriers[m.key.tag].Fingerprint, State: next}
	}
	return []notice{result}
}

// notice is a side effect of an outcome (a log line, a stored transition),
// run outside the lock.
type notice struct {
	message []any
	// debug logs message at debug level instead of info.
	debug bool
	// cache is read under the lock: LoadCache may attach it concurrently.
	cache    Cache
	storeKey string
	store    CacheEntry
}

func (g *Guard) runNotices(notices []notice) {
	for _, item := range notices {
		if item.debug {
			g.logger.Debug(item.message...)
		} else {
			g.logger.Info(item.message...)
		}
		if item.storeKey == "" {
			continue
		}
		if err := item.cache.StoreSniffGuard(item.storeKey, item.store); err != nil {
			g.logger.Warn("sniff guard: store ", item.storeKey, ": ", err)
		}
	}
}

func describe(protect bool) string {
	if protect {
		return "with protection"
	}
	return "without protection"
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
