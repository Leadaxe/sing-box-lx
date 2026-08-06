//go:build with_gvisor

package wireguard

// lx:begin SPEC 052 netstack connect deadline

import (
	"context"

	C "github.com/sagernet/sing-box/constant"
)

// connectContextLx bounds the CONNECT phase of a TCP dial through the endpoint's
// gVisor netstack (SPEC 052).
//
// Why it exists: C.TCPConnectTimeout (5s) lives exclusively in the system-socket
// net.Dialer, and no layer above (route, groups, detour, tun inbound) puts a
// deadline on the ctx of a user-traffic dial — so a netstack dial's only bound
// is gVisor's SYN retransmit machinery, which gives up after ~127s (six
// retransmits, 1+2+4+8+16+32+64). On any silent blackhole (Wi-Fi dropping UDP
// to the peer, a dead node) every dial through the tunnel hangs those ~127s
// with no error to react to. Inherited upstream shape, not an lx regression.
//
// Why C.TCPTimeout (15s) and not the 5s connect timeout: the group health-check
// probes dial with C.TCPTimeout; a USER deadline below the PROBE budget opens a
// gap where a slow-but-alive node keeps passing probes while every user dial
// through it times out — a permanent outage where today is "slow but works".
// Sharing the constant keeps the two in lockstep. The measured legitimate
// ceiling is well under it: a cold level-3 wake (teardown -> rebuild + fresh
// handshake + SYN) reaches ~4.8s at RTT≈0, ~8-10s in the field with one lost
// handshake initiation (RekeyTimeout retry is 5s) — a 5s cap would cut healthy
// cold dials, 15s clears them with margin.
//
// Why HERE and not higher up: this is the only seam where the deadline is
// naturally connect-scoped. The caller holds cancel via defer, so the deadline
// dies with the connect and can never outlive it into the returned conn — a
// deadline attached any higher (group, route, detour) survives into the
// connection and kills long-lived streams (the SPEC 050 failure class: XHTTP
// binds its stream-one request to the dial ctx). Per-address budgeting in
// DialSerial chains falls out for free: every address's connect gets its own
// fresh budget, so a blackholed first address cannot starve the fallback.
//
// UDP is deliberately untouched: a netstack UDP "dial" is local-only (nothing
// blocks on the network), deadness there is only visible to the protocol above
// (QUIC/DNS carry their own timers), and with no connect phase any deadline
// would bound the session itself — harming healthy long-lived flows.
//
// An earlier parent deadline still wins: context.WithTimeout never extends one.
// On expiry the dial returns context.DeadlineExceeded (distinct from a caller's
// context.Canceled), so a future failure-reaction layer can tell them apart.
func connectContextLx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, C.TCPTimeout)
}

// lx:end SPEC 052
