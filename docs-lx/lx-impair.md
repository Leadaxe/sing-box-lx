# sing-box-lx — the `impair` test outbound: a degraded link inside the binary

> RU: [lx-impair.ru.md](lx-impair.ru.md) · Feature: [IMPAIR](../SPECS/FEATURES/020-IMPAIR/FEATURE.md)

`impair` is an outbound **for testing**. It dials like `direct`, or through its own `detour`, and
degrades everything that goes through it on purpose: a bottleneck with a queue, added delay and its
jitter, random and bursty loss, rate swings, short stalls and scheduled outages. It needs no root and
no netem and degrades both directions from one end.

Use it to test balancing, failover, `urltest`, chains and transports against real servers when the
real network is too fast or too even: the link is degraded inside the client binary, without a netem
stand.

It has no place in a production config. On start it always logs a warning:

```text
WARN impair outbound slow-se: traffic through the direct dialer is degraded on purpose (test tool): up rate 10mbit delay 20ms; down rate 10mbit delay 20ms
```

While it runs it logs each link event at the moment it happens, with or without traffic: schedule
entries and runtime commands at INFO, the start and end of a random `stall` at DEBUG. Rate swings
of `vary` are not logged.

```text
INFO impair outbound slow-se: schedule at 55s: up outage begins, connections reset; down outage begins, connections reset
INFO impair outbound slow-se: schedule at 65s: up rate 3mbit delay 20ms outage ends; down rate 3mbit delay 20ms outage ends
DEBUG impair outbound slow-se: stall of the whole link for 2s
DEBUG impair outbound slow-se: stall ends
```

When the core stops it logs counters per direction: units of data, bytes, drops on a full queue, on
loss and in an outage, and stream stalls:

```text
INFO impair outbound slow-se up: 18234 units 25317890 bytes; dropped queue 0 loss 41 outage 0; stream stalls 12
INFO impair outbound slow-se down: 61020 units 88410332 bytes; dropped queue 7 loss 130 outage 0; stream stalls 35
```

The build tag `with_lx_impair` is in `LX_TAGS` and in the AAR: release binaries for desktops, routers
and Android have `impair`. It adds about 150 KB and does nothing until a config declares
`"type": "impair"`. A custom build without the tag rejects the type at load time:

```text
FATAL[0000] initialize outbound[0] impair[slow]: impair outbound is not included in this build, rebuild with -tags with_lx_impair
```

To add it to a custom build:

```sh
go build -tags with_lx_impair ./cmd/sing-box
```

## Two placements

**Under a proxy (the main one).** The proxy outbound points at `impair` with the standard `detour`
key, and `impair` has no `detour` of its own. `impair` then dials the proxy server itself and degrades
that carrier connection: the TCP connection to a vless, trojan or shadowsocks server, or the UDP flow
of hysteria2 or tuic. The rest of the config refers to the proxy as it would without `impair`.

```json
{"type": "vless", "tag": "via-se", "server": "se.example.net", "server_port": 443, "detour": "slow-se", "...": "..."},
{"type": "impair", "tag": "slow-se", "rate": "10mbit", "delay": "20ms"}
```

This is how a real link works: the slow and uneven part is the road to the proxy server, and the
proxy's encryption and framing ride on top of it. Each proxy node gets its own `impair`.

**Over an outbound.** A route rule, a group or another outbound points at `impair`, and `impair`
points with its `detour` at the real outbound. Without `detour` it dials by itself, like `direct`.
This placement is for an exit without a proxy (`direct`, including one with `bind_interface`), where
there is nothing to put it under.

```json
{"type": "impair", "tag": "slow-lte", "detour": "wan-lte", "rate": "8mbit", "delay": "40ms"},
{"type": "direct", "tag": "wan-lte", "bind_interface": "wwan0"}
```

If there is a proxy, put `impair` under it. Over a proxy it works too, but it degrades the proxy's
content instead: UDP carried inside a vless stream loses datagrams before the proxy, which a real
link does not do.

**What `impair` sees in each placement.** The placement decides not only what is degraded but also
which connections `impair` can tell apart.

| | Under a proxy | Over an outbound |
|---|---|---|
| Destination of a connection | the proxy server's address and port, the same for every connection | the real destination of each connection: the site's domain, the urltest probe's address |
| One `impair` connection is | a carrier connection to the proxy; with multiplexing it carries many user connections | one user connection or one probe |
| Telling one site from another, a probe from traffic | impossible: everything goes to one server | possible, by the destination domain |

