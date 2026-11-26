# sing-box-lx — the `lx` root block

> RU: [lx-block.ru.md](lx-block.ru.md)

The fork's global settings sit in one root block, `lx`, grouped by subsystem. Whatever belongs
to a single node stays in that node's object, as upstream does: AWG fields on the endpoint,
`xhttp` in `transport`, a masque node's own `idle_timeout` on the node. Every key is optional;
an empty `lx` is the same as none. A misspelt key is a load error, not a silent default.
`GetRunningConfig` returns the block without empty sub-blocks and without defaults written out
explicitly.

```jsonc
"lx": {
  "wg": {
    "idle": {
      "suspend": "30s",
      "suspend_reachable": "5m",
      "teardown": "10m",
      "lazy_build": true,
      "build_max": 4,
      "build_overflow": "wait"
    },
    "buffers": {
      "read_datagram_count": 8,
      "send_by_mtu": true
    },
    "connected_socket": true
  },
  "masque": {
    "idle_timeout": "5m"
  },
  "trusttunnel": {
    "idle_timeout": "5m"
  },
  "detour": {
    "mtu_align": "clamp",
    "strip": true
  },
  "sniff_guard": {
    "tls": { "method": "record_fragment" },
    "quic": { "method": "decoy_first_byte" }
  }
}
```

## Table of contents

