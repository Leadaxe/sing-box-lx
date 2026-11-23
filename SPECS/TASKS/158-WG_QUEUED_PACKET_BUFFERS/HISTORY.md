# HISTORY: 158 — WG_QUEUED_PACKET_BUFFERS

> 🌐 Русская версия: **[HISTORY.ru.md](HISTORY.ru.md)** (auxiliary copy; this English file is primary). Both are edited together.

How the current solution was reached. The current state is in [SPEC.md](SPEC.md).

## 1. Where the task came from

On 2026-11-22 KeenProxy (ordo-systems, Keenetic routers running the sing-box-lx core) sent PR
[wireguard-go-awg2-lx#5](https://github.com/Leadaxe/wireguard-go-awg2-lx/pull/5): on a router at
300–400 Mbit/s the heap profile attributed about 300 MiB to the site where WireGuard's receive
buffers are allocated. Their change copied every received transport packet into a packet-sized
buffer.

The PR could be neither merged nor reworked in place:

- it was made from the default branch `lx`, while releases are built from `lx-awg2-v007` (the two
  lines diverged by 83 and 131 commits);
- the source fork belongs to an organization, and for such forks GitHub does not let a maintainer
  edit the branch.

KeenProxy also has a port of the change onto the release base (`392d8d3`); it is in their builds.
It lacks the guard for a TUN with batched writes that the PR itself had.

## 2. First rework: slot budget (PR #6, `ae1a651`, kept as branch `rx-slot-budget`)

A measurement on a Mac (M1 Pro, in-memory exchange, one worker) showed that copying every packet
costs +7.0 % per packet for WireGuard and +8.1 % for AWG. To avoid paying this cost when there is
no backlog, the first rework copied a packet only when a read batch (128 slots) was already
waiting in the device's queues. Memory with a backlog of 1026 packets: 66.2 MiB for the baseline,
4.2 MiB for KeenProxy's port, 11.9 MiB for the slot-budget variant; cost per packet +2.5 %.

An independent review found in the first variant a leak of the slot counter on platforms without
bounded pools (the queue of a removed peer went to the garbage collector without being flushed).
Fixed before publication.

What turned out to be wrong in this variant:

- **It fixes the smaller half.** The profile from KeenProxy's router, sent in reply to PR #6,
  showed that the largest spot is the 64 KiB buffers of the sing allocator (100 MiB), that is,
  send, which neither their change nor PR #6 touched.
- **The cost of the copy was measured on the wrong hardware.** On Cortex-A55 copy on read is not
  slower than the baseline (see SPEC §4.3), so the slot budget bought speed that does not exist
  on the target device class, and paid for it with 8 MiB and a counter that has to be balanced
  on every exit path.

## 3. Independent revision (2026-11-23)

Owner decision: only the pointer to the problem is taken from KeenProxy's port; the solution is
developed independently.

Tracing the packet path showed that send is built the same way as receive and that it has a
solution with no copying at all: sizing the TUN read buffer by MTU (SPEC §2.2).

For receive, three methods were compared in one binary (a mode switch; it did not go into the
release branch):

| Mode | Essence |
|---|---|
| slot | as in upstream: the slot travels with the packet |
| decrypt into a compact buffer | the receive routine attaches a compact buffer to the element; the decryption worker opens the message from the slot straight into it and returns the slot |
| copy on read | the receive routine copies the message into a compact buffer and keeps the slot |

Decrypting into a compact buffer looked better on paper: there is no extra pass over the data,
the AEAD itself plays the role of the copy. On the board it turned out to be the slowest, and in
memory use in the end-to-end test it was worse than copy on read (SPEC §4). The reason is that the
slot still leaves the receive routine and returns to the pool cold from another goroutine, and
the write goes into two different buffers.

The owner chose copy on read on 2026-11-23, based on two rounds of the end-to-end test; the third
round and the comparison of modes in one binary confirmed the choice.

## 4. What surfaced along the way

- **Loss of the first packet under AWG.** The TUN reader starts together with the device and is
  already blocked in a read, with a buffer sized without padding, when the first configuration
  with `s4` arrives. A 64 KiB buffer had slack to shift the packet; an MTU-sized buffer has none,
  and the first full-size packet was dropped. The stand caught this as "delivered 2050 of 2051".
  Now such a packet is moved to a larger buffer (SPEC §2.2); along with that, the former drop of
  injected packets on `s4` growth on the fly is gone.
- **Coalescing on the L3 return path.** The second independent review noticed that the core's
  device with an attached return path hands written packets to TUN with coalescing, although it
  declares one-packet writes itself. Hence the dynamic flag `MergesWrites` (SPEC §2.4). The live
  run of the release candidate showed its cost: on Linux with GSO on the TUN inbound the return
  path is attached immediately, and receive of the whole endpoint stays in slots.
- **Two benchmark hangs** with KeenProxy's port in the first series on the Mac (the pipeline
  idles, one packet is missing). In the new series on NanoPi the stand reports such stops itself;
  the outcome is in SPEC §6.

## 5. Part 2: buffer settings (2026-11-23)

After part 1, the permanent buffers of the receive routines, 16.4 MiB, remained in the heap of a
tunnel without `detour`. The batch-size measurement (SPEC §6.1) showed that on the board speed
does not depend on it, while memory does. The owner decided to make the number of buffers a
setting and to add a switch for the send buffer by MTU for diagnostics; the key names and the
`lx.wg.buffers` block are the owner's.

At first the settings were written up as a separate task 160 (`WG_BUFFER_SETTINGS`, commit
`66d027f14`). The same day the owner merged it into this one: receive and send buffers are one
problem, and it has one owning task. Number 160 is now taken by the task about the send cost on
the direct path.

What changed in the problem statement along the way:

- **Kernel coalescing is turned off only on receive.** The measurement switch in §6.1 turned off
  both receive and send with a small batch. For the setting, send was left on: it does not depend
  on the number of read buffers. The combination was measured again separately (SPEC §9.2).
- **Why the message set must not be reduced.** At first the cause was taken to be a stack with
  batched reads (that is how 020 recorded the 2026-07 panic). The code analysis for Android showed
  that the stack there delivers one packet at a time, and more than eight datagrams per call come
  from the AmneziaWG handshake. The decision stayed the same, the rationale was rewritten
  (SPEC D20).
- **The 020 conclusion "the memory lever on Android is not in the batch"** referred to the code
  before part 1: at that time the "pool" in the profile was the buffers that travelled with
  packets. After part 1 the read batch became the main permanent holder (SPEC §9.5.1). This has
  not been measured on a device.
