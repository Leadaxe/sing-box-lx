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
  "sticky": {
    "mode": "jumphash",
    "timeout": "10m",
    "cap": 2000,
    "hash": ["process", "domain"]
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
  behaviour; `sticky` is ignored.
- `round_robin`: rotate across the live nodes (those with a fresh URL-test result that
  support the network). Selection happens once per connection; a UDP/QUIC session stays on
  one node.
- `least_connection`: not implemented yet (planned); configuring it is an error.

When no node is live, the first usable outbound is used as a fallback.

#### sticky

!!! quote "sing-box-lx"

    An `lx` extension (SPEC 019), not present in upstream sing-box.

Binds one flow to one node in `round_robin` / `least_connection` mode, so the same key
(e.g. the same destination domain) always reaches the same node. Omit it (or leave `hash`
empty) for no stickiness.

##### sticky.mode

The binding mechanism:

- `jumphash` (default): stateless consistent hash over the live nodes. No table; changing
  the live-node count remaps only ~1/n of keys. `timeout` / `cap` are ignored.
- `ttlmap`: a `key → node` table. A key sticks to its node while that node is alive and the
  entry is younger than `timeout`; a dead node re-pins to a surviving one.

##### sticky.timeout

`ttlmap` entry TTL. `10m` will be used if empty. Ignored for `jumphash`.

##### sticky.cap

`ttlmap` maximum entries; the oldest are evicted past this. `2000` will be used if empty.
Ignored for `jumphash`.

##### sticky.hash

Key components, concatenated in order. An absent component contributes an empty string;
when all are empty the key is `""`, which maps to a single fixed node (so keyless flows do
not rotate). Allowed values:

- `process`: the source process (Android package name, else executable path).
- `domain`: the destination domain (empty for IP destinations).
- `source_ip`: the source IP.
- `dest_ip`: the destination IP.
- `dest_port`: the destination port.
