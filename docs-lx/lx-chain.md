# sing-box-lx — the `chain` outbound: a hop chain of groups and nodes

> RU: [lx-chain.ru.md](lx-chain.ru.md) · Feature: [CHAIN](../SPECS/FEATURES/015-CHAIN/FEATURE.md) · Task: [SPEC 165](../SPECS/TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md)

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
  "rewrite": { "wireguard": { "mtu": 1200 } },
  "interrupt_exist_connections": false
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
| `outbounds` | list of tags, ≥ 2, all distinct | required | positions in packet order; a repeated tag is a load error — see below |
| `rewrite` | map type → JSON object | `{}` | merge-patch ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)) applied to every node of that type taken over by a position ≥ 1; it cannot touch `detour` |
| `interrupt_exist_connections` | bool | `false` | on a position toggle, also cut the user connections through the chain; the connections the nodes keep through the hops are always cut |

`idle_timeout`, `strip_evasion` and `strip` are not chain keys: a config with any of them fails
to load and the error names the replacement. There are no link instances to keep alive, and
what the core strips from a node under `detour` is set in
[`lx.detour.strip`](lx-block.md#lxdetour--what-the-core-does-to-a-node-under-detour).

Tags in `outbounds` must be distinct: a repeat fails the load with
`chain <tag>: duplicate outbound <tag> (position <i>): one node or group has one path`, because in a list read as a path it is
almost always a typo. To place the same node at several positions on purpose, declare it once per
position under different tags; the declarations may carry identical credentials. This is how the
"sandwich" `WARP → someone else's node → WARP` is built: the outer layers hide the middle node
from your address and the destination from the middle node. Each declaration is a node of its
own with its own `detour`. A WireGuard key holds one session on the server, so two WireGuard
declarations need two keys.

## How the chain is built

The chain rewrites the config at load time, before any node is created:

- it appends the hops `<tag>#0 … <tag>#n−1` to the outbound list. A hop is an outbound of the
  internal type `chain-hop` with the fields `chain`, `position`, `outbound` (the tag of its
  position) and, from position 1 on, `below` (the hop of the position below). Hop `i` dials
  through position `i`; the chain itself dials through its top hop;
- every node reached on a position `i ≥ 1` gets `detour` = `<tag>#<i−1>`. Reached means the
  position itself if it is a node, or every leaf of the group there, at any depth, picked or not,
  except `direct` and `block`. The node's own `detour` is overridden;
- then `rewrite` is applied to every taken-over node by its type;
- position 0 is not rewritten: its nodes go their own way. Groups are not rewritten either, only
  their leaves; the group keeps all its logic (manual selection, health check, sticky,
  penalties, `interrupt_exist_connections`).

A node keeps one instance and one path. A route straight to a taken-over node goes the chain's
path too, and so does a manual check of the node from a UI. A group on position ≥ 1 checks the
very instances that carry its traffic: a node dead on the chain's path leaves the pool even if
it would work on its own `detour`. Upstream does not apply a node's socket options under
`detour`, so `bind_interface`, `routing_mark`, `tcp_fast_open` and the like of a taken-over node
do nothing, as under any `detour`; `GetOutboundConfig` shows them as `lx.detour` changes
([lx-block](lx-block.md#what-detour-does-not-apply)). `domain_resolver` keeps working.

Hops are ordinary outbounds: they show in outbound lists (`GetOutbounds`, Clash API) with the
type `chain-hop`, and UIs hide them by type. A config that declares the type `chain-hop` itself,
or uses a hop tag for its own outbound, fails to load.

**At runtime** hop `i` dials as follows:

| Position `i` | The dial goes |
|---|---|
| a node | through the node, which reaches its server through `<tag>#<i−1>` |
| a group | through the group; the member it picks goes its own `detour`, `<tag>#<i−1>` |
| `direct` on `i ≥ 1` — the position itself or the pick of a group there | to the hop below, `<tag>#<i−1>`: traffic does not bypass the positions below |
| disabled, `i ≥ 1` | to the hop below |
| disabled, `i = 0` | to the network through the core's direct dialer |
| `block` | refused |

If every position ≥ 1 is `direct`, the chain equals position 0. A `direct` in a selector on a
position switches that position off at runtime.

**Position toggle.** `SetChainPositionEnabled(chain, position, enabled)` switches one position off
or on without a reload; any combination is valid, and with every position off the chain goes
direct. The disabled set is stored in `experimental.cache_file` by position tag, so it survives a
restart and a config edit that moves positions. A toggle always cuts the connections the nodes
keep through the hops; user connections through the chain are cut only with
`interrupt_exist_connections: true`.

## What fails the load

`sing-box check` catches these too. The error names the chain, the position and the node.

- A rewrite conflict: a node or a group needed in two places — in two chains, when at least one
  of the places is a position ≥ 1 (the entry of one and a position of another too; an entry
  shared by two chains is fine) or on two positions ≥ 1 of one chain —
  through groups of any depth, every member counted. The error is
  `<tag> is needed on chain … position … and on chain … position …`. For two chains, copy the
  group and its nodes under other tags (WireGuard nodes need other keys).
- A nested `chain` above position 0, or a position that contains the chain itself.
- A node type that cannot dial through `detour`.
- A node on position ≥ 1 with a fixed local port: WireGuard `listen_port`, Tailscale
  `listen_port` / `relay_server_port`, OpenConnect `dtls_local_port`. Socket options are not an
  error: they do nothing under `detour`, as upstream has it.
- A ring — a `detour` cycle through a hop: `[A, Dir{A, X}]` (A on the entry and in the group
  above) or `[A, N]` where `A` has its own `detour: N`. Upstream's start-order check reports it:
  `circular outbound dependency` with the hop tags `<tag>#<i>`.
- A `rewrite` that touches `detour`, a `rewrite` value that is not an object, or a patch that
  leaves an unknown field in the node.

## MTU, strip and the change record

- **MTU and packet size** of taken-over nodes are aligned by
  [`lx.detour.mtu_align`](lx-block.md#lxdetourmtu_align--path-mtu--packet-size-alignment), the
  same mechanism as under a plain `detour`. `mtu` in the node's config means "as a standalone
  node". A hop carries `min(position, hop below)`: the position may be off or pick `direct`, and
  the node above fits in every state. A chain as a `detour` target carries what its top hop
  carries. Over any proxy (vless/trojan/ss/hysteria2/…) nothing changes. Mode and
  `except_outbounds` apply to taken-over nodes as to any other.
- **Strip filters.** The chain itself strips nothing. A taken-over node is a node with `detour`,
  so [`lx.detour.strip`](lx-block.md#lxdetour--what-the-core-does-to-a-node-under-detour)
  acts on it like on any other: per handshake, when the conn under it is not a kernel socket, the
  node does not split its ClientHello, send `tls.spoof`, turn on kTLS or use the `apple` engine.
  Server contracts — `flow`, `obfs`, `shadowtls`, `plugin`, `udp_over_tcp`, `ech`, transport
  paths — are never touched.
- **Order** for a taken-over node: `detour` → `rewrite` → MTU. Every changed field is a change
  record. `GetOutboundConfig(tag)` returns the node as written (`original`), as built
  (`effective`) and the list of `changes`, each with `by.type` `chain`, `chain_rewrite`, `lx.detour` or
  `mtu_align`; applied to `original` they give `effective`. `GetEffectiveConfig` returns the whole
  config as built, hops included ([lx-observability.md](lx-observability.md)).

> ⚠️ With the default `lx.detour`, a taken-over node over a proxy or tunnel position does not split
> its ClientHello. That suits the usual case, where the DPI sits between you and the first hop.
> With a domestic relay as position 0 and the DPI at the border (`["relay", "foreign-node"]`),
> the split is needed at position 1: put `foreign-node` into `lx.detour.except_outbounds`. That
> also turns MTU alignment off for that node. Through a relay the split waits
> `fragment_fallback_delay` (500 ms) between pieces.

## Observability

A connection's `detourList` ([§8](lx-observability.md)) shows the resolved path.
`GetChainStatus(tag)` (CommandClient, lxd gRPC) and Clash API `/proxies/<tag>` → `chain` return
the state by position: the position tag, whether it is a group, the node it resolves to now,
`transparent` (a `direct` on position ≥ 1), `disabled`, and the position's dial errors; plus the
chain's dial and error counters. Dial errors name the position and the hop below:
`chain[virtualisation] #2 (warp-exit) via #1 (node-m): …`. Per-layer latency is measured by
URLTest on the hop tags `<tag>#0`, `<tag>#1`, …; each tag measures the path up to its position.

Known limits: a tunnel above a hop without UDP fails at dial time, and the error names both
positions; a node or a group belongs to one chain position; nested `chain` is allowed only at
position 0.

Two more examples.

A "sandwich" `WARP → foreign node → WARP`: one node on two positions is declared under two tags
with the same credentials.

```jsonc
{ "type": "chain", "tag": "sandwich", "outbounds": ["warp-in", "node-mid", "warp-out"] }
```

A home relay at position 0 with DPI at the border: ClientHello fragmentation is needed at
position 1, so the exit node is excepted from `lx.detour`.

```jsonc
{ "type": "chain", "tag": "via-home", "outbounds": ["relay", "foreign-node"] }
"lx": { "detour": { "except_outbounds": ["foreign-node"] } }
```
