### Structure

```json
{
  "type": "urltest",
  "tag": "auto",
  
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "url": "",
  "interval": "",
  "tolerance": 0,
  "idle_timeout": "",
  "interrupt_exist_connections": false,

  "mode": "least_test",
  "balancer": {
    "pool": 3,
    "pool_tolerance": 0,
    "sticky_hash": ["process", "domain"]
  }
}
```

### Fields

#### outbounds

==Required==

List of outbound tags to test.

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### tolerance

The test tolerance in milliseconds. `50` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.

#### mode

!!! quote "sing-box-lx"

    An `lx` extension (SPEC 019, SPEC 116), not present in upstream sing-box.

Load-balancing mode — how a node is chosen per connection:

- `least_test` (default, also when empty): pick the lowest-delay node, as upstream does. A
  dial that fails with a "path is dead" error gives the node a penalty and retries once
  through the next node ([lx-urltest.md §2](../../../docs-lx/lx-urltest.md#2-least_test--fastest-node)). `balancer`
  must not be set.
- `round_robin`: rotate over a fixed-size **pool** of nodes (see `balancer`). Selection
  happens once per connection; a UDP/QUIC session stays on one node. Only the pool is
  health-checked, not every node, so the mode suits large node lists
  ([lx-urltest.md §3](../../../docs-lx/lx-urltest.md#3-round_robin--pool-with-stickiness)).
- `failover`: pick the fastest node and stay on it until it fails, even if another node gets
  faster. Each `interval` only that node is tested. The group moves to the fastest live node
  when the node fails a test, when a dial through it fails with a "path is dead" error, or
  when it answers more than 2× slower than the fastest other node twice in a row and another
  node is more than 1.5× faster. A manual URL test tests every node and picks the fastest.
  `tolerance` is ignored with a startup warning; `balancer` must not be set
  ([lx-urltest.md §4](../../../docs-lx/lx-urltest.md#4-failover--hold-until-it-fails)).

`least_connection` is rejected at load: `round_robin` already spreads connections evenly.
Which mode to pick: [lx-urltest.md §5](../../../docs-lx/lx-urltest.md#5-choosing-a-mode).

#### balancer

!!! quote "sing-box-lx"

    An `lx` extension (SPEC 019), not present in upstream sing-box.

`round_robin` parameters. Only valid with `mode: round_robin` (an error otherwise); when
omitted, defaults apply. The upstream `tolerance` field is **ignored** in `round_robin` (a
warning is logged) — use `pool_tolerance` instead.

##### balancer.pool

The rotation pool size — how many nodes are in rotation at once. `0` or omitted uses the
default `3`; a negative value is an error. The effective size is
`min(pool, number of outbounds)`. Only the pool is health-checked each interval, so a list
of hundreds/thousands of nodes does not mean hundreds/thousands of URL tests.

##### balancer.pool_tolerance

In milliseconds. Controls how the pool is filled and maintained:

- `0` (default): keep the pool full of **live** nodes, delay is not ranked. The core stops
  testing once it has found `pool` live nodes plus a reserve of `max(ceil(pool/2), 1)` — the
  cheap mode for large lists. Reserve nodes carry no traffic; they are taken first for a
  replacement.
- `> 0`: test all nodes and keep the **fastest** `pool` of them. A pool member is replaced
  only when an outside node beats it by more than `pool_tolerance` ms (hysteresis against
  churn); the slot's keys move to the new node. Testing all nodes is more expensive —
  suitable for smaller lists.

The number of slots never changes: a dead node's slot stays empty until a live replacement is
found. While the pool has a live node, new connections skip the slot of a node that failed its
last health-check: with stickiness the keys of dead slots spread evenly over the live slots
and come back once the slot is filled again, without stickiness the rotation runs over the
live slots. When every slot is dead, selection is unchanged. A quarantined node (one that
fails connections more often than the rest of the pool) is skipped the same way.

A `round_robin` pool handles node failures between checks on its own. A node that dies
between two runs of `interval` is replaced at once, and the connection that hit it opens
through the next node. A node is struck out only when it stays silent on its own probe while
a control node answers; when nobody answers, nothing is concluded. A node that accepts
connections and stays silent is skipped after 5 s and replaced in about 41 s; a node that
refuses connections is struck out in under a second. Only a node that answered its own probe
fills an empty slot, and a node struck out twice between scheduled checks is quarantined. A
site closed to one node (a geo-restriction, the site bans the node's address) moves to another
pool node at once on an explicit refusal, otherwise on its second failure on that node, while
the rest of the traffic stays on its nodes. A lost local network (device sleep, a network
switch) strikes out no node; while nobody answers, the pause between checks grows from 15 s to
120 s. This works only in `round_robin` and has no config keys. The scenarios and limits are
in [lx-urltest.md](../../../docs-lx/lx-urltest.md#pool-self-heal).

Liveness is the node's shared URL test history. A manual delay test with a short timeout
(Clash API `/proxies/{name}/delay`, the daemon's `URLTestOutbound`) that does not complete,
or a failed dial of a `least_test` group sharing the node, also takes the node out of the
rotation until the next pool health-check, at most `interval` plus the probe time.

##### balancer.sticky_hash

Binds one flow to one node, so the same key (e.g. the same destination domain) always
reaches the same pool node. Key components, concatenated in order. **Omitted (or `[]`) →
defaults to `["process", "domain"]`** (stickiness on). To **disable** stickiness (pure
round-robin) use the sentinel **`["none"]`** — a bare `[]` cannot be used because the config
decoder collapses an empty array to "omitted" (see the note below). An absent component
contributes an empty string; when all are empty the key is `""`, which maps to a single fixed
slot (so keyless flows do not rotate). Allowed values:

- `process`: the source process (Android package name, else executable path).
- `domain`: the destination domain from the original (sniffed) request — read from
  `metadata.Domain`, which survives the router's domain→IP resolve, so it is populated
  for normal domain traffic. Only truly empty for raw-IP destinations (no domain to sniff).
- `source_ip`: the source IP.
- `dest_ip`: the destination IP — **empty until the destination is resolved**, so for
  domain-based traffic (socks5h / sniffed) it is `""` when the key is built.
- `dest_port`: the destination port.
- `none`: **disables** stickiness. Must be the only element (mixing it with a real component
  is an error).

!!! note

    `["none"]` rather than `[]` is required to turn stickiness off: the sing-box config decoder
    re-marshals each outbound and an empty JSON array does not survive that round-trip (it
    becomes indistinguishable from an omitted field, which means *default*). The `"none"`
    sentinel is unambiguous.

Binding is to a fixed **slot index** (`slot[hash(key) % pool]`), not a node position. Since
slots never move and a replacement takes the exact slot it evicts, a node that stays in its
slot keeps all its keys when other slots change — no needless reconnects, no per-key state
([lx-urltest.md](../../../docs-lx/lx-urltest.md#slot-hash-binding)).

!!! note

    Keep `domain` in the key for domain traffic: it stays filled after the router
    resolves the domain to an IP. `dest_ip` is `""` until then, so a key of only
    `source_ip` / `dest_ip` / `dest_port` puts every unresolved flow of one source on one
    slot. For raw-IP destinations, which have no domain, use `dest_ip`.

!!! tip

    For a large node list, `mode: round_robin` with `pool_tolerance: 0` and a small `pool`
    (e.g. 3) is the recommended setup: a handful of live nodes in rotation, minimal testing.
    A longer `interval` (e.g. `15m`) further reduces background testing.
