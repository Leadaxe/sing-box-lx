# PLAN 139 — StartConnectionTest

> 🌐 Русская версия: **[PLAN.ru.md](PLAN.ru.md)** (a Russian copy for reference; this English file is primary).

A §3.6 "bridge" (a seam in `.proto` under `// lx:`, a handler behind
`with_lx_command`, a stub twin, a wrapper in `CommandClient`) plus a
**trace**: a stage holder in ctx and one-line insertions at pipeline points
that write into it only when it is present. Models: `GetURLViaOutbound`
(058) — error model, body clamp, per-call HTTP client;
`dnstrack.QueryTrace` (035) — holder in an ancestor ctx, write-once
attribution, snapshot at emit. The bridge is the §3.6 part; the trace is
lx logic under §3.2–3.3, with its merge cost and releases in SPEC §9–10.

## 1. Trace holder — `common/lxtrace`

A new package, a pair of files by tag:

- `trace_lx.go` (`with_lx_command`) — the implementation;
- `trace_stub.go` (`!with_lx_command`) — the same functions with empty
  bodies, so insertions in upstream files compile in any build and cost
  nothing without the tag.

API (sketch):

```go
type Trace struct { ... }                         // stages, id counter, stream emitter, request flags
func With(ctx context.Context, t *Trace) context.Context
func Begin(ctx context.Context, kind, subject string) (context.Context, *Stage) // nil without a trace
func (s *Stage) Attr(key, value string)           // nil-safe
func (s *Stage) End(err error)                    // OK / FAILED; nil-safe
func (s *Stage) Mark(result Result)               // REUSED / NO_ACK / NOT_EVALUATED / SKIPPED
func Fresh(ctx context.Context) bool              // request with fresh=true
func IsProbe(ctx context.Context) bool            // test connection — for groups (SPEC §5.7)
func Routed(ctx context.Context, metadata *adapter.InboundContext, rule adapter.Rule, outbound adapter.Outbound) (stop bool) // route stage; true = DEPTH_ROUTE, caller closes
func (s *Stage) LostRace(winner *Stage)           // SKIPPED + lost_race=<winner id> (SPEC §5.13)
```

- **Stage cap:** 512 recorded stages per trace (SPEC §5.15); beyond it
  `Begin` returns a nil stage and bumps a counter that sets
  `stagesTruncated` on the result.

- `Begin` puts the new stage into ctx as the current parent: anything that
  starts in the child ctx becomes its descendant. The resolve nesting
  (SPEC §4) thus comes from ordinary ctx passing, with no knowledge of
  levels.
- **Resolve cycle:** a `lookup` stage walks its ancestors in `Begin`; the
  same "resolver, domain" pair among the ancestors means `FAILED` with the
  `cycle_with` attribute and an immediate error returned to the caller.
- Emit to the stream on `Begin` (RUNNING) and `End`; a buffered channel to
  the handler; overflow does not block the pipeline (the stage is added to
  the final snapshot).
- Without a trace in ctx every function is one `ctx.Value` check and a
  return.

## 2. Handler

`daemon/started_service_conntest_lx.go` + stub. Steps:

1. **Validation** (errors of SPEC §3.2): URL, node tag in both managers
   (as in 058), inbound tag, `depth`/`appDNS`/`network` combinations,
   concurrency limit (a semaphore of 4).
2. **Test ctx:** the per-call stream ctx + the overall timeout (default
   15 s, ceiling 60 s) + `lxtrace.With`.
3. **Source metadata** → `adapter.InboundContext`:
   - `Inbound` = the tag from the request; `InboundType` = the marker
     `lx-connection-test`, so the connection is distinguishable in the list
     (§5.8), while `inbound` rules match by tag and are unaffected;
   - `ProcessInfo` from `packageName`/`uid`/`processPath`. UID from the
     package — via `adapter.NetworkManager.PackageManager()`, which exists
     only with a `tun` inbound on a platform build; otherwise `uid` stays 0
     (SPEC §7). The router searches for
     the process only when `ProcessInfo` is empty
     (`route/process_cache.go:39`) — a pre-filled one is not overwritten,
     no insertion needed;
   - `Source` from `sourceIP` (ephemeral port), `SourceMACAddress` from
     `sourceMAC`, `User` from `authUser`.
4. **appDNS:** for A, AAAA and HTTPS — `dnsRouter.Exchange` with a ctx
   extended with the source metadata, the way the hijack path does it
   (`route/dns.go:20`, `dnsOutbound.HandleStreamDNSRequest`). One `app_dns`
   stage per query.
