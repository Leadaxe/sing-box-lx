# sing-box-lx — the `lx` root block

> RU: [lx-block.ru.md](lx-block.ru.md)

The fork's global knobs live in one root block, `lx`, grouped by subsystem. Whatever describes a
single node stays in that node's object, as upstream does: AWG fields on the endpoint, `xhttp` in
`transport`, a masque node's own `idle_timeout`. Every sub-block and key is optional; an empty `lx`
is the same as none. A misspelt key is a load error, not a silent default.
The running config (`GetRunningConfig`) returns the `lx` block canonical: empty sub-blocks are dropped.

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
  "mtu_align": "clamp",
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
- [`lx.mtu_align` — path MTU / packet-size alignment](#lxmtu_align--path-mtu--packet-size-alignment)
- [`lx.sniff_guard` — guard against rerouting by SNI](#lxsniff_guard--guard-against-rerouting-by-sni)
- [Removed keys](#removed-keys)
- [Reserved: `lx.naive`](#reserved-lxnaive)

---

## `lx.wg` — WireGuard / AmneziaWG endpoints

The `idle` sub-block works only in the mobile build (`with_lx_idle_suspend`); the other `lx.wg`
keys work in every build.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `idle.suspend` | duration | off | Idle threshold for an endpoint outside the active routing tree: past it the device goes Down (sockets closed, receive buffers freed), the next dial wakes it. Absent or `0` turns the mechanism off; the idle tick does not start |
| `idle.suspend_reachable` | duration | off | Second, longer threshold for endpoints in the tree (urltest pool member, selected node, final); without it they never sleep. Must be ≥ `idle.suspend` and requires it |
| `idle.teardown` | duration, explicit `"0"` allowed | inherits `idle.suspend_reachable` | How long an already-sleeping endpoint sleeps before its device and netstack are torn down, counted from falling asleep. `"0"` disables teardown. Requires `idle.suspend` |
| `idle.lazy_build` | bool | `false` | Endpoints start torn down; the device is built on the first dial. Requires `idle.suspend` |
| `idle.build_max` | integer ≥ 0 | `0` = no cap | At most N devices built at once |
| `idle.build_overflow` | `wait` \| `build` | `wait` | When all `build_max` devices carry live connections: wait within the dial deadline, or build above the cap with a warning |
| `buffers.read_datagram_count` | integer 1–128 | `128`, as upstream | How many datagrams are reserved per socket read. `8` for weak machines. Linux and Android, tunnels without `detour` |
| `buffers.send_by_mtu` | bool | `true` | Size the buffers read from the stack by the tunnel MTU instead of 64 KiB. `false` is for diagnostics |
| `connected_socket` | bool | `false` | Connect the tunnel socket to the server address in single-peer mode. Linux and Android |

Timelines for the three idle keys and the recommended mobile configuration are in
[lx-energy.md](lx-energy.md). The victim order and the states for `lazy_build`, `build_max` and
`build_overflow` are in
[lx-energy.md §11](lx-energy.md#11-lazy-build-and-the-build-budget-keep-devices-from-being-built-for-nothing). How these three
keys behave:

- `lazy_build` does not apply to an endpoint with `listen_port` (nothing dials it).
- `build_max` without `lazy_build` is legal: devices are built at start, and the cap applies
  from the first rebuild (a wake after `idle.teardown`).
- The `wait` of `build_overflow` lasts until the dial's own deadline, at most 15 s.
- A `build_max` below the number of nodes used at the same time makes them tear each other down
  on every switch, each dial paying a device build. Set it for probe-heavy sessions, not as a
  general memory knob.

`buffers` and `connected_socket` are about the socket of a tunnel without `detour`; a tunnel
through `detour` has no socket of its own and is not affected.

- `buffers.read_datagram_count`: a tunnel keeps one 64 KiB buffer per datagram it may read at
  once, in each of its two receive routines (IPv4 and IPv6), for as long as it is up: 16.4 MiB
  at the default 128, about 1 MiB at 8. Below 128 the socket is created without kernel receive
  coalescing (UDP GRO); send coalescing (UDP GSO) stays. On a 4-core Cortex-A55 board between
  100 and 150 Mbit/s the value made no difference to speed or CPU. On a real NIC and on faster
  links the batch and GRO may matter, so the default stays as upstream. Accepted and ignored on
  macOS and Windows.
  Measured with 100 idle tunnels without `detour` in one instance (the same board):

  | `read_datagram_count` | Go heap | Physically used (RSS) |
  |---|---:|---:|
  | 128 (default) | 1740 MiB | 94 MiB |
  | 8 | 138 MiB | 77 MiB |
  | 2 | 58 MiB | 75 MiB |
  | 1 | 44 MiB | 74 MiB |

  An idle tunnel costs little physical memory at any value: a buffer that was never read into
  is not backed by pages. The heap still matters. It counts toward a runtime memory limit, it
  moves the garbage collection threshold (with 1.74 GiB live the next collection is at
  3.5 GiB), and a buffer becomes physical once the tunnel carries traffic. So the more
  tunnels, the smaller the value: `8` for a tunnel or two on a weak machine, `1` or `2` for
  tens and hundreds. Speed at `1` is not measured on the release build; a measurement build
  showed 3 % less upload than at `8`.
- `buffers.send_by_mtu: false` sets the buffers read from the tunnel stack to 64 KiB each, as in
  versions before lx.36. It exists to rule the MTU-sized buffers out when a problem is being
  narrowed down. There is no reason to set it otherwise.
- `connected_socket` applies to a tunnel with one peer, a fixed server address and no
  `listen_port`. The socket is connected to the server, so the kernel keeps the route instead
  of looking it up per datagram. On the same board that gives about 5 % more upload, 7 % together
  with `buffers.read_datagram_count: 8`; measured over loopback. The price: a connected socket
  hears only the server's address, and its source address is fixed at connect time. The core
  disconnects it whenever a send fails or a handshake goes unanswered, then connects again. A
  server that answers from a new address is heard after the next handshake retry, not at once.
  The key is off by default because it is not verified on a phone that changes networks. It
  applies on Linux and Android; macOS connects in this mode by itself.

The keys of `lx.wg.idle` act only in builds with `with_lx_idle_suspend` (the mobile AAR). A
desktop/CLI binary refuses them at start — any idle window, an explicit `idle.teardown` (`"0"`
included), `lazy_build`, a non-zero `build_max` or `build_overflow: "build"`:
`lx.wg.idle.* is set but this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend (mobile-only feature)`.
`lx.wg.buffers` and `lx.wg.connected_socket` never take part in that refusal.
`sing-box check` validates the keys but does not start the router, so it accepts them on desktop.

The canonical form drops defaults written out explicitly: `read_datagram_count: 128`,
`send_by_mtu: true`, `connected_socket: false`, and an empty `idle` or `buffers` block.

Validation errors name the full key path:

- `lx.wg.idle.suspend_reachable must be >= lx.wg.idle.suspend`
- `lx.wg.idle.suspend_reachable requires lx.wg.idle.suspend`, and the same for `idle.teardown`, `idle.lazy_build`
- `lx.wg.idle.<key> must be >= 0` for a negative duration, `lx.wg.idle.build_max must be >= 0`
- `lx.wg.idle.build_overflow must be "wait" or "build"`
- `lx.wg.buffers.read_datagram_count must be between 1 and 128` for `0`, a negative value or above 128
- `lx.wg.<old key> is moved to lx.wg.idle.<new key>` for a flat key of the old layout; the key mapping is in [Removed keys](#removed-keys)

## `lx.masque` — MASQUE outbounds

| Key | Type | Default | Meaning |
|---|---|---|---|
| `idle_timeout` | duration ≥ 0 | off | Global idle window for every `masque` node without its own `idle_timeout`: after it the session (netstack, pumps, QUIC keepalive) is taken down and the next dial builds it again |

Priority: the node's `idle_timeout` > `lx.masque.idle_timeout` > off. An explicit `"0"` on a node
keeps that node's tunnel up even when the global value is set. MASQUE builds its session lazily
on its own, so there is no cap here.

Validation error: `lx.masque.idle_timeout must be >= 0` for a negative duration.

## `lx.trusttunnel` — TrustTunnel outbounds

| Key | Type | Default | Meaning |
|---|---|---|---|
| `idle_timeout` | duration ≥ 0 | `5m` | Global idle window for every `trusttunnel` node without its own `idle_timeout`: after it the session is closed locally and the next dial opens it again. Absent or `0` means 5 minutes |

Priority: the node's `idle_timeout` > `lx.trusttunnel.idle_timeout` > 5 minutes. An explicit `"0"`
on a node disables teardown of its session whatever the global value is. Unlike `lx.masque`, a
global `0` here does not turn the window off, it means "unset": teardown can only be disabled on
the node. The fields of the outbound itself, its `idle_timeout` included, are in
[protocols-transports.md §6.2](protocols-transports.md#62-outbound-fields).

Validation error: `lx.trusttunnel.idle_timeout must be >= 0` for a negative duration.

## `lx.mtu_align` — path MTU / packet-size alignment

A node can sit right above an IP tunnel: via `detour`, via a group under `detour`, or as a
`chain` link ([§10](lx-config.md#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes)).
Such a node gets a packet size that fits the tunnel: `mtu` on tunnels (`wireguard`, `masque`),
`initial_packet_size` plus `disable_path_mtu_discovery: true` on QUIC proxies (`hysteria2`,
`tuic`, `hysteria`). The size is computed once at start from the config, before the nodes are
created, and the upstream constructors read the already-adjusted options. Values only go down;
every changed field is visible in the start log with its reason. The originating case is
issue #37: hysteria2 via `detour` over WARP worked against an IPv4 server and was silent against
an IPv6 one.

One key in the root `lx` block, two shapes:

```jsonc
"lx": { "mtu_align": "clamp" }                                        // string — the mode
"lx": { "mtu_align": { "mode": "clamp", "except": ["my-wg"] } }       // object — mode + exceptions
```

| Mode | Field not set | Set explicitly and above the limit | Set explicitly and fits |
|---|---|---|---|
| `off` | left alone | left alone | left alone |
| `fill` | limit is filled in | left alone; warning "does not fit under `<tag>`" | left alone |
| `clamp` (default) | limit is filled in | lowered to the limit; logged as configured → effective with the reason | left alone |

No mode ever raises a value. `except` lists node tags that are never touched in any mode. An
unknown mode is rejected when the config is parsed, an unknown tag later, when the instance is
built and every node is known. Without the key the mode is `clamp`. This is the `chain` contract
("`mtu` in the config = as standalone, the chain only lowers"), and it applies under a plain
`detour` as well.

Validation errors (`sing-box check` catches all three too):

- `lx.mtu_align.mode must be "off", "fill" or "clamp"` — an unknown mode, in the string and in
  the object form; at config parse
- `lx.mtu_align.except: empty tag` — an empty string in `except`
- `lx.mtu_align.except: unknown outbound/endpoint tag: <tags>` — the tag is neither an outbound
  nor an endpoint; checked before alignment, ahead of node creation

**Capacity of a node** — how many bytes of IP packet it carries for the node above it:

| Node below | Capacity |
|---|---|
| `masque`, `wireguard` (endpoint and legacy outbound), `openvpn-client`, `openconnect` | its effective `mtu` (after its own alignment, recursively downwards) |
| `tailscale` endpoint | `system_interface_mtu`, else 1280 |
| `selector`, `urltest` | min over all reachable members, recursively |
| `chain` as a `detour` target | unlimited + warning (a chain aligns itself) |
| any other node — `direct` and every proxy (vless, trojan, ss, socks, hysteria2, tuic, …), with or without `detour` | unlimited: a proxy delivers the packet of the node above whole (as a TCP stream, or by its own fragmentation), so whatever lies under its `detour` does not limit the node above |

Only tunnels and groups carry a capacity upwards. This rule leaves out one case: a tunnel over
`shadowsocks` or `socks` UDP whose own `detour` is a tunnel. Those pass datagrams through, and the
limit below them applies; set `mtu` on the upper tunnel by hand there.

**What the node above gets** (`ipudp` = 28 for an IPv4 server, 48 for IPv6; a domain or an
unparsed address counts as IPv6; a WG endpoint takes its family from `peers[0].address`):

| Node above | Field | Limit | Overhead v4 / v6 |
|---|---|---|---|
| `wireguard` | `mtu` | `capacity − 32 − s4 − ipudp` (`s4` = AmneziaWG transport padding, 0 for plain WG) | 60 / 80 (+ `s4`) |
| `masque` | `mtu` | `capacity − 51 − ipudp` | 79 / 99 |
| `hysteria2`, `tuic`, `hysteria` | `initial_packet_size` (+ `disable_path_mtu_discovery: true`) | `capacity − ipudp`, clamped 1200…1452 | — |
| `openvpn-client`, `openconnect` | — | not derived; one warning if there is capacity below the node | unknown |

**IPv6 floor.** A tunnel that carries IPv6 inside is never aligned below 1280. Such tunnels are a
`wireguard` endpoint with an IPv6 prefix in `address`, and `masque` always. gvisor silently refuses to bring
up IPv6 on a link with a smaller MTU, and a dead IPv6 inside the tunnel is worse than fragments. When
the path leaves less than 1280, the tunnel gets 1280 and a Warn line (`… is above the path: … leaves
1220 bytes, below the IPv6 minimum 1280; outer packets will be fragmented`). A tunnel with only IPv4
inside can be aligned below 1280; an explicit `mtu` below 1280 is never raised.

If a QUIC node's limit is below 1200 (a tunnel with `mtu` < 1248 under an IPv6 server), 1200 is set
with a warning: QUIC cannot go below its minimum. PMTUD is switched off together with the derived
`initial_packet_size`, because quic-go only knows how to search upwards to 1452: over IPv6 the
probes are lost with the fragments, over IPv4 they "pass" by fragmentation and every packet then
travels as two. A node with an explicit `initial_packet_size` is left alone entirely, PMTUD included.

**Example — default WARP** (`masque` with `mtu` 1280) and hysteria2 with a `detour` to it:

```jsonc
{ "type": "masque", "tag": "warp", "mtu": 1280, /* … */ },
{ "type": "hysteria2", "tag": "hy2", "server": "2001:db8::1", "detour": "warp", /* … */ }
```

`hy2` gets `initial_packet_size: 1232` (1280 − 48) and `disable_path_mtu_discovery: true`; with an
IPv4 server — 1252 (1280 − 28). Without alignment quic-go sends 1280-byte payloads, 1328 bytes over
IPv6. The tunnel splits them into fragments, and WARP drops those ([§4](lx-config.md#4-masque-outbound--cloudflare-warp)).

**Groups.** Under a group the min over all members is taken at start; switching the selector/urltest
does not recompute it. The QUIC packet size is fixed when the client is created, a WG device's MTU
when the device is created, and a live connection does not migrate to the new path. The cost on a
wide group member: up to 200 bytes per packet (1452 → 1252) and PMTUD off. If that cost matters,
there are three ways out: an explicit value in `fill` mode, the tag in `except`, or `off` mode.

**Start log** — one Info line per changed field, warnings at Warn:

```
mtu_align: hy2 initial_packet_size → 1232 (detour warp[masque] mtu 1280 − 48 ipv6; pmtud off)
mtu_align: wg-exit mtu 1420 → 1200 clamped (limited by warp[masque] mtu 1280 via sel −80 ipv6)
```

For `chain` the same decision shows in `ChainInfo` (`mtu_configured` / `mtu_effective` /
`mtu_reason` with the mode prefix `clamped:` / `filled:`). The lx RPC does not expose decisions
made for `detour`.

**Not aligned:**

- xhttp over h3 and naive (cronet) — they have their own QUIC transport;
- `openvpn-client` and `openconnect` as upper nodes — a warning only;
- the server → client direction — the server controls it.

What masque does from below (outer `InitialPacketSize` = `mtu + 51`, ICMP Packet Too Big with the
real budget) is in [§4](lx-config.md#4-masque-outbound--cloudflare-warp).

## `lx.sniff_guard` — guard against rerouting by SNI

A node between the client and the server can read the name out of the first handshake packet and
send the connection to that name instead of the dialled address. This is what an Xray inbound
with `sniffing.destOverride` and without `routeOnly` does. It shows up on a relay under `detour`
and on a router with a VPN or a transparent proxy in the local network. While the SNI is the
server's own name, the rerouting leads to the same place. When the SNI is a disguise (REALITY,
WARP, hysteria2 or tuic with a fake SNI, the AmneziaWG QUIC masquerade), the connection lands on
someone else's host and the node looks dead. The block makes the first handshake packet
unreadable to the sniffer and does not change what the server receives. From connection outcomes
the block learns which paths need this.

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
| `outbound_type` | a list of types, `false` | no filter | Only nodes of the listed types. `true`, an empty list and an unknown type are load errors; a known type that cannot be guarded is allowed and skipped |

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
carrier cannot be determined, the path is unknown: no protection, no label.

| Value | Meaning |
|---|---|
| `never` | Never any protection on this path: no label, no retries, node keys work as upstream |
| `clean` | Starts without protection |
| `suspect` | Starts with protection |

The `"*"` key covers every type not in the map. Groups cannot be keys. The map default: `direct`,
`ssh`, `socks` — `clean`; `"*"` — `suspect`. The `tls` or `quic` section can override single
types with its own `detour_type` map; the rest come from the shared one. `"socks": "never"` in
the `quic` section of the example comes from a field report: through `socks` with UDP-over-TCP
the decoy byte broke the tunnel even though there was no sniffer on the path.

**The `tls` and `quic` sections, `override`.**

| Key | Values | Absent | Meaning |
|---|---|---|---|
| `tls.method` | `record_fragment`, `fragment`, `delay`, `off` | `off` | Method for TLS over TCP (table below) |
| `quic.method` | `decoy_first_byte`, `delay`, `off` | `off` | Method for QUIC |
| `delay` (per section) | positive duration | `300ms` | Pause of that section's `delay` method. Against Xray it must exceed 200 ms; a smaller value is allowed for a sniffer that waits less. Zero and negative are load errors |
| `persist` (per section) | `true`, `false` | `false` | Keep labels of encrypted relays in `experimental.cache_file`. `true` without `cache_file` enabled is a load error |
| `detour_type` (per section) | a map as above | the shared map | Starting states overridden for that protocol |
| `override` (shared) | `true`, `false` | `false` | `false` — a node's `fragment` / `record_fragment` win over the block; a node with an explicit value follows it. `true` — for a node that passed `nodes`, the path label decides the ClientHello shape: the block's method on `suspect`, no split on `clean`, even if the node asked for a split. On a `never` path the block does nothing. QUIC has no node keys, `override` has no effect there |

The node keys `tls.fragment`, `tls.record_fragment` (an explicit `false` opts out of splitting)
and `tls.fragment_fallback_delay` work in any state of the block, as upstream.

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
or the tag of the nearest relay, the protocol is `tls` or `quic`. The key is the tag, not the
type: two VLESS relays with different sniffing settings are told apart. The label is shared by
all nodes on that path; TLS and QUIC do not affect each other. On `clean` a node goes without
protection, on `suspect` with it.

The label changes only on a pair of outcomes on one node:

- the node failed without protection, then passed with it → the path becomes `suspect`;
- the node failed with protection, then passed without it → the path becomes `clean`;
- any other outcome leaves the label alone. A dead node that fails both ways changes nothing, so
  a bulk test of many nodes, some of them dead, does not take protection off a path.

A fast error (not a timeout) is retried in the same dial with the opposite decision. A timeout
returns the error, and the opposite decision goes to the node's next dial on that path (within
10 minutes). On a `suspect` path an attempt without protection after a failure is allowed at most
once per 10 minutes per path; the rest of the time a failure on `suspect` leads to nothing.

Only attempts in which the handshake actually happened and the block's decision was applied go
into a pair. A dial that lands on an already open session (mux, the anytls pool, XHTTP, a long
QUIC session of masque, hysteria2, tuic, trusttunnel) and a node with its own choice under
`override: false` do not.

Only nodes whose SNI differs from `server` (per the table above) move the label, regardless of
the `sni_mismatch` filter. A node with its own name passes a sniffing relay unprotected anyway, so
its outcomes say nothing about the sniffer. Such a node is protected by the label only with
`sni_mismatch: false`. On a `clean` path it does not retry. On a `suspect` path, if it failed
with protection and passed without it, the node records a personal opt-out: from then on it goes
unprotected on that path, while the path label and the other nodes stay as they were. The reverse pair on that node
(failed without, passed with) clears the opt-out. Retries for such a node happen at most once per
10 minutes. The opt-out lives as long as the core instance and is not persisted.

Protocol specifics:

- **hysteria2, tuic** — the handshake runs inside the QUIC library; the outcome is judged by the
  connection's lifetime: closed less than 30 s after the dial is a failure, longer is a success.
  A success counts as soon as the connection has lived 30 s; a failure is learnt when the node
  makes its next new connection. There is no retry within the same dial; a pair forms through the
  next dial. With hysteria2 port hopping,
  the sockets opened by later hops carry no handshake and are not guarded; hysteria2 realm mode
  is not guarded at all.
- **masque `auto`** — the legs follow different labels: h3 the `quic` one, h2 the `tls` one. A
  fast-failing h3 is retried with the opposite decision before the h2 fallback. A silent h3 yields
  to h2 after 3 s without a handshake, and the opposite decision goes to the next h3 leg.
- **AmneziaWG** — a `wireguard` endpoint is covered when its masquerade is `ip: "quic"` or it has
  a non-empty `i1`; the byte goes only ahead of a QUIC-shaped burst. The decision is taken from
  the label when the socket is created. The handshake outcome does not move the label, so
  AmneziaWG never moves a path label. With no other QUIC node on the path the label stays at its
  start. For an AWG node on a path that starts `clean` (direct, `socks`) set protection
  explicitly: `suspect` for that carrier type in `quic.detour_type`.

Every label change and every personal opt-out set or cleared writes an info-level log line: path,
protocol, old and new state, the node, and the outcomes of both attempts. There is no separate
API.

**Cost of learning.**

- The first failure on a `clean` path is a timeout, not an error: the sniffer reroutes the
  connection to a name where nobody answers. The node's first dial hangs until its deadline, its
  first URL test fails, protection arrives from the second dial.
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

**Load errors.** All of them are caught by `sing-box check`. `<key>` is the full path of the key
in question, for example `lx.sniff_guard.quic.detour_type`.

| Cause | Message |
|---|---|
| Unknown method | `lx.sniff_guard.tls.method must be one of "record_fragment", "fragment", "delay", "off"`; `lx.sniff_guard.quic.method must be one of "decoy_first_byte", "delay", "off"` |
| `nodes.outbound_type: true` | `lx.sniff_guard.nodes.outbound_type: true is not a filter; list the types, or use false for no filter` |
| `nodes.outbound_type: []` | `lx.sniff_guard.nodes.outbound_type: empty list; list the types, or use false for no filter` |
| A type no outbound or endpoint of this build knows, in `nodes.outbound_type` or as a `detour_type` key | `<key>: unknown outbound type "<t>"` |
| A group in `nodes.outbound_type` | `lx.sniff_guard.nodes.outbound_type: "<t>" is a group; a group is never dialled itself, name the member types` |
| A group as a `detour_type` key | `<key>: "<t>" is a group; name the member types, a group is resolved to the member it picks` |
| An unknown value in a `detour_type` map | `<key>.<type> must be "never", "clean" or "suspect"` |
| A zero or negative `delay` | `<key>.delay must be > 0` (`<key>` is `lx.sniff_guard.tls` or `lx.sniff_guard.quic`) |
| `persist: true` without `experimental.cache_file` enabled | `lx.sniff_guard.tls.persist: requires experimental.cache_file to be enabled`; the same with `.quic` |
| An unknown key, or a value that is not an object | the message starts with the key: `lx.sniff_guard`, `lx.sniff_guard.nodes`, `lx.sniff_guard.tls`, `lx.sniff_guard.quic` |
| The removed `lx.quic_sniff_decoy` key | `lx.quic_sniff_decoy is removed: the QUIC sniff decoy is configured by lx.sniff_guard.quic` |

The unknown-type and `persist` checks run only when at least one section has a method; `persist`
is checked for the section that has one.

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

## Removed keys

A config with any of these keys fails to load.

| Key | Instead |
|---|---|
| `lx.quic_sniff_decoy` | `lx.sniff_guard.quic` with `"method": "decoy_first_byte"` |
| `lx.awg_masquerade` | nothing: the AmneziaWG burst goes on every path |
| `lx.wg.idle_suspend`, `idle_suspend_reachable`, `idle_teardown`, `lazy_build`, `build_max`, `build_overflow` | the same key in `lx.wg.idle`: `idle_suspend` → `idle.suspend`, `lazy_build` → `idle.lazy_build` |
| `route.lx_idle_suspend`, `lx_idle_suspend_reachable`, `lx_idle_teardown` | `lx.wg.idle.suspend`, `suspend_reachable`, `teardown` |
| `network` in a `masque` node | `vhttp` |

## Reserved: `lx.naive`

`single_engine` and `lazy_start` are reserved for SPEC 096. Until that lands, an `lx.naive`
sub-block is rejected like any unknown key.
