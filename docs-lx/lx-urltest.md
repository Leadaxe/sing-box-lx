# sing-box-lx — urltest node selection modes

> RU: [lx-urltest.ru.md](lx-urltest.ru.md) · Feature: [URLTEST_BALANCE](../SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md)

Upstream `urltest` always selects the lowest-delay node. sing-box-lx adds two more modes:

| `mode` | Selection | Switches | Checked each `interval` |
|---|---|---|---|
| `least_test` (default) | the fastest node | when another one is faster by `tolerance` or a connection through the current one fails to open | every node |
| `round_robin` | a pool of live nodes | connections are spread over the pool | only the pool |
| `failover` | the fastest node at selection time | only when the current one fails or gets twice as slow as the others; the next one is again the fastest | only the current node |

The `url`, `interval`, `idle_timeout` and `interrupt_exist_connections` fields work as upstream.
Pick a probe `url` that the nodes do not block: as upstream, the group treats a node that does not
pass the probe address, by a refusal or by silence, as dead, even if other traffic goes through it.
The `mode` and `balancer` fields work in every build; the pool state for a UI (`GetPool`,
[§8](lx-observability.md)) is in a build with `with_lx_command`.

## Table of contents

- [1. Common fields](#1-common-fields)
- [2. least_test — fastest node](#2-least_test--fastest-node)
- [3. round_robin — pool with stickiness](#3-round_robin--pool-with-stickiness)
  - [`balancer` fields](#balancer-fields)
  - [Slot-hash binding](#slot-hash-binding)
  - [Pool self-heal](#pool-self-heal)
  - [Example — urltest with round_robin](#example--urltest-with-round_robin)
- [4. failover — hold until it fails](#4-failover--hold-until-it-fails)
  - [Example — urltest with failover](#example--urltest-with-failover)
- [5. Choosing a mode](#5-choosing-a-mode)

---

## 1. Common fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `mode` | string | `least_test` | `least_test` — the fastest node, as upstream; `round_robin` — a pool; `failover` — hold the working node until it fails. `least_connection` is a load error: `round_robin` already spreads connections evenly |
| `balancer` | object | — | `round_robin` parameters; with another `mode` it is a startup error. The fields are in [§3](#3-round_robin--pool-with-stickiness) |

## 2. least_test — fastest node

Every `interval` the group checks every node and moves to another one as soon as it is faster
than the current one by more than `tolerance` ms (default `50`). Upstream and a group without
`mode` work the same way.

The fork adds a reaction to failures. If a connection through a node fails with a "path is
dead" error — a timeout, an unreachable host, a refused connection to a proxy node — the node
gets a penalty, and the connection opens once through the best of the other nodes. If that
works, the group moves to that node, and open connections stay where they are. The penalty is
lifted when the node answers again. For `direct` and tunnels (WireGuard, AWG, Tailscale) a
refused connection counts as the site's answer and gives no penalty.

## 3. round_robin — pool with stickiness

`round_robin` spreads connections over a pool of several live nodes. Each `interval` only the
pool is checked, not every node, so the mode suits lists of hundreds of nodes. A node is picked
once per connection; a UDP/QUIC session stays on its node. The upstream `tolerance` field does
not apply here; `pool_tolerance` takes its place. If `tolerance` is set and `pool_tolerance` is
not, a startup warning says so.

### `balancer` fields

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `pool` | int | `3` | how many nodes the pool holds. `0` or omitted — `3`; negative is an error. The pool holds no more nodes than `outbounds` has |
| `pool_tolerance` | int (ms) | `0` | `0` — the pool is filled with live nodes without comparing delays, and the check stops once it has found `pool` live nodes plus a reserve of `max(⌈pool/2⌉, 1)`; the mode for large lists. `> 0` — every node is checked and the pool keeps the fastest `pool`; a pool node is replaced only when an outside node beats it by more than `pool_tolerance` ms |
| `sticky_hash` | string[] | `["process","domain"]` | what the binding key is built from (see [Slot-hash binding](#slot-hash-binding)): `process`, `domain`, `source_ip`, `dest_ip`, `dest_port`. Omitted or `[]` — the default. Only `["none"]` turns the binding off |

> ⚠️ `"sticky_hash": []` does not turn the binding off. The sing-box config decoder reads an
> empty array as an omitted field, that is, as `["process","domain"]`. `["none"]` turns the
> binding off; `none` together with another component is an error.

The number of slots does not change: a dead node's slot stays empty until a live replacement is
found, and the live nodes stay in their slots. While the pool has a live node, new connections
skip the empty slot: its sticky keys spread evenly over the live nodes and come back once the
slot is filled again; without the binding the rotation runs over the live nodes. A quarantined
node is skipped the same way. A failed connection starts
the [self-heal](#pool-self-heal).

A node is live or dead by the shared history of its checks. So a manual delay test with a short
timeout that does not complete (Clash API `/proxies/{name}/delay`, `URLTestOutbound`) and a
failed connection of a `least_test` group through the same node also take it out of rotation
until the next pool check (at most `interval` plus the probe time).

### Slot-hash binding

A flow's key is bound not to a node but to a slot number: `slot[hash(key) % pool]`. When the
node in a slot changes, the replacement takes the same slot. So the keys of the other slots stay
on their nodes, and their connections are not reopened. With `pool_tolerance > 0` the scheduled
check can swap a slot's live node for a faster one — that is the point of the mode; the slot's
keys move to the new node.

By default the key is the process and the destination domain. `domain` is the domain from the
original request, and it stays filled after the router resolves the domain to an IP. Keep
`domain` in the key for domain traffic: `dest_ip` is empty until the resolve, so a key of only
`source_ip`/`dest_ip`/`dest_port` puts every such flow of one source on one slot.

### Pool self-heal

The pool recovers from a node failure without waiting for `interval`.

The scheduled check walks the nodes once per `interval`, and on a large list that is a long
process: two checks are often 10–15 minutes apart. That is too rare for live traffic: a node that
dies right after a check would keep its share of new connections until the next one. So a
`round_robin` group has recovery mechanisms driven by the connections themselves. A failed node
does not drop connections: one that does not open through the dead node opens through the next
one, and the node is replaced with a live one without waiting for the check. A site closed to one
node moves to another node of the pool, while the rest of the traffic stays on its nodes by
stickiness.

One error decides nothing: silence, a node's refusal or a client leaving give only a retry
through another slot, a probe of the node and a count. A node is struck out only when it stayed
silent on its own probe while a control node in the same check answered. If nobody answered,
there is no conclusion and the pool does not change. After a strike-out the slot stays empty until
there is a replacement, and only a node that answered its own probe takes it. The replacement is
looked for among the nodes outside the pool; the live pool nodes are left alone.

With `pool_tolerance: 0` the scheduled check looks for live nodes not only for the pool but also
for a reserve of `max(⌈pool/2⌉, 1)` nodes. Reserve nodes carry no traffic, but they are taken
first for a replacement, so a slot is usually filled within a fraction of a second.

While nobody answers, checks get rarer. If every candidate has been asked and none answered, the
pause between checks grows: 15 → 30 → 60 → 120 s. Any probe answer or platform signal (a wake, a
network change) brings it back to 15 s, and a connection opened during a long pause gives one
extra check. The whole core runs at most 16 self-heal probes at once, and a node with the same
probe URL is checked with one probe for all groups.

If `round_robin` groups are nested (one is a node's `detour` or a member of another), only the
outer group retries a connection, so retries do not multiply.

There is nothing to configure: the behaviour is always on, with no config keys.

#### What the application sees when a node fails

| What happened | What the group does | What the application sees |
|---|---|---|
| A pool node died between checks: the server process stopped, the host went down | A connection that does not open through the node is retried through another slot, and the node gets a probe. Silent while the control answered — it is struck out, in under a second on a connection refusal; its slot gets a replacement | No failed connection. If the host is down rather than the process, the first connection waits for the failure and goes to the retry; from the fifth second of waiting new connections skip the slot |
| A node accepts connections and stays silent | After 5 s new connections skip its slot, and the node gets a probe. About 41 s after the silence began the node is struck out and replaced. Connections still opening through it are retried through another node; open connections still waiting for a first byte are closed | Connections that reached the node in its first 5 s are lost; the application repeats a closed one |
| A destination is unreachable through one node: a geo-restriction, the site bans the node's address | An explicit refusal from the node or the site — the connection is retried through another slot at once, and the destination moves to the next slot. A close without a code or a site silent for over 15 s — the destination moves on the second such case on the same node, if the node answered other sites in between. A SOCKS "general failure" reply and a client leaving before the answer do not move the destination. Other destinations stay on their nodes | On an explicit refusal — nothing; otherwise the first request can fail (see the limits), the next ones go through another node |
| A node answers probes but fails a noticeable share of connections | A node with at least 5 % failures and more than twice the pool median goes to quarantine: its slot is skipped and gets a replacement until the next scheduled check or network change. A node struck out twice between scheduled checks goes to quarantine the same way. The scheduled check always probes quarantined nodes | Fewer failed connections |
| The local network is gone: device sleep, a Wi-Fi or mobile network switch, an ISP outage | The group does not conclude "the network is down" from probes: when nobody answers, nobody is struck out. A wake, a network change or a clock jump over 10 s freezes the group for 15 s: the retry through another slot works, there are no conclusions and no records | The same pool once the network is back |

Where it shows:

- a pool with a rare check (`interval` of 10 minutes or more) — a server that goes down between
  checks does not take its share of connections until the next check, it is replaced at once;
- a site closed to one node (by geography, for example) opens through another and keeps that
  choice;
- phones and laptops — sleep and network switches do not lose the pool.

Limits:

- Only `mode: round_robin` recovers this way. `least_test` and `failover` keep their penalty and
  fallback dial on "path is dead" errors ([§2](#2-least_test--fastest-node)).
- The first request to a destination closed to a node without an explicit refusal can fail.
  Through VLESS, Trojan, VMess and Shadowsocks such a failure shows only after the connection has
  opened, so the group does not retry it; the application does, and the destination moves to
  another node from the second failure.
- On nodes that close connections en masse without an answer, destinations occasionally move
  once too often.
- A node that takes longer than 15 s on half of its probes is struck out as silent.
- If the reserve is struck out or empty, a large group with a small pool can take minutes to find
  a replacement.
- A destination stays on its new slot until the core restarts. The table holds 1024 keys, and the
  oldest drops out when it is full. A destination closed through every pool node returns to its
  own slot until the next check or network change.
- With stickiness off (`sticky_hash: ["none"]`) destinations do not move; dead and quarantined
  nodes are still skipped.
- UDP is covered in part: a flow that fails to open is retried and a dead node is struck out for
  UDP too, but UDP failures move no keys and do not count toward quarantine.
- A network change the platform does not report (Windows Modern Standby included) can strike out
  a live node once. It comes back by itself at the next replacement or check.
- Each connection costs a little extra work when it opens. Download and upload speed stay at the
  level of a build without self-heal; the difference shows only in a synthetic run of thousands
  of very short connections in a row ([measurements](../SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/IMPLEMENTATION_REPORT.md)).

`sing-box api group pool <tag>` (a build with `with_lx_command`) shows the state: a slot table
with `STATE` (`live`/`dead`/`quarantined`), `CONNECTIONS` and `FAILURES`, plus the moved keys, the
address problems and the freeze:

- `quarantined` — the node fails connections more often than the rest or was struck out twice; new connections skip it
  until the next check or network change;
- a moved key — a site closed to its node has moved to the next pool node;
- an address problem — the site did not open through any pool node and went back to its own node
  until the next check or network change;
- the freeze — for 15 s after the device wakes, the network changes or the clock jumps, the group
  draws no conclusions; the retry through another slot works.

The daemon's `GetPool` carries the same: per slot `quarantined`, `connections` and
`confirmedFailures`, per group `shiftedKeys`, `markedKeys`, `frozen` and `frozenSeconds`. In
libbox the group fields come with `GetPoolState` ([§8](lx-observability.md)).
The connection list shows the node each connection went through, including a retry. The full
rules are in [SPEC 138](../SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/SPEC.md), the exact
definitions and constants in [ALGORITHM.md](../SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/ALGORITHM.md).

### Example — urltest with round_robin

```jsonc
{
  "type": "urltest",
  "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "interval": "15m",
  "mode": "round_robin",
  "balancer": {
    "pool": 3,
    "pool_tolerance": 0,
    "sticky_hash": ["process", "domain"]   // ["none"] to disable stickiness
  }
}
```

For a large node list, `pool_tolerance: 0`, a small `pool` and a longer `interval` give the
fewest checks.

Status: even rotation is verified locally (10/10/10 with stickiness off) and on a device, on a
real multi-node pool. Per-domain uniformity on the device is 0.95+.

📖 [Field reference](../docs/configuration/outbound/urltest.md): every field and the binding key
components.

## 4. failover — hold until it fails

**Selection.** The group takes the fastest node at the moment of choice, without `tolerance`;
the order of the `outbounds` list does not matter.

**Hold.** While the node works the group stays on it, even if another one becomes faster. Each
`interval` only that node is checked: one probe instead of checking every node.

**Degradation.** If the node answers more than 2× slower than the fastest other node of the same
network (by its last known delay), it is checked once more at once. If it is still more than 2×
slower, the group checks every node and moves to the fastest only if that node is more than
1.5× faster; otherwise it stays: the network got slower, not the node. No config key.

**Failure.** Two kinds:

- the node does not answer a probe → the group checks every node and moves to the fastest live
  one;
- a connection through the node fails with a "path is dead" error → a penalty and one fallback
  connection, as in [least_test](#2-least_test--fastest-node); the group moves to the fallback
  node, and open connections stay where they are.

After the move the group holds the new node.

**Manual test.** A group test from the UI, CLI (`sing-box api group urltest <tag>`) or Clash API
checks every node and picks the fastest. This is the only way back to the fastest node while the
current one is alive.

**What does not apply.** `tolerance` is ignored, and a startup warning says so. `balancer` is a
startup error.

The UI shows delays of the other nodes as of the last check of every node.

### Example — urltest with failover

```jsonc
{
  "type": "urltest",
  "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "url": "https://www.gstatic.com/generate_204",
  "interval": "15m",
  "mode": "failover"
  // no tolerance: there are no speed-based switches (a non-zero value logs a warning)
  // no balancer: with failover it is a startup error
}
```

When to pick it: holding one working node matters more than always being on the fastest one —
mobile profiles, for example. How many probes this saves is in [lx-energy.md §6](lx-energy.md#6-urltest-probes-wake-sleepers-so-five-mechanisms-bound-them).

## 5. Choosing a mode

| What matters more | Mode |
|---|---|
| fewest switches and one probe per `interval` | `failover` |
| spreading load over several nodes | `round_robin` |
| always the fastest node, at the cost of checking every node | `least_test` |

On energy: `least_test` wakes every node each `interval`, `round_robin` only the pool (with
`pool_tolerance: 0`), `failover` one node; details in [lx-energy.md §6](lx-energy.md#6-urltest-probes-wake-sleepers-so-five-mechanisms-bound-them).