5. **Through the router:** `net.Pipe()`. The server side goes to
   `router.RouteConnectionEx(ctx, server, metadata, onClose)` with the
   destination `connectIP` / the `app_dns` answer / the URL domain; the
   error from `onClose` is a route or dial refusal (including `reject` and
   "missing fakeip record", which never reach the tracker). The client side
   is the transport of the test's HTTP client.
   **Through a node:** the node's `DialContext`, as in 058.
   The `dial` stage of the first hop opens around the outbound
   `DialContext` / `DialSerialNetwork` in `route/conn.go` (`NewConnection`,
   `NewPacketConnection`); nested hops open it in
   `DetourDialer.DialContext`. `DEPTH_CONNECT` ends the test when the
   first-hop `dial` ends. Closing `dial` from the tracker's conn wrapper
   instead was rejected: the wrapper sees only the first bytes from the
   remote side, so for header-only protocols (VLESS, Trojan) it never
   learns that the tunnel is up.
6. **The test's HTTP client** — as in 058 (per-call client, certificate
   store on Android, `DisableKeepAlives`, body clamp) plus
   `httptrace.ClientTrace`: `TLSHandshakeStart/Done` → `target_tls`,
   `GotFirstResponseByte` → `first_byte`, the response → `http`.
7. **Result:** root (§4 below), findings (§5), HTTP fields, closing all
   test connections.

UDP (`network = "udp"`, only `DEPTH_ROUTE`): `RoutePacketConnectionEx` with
a packet pipe and no payload. Sniffing does not fire — `protocol` rules go
to "not evaluated".

## 3. Capture points

### 3.1 Without editing upstream files

| Stage | Where | How |
|-------|-------|-----|

| `group` for urltest / `round_robin` / failover | lx files `protocol/group/urltest_*_lx.go` | the choice and slot go to the trace; with `IsProbe` — no penalties, history, degradation or stickiness |
| `chain` | `protocol/chain` (our package) | positions and links |
| `transport` XHTTP | `transport/v2rayxhttp` (our package) | method, path, mode, status |
| `auth` / `quic` for MASQUE, TrustTunnel | `protocol/masque`, `protocol/trusttunnel` (our packages) | CONNECT status |
| `wake`, `wg_handshake` | lx code of the WG/AWG endpoint (SPEC 020, 114) | wake from suspend; handshake — from the peer's `LastHandshake` before and after |
| `dns` (server, group, probes) | `dns/client_log.go` (lx) | the same collection as for `QueryEvent`, but into the trace and without the subscriber gate |
| `proxy_header` | lx code of the handler | synthesized from the node type: for VLESS/VMess/Trojan/Shadowsocks after `tls`/`transport` — `NO_ACK` |
| `target_tls`, `first_byte`, `http` | handler | `httptrace` |
| Package excluded from the VPN | handler | TUN options from the `GetRunningConfig` snapshot |
| fakeip state | an lx file in `dns/transport/fakeip` | range, current address, wrap counter (insertion 3.2) |

### 3.2 `// lx:` insertions in upstream files

Each one is an `lxtrace` call; the logic lives in lx files.

| File | Insertion | Why |
|------|-----------|-----|
| `common/dialer/default.go` | `DialContext` / `ListenPacket`: a `tcp_connect` / `udp` stage, a conn wrapper for the first write and first read | connection time; "reset right after ClientHello" |
| `common/dialer/detour.go` | a `detour` stage (tag, domain or address) | tree branching on a detour |
| `common/dialer/resolve.go` | a `lookup` stage (`domain_resolver` / `default_domain_resolver`, server), cycle check | resolving the node's server address |
| `protocol/wireguard/endpoint.go` | `DialContext` (line ~642): a `lookup` stage with `via=dns_rules` around `dnsRouter.Lookup` (lines ~655, ~678) and a `tcp_connect` stage around the netstack dial | a domain handed to the detour is resolved by rules (finding `resolve_via_rules_in_source_context`); the connection inside the tunnel is opened by netstack, not `DefaultDialer`, so `default.go` never sees it |
| `dns/router.go` | `exchangeWithRules` / `lookupWithRules` / the legacy branch: a "rule `dns[i]`" attribute on the current stage | which DNS rule matched |
| `dns/client.go` | `Exchange` / `Lookup`: with `lxtrace.Fresh(ctx)` set `options.DisableCache` (upstream already skips both the cache read and the cache write under it, lines ~287 and ~386); a cached answer — `REUSED` with age | `fresh` on nested lookups, whose options the handler does not control; `app_dns` passes `DisableCache` itself, no insertion |
| `dns/transport/conn_pool.go` | `Acquire*`: `created == false` — `REUSED` with age; with `Fresh` — a new connection, closed after the answer. The dial already runs on the caller's ctx (`dialAndInstall`, line ~283), so the trace reaches the nested dial with no insertion | a reused DNS connection is not passed off as a new one |
| `common/tls/client.go` | `ClientHandshake`: a `tls` stage (SNI, fingerprint, ALPN, version, key share) | — |
| `common/tls/reality_client.go` | verdict `accepted` / `dest_cert` / `invalid_cert` at the existing `verifier.verified` check | REALITY distinction |
| `route/route.go` | before the trackers in `routeConnection` / `routePacketConnection`: `if lxtrace.Routed(ctx, &metadata, selectedRule, selectedOutbound) { conn.Close(); return nil }` — one call writes the `route` stage (rule, action, outbound, final metadata) and stops `DEPTH_ROUTE` | the `route` stage for TCP and UDP from one point, no tracker; `DEPTH_ROUTE` opens no connections and never shows in the connection list. Domain origin is derived from the metadata: `FakeIP` → fakeip, `Protocol` + `Domain` → sniff, `Domain` without `Protocol` → reverse mapping, `Destination.Fqdn` → request |
| `route/conn.go` | `NewConnection` / `NewPacketConnection`: a `dial` stage around the first-hop `DialContext` / `DialSerialNetwork` | the top of the dial subtree; `DEPTH_CONNECT` |
| `dns/transport/fakeip/store.go` | `Create`: a counter and the time of a pool wrap when jumping to the range start | finding `fakeip_pool_wrapped` |

