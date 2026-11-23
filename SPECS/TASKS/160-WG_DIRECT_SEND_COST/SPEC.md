# SPEC: 160 — WG_DIRECT_SEND_COST

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-11-23).

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | B (bugfix on top of upstream) + one setting `lx.wg.connected_socket` |
| Status | O (open) — the causes are found and measured, implementation has not started |
| Branch | `lx`; fork `Leadaxe/wireguard-go-awg2-lx`, branch `lx-awg2-v007` |
| Build tag | — (applies to any build) |
| Source | Measurement in [158](../158-WG_QUEUED_PACKET_BUFFERS/SPEC.md) §6.1: upload over a tunnel without `detour` is about 28 % slower and costlier in CPU than over the same tunnel through `detour` → `direct`. Analysis of 2026-11-23, raw data — `.work/spec158/send-cost-findings.md` and `.work/spec158/sendcost/` |
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

Not measured: download with the fix at the default value (128 with kernel coalescing on
receive). There receive keeps the full cleanup (§2.1), and the download gain is expected to be
smaller.

Measured in one round, not three: the connected socket without the first fix (126.0 Mbit/s
against 124.3 for the baseline — no effect is visible while the CPU is busy with cleanup) and
download (current build 89.5 Mbit/s, with the connected socket alone, without the first fix,
93.6 — within the spread).

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

- **Send:** clean up as many messages as were filled. If the send was retried without
  coalescing (the kernel refused UDP GSO), the larger of the two numbers is taken.
- **Receive without kernel coalescing:** prepare and clean up as many messages as buffers were
  passed, and read into a slice of the same length.
- **Receive with kernel coalescing (UDP GRO):** unchanged. The kernel reads into the tail of the
  set, parsing spreads the segments across the whole head, and "filled" here means the whole set.
  A lighter cleanup for this path (a full one only for the messages actually in use) is possible
  but not measured; it is not part of this task.

The logic of the layer does not change; the amount of cleanup does. Applies on all platforms
where a tunnel without `detour` goes through this layer: Linux, Android, macOS, Windows (there the
batch is 1, and all 128 were still cleaned up).

The layer file is upstream: insertions are marked `// lx:`, the counting lives in a separate lx
file.

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

The same logic for Linux and Android, behind a switch (§4):

- on the first send the socket of the matching family (IPv4 or IPv6) is connected to the peer
  address; after that datagrams go out without an address and without the source-address control
  message; send coalescing (UDP GSO) stays;
- if a send goes to a second address, the socket is disconnected, and the layer works as before
  for the rest of the socket's life;
- if the kernel reports that the local address or the network is gone, the layer returns a
  "new socket needed" error. On it the device reopens the socket — the same path as on macOS;
- a reopened socket is connected again on the first send;
- a tunnel with `listen_port`, with several peers, and a tunnel through `detour` are not
  affected.

### 3.3 What changes in behaviour

- **Receive only from the server address.** A connected socket does not accept datagrams from
  other addresses. Now, if the server replies from a new address, the client accepts the reply
  and remembers the address; with a connected socket the kernel drops such a reply. On macOS this
  is already the case.
- **The source address is fixed at connect time.** Now it is chosen per datagram, and a network
  change (Wi-Fi ↔ LTE) goes through without the layer taking part. With a connected socket a new
  socket is needed after a network change. It appears on a send error or, if there is no error,
  through self-recovery after failed handshakes (041). How long this takes on a phone is not
  measured.

Because of these two points the fix goes behind a switch and is off by default.

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

## 6. Verification

Not done — the list of what has to be done before status I.

Message cleanup:

- layer tests: send coalescing, parsing of coalesced data on receive, a send retry after the
  kernel refused coalescing — after the set is returned, no references to foreign buffers remain
  in it;
- `reserved` bytes (WARP) and AmneziaWG paddings arrive as before;
- NanoPi, one build: upload and download in three rounds against the current build; profile —
  cleanup under 1 %;
- builds for Windows and macOS, one live tunnel each.

Connected socket (key on):

- NanoPi: upload and download in three rounds, with and without the key;
- a second peer address: the socket is disconnected, traffic flows;
- loss of the local address: the layer asks for a new socket, the tunnel comes up;
- the socket is connected again after self-recovery (041) and after wake-up (020);
- a tunnel with `auto_detect_interface` (per-interface sockets send before this branch): the
  modes do not interfere with each other;
- AmneziaWG: junk and garbage packets before the handshake, a handshake to several candidate
  addresses;
- a node whose server is given by a domain name: whether single-peer mode turns on;
- **phone:** Wi-Fi ↔ LTE switch under traffic and while idle — time to recovery with and without
  the key; start and operation of the tunnel in the app.

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

Filled in during implementation.
