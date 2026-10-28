package wireguard

// lx: SPEC 139 — connection-test trace helpers for DialContext and
// ListenPacketWithDestination. A domain handed to this endpoint is resolved
// by the DNS rules (`lookup` with resolver dns_rules), and the connection
// inside the tunnel is opened by netstack, not DefaultDialer, so the socket
// stage is recorded here. Without a trace in ctx each helper costs one
// ctx.Value lookup and records nothing.
//
// Release 2 adds the endpoint's own steps: `wake` when a dial wakes an
// idle-suspended endpoint (SPEC 020), and `wg_handshake` next to every TCP
// dial inside the tunnel, read from the peers' last-handshake time before and
// after the dial (SPEC 114). Neither one changes what the endpoint does: the
// test wakes an endpoint as any dial would (SPEC 139 §5.10) and never forces
// a handshake (§5.6).

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lookupLx is w.dnsRouter.Lookup inside a `lookup` stage with the
// resolve-cycle check. The stage ends before the dial; the caller dials in
// its own ctx.
func (w *Endpoint) lookupLx(ctx context.Context, domain string) ([]netip.Addr, error) {
	lookupCtx, stage, err := lxtrace.BeginLookup(ctx, "dns_rules", domain)
	if err != nil {
		return nil, err
	}
	addresses, err := w.dnsRouter.Lookup(lookupCtx, domain, adapter.DNSQueryOptions{})
	if stage != nil && len(addresses) > 0 {
		stage.Attr("addresses", strings.Join(common.Map(addresses, netip.Addr.String), ","))
	}
	stage.End(err)
	return addresses, err
}

// netstackLx returns the netstack dialer, wrapped to record one
// `tcp_connect` / `udp` stage per inner dial when ctx carries a trace.
func (w *Endpoint) netstackLx(ctx context.Context) N.Dialer {
	if lxtrace.From(ctx) == nil {
		return w.endpoint
	}
	return &netstackTraceDialerLx{Dialer: w.endpoint, endpoint: w}
}

type netstackTraceDialerLx struct {
	N.Dialer
	endpoint *Endpoint
}

func (d *netstackTraceDialerLx) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	kind := "tcp_connect"
	if N.NetworkName(network) == N.NetworkUDP {
		kind = "udp"
	}
	var handshake *handshakeTraceLx
	if kind == "tcp_connect" {
		// A TCP dial sends a SYN at once, so it is what triggers a handshake
		// on a peer without a live session; a UDP socket sends nothing yet.
		handshake = d.endpoint.beginHandshakeLx(ctx)
	}
	_, stage := lxtrace.Begin(ctx, kind, destination.String())
	stage.Attr("inside", d.endpoint.Tag())
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	// The handshake ends first: when the tunnel never came up, it is the
	// cause and the dial inside it only the consequence (§5.2).
	handshake.end(err)
	stage.End(err)
	return conn, err
}

func (d *netstackTraceDialerLx) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	_, stage := lxtrace.Begin(ctx, "udp", destination.String())
	stage.Attr("inside", d.endpoint.Tag())
	conn, err := d.Dialer.ListenPacket(ctx, destination)
	stage.End(err)
	return conn, err
}

// wakeLx opens a `wake` stage for the wake resumeOnDial is about to run and
// returns the func that closes it; resumeOnDial defers it under resumeMu, so
// the outcome it reads is the one the wake left. The stage carries how long
// the endpoint slept (seconds) and whether it resumed a suspended device or
// rebuilt a torn-down one. Waking is what any dial does (SPEC 139 §5.10), so
// a probe connection records it like any other. ctx is nil on the L3-forward
// path; without a trace the returned func is a shared no-op.
func (w *Endpoint) wakeLx(ctx context.Context) func() {
	if ctx == nil {
		return noopLx
	}
	_, stage := lxtrace.Begin(ctx, "wake", w.Tag())
	if stage == nil {
		return noopLx
	}
	stage.Attr("slept", strconv.FormatInt(int64(w.SleepSince()/time.Second), 10))
	if w.torndown.Load() {
		stage.Attr("mode", "rebuild")
	} else {
		stage.Attr("mode", "resume")
	}
	return func() {
		if w.idleAsleep.Load() || !w.started.Load() {
			stage.End(errWakeFailedLx)
			return
		}
		stage.End(nil)
	}
}