The gRPC, WS and HTTPUpgrade transports (upstream) — response status by an
insertion when needed; not in the first iteration, their error is visible
in `tls` or in the next stage.

### 3.3 External modules — no insertions

Hysteria2, TUIC (`sing-quic`), mux (`sing-mux`), naive (`cronet-go`) do not
report intermediate steps. One `quic + auth` (or `auth`) stage per dial
call; the outcome is classified by the error: Hysteria2 reports a refusal
as `authentication failed, status code: N`. Reuse of QUIC and mux
connections cannot be controlled from outside: with `fresh` such stages get
the attribute `reuse=uncontrolled`.

## 4. Root

SPEC §5.2, literally: among `FAILED` stages, excluding those marked
`lost_race` (SPEC §5.13) and those with an ancestor that ended OK after
them (a recovered step: MASQUE h3 falling back to h2, TrustTunnel replacing a stale
session), take the one that finished first and has no
failed descendants. Stages that did not run because of it are `SKIPPED`.
On timeout, unfinished stages are closed as `FAILED/timeout` and the same
rule applies to them. Lost-race marking: `DialParallelNetwork` and
`DialSerialNetwork` in `common/dialer/default_parallel_network.go` cancel
the losers — the `tcp_connect` insertion in `default.go` sees
`context.Canceled` with a winner present in the parent and calls
`LostRace` instead of `End(err)`.

## 5. Findings

`common/lxtrace/findings_lx.go`: pure functions over the trace snapshot and
the core state (TUN options, fakeip store, rules). Each returns zero or one
finding. Unit-tested on synthetic traces.

- `resolve_via_rules_in_source_context` — a `lookup` stage with
  `via=dns_rules` under a `detour` stage.
- `dependency_outside_route` — a node from a `dial` stage under `dns` that
  is not in the route chain.
- `ip_rules_not_evaluated` — no insertion in `matchRule`: every rule with
  an index below the selected one did not match (non-final actions applied
  along the way are visible in the metadata: `Protocol` for sniff,
  `DestinationAddresses` for resolve). Among those, rules whose options
  contain address conditions while the address is unknown are "not
  evaluated". Rule options — from the config snapshot, as in `GetRules`
  (014).

## 6. libbox and CLI

- `experimental/libbox/command_client_conntest_lx.go` + types: the request
  with setters, handler, session with `Cancel`, stage/result/finding with
  getters, attribute iterators. Shape — after upstream `StartSTUNTest`.
- The 038 guard over the `CommandClient` surface — no exceptions.
- `cmd/sing-box/cmd_api_connection_test_lx.go`: `api connection-test`,
  printing the tree by `parent`.

## 7. Wire

`daemon/started_service.proto` — inside the existing
`lx:begin/end lx_command` block. Regeneration: `make -f Makefile.lx lx-proto`.
Afterwards revert protogen noise and gofumpt edits in the `wireguard-go`
submodule.

## Upstream touch zone

| File | Insertions |
|------|------------|
| `daemon/started_service.proto` | inside the existing lx block |

| `common/dialer/default.go`, `detour.go`, `resolve.go` | 1–2 each |
| `common/tls/client.go`, `reality_client.go` | 1 each (the latter already has lx edits) |
| `dns/router.go`, `dns/client.go`, `dns/transport/conn_pool.go` | 1–3 each |
| `dns/transport/fakeip/store.go` | 1 |
| `route/route.go` | 1 (`Routed`, before the trackers) |
| `route/conn.go` | 1–2 |
| `protocol/wireguard/endpoint.go` | 2 (next to the SPEC 020 lx blocks) |

Every insertion is a one-line `lxtrace` call. The same table by release is
SPEC §10. The most conflict-prone spots on merge are `exchangeWithRules` in
`dns/router.go` and `NewConnection` in `route/conn.go`: upstream edits them
more often than the rest. Their insertions go in a separate commit so they
can be dropped and restored on a conflict without the rest.
