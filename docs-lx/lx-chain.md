# sing-box-lx — the `chain` outbound: a hop chain of groups and nodes

> RU: [lx-chain.ru.md](lx-chain.ru.md) · Feature: [CHAIN](../SPECS/FEATURES/015-CHAIN/FEATURE.md)

The `chain` outbound builds a multi-hop path from existing nodes and groups: the entry, the middle and
the exit are each chosen by their own group, and the chain follows the choice without copying nodes.

The build tag `with_lx_chain` is in the desktop `LX_TAGS` and the AAR. Without it
`"type": "chain"` is rejected at load time. Details are in the
[CHAIN feature](../SPECS/FEATURES/015-CHAIN/FEATURE.md).

```json
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "idle_timeout": "5m",
  "strip_evasion": true,
  "strip": { "multiplex.padding": false, "tls.utls": true },
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

Positions in `outbounds` follow packet order. `outbounds[0]` is the first hop from the
client: it touches the real network and is used as is, dial fields included. The last entry
is the node whose address the destination sees. A `detour` chain reads the opposite way:
there the node carrying `detour` is the exit. Any position may be a node, an endpoint or a
group of any nesting. All three shapes behave the same, and the length is free (≥ 2):

```
["selector-in", "selector-mid", "selector-exit"]   // all groups
["node-in",     "node-mid",     "node-exit"]       // all nodes
["node-in",     "selector-mid", "node-exit"]       // mixed
```

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `outbounds` | list of tags, ≥ 2, all distinct | required | positions in packet order; a repeated tag is a start error — see below |
| `idle_timeout` | duration | `5m` | an idle link instance (see below) with zero live connections is removed after this; `0` = keep until stop |
| `strip_evasion` | bool | `true` | remove one-sided DPI tricks from links at positions ≥ 1 (catalog below) |
| `strip` | map key → bool | `{}` | patch over the catalog: `false` keep, `true` strip additionally; unknown key = start error |
| `rewrite` | map type → JSON object | `{}` | merge-patch (RFC 7396) applied to the config of every link of that type at positions ≥ 1 |

Tags in `outbounds` must be distinct: a repeat fails the start with
`duplicate outbound in chain`, because in a list read as a path it is almost always a typo.
To place the same node at several positions on purpose, declare it once per position under
different tags; the declarations may carry identical credentials. This is how the "sandwich"
`WARP → someone else's node → WARP` is built: the outer layers hide the middle node from your
address and the destination from the middle node. Each position gets its own link with its
own detour (a link is keyed by position and node), so the instances are independent.

Groups are never copied: the chain calls the original group and lets it pick with all its
logic (manual selection, health check, sticky, penalties, `interrupt_exist_connections`). A
node picked at position ≥ 1 works through its link, a runtime instance of that node that dials
its server through the previous position. Links are created on first use. At start, positions
whose pick is known are warmed up — nodes and selectors; urltest positions stay lazy. A link
shares the node's tag, so the group's history and penalties keep working. A link is removed
only when it has no live connections and was not picked for `idle_timeout`. Switching a group
does not kill the old link while a stream still runs through it. WireGuard links obey
idle-suspend like any endpoint.

- **`direct` at position ≥ 1 is transparent**: there is no hop. Put `direct` into a selector
  to switch a position off at runtime. `block` rejects. If every position ≥ 1 is `direct`, the
  chain equals position 0.
- **MTU and packet size of links are aligned automatically** by the shared `lx.mtu_align`
  mechanism ([lx-block.md](lx-block.md#lxmtu_align--path-mtu--packet-size-alignment)), the same one that works
  under a plain `detour`. `mtu` in the node's config means "as a standalone node". The chain
  subtracts the exact encapsulation overhead of IP tunnels *below* the link, taking the worst
  case over a group's members: WG inside an IP tunnel −60/−80 by the server address family,
  MASQUE −79/−99. QUIC links (hysteria2, tuic, hysteria) at positions ≥ 1 get an
  `initial_packet_size` that fits the hop below and `disable_path_mtu_discovery: true`. Over
  any proxy (vless/trojan/ss/hysteria2/…) nothing changes. The mode (`clamp` by default,
  `fill`, `off`) and `except` apply to links the same way.
- **`strip` catalog** — one-sided tricks the server never sees:
  - `tls.fragment` — packet-level ClientHello fragmentation and `fragment_fallback_delay`.
    The catalog does not touch `record_fragment`: protection of the path against rerouting
    by SNI is set by `lx.sniff_guard` (§9, §13);
  - `multiplex.padding`;
  - `xhttp.padding` — `x_padding_bytes` is pinned to the lower bound of the node's range, or
    to `100` when no range is set; placement and obfs mode are kept;
  - `tls.utls` — only via an explicit `"tls.utls": true`; on a node that uses `reality` it is
    a start error.
- Server contracts — `flow`, `obfs`, `shadowtls`, `plugin`, `udp_over_tcp`, `ech`, transport
  paths — are never stripped.
- Order of transformations for a link: `strip` → `rewrite` → MTU. All patches are dry-run at
  start against every node reachable at positions ≥ 1: a `rewrite` with an unknown field
  fails the start, not the first dial.

> ⚠️ By default the chain strips ClientHello fragmentation at positions ≥ 1, because the usual
> DPI sits between you and the first hop. With a domestic relay as position 0 and the DPI at
> the border (`["relay", "foreign-node"]`), the fragmentation is needed at position 1. For
> that, set `"strip": {"tls.fragment": false}`.

A connection's `detourList` ([§8](lx-observability.md)) shows the
resolved path. `GetChains` (CommandClient) and Clash API `/proxies/<tag>` → `chain` return the
per-position state and counters. The state of a position is the picked node, the link state
`starting|active|idle`, live connections, effective MTU and why, what was stripped or
rewritten, and the last error. Dial errors name the position and the hop below:
`chain[virtualisation] #2 (warp-exit) via #1 (node-m): …`. Per-layer latency is measured by
URLTest on the internal hop tags `<tag>#0`, `<tag>#1`, …; each tag measures the path up to its
position.

Known limits: a group at position ≥ 1 ranks its nodes by their *direct* measurements; a
tunnel above a hop without UDP fails at dial time, and the error names both positions; nested
`chain` is allowed only at position 0.

Two more examples.

A "sandwich" `WARP → foreign node → WARP`: one node on two positions is declared under two tags
with the same credentials.

```jsonc
{ "type": "chain", "tag": "sandwich", "outbounds": ["warp-in", "node-mid", "warp-out"] }
```

A home relay at position 0 with DPI at the border: ClientHello fragmentation is needed at
position 1, so it is not stripped.

```jsonc
{ "type": "chain", "tag": "via-home", "outbounds": ["relay", "foreign-node"],
  "strip": { "tls.fragment": false } }
```