func noopLx() {}

var errWakeFailedLx = E.New("endpoint did not wake")

// rejectAfterTimeLx is WireGuard's RejectAfterTime: a session older than this
// cannot carry data, so a dial through it waits for a new handshake.
const rejectAfterTimeLx = 180 * time.Second

// handshakeTraceLx is a running `wg_handshake` stage and the peers' latest
// handshake time when it began.
type handshakeTraceLx struct {
	statuses func() []adapter.PeerStatus
	stage    *lxtrace.Stage
	before   time.Time
}

// beginHandshakeLx opens a `wg_handshake` stage before a dial inside the
// tunnel. It reads the peers' last handshake (one UAPI dump, only under a
// trace) and never triggers one: the dial does that, as in production.
func (w *Endpoint) beginHandshakeLx(ctx context.Context) *handshakeTraceLx {
	_, stage := lxtrace.Begin(ctx, "wg_handshake", w.Tag())
	if stage == nil {
		return nil
	}
	// The outer UDP runs over the endpoint's detour, set up at start on the
	// endpoint's own context, so it is never a stage of the test (SPEC 139
	// §7): naming it tells a silent peer from a dead detour to test next.
	if dependencies := w.Dependencies(); len(dependencies) > 0 {
		stage.Attr("detour", dependencies[0])
	}
	return newHandshakeTraceLx(stage, w.PeerStatuses)
}

func newHandshakeTraceLx(stage *lxtrace.Stage, statuses func() []adapter.PeerStatus) *handshakeTraceLx {
	before, _ := latestHandshakeLx(statuses())
	return &handshakeTraceLx{statuses: statuses, stage: stage, before: before}
}

// end closes the stage from the handshake time after the dial:
//   - it moved: a new handshake, OK, ending when the handshake completed;
//   - it did not, but a session existed: REUSED with the age of the last
//     handshake — the test does not restart a tunnel (SPEC 139 §5.6), and a
//     failed dial over a live session is not the handshake's failure;
//   - no session and the dial failed: FAILED, the peer never answered;
//   - no session and the dial succeeded: the status could not be read (a
//     build in progress), NOT_EVALUATED.
func (h *handshakeTraceLx) end(dialErr error) {
	if h == nil {
		return
	}
	after, peer := latestHandshakeLx(h.statuses())
	if after.After(h.before) {
		h.stage.Attr("handshake", "new")
		if peer != "" {
			h.stage.Attr("peer", peer)
		}
		h.stage.EndAt(after, nil)
		return
	}
	if !h.before.IsZero() {
		age := time.Since(h.before)
		if dialErr == nil || age < rejectAfterTimeLx {
			h.stage.Attr("handshake", "existing")
			h.stage.Attr("age", strconv.FormatInt(int64(age/time.Second), 10))
			h.stage.Mark(lxtrace.Reused)
			return
		}
	}
	if dialErr != nil {
		h.stage.End(errNoHandshakeLx)
		return
	}
	h.stage.Attr("handshake", "unknown")
	h.stage.Mark(lxtrace.NotEvaluated)
}

var errNoHandshakeLx = E.New("no handshake with the peer")

// latestHandshakeLx returns the most recent handshake among the peers and the
// public key of that peer; zero when no peer has completed one.
func latestHandshakeLx(statuses []adapter.PeerStatus) (time.Time, string) {
	var (
		latest time.Time
		peer   string
	)
	for _, status := range statuses {
		if status.LastHandshake.After(latest) {
			latest, peer = status.LastHandshake, status.PublicKey
		}
	}
	return latest, peer
}
