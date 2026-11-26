# sing-box-lx — the `impair` test outbound: a controlled bad link inside the core

> RU: [lx-impair.ru.md](lx-impair.ru.md) · Feature: [IMPAIR](../SPECS/FEATURES/020-IMPAIR/FEATURE.md)

`impair` is an outbound **for testing**. It dials like `direct`, or through its own `detour`, and
degrades everything that goes through it on purpose: a bottleneck with a queue, added delay and its
jitter, random and bursty loss, rate swings, short stalls and outages on a schedule. It needs no root
and no netem and degrades both directions from one end. What to degrade, how much and when is set by
the config and can be changed while the core runs; `seed` repeats a run exactly.

Use it to test balancing, failover, `urltest`, chains and transports against real servers when the
real network is too fast or too even: the link is degraded inside the client core, without a netem
stand.

It has no place in a production config. On start it always logs a warning with the line it builds:

```text
WARN impair outbound slow-se: traffic through the direct dialer is degraded on purpose (test tool): up rate 10mbit delay 20ms; down rate 10mbit delay 20ms; 1 sublinks; schedule of 3 entries repeating every 4m0s
```

While it runs it logs each link event at the moment it happens, with or without traffic: schedule
entries and runtime commands at INFO, the start and end of a random `stall` at DEBUG. Rate swings
of `vary` are not logged.

```text
INFO impair outbound slow-se: schedule at 1m0s: profile metro; up rate 2mbit..12mbit/1s delay 35ms±10ms loss 3%; down rate 4mbit..30mbit/1s delay 35ms±10ms loss 3%; stall every 3s for 500ms
INFO impair outbound slow-se: schedule at 2m0s: profile metro; up … outage (reset); down … outage (reset); stall every 3s for 500ms
INFO impair outbound slow-se: set: profile dsl; up rate 2mbit delay 12ms±1ms; down rate 16mbit delay 12ms±1ms
INFO impair outbound slow-se: new schedule of 1 entries, link and sublinks back to the config; starts now, repeats every 4s
DEBUG impair outbound slow-se: stall of the whole line for 500ms
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
{"type": "impair", "tag": "slow-se", "link_params": {"rate": "10mbit", "delay": "20ms"}}
```

This is how a real link works: the slow and uneven part is the road to the proxy server, and the
proxy's encryption and framing ride on top of it. Each proxy node gets its own `impair`.

**Over an outbound.** A route rule, a group or another outbound points at `impair`, and `impair`
points with its `detour` at the real outbound. Without `detour` it dials by itself, like `direct`.
This placement is for an exit without a proxy (`direct`, including one with `bind_interface`), where
there is nothing to put it under, and for tests that must tell one site from another.

```json
{"type": "impair", "tag": "slow-lte", "detour": "wan-lte", "link_params": {"rate": "8mbit", "delay": "40ms"}},
{"type": "direct", "tag": "wan-lte", "bind_interface": "wwan0"}
```

If there is a proxy and the test does not need to tell sites apart, put `impair` under it. Over a
proxy it works too, but it degrades the proxy's content instead: UDP carried inside a vless stream
loses datagrams before the proxy, which a real link does not do.

**What `impair` sees in each placement.** The placement decides not only what is degraded but also
which connections `impair` can tell apart.