So a scenario such as "the node fails one site" or "the node breaks connections to sites but lets
probes through" needs the placement over an outbound: `impair` with `detour` set to the proxy node
becomes a member of the group in place of the node. Under a proxy the whole road to the server is
degraded, and probes and traffic suffer alike.

## Keys

`impair` has every dial key `direct` has: `detour`, `bind_interface`, `routing_mark`,
`domain_resolver`, `connect_timeout`. Link keys at the top level apply to both
directions. The `up` (what this end sends) and `down` (what it receives) objects override them for
one direction. Layer order: defaults < `profile` < top level < `up`/`down`.

| Key | Meaning | Default | Range |
|---|---|---|---|
| `rate` | bottleneck rate: `"10mbit"`, `"1.5mbit"`, `"500kbit"`, `"1gbit"`, `"unlimited"` | unlimited | 8kbit..10gbit |
| `queue` | queue before the bottleneck: transmit time at the current rate (`"50ms"`) or a size (`"64KB"`, `"30000B"`; KB and MB are binary) | `"50ms"` | 1ms..10s, 1500B..64MB |
| `delay` | added one-way delay | 0 | 0..10s |
| `jitter` | delay spread: uniform in ±`jitter`, the delay never goes below 0 | 0 | 0..10s |
| `reorder` | let datagrams overtake each other because of the spread | `false` (order kept) | — |
| `loss` | share of random loss, a string with `%`: `"0.5%"` | 0 | 0%..100% |
| `loss_burst` | mean length of a loss run in packets (Gilbert model); 1 means independent losses | 1 | 1..1000 |
| `stream_stall` | how long a stream stops on a loss (see below) | max(200ms, 2·`delay` + 4·`jitter`) | 1ms..60s |
| `vary` | `{"min": "3mbit", "max": "12mbit", "every": "2s"}`: every `every` the rate jumps to a uniformly drawn value in [min, max] | none | every 10ms..1h |
| `up`, `down` | objects with the keys of the rows above | — | — |
| `stall` | `{"every": "20s", "duration": "400ms"}`: random stops of the whole link (both directions), exponential gaps with mean `every` | none | every 100ms..24h, duration 1ms..1h |
| `schedule` | a list of changes over time, see below | none | — |
| `start` | what `schedule` counts from: `"process"` (outbound creation, that is process start) or `"first_use"` (the first traffic through it: a new connection or a unit of data on one already open) | `"process"` | — |
| `profile` | a ready set: `"dsl"`, `"lte-walk"`, `"wifi-lossy"` | none | — |
| `seed` | random seed, an integer: the same seed gives the same losses, jumps and stalls | random | — |

`rate` together with `vary` in one object holds until the first jump after `every`. `vary` without
`rate` draws the first value at once. `rate` in a higher layer (or in a schedule entry) without
`vary` cancels the `vary` of the layer below: `"profile": "lte-walk", "rate": "5mbit"` gives a flat
5 Mbit/s with the profile's delays and losses. The rate counts the payload of the carrier
connection, the bytes the proxy or the application writes to the socket, without IP, UDP and TCP
headers.

`schedule` entries:

| Key | Meaning |
|---|---|
| `at` | when, from the start point (`start`): `"15s"`, from 0 to `"168h"`; required, entries go in ascending `at` |
| `direction` | `"up"` or `"down"`; without it, both directions |
| link keys | `rate`, `queue`, `delay`, `jitter`, `reorder`, `loss`, `loss_burst`, `stream_stall`, `vary` — the new value from this moment |
| `outage` | `true` — the link silently goes away, `false` — it comes back |
| `reset` | only together with `"outage": true`: when the outage starts, every connection through `impair` is torn down (reads and writes return ECONNRESET), and no new connection opens until the outage ends. This is a carrier that drops the TCP connection |

```json
"schedule": [
  {"at": "15s", "loss": "2%"},
  {"at": "25s", "rate": "6mbit"},
  {"at": "40s", "direction": "down", "delay": "120ms"},
  {"at": "55s", "outage": true},
  {"at": "61s", "outage": false},
  {"at": "90s", "outage": true, "reset": true},
  {"at": "95s", "outage": false}
]
```

The outage key is `outage`, not `down`, because `down` is a direction. An entry that changes nothing
is an error.

## Runtime control

The link can be changed without restarting the core: a restart would reset the state of the groups
under test. Three `StartedService` (gRPC) commands work by outbound tag and answer with the state:

| Command | CLI | What it does |
|---|---|---|
| `SetImpair` | `sing-box api impair set <tag> <entry>` | changes the link now, on top of the running schedule. The entry is a `schedule` entry without `at` |
| `ScheduleImpair` | `sing-box api impair schedule <tag> <list> [--start now\|first_use]` | replaces the schedule: the link goes back to the config's parameters with no outage, then the entries run from the call (`now`) or from the first traffic (`first_use`). `[]` stops the schedule and only restores the config's link |
| `GetImpairStatus` | `sing-box api impair status <tag>` | the current parameters and counters per direction, whether a `stall` is on, where the schedule is |

Entries are checked by the same rules as the keys in the config. A wrong entry is rejected whole and
the link stays as it was. Every change is logged like a schedule event. When the config sets `seed`,
a new schedule restarts the random generators: the same scenario over the same traffic draws the
same losses. A CLI argument is inline JSON, `@file` or `-` (stdin).

```sh
sing-box api impair set slow-se '{"outage": true, "reset": true}'
sing-box api impair set slow-se '{"direction": "down", "delay": "300ms"}'
sing-box api impair schedule slow-se --start first_use '[
  {"at": "0s",  "loss": "2%"},
  {"at": "30s", "outage": true, "reset": true},
  {"at": "40s", "outage": false}
]'
sing-box api impair schedule slow-se '[]'
sing-box api impair status slow-se
```

```json
{
  "tag": "slow-se",
  "up":   {"rate": "10mbit", "queue": "50ms", "delay": "20ms", "jitter": "0s", "reorder": false,
           "loss": "2%", "loss_burst": 1, "stream_stall": "200ms", "outage": false, "reset": false,
           "units": 1830, "bytes": 2531789, "dropped_queue": 0, "dropped_loss": 37,
           "dropped_outage": 0, "stream_stalls": 4},
  "down": {"...": "the same fields"},
  "stall": false,
  "schedule": {"entries": 3, "applied": 1, "waiting_for_traffic": false, "elapsed": "12.4s", "next_at": "30s"}
}
```

`schedule.waiting_for_traffic` means a `first_use` schedule waits for the first traffic. `elapsed`
and `next_at` count from the start of the schedule, like `at`. A direction's `vary` is the swing
range, `rate` the rate at this moment.

The commands are wherever `StartedService` is: through the `api` service in the config, through
`lxd` and through libbox. For development on one's own machine the `api` service on loopback is
enough, no keys needed:

```json
"services": [{"type": "api", "listen": "127.0.0.1", "listen_port": 9090}]
```

```sh
export BOX_API_URL=127.0.0.1:9090
```

Without `secret` the service lets in anyone who reaches the port, so listen on `127.0.0.1` only.
The commands are in builds with `with_lx_command`. A build without `with_lx_impair` has no `impair`
outbound, and the commands answer `InvalidArgument` ("not an impair outbound").

## How datagrams and streams are degraded

There is one bottleneck per `impair` instance and per direction. Every connection through this
outbound shares it, as connections share a real link. Each unit of data (a datagram, or a stream
chunk of up to 4 KB) joins the queue. Transmission starts when the bottleneck is free and ends after
size/rate, and delivery happens `delay` ± `jitter` after the end of transmission. If the wait in the
queue exceeds `queue`, the queue is full. A rate change (`vary`, schedule) applies from the next
unit.

**Datagrams** (`ListenPacket`, UDP connections). With a full queue the datagram is dropped from the
tail. `loss` drops it after it has used the bottleneck. During an outage it is dropped too. The
sender gets no error. Timed delivery is run by one goroutine per direction for the whole outbound,
with one reused timer. On receive, datagrams that arrived on a connection and are not yet read wait
in a queue of up to 1024; beyond that they are dropped, as with a full socket buffer.

**Streams** (TCP). No data is lost. A full queue and an outage delay: `Write` waits, like a write to a
TCP socket with a full send buffer, and reading the carrier pauses. A loss on a real TCP carrier is
not missing data but a stop of the stream until retransmission. So `loss` on a stream works like
this: on average every 1448/`loss` bytes (one TCP segment in `1/loss`, exponential gaps) delivery on
this connection stops for `stream_stall`, and everything behind the lost segment waits for it
(head-of-line blocking). Several losses in one chunk give one stop. `loss_burst` and `reorder` do not
apply to streams: a stream always arrives in order. During a silent outage data "in flight" freezes
too and arrives after it.

During a silent outage a connection (both TCP and a UDP socket) does not open: the attempt waits for
the outage to end, but no longer than the connect timeout (the outbound's `connect_timeout`, 5 s by
default), and then fails with a timeout, like a handshake that gets no answer. An outage with
`reset` refuses at once.

