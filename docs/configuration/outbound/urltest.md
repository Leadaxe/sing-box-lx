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

    An `lx` extension (SPEC 019), not present in upstream sing-box.

Load-balancing mode — how a node is chosen per connection:

- `least_test` (default, also when empty): pick the lowest-delay node. This is upstream's
  behaviour; `balancer` must not be set.
- `round_robin`: rotate over a fixed-size **pool** of nodes (see `balancer`). Selection
  happens once per connection; a UDP/QUIC session stays on one node. Designed to scale to
  large node lists — only the pool is health-checked, not every node.

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

A dead pool node keeps its slot until a live replacement is found, so the pool never empties.
A dial error never changes the pool (its cause is unknowable from one failure); only the
periodic health-check does.

##### balancer.sticky_hash

Binds one flow to one node, so the same key (e.g. the same destination domain) always
reaches the same pool node. Key components, concatenated in order. **Omitted → defaults to
`["process", "domain"]`** (stickiness on); an explicit `[]` disables stickiness (pure
round-robin). An absent component contributes an empty string; when all are empty the key is
`""`, which maps to a single fixed slot (so keyless flows do not rotate). Allowed values:

- `process`: the source process (Android package name, else executable path).
- `domain`: the destination domain (empty for IP destinations).
- `source_ip`: the source IP.
- `dest_ip`: the destination IP — **empty until the destination is resolved**, so for
  domain-based traffic (socks5h / sniffed) it is `""` when the key is built.
- `dest_port`: the destination port.

Binding is to a fixed **slot index** (`slot[hash(key) % pool]`), not a node position. Since
slots never move and a replacement takes the exact slot it evicts, a node that stays in its
slot keeps all its keys when other slots change — no needless reconnects, no per-key state.

!!! warning

    For domain-based traffic, keep `domain` in `sticky_hash`. A key of only
    `source_ip` / `dest_ip` / `dest_port` collapses to `""` for an unresolved
    destination, so with a single source every flow shares one key and sticks to a
    single node — correct stickiness, but not the granularity you want.

!!! tip

    For a large node list, `mode: round_robin` with `pool_tolerance: 0` and a small `pool`
    (e.g. 3) is the recommended setup: a handful of live nodes in rotation, minimal testing.
    A longer `interval` (e.g. `15m`) further reduces background testing.