| | Under a proxy | Over an outbound |
|---|---|---|
| Destination of a connection | the proxy server's address and port, the same for every connection | the real destination of each connection: the site's domain, the urltest probe's address |
| One `impair` connection is | a carrier connection to the proxy; with multiplexing it carries many user connections | one user connection or one probe |
| Telling one site from another, a probe from traffic | impossible: everything goes to one server | possible, by the destination domain ([sublinks](#sublinks)) |

So a scenario such as "the node fails one site" or "the node breaks connections to sites but lets
probes through" needs the placement over an outbound: `impair` with `detour` set to the proxy node
becomes a member of the group in place of the node, and a sublink picks the site. Under a proxy the
whole road to the server is degraded, and probes and traffic suffer alike.

## Keys

The outbound itself has few keys. Everything about how the link is degraded lives in one object,
`link_params`, and that object is the same in five places: the outbound's own line, a
[sublink](#sublinks), a [schedule](#schedule) entry, the [`impair set`](#runtime-control) command
and a [profile](#profiles).

| Outbound key | Meaning | Default |
|---|---|---|
| dial keys | everything `direct` has: `detour`, `bind_interface`, `routing_mark`, `domain_resolver`, `connect_timeout` | as in `direct` |
| `link_params` | the outbound's line, see below | no degradation |
| `sublinks` | a second stage for the connections that match, see [Sublinks](#sublinks) | none |
| `schedule` | changes over time, see [Schedule](#schedule) | none |
| `schedule_repeat` | the length of one round: the schedule starts over every round, `"60s"`; 100ms..168h | the schedule runs once |
| `start` | what `schedule` counts from: `"process"` (outbound creation, that is process start) or `"first_use"` (the first traffic through it: a new connection or a unit of data on one already open) | `"process"` |
| `seed` | random seed, an integer: the same seed gives the same losses, jumps, stalls and `share` picks | random |

A link key at the top level of the outbound (`"rate": "10mbit"` next to `type`) is a load error:
`json: unknown field "rate"`. Link keys go inside `link_params`.

`link_params`:

| Key | Meaning | Default | Range |
|---|---|---|---|
| `profile` | a template: the line goes back to the defaults, the template is laid on it, and the other keys of this `link_params` tune it, see [Profiles](#profiles) | none | a built-in or `lx.impair_profiles` name |
| `rate` | bottleneck rate: `"10mbit"`, `"1.5mbit"`, `"500kbit"`, `"1gbit"`, `"unlimited"` | unlimited | 8kbit..10gbit |
| `queue` | queue before the bottleneck: transmit time at the current rate (`"50ms"`) or a size (`"64KB"`, `"30000B"`; KB and MB are binary) | `"50ms"` | 1ms..10s, 1500B..64MB |
| `delay` | added one-way delay | 0 | 0..10s |
| `jitter` | delay spread: uniform in ±`jitter`, the delay never goes below 0 | 0 | 0..10s |
| `reorder` | let datagrams overtake each other because of the spread | `false` (order kept) | — |
| `loss` | share of random loss, a string with `%`: `"0.5%"` | 0 | 0%..100% |
| `loss_burst` | mean length of a loss run in packets (Gilbert model); 1 means independent losses | 1 | 1..1000 |
| `stream_stall` | how long a stream stops on a loss (see below) | max(200ms, 2·`delay` + 4·`jitter`) | 1ms..60s |
| `vary` | `{"min": "3mbit", "max": "12mbit", "every": "2s"}`: every `every` the rate jumps to a uniformly drawn value in [min, max]; `false` turns the swings off | none | every 10ms..1h |
| `outage` | `true` — the line silently goes away, `false` — it comes back | `false` | — |
| `up`, `down` | the same link keys, `outage` included, for one direction: `up` is what this end sends, `down` what it receives | — | — |
| `stall` | `{"every": "20s", "duration": "400ms"}`: random stops of the whole line (both directions), exponential gaps with mean `every`; opening a connection waits for a stop to end, no longer than `connect_timeout`; `false` turns them off | none | every 100ms..24h, duration 1ms..1h |
| `reset` | `true` together with an outage that begins in the same `link_params` (`"outage": true` or a profile with one): see below | `false` | — |

The link keys at the top of `link_params` apply to both directions; `up` and `down` override them for
one. `rate` together with `vary` in one object holds until the first jump after `every`. `vary`
without `rate` draws the first value at once. `rate` without `vary` cancels a `vary` the line already
had: `{"profile": "lte-walk", "rate": "5mbit"}` gives a flat 5 Mbit/s with the profile's delays and
losses. The rate counts the payload of the carrier connection, the bytes the proxy or the
application writes to the socket, without IP, UDP and TCP headers.

**`reset`** is allowed in a schedule entry, in `impair set` and in a sublink. In a schedule entry and
in `impair set` it breaks every connection of the line when the outage begins (reads and writes
return ECONNRESET), and no new connection opens until the outage ends: a carrier that drops the TCP
connection. In a sublink it breaks the sublink's connections and refuses new matching ones at once;
the outbound's other connections are untouched. In the outbound's own `link_params` it is a load
error.

**Turning a value off** is written `false`: `"vary": false`, `"stall": false`, and in a schedule
entry or `impair set` `"sublinks": false`. An empty `[]` or `{}` turns nothing off: in a config file
the sing-box decoder drops it as if the key were absent, and a command rejects it with a hint to
write `false`.

## Sublinks

A sublink degrades only some of the connections: one site, one port, every tenth connection, a tenth
of UDP flows. It is a **second stage** after the outbound's line, not a replacement: a matching
connection goes through the outbound's line first and then through its sublink. The sublink's
`link_params` count from neutral values, not from the outbound's line, and an outage of the
outbound's line breaks the sublink's connections too.

```json
"sublinks": [
  {"match": {"domain_suffix": "example.org"}, "link_params": {"outage": true, "reset": true}},
  {"match": {"network": "udp", "share": "10%"}, "link_params": {"loss": "20%"}},
  {"match": {"port": 443, "every": 5}, "link_params": {"down": {"outage": true}}}
]
```

Both keys are required, and `match` needs at least one field. The fields of one `match` combine with
AND, the values of one field with OR:

| `match` field | Matches |
|---|---|
| `domain`, `domain_suffix` | the destination domain, exact or by suffix |
| `ip_cidr` | the destination address: a prefix or a single address |
| `port` | the destination port |
| `network` | `"tcp"` or `"udp"` |
| `every` | each N-th connection among those that match the other fields; 1..1000000 |
| `share` | a share of the connections that match the other fields, `"10%"`; the picks follow `seed` |

A connection picks its sublink when it opens: the first sublink that matches, or none. A connection
that is already open keeps the sublink it got, even when the list changes. `every` and `share` count
on their own for each sublink and start again when the list is replaced.

The domain is taken from the routing metadata, so `domain` and `domain_suffix` work only when
`impair` stands over a node (`detour` to the proxy, `impair` as the group member). Under a proxy
`impair` sees only the proxy server, see [What `impair` sees](#two-placements).

An outage only on receive (`"down": {"outage": true}`) does not stop the connection from opening:
it connects and gets no answer. Dialing waits only during an outage of sending, and no longer than
`connect_timeout`.

## Schedule

A schedule entry changes the outbound at the moment `at`: its line (`link_params`), its sublinks
(`sublinks`), or both. `sublinks` replaces the whole list; `"sublinks": false` removes it. An entry
without either key, or with a `link_params` that changes nothing, is an error.

```json
"schedule_repeat": "5m",
"schedule": [
  {"at": "60s",  "link_params": {"loss": "2%"}},
  {"at": "90s",  "link_params": {"down": {"delay": "120ms"}}},
  {"at": "120s", "sublinks": [{"match": {"domain_suffix": "example.org"}, "link_params": {"loss": "30%"}}]},
  {"at": "180s", "link_params": {"outage": true, "reset": true}},
  {"at": "190s", "link_params": {"outage": false}, "sublinks": false},
  {"at": "240s", "link_params": {"profile": "lte-walk"}}
]
```

`at` counts from the start point (`start`), from 0 to `"168h"`; entries go in ascending `at`. With
`schedule_repeat` every `at` must be inside one round (`at` < `schedule_repeat`), and the schedule
starts over each round; without it the schedule runs once and the last state stays.

An entry without `profile` changes only the keys it names: the 90 s entry above adds delay on
receive and keeps the 2 % loss. An entry with `profile` resets the whole line to the template, so
the 240 s entry drops everything that came before it, the outage and `stall` included.

## Profiles

A profile is a template for `link_params`: the line goes back to the defaults, takes the template,
and the other keys of the same `link_params` tune it. `{"profile": "dsl", "delay": "40ms"}` is the
`dsl` line with 40 ms of delay. A change of profile resets everything that came before, including
`stall` and `outage`; `link_params` without `profile` changes only the keys it names.

Built-in profiles:

| Profile | `up` | `down` | Both |
|---|---|---|---|
| `dsl` — a steady asymmetric wired line | `rate` 2mbit | `rate` 16mbit | `queue` 100ms, `delay` 12ms, `jitter` 1ms, no loss |
| `lte-walk` — a mobile carrier while walking | `rate` 6mbit, `vary` 2..12mbit every 1s | `rate` 15mbit, `vary` 4..30mbit every 1s | `queue` 120ms, `delay` 35ms, `jitter` 10ms, `loss` 0.3% in runs of 3, `stall` on average every 20s for 400ms |
| `wifi-lossy` — a congested Wi-Fi hop | — | — | `rate` 25mbit, `vary` 8..40mbit every 500ms, `queue` 40ms, `delay` 4ms, `jitter` 6ms, `loss` 2% in runs of 2 |

Your own profiles live in the root `lx` block and are shared by every `impair` in the config:

```json
"lx": {"impair_profiles": {
  "metro":  {"link_params": {"profile": "lte-walk", "loss": "3%", "stall": {"every": "30s", "duration": "3s"}}},
  "tunnel": {"link_params": {"profile": "metro", "outage": true}}
}}
```

A profile can build on another one (`tunnel` on `metro`, `metro` on `lte-walk`); a loop is a load
error. Your name overrides a built-in one with the same name. `reset` in a profile is a load error:
whether to break connections is decided where the profile is applied. Every profile is checked at
load, even one no line uses yet.

## Runtime control

The link can be changed without restarting the core: a restart would reset the state of the groups
under test. Three `StartedService` (gRPC) commands work by outbound tag and answer with the state:

| Command | CLI | What it does |
|---|---|---|
| `SetImpair` | `sing-box api impair set <tag> <entry>` | changes the line or the sublinks now, on top of the running schedule. The entry is a schedule entry without `at`: `link_params`, `sublinks` or both |
| `ScheduleImpair` | `sing-box api impair schedule <tag> [--start now\|first_use] [--repeat 60s] <list>` | replaces the schedule: the line and the sublinks go back to the config, the random generators restart from `seed`, then the entries run from the call (`now`) or from the first traffic (`first_use`), once or every `--repeat`. `'[]'` stops the schedule and only restores the config |
| `GetImpairStatus` | `sing-box api impair status <tag>` | the current line, counters per direction, the sublinks with their counters, where the schedule is |

Entries are checked by the same rules as the config. A wrong entry is rejected whole and the link
stays as it was. Every change is logged like a schedule event. With `seed` in the config, a new
schedule over the same traffic draws the same losses. A CLI argument is inline JSON, `@file` or `-`
(stdin). `'[]'` is allowed for `schedule` because a CLI list does not pass through the config
decoder.

```sh
sing-box api impair set slow-se '{"link_params": {"profile": "metro"}}'
sing-box api impair set slow-se '{"link_params": {"outage": true, "reset": true}}'
sing-box api impair set slow-se '{"link_params": {"down": {"delay": "300ms"}}}'
sing-box api impair set slow-se '{"sublinks": [{"match": {"domain_suffix": "example.org"}, "link_params": {"loss": "30%"}}]}'
sing-box api impair set slow-se '{"sublinks": false}'
sing-box api impair schedule slow-se --start first_use --repeat 60s '[
  {"at": "0s",  "link_params": {"loss": "2%"}},
  {"at": "30s", "link_params": {"outage": true, "reset": true}},
  {"at": "40s", "link_params": {"outage": false}}
]'
sing-box api impair schedule slow-se '[]'
sing-box api impair status slow-se
```

```json
{
  "tag": "slow-se",
  "link_params": {
    "profile": "metro",
    "up":   {"rate": "6mbit", "vary": "2mbit..12mbit/1s", "queue": "120ms", "delay": "35ms", "jitter": "10ms",
             "reorder": false, "loss": "3%", "loss_burst": 3, "stream_stall": "200ms", "outage": false, "reset": false},
    "down": {"...": "the same fields"},
    "stall": {"every": "3s", "duration": "500ms", "now": false}
  },
  "counters": {
    "up":   {"units": 1830, "bytes": 2531789, "dropped_queue": 0, "dropped_loss": 37, "dropped_outage": 0, "stream_stalls": 4},
    "down": {"...": "the same fields"}
  },
  "sublinks": [
    {"match": {"network": "udp", "share": "10%"}, "matched": 12,
     "link_params": {"...": "as above"}, "counters": {"...": "as above"}}
  ],
  "schedule": {"entries": 3, "applied": 1, "repeat": "4s", "round": 1, "waiting_for_traffic": false,
               "elapsed": "1.044s", "next_at": "2s"}
}
```

`link_params.profile` is the template the line was last built from. A direction's `vary` is the
swing range, `rate` the rate at this moment. `stall.now` says whether a random stall is on. A
sublink's `matched` counts the connections it took. `schedule.round` is the current round of
`schedule_repeat`, counted from 0; `elapsed` and `next_at` count from the start of the round, like `at`;
`waiting_for_traffic` means a `first_use` schedule waits for the first traffic.

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

In an app on libbox (Android, iOS) the commands are methods of `CommandClient`; the answer is an
`ImpairStatus` object with the status JSON in its `Content` field:

```java
ImpairStatus setImpair(String outboundTag, String entry) throws Exception;
ImpairStatus scheduleImpair(String outboundTag, String entries, String start, String repeat) throws Exception;
ImpairStatus getImpairStatus(String outboundTag) throws Exception;
```

## How datagrams and streams are degraded

There is one bottleneck per `impair` instance and per direction. Every connection through this
outbound shares it, as connections share a real link. Each unit of data (a datagram, or a stream
chunk of up to 4 KB) joins the queue. Transmission starts when the bottleneck is free and ends after
size/rate, and delivery happens `delay` ± `jitter` after the end of transmission. If the wait in the
queue exceeds `queue`, the queue is full. A rate change (`vary`, a schedule entry, `impair set`)
applies from the next unit. A sublink is a second queue of the same kind behind the outbound's one.

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

During a silent outage of sending or a random `stall` a connection (both TCP and a UDP socket) does
not open: the attempt waits for it to end, but no longer than the connect timeout (the outbound's
`connect_timeout`, 5 s by default), and then fails with a timeout, like a handshake that gets no
answer. A stall longer than `connect_timeout` therefore fails the opening. An outage only on receive
does not hold the dial: the connection opens and gets no answer. An outage with `reset` refuses at
once.

A reset gives ECONNRESET with its cause: `impair: link reset by schedule at 1m0s` (a schedule
entry), `impair: link reset by impair set` (the command), `impair: link reset by sublink [domain
site3.test]` (a sublink with `reset`).

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

Validation errors carry the sing-box prefix and the word `impair`, with the path to the key, for
example:

```text
FATAL[0000] decode config at config.json: outbounds[1].rate: json: unknown field "rate"
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: link_params.rate: invalid rate "10mbps": want a number with bit, kbit, mbit or gbit ("10mbit"), or "unlimited"
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: link_params.vary: min 12mbit is above max 3mbit
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: link_params.reset: only a schedule entry, a SetImpair command or a sublink can reset
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: link_params.profile: unknown profile "nope" (known: "dsl", "lte-walk", "wifi-lossy")
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: schedule[1].at 2s is before the previous entry: list the entries in time order
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: schedule[0].at 5s is not inside one round of schedule_repeat 4s
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: sublinks[0].match is required, with at least one field
FATAL[0000] initialize outbound[1] impair[slow-se]: impair: lx.impair_profiles.x.link_params.reset has no place in a profile
```

A command answers `InvalidArgument` with the same text, for example
`impair: entry: link_params.vary: empty {}: write false to turn it off`.

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
    {"type": "impair", "tag": "slow-se", "seed": 1, "link_params": {"rate": "10mbit", "vary": {"min": "3mbit", "max": "12mbit", "every": "2s"}}},
    {"type": "impair", "tag": "slow-de", "seed": 2, "link_params": {"rate": "10mbit", "vary": {"min": "3mbit", "max": "12mbit", "every": "2s"}}},
    {"type": "impair", "tag": "slow-nl", "seed": 3, "link_params": {"rate": "10mbit", "vary": {"min": "3mbit", "max": "12mbit", "every": "2s"}}},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {"final": "auto"}
}
```

The other vless keys (TLS, transport) are the same as for a proxy without `impair`. The mean rate of
each link is 7.5 Mbit/s, the middle of 3..12.

## Recipe: an evening on the metro

One line under a proxy, a commute on a loop: the train between stations, a tunnel with no signal,
a station with congested Wi-Fi. The schedule starts with the first traffic and repeats every
3 minutes.

```json
{
  "lx": {"impair_profiles": {
    "metro":  {"link_params": {"profile": "lte-walk", "loss": "3%", "stall": {"every": "30s", "duration": "3s"}}},
    "tunnel": {"link_params": {"profile": "metro", "outage": true}}
  }},
  "outbounds": [
    {"type": "vless", "tag": "via-se", "server": "<SE>", "server_port": 443, "uuid": "<UUID>", "detour": "commute"},
    {"type": "impair", "tag": "commute", "seed": 7, "start": "first_use", "schedule_repeat": "3m",
     "link_params": {"profile": "metro"},
     "schedule": [
       {"at": "0s",   "link_params": {"profile": "metro"}},
       {"at": "100s", "link_params": {"profile": "tunnel"}},
       {"at": "130s", "link_params": {"profile": "metro"}},
       {"at": "150s", "link_params": {"profile": "wifi-lossy"}}
     ]}
  ],
  "route": {"final": "via-se"}
}
```

`tunnel` is `metro` with the line gone: connections hang and do not break, as in a real tunnel. For
a carrier that drops the connection on entering the tunnel, write
`{"profile": "tunnel", "reset": true}` in the 100 s entry.

## Recipes for balancing tests

A `round_robin` stand where each member is `impair` over its own node, so `impair` sees the
destination of every connection and can fail one site:

```json
{
  "lx": {"impair_profiles": {
    "metro": {"link_params": {"profile": "lte-walk", "loss": "3%"}}
  }},
  "outbounds": [
    {"type": "urltest", "tag": "pool", "outbounds": ["node-a", "node-b", "node-c", "node-d"], "interval": "15m",
     "mode": "round_robin", "balancer": {"pool": 3}},
    {"type": "impair", "tag": "node-a", "detour": "via-a", "seed": 1, "link_params": {"delay": "30ms"}},
    {"type": "impair", "tag": "node-b", "detour": "via-b", "seed": 2, "link_params": {"profile": "metro"}},
    {"type": "impair", "tag": "node-c", "detour": "via-c", "seed": 3, "schedule_repeat": "2m",
     "schedule": [
       {"at": "0s",  "link_params": {"outage": false}},
       {"at": "80s", "link_params": {"outage": true}}
     ]},
    {"type": "impair", "tag": "node-d", "detour": "via-d", "seed": 4,
     "sublinks": [{"match": {"domain_suffix": "example.org"}, "link_params": {"outage": true, "reset": true}}]},
    {"type": "vless", "tag": "via-a", "server": "<A>", "server_port": 443, "uuid": "<UUID>"},
    {"type": "vless", "tag": "via-b", "server": "<B>", "server_port": 443, "uuid": "<UUID>"},
    {"type": "vless", "tag": "via-c", "server": "<C>", "server_port": 443, "uuid": "<UUID>"},
    {"type": "vless", "tag": "via-d", "server": "<D>", "server_port": 443, "uuid": "<UUID>"}
  ],
  "route": {"final": "pool"}
}
```

- `node-a` — an even link, the control.
- `node-b` — the `metro` profile: rate swings, a deep buffer, 3 % loss in runs. Over a vless stream
  the losses stop the stream for at least 200 ms each time: the node works, slowly.
- `node-c` — a flapping node: in every 2-minute round the line works for 80 s and silently goes away
  for 40 s. The node is struck out and comes back; a second strike-out between scheduled checks
  sends it to quarantine ([pool self-heal](lx-urltest.md#pool-self-heal)).
- `node-d` — the node fails one site: connections to `example.org` are refused at once, the rest
  and the probes pass. The group retries the refused connection through another node, and from the
  second refusal the site moves to the next node of the pool, while the other sites stay
  ([what the application sees](lx-urltest.md#what-the-application-sees-when-a-node-fails)).

Here `impair` stands over the node, so it degrades the proxy's content rather than the road to the
server; for TCP tests this is the same. For a slow node at the 15 s probe timeout put
`{"delay": "2s", "jitter": "1s"}` on a member: a probe makes several round trips of about 4 s each,
so some probes fit into 15 s and some do not; tune `delay` by the probe delay on your stand.

`failover` whose current node first slows down and then breaks connections, with `impair` under the
proxy:

```json
{
  "outbounds": [
    {"type": "urltest", "tag": "main", "outbounds": ["via-a", "via-b"], "interval": "1m", "mode": "failover"},
    {"type": "vless", "tag": "via-a", "server": "<A>", "server_port": 443, "uuid": "<UUID>", "detour": "line-a"},
    {"type": "vless", "tag": "via-b", "server": "<B>", "server_port": 443, "uuid": "<UUID>"},
    {"type": "impair", "tag": "line-a", "start": "first_use", "seed": 1, "schedule": [
      {"at": "60s",  "link_params": {"delay": "400ms"}},
      {"at": "240s", "link_params": {"delay": "0s"}},
      {"at": "300s", "link_params": {"outage": true, "reset": true}},
      {"at": "310s", "link_params": {"outage": false}}
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
