package lxsniffguard

import (
	"context"
	"time"
)

// Decision is the form of one handshake.
//
// The zero Decision means the block does not act on this handshake (feature
// off, `never` or unknown path, method not applicable): the handshake layer
// leaves the node's own keys alone. Otherwise Protect says whether the
// section's Method (with Delay for the `delay` method) is applied; with
// Override set the layer drops the node's own fragment / record_fragment and
// shapes the ClientHello by Protect alone, including no split on a clean
// path.
type Decision struct {
	Protect  bool
	Method   string
	Delay    time.Duration
	Override bool
}

// Attempt is one call of the node's dial function inside Node.Dial. A nil
// *Attempt means the feature is off for this dial.
type Attempt struct {
	node  *Node
	start time.Time
	// forced is the opposite decision of an in-dial retry.
	forced *forcedDecision
	// Guarded by node.guard.access.
	mark *mark
	late bool
	// closed is set once the attempt can no longer carry a mark: Node.Dial
	// settled it, or Withdraw dropped its mark. A later Decide answers by the
	// label without a mark and without the pending pair.
	closed bool
}

type forcedDecision struct {
	key     labelKey
	protect bool
}

// mark is the handshake mark: the handshake happened on key with this
// decision applied.
type mark struct {
	key      labelKey
	start    string
	epoch    uint64
	began    time.Time
	protect  bool
	decision Decision
	// second marks the opposite half of a pair: an in-dial retry or a dial
	// that took the node's pending pair.
	second bool
	// pair is the pending pair this attempt took.
	pair *pendingPair
}

type attemptKey struct{}

func contextWithAttempt(ctx context.Context, attempt *Attempt) context.Context {
	return context.WithValue(ctx, attemptKey{}, attempt)
}

// AttemptFromContext returns the attempt of the dial ctx belongs to, or nil:
// then the handshake layer does not intervene.
func AttemptFromContext(ctx context.Context) *Attempt {
	attempt, _ := ctx.Value(attemptKey{}).(*Attempt)
	return attempt
}

// Override reports lx.sniff_guard.override, for a handshake layer that must
// know it before calling Decide (a node with its own fragment keys is not
// applicable without override).
func (a *Attempt) Override() bool {
	return a != nil && a.node.guard.policy.Override
}

// Decide is called by the layer that performs the handshake, once per
// handshake, and returns the form to use. applicable=false means the
// section's method does not apply to this node or the node made its own
// choice under override=false: the zero Decision is returned and the outcome
// of the dial is not counted.
//
// Calling Decide again for the same key returns the same decision. Only an
// attempt that called Decide with applicable=true has its outcome counted
// and may be retried; a dial that reused a ready session leaves no mark.
func (a *Attempt) Decide(proto Proto, path Path, applicable bool) Decision {
	if a == nil {
		return Decision{}
	}
	node := a.node
	g := node.guard
	g.access.Lock()
	defer g.access.Unlock()
	key, start, ok := g.resolve(proto, path)
	if !ok || !applicable {
		return Decision{}
	}
	if a.closed {
		// The handshake started after the attempt was settled (a lazy
		// handshake, early data) or after its mark was withdrawn: it goes by
		// the label and is not counted.
		return g.decision(proto, node.byLabel(key, g.label(key, start)))
	}
	if a.mark != nil {
		if a.mark.key == key {
			return a.mark.decision
		}
		if a.mark.pair != nil {
			a.mark.pair.taken = false
		}
	}
	now := g.now()
	m := &mark{key: key, start: start, epoch: g.epoch, began: a.start}
	m.protect = node.byLabel(key, g.label(key, start))
	if a.forced != nil && a.forced.key == key {
		m.protect = a.forced.protect
		m.second = true
	} else if pair := node.livePending(now); pair != nil && pair.key == key && !pair.taken {
		pair.taken = true
		m.protect = !pair.failedProtect
		m.second = true
		m.pair = pair
	}
	m.decision = g.decision(proto, m.protect)
	a.mark = m
	return m.decision
}

// Late hands the outcome of this attempt over to the seam that learns it
// later (hysteria2, tuic: the 30-second window). Node.Dial then neither
// counts the dial's error nor retries; the seam reports through the returned
// node's Report with the decision Decide gave.
func (a *Attempt) Late() *Node {
	if a == nil {
		return nil
	}
	a.node.guard.access.Lock()
	a.late = true
	a.node.guard.access.Unlock()
	return a.node
}

// Withdraw drops the handshake mark: the decided method turned out to do
// nothing on this handshake (quic.delay on a ClientHello that fits one
// Initial), so the method does not apply to this node. The outcome of the
// attempt is then neither counted nor retried, and a pending pair the
// attempt took is left for the next dial.
func (a *Attempt) Withdraw() {
	if a == nil {
		return
	}
	g := a.node.guard
	g.access.Lock()
	defer g.access.Unlock()
	if a.mark != nil && a.mark.pair != nil {
		a.mark.pair.taken = false
	}
	a.mark = nil
	a.closed = true
}

// Method returns the method of proto's section whether or not this
// handshake is protected, "" when the section is off: a seam whose method
// may turn out not to apply (quic.delay) watches the handshake either way.
func (a *Attempt) Method(proto Proto) string {
	if a == nil || (proto != ProtoTLS && proto != ProtoQUIC) {
		return ""
	}
	return a.node.guard.section(proto).Method
}

// ContextWithAttempt carries attempt into ctx, for a layer whose handshake
// runs under a context of its own (an HTTP request built from a transport
// context) but belongs to the dial that carries attempt. A nil attempt
// leaves ctx as it is.
func ContextWithAttempt(ctx context.Context, attempt *Attempt) context.Context {
	if attempt == nil {
		return ctx
	}
	return contextWithAttempt(ctx, attempt)
}

// WithoutAttempt hides the attempt of the node that started the dial from a
// dial nested in it: the handshake of a relay the node goes through is not
// the node's own handshake.
func WithoutAttempt(ctx context.Context) context.Context {
	if AttemptFromContext(ctx) == nil {
		return ctx
	}
	return contextWithAttempt(ctx, nil)
}
