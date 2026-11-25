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

- `least_test` (default, also when empty): pick the lowest-delay node. This is upstream's
  behaviour; `balancer` must not be set.
- `round_robin`: rotate over a fixed-size **pool** of nodes (see `balancer`). Selection
  happens once per connection; a UDP/QUIC session stays on one node. Designed to scale to
  large node lists — only the pool is health-checked, not every node.
- `failover` (SPEC 116): pick the lowest-delay node and **hold it until it fails**, even if
  another node becomes faster. Each `interval` only the held node is tested. When it fails
  a test, or a dial through it fails with a "path is dead" error, the group moves to the
  lowest-delay live node and holds that one. A degraded node moves too (SPEC 126): when the
  held node answers more than 2× slower than the fastest other node of the same network,
  it is tested once more; still more than 2× slower → every node is tested, and the group
  moves only to a node more than 1.5× faster (otherwise the whole network got slower, not
  the node). These ratios have no config key. A manual URL test tests every node and
  re-selects the fastest. `tolerance` is ignored (a startup warning says so); `balancer`
  must not be set.

`least_connection` was considered and dropped (`round_robin` is statistically even).

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

- `0` (default): keep the pool full of **live** nodes, delay is not ranked. The core tests
  no more nodes than needed to refill the pool, then stops — the cheap mode for large lists.
- `> 0`: test all nodes and keep the **fastest** `pool` of them. A pool member is replaced
  only when an outside node beats it by more than `pool_tolerance` ms (hysteresis against
  churn). Testing all nodes is more expensive — suitable for smaller lists.

A dead pool node keeps its slot until a live replacement is found, so the pool never empties,
but while the pool has a live node, new connections skip the slot of a node that failed its
last health-check: with stickiness the keys of dead slots spread evenly over the live slots
and come back once the node answers again, without stickiness the rotation runs over the
live slots. When every slot is dead, selection is unchanged. A quarantined node (below) is
skipped the same way.

A `round_robin` pool handles node failures between checks on its own. A node that
dies between two runs of `interval` is replaced in a fraction of a second, and the connection that
hit it opens through the next slot: on a NanoPi stand, 30 of 30 requests went through with one
pool node killed and the slot was refilled in 0.13 s. A node that accepts connections and stays
silent is skipped after 5 s and replaced after 20 s. A destination closed to one node (a
geo-restriction, the site bans the node's address) moves to another pool node after its first
failure, while other destinations stay where they are. A loss of the local network (device sleep,
a network switch) strikes out no node. This works only in `round_robin`, has no config keys and
costs 7 % more CPU on a stream of short connections. The scenarios and limits are in
[lx-config.md](../../../docs-lx/lx-config.md#the-pool-recovers-from-a-node-failure-without-waiting-for-interval).

**Self-heal on a failed connection** (SPEC 138). One failed connection does not tell a dead node
from a destination that is unreachable through it or from a local network outage, so a failure
by itself changes neither the pool nor the history. Instead:

- A connection that does not open through a pool node is retried once through the next live slot,
  so the application keeps its connection. A connection that closes without a single byte from the
  destination after the client sent something is a failure too, but it is not retried (the
  application already sent its data).
- The failure waits for a verdict on its node. When the node proves alive after the failure — a
  first byte on a connection started later, or an answer to a probe started later — the
  destination is the problem: with stickiness the sticky key moves to the next slot, and a key that
  failed through every node of the pool is marked as an address problem and returns to its own
  slot until the next run.
- A failure that points at the destination — a close without a byte, a failed open through a
  `direct` or endpoint (WireGuard, AWG, Tailscale and other tunnels) leaf, a failed retry — is
  settled at once by a probe of that node alone with the group's `url`, at most once per node in
  3 s; its answer moves the key, its silence starts the check below. A failed open through a proxy
  node puts the node itself under suspicion and goes to the check at once.
- The check probes the node and a control node (the next slot) with the group's `url`, without
  writing history. The node is silent and the control answers → the node is dead: its history is
  dropped, its connections still waiting for it are closed, and its slot gets a replacement from
  outside the pool at once, without waiting for `interval`. Nobody answers → the local network is
  down: nothing changes, and the self-heal freezes for 15 s.
- A node that accepts connections and stays silent gives no failure; a connection hanging without
  an answer for more than 5 s triggers the same check. Until the node proves alive after that
  connection started, its slot is skipped like a dead one, as long as the pool has a slot that is
  neither dead nor holding such a connection.
- A node that answers probes but fails noticeably more connections than the rest of the pool
  (more than twice the pool median, from 8 connections, 3 confirmed failures and a 5 % failure
  share) is quarantined:
  skipped and replaced until the next scheduled run, manual run or network change. Its history
  stays.
- The self-heal also freezes for 15 s on a device wake and on a network change. It runs at most
  one check at a time, with at least 15 s between checks, so a burst of failures costs one check;
  the single-node probes keep their own limit of one per node in 3 s.
- UDP uses the same slots; a failed UDP open is retried and checked as TCP, a flow without an
  answer only triggers a check, and UDP never moves sticky keys.

There are no config keys for any of this. The state is visible with
`sing-box api group pool <tag>` (with `with_lx_command`) and the daemon's `GetPool`: per slot the
delay, `live`/`dead`/`quarantined`, connections and confirmed failures; per group the number of
shifted and address-problem keys and the freeze. The connection list shows the node each
connection actually went through, including the retry.

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
slot keeps all its keys when other slots change — no needless reconnects, no per-key state.

!!! note

    For domain-based traffic, `domain` is the natural sticky key and works out of the
    box (it reads the sniffed domain, which survives domain→IP resolution). `dest_ip`
    remains a valid alternative, but note it is `""` until the destination is resolved,
    so a key of only `source_ip` / `dest_ip` / `dest_port` can collapse to one shared
    key for an unresolved destination from a single source — correct stickiness, but
    coarser granularity. For raw-IP destinations (no domain to sniff) use `dest_ip`.

!!! tip

    For a large node list, `mode: round_robin` with `pool_tolerance: 0` and a small `pool`
    (e.g. 3) is the recommended setup: a handful of live nodes in rotation, minimal testing.
    A longer `interval` (e.g. `15m`) further reduces background testing.
