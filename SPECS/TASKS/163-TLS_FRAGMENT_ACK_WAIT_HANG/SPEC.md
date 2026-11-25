# SPEC: 163 — TLS_FRAGMENT_ACK_WAIT_HANG

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)**.

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | B (bug) — upstream: with `tls.fragment` the handshake ignores its timeout and hangs until the box is closed |
| Status | I (implemented) — darwin verified by a test, Linux not checked on a stand |
| Source | [issue #85](https://github.com/Leadaxe/sing-box-lx/issues/85); the L×Box side of the analysis — `LxBox/research/core-tlsfragment-ack-wait-ignores-timeout.md` |

## Problem

A node with `tls.fragment: true`. The server accepts the TCP connection but
does not acknowledge data: the IP is filtered after SYN, or the backend is
dead. A URL test of such a node does not return by its timeout and hangs
until the box is closed or TCP gives up on retransmissions (~15 min). Without
`fragment` the same node fails by the timeout. In L×Box the server test of a
subscription stalls on the same node every time.

## Where the defect is

The code is upstream's: `common/tlsfragment` is identical to
`upstream/stable`; `upstream/testing` differs by one unrelated line.

The core splits the ClientHello into fragments. Before writing the next one,
`writeAndWaitAck` asks the OS every 10 ms whether the bytes already written
are acknowledged (Linux — `TCP_INFO.Unacked`, darwin — `SO_NWRITE`,
Windows — ESTATS through `winiphlpapi`). It exits when everything is
acknowledged or on `ctx.Done()`.

That context is the one of the TLS config (`tf.NewConn(conn, c.ctx, …)` in
`common/tls/std_client.go` and `utls_client.go`); it lives as long as the box.
The handshake context never reaches the loop.

When the handshake context expires, crypto/tls and uTLS close the
connection. That does not help: the loop runs inside `SyscallConn().Control`
and holds a reference on the descriptor, and `Close` waits for that reference
to be released. The loop waits for an ACK, the close waits for the loop.

The path without a `*net.TCPConn` (`time.Sleep(fallbackDelay)` between
fragments) does not hang and is not affected.

## Contract

- Closing a `tf.Conn` ends a pending ACK wait before the underlying
  connection is closed. A handshake whose context expired returns within its
  timeout; `Close` does not wait for the loop.
- The ACK wait of the first write is bounded by a budget of **10 s** for the
  whole write, not per fragment. Once the budget is spent, the remaining
  fragments go out without waiting, the write completes without an error,
  and the handshake proceeds by its own deadline.
- A closed connection returns `net.ErrClosed`; a cancelled config context
  (box closed) returns its error.
- `wait_*.go` are unchanged: all three platforms already stop on the context
  they are given.
- No config keys, build tags or dependencies.

## Implementation

- `common/tlsfragment/conn_lx.go`: a `Close` method on `tf.Conn`; the
  `writeAndWaitAckLx` wrapper with a context derived from the config
  context, the close signal and the budget; telling the exit causes apart.
- `common/tlsfragment/conn.go`: the `lx` field and the call of the wrapper
  in place of `writeAndWaitAck` (`// lx: SPEC 163`).
- `Close` reaches `tf.Conn` through every wrapper: `tlsspoof.Conn` and
  `sniffGuardConn` delegate the close, REALITY builds its UConn on top of the
  same wrapper (`wrapClientConn`).

## Removal condition

Upstream starts ending the ACK wait on the handshake context or on the close
of the connection (check `common/tlsfragment/conn.go` and `wait_*.go` on each
merge). Then remove `conn_lx.go`, the two inserts and the tests; before
removing, run `conn_ackwait*_lx_test.go` against the upstream code.

## Acceptance

1. `TestTLSFragmentAckWaitHonoursHandshakeContext`: the peer accepts and never
   reads, its receive window is full; a handshake with a 1 s deadline returns
   an error in under 3 s. Red without the fix (no return within 5 s).
2. `TestAckWaitBudgetLx`: three fragments without an ACK at a 500 ms budget —
   the write ends within [budget, 2×budget) without an error.
3. Both tests under `-race`. Darwin only: on Linux a zero window leaves
   `TCP_INFO.Unacked` at 0 (unsent bytes are not counted), so the stand does
   not reproduce the defect there. lx-ci does not run them — it has no macOS
   job.
4. `go vet` of the package for linux, windows, darwin; `common/tls` builds
   with `LX_TAGS`.

## Not checked

Linux on a stand: it needs a server that drops data packets after the
handshake (`iptables -A INPUT -p tcp --dport 443 ! --syn -m length --length
53:65535 -j DROP`, check the length boundary with tcpdump) and a URL test of a
node with `fragment` and a 2 s timeout.