**Closing a stream.** Data written before `Close` (including before `CloseWrite`) is not thrown
away. `Close` returns at once and reads and writes fail after it, while what was written passes the
queue and the delay in the background and goes to the carrier; only then is the carrier closed. This
is how a real TCP close sends out its send buffer. The background delivery lasts at most 5 s: if an
outage or a slow link holds it longer, the rest is lost. A scheduled outage with
`reset` and closing the outbound itself break the connection at once. Datagrams not sent by the
time of `Close` are lost, as is normal for UDP.

**What degrading the receive side from this end does not reproduce.** The `down` direction is
degraded after the data has crossed the real network. The real link carried it anyway, and its load
and queue do not change.

- For datagrams this works as a policer with a queue on input. The excess is dropped at the
  receiver, and the sender learns of it only from missing acknowledgements, as it would from losses
  on a real link. But the sender can send faster than `rate`, and the real network carries it.
- For streams the delay reaches the sender through the TCP window. While the queue is full, `impair`
  does not read the carrier, the kernel receive buffer fills, the window closes and the server slows
  down. Between `impair` and the server there is then a kernel buffer of hundreds of KB that a real
  narrow link does not have. In the first fractions of a second the server can send more, and the
  RTT the server sees is that of the real network, not of the degraded one.
- The `up` direction is reproduced fully: data is delayed and lost before it is sent to the network.

So `impair` goes on the client, and it degrades the link in both directions. It is not needed on the
server.

## Profiles

A profile is the bottom layer: any key set explicitly overrides it.

| Profile | `up` | `down` | Both |
|---|---|---|---|
| `dsl` — a steady asymmetric wired line | `rate` 2mbit | `rate` 16mbit | `queue` 100ms, `delay` 12ms, `jitter` 1ms, no loss |
| `lte-walk` — a mobile carrier while walking | `rate` 6mbit, `vary` 2..12mbit every 1s | `rate` 15mbit, `vary` 4..30mbit every 1s | `queue` 120ms, `delay` 35ms, `jitter` 10ms, `loss` 0.3% in runs of 3, `stall` on average every 20s for 400ms |
| `wifi-lossy` — a congested Wi-Fi hop | — | — | `rate` 25mbit, `vary` 8..40mbit every 500ms, `queue` 40ms, `delay` 4ms, `jitter` 6ms, `loss` 2% in runs of 2 |

## CPU cost

The hot path does not allocate: buffers come from pools. No timer is created per packet: datagram
delivery is run by one goroutine per direction for the whole outbound, and a stream connection has
one goroutine per direction with a reused timer. Stream chunks ready to send are written to the
carrier in one write. Measured on an Apple M1 Pro (`go test -bench . ./protocol/impair/`, the carrier
is a stub, so only what `impair` adds is counted):

| What | CPU | Share of a core at 10 Mbit/s |
|---|---|---|
| a 1400-byte datagram: model, delivery queue, delivery goroutine | ≈ 450 ns | ≈ 0.04 % |
| a stream, 64 KB writes (4 KB chunks) | ≈ 0.9 ns per byte | ≈ 0.11 % |
| the model alone (the verdict on one datagram) | ≈ 33 ns | — |

There are no allocations per unit of data (0 allocs/op). At a real rate the datagram delivery
goroutine wakes at most once per 250 µs and sends in one batch everything that came due in that time,
so no more than 4000 wake-ups per second per direction. On a two-core router the cost was not measured
separately; the estimate is an order of magnitude higher: 1 % of a core per 10 Mbit/s.

## Errors

Validation errors carry the sing-box prefix and the word `impair`, for example:

```text
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: up.rate: invalid rate "10mbps": want a number with bit, kbit, mbit or gbit ("10mbit"), or "unlimited"
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: vary: min 12mbit is above max 3mbit
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: schedule[2].at 10s is before the previous entry: list the entries in time order
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: schedule[0].reset needs "outage": true in the same entry
```

Units such as `kbps`, `mbps`, `MB/s` are not accepted: different tools read them as bits or as
bytes.

## Recipe: a group of three proxies, each swinging within 3–12 Mbit/s

Each proxy has its own `impair` and its own `seed`, so the links swing independently and
reproducibly.

