package lxdatagram

// lx: SPEC 151 — a decoy datagram ahead of the QUIC Initial, on the retry.
//
// A proxy relay that sniffs UDP to rewrite the destination by the SNI it finds
// (Xray `sniffing` with `destOverride` and no `routeOnly`) decides on the
// first datagram of a flow: Xray drops its QUIC sniffer from the flow once the
// leading bytes are not a QUIC long-header packet. One datagram that no
// sniffer recognises, sent before the ClientHello, leaves the destination as
// the client dialled it. The server discards it unparsed: a QUIC packet must
// have the fixed bit (0x40) set (RFC 9000 §17.2, §17.3), and a single byte is
// too short to draw a stateless reset.
//
// The client cannot see whether a relay sniffs, and the byte itself breaks
// some paths that have no sniffer (field report 2026-11-06). So the byte is
// not sent on the first attempt: a dial goes clean, and only a dial that
// failed is retried with the byte in front. The outcome is remembered per
// consumer and per path (a kernel socket, or a relay of some type), see
// retryMemory. Which consumers, through which relay types and on which paths
// is lx.quic_sniff_decoy; absent, nothing is ever sent.

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

var sniffDecoy = []byte{0x00}

// SniffDecoyFromContext returns the resolved lx.quic_sniff_decoy for
// outbounds built in ctx; all-off when the box has no `lx` block.
func SniffDecoyFromContext(ctx context.Context) option.LXQUICSniffDecoyResolved {
	return service.PtrFromContext[option.LXResolved](ctx).QUICSniffDecoyOrOff()
}

// SniffDecoy returns the decoy datagram, for a sender that assembles its own
// burst (the AmneziaWG handshake, transport/wireguard).
func SniffDecoy() []byte {
	return append([]byte(nil), sniffDecoy...)
}

// WriteSniffDecoy sends the decoy datagram on a freshly dialled QUIC conn,
// before the QUIC stack writes its first Initial. A failed write is logged and
// otherwise ignored: the decoy is advisory and the handshake reports any real
// fault of the conn itself.
func WriteSniffDecoy(conn net.Conn, logger logger.Logger) {
	_, err := conn.Write(sniffDecoy)
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debug("sniff decoy datagram not sent: ", err)
	} else {
		logger.Trace("sniff decoy datagram sent ahead of the QUIC Initial")
	}
}

// Path is where a dial went, as classified from the conn as dialled: a
// kernel socket (Kernel), or a conn carried by another outbound of type
// Carrier ("" when no seam recorded one).
type Path struct {
	Kernel  bool
	Carrier string
}

// key is the memory slot of the path: the direct path, or the relay type.
func (p Path) key() string {
	if p.Kernel {
		return "direct"
	}
	return "detour:" + p.Carrier
}

// ClassifyPath reads the path of conn: a kernel socket, or the carrier the
// dial context recorded (adapter.CarrierRecord).
func ClassifyPath(conn any, record *adapter.CarrierRecord) Path {
	path := Path{Kernel: kernelUnder(conn)}
	if !path.Kernel && record != nil {
		path.Carrier = record.Type
	}
	return path
}

// PolicyApplies reports whether policy allows the decoy on path: "direct" a
// kernel socket, "detour" a carried conn whose relay type is in detour_type
// (an unrecorded carrier never matches), "all" both, "off" neither.
func PolicyApplies(policy option.LXQUICSniffDecoyResolved, path Path) bool {
	switch policy.Mode {
	case option.LXQUICSniffDecoyAll:
		return path.Kernel || policy.DetourMatches(path.Carrier)
	case option.LXQUICSniffDecoyDirect:
		return path.Kernel
	case option.LXQUICSniffDecoyDetour:
		return !path.Kernel && policy.DetourMatches(path.Carrier)
	}
	return false
}

// Decoy is one consumer's sniff decoy: the policy for its outbound type and
// the retry memory per path. A nil *Decoy is the disabled consumer: Begin
// returns an attempt that never writes the byte and never asks for a redo.
type Decoy struct {
	policy option.LXQUICSniffDecoyResolved
	logger logger.Logger
	memory retryMemory
}

// NewSniffDecoy returns the decoy controller for an outbound of outboundType
// built in ctx, or nil when lx.quic_sniff_decoy does not cover that type.
func NewSniffDecoy(ctx context.Context, outboundType string, logger logger.Logger) *Decoy {
	policy := SniffDecoyFromContext(ctx)
	if !policy.Enabled(outboundType) {
		return nil
	}
	return &Decoy{policy: policy, logger: logger}
}

