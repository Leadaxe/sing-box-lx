# sing-box-lx — DNS server group

> RU: [lx-dns-group.ru.md](lx-dns-group.ru.md) · Feature: [DNS_GROUP](../SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)

The `group` type in `dns.servers[]` puts several DNS servers behind one tag: if one server does
not answer, the query goes to the others. Upstream has no such fallback. There `dns.final` is a
default server, not a spare, and a query that a rule routes to a server fails without a retry on
a network error, a timeout or `SERVFAIL`. No build tag is needed; a config without `group` works
as upstream.

## Table of contents

- [Fields](#fields)
- [How the group picks a server](#how-the-group-picks-a-server)
- [Local names first: `hosts`](#local-names-first-hosts)
- [What shows from the outside](#what-shows-from-the-outside)
- [Example — a fault-tolerant default public DNS](#example--a-fault-tolerant-default-public-dns)
- [Example — one local name for a WireGuard server with IPv4 and IPv6](#example--one-local-name-for-a-wireguard-server-with-ipv4-and-ipv6)

---

## Fields

```jsonc
{
  "type": "group",
  "tag": "public",
  "hosts": ["local"],               // optional: tags of hosts-type servers,
                                    //   asked first, in this order
  "servers": ["google", "cloudflare", "quad9"], // required, at least one tag;
                                    //   the order does not matter in any mode
  "mode": "stable",                 // stable (default) | fastest | parallel
  "error_ttl": "2m",                // default 2m: how long a server's error is remembered
  "win_ttl": "5m"                   // default 5m: how long a server's win is remembered;
                                    //   fastest only, a warning in other modes
}
```

A group tag is accepted wherever a DNS server tag is: `dns.final`, rules, a node's
`domain_resolver` and `route.default_domain_resolver`.

The `mode: failover` and `race`, `interval` and `down_time` keys of the first group version are
removed: a config with them fails to load.

## How the group picks a server

A failure is a transport error, a timeout or `SERVFAIL`. `NXDOMAIN` and an empty answer are
normal answers.

The group remembers servers' errors and wins, each record for its own time. An error is
remembered for `error_ttl` and erases that server's wins. A win is remembered for `win_ttl`; only
the server that answered first, when the query went to several at once, gets it, and any success
erases the server's errors. A clean server is one with no errors remembered. A network change
erases all of it.

Three modes pick among clean servers:

- `stable` — the group holds the current server while it is clean. Only when the current one gets
  an error does it pick a random clean one. It does not go back to the former server once that
  recovers.
- `fastest` — the clean server with the most wins. When nobody has wins, the query goes to every
  clean server at once, and whoever answers first gets the win. Such elections run one at a time;
  queries meanwhile go to a random clean server. Elections repeat when the wins expire.
- `parallel` — every query goes to every clean server at once, and the group counts no wins.
  Servers get N times more queries.

When the chosen server does not answer, the query goes to every other clean server at once. The
first attempt gets half of the query's remaining time, and the other half is kept for this
fallback. The first success answers and becomes current. The other answers do not go to the
cache, but they clear their servers' errors.

If there are no clean servers, the group makes one attempt to the server with the fewest errors
and does not ask the others. That way a storm of queries is not multiplied by the number of
servers.

> ⚠️ On a failure every mode sends the query name to every clean server, and `parallel` does so
> with every query. Do not mix internal and public resolvers in one group.

## Local names first: `hosts`

The `hosts` key lists `hosts`-type servers that the group asks before picking a server, in the
key's order:

- **Hit** — an A or AAAA query, the `hosts` server knows the name and answers NOERROR. That answer
  goes back as is, and the network is not asked. It is a hit also when the name has an address
  only of the other family: a name with only IPv4 gets an empty answer to an AAAA query, and the
  query does not go to the network. So `strategy: ipv6_only` gives no address for such a name —
  the same as the OS `hosts` file.
- **Miss** — the name is unknown, the query type is different, or the answer is not NOERROR. The
  query goes to `servers` by the group's mode.
- Servers in `hosts` are not remembered as erroring or winning, take no part in the fallback or
  the single attempt, never become current and do not show in `GetDNSGroups`.
- `servers` stays required and non-empty. A `hosts` or `fakeip` server inside `servers` is a load
  error that points to the `hosts` key. `hosts` takes only tags of `hosts`-type servers, without
  repeats and without overlap with `servers`.

The key exists for the addresses of servers themselves: outbounds and endpoints resolve their
own address through `domain_resolver`, and that resolver goes to the server directly, past the
DNS rules. So neither the `predefined` action nor a `server: hosts` rule reaches such addresses.
A `hosts` server cannot be the resolver itself: it answers NXDOMAIN to any other name.

## What shows from the outside

The DNS query stream shows the server that answered; on a cache hit and on a total failure — the
group tag. Every query carries a probe trace: the path through groups, the outcome of each probe
(`answered`, `timeout`, `network_error`, `servfail`) and its time, plus the `fanned` (the query
went to everyone) and `survival` (no clean servers) flags.

`GetDNSGroups` (a build with `with_lx_command`,
[lx-config §8](lx-observability.md)) returns for every server of
the group: whether it is clean, how many errors are remembered and when the last one was, how
many wins, the time of the last answer and whether it is current.

## Example — a fault-tolerant default public DNS

```jsonc
{
  "dns": {
    "servers": [
      { "type": "udp", "tag": "google",     "server": "8.8.8.8" },
      { "type": "udp", "tag": "cloudflare", "server": "1.1.1.1" },
      { "type": "group", "tag": "public",
        "servers": ["google", "cloudflare"],
        "mode": "fastest", "error_ttl": "2m", "win_ttl": "5m" }
    ],
    "final": "public"
  }
}
```

## Example — one local name for a WireGuard server with IPv4 and IPv6

Your own WireGuard server (or WARP), reachable over IPv4 and IPv6 under one local name
(issue #60):

```jsonc
{
  "dns": {
    "servers": [
      { "type": "hosts", "tag": "local",
        "predefined": { "wg.home": ["203.0.113.10", "2001:db8::10"] } },
      { "type": "udp", "tag": "google",     "server": "8.8.8.8" },
      { "type": "udp", "tag": "cloudflare", "server": "1.1.1.1" },
      { "type": "group", "tag": "resolver",
        "hosts": ["local"],
        "servers": ["google", "cloudflare"],
        "mode": "stable" }
    ],
    "final": "resolver"
  },
  "route": { "default_domain_resolver": { "server": "resolver", "strategy": "prefer_ipv6" } }
}
```

`wg.home` in an endpoint's server address gets both addresses from `local`, and `strategy` picks
the family. Any other name goes to `google` or `cloudflare` by the `stable` mode.
