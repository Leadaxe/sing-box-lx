# SPEC: 158 — WG_QUEUED_PACKET_BUFFERS

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-11-23).

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)
**Touches:** P1 — entry 158 in the registry carries the removal condition. The other promises are not affected. Adjacent [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md): the permanent buffers of the receive routines, described by [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md), are not changed here (§6).

| Field | Value |
|-------|-------|
| Type | B (bug) — a property of upstream `wireguard-go`: a queued packet holds a 64 KiB buffer |
| Status | I (implemented) — ships in `v1.14.4-lx.36`. Merged into the fork's release branch ([PR #6](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/6), commit `f1e73df`), the submodule is bumped, the core side (§2.4) is written. Verified by the stand, the end-to-end test and a live run of the release candidate on NanoPi (Linux arm64). Not verified: an Android device, a live link |
| Branch | `lx`; fork `Leadaxe/wireguard-go-awg2-lx`, branch `lx-awg2-v007` |
| Build tag | — (the change is in the submodule and applies to all builds) |
| Source | [wireguard-go-awg2-lx#5](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/5) — KeenProxy: about 300 MiB on a router at 300–400 Mbit/s |
| Related | [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (permanent buffers of the receive routines), [010](../010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (batched socket reads), [148](../148-OOM_KILLER_RUNTIME_MEMORY/SPEC.md) (runtime memory limit — these buffers count toward it) |

How this solution was reached and what was rejected along the way is in [HISTORY.md](HISTORY.md).

## 0. In plain words

WireGuard reads from the network and from TUN into 64 KiB buffers, sized with a margin in case a
single read returns a coalesced batch. The packet then travels through the encryption or decryption
queue and through the peer queue, and the buffer travels with it. A 1400-byte packet waiting for
its turn occupies 64 KiB.

While the consumer keeps up, this is not visible: the buffer is held for a fraction of a
millisecond. When the consumer is slower than the source (fast link, weak CPU), packets pile up,
and a thousand queued packets take 64 MiB instead of one and a half. On a router with a gigabyte
of memory and several tunnels the total reaches hundreds of megabytes.

The change makes a queued packet hold a buffer of its own size, about 2 KiB:

- **on send** the buffer is allocated by the tunnel MTU from the start, nothing is copied;
- **on receive** the size is not known in advance, so the read goes into a large buffer, and a
  copy of the packet in a compact buffer goes into the queue; the large buffer stays with the
  reading loop.

Where the large buffer is needed for a reason (a TUN that writes and reads in batches), everything
stays as it was.

## 1. Problem

### 1.1 Mechanism

Receive. The socket read routine reads datagrams into `MaxMessageSize` read slots (65535 bytes on
Linux, macOS, Android; 2016 on Windows; 1700 on iOS). A transport message is placed into a queue
element together with its slot, and the routine takes a new slot from the pool. The element passes
the decryption queue (1024 containers) and the peer queue (1024 containers) and releases the slot
only after the write to TUN.

Send. The TUN read loop reads a packet into a `MaxMessageSize` buffer from the sing allocator (the
64 KiB class). The element with this buffer passes the staged queue, the encryption queue and the
peer queue and releases the buffer after the send to the socket. The injection path (`InputPacket`,
used by direct L3 forwarding) already allocates by packet size; the TUN read loop does not.

A container carries up to one read batch: one element when the bind reads one packet at a time, up
to 128 with batched reads. Hence the upper bound of the peer queue: 1024 packets through `detour`
and up to 131 072 without it.

### 1.2 The permanent part

Besides the queues, buffers are held permanently for as long as the device is alive:

| Who | How much | Depends on |
|---|---|---|
| Receive routines (IPv4 and IPv6) | 2 × 128 slots = 16.4 MiB per endpoint | bind without `detour` (batched reads on Linux and Android); through `detour` — 2 slots |
| TUN read loop | 128 buffers × 64 KiB = 8.2 MiB per endpoint | the device batch size = the maximum of bind and TUN, that is 128 for a bind without `detour`, even if TUN delivers one packet at a time |

Of the 128 buffers of the read loop of a TUN that delivers one packet at a time, one is used; the
rest are counted in the Go heap, but their pages are untouched.

### 1.3 Who is affected

- **All tunnels on `wireguard-go`**: WireGuard, AmneziaWG, MASQUE/WARP on top of a WG device.
  The Tailscale endpoint runs on its own fork of `wireguard-go` and is not affected by this change.
- **Hit hardest** — routers and phones: the link is faster than the CPU, memory is scarce.
- **Send affects downloads too**: the client sends TCP acknowledgements, each 60 bytes in a
  64 KiB buffer.
- **Not affected**: Windows and iOS on receive (slots there are already 2016 and 1700 bytes). Send
  is affected everywhere.

### 1.4 What bounds the queue in practice

The capacity of the queues (§1.1) and the TCP windows: the queue cannot hold more data than the
senders keep in flight. Eight streams with a window of several megabytes mean thousands of packets.
On Android and iOS there is also a hard limit of the slot pool (4096 and 1024).

## 2. Solution

Everything new is in one file of the fork, `device/lx_packet_sized.go`; the state of the change is
gathered in one structure of the device. Upstream files carry only marked insertions: `receive.go`,
`send.go`, `pools.go`, `device.go`, +43 −13 lines.

### 2.1 Receive: copy on read

The receive routine reads into slots as before. A transport message that fits into 2 KiB is copied
into a buffer from a separate pool, and that buffer goes into the queue. The slot does not leave
the routine and is used for the next read. Decryption runs in place in the compact buffer, as it
used to run in the slot.

A queue element owns one of two buffers: a slot (as in upstream) or a compact buffer. The places
where upstream returns the slot are untouched: returning an empty slot is a no-op, and the compact
buffer is returned in the same place where the element is returned to its pool.

### 2.2 Send: buffer by MTU

A TUN that delivers one packet per read does not return more than its MTU. The read loop allocates
a buffer for the MTU plus what encryption builds around the packet: headroom at the front, the
transport padding `s4`, the header, the tag, a tail for message padding. The layout is the same as
in the injection path. Data is not copied.

Three safeguards:

- **MTU or `s4` grew on the fly.** The loop checks the required size before every read and
  replaces the buffers.
- **A packet was read under the old `s4`.** The reader starts together with the device and is
  already blocked in a read when the first configuration arrives. Encryption already used to shift
  such a packet to a new position in the buffer; when a packet-sized buffer has no room, the packet
  is moved to a larger one. Previously an injected packet was dropped in this situation; now it is
  moved as well.
- **TUN delivered more than the MTU promised.** The sign is a buffer filled to the end: the packet
  may have been truncated, and there is no room for the tag. The packet is dropped, and the device
  reads into 64 KiB buffers for the rest of its lifetime and writes this to the log. If the buffer
  is merely stale (the MTU has just grown), one packet is dropped and the size is corrected on the
  next read.

### 2.3 When the full buffer stays

| Condition | Receive | Send |
|---|---|---|
| TUN writes or reads in batches (`BatchSize() > 1`) or implements `tun.GRODevice` | slot | 64 KiB |
| TUN reports that it hands written packets over for coalescing (`MergesWrites`, §2.4) | slot | by MTU |
| The message is longer than 2048 bytes on the wire, not counting `s4` (plaintext with AWG padding larger than 2016) | slot | — |
| The platform slot is already no larger than 2 KiB (Windows, iOS) | slot | by MTU |
| Handshakes | slot | — |

Coalescing on the write to TUN runs in place, into the capacity behind the first packet. A compact
buffer has no room to coalesce into: the data would arrive, but every segment would go out as a
separate write.

### 2.4 The core side: the L3 return path

The core's device for a non-system stack declares one-packet writes, but with direct L3 forwarding
from a TUN inbound a return path is attached to it, and decrypted packets go to TUN bypassing the
endpoint's stack. On a Linux TUN with the virtio header (GSO) this write coalesces segments in
place. The device batch size does not reveal this.

The fork asks TUN for an optional method `MergesWrites() bool` before every packet. The device
wrapper in the core answers "yes" when a return path is attached and that path requests room in
front of the packet beyond `tun.PacketOffset`: among the writing sides of sing-tun only the Linux
TUN with the virtio header does so; the write through gVisor and the return path of ICMP forwarding
request no room and copy the data. The method lives in a new lx file
`transport/wireguard/port_merge_lx.go`; the core's upstream files are untouched.

**A consequence worth knowing.** The return path is attached at the very first flow from the TUN
inbound into the tunnel and stays until close. From that moment **all** receive of this endpoint
runs in slots, as before the change — including traffic that did not arrive through TUN (SOCKS
inbound, the endpoint's stack): at the time a datagram is read it is not known which path the
packet will take. The result by configuration:

| Inbound | Receive | Send |
|---|---|---|
| SOCKS/HTTP/mixed, tproxy, redirect; TUN without GSO (Android, macOS, Windows) | compact buffer | by MTU |
| TUN on Linux with GSO, direct forwarding into the tunnel | slot, as before the change | already packet-sized (injection path); the permanent part of the read loop — by MTU |

There is no regression in the second row: receive behaviour there is unchanged, coalescing is
preserved (§4.8).

### 2.5 Bounded pools (Android, iOS)

The compact-buffer pool has the same accounting as the slot pool and the same limit. Only the
receive routine takes from it — in the same place where upstream waits for a slot. Further down
the pipeline buffers are only returned, so nothing there waits for a buffer held by something
even further down.

On Android the queue used to hit 4096 slots (256 MiB); now it hits 4096 compact buffers (8 MiB).

## 3. Decisions and rationale

**D1. Both directions are fixed, not only receive.** The task arrived as "receive buffers", but
the profile from KeenProxy's router and the project's own end-to-end test show that send weighs no
less: in their profile the 64 KiB buffers of the sing allocator take 100 MiB against 47–50 MiB of
slots; in the end-to-end test send alone takes 30–45 MiB off the process peak, while a
receive-only change (KeenProxy's port) differs from the baseline within the stand's resolution
(§4.4).

**D2. Send: size by MTU, not a copy.** The size of a packet from TUN is known in advance: it does
not exceed the MTU. So the required amount can be allocated at once, with no pass over the data.
A copy here would be a cost paid for nothing.

**D3. Receive: copy on read.** The size of a datagram is unknown before the read, so the read has
to go into a full slot, and from there three ways exist. They were compared in one binary (§4.3,
§4.4):

| Method | Memory under backlog | Speed on the board | Complexity |
|---|---|---|---|
| Slot budget: copy only after 128 slots are in flight (first version of PR #6) | 11.9 MiB per 1026 packets, 8 MiB of which is the budget | same as the baseline | a counter that has to be balanced on every exit path; it already had a leak |
| Decrypt straight into a compact buffer | 4.2 MiB | slower than the baseline by 1.4–4.5 % with a small window | the slot still leaves the routine and comes back from the worker; insertions in the decryption worker |
| **Copy on read** | **4.2 MiB** | **from −3.7 to +0.9 % against the baseline** | **the slot does not leave the routine; the decryption worker is untouched** |

The copy costs one pass over 1.4 KiB. On a router-class CPU it pays for itself because the slot
stays in cache: upstream takes a cold 64 KiB slot from the pool for every packet. On a Mac with a
large cache the copy cost +7–8 % per packet on the in-memory stand; the CPU is not the bottleneck
there, and on real sockets the difference drowns in the cost of system calls.

The principle is the same as in KeenProxy's change. The implementation is the fork's own: the
buffer is owned by the element, not by a device mode; a pool with accounting; a coalescing guard;
upstream's release sites are untouched.

**D4. The element owns the buffer; upstream's release sites are not touched.** Upstream returns
the slot in several places. If each of them asked "which mode is this", a new release site arriving
with a merge would silently start leaking or returning the wrong buffer. So returning an empty
slot is made a no-op, and the compact buffer is returned together with the element — in one place
that every path calls.

**D5. One fixed size of 2 KiB in a dedicated pool, not sing's size classes.** Pool accounting is
needed for Android and iOS (D7); the sing allocator has none. One size covers any tunnel with an
MTU up to 2016; tunnels with a larger MTU are rare and stay on slots (§6).

**D6. The coalescing guard is double: by the device and by its word.** The batch size and
`GRODevice` are known when the device is created and cost nothing per packet. The core's return
path is attached on the fly and is not visible through these signs, so it gets a separate question
to TUN before each packet — one call and one atomic read. Without it the change would quietly
reduce throughput in the fastest Linux configuration. The cost of the guard is that it applies to
the whole endpoint, not to a packet: while the return path is attached, receive stays in slots
entirely (§2.4). The choice was to keep the speed where it was and not promise savings, rather
than the reverse.

**D7. The limit on mobile platforms is kept, and the buffer is taken only in the receive
routine.** If the decryption worker took the buffer, then on pool exhaustion the worker of the
head container could wait for a buffer held by the containers behind it, while those wait for the
head container's write. In the receive routine the wait is ordinary backpressure on the socket,
as in upstream.

**D8. A packet larger than the promised MTU: drop and fall back for good, rather than crash or
guess.** All TUN devices of the core and of the fork return a truncated packet for a short buffer.
A truncated packet must not be sent, and continuing with compact buffers after a violation means
losing more packets later. One lost packet and a return to upstream behaviour is the cheapest
reaction. A stale buffer after an MTU increase is distinguished from a violation and does not
disable the optimization.

**D9. Move to a larger buffer instead of a drop when `s4` grows.** The packet-sized buffer took
away the slack on which the shift of a packet read under the previous padding relied. Without the
move, the first full-size packet after start under AWG was lost — the stand caught this.

**D10. Handshakes are not touched.** There are few of them, they live for fractions of a second,
and the handshake queue is bounded separately.

**D11. The permanent buffers of the receive routines are not touched.** 16.4 MiB per endpoint
without `detour` is the subject of [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md): batched reading
requires ready buffers for the whole batch, and the batch size has to be changed consistently in
the bind and in TUN. After this change they become the largest item in the tunnel's heap (§4.5)
and the first candidate for the next step.

**D12. Queue depth is not limited.** The change makes a queued packet cheap but does not shorten
the queue. Dropping on overflow is a different behaviour with different consequences for TCP, and
after the change it has little to do with memory: 131 072 packets of 2 KiB each are 256 MiB in
the worst theoretical case, against 8 GiB.

**D13. The measurement stand lives in the fork's tests.** It builds the same way for any branch
and can run over real sockets, so the next measurement (after an upstream merge, on another board)
does not require writing it again. The measurement is enabled by an environment variable; a
regular run keeps only the checks with bounds.

## 4. Measurements

### 4.1 Stands

| Stand | What it is |
|---|---|
| In-memory | Two real `Device` instances in one process; bind and TUN are channels. The receiver's TUN or the sender's socket can be closed to build up a backlog |
| Sockets | The same over real UDP sockets on loopback: batched `recvmmsg` reads, kernel GSO/GRO |
| End-to-end | Two instances of the core on one board, client and server; SOCKS inbound → WG/AWG endpoint without `detour` → loopback → the server's endpoint → `direct` → local data source. 8 TCP streams, 20 s of download, then 20 s of upload |

Hardware: NanoPi, 4 × Cortex-A55, 1 GiB, OpenWrt 25.12, Linux 6.1 arm64, Go 1.26.10. Both core
instances and the load generator share the board's four CPU cores; the CPU is fully loaded. The
first series (HISTORY §2) was taken on a Mac M1 Pro.

Variants: **baseline** — `3ed0471`; **KeenProxy's port** — `392d8d3`; **slot budget** — `ae1a651`;
the receive modes "slot", "decrypt into a compact buffer", "copy on read" and the send modes
"64 KiB" / "by MTU" — one binary with a switch.

### 4.2 Memory with a full queue

The queue is built up until the pipeline stops; heap growth after two garbage-collection cycles.
About 2 MiB in every receive figure is the stand's own queues.

| | Receive, in-memory | Send, in-memory | Receive, sockets | Send, sockets |
|---|---:|---:|---:|---:|
| Packets in the queue | 1026 | 1026 | 1360–2050 | 1026 |
| baseline | 66.2 MiB | 64.3 MiB | 117–130 MiB | 65.4 MiB |
| KeenProxy's port | 4.2 MiB | 64.3 MiB | 4.2 MiB | 65.4 MiB |
| slot budget | 11.9 MiB | 64.3 MiB | 11.9–12.1 MiB | 65.4 MiB |
| decrypt into a compact buffer + send by MTU | 4.2 MiB | 2.1 MiB | 4.1 MiB | 3.3 MiB |
| **copy on read + send by MTU** (final branch) | **4.2 MiB** | **2.1 MiB** | **4.2–4.4 MiB** | **3.3 MiB** |

Per queued packet: 66–68 KiB for the baseline, 3–4 KiB after the change.

### 4.3 Cost per packet

The whole A → B path, 1400-byte packets, four workers, the sender keeps a window of packets in
flight. Two-second runs, variants alternate.

One binary, modes are switched by an environment variable. Percentages are relative to the first
column; "injection" — the sender feeds packets through `InputPacket`, "TUN" — through the TUN read
loop.

| Stand, window | Path | slot + 64 KiB, ns/packet | slot + by MTU | decrypt into compact + by MTU | **copy on read + by MTU** |
|---|---|---:|---:|---:|---:|
| in-memory, 64 | injection, WireGuard | 20 048 | +0.3 % | +3.9 % | **+0.7 %** |
| in-memory, 64 | injection, AWG | 20 302 | +1.7 % | +4.5 % | **+0.9 %** |
| in-memory, 64 | TUN, WireGuard | 23 546 | −1.4 % | +1.4 % | **−2.3 %** |
| in-memory, 64 | TUN, AWG | 23 506 | +0.5 % | +2.9 % | **+0.5 %** |
| in-memory, 1024 | injection, WireGuard | 16 204 | +0.3 % | −0.9 % | **−1.2 %** |
| in-memory, 1024 | injection, AWG | 16 590 | −0.2 % | −0.7 % | **−2.5 %** |
| in-memory, 1024 | TUN, WireGuard | 18 040 | −2.3 % | +1.2 % | **−3.7 %** |
| in-memory, 1024 | TUN, AWG | 18 355 | −2.1 % | +0.2 % | **−3.0 %** |
| sockets, 64 | injection, WireGuard | 54 765 | −0.1 % | −0.7 % | **−0.3 %** |
| sockets, 64 | injection, AWG | 54 324 | −0.5 % | −0.7 % | **+1.0 %** |
| sockets, 64 | TUN, WireGuard | 55 149 | −0.2 % | −1.1 % | **−0.9 %** |
| sockets, 64 | TUN, AWG | 54 761 | +0.2 % | +2.1 % | **+1.2 %** |

In memory — the median of four rounds, on sockets — of three. CPU time per packet follows
wall-clock time with the same sign.

- **Copy on read is not slower than the baseline**: from −3.7 to +0.9 % in memory, ±1.2 % on
  sockets.
- **Decrypting into a compact buffer is slower with a small window**: +1.4…+4.5 %.
- **Send by MTU by itself does not change the cost**: ±2.3 %.
- **On real sockets everything drowns in the cost of system calls**: a packet costs 54 µs against
  20 in memory, and the differences between modes are within ±2 %.

The first series compared different binaries (6 rounds): in memory with window 64 KeenProxy's port
is faster than the baseline by 4–6 %, decrypting into a compact buffer is slower by 3–5 %, the slot
budget is within ±1 %; with window 1024 and on sockets all are within ±3 %. The sign is the same,
the spread is larger — part of it comes from the re-layout of the binary itself, so the
conclusions are drawn from the table above.

On a Mac M1 Pro (in memory, one worker) the sign is reversed: copying every packet costs +7.0 %
for WireGuard and +8.1 % for AWG.

### 4.4 End-to-end test

Median of three rounds. Peak process RSS, MiB; "download" — the peak over the first phase, "whole
run" — over both.

WireGuard:

| Variant | Client, download | Server, download | Client, whole run | Server, whole run | Mbit/s ↓ / ↑ |
|---|---:|---:|---:|---:|---:|
| baseline | 90 | 113 | 137 | 150 | 97 / 124 |
| slot + send 64 KiB (baseline from the new branch) | 96 | 111 | 139 | 146 | 102 / 128 |
| slot + send by MTU | 69 | 91 | 103 | 106 | 98 / 126 |
| decrypt into a compact buffer + by MTU | 73 | 94 | 99 | 119 | 101 / 119 |
| **copy on read + by MTU** | **65** | **96** | **92** | **117** | **98 / 126** |
| KeenProxy's port | 86 | 110 | 136 | 154 | 101 / 124 |

AmneziaWG (`s4=60`):

| Variant | Client, download | Server, download | Client, whole run | Server, whole run | Mbit/s ↓ / ↑ |
|---|---:|---:|---:|---:|---:|
| baseline | 97 | 113 | 148 | 159 | 102 / 121 |
| slot + send 64 KiB (baseline from the new branch) | 84 | 109 | 133 | 142 | 102 / 120 |
| slot + send by MTU | 78 | 92 | 103 | 108 | 101 / 122 |
| decrypt into a compact buffer + by MTU | 75 | 88 | 99 | 108 | 101 / 124 |
| **copy on read + by MTU** | **63** | **92** | **99** | **113** | **99 / 121** |
| KeenProxy's port | 86 | 111 | 129 | 155 | 102 / 121 |

What follows from this:

- **The stand's resolution is about 15 MiB.** The first two rows are the same behaviour from
  different builds, and they differ by 2–15 MiB. The stand does not resolve differences smaller
  than that.
- **Send by MTU takes 30–45 MiB off the peak** on both sides.
- **Copy on read takes another 4–15 MiB off on the client during download**, on top of send by
  MTU — at the edge of the stand's resolution; on the server, which receives almost nothing in
  this phase, there is no difference. The heap profile (§4.5) shows the same directly: 30 MiB of
  slots in the client's queue disappear.
- **A receive-only change barely differs from the baseline**: KeenProxy's port — 86 against 90–97
  on the client during download and 129–136 against 137–148 over the whole run, within the stand's
  resolution.
- **Speed and CPU time per gigabyte are the same** for all variants within ±5 %.

A separate build of the core with the final branch (without the switch), two rounds against the
baseline, gave the same: the client during download 64 against 87 MiB over WireGuard and 63
against 85 over AWG; over the whole run the client 86 against 122 and 93 against 132, the server
110 against 133 and 108 against 143.

### 4.5 Heap profile

One snapshot at the twelfth second of the download, WireGuard, MiB in use:

| | Client: total | slots | sing 64 KiB | sing 2 KiB | Server: total | slots | sing 64 KiB | gVisor buffers |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| baseline | 63.6 | 47.9 | 11.7 | — | 58.2 | 20.2 | 30.3 | 3.1 |
| KeenProxy's port | 32.0 | 18.0 | 10.9 | — | — | — | — | — |
| copy on read + by MTU | 22.6 | 18.0 | — | 1.0 | 44.5 | 18.0 | 1.6 | 15.7 |

- **18.0 MiB of slots always remain** — these are the permanent buffers of the two receive
  routines (§1.2). After the change this is the largest item in the tunnel's heap.
- **On the server the queue moved into gVisor.** The stack's buffers grew from 3 to 16 MiB:
  packets that used to wait in WireGuard at 64 KiB each now wait in the stack at their own size.

### 4.6 KeenProxy's measurement on a router

Keenetic ARM64, live traffic, one Speedtest run per variant; the control is their port, the
candidate is the slot-budget variant (`ae1a651`). Peak RSS 430 → 385 MiB, CPU per second −11 %.
In the profile of both: slots 47–50 MiB, sing 64 KiB buffers 90–100 MiB, gVisor buffers 51–58 MiB.

The authors themselves state the caveats: one run each, speeds not equalized, the candidate sits
on a different base. What matters in this measurement for this task is the distribution by site:
slots there barely depend on the receive change (they are the permanent part of three endpoints),
and the main site is send.

The current variant has not been measured on their router.

### 4.7 What the measurements do not show

- **A live link.** Both ends of the tunnel are on one board over loopback; there is no real
  network interface, no loss and no third-party server.
- **Android and iOS.** Behaviour on pool exhaustion is verified only by a test with substituted
  limits.
- **The 1.15 line.** The change merges into the fork's `lx-1.15` without textual conflicts; the
  build and the tests were not run there.

### 4.8 Live run of the release candidate

The core from the merged tree (`lx` after the merge of `upstream/stable` on 2026-11-23, full tag
set), the same board. A client with two inbounds: SOCKS and TUN (`stack: system`, no `auto_route`,
a route to the test address), TUN with TCP GSO; traffic from TUN goes into the tunnel by direct L3
forwarding.

- **Stability.** 10 cycles of start → load through both inbounds → stop (5 WireGuard, 5 AWG):
  traffic flows every time, there are no panics or hangs, the stop takes 100–300 ms.
- **Speed on the TUN path did not change.** Download 48–50 Mbit/s against 43–48 for the baseline,
  upload 255–262 against 242–264.
- **The server during upload through TUN** (receive on the server; it has no return path): peak
  RSS 270 → 117 MiB over WireGuard and 174 → 136 over AWG; in the heap, slots 156 → 12 MiB, sing
  64 KiB buffers 79 → less than 1 MiB.
- **The client with an attached return path** keeps slots on receive, like the baseline: 46 MiB
  against 50. Its sing 64 KiB buffers are gone all the same: 63 MiB → less than 1 MiB.
- **The client's peak during upload through TUN stays high** — 188–197 MiB, 168–212 for the
  baseline. These are not WireGuard buffers: the growth comes from the queue of the TUN inbound
  itself and from the stack; it is outside this task.

The compared builds differ in tag set (the release candidate has the full one, idle RSS 50 MiB
against 29), so speeds, increments and heap profiles are compared, not the client's absolute peaks.

## 5. Verification

Fork tests (`device/lx_packet_sized_test.go`; the stand is `device/lx_backlog_stand_test.go`):

- which buffer the element holds: the message fits, sits right at the boundary, does not fit;
- enqueueing a packet does not allocate memory;
- a full queue in both directions costs no more than 6 KiB per packet, packets arrive byte for
  byte and in order (WireGuard and `s4=60`);
- pools with limits: the receiver stops at the limit, after TUN is opened everything is delivered,
  all buffers are returned;
- packets for a stopped peer and the queue of a removed peer return their buffers;
- a batching TUN sees only slots and is read into full buffers; a TUN that reports coalescing gets
  slots for as long as it reports it;
- a message larger than 2 KiB arrives in a slot;
- the read buffer size matches the layout of the injection path and grows with the padding;
- the first full-size packet under `s4`, read before the configuration, is not lost;
- `s4` growth on the fly; a packet larger than the MTU is dropped and disables packet-sized
  buffers; a stale buffer after an MTU increase does not.

Runs: `go test -race ./device ./conn` on darwin/arm64; the full `device` and `conn` suites on
linux/arm64 (NanoPi); `go vet ./device` for linux/arm64, linux/mipsle, android/arm64,
windows/amd64, windows/386, freebsd/amd64, ios/arm64.

Two independent reviews (of the first version and of the current one). The second found no
buffer-ownership errors, races or out-of-bounds accesses; it found the loss of coalescing on the
return path (§2.4) and a false trigger of the guard on MTU growth (§2.2) — both are fixed.

## 6. What remains open

- **Linux with GSO on the TUN inbound gets no gain on receive** (§2.4): the coalescing guard
  applies to the whole endpoint while the return path is attached. Narrowing it to the packets
  that will actually go to TUN is not possible at the time a datagram is read — a different
  approach is needed (for example, coalescing for which compact buffers are enough).
- **Permanent buffers of the receive routines** — 16.4 MiB per endpoint without `detour` (D11).
- **Messages larger than 2 KiB** (tunnel MTU above 2016) stay on slots.
- **Queue depth** is not limited on Linux and macOS (D12).
- **Field verification**: an Android device, a router with a live link.
- **The default branch of the fork** points to the old `lx` line, which is why the external PR was
  made from the wrong base. A repository setting; the owner changes it.
- **Benchmark hangs** from the first series (HISTORY §4): the cause is not established. On NanoPi
  there were no stops over 624 completed benchmark runs (90 of them with KeenProxy's port); the
  stand now reports a stop itself, with the number of packets in flight.

### 6.1 Read batches: a measurement and a potential

Not a defect and not part of this task — an observation that came up while the batch size was
being traced.

Without `detour`, WireGuard talks to the kernel through a UDP socket: it reads and writes up to
128 datagrams in one system call, and the kernel coalesces and splits segments itself. Through
`detour` there is no kernel socket: the core's binding layer (`ClientBind`) reads one datagram at
a time from the outer node's connection and writes one at a time, so the batch size is 1.

**Measurement (2026-11-23, NanoPi, release candidate).** The same WireGuard tunnel three ways:
directly; through `detour` to a `direct` outbound (the same loopback UDP socket underneath, but
read one datagram at a time); through `detour` to shadowsocks on loopback (a real outer node, a
third process). SOCKS inbound, 8 TCP streams, 20 s per direction, median of three rounds.

| Client path | Download, Mbit/s | Upload, Mbit/s | CPU, s/GiB: client + server (+ node), down / up |
|---|---:|---:|---:|
| directly, batches of 128 | 96.6 | 122.3 | 331 / 263 |
| `detour` → `direct`, one at a time | 102.1 | 157.6 | 302 / 209 |
| `detour` → shadowsocks | 50.7 | 70.6 | 615 / 459 |

The spread between rounds is up to 4 Mbit/s on download and up to 10 on upload.

What follows:

- **There is no reason to gather batches in the binding layer for the path through `detour`.**
  The one-at-a-time path is not slower than the batched one: +6 % on download, +29 % on upload,
  9–21 % less CPU time per gigabyte. The idea is closed by the measurement.
- **Speed through `detour` is cut by the outer node**, not by the absence of batches: with a
  second encryption and framing, by half.
- **A potential in the opposite direction: a batch of 128 on the direct path may hurt** on a CPU
  with a small cache. The mechanism is the one in §4.3: 128 slots of 64 KiB per receive routine
  are cycled through, each next one cold; packets cross the pipeline in groups. A smaller batch
  would also give memory back: the permanent buffers of the receive routines (§1.2) — 16.4 MiB
  per endpoint — shrink in proportion.

Why the last point is a hypothesis for now:

- Two different binding layers were compared as a whole, not two batch sizes: the direct path has
  its own send code and two sockets (IPv4 and IPv6), the path through `detour` has one connected
  socket.
- Everything ran over loopback; on a real network card, coalescing in the kernel may give a gain
  that is not visible here.
- The speeds are 100–160 Mbit/s; batches are designed for more.
- `detour` was on the client only; the server read in batches in both cases.

A clean check: the same direct path with a batch of 128 and with a small one (8–16), on the board
and through its network port. The batch size is wired into several places (the bind, the message
pools, the device batch size as the maximum of bind and TUN) and has to change consistently —
this has gone wrong before, see [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md). Raw data of the
measurement: `.work/spec158/logs/detour.log`; the scenario: `.work/spec158/detour.sh`.

## 7. Files

The `wireguard-go` fork (branch `lx-awg2-v007`, commit `f1e73df`):

| File | What |
|---|---|
| `device/lx_packet_sized.go` | new: the compact-buffer pool, buffer choice on receive, size and safeguards of the TUN read, the move on padding growth |
| `device/receive.go` | insertions: element field, enqueueing, the frame for TUN |
| `device/send.go` | insertions: buffer size of the read loop, overflow check, move instead of drop |
| `device/pools.go` | insertions: pool creation, return of an empty slot, return of a compact buffer |
| `device/device.go` | insertion: pool fields |
| `device/lx_packet_sized_test.go`, `device/lx_backlog_stand_test.go` | new: tests and the stand |

The core: `transport/wireguard/port_merge_lx.go` (the `MergesWrites` method of the device wrapper)
and its test; the `submodules/wireguard-go` gitlink.

## 8. Removal condition

Upstream (`WireGuard/wireguard-go` or `sagernet/wireguard-go`) stops carrying a `MaxMessageSize`
buffer with every queued packet — on receive and in the TUN read loop. To be checked on every
upstream merge into the fork: if `RoutineReceiveIncoming` and `RoutineReadFromTUN` gain their own
allocation by size, the fork's insertions are removed and the stand stays for cross-checking.