// Enabled reports whether the consumer sends the byte at all.
func (d *Decoy) Enabled() bool {
	return d != nil
}

// Attempt is one dial: Begin → DialContext with Context → Dialed → the QUIC
// handshake → Done.
type Attempt struct {
	decoy  *Decoy
	record *adapter.CarrierRecord
	// Context carries the carrier record; the consumer dials with it.
	Context context.Context
	path    Path
	applies bool
	// withDecoy records whether this attempt carried the byte.
	withDecoy bool
}

// Begin opens an attempt. The returned context must be the one the UDP dial
// uses, so the carrier seams can record the relay.
func (d *Decoy) Begin(ctx context.Context) *Attempt {
	attempt := &Attempt{decoy: d, Context: ctx}
	if d == nil {
		return attempt
	}
	attempt.Context, attempt.record = adapter.ContextWithCarrierRecord(ctx)
	return attempt
}

// Dialed classifies the freshly dialled conn (as dialled, before the
// consumer's wrappers), decides whether this attempt carries the byte and
// writes it on writeTo — the conn the QUIC stack will write to, after the
// consumer's wrappers. Returns whether the byte was written.
func (a *Attempt) Dialed(conn net.Conn, writeTo net.Conn) bool {
	if a.decoy == nil {
		return false
	}
	a.path = ClassifyPath(conn, a.record)
	a.applies = PolicyApplies(a.decoy.policy, a.path)
	if !a.applies {
		return false
	}
	if a.decoy.policy.Retry {
		a.withDecoy = a.decoy.memory.withDecoy(a.path.key())
	} else {
		a.withDecoy = true
	}
	if a.withDecoy {
		WriteSniffDecoy(writeTo, a.decoy.logger)
	}
	return a.withDecoy
}

// Path returns the path Dialed classified.
func (a *Attempt) Path() Path {
	return a.path
}

// WithDecoy reports whether the attempt carried the byte.
func (a *Attempt) WithDecoy() bool {
	return a.withDecoy
}

// Done records the outcome of the handshake and reports whether the consumer
// should redo the dial right now: the attempt was clean, the policy applies
// to its path, the failure was not a timeout and the caller's context is
// still alive. A silent relay runs out the deadline instead; the next dial
// then carries the byte from the memory.
func (a *Attempt) Done(err error) (redoNow bool) {
	if a.decoy == nil || !a.applies {
		return false
	}
	if !a.decoy.policy.Retry {
		return false
	}
	ok := err == nil
	a.decoy.memory.report(a.path.key(), a.withDecoy, ok)
	if ok || a.withDecoy {
		return false
	}
	if isTimeout(err) || a.Context.Err() != nil {
		return false
	}
	if a.decoy.logger != nil {
		a.decoy.logger.Debug("dial failed without the sniff decoy (", err, "); retrying with it")
	}
	return true
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// retryMemory is the per-path state of the retry rule:
//
//	clean (initial): no byte; success keeps clean, failure arms.
//	armed:           byte;    success pins decoy, failure returns to clean.
//	decoy:           byte;    success stays,      failure returns to clean.
//
// A failure with the byte returns to clean so that a node that was merely
// down does not pin the byte on a path where the byte itself breaks the flow.
type retryMemory struct {
	mu    sync.Mutex
	state map[string]retryState
}

type retryState uint8

const (
	retryClean retryState = iota
	retryArmed
	retryDecoy
)

// withDecoy reports whether the next attempt on path carries the byte.
func (m *retryMemory) withDecoy(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[path] != retryClean
}

// report records the outcome of an attempt on path.
func (m *retryMemory) report(path string, withDecoy, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		m.state = make(map[string]retryState)
	}
	switch {
	case !withDecoy && !ok:
		m.state[path] = retryArmed
	case withDecoy && ok:
		m.state[path] = retryDecoy
	default:
		// clean+ok stays clean; decoy+fail (armed or pinned) returns to clean
		delete(m.state, path)
	}
}

// State returns the memory state of path for tests and status: "clean",
// "armed" or "decoy".
func (d *Decoy) State(path Path) string {
	if d == nil {
		return "clean"
	}
	d.memory.mu.Lock()
	defer d.memory.mu.Unlock()
	switch d.memory.state[path.key()] {
	case retryArmed:
		return "armed"
	case retryDecoy:
		return "decoy"
	}
	return "clean"
}
