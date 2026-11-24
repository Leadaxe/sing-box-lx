# SPEC: 160 — WG_DIRECT_SEND_COST

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-11-23).

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | B (bugfix on top of upstream) + one setting `lx.wg.connected_socket` |
| Status | I (implemented) — ships in `v1.14.4-lx.36`. Fork: [PR #7](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/7), `012c2e9`; the submodule is bumped, the key is wired into the core. Verified by the fork's tests on linux/arm64 and by a measurement of the finished build on NanoPi. Not verified: a phone with a network change, a real network card, download on a live link |
| Branch | `lx`; fork `Leadaxe/wireguard-go-awg2-lx`, branch `lx-awg2-v007` |
| Build tag | — (applies to any build) |
| Source | Measurement in [158](../158-WG_QUEUED_PACKET_BUFFERS/SPEC.md) §6.1: upload over a tunnel without `detour` is about 22 % slower (120 vs 154 Mbit/s) and about 29 % costlier in CPU per byte than over the same tunnel through `detour` → `direct`. Analysis of 2026-11-23, raw data — `.work/spec158/send-cost-findings.md` and `.work/spec158/sendcost/` |
| Related | [158](../158-WG_QUEUED_PACKET_BUFFERS/SPEC.md) (WireGuard buffers; its second part, the buffer settings, changes the same read line), [098](../098-LX_ROOT_CONFIG_BLOCK/SPEC.md) (layout of `lx.wg`), [041](../041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) (self-recovery with a new socket) |

## 0. In plain words

A WireGuard tunnel without `detour` sends through a socket layer designed for batches of 128
datagrams. The tunnel stack hands packets over one at a time, and for every datagram the layer
does two extra jobs:

1. **It cleans up all 128 prepared messages**, although only one is filled.
2. **It sends through an unconnected socket** — with an address and a control message on every
   datagram, from which the kernel looks up the route again.

Through `detour` → `direct` neither job happens, which is why sending there is faster and
cheaper.

| Cause | Share of the gap | What we do |
|---|---|---|
| Cleanup of 128 messages | about two thirds | Fix unconditionally, for everyone |
| Unconnected socket | the rest | Connect the socket to the server address, behind a switch; off by default until checked on a phone |

## 1. Measurement

NanoPi (4×Cortex-A55, Linux 6.1), two core instances over loopback, 8 TCP streams, 20 s,
upload. Median of three rounds, min–max in brackets. The switches are measurement-only, on the
client only.

| Variant | Mbit/s | Client CPU, s/GiB | Server CPU, s/GiB |
|---|---:|---:|---:|
| without `detour`, as now | 120.0 (119.4–122.6) | 157.9 (156.0–164.9) | 107.5 |
| through `detour` → `direct` | 153.8 (143.9–155.1) | 122.6 (112.6–132.8) | 95.1 |
| clean up only filled messages | 140.2 (135.8–144.6) | 127.2 (122.8–140.4) | 99.1 |
| the same + connected socket | 150.6 (143.4–150.7) | 124.2 (115.7–126.8) | 97.0 |
| the same + connected socket, one write at a time | 148.2 (145.2–155.0) | 125.7 (120.3–133.0) | 93.9 |
| clean up only filled + batch of 1 | 138.2 (135.7–138.7) | 135.2 (133.6–135.9) | 99.6 |

- The first fix: +17 % speed and −20 % client CPU per byte.
- The second on top of the first: about +7 % more; the gap with `detour` is closed within the
  resolution of the stand.
- The batch size is not the cause (last row): with a batch of 1 the speed is the same, and the
  client CPU per byte is even 6 % higher.
- In the client profile, message cleanup took 6.6–7.4 % of all CPU time, on the server 6–7 %;
  after the fix — under 1 %. The share of system time is the same in all variants (22–28 %): the
  extra is spent in Go, not in the kernel.
- There was no UDP loss in any run.

A separate measurement with the switch on both instances and a read datagram count of 8
(158 §9.2; three rounds): the cleanup fix gives download 97.7 → 120.3 Mbit/s (+23 %) and upload
122.0 → 141.9 (+16 %), CPU per byte on both sides is 13–20 % lower. Download gains here because
with eight buffers the cleanup on receive becomes short as well.

Download with the fix at the default value (128 with kernel coalescing on receive) was not
measured with the measurement build; it is measured on the finished build, §1.1.

Measured in one round, not three: the connected socket without the first fix (126.0 Mbit/s
against 124.3 for the baseline — no effect is visible while the CPU is busy with cleanup) and
download (current build 89.5 Mbit/s, with the connected socket alone, without the first fix,
93.6 — within the spread).

### 1.1 Finished build

The same stand, but now the implementation rather than measurement switches: the core with fork
`d18503c` (the PR branch; the same tree as the merged `012c2e9`), controlled by the `lx` block in the config, the same on client and server. "Before" is
the release candidate without these fixes. Three rounds, median; raw data —
`.work/spec158/pr7-final-build.md`.

| Variant | Download, Mbit/s | Upload, Mbit/s | CPU client + server, s/GiB, download / upload |
|---|---:|---:|---:|
| before the fixes | 103.1 | 127.0 | 299 / 255 |
| no keys — only the cleanup applies | 118.9 | 140.0 | 267 / 235 |
| `connected_socket` | 116.2 | 146.4 | 276 / 228 |
| `connected_socket` + `read_datagram_count: 8` | 115.8 | 149.8 | 271 / 220 |

- **Cleanup, no settings:** download +15 %, upload +10 %; client CPU per byte lower by 13 % and
  8 %.
- **Connected socket:** on upload +5 % over the build without keys (the round spreads overlap),
  together with a datagram count of 8 — +7 %, the best upload and the lowest CPU use. No gain on
  download.
- The measurement build showed a larger gain from the cleanup (§1) because there "before" and
  "after" were compared with a datagram count of 8.

Functional checks of the same build on the board:

- the client socket with the key is connected to the server, without the key it is not; the
  server socket (it has `listen_port`) is not connected;
- a server restart under traffic for 20 s: traffic comes back after 5 s both with and without
  the key. While the server was down, the client socket was disconnected on an ICMP error and
  after the restart connected again on the same port; the logs show no receive stop;
- AmneziaWG (16 datagrams in the handshake) with both keys: the tunnel comes up, data flows;
- a client with `listen_port` and the key: works, the socket is not connected;
- no UDP loss, panics or hangs across all runs.

## 2. First cause: cleanup of all prepared messages

The socket layer keeps a set of 128 prepared messages for the system call. Sending fills as many
of them as there are datagrams (usually one), and when the set is returned all 128 are zeroed:
buffer references, control data, address. That is a write of about 11 KiB of structures per
datagram; on Cortex-A55 it costs more than encrypting the same packet.

The code is designed for a TUN with batched reads, where the batch is full. In the core the
tunnel stack is gVisor, which hands over one packet at a time, and the batch almost always holds
one.

The same happens on receive: for every read all 128 messages are prepared and cleaned up, while
on average 1.7–1.9 datagrams arrive.

### 2.1 Fix

Everyone who uses the set fills it from the start: sending fills the first few messages,
receive one per buffer passed. So the cleanup goes from the start and stops at the first message
that is still the way the previous cleanup left it.

- **Send:** the filled messages are cleaned up, usually one.
- **Receive without kernel coalescing:** reading goes only into the messages that were given a
  buffer — as many as buffers were passed; the same ones are cleaned up.
- **Receive with kernel coalescing (UDP GRO):** unchanged. All 128 messages get a buffer: the
  kernel reads into the tail of the set, parsing spreads the segments across its start. A lighter
  cleanup for this path is possible but not measured; it is not part of this task.

The logic of the layer does not change; the amount of cleanup does. Applies on all platforms
where a tunnel without `detour` goes through this layer: Linux, Android, macOS, Windows (there the
batch is 1, and all 128 were still cleaned up).

The layer file is upstream: the insertion is one condition in the cleanup loop and a slice in
the read call, the rest lives in a separate lx file.

**A send-retry defect is fixed along the way.** When the system refused send coalescing (UDP
GSO), the layer retried the batch without coalescing, but the first message kept the list of
coalesced segments from the failed attempt. The first datagram went out with a tail made of its
neighbours and was dropped on the other side. It happens once per socket, on a network card
without hardware checksum offload, and only when more than one datagram goes out per call — for
example, an AmneziaWG handshake.

The defect is upstream's: it appeared in `sagernet/wireguard-go` with commit `fcbb7c4` ("Coalesce
UDP GSO segments as iovecs", 2026-07-06), when coalescing went from copying to a list of buffers
and the retry branch was not adapted. It was found by reading the code and was not reproduced on
hardware. It has its own record: [issue #81](https://github.com/Leadaxe/sing-box-lx/issues/81).

### 2.2 Removal condition

Upstream (`sagernet/wireguard-go`) stops zeroing the whole set on return — or the socket layer is
replaced by another one.

## 3. Second cause: unconnected socket

The socket of a tunnel without `detour` is not connected to the server address. Every datagram
goes out with a destination address and, on Linux, with a control message carrying the source
address; for each one the kernel looks up the route again and parses the control message. With
`detour` → `direct` the socket is connected, and the kernel uses the route remembered at connect
time.

(The route lookup part comes from how the Linux kernel works; there is no kernel profiler on the
board, and the measurement confirms only the combined effect.)

### 3.1 What already exists

The core turns on the layer's "single-peer mode" when the tunnel has no `listen_port`, there is
one peer and its address is known at start. On macOS the layer in this mode connects the socket
to the peer address: only this way does batched sending work there. On Linux and Android the mode
currently does nothing.

### 3.2 Fix

On Linux and Android the layer in single-peer mode may connect the socket to the server address,
behind a switch (§4). Connecting is an optimisation nothing relies on: at any doubt the socket is
disconnected, and the datagram goes out the way it always did.

- **Connect.** On the first send the socket of the matching family (IPv4 or IPv6) is connected
  to the peer address. After that datagrams go out without an address and without the
  source-address control message; send coalescing (UDP GSO) stays.
- **A send error that may be caused by the connection** (the source address or the route is
  gone, an ICMP error arrived for an earlier datagram): the socket is disconnected, the batch
  goes out the usual way right away, the next connect is no sooner than one second later.
  Datagrams that went out before the error go out twice; the receiver drops the duplicates.
- **ICMP error on read.** A connected socket gets the ICMP error for an earlier datagram on the
  next read. The device would take it for a dead socket and stop receiving for good. The layer
  does not pass such an error on but disconnects the socket: an unreachable server then does not
  produce a series of them.
- **Path reset.** Where the device resets the remembered source address — a handshake went
  unanswered, the routes changed — it now tells the layer as well. The socket is disconnected,
  accepts from any address for three seconds and then connects again, with a fresh source
  address and route.
- **A second destination address** on a connected socket: the layer serves more than one peer.
  The socket is disconnected and works as before until it is reopened.
- **Port.** Disconnecting a socket whose port was chosen by the system gives the port back: the
  socket gets another one on the next send, and replies to the old one are lost. So a layer that
  is allowed to connect chooses the port itself from the same range and binds the sockets to it
  by number. If the sockets ended up on a different port (IPv4 unavailable), the layer does not
  connect.
- A reopened socket is connected again on the first send.
- A tunnel with `listen_port`, with several peers, and a tunnel through `detour` are not
  affected.

### 3.3 What changes in behaviour

- **Receive only from the server address.** A connected socket does not accept datagrams from
  other addresses. Now, if the server replies from a new address, the client accepts the reply
  at once; with a connected socket — after the next handshake retry: the device resets the path,
  and for three seconds the socket listens to everyone. On macOS the socket in this mode is
  always connected and does not hear a change of the server address.
- **The source address is fixed at connect time.** Now it is chosen per datagram, and a network
  change (Wi-Fi ↔ LTE) goes through without the layer taking part. With a connected socket it is
  updated after a network change in one of two ways: at once, if the system returned an error on
  send, or on a path reset, when a handshake went unanswered. The second way is the same one by
  which the remembered source address is already updated on Linux today. How long this takes on
  a phone is not measured.
- **The client port** is chosen by the sing-box core in advance, not by the system at bind time.
  The range is the same.
- **A handshake to several candidate addresses** gives a second destination address right away,
  and the connection is silently not used for such a tunnel.

Because of the first two points the fix goes behind a switch and is off by default.

## 4. Setting

```json
"lx": {
  "wg": {
    "connected_socket": true
  }
}
```

| Key | Type | Default | Meaning |
|---|---|---|---|
| `lx.wg.connected_socket` | bool | unset = `false` | Connect the tunnel socket to the server address in single-peer mode. Linux and Android |

- Global: applies to all WireGuard and AmneziaWG tunnels of the config for which the core has
  turned on single-peer mode.
- On macOS the key is accepted and changes nothing: there the socket is always connected in this
  mode. On Windows it is accepted and changes nothing.
- Works in any build: only the `lx.wg.idle` block requires the mobile build (098 §2.1).
- `false` is normalised to an absent key.
- With the key on, every connect and disconnect goes to the endpoint's **debug** log with its
  reason, prefixed `connected socket:` — `ipv4 connected to <server>`; `ipv4 disconnected from
  <server>:` then `path reset (handshake unanswered or route change), listening to any address
  for 3s`, `send: <error>, retry in 1s`, `read: <error>, retry in 1s` or `a second destination
  <address>, unconnected until the bind is reopened`; `ipv4 connect to <server>: <error>`; and
  `off: the sockets are bound to port N, not the chosen M` when the port could not be pinned.
  A server that does not answer handshakes gives a disconnect line every few seconds, hence the
  debug level.

## 5. Decisions and rationale

**D1. One task for both causes.** They were found in one analysis, sit in one layer and together
close one gap; the measurements are shared. Split into two tasks, they would lose the table that
shows the contribution of each.

**D2. Cleanup is fixed without a switch.** The behaviour of the layer does not change: the same
messages go out through the same calls. There is nothing for the user to choose.

**D3. The connected socket is behind a switch, off by default.** The fix changes socket behaviour
(§3.3) and is not verified on a phone. The switch allows shipping the code, checking it on a
device through the config and only then deciding on the default. Owner decision 2026-11-23:
separately and with a flag.

**D4. The key does not touch macOS.** There a connected socket is the condition for batched
sending (about 2.7 times cheaper than addressed sending, by the measurement quoted in the comment in the
fork's `conn/msgx_darwin.go`) and works in released builds. A key with the
default "off" that also applied on macOS would roll this back.

**D5. The name `connected_socket`, a flat key.** The name is the owner's. "Connected socket" is a
network programming term and names a state of the socket; the `connect_peer` that was considered
read as "connect to the server". There is no separate `socket` group: it would hold a single key,
and the build condition does not depend on the group (098 §2.1). When a second socket key is
needed, that task will create the group.

**D6. Receive with kernel coalescing stays as it is.** There the whole set counts as filled, and
there is no short way to clean up less. A `read_datagram_count` value below 128 (158 §9) turns
off receive coalescing, and then receive goes through the short path of this fix.

**D7. Order relative to the second part of 158.** The receive fix and the datagram count setting
change the same read line. This task goes first, the setting is laid on top.

**D8. At doubt — disconnect, not ask for a new socket.** On macOS the layer, on such an error,
asks the device to reopen the socket. Here a fallback to the usual path is chosen: the port does
not change, the batch on which the error happened is not lost, the receive routine is not
restarted. A socket without a connection behaves the same as today, so the fallback carries no
risk; the connection comes back by itself, no more often than once per second.

**D9. A network change is caught by the same signal as the remembered source address.** The
device already resets the remembered source address when a handshake goes unanswered or the
routes change: a stale address is the first cause to rule out. For a connected socket the address
is held by the system kernel, and a reset on the device side does not reach it. So in the same
two places the device tells the layer as well. The layer sets up no separate network watching.

**D10. After a path reset — three seconds without a connection.** If it reconnected at once, a
server reply from a new address would never be heard: the socket would be connected to the old
one again before the reply arrives. Three seconds cover the handshake reply delay and are shorter
than its retry interval (five seconds).

**D11. The ICMP error on read is suppressed, and the socket is disconnected.** Suppressing
without disconnecting cannot be bounded sensibly: an unreachable server produces such errors in
series, and any limit on the number in a row would leave receive stopped (an independent review
found this). After disconnecting, ICMP errors are not delivered to the socket at all, and the
series ends by itself.

**D12. The port is bound by number.** Otherwise the very first disconnect would change the client
port and lose the replies to the previous one — a test on the board showed this. The port is
picked with a probe socket from the same range; the race for it is closed by retrying the open.

**D13. Cleanup up to the first untouched message, not by a counter.** A counter of filled
messages required insertions in every send branch and changed the signature of an upstream
function. A condition in the cleanup loop is one insertion, and it cannot diverge from what was
actually filled.

## 6. Verification

Done:

- **Fork tests** (`conn/lx_messages_test.go`, `conn/lx_connect_linux_test.go`, device tests in
  `device/lx_packet_sized_test.go`), the full `conn` and `device` suites on linux/arm64 (NanoPi),
  `conn` and the new device tests on darwin/arm64; `go vet` for linux and android, builds for
  windows and freebsd:
  - the set comes back clean however it was filled: send, send over a connected socket, receive,
    receive with no datagrams;
  - a send retry after coalescing was refused carries no foreign segments;
  - connect on the first send, a coalesced batch, the server reply; the remembered source
    address does not interfere;
  - a second destination address disconnects the socket until it is reopened, both addresses
    are served;
  - path reset: the socket is disconnected, the port is the same, it hears anyone, after the
    window it connects again;
  - the server changed its address: after a path reset its reply is heard, the socket connects to
    the new address and does not count it as a second one;
  - 60 sends in a row to a closed port: ICMP errors do not reach the device, after the server
    returns traffic flows;
  - a tunnel between two devices on connected sockets, WireGuard and AmneziaWG;
  - the device tells the layer about a path reset once per source-address reset.
- **Independent review** of the three commits. It found three defects of the connected socket: a
  receive stop after a series of ICMP errors, an exclusive lock on every send after the
  connection was given up, and an unheard server reply from a new address. All three were fixed
  before the merge and are covered by tests.
- **Measurement and functional checks of the finished build** on NanoPi — §1.1.

Not done:

- **phone:** Wi-Fi ↔ LTE switch under traffic and while idle — time to recovery with and without
  the key; start and operation of the tunnel in the app. Until then the key is off by default;
- loss of the local address on a live interface (in the tests — only the parsing of error codes);
- a tunnel with `auto_detect_interface` (per-interface sockets send before the connected one);
- the socket after self-recovery (041) and after wake-up (020) as part of the core;
- a node whose server is given by a domain name: whether single-peer mode turns on;
- download and upload on a real network card;
- for Windows the build is checked, on macOS the socket layer tests pass; no live tunnel with
  the new cleanup was run on either.

## 7. Boundaries

- Everything is measured on loopback. Cleanup is CPU spent in Go and does not depend on the
  network card; the gain of the connected socket on a real card may differ.
- The most expensive spot of the client in any mode is ACK processing in the gVisor inner TCP:
  25–37 % of CPU. Not part of this task; it was noticed that the freer the CPU, the less often
  the server sends ACKs (0.136 per packet now, 0.092 with both fixes).
- A tunnel through `detour`, a tunnel with `listen_port` and the Tailscale endpoint are not
  affected.
- iOS: the layer there does not use its connect mode, the key has no effect.

## 8. Files

Fork `wireguard-go` (branch `lx-awg2-v007`, [PR #7](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/7), commits `34269a2` and `012c2e9`):

| File | What |
|---|---|
| `conn/lx_messages.go` | new: untouched-message check, replacing a message's buffer |
| `conn/lx_connect.go` | new: path reset interface |
| `conn/lx_connect_linux.go` | new: connected socket — state, connect and disconnect, send, port binding, interception of ICMP errors on read |
| `conn/lx_connect_default.go` | new: stubs for the other platforms |
| `conn/bind_std.go` | insertions: a state field, four lines in socket opening, a condition in the cleanup loop, a slice in the read call, a call in send, buffer replacement on retry |
| `device/lx_bind_path.go` | new: telling the layer about a path reset |
| `device/peer.go` | insertions: one line in each of the two places where the source address is reset |
| `conn/lx_messages_test.go`, `conn/lx_connect_linux_test.go`, `device/lx_packet_sized_test.go`, `device/lx_backlog_stand_test.go` | tests and the stand |

Core: `option/lx.go` (the `connected_socket` key), `transport/wireguard/tuning_lx.go` and one
line in `transport/wireguard/endpoint.go`; the `submodules/wireguard-go` gitlink.
