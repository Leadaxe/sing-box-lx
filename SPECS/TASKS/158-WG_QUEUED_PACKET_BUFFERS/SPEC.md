# SPEC: 158 — WG_QUEUED_PACKET_BUFFERS

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-11-23).

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)
**Touches:** P1 — entry 158 in the registry carries the removal condition. The other promises are not affected. Adjacent [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md): the permanent buffers of the receive routines, described by [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md), are not changed by part 1; their number is set by the part 2 setting (§9), and ENERGY's Boundaries section points to `read_datagram_count`.

| Field | Value |
|-------|-------|
| Type | Part 1 — B (bug): a property of upstream `wireguard-go`, a queued packet holds a 64 KiB buffer. Part 2 — F (feature): two buffer settings in `lx.wg.buffers` |
| Status | **Part 1 — I (implemented)**, **part 2 — I (implemented), §9**. Both ship in `v1.14.4-lx.36`. Part 2: fork [PR #7](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/7), `b565d0c`; verified by the fork's tests and by a measurement of the finished build on NanoPi, not verified on Android. Part 1: merged into the fork's release branch ([PR #6](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/6), commit `f1e73df`), the submodule is bumped, the core side (§2.4) is written. Verified by the stand, the end-to-end test and a live run of the release candidate on NanoPi (Linux arm64). Not verified: an Android device, a live link |
| Branch | `lx`; fork `Leadaxe/wireguard-go-awg2-lx`, branch `lx-awg2-v007` |
| Build tag | — (the change is in the submodule and applies to all builds; the part 2 keys also work in any build, they do not need the `with_lx_idle_suspend` tag) |
| Source | [wireguard-go-awg2-lx#5](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/5) — KeenProxy: about 300 MiB on a router at 300–400 Mbit/s |
| Related | [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (permanent buffers of the receive routines), [010](../010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (batched socket reads), [148](../148-OOM_KILLER_RUNTIME_MEMORY/SPEC.md) (runtime memory limit — these buffers count toward it), [098](../098-LX_ROOT_CONFIG_BLOCK/SPEC.md) (layout of the `lx.wg` block), [160](../160-WG_DIRECT_SEND_COST/SPEC.md) (send cost on the direct path; done before part 2, edits the same read line) |

How this solution was reached and what was rejected along the way is in [HISTORY.md](HISTORY.md).

**A task in two parts** — both about the 64 KiB WireGuard buffers, on receive and on send:

| Part | What it is about | Sections | State |
|---|---|---|---|
| 1 | Buffers that travel with queued packets: a packet holds about 2 KiB instead of 64 KiB | §0–§8 | done |
| 2 | Permanent receive buffers and the `lx.wg.buffers` settings: the number of buffers per socket read and the switch for the send buffer by MTU | §9 | done |

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

**D11. Part 1 does not touch the permanent buffers of the receive routines.** 16.4 MiB per
endpoint without `detour`: batched reading requires ready buffers for the whole batch, and only
their number can be reduced. After the part 1 change they become the largest item in the tunnel's
heap (§4.5). Part 2 deals with them (§9): the number of buffers is set by a setting, and the
default stays as in upstream.

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
- **Permanent buffers of the receive routines** — 16.4 MiB per endpoint without `detour`. Part 1
  does not touch them (D11); their number is set by the part 2 setting (§9).
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
- **The hypothesis "a batch of 128 on the direct path hurts speed" was checked separately and
  was not confirmed** — see below.

**A clean check of the batch size (2026-11-23).** One binary with a measurement switch (not part
of the release): the direct path, both instances with the same setting — a batch of 128 with
kernel coalescing (as released), 128 without coalescing, 32, 8 and 1 (without coalescing: reading
a coalesced datagram needs the full batch). The same scenario, median of three rounds.

| Batch | Download, Mbit/s | Upload, Mbit/s | CPU, s/GiB, down / up | Client peak RSS, download, MiB | Client RSS after warm-up, MiB |
|---|---:|---:|---:|---:|---:|
| 128 + GRO/GSO | 99.4 | 122.2 | 318 / 266 | 63 | 49 |
| 128 | 102.1 | 125.5 | 312 / 260 | 65 | 53 |
| 32 | 103.2 | 123.7 | 306 / 260 | 56 | 49 |
| 8 | 102.4 | 122.5 | 312 / 265 | 52 | 46 |
| 1 | 100.2 | 119.1 | 315 / 276 | 50 | 45 |

- **The batch size does not affect speed here**: all variants are within the spread between
  rounds (up to 6 Mbit/s on download, up to 9 on upload), CPU time per gigabyte within ±4 %.
  Kernel coalescing gives nothing on loopback either.
- **It does affect memory**: the client peak during download is 63–65 MiB with a batch of 128,
  56 with 32, 52 with 8, 50 with 1. These are the permanent buffers of the receive routines
  (§1.2): 16.4 MiB of heap with a batch of 128, about 1 MiB with a batch of 8.
- **So the gain of the `detour` → `direct` path (+29 % on upload) does not come from the batch
  size**, but from another difference between the binding layers: on upload the client spends
  154–163 s/GiB on the direct path with any batch and 117 through `ClientBind`. The cause was not
  investigated; the candidates are the send code of the direct path and its two sockets.

What can be taken from this:

- **A batch-size setting for the sake of memory**: 12–15 MiB less heap per endpoint without
  `detour`, with no loss of speed on this board. It is taken up as part 2 of this task — §9.
- **Finding out why sending on the direct path costs more** than sending through `ClientBind` is
  task [160](../160-WG_DIRECT_SEND_COST/SPEC.md): the causes are found, the gain is up to a quarter
  on upload.

Caveats for the whole section: loopback, not a network card (on a real card kernel coalescing may
give a gain that is absent here); speeds of 100–160 Mbit/s, while batches are designed for more;
one board. Raw data: `.work/spec158/logs/detour.log` and `batch.log`; scenarios:
`.work/spec158/detour.sh` and `batch.sh`.

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

Applies to part 1. The part 2 settings are the fork's own knob and have no removal condition.

Upstream (`WireGuard/wireguard-go` or `sagernet/wireguard-go`) stops carrying a `MaxMessageSize`
buffer with every queued packet — on receive and in the TUN read loop. To be checked on every
upstream merge into the fork: if `RoutineReceiveIncoming` and `RoutineReadFromTUN` gain their own
allocation by size, the fork's insertions are removed and the stand stays for cross-checking.

## 9. Part 2: permanent receive buffers and the `lx.wg.buffers` settings

State: implemented, ships in `v1.14.4-lx.36`. The key names are the owner's (2026-11-23).

### 9.0 In plain words

Every WireGuard tunnel without `detour` on Linux and Android has permanent buffers that are held
for as long as the tunnel is alive:

| Buffers | How many | Size | Total per tunnel |
|---|---|---|---:|
| receive — datagrams from the network are read into them | 128 per receive routine, two routines | 64 KiB | 16.4 MiB |
| send — packets from the stack are read into them | 128 | about 2 KiB (after part 1; was 64 KiB) | 0.25 MiB |

The size of a receive buffer cannot be reduced: the length of a datagram is unknown before the
read. Only their number can be reduced. That is what the first setting does. The second is a
switch for the part 1 change, for diagnostics.

```json
"lx": {
  "wg": {
    "buffers": {
      "read_datagram_count": 8,
      "send_by_mtu": true
    }
  }
}
```

### 9.1 Settings

The `lx.wg.buffers` block is global: it applies to all WireGuard and AmneziaWG endpoints of the
config.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `read_datagram_count` | integer 1–128 | not set = 128, as in upstream | How many datagrams are reserved for one socket read. For weak machines — `8` |
| `send_by_mtu` | bool | not set = `true` | Take the buffer for reading from the stack by the tunnel MTU, not 64 KiB. `false` — for diagnostics |

#### 9.1.1 `read_datagram_count`

- The value is the number of 64 KiB buffers **per receive routine**. A tunnel has two routines,
  IPv4 and IPv6, so there are twice as many buffers as the value: at 128 — 256 buffers
  (16.4 MiB), at 8 — 16 (1 MiB), at 1 — 2 (0.13 MiB).
- **With a value below 128, kernel coalescing on receive (UDP GRO) is turned off on the tunnel's
  socket.** Reading a coalesced datagram needs the full batch: it is split back into segments into
  the same set of buffers. Coalescing on send (UDP GSO) does not depend on the number of read
  buffers and stays on. On kernels below 5.12 UDP GRO is not enabled even without the setting.
- The number of send buffers depends on the same number when the tunnel's stack delivers one
  packet at a time: at 8 there are 8 of them instead of 128. After part 1 this is 16 KiB instead
  of 256 KiB.
- The value is one for the whole lifetime of the tunnel: a socket opened again (network change,
  wake-up after idle suspend) gets the same number.
- Applies on Linux and Android. On macOS the socket has its own batch code, on Windows its own
  socket; there the key is accepted and changes nothing.
- Applies only to tunnels without `detour`: through `detour` reading already goes one datagram at
  a time.
- Applies to tunnels with `listen_port` as well.
- `0`, a negative value and a value above 128 are a config load error. `128` is the same as an
  absent key.

#### 9.1.2 `send_by_mtu`

- `true` (default): the part 1 behaviour (§2.2) — the buffer for reading from the stack is taken
  by the tunnel MTU. Where the stack reads in batches, the buffers stay at 64 KiB anyway; if the
  stack returned a packet longer than the MTU, the tunnel itself falls back to 64 KiB for the rest
  of its lifetime.
- `false`: buffers for reading from the stack are always 64 KiB, as before part 1. Needed to rule
  the change out as a suspect when investigating a problem, without rebuilding the core.
- The key does not affect receive (the copy of the message into a compact buffer, §2.1).

#### 9.1.3 Builds

Both keys work in any build, including desktop and router builds. Only the idle-suspend keys
require the mobile build (tag `with_lx_idle_suspend`); they are gathered in the `lx.wg.idle`
block, and the core's refusal without the tag applies only to that block. The full layout of
`lx.wg` is in [098](../098-LX_ROOT_CONFIG_BLOCK/SPEC.md) §2.1.

### 9.2 What it gives

There are three measurements on the board, two with the measurement build and one with the
finished build (NanoPi, 4×Cortex-A55, the direct path, both core instances with the same value,
8 TCP streams, median of three rounds).

**The first** is in §6.1, the table "A clean check of the batch size": values 128, 32, 8 and 1.
Speed and CPU time do not depend on the value, memory goes down. The measurement switch there
turned kernel coalescing off both on receive and on send.

**The second** is the combination the setting produces: coalescing is off only on receive
(§9.1.1). Raw data — `.work/spec158/read-count-gro-only.md`.

| Variant | Download, Mbit/s | Upload, Mbit/s | CPU, s/GiB, down / up | Client peak RSS, download, MiB | 64 KiB buffers in the client heap, MiB |
|---|---:|---:|---:|---:|---:|
| 128, coalescing on (default) | 96.4 | 122.9 | 329 / 264 | 61.6 | 16.5 |
| 8, coalescing off on receive — **how the setting works** | 97.7 | 122.0 | 326 / 260 | 52.7 | about 1 (0.5 in the sample) |
| 8, coalescing off in both directions | 103.4 | 125.1 | 303 / 258 | 52.1 | about 1 (2.1 in the sample) |
| 8, coalescing off on receive + the message cleanup change ([160](../160-WG_DIRECT_SEND_COST/SPEC.md)) | 120.3 | 141.9 | 265 / 229 | 53.4 | about 1 (1.1 in the sample) |

- The setting against the default: speed and CPU are the same, receive buffers 16.5 MiB (by the
  profile; 16.4 computed) → about
  1 MiB, the client peak RSS during download −9 MiB. During upload the peak RSS is noisy
  (83–97 MiB in all variants), no difference is visible.
- The saving is multiplied by the number of tunnels without `detour`.
- Coalescing on send stays on with the setting: the socket is opened with it. On a data stream it
  has nothing to coalesce, the stack delivers one packet at a time; it did trigger on the
  AmneziaWG handshake (16 datagrams went out as 11 messages). That is why the second and third
  rows cannot be told apart on this stand.
- Coalescing on receive with the default value did not coalesce a single datagram on loopback.
  What it gives on a real network card the stand does not show.
- AmneziaWG with `Jc` 10 and i1–i5 (16 datagrams in the first send) starts with the value 8, data
  flows. There were no UDP losses, panics or hangs.
- Together with the message cleanup change (task 160) a small batch gives +23 % on download and
  +16 % on upload: with eight buffers, receive prepares and cleans up eight messages instead of
  128.

Both measurements were taken with a measurement build. **The third is the finished build**, keys
in the config, the same on client and server, three rounds (`.work/spec158/pr7-final-build.md`):

| Variant | Download, Mbit/s | Upload, Mbit/s | CPU, s/GiB, down / up | Client peak RSS, download, MiB | 64 KiB receive slots in the client heap, MiB |
|---|---:|---:|---:|---:|---:|
| no keys | 118.9 | 140.0 | 267 / 235 | 73.6 | 12–15 |
| `read_datagram_count: 8` | 124.1 | 142.3 | 258 / 227 | 62.3 | about 1 |
| `send_by_mtu: false` (one round) | 118.1 | 139.2 | 272 / 234 | 105.3 | — |

- With the key `8` the speed is within the spread of the build without keys, the receiver's CPU
  is lower (the client on download 104 against 114 s/GiB), the client peak RSS on download
  −11 MiB.
- `send_by_mtu: false` brings back the 64 KiB buffers for reading from the stack: 8.2 MiB in the
  heap on download and 37.6 MiB on upload against 0.5–1.5 MiB; the peak RSS is higher by 32 and
  49 MiB. Speed and CPU are the same. The switch works and shows the cost of the buffers before
  part 1.

What the measurements do not show: everything is on loopback and at 100–160 Mbit/s. On a real
network card and on fast links batches and kernel coalescing may give a gain that is not visible
here. That is why the default stays as in upstream, and `8` is a recommendation for weak
machines, not a new standard.

#### 9.2.1 Many tunnels

Measurement: 100 WireGuard tunnels without `detour` in one instance of the core, the tunnels
are up, no traffic goes through them (NanoPi, finished build; raw data —
`.work/spec158/pr7/idle100.log`).

| `read_datagram_count` | Go heap | Physically used (RSS) |
|---|---:|---:|
| 128 (default) | 1740 MiB | 94 MiB |
| 8 | 138 MiB | 77 MiB |
| 2 | 58 MiB | 75 MiB |
| 1 | 44 MiB | 74 MiB |

For comparison: one tunnel at the default — heap 21 MiB, RSS 50 MiB. 115 AmneziaWG tunnels
with real node parameters (IPv4 and IPv6, `i1`) at the default — heap 2003 MiB, RSS 105 MiB.

- **The heap and RSS are different things.** The heap is the memory the Go runtime set aside
  for objects. RSS is what the system backed with pages. A buffer that has never been read
  into is not backed. So an idle tunnel costs almost nothing physically: about 0.45 MiB at 128
  and about 0.25 MiB at 1–8.
- **The heap is still not free.**
  - All of it counts toward the runtime memory limit
    ([148](../148-OOM_KILLER_RUNTIME_MEMORY/SPEC.md)) and toward any external limit that counts
    virtual memory.
  - The garbage collection threshold is computed from it: with a live heap of 1.74 GiB the
    next collection is set at 3.5 GiB, and garbage below that threshold is not collected. In
    the measurement, unrelated traffic through the same core (a plain proxy, 6 Gbit/s, 40 s)
    did not raise RSS: 95–97 MiB. A load that produces heap garbage was not tested.
  - A buffer becomes physical once it has been read into. A tunnel under traffic occupies its
    slots for real: 12–15 MiB per tunnel at 128 (the third measurement in §9.2).
- **The more tunnels, the smaller the value.** For one or two tunnels on a weak machine — `8`.
  For tens and hundreds — `1` or `2`: the difference between 8 and 1 over a hundred tunnels is
  94 MiB of heap. The value `1` was not measured for speed on the finished build; with the
  measurement build upload was 3 % lower than at 8, at the edge of the spread.
- A tunnel through `detour` keeps one 64 KiB buffer at any value; a suspended tunnel (mobile
  build) keeps none.

### 9.3 How it works

**The `wireguard-go` fork:**

- The binding layer of the direct path (`StdNetBind`) gets a setting for the number of datagrams
  per read. It is set before the socket is opened and determines which batch size the layer
  declares to the device. From it the device takes the number of buffers of the receive routine.
- A socket read goes only into the buffers passed in, not into the whole prepared set of
  messages.
- With a value below 128 the socket is created without UDP GRO, and coalescing on receive is not
  declared for it. The switch-off happens at the same point where the socket's other options are
  set, before binding to the port: there is no window in which the kernel could coalesce a
  datagram. If coalescing stayed declared, the read would go into the tail of the message set,
  where there are no buffers, and receive would silently disappear entirely.
- The message set for system calls stays at 128 elements (D20).
- The number of datagrams is a property of the layer for the whole lifetime of the device. The
  peer's sequential receiver allocates its working array by the batch size once; a layer that
  declared a different number after the socket is reopened would break it.
- The device gets a method that turns off buffers by MTU on send. The read loop from the stack
  already checks the required size before every read (§2.2), so the switch-off takes effect from
  the next packet.

**The core:**

- The `buffers` block in `lx.wg`: parsing, range check, normalization to the canonical form (an
  empty block is dropped, default values are not written).
- The WireGuard endpoint, when creating the binding layer of the direct path, passes it the number
  of datagrams, and after creating the device, if `send_by_mtu: false`, turns off buffers by MTU.
- Insertions in upstream files: two in the transport endpoint file — the datagram count is
  passed to the layer next to the enabling of single-peer mode, and the MTU-sized buffers are
  turned off right after the device is created.

### 9.4 Decisions and rationale

**D14. A separate `lx.wg.buffers` block.** Both keys are about buffers, and their build condition
differs from that of the six idle-suspend keys. In one flat list the user could not tell a key
that requires the mobile build from a key that works everywhere; for the same reason the
idle-suspend keys move to `lx.wg.idle` (098). Future buffer settings already have a place.

**D15. The key names are the owner's.** `read_datagram_count` says what the number is, without
jargon from the code (`batch`). `send_by_mtu` — without the word "align": the core already has
`lx.mtu_align` for MTU agreement along a chain, and a second key with `align` next to `mtu` would
be confusing.

**D16. Default 128, as in upstream.** The measurement was taken on one board, on loopback and at
100–160 Mbit/s. That is enough to recommend `8` to weak machines, and not enough to change the
behaviour for everyone: batches and coalescing on a fast network card were not invented for
nothing.

**D17. The recommendation for weak machines is 8, for many tunnels 1 or 2.** At 8, 1 MiB of
16.4 remains. Going further down to 1 gives another 0.9 MiB per tunnel and in the measurement
cost 3 % on upload (at the edge of the spread): for one tunnel it does not pay off. For a
hundred tunnels the same 0.9 MiB is 94 MiB of heap, and the calculation is different (§9.2.1).

**D18. A small value turns off kernel coalescing on receive, without a separate key.** This is
not a choice but a correctness condition: splitting a coalesced datagram needs the full batch. A
separate key would allow a combination that does not work. The user has no need for the reverse
combination (128 without coalescing): it saves nothing. The setting does not touch coalescing on
send: it has nothing to do with the number of read buffers, and turning it off along the way
would change behaviour the key does not speak of.

**D19. Global, not per tunnel.** It is the machine that is weak, not the tunnel. A per-tunnel key
would have to be written by config generators into every subscription node.

**D20. The message set for system calls is not reduced.** Send uses the same set, and it gets
more datagrams than are read at once, with any stack:

- the AmneziaWG handshake goes out in one call — the decoy packets, i1–i5, the `Jc` junk packets
  and the handshake itself;
- packets injected into the tunnel in a batch (direct L3 forwarding) go in containers of up to
  128.

In the 2026-07 experiment the set was cut to 8 together with the batch, and the core crashed at
tunnel start (§9.5). A set of 128 empty headers takes kilobytes; there is nothing to save on it.
The read is limited to a slice of the set by the number of buffers passed in: without the slice
the kernel could return more messages than there are buffers, and parsing would go out of bounds.

**D21. Not "the path as through `detour`", but a small batch on the same path.** Considered:
forcing the tunnel through the binding layer for `detour`, which reads one at a time. This
changes not the number but the whole layer: the traffic counters for the energy report, the
per-interface socket pool and simultaneous IPv4 and IPv6 are lost; the path for AWG masquerading
is determined differently; a tunnel with `listen_port` cannot work this way at all. A small batch
gives the same memory without these losses, and equally for client and server.

**D22. `send_by_mtu` is a public key, not an environment variable.** The owner's decision: any
user should be able to run the diagnostics, including in the Android app, where an environment
variable does not reach the core's code (§9.5). The part 1 change has no cost that
would justify turning it off in normal use, so the key is described as diagnostic and is on by
default.

**D23. Both keys are outside the `with_lx_idle_suspend` tag.** The tag enables idle suspend,
which makes sense only on mobile. Buffers are the same everywhere, and the main audience of
`read_datagram_count` is routers, where the tag is absent.

**D24. On macOS and Windows `read_datagram_count` is accepted and has no effect.** Batches there
are arranged differently or are absent. A load failure on these platforms would break one config
shared by several machines; the silent no-op is described in the documentation.

### 9.5 Why this is not a repeat of what was rejected in 020

In 2026-07, within [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/HISTORY.md), the experiment "coalescing
off, batch 8" was rejected for three reasons. Where each stands now:

| Reason for rejection | Now |
|---|---|
| **The wrong holder.** On Android the main memory holder turned out to be the shared slot pool, which does not depend on the batch; the buffers of the receive routines were secondary | The July profile called "pool" the allocation site shared by all 64 KiB slots. At that time it supplied the buffers of the read loop from the stack (128 of them, 8 MiB) and the buffer of every packet in the send and receive queues. Part 1 moved all of these to packet-sized buffers. The pool itself allocates nothing in advance: 4096 is a ceiling, not a reserve. By code, the part 1 change works in both directions on Android (§9.5.1), and the permanent holder that remains is the slots of the receive routines — 16 MiB per tunnel without `detour`. **Not measured on a device** |
| **The switch was not delivered.** An environment variable does not reach Go code in the Android app | A config key |
| **Panic at start.** The message set was cut to 8 together with the batch | The message set does not change (D20); a handshake longer than the read batch and the combination with a batching stack are covered by tests (§9.6) |

020 recorded the cause of the July panic as "the stack gave the device a batch of 128". For
Android this explanation is most likely wrong: the tunnel's stack there delivers one packet at a
time. The probable cause is the AmneziaWG handshake, which at tunnel start sends more than eight
datagrams in one call. The panic text was not saved at the time, so this is a conclusion from the
code, not a fact. It does not affect D20: both explanations lead to the same thing — do not
reduce the set.

#### 9.5.1 Android by code (2026-11-23)

The analysis was done by reading the code, without a build or a device.

- **Which layer.** A tunnel without `detour` gets the same direct-path layer as on Linux, with a
  batch of 128. There are two receive routines (IPv4 and IPv6); the third one, for per-interface
  sockets, does not exist on Android. A tunnel through `detour` reads one datagram at a time into
  one buffer.
- **Kernel coalescing.** Files with the `_linux` suffix are compiled for Android too, so UDP GRO
  is enabled on kernels 5.12 and newer. The note in the results of the 2026-07 experiment that the
  variant without coalescing is what works on Android is wrong.
- **The tunnel's stack.** The WireGuard device sees the gVisor stack, which reads and writes one
  packet at a time. The device batch size is therefore equal to the layer's batch: 128 without
  `detour`, 1 through `detour`.
- **The return path into TUN.** Android hands over the VpnService descriptor without the virtio
  header, GSO on the TUN inbound is not enabled there, and writes to TUN are not coalesced. The
  condition that keeps receive on slots on Linux with GSO (§2.4) never triggers on Android.
- **What holds memory per tunnel without `detour`:** at idle 16 MiB of receive-routine slots and
  0.25 MiB of buffers for reading from the stack; under load 2 KiB buffers are added — up to
  8 MiB on receive with a ceiling of 4096. 64 KiB slots beyond the permanent ones appear only in
  the handshake queue, for keepalive, for messages longer than 2 KiB and after a packet from the
  stack longer than the MTU.
- **What `read_datagram_count: 8` gives:** 16 MiB → 1 MiB per tunnel without `detour`, buffers
  for reading from the stack 0.25 MiB → 16 KiB. For tunnels through `detour` the setting changes
  nothing.

The 020 conclusion "the memory lever on Android is not in the batch" referred to the code before
part 1 and, by this analysis, does not apply to the current code: the batch is now the main
permanent holder. Idle suspend is not replaced by this — it frees a sleeping tunnel entirely,
while the setting reduces a working one as well. Until the measurement on a device (§9.6) this
remains a conclusion from the code.

### 9.6 Verification

Done.

Fork (`conn/lx_read_count_test.go`, device tests in `device/lx_packet_sized_test.go`; the full
`conn` and `device` suites on linux/arm64, `conn` and the new device tests on darwin/arm64,
`go vet` for linux and android, builds for windows and freebsd):

- the value range; a change on an open layer is rejected;
- the layer's batch size equals the setting on Linux and Android, 128 by default; on other
  platforms it does not change;
- with a small value the sockets are opened without UDP GRO, on every open, and coalescing on
  send stays the same as without the setting;
- a batch of 60 datagrams through a real socket with values 1, 8, 128 and without the setting:
  everything is delivered, intact and in order, no read returned more than promised;
- one send of 40 datagrams with the value 8: all arrived (a handshake longer than the read batch,
  a batch of injected packets);
- **the combination with a batching stack** (TUN declares a batch of 128, the setting is 8, real
  sockets): layer batch 8, device batch 128, traffic flows, no panic;
- the switch for buffers by MTU: the read buffer size becomes 64 KiB, a packet in the send queue
  holds 64 KiB again, packets arrive without loss.

The core: parsing and checking the block (1, 8, 128; 0, −1 and 129 — an error; default values
are normalized to an absent key, `send_by_mtu: false` is kept); the block is accepted by a build
without `with_lx_idle_suspend`; the value reaches the layer, and "not set" in any form leaves the
layer untouched.

The board (NanoPi), the finished build — §9.2, the third measurement. An AmneziaWG tunnel and a
tunnel with `listen_port` start and work with the key `8`.

Not done:

- **An Android device** (without it §9.5.1 remains a conclusion from the code): heap profile at
  idle and in a speed test; whether coalescing on receive is enabled on kernels 5.10 and
  5.15/6.1, without the setting and with it; download speed at `8` where coalescing is present;
  an AmneziaWG node with a long handshake; wake-up after idle suspend (020) and socket reopening
  on a network change with the setting; the key in the app's config generator;
- a real network card and speeds above 150 Mbit/s;
- a TUN inbound with direct L3 forwarding and the key `8` as part of the core (in the fork the
  combination with a batching stack is covered by a test).

### 9.7 Boundaries

- Permanent buffers on macOS and Windows do not change.
- A tunnel through `detour` is not affected: the batch there is already 1.
- The Tailscale endpoint runs on its own fork of `wireguard-go` and does not see these keys.
- The reason why sending on the direct path costs more than sending through `detour` (§6.1) is
  task [160](../160-WG_DIRECT_SEND_COST/SPEC.md).
- Android: the key's effect on memory and on speed without coalescing on receive has not been
  measured on a device (§9.5.1).
- What the carrier connections of a tunnel through `detour` and the tunnel's stack itself hold is
  not analysed here.

### 9.8 Files

Fork `wireguard-go` (branch `lx-awg2-v007`, [PR #7](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/7), commit `b565d0c`):

| File | What |
|---|---|
| `conn/lx_read_count.go` | new: the datagram count setting, the layer's batch size, turning off receive coalescing when the socket is created |
| `conn/lx_read_count_linux.go`, `conn/lx_read_count_default.go` | new: removing UDP GRO on Linux, a stub for the others |
| `conn/bind_std.go` | insertions: a field, the socket options function in two calls, the batch size |
| `device/lx_packet_sized.go` | a method that turns off read buffers by MTU |
| `conn/lx_read_count_test.go`, `device/lx_packet_sized_test.go`, `device/lx_backlog_stand_test.go` | tests and the stand |

The core: `option/lx.go` (the `buffers` block), `transport/wireguard/tuning_lx.go` and two
insertions in `transport/wireguard/endpoint.go`; the `submodules/wireguard-go` gitlink.