- [`lx.wg` — WireGuard / AmneziaWG endpoints](#lxwg--wireguard--amneziawg-endpoints)
- [`lx.masque` — MASQUE outbounds](#lxmasque--masque-outbounds)
- [`lx.trusttunnel` — TrustTunnel outbounds](#lxtrusttunnel--trusttunnel-outbounds)
- [`lx.detour` — what the core does to a node under `detour`](#lxdetour--what-the-core-does-to-a-node-under-detour)
  - [`lx.detour.mtu_align` — path MTU / packet-size alignment](#lxdetourmtu_align--path-mtu--packet-size-alignment)
  - [`lx.detour.strip` — filters for a node without a kernel socket](#lxdetourstrip--filters-for-a-node-without-a-kernel-socket)
- [`lx.sniff_guard` — guard against rerouting by SNI](#lxsniff_guard--guard-against-rerouting-by-sni)
  - [Node TLS keys and the block](#node-tls-keys-and-the-block)
- [Removed keys](#removed-keys)

---

## `lx.wg` — WireGuard / AmneziaWG endpoints

The `idle` sub-block works only in the mobile build (`with_lx_idle_suspend`). A desktop binary
refuses to start if `idle` sets anything that takes effect: an idle window, an explicit
`teardown` (`"0"` included), `lazy_build`, a non-zero `build_max` or `build_overflow: "build"`.
`sing-box check` does not start the router and accepts such keys on desktop. `buffers` and
`connected_socket` work in every build.

| Key | Type | Default | What it sets |
|---|---|---|---|
| `idle.suspend` | duration | off | Idle threshold for an endpoint outside the active routing tree: past it the device goes Down (sockets closed, receive buffers freed), the next dial wakes it. Absent or `0` turns the mechanism off |
| `idle.suspend_reachable` | duration | off | Second, longer threshold for endpoints in the tree (urltest pool member, selected node, final); without it they never sleep. Not less than `idle.suspend`, requires it |
| `idle.teardown` | duration, explicit `"0"` allowed | as `idle.suspend_reachable` | How long an already-sleeping endpoint sleeps before its device and netstack are torn down, counted from falling asleep. `"0"` disables teardown. Requires `idle.suspend` |
| `idle.lazy_build` | bool | `false` | Endpoints start torn down; the device is built on the first dial. Requires `idle.suspend` |
| `idle.build_max` | integer ≥ 0 | `0` = no cap | At most N devices built at once |
| `idle.build_overflow` | `wait` \| `build` | `wait` | When all `build_max` devices carry live connections: wait within the dial deadline, or build above the cap with a warning |
| `buffers.read_datagram_count` | integer 1–128 | `128`, as upstream | How many datagrams are reserved per socket read. Linux and Android, tunnels without `detour` |
| `buffers.send_by_mtu` | bool | `true` | Size the buffers read from the stack by the tunnel MTU instead of 64 KiB. `false` is for diagnostics |
| `connected_socket` | bool | `false` | Connect the tunnel socket to the server address in single-peer mode. Linux and Android |

**Sleep and device builds.** Timelines for the idle keys and the recommended mobile configuration
are in [lx-energy.md](lx-energy.md), the victim order for `build_max` in
[lx-energy.md §11](lx-energy.md#11-lazy-build-and-the-build-budget-keep-devices-from-being-built-for-nothing).

- `lazy_build` does not apply to an endpoint with `listen_port`: nothing dials it.
- `build_max` without `lazy_build` is legal: devices are built at start, and the cap applies
  from the first rebuild after `idle.teardown`.
- The `wait` of `build_overflow` lasts until the dial's deadline, at most 15 s.
- A `build_max` below the number of nodes used at the same time makes them tear each other down
  on every switch, each dial paying a device build. It is a key for probe-heavy sessions, not a
  general memory saving.

**The tunnel socket.** `buffers` and `connected_socket` apply only to a tunnel without `detour`:
a tunnel through `detour` has no socket of its own.

- `buffers.read_datagram_count`. A tunnel keeps a 64 KiB buffer per datagram it may read at once,
  in each of its two receive routines (IPv4 and IPv6), for as long as it is up: 16.4 MiB at 128,
  about 1 MiB at 8. An idle tunnel takes almost no physical memory, but the Go heap grows: it
  counts toward the runtime memory limit, pushes garbage collection further out, and the buffers
  become physical with the first traffic. The more tunnels, the smaller the value: `8` for a
  tunnel or two on a weak machine, `1` or `2` for tens and hundreds; speed at `1` is not measured
  on the release build. Below 128 the socket is created without receive coalescing (UDP GRO);
  send coalescing (UDP GSO) stays. On fast links and a real NIC the batch and GRO may pay off, so
  the default stays as upstream. Accepted and ignored on macOS and Windows.
  [Measurements](../SPECS/TASKS/158-WG_QUEUED_PACKET_BUFFERS/SPEC.md).
- `buffers.send_by_mtu: false` brings back 64 KiB buffers read from the stack. It is for
  narrowing down a problem, to rule the MTU-sized buffers out.
- `connected_socket` applies to a tunnel with one peer, a fixed server address and no
  `listen_port`. The socket is connected to the server, so the kernel keeps the route instead of
  looking it up per datagram; on a weak board that is a few percent of upload
  ([measurements](../SPECS/TASKS/160-WG_DIRECT_SEND_COST/SPEC.md)). The price: a connected socket
  hears only the server's address, and its source address is fixed at connect time. The core
  reconnects the socket when a send fails and when a handshake goes unanswered, so a server that
  answers from a new address is heard after the next handshake retry, not at once. The key is
  off by default: it is not verified on a phone that changes networks. macOS always connects the
  socket in this mode.

**Load errors.**

| Message | Cause |
|---|---|
| `lx.wg.idle.* is set but this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend (mobile-only feature)` | An effective `idle` key in a desktop build |
| `lx.wg.idle.suspend_reachable must be >= lx.wg.idle.suspend` | `suspend_reachable` below `suspend` |
| `lx.wg.idle.<key> requires lx.wg.idle.suspend` | `suspend_reachable`, `teardown` or `lazy_build` without `suspend` |
| `lx.wg.idle.<key> must be >= 0` | A negative duration or `build_max` |
| `lx.wg.idle.build_overflow must be "wait" or "build"` | An unknown value |
| `lx.wg.buffers.read_datagram_count must be between 1 and 128` | `0`, a negative value or above 128 |
| `lx.wg.<old key> is moved to lx.wg.idle.<new key>` | A flat key of the old layout, see [Removed keys](#removed-keys) |

## `lx.masque` — MASQUE outbounds

| Key | Type | Default | What it sets |
|---|---|---|---|
| `idle_timeout` | duration ≥ 0 | off | Idle window for every `masque` node without its own `idle_timeout`: after it the session (netstack, pumps, QUIC keepalive) is taken down and the next dial builds it again |

```json
"lx": { "masque": { "idle_timeout": "5m" } }
```

Priority: the node's `idle_timeout`, then `lx.masque.idle_timeout`, otherwise off. An explicit
`"0"` on a node keeps its tunnel up whatever the global value is. A MASQUE session is built
lazily anyway, so there is no cap here.

**Load error:** `lx.masque.idle_timeout must be >= 0` — a negative duration.

## `lx.trusttunnel` — TrustTunnel outbounds

| Key | Type | Default | What it sets |
|---|---|---|---|
| `idle_timeout` | duration ≥ 0 | `5m` | Idle window for every `trusttunnel` node without its own `idle_timeout`: after it the session is closed locally and the next dial opens it again. Absent or `0` means 5 minutes |

```json
"lx": { "trusttunnel": { "idle_timeout": "10m" } }
```

Priority: the node's `idle_timeout`, then `lx.trusttunnel.idle_timeout`, otherwise 5 minutes. An
explicit `"0"` on a node disables teardown of its session. Unlike `lx.masque`, a global `0` here
means "unset", not "off": teardown can only be disabled on the node. The fields of the outbound
itself are in [protocols-transports.md §6.2](protocols-transports.md#62-outbound-fields).

**Load error:** `lx.trusttunnel.idle_timeout must be >= 0` — a negative duration.

## `lx.detour` — what the core does to a node under `detour`

A node that dials through `detour` — directly, through a group under `detour`, or as a node taken
over by a [`chain`](lx-chain.md) position — gets two things from the core: its packet size is
fitted to the tunnel below (`mtu_align`), and mechanisms that need a kernel socket are filtered
when there is none under it (`strip`). One `except_outbounds` list exempts nodes from both
([SPEC 165](../SPECS/TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md)).

```jsonc
"lx": {
  "detour": {
    "mtu_align": "clamp",
    "strip": {
      "filters": {
        "tls": { "fragment": true, "spoof": true, "kernel": true, "engine": true }
      }
    },
    "except_outbounds": ["foreign-node"]
  }
}
```

The block is `true` by default: no `detour` key is the same as `"detour": true`.

| Written | `mtu_align` | `strip` |
|---|---|---|
| no key / `true` | `clamp` | the whole catalog: all four filters |
| `false` | `off` | none — upstream behaviour |
| object | as written; no key → `clamp` | as written; no key → the whole catalog |

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `mtu_align` | `off`, `fill`, `clamp` | `clamp` | The alignment mode ([below](#lxdetourmtu_align--path-mtu--packet-size-alignment)) |
| `strip` | `true`, `false`, object `{filters}` | the catalog | `true` — the catalog; `false` — nothing; an object without `filters` — the catalog |
| `strip.filters` | `true`, `false`, object `{tls: {fragment, spoof, kernel, engine}}` | the catalog | `true` — the catalog; `false` — nothing; an object replaces the catalog whole: a filter it does not name is off, `{}` filters nothing |
| `except_outbounds` | list of tags | none | Nodes neither aligned nor filtered |

`GetEffectiveConfig` writes the block out expanded, every key and every filter explicit;
`GetRunningConfig` returns it as written.

### `lx.detour.mtu_align` — path MTU / packet-size alignment

A node can sit right above an IP tunnel: via `detour`, via a group under `detour`, or as a node on a
[`chain`](lx-chain.md) position. Such a node gets a packet size that fits the tunnel: `mtu` on tunnels
(`wireguard`, `masque`), `initial_packet_size` plus `disable_path_mtu_discovery: true` on QUIC
proxies (`hysteria2`, `tuic`, `hysteria`). Without it, for example, hysteria2 over WARP works
against an IPv4 server and is silent against an IPv6 one. The size is computed once at start
from the config, before the nodes are created. Values only go down; every changed field is
visible in the start log with its reason.

```jsonc
"lx": { "detour": { "mtu_align": "clamp", "except_outbounds": ["my-wg"] } }
```

| Mode | Field not set | Set explicitly and above the limit | Set explicitly and fits |
|---|---|---|---|
| `off` | left alone | left alone | left alone |
| `fill` | limit is filled in | left alone; warning "does not fit under `<tag>`" | left alone |
| `clamp` (default) | limit is filled in | lowered to the limit; logged as configured → effective with the reason | left alone |

No mode ever raises a value. A node in `except_outbounds` is never touched in any mode. Without
the key the mode is `clamp`, for chain positions and under a plain `detour` alike.

**Capacity of a node** — how many bytes of IP packet it carries for the node above it:

| Node below | Capacity |
|---|---|
| `masque`, `wireguard` (endpoint and legacy outbound), `openvpn-client`, `openconnect` | its effective `mtu` (after its own alignment, recursively downwards) |
| `tailscale` endpoint | `system_interface_mtu`, else 1280 |
| `selector`, `urltest` | min over all reachable members, recursively |
| `chain-hop` `<chain>#<i>` | min of its position and the hop below: the position may be off or pick `direct`, and the node above must fit in every state; hop 0 — its position |
| `chain` as a `detour` target | what its top hop carries |
| any other node — `direct` and every proxy (vless, trojan, ss, socks, hysteria2, tuic, …), with or without `detour` | unlimited: a proxy delivers the packet of the node above whole (as a TCP stream, or by its own fragmentation), so whatever lies under its `detour` does not limit the node above |

Only tunnels, groups and chain hops carry a capacity upwards. The rule leaves out one case: a
tunnel over `shadowsocks` or `socks` UDP whose own `detour` is a tunnel. Those pass datagrams
through, and the limit below them applies; set `mtu` on the upper tunnel by hand there.

**What the node above gets** (`ipudp` = 28 for an IPv4 server, 48 for IPv6; a domain or an
unparsed address counts as IPv6; a WG endpoint takes its family from `peers[0].address`):

| Node above | Field | Limit | Overhead v4 / v6 |
|---|---|---|---|
| `wireguard` | `mtu` | `capacity − 32 − s4 − ipudp` (`s4` = AmneziaWG transport padding, 0 for plain WG) | 60 / 80 (+ `s4`) |
| `masque` | `mtu` | `capacity − 51 − ipudp` | 79 / 99 |
| `hysteria2`, `tuic`, `hysteria` | `initial_packet_size` (+ `disable_path_mtu_discovery: true`) | `capacity − ipudp`, clamped 1200…1452 | — |
| `openvpn-client`, `openconnect` | — | not derived; one warning if there is capacity below the node | unknown |

**IPv6 floor.** A tunnel that carries IPv6 inside is never aligned below 1280: a `wireguard`
endpoint with an IPv6 prefix in `address`, and `masque` always. gvisor silently refuses to bring
up IPv6 on a link with a smaller MTU, and a dead IPv6 inside the tunnel is worse than fragments.
When the path leaves less than 1280, the tunnel gets 1280 and a Warn line (`… is above the path:
… leaves 1220 bytes, below the IPv6 minimum 1280; outer packets will be fragmented`). A tunnel
with only IPv4 inside can be aligned below 1280; an explicit `mtu` below 1280 is never raised.

If a QUIC node's limit is below 1200 (a tunnel with `mtu` < 1248 under an IPv6 server), 1200 is set
with a warning: QUIC cannot go below its minimum. PMTUD is switched off together with the derived
`initial_packet_size`: quic-go only searches upwards to 1452, over IPv6 the probes are lost with
the fragments, over IPv4 they "pass" by fragmentation and every packet then travels as two. A node
with an explicit `initial_packet_size` is left alone entirely, PMTUD included.

**Example — default WARP** (`masque` with `mtu` 1280) and hysteria2 with a `detour` to it:

```jsonc
{ "type": "masque", "tag": "warp", "mtu": 1280, /* … */ },
{ "type": "hysteria2", "tag": "hy2", "server": "2001:db8::1", "detour": "warp", /* … */ }
```

`hy2` gets `initial_packet_size: 1232` (1280 − 48) and `disable_path_mtu_discovery: true`; with an
IPv4 server — 1252 (1280 − 28). Without alignment quic-go sends 1280-byte payloads, 1328 bytes over
IPv6. The tunnel splits them into fragments, and WARP drops those ([masque-explained.md §4](masque-explained.md#4-packet-size)).

**Groups.** Under a group the min over all members is taken at start; switching the selector or
urltest does not recompute it. The QUIC packet size is fixed when the client is created, a WG
device's MTU when the device is created, and a live connection does not migrate to the new path.
The cost on a wide group member: up to 200 bytes per packet (1452 → 1252) and PMTUD off. If that
cost matters, there are three ways out: an explicit value in `fill` mode, the tag in
`except_outbounds`, or `off` mode.

**Start log** — one Info line per changed field, warnings at Warn:

```
mtu_align: hy2 initial_packet_size → 1232 (detour warp[masque] mtu 1280 − 48 ipv6; pmtud off)
mtu_align: wg-exit mtu 1420 → 1200 clamped (limited by warp[masque] mtu 1280 via sel −80 ipv6)
```

Every changed field is also a change record in `GetOutboundConfig(tag)`: `by.type` is
`mtu_align`, with the mode, the node below, the overhead and the same reason as the log line. A
QUIC node gets two records, `initial_packet_size` and `disable_path_mtu_discovery`
([lx-observability.md](lx-observability.md)).

**Not aligned:**

- xhttp over h3 and naive (cronet) — they have their own QUIC transport;
- `openvpn-client` and `openconnect` as upper nodes — a warning only;
- the server → client direction — the server controls it.

What masque does from below (outer `InitialPacketSize` = `mtu + 51`, ICMP Packet Too Big with the
real budget) is in [masque-explained.md §4](masque-explained.md#4-packet-size).

### `lx.detour.strip` — filters for a node without a kernel socket

Some TLS mechanisms of a node need the device's own TCP socket under the handshake. Through a
tunnel or a proxy the conn under the handshake is a stream inside another connection, and there
these mechanisms cost time or break the handshake. A filter keeps the node from using its
mechanism on such a handshake. On the path from the relay to the server none of them does
anything the filter takes away: the relay forwards only the real bytes.

| Filter | What is not applied without a kernel socket | Why |
|---|---|---|
| `tls.fragment` | the node's own ClientHello split (`fragment`, `fragment_fallback_delay`); `record_fragment` stays | the split cannot wait for its pieces to be acknowledged and sleeps `fragment_fallback_delay` (500 ms) between them: 0.5–1.5 s per handshake |
| `tls.spoof` | the forged ClientHello sent as a raw TCP packet | through ws, grpc, xhttp, httpupgrade, mux, QUIC or WireGuard it breaks every handshake; through vless/trojan over TLS it injects the decoy into the relay's stream |
| `tls.kernel` | kTLS (`kernel_tx`, `kernel_rx`): TLS stays in user space | breaks every handshake |
| `tls.engine` | the `apple` engine: the handshake goes through `go`, and the fingerprint becomes Go's | the macOS/iOS system TLS needs a socket and breaks the handshake |

- **Which nodes.** Any node with `detour`, taken-over chain nodes included, that is not in
  `except_outbounds`. A node without `detour` is never filtered.
- **Per handshake.** When the conn from `detour` is a kernel socket — `detour` to `direct`, or to a
  group that picks `direct` right now — the node's mechanisms work as configured. A node with
  `fragment` and `detour: S`, `S = [direct, wg-relay]`: while `S` picks `direct`, the ClientHello
  is split; after a switch to `wg-relay` the next handshake goes without the split and without the
  500 ms pauses.
- **`lx.sniff_guard` is stronger.** When the guard shapes this handshake itself, its shape applies
  and the filter does not cancel it. A split dropped by the filter is not the node's choice for
  the guard: a node without an explicit `record_fragment` can still get the guard's shape.
- **What shows.** The node's options do not change, and filters are not in `changes`. An applied
  filter writes one Info line per node and filter
  (`lx.detour: <tag>: strip tls.fragment: no kernel socket under detour`) and a `detour_strip`
  stage with the filter name in the connection test trace.
- **Filter off or `"detour": false`** — upstream behaviour: the mechanism is tried on a conn
  without a kernel socket and, apart from the split, fails.

To keep the node's split under `detour` and filter the rest:

```jsonc
"lx": { "detour": { "strip": { "filters": { "tls": { "spoof": true, "kernel": true, "engine": true } } } } }
```

### What `detour` does not apply

With a `detour` upstream builds the dialer of the node below and never the node's own
(`common/dialer/dialer.go`, `NewWithOptions`), and only the node's own dialer reads the socket
fields. Under `detour` these fields do nothing and say nothing; the core keeps it that way and
makes it visible: `GetOutboundConfig` shows each one in `changes` with `by.type: "lx.detour"` and
no `to`, and the effective view leaves it out. The node's options go to upstream as written.

`bind_interface`, `inet4_bind_address`, `inet6_bind_address`, `bind_address_no_port`,
`protect_path`, `routing_mark`, `reuse_addr`, `netns`, `connect_timeout`, `tcp_fast_open`,
`tcp_multi_path`, `disable_tcp_keep_alive`, `tcp_keep_alive`, `tcp_keep_alive_interval`,
`udp_fragment`, `network_strategy`, `network_type`, `fallback_network_type`, `fallback_delay`.

`domain_resolver` works under `detour`. Two exceptions stay as upstream has them: anytls fails
the load on `tcp_fast_open`, openconnect applies its keepalive fields itself. There is no key
for this: `"detour": false` and `except_outbounds` do not make these fields work.

### Load errors

`sing-box check` catches all of them too.

| Message | Cause |
|---|---|
| `lx.detour.mtu_align must be "off", "fill" or "clamp"` | An unknown mode |
| `lx.detour: unknown key <key>`; the same with `lx.detour.strip`, `lx.detour.strip.filters`, `lx.detour.strip.filters.tls` | An unknown key at that level. The keys of the old chain catalog (`tls.utls`, `multiplex.padding`, `xhttp.padding`) are unknown keys too |
| `<key> must be true, false or an object` | `lx.detour`, `strip`, `strip.filters` or `strip.filters.tls` of another type |
| `lx.detour.strip.filters.tls.<filter> must be true or false` | A filter value that is not a boolean |
| `lx.detour.except_outbounds must be a list of tags` | Neither a list nor a single tag |
| `lx.detour.except_outbounds: empty tag` | An empty string in `except_outbounds` |
| `lx.detour.except_outbounds: unknown outbound/endpoint tag: <tags>` | The tag is neither an outbound nor an endpoint |
| `lx.mtu_align is moved to lx.detour.mtu_align (lx.mtu_align.except is lx.detour.except_outbounds)` | The old root key `lx.mtu_align` |

## `lx.sniff_guard` — guard against rerouting by SNI

A node between the client and the server can read the name out of the first handshake packet and
send the connection to that name instead of the dialled address. This is what an Xray inbound
with `sniffing.destOverride` and without `routeOnly` does. It shows up on a relay under `detour`
and on a router with a VPN or a transparent proxy in the local network. While the SNI is the
server's own name, the rerouting leads to the same place. When the SNI is a disguise (REALITY,
WARP, hysteria2 or tuic with a fake SNI, the AmneziaWG QUIC masquerade), the connection lands on
someone else's host and the node looks dead: the handshake hangs and fails after 12–17 s, or a
foreign key arrives. The block makes the first handshake packet unreadable to the sniffer and
does not change what the server receives. From connection outcomes the block learns which paths
need this.

Without the block, as without a `method` in a section, there is no protection: the handshake goes
out as upstream sends it. A block in which neither section has a method is the same as no block.
The block does not turn itself on: protection defaults are up to the application that writes
the config.

```jsonc
"lx": {
  "sniff_guard": {
    "nodes": {
      "sni_mismatch": true,                 // absent = true; false — no filter
      "outbound_type": ["vless", "masque"]  // false | absent — no filter
    },
    "detour_type": {                        // no detour = "direct"
      "direct": "clean",
      "ssh":    "clean",
      "socks":  "clean",
      "*":      "suspect"
    },
    "override": false,
    "tls": {
      "method": "record_fragment",          // record_fragment | fragment | delay | off
      "delay": "300ms",
      "persist": true
    },
    "quic": {
      "method": "decoy_first_byte",         // decoy_first_byte | delay | off
      "detour_type": { "socks": "never" },
      "persist": true
    }
  }
}
```

**Which nodes — `nodes`.** Two filters; a node is guarded when it passes both. A node that does
not pass works as without the block. Without a `nodes` block `sni_mismatch: true` applies: a node
with its own name passes a sniffing relay unprotected anyway. `sni_mismatch: false` guards every
node that supports it.

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `sni_mismatch` | `true`, `false` | `true` | `true` — only nodes whose SNI differs from the `server` address; `false` — no filter |
| `outbound_type` | a list of types, `false` | no filter | Only nodes of the listed types. A known type that cannot be guarded is allowed and skipped |

Whether the SNI matches `server` is decided at load time by comparing strings, without DNS:
case-insensitive, trailing dot ignored, a subdomain is a different name. The name compared is the
one that actually goes into the ClientHello or Initial; with ECH, the outer, public one. The
outer name is taken only from an ECH config given inline in the node (`ech.config`); with
`ech.config_path` or ECH from DNS the plain `server_name` is compared.

| `server` | SNI | Result |
|---|---|---|
| domain | not set, or the same domain | match |
| domain | another name | mismatch |
| IP | a name | mismatch |
| IP | not set | match — nothing for a sniffer to read |
| any | no SNI sent (`disable_sni`) | match |
| hysteria2 with Salamander obfuscation | any | match — a sniffer cannot read the Initial |
| REALITY, masque (WARP), AWG QUIC masquerade | any | always mismatch |

A node behind a CDN (`server` is a CDN IP, SNI is its own domain) passes the filter without
needing to; the protection does not hurt it. A node whose name the relay's DNS resolves to a
different address does not pass `sni_mismatch`. Use `sni_mismatch: false` for it, narrowed with
`outbound_type` if needed.

**Starting state of a path — `detour_type`.** A map "carrier type → state the path starts in".
The carrier is the nearest non-group outbound the connection actually goes through: a group
(`selector`, `urltest`) resolves to its selected member. A node without `detour`, with `detour`
to `direct`, or to a group currently set to `direct`, is on a path of type `direct`. When the
carrier cannot be determined, there is no protection and no label.

| Value | Meaning |
|---|---|
| `never` | Never any protection on this path: no label, no retries, node keys work as upstream |
| `clean` | Starts without protection |
| `suspect` | Starts with protection |

The `"*"` key covers every type not in the map. Groups cannot be keys. The default map: `direct`,
`ssh`, `socks` — `clean`; `"*"` — `suspect`. Your map is laid over this one, so list only what
changes. The `tls` or `quic` section can override single types with its own `detour_type` map.
`"socks": "never"` in the `quic` section of the example comes from a field report: through
`socks` with UDP-over-TCP the decoy byte broke the tunnel even though there was no sniffer on the
path.

**The `tls` and `quic` sections, `override`.**

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `tls.method` | `record_fragment`, `fragment`, `delay`, `off` | `off` | Method for TLS over TCP (table below) |
| `quic.method` | `decoy_first_byte`, `delay`, `off` | `off` | Method for QUIC |
| `delay` (per section) | positive duration | `300ms` | Pause of that section's `delay` method. Against Xray it must exceed 200 ms; a smaller value is allowed for a sniffer that waits less |
| `persist` (per section) | `true`, `false` | `false` | Keep labels of encrypted relays in `experimental.cache_file`; requires `cache_file` enabled |
| `detour_type` (per section) | a map as above | the shared map | Starting states overridden for that protocol |
| `override` (shared) | `true`, `false` | `false` | `false` — a node's `fragment` / `record_fragment` win over the block. `true` — for a node that passed `nodes`, the path label decides the ClientHello shape, even if the node asked otherwise. More in [Node TLS keys and the block](#node-tls-keys-and-the-block) |

**Methods.** The server receives a valid handshake in every case. Traffic after the handshake is
untouched.

| Method | What goes on the wire | Against the Xray sniffer | Cost |
|---|---|---|---|
| `tls.record_fragment` | The ClientHello split into several TLS records, split points inside the SNI name | The sniffer parses only the first record, which ends mid-extensions — "not a ClientHello" on the first try | None |
| `tls.fragment` | The ClientHello as one record, in chunks over TCP segments, waiting for the ACK (on the device's own socket) or pausing (through a relay) | Protects while the pause between chunks exceeds 200 ms: through a relay that is `fragment_fallback_delay` (500 ms), and on the direct path behind a transparent proxy the core holds the same pause | `fragment_fallback_delay` pause through a relay and on the direct path behind a transparent proxy |
| `tls.delay` | The ClientHello as one record, in two chunks split inside the SNI, with a `delay` pause between them | The first chunk is incomplete; the sniffer waits 200 ms for the rest and gives up | `delay` pause on every guarded handshake |
| `quic.decoy_first_byte` | A one-byte datagram `0x00` ahead of the first Initial; the server drops it | The QUIC sniffer decides on the first datagram: "not QUIC" | None |
| `quic.delay` | The second Initial is held back by `delay` | The sniffer assembles the ClientHello from Initial packets within 200 ms and runs out of time | `delay` pause on every guarded handshake |

**Which nodes can be guarded.** TLS over TCP: vless, trojan, vmess, anytls, shadowtls, http with
TLS, masque `h2`, trusttunnel `h2`, XHTTP on `h2`. QUIC: masque `h3`, hysteria2, tuic,
trusttunnel with QUIC, XHTTP on `h3`, wireguard with the QUIC-shaped AmneziaWG masquerade.
Skipped silently: naive and nodes with a system TLS engine (`engine: apple`, `windows`).

| Method | Applies to | Does not apply to |
|---|---|---|
| `tls.record_fragment`, `tls.fragment`, `tls.delay` | every TLS node listed above | naive, system TLS engines |
| `quic.decoy_first_byte` | every QUIC node listed, AmneziaWG included | — |
| `quic.delay` | QUIC nodes whose ClientHello spans two Initial packets | AmneziaWG; a node whose ClientHello fits in one Initial; hysteria2 with Salamander obfuscation. On such a node the method does nothing |

A node the section's method does not apply to goes as without the block and does not move the
path label.

**How the path label is learned.** A label is kept per "path + protocol": the path is `direct`
or the tag of the nearest relay, the protocol is `tls` or `quic`. Two relays of one type with
different sniffing settings are told apart by tag. On `clean` the nodes of that path go without
protection, on `suspect` with it.

Only a pair of outcomes on one node changes the label:

- failed without protection, then passed with it → the path becomes `suspect`;
- failed with protection, then passed without it → the path becomes `clean`;
- nothing else touches the label. A dead node fails both ways and changes nothing, so a bulk
  test of nodes does not take protection off a path.

A fast error is retried in the same dial with the opposite decision. After a timeout the
opposite decision goes to the node's next dial on that path, if it comes within 10 minutes. On a
`suspect` path an attempt without protection after a failure is allowed at most once per
10 minutes per path.

Only real handshakes to which the block's decision was applied go into a pair. Not included: a
dial that lands on an already open session (mux, the anytls pool, XHTTP, a long QUIC session of
masque, hysteria2, tuic, trusttunnel), and a node with its own `fragment` / `record_fragment`
under `override: false`. A `fragment` dropped by the `lx.detour.strip` filter does not count as the
node's own.

Only nodes whose SNI differs from `server` move the label, even with the `sni_mismatch` filter
off: a node with its own name passes a sniffing relay anyway and says nothing about the sniffer.
Such a node is protected by the label only with `sni_mismatch: false`. On a `clean` path it does
not retry. If on a `suspect` path it failed with protection and passed without it, it records a
personal opt-out and from then on goes unprotected on that path; the path label and other nodes
do not change. The reverse pair clears the opt-out. Retries for such a node happen at most once
per 10 minutes. The opt-out lives as long as the core instance and is not persisted.

Protocol specifics:

- **hysteria2, tuic** — the handshake runs inside the QUIC library, so the outcome is judged by
  the connection's lifetime: closed within 30 s is a failure, longer is a success. A success
  counts as soon as the connection has lived 30 s, a failure on the node's next new connection.
  There is no retry within the same dial; a pair forms through the next dial. Sockets opened by
  later hops of hysteria2 port hopping carry no handshake and are not guarded; hysteria2 realm
  mode is not guarded.
- **masque `auto`** — h3 follows the `quic` label, h2 the `tls` one. A fast-failing h3 is retried
  with the opposite decision before the h2 fallback. A silent h3 yields to h2 after 3 s, and the
  opposite decision goes to the next h3 attempt.
- **AmneziaWG** — a `wireguard` endpoint is covered when its masquerade is `ip: "quic"` or it has
  a non-empty `i1`; the byte goes only ahead of a QUIC-shaped burst. The decision is taken from
  the label when the socket is created, and the handshake outcome does not move the label. So on
  a path that starts `clean` (direct, `socks`) set protection for AWG explicitly: `suspect` for
  that carrier type in `quic.detour_type`.

Every label change and every personal opt-out writes an info-level log line: path, protocol, old
and new state, the node, and the outcomes of both attempts. There is no separate API.

**Cost of learning.**

- The first failure on a `clean` path is a timeout: the sniffer reroutes the connection to where
  nobody answers. The node's first dial hangs until its deadline, its first URL test fails,
  protection arrives from the second dial.
- A node that fails fast costs two handshakes instead of one.
- A random glitch on a clean path (a failure without protection followed by a success with it)
  sets `suspect`, and only the reverse pair clears it. For `record_fragment` and
  `decoy_first_byte` this costs nothing; for `delay` and `fragment` it is a pause on every
  handshake, with `persist` across restarts too.

**Network change and `persist`.** A network change resets the labels of the `direct` path and of
unencrypted carriers (`socks`, `http` without TLS) to their start, together with the personal
opt-outs and pending retries on them. Labels of encrypted relays survive a network change. If a
different relay (type, address or port) shows up under the same tag, its labels and opt-outs are
reset. Without `persist` labels live as long as the core instance: a restart or applying a new
config starts from the starting states. With `persist` the labels of encrypted relays are written
to `experimental.cache_file` and loaded at start if the same relay is under the same tag. An
entry for a path that the current config's map declares `never` is not applied. Labels of
`direct` and unencrypted carriers are not stored.

**Limits.**

- A sniffer is not detected directly and not told apart from other causes of failure. The only
  sign is a pair of outcomes on one node, and a random glitch can form a false pair.
- A handshake that completes without an error in the wrong place counts as a success: `tuic` with
  `insecure` and an SNI that resolves to a live QUIC host. masque is protected by key pinning,
  hysteria2 by authentication, TLS nodes by certificate verification (without `insecure`).
- The full gRPC transport (`grpc`, not lite) is not shaped: its handshake runs outside the node's
  dial. Such a node goes as without the block.
- On the direct path shadowtls v2/v3 and any node with `tcp_fast_open` are not protected: the
  path cannot be classified. Through `detour` they are protected like the others.
- shadowtls v3 without uTLS is not protected on any path: its handshake bypasses the core TLS
  layer. With uTLS it is protected like the others.
- The block does not fix the relay; the fix on the relay side is `"routeOnly": true` in
  `sniffing`.
- Not tested against sniffers other than Xray. sing-box as a relay does not reroute by sniffing
  and needs no protection. The 200 ms wait is an Xray constant; for another sniffer tune `delay`.
- A DPI that drops a guarded handshake (treating record splitting as a signature) is not bypassed:
  the pair moves that path's label to `clean`.

**Load errors.** All of them are caught by `sing-box check`. `<key>` is the full path of the key
in question, for example `lx.sniff_guard.quic.detour_type`.

| Message | Cause |
|---|---|
| `lx.sniff_guard.tls.method must be one of "record_fragment", "fragment", "delay", "off"`; the same for `quic` with its methods | An unknown method |
| `lx.sniff_guard.nodes.outbound_type: true is not a filter; list the types, or use false for no filter` | `nodes.outbound_type: true` |
| `lx.sniff_guard.nodes.outbound_type: empty list; list the types, or use false for no filter` | `nodes.outbound_type: []` |
| `<key>: unknown outbound type "<t>"` | A type no outbound or endpoint of this build knows, in `nodes.outbound_type` or as a `detour_type` key |
| `lx.sniff_guard.nodes.outbound_type: "<t>" is a group; a group is never dialled itself, name the member types` | A group in `nodes.outbound_type` |
| `<key>: "<t>" is a group; name the member types, a group is resolved to the member it picks` | A group as a `detour_type` key |
| `<key>.<type> must be "never", "clean" or "suspect"` | An unknown value in a `detour_type` map |
| `<key>.delay must be > 0` (`<key>` is `lx.sniff_guard.tls` or `lx.sniff_guard.quic`) | A zero or negative `delay` |
| `lx.sniff_guard.tls.persist: requires experimental.cache_file to be enabled`; the same with `.quic` | `persist: true` without `experimental.cache_file` enabled |
| starts with the key: `lx.sniff_guard`, `lx.sniff_guard.nodes`, `lx.sniff_guard.tls`, `lx.sniff_guard.quic` | An unknown key, or a value that is not an object |
| `lx.quic_sniff_decoy is removed: the QUIC sniff decoy is configured by lx.sniff_guard.quic` | The removed `lx.quic_sniff_decoy` key |

The unknown-type and `persist` checks run only when at least one section has a method; `persist`
is checked for the section that has one.

### Node TLS keys and the block

The minimal block that guards TLS nodes:

```json
"lx": { "sniff_guard": { "tls": { "method": "record_fragment" } } }
```

With it, a path through any relay except `ssh` and `socks` starts with the split; the direct path
and a path through `ssh` or `socks` start without it, until a node on that path fails without the
split and passes with it. To start every relay with the split, add
`"detour_type": {"ssh": "suspect", "socks": "suspect"}`. To split nodes whose SNI matches
`server` as well — `"nodes": {"sni_mismatch": false}`.

Upstream node keys work in any state of the block:

- **`fragment` / `record_fragment` / `fragment_fallback_delay`** on a node work as upstream, with
  or without the block. Under a `detour` without a kernel socket below, the `tls.fragment` filter
  of [`lx.detour.strip`](#lxdetourstrip--filters-for-a-node-without-a-kernel-socket) drops the
  node's `fragment` by default; `record_fragment` stays, and a node without an explicit
  `record_fragment` is then shaped by the block as if it had no split. Otherwise the block
  overrides them only with `"override": true`: then, for a node that passed `nodes`, the block's method goes on `suspect`
  and an unsplit ClientHello on `clean`. On a `never` path the block does nothing. QUIC has no
  node keys, so `override` has no effect there.
- **`"record_fragment": false`** — the node asks for the ClientHello in one record. Under the
  block without `override` such a node goes unsplit on every path.
- **REALITY nodes** — `fragment` / `record_fragment` work on them too. In upstream the REALITY
  client builds its uTLS connection on the bare socket, and these keys are silently ignored
  there. The hybrid ClientHello is 1.5–1.9 KB, which is two TCP segments. Whether a split
  helps on a network that drops that first flight is a property of that network. The other
  option is `key_share` ([protocols-transports.md §5.2](protocols-transports.md#52-fingerprints-and-the-hybrid-key-share)).

## Removed keys

A config with any of these keys fails to load.

| Key | Instead |
|---|---|
| `lx.quic_sniff_decoy` | `lx.sniff_guard.quic` with `"method": "decoy_first_byte"` |
| `lx.awg_masquerade` | nothing: the AmneziaWG burst goes on every path |
| `lx.wg.idle_suspend`, `idle_suspend_reachable`, `idle_teardown`, `lazy_build`, `build_max`, `build_overflow` | the same key in `lx.wg.idle`: `idle_suspend` → `idle.suspend`, `lazy_build` → `idle.lazy_build` |
| `route.lx_idle_suspend`, `lx_idle_suspend_reachable`, `lx_idle_teardown` | `lx.wg.idle.suspend`, `suspend_reachable`, `teardown` |
| `network` in a `masque` node | `vhttp` |
| `lx.mtu_align` | `lx.detour.mtu_align`; `except` → `lx.detour.except_outbounds` |
| `idle_timeout` in a `chain` | nothing: a chain creates no link instances, a node on a position ≥ 1 dials through the hop below as itself |
| `strip_evasion`, `strip` in a `chain` | `lx.detour.strip` |