```json
{
  "outbounds": [
    {"type": "urltest", "tag": "auto", "outbounds": ["via-se", "via-de", "via-nl"]},
    {"type": "vless", "tag": "via-se", "server": "<SE>", "server_port": 443, "uuid": "<UUID>", "detour": "slow-se"},
    {"type": "vless", "tag": "via-de", "server": "<DE>", "server_port": 443, "uuid": "<UUID>", "detour": "slow-de"},
    {"type": "vless", "tag": "via-nl", "server": "<NL>", "server_port": 443, "uuid": "<UUID>", "detour": "slow-nl"},
    {"type": "impair", "tag": "slow-se", "rate": "10mbit", "vary": {"min": "3mbit", "max": "12mbit", "every": "2s"}, "seed": 1},
    {"type": "impair", "tag": "slow-de", "rate": "10mbit", "vary": {"min": "3mbit", "max": "12mbit", "every": "2s"}, "seed": 2},
    {"type": "impair", "tag": "slow-nl", "rate": "10mbit", "vary": {"min": "3mbit", "max": "12mbit", "every": "2s"}, "seed": 3},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {"final": "auto"}
}
```

The other vless keys (TLS, transport) are the same as for a proxy without `impair`. The mean rate of
each link is 7.5 Mbit/s, the middle of 3..12.

## Recipes for balancing tests

A `round_robin` pool of four nodes, each with its own link:

```json
{
  "outbounds": [
    {"type": "urltest", "tag": "pool", "outbounds": ["via-a", "via-b", "via-c", "via-d"], "interval": "15m",
     "mode": "round_robin", "balancer": {"pool": 3}},
    {"type": "vless", "tag": "via-a", "server": "<A>", "server_port": 443, "uuid": "<UUID>", "detour": "line-a"},
    {"type": "vless", "tag": "via-b", "server": "<B>", "server_port": 443, "uuid": "<UUID>", "detour": "line-b"},
    {"type": "vless", "tag": "via-c", "server": "<C>", "server_port": 443, "uuid": "<UUID>", "detour": "line-c"},
    {"type": "vless", "tag": "via-d", "server": "<D>", "server_port": 443, "uuid": "<UUID>", "detour": "line-d"},
    {"type": "impair", "tag": "line-a", "delay": "30ms", "seed": 1},
    {"type": "impair", "tag": "line-b", "delay": "30ms", "loss": "5%", "seed": 2},
    {"type": "impair", "tag": "line-c", "stall": {"every": "2m", "duration": "40s"}, "seed": 3},
    {"type": "impair", "tag": "line-d", "delay": "2s", "jitter": "1s", "seed": 4}
  ],
  "route": {"final": "pool"}
}
```

- `line-a` — an even link, the control.
- `line-b` — 5 % loss. Over the TCP carrier of vless it does not break connections but stops the
  stream for at least 200 ms about once per 29 KB: the node works, slowly. Over a UDP carrier
  (hysteria2, tuic) the datagrams are really lost.
- `line-c` — a flapping node: about once every 2 minutes the link silently goes away for 40 s. The
  node is struck out and comes back; a second strike-out between scheduled checks sends it to
  quarantine ([pool self-heal](lx-urltest.md#pool-self-heal)).
- `line-d` — a slow node at the 15 s probe timeout. A probe makes several round trips, each costs
  about 4 s here, so some probes fit into 15 s and some do not. The number of round trips depends
  on the protocol, so tune `delay` by the probe delay on your stand.

`failover` whose current node first slows down and then breaks connections:

```json
{
  "outbounds": [
    {"type": "urltest", "tag": "main", "outbounds": ["via-a", "via-b"], "interval": "1m", "mode": "failover"},
    {"type": "vless", "tag": "via-a", "server": "<A>", "server_port": 443, "uuid": "<UUID>", "detour": "line-a"},
    {"type": "vless", "tag": "via-b", "server": "<B>", "server_port": 443, "uuid": "<UUID>"},
    {"type": "impair", "tag": "line-a", "start": "first_use", "seed": 1, "schedule": [
      {"at": "60s", "delay": "400ms"},
      {"at": "240s", "delay": "0s"},
      {"at": "300s", "outage": true, "reset": true},
      {"at": "310s", "outage": false}
    ]}
  ],
  "route": {"final": "main"}
}
```

The schedule counts from the first traffic through `line-a` (`"start": "first_use"`). From the
60th to the 240th second 400 ms is added to the delay each way. The scheduled probe once a minute
sees it, and if the node has become more than twice as slow as `via-b`, the group checks the
[degradation](lx-urltest.md#4-failover--hold-until-it-fails). At the 300th second every connection
through `line-a` breaks, and for 10 s new ones are refused at once: the group penalises the node and
moves to `via-b`.

The same `seed` repeats the same losses, jumps and stalls from run to run; to compare builds, change
the build, not the `seed`.
