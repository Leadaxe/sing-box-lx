---
icon: material/alert-decagram
---

# lx changelog

Changes in the `sing-box-lx` fork (the `lx` features layered on top of upstream
sing-box). Upstream's own changelog is in [changelog.md](../docs/changelog.md); this file
tracks only the fork. Versions are tagged `vX.Y.Z-lx.N`; releases are built by
`lx-release.yml`. Tags carrying an `-rc.N` / `-alpha.N` / `-beta.N` suffix publish
as GitHub **pre-releases** and never become "Latest".

#### v1.14.0-lx.1-rc.18

**Pre-release.** Adds **idle-suspend for WireGuard / AmneziaWG endpoints** (SPEC 020) — on
Android, many live WG/AWG endpoints pin the CPU because each holds a recv-worker `bufsArrs`
(`128 × 65535 × 2` per device ≈ 16 MB) that keeps the Go GC scan-bound even with no traffic.
Idle-suspend brings **idle *and* unreachable** endpoints `Down` to free those buffers, and
wakes them on the next dial. Opt-in via one route field; off by default (byte-for-byte
upstream behaviour when unset).

* **`route.lx_idle_suspend` — suspend idle, unreachable WG/AWG endpoints.** Set it to a
  duration (e.g. `"5m"`); `0`/omitted disables the feature (kill-switch). An endpoint is
  suspended only when it is **both** idle past the threshold **and** unreachable from the
  active routing tree — i.e. nothing can currently route to it: not the `final` outbound, not
  a routing-rule target, not a selector's active pick, not a member of a `urltest` group's
  current pool, and not detoured-to by any of those. A suspended endpoint's recv-workers exit
  and its `bufsArrs` are freed; the next dial through it wakes it (`Up`, a fresh handshake —
  expected, the Down model zeroes the crypto session). Edge-triggered INFO logs
  `lx idle: suspend <tag>` / `wake <tag> by=dial`.

* **Reachability is resolved by an event-driven walk, not per-tick.** The reachable set is
  recomputed only when the active routing tree changes — a selector switch, a `urltest`
  auto-switch / pool rebuild, or a config reload each invalidate a cached set; between events
  the idle tick does one comparison per endpoint. The walk descends selectors via `Now()`,
  a `round_robin` pool via its whole active set, and static detours transitively, with a
  cycle guard.

* **Why suspend and not a smaller receive batch.** SPEC.md's first-cut fix was to shrink
  `StdNetBind.BatchSize()` (128→8) to make `bufsArrs` smaller. Code recon of the wireguard-go
  submodule rejected it: GRO receive is enabled on Android and its coalesced-packet split
  hardcodes `IdealBatchSize`=128 (the message array must hold a 64-datagram expansion), so a
  smaller batch panics / overflows and GRO can't be dropped (it's needed for download
  throughput, §010). `Down` is the only lever that frees the buffers whole while leaving the
  active node's batch — and its GRO — intact.

* **Concurrency & correctness.** The idle goroutine is stopped and joined before endpoints are
  torn down (no use-after-close race on the device); `SuspendIfIdle` and the dial-path wake are
  mutually excluded by a per-endpoint mutex (no dial-lands-on-a-suspending-device drop); the
  legacy `least_test` cold-start auto-switch now invalidates reachability (so a freshly-selected
  active node is never wrongly suspended); AWG-over-WG guard-suspended endpoints are never
  idle-woken. Unit tests cover the walk, the idle logic, the event cache, and the
  endpoint-manager iteration seam.

* **Device-verified (2026-07-01, all 8 test nodes):** suspend fires for idle+unreachable
  endpoints and never for reachable ones (final / rule target / selector pick / urltest pool);
  wake-on-dial works; a selector switch dynamically re-suspends the deselected node and wakes
  the new one; the kill-switch (`0`) runs zero ticks. Resource win measured: recv-worker
  goroutines 16→0, RSS −31%. See `SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/TEST_PLAN_idle_suspend.md`
  §RESULTS.

#### v1.14.0-lx.1-rc.17

**Pre-release.** Fixes a build-tag regression that shipped in every desktop/CLI release
since rc.1: `with_clash_api` was dropped from **all** platforms, not just the Android AAR
it was meant for. No data-path change; desktop binaries only.

* **Desktop/CLI binaries get the Clash API back.** SPEC 014 dropped `with_clash_api`
  because LxBox (Android) manages the core over the native libbox `CommandClient` — so on
  the **AAR** the Clash REST server is dead weight. But the drop landed in the shared
  `Makefile.lx` `LX_TAGS`, which also feeds every desktop/CLI release build
  (mac/windows/linux-musl, via `make -s lx-print-tags`). A CLI binary has **no native
  CommandClient channel** — it is driven by external dashboards (yacd, MetaCubeXD,
  clash-dashboard) over the Clash REST API — so dropping the tag left desktop users with
  no way to manage the core: a config with `experimental.clash_api` failed fast. CI stayed
  green the whole time (`lx-ci.yml` kept `with_clash_api` in its check tags), so the bug
  was invisible outside the release artifact. **Fix:** `with_clash_api` is restored to the
  desktop `LX_TAGS`; the AAR tag set (`build_libbox`) still drops it. The two sets now
  diverge by design — desktop = with Clash API, AAR = without. Verified: the desktop
  binary builds with `with_clash_api`, `check` accepts an `experimental.clash_api` config,
  and the Clash REST server comes up live (all endpoints answer instead of the stub's
  fail-fast).

#### v1.14.0-lx.1-rc.16

**Pre-release.** Full client-side support for the extended Xray/sing-box-extended **XHTTP**
parameters (SPEC 002 v2), on the existing lean-native client — no Xray vendoring. The default
(non-obfs) wire shape is unchanged and stays byte-identical to the live-verified v1, so existing
configs behave exactly as before; the new fields are all opt-in.

* **12 client-relevant XHTTP params + 2 tuning fields.** On a `xhttp` transport you can now set:
  - **session/seq placement** — `session_placement` / `seq_placement` (`path` default, or
    `query`/`header`/`cookie`) with `session_key` / `seq_key`.
  - **uplink-data placement** — `uplink_data_placement` (`body`/`auto` default, or `header`/`cookie`
    with chunked base64) + `uplink_data_key` + `uplink_chunk_size`.
  - **`uplink_http_method`** — upper-cased; `GET` allowed only in packet-up.
  - **X-Padding obfuscation** — `x_padding_obfs_mode` + `x_padding_placement`
    (`cookie`/`header`/`query`/`queryInHeader`) + `x_padding_key` / `x_padding_header` +
    `x_padding_method` (`repeat-x` or `tokenish`, the latter HPACK-Huffman-length-tuned).
  - **packet-up tuning** — `sc_max_each_post_bytes` (POST split threshold) and
    `sc_min_posts_interval_ms` (anti-burst throttle).
  Range fields use the `"min-max"` string form. Four server-only fields
  (`server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts`, `sc_stream_up_server_secs`)
  and the legacy `sc_max_concurrent_posts` are accepted but ignored by the client.

* **Wire-protocol audit: 0 confirmed mismatches.** Every new param was checked byte-for-byte
  against `PARAM_MAP.md` (an audit of Xray-core `splithttp` + sing-box-extended) — base64 variant
  (`RawURLEncoding`), chunk naming (`X-Data-<i>` / `x_data_<i>`), default keys (`X-Session` header
  vs `x_session` query/cookie), path-segment order, and the `["none"]`-style defaults all match
  Xray's normalizers. 16/16 unit tests, `sing-box check` on the full-obfs config, `go vet`/`gofmt`
  clean.

* **Verification status.** The default path (packet-up + `auto`→stream-one on reality) is
  **live-verified** on 4 real public nodes (1 MB download each), which also closes the task-011
  stream-one TODO. The non-default obfs/placement modes are covered by unit tests + `check` + the
  wire audit but are **not yet live-tested** against an Xray server configured for them (no public
  node uses them).

Also merges upstream/testing (version bump, linux ping fix).

#### v1.14.0-lx.1-rc.15

**Pre-release.** Device verification of SPEC 019 v2 `round_robin` on a real pool surfaced
**three** bugs, all fixed here. The headline one: with the default `sticky_hash:
["process","domain"]`, **every connection collapsed onto a single pool node** — the domain
component was always empty at selection time, so the key degenerated to the process alone and one
browser pinned all its traffic to one slot. On a device this measured 28/1/1 across a 3-node pool
(uniformity 0.27); after the fix the same traffic spreads across the pool (0.95+).

* **The sticky key's `domain` component was always empty, collapsing all traffic to one node.**
  The router resolves a domain destination to an IP and overwrites `metadata.Destination` *before*
  a group's `DialContext` runs, so `destination.Fqdn` is empty when the balancer builds the sticky
  key. `stickyComponent("domain")` read `destination.Fqdn` and got `""`, so a single process's key
  was `process + "\0"` for every site — one fixed slot. The original domain survives in
  `metadata.Domain` (set by sniffing / reverse mapping); the key now reads that, falling back to
  `destination.Fqdn` only for a direct dial. `dest_ip` was unaffected (the IP *is* present at dial
  time), so adding it to `sticky_hash` is a viable workaround on an already-built core.

* **Living pool nodes could change slot index during a health-check, moving sticky keys.** The
  SPEC invariant is *replace-in-slot*: a living node always keeps its exact slot index; only an
  evicted slot's occupant changes (sticky binds keys to `slot[hash(key) % pool]`). Two code paths
  broke it:
  - `balancePoolFirstLive` (`pool_tolerance: 0`) rebuilt the pool with a filtering `append`, so a
    transiently-dead slot left no placeholder and every living node *after* it shifted left one
    slot. On a device this produced a single `DE→FI→DE` outlier for one key.
  - `planTolerantPool` (`pool_tolerance > 0`) did `delete(inPool, occupant)` on eviction, letting
    the evicted-but-living node re-enter a *later* slot — relocating it (and cascading across
    slots from one fast newcomer).
  - the manual `URLTest` rebuild ran through the tolerant planner even at `pool_tolerance: 0`,
    re-ranking a stable first-live pool by delay and reshuffling living nodes.

  All three now preserve slot index (fixed-length `copy(current)`, only dead/empty slots rewritten
  in place; manual rebuild at `pool_tolerance: 0` uses a dedicated first-live planner). Regression
  tests pin survivors to their slot indices (they fail against the pre-fix code).

* **Stickiness could not be disabled — `sticky_hash: []` was silently ignored.** The design used a
  bare `[]` to mean "off" (vs. omitted = default), relying on `encoding/json` distinguishing a nil
  slice from an empty one. But the sing-box config decoder (`badjson.UnmarshallExcludedContext`)
  re-marshals each outbound, and an empty JSON array does not survive that round-trip — it arrives
  as nil, indistinguishable from "omitted", so the default `["process", "domain"]` always kicked in
  and a `round_robin` group never actually rotated. A local run pinned every connection to one node
  until this was found. **Fix:** disabling stickiness now uses the explicit sentinel
  `sticky_hash: ["none"]`, which survives any re-marshal. Omitted or `[]` → default; `["none"]` →
  off (pure even rotation); `"none"` mixed with a real component → error. Confirmed locally:
  `["none"]` rotates 10/10/10, `["domain"]` pins each domain to one node.

#### v1.14.0-lx.1-rc.14

**Pre-release.** Desktop smoke-test follow-up to rc.13's SPEC 019 v2. No behaviour change to
the pool/sticky logic — one validation fix found by running the rc.13 binary locally.

* **`balancer.pool: 0` is now the default, not an error.** A Go `int` with `omitempty` cannot
  distinguish `0` from an omitted field, so `pool: 0` reaching `< 1` validation rejected a
  config that should have used the default. Now `pool` `0`/omitted → default `3`; only a
  negative `pool` is rejected. Verified on the rc.13 desktop binary alongside the round_robin
  pool fill (`pool_tolerance: 0` tests only pool-many nodes; `> 0` tests all), config
  fail-fast, and live routing.

#### v1.14.0-lx.1-rc.13

**Pre-release.** Reworks `urltest` `round_robin` (SPEC 019 v2) to scale to large node lists
(hundreds–thousands) and exposes the rotation pool to clients. **Breaking** for the rc.11/12
`round_robin` shape — config moved under a `balancer` object; `least_test` (the default) is
untouched. Not yet device-verified.

* **Fixed-size rotation pool.** `round_robin` no longer rotates over *all* live nodes (which
  meant URL-testing every node each interval — unworkable at 1000 nodes). Instead it keeps a
  fixed pool of `balancer.pool` nodes (default 3) and rotates only within it. The pool is
  lazily health-checked: with `pool_tolerance: 0` (cheap mode) the core tests no more nodes
  than needed to keep the pool full of live nodes and then stops; with `pool_tolerance > 0`
  it tests all nodes and keeps the fastest, evicting a pool member only when an outsider beats
  it by more than the tolerance (ms). A dead pool node keeps its slot until a live replacement
  is found — the pool never empties. A dial error never changes the pool (the cause — dead
  node vs. dead destination vs. local network drop — is unknowable from one failure); only the
  health-check does.
* **New config shape.** Balancing options moved into a `balancer` object on `urltest`:
  `{ "mode": "round_robin", "balancer": { "pool": 3, "pool_tolerance": 0, "sticky_hash": ["process","domain"] } }`.
  `balancer` is only valid with `round_robin` (error otherwise); the upstream `tolerance`
  field is ignored in `round_robin` (warned). `sticky_hash` omitted → defaults to
  `["process","domain"]`; explicit `[]` disables stickiness.
* **Sticky via fixed slots (strict zero reconnects).** Stickiness now binds a flow's key to a
  fixed *slot index* (`slot[hash(key) % pool]`), not a node position. Because slot indices
  never move and a replacement takes the exact slot it evicts, a node that stays in its slot
  keeps **all** its keys when other slots churn — zero needless reconnects, zero per-key state
  (no table to grow or sweep). Replaces both rc.11 sticky mechanisms (`jumphash` over the live
  list — which broke on mid-list eviction — and `ttl_map`), which are removed.
* **`GetPool` RPC.** New `CommandClient.GetPool(groupTag)` returns the current pool — one
  `PoolSlot{slot, tag, delay}` per slot — so the client can show which N nodes are actually in
  rotation, with their delays, instead of the full config list. `delay` is `0` only for a
  dead/unmeasured node (a live sub-ms node is clamped to 1). A non-`round_robin` group returns
  an empty pool, not an error. Additive proto/libbox, behind `with_lx_command`.
* **`least_connection` dropped** from the roadmap: `round_robin` is statistically even, and
  per-node active-connection counting (with decrement-on-close, leak risk) was not worth its
  complexity.

#### v1.14.0-lx.1-rc.12

**Pre-release.** One UI-facing fix on top of rc.11's SPEC 019 load-balancing: the
`urltest` group now reports the node it actually dials during the cold-start window,
instead of showing blank. No data-path change.

* **`urltest` `Now()` cold-start fallback** (SPEC 019). Before the first URL-test fills
  the delay history, `selectedOutbound*` is still nil — but traffic already flows through
  the `Select()` fallback (the first usable outbound). Previously `Now()` returned `""` in
  that window, so the UI showed no server even though connections were live. `Now()` now
  falls through to `Select(tcp)`/`Select(udp)` and reports the same node the next
  `DialContext` will pick — the same source of truth the dial path uses, not a guess. Only
  `least_test` (the default) is affected; `round_robin`/`ttlmap` already reported the
  last-picked tag and are unchanged. (Note: when the process is not unloaded from memory —
  a fast config restart on Android — the in-memory history survives, so `Now()` shows the
  prior pick immediately with no warm-up; that's upstream behaviour.)

#### v1.14.0-lx.1-rc.11

**Pre-release.** Adds load-balancing to the `urltest` group (SPEC 019, live-verified on
5 vless nodes — see `SPECS/019-URLTEST_MODE_STICKY/TEST_REPORT.md`) and lands the SPEC 016
connections-map mutex. No data-path change for existing configs — `urltest` without `mode`
behaves exactly as before.

* **`urltest` gains a `mode`.** `least_test` (default, unchanged: pick the lowest-delay node)
  plus `round_robin` (rotate across the live nodes — those with a fresh URL-test result that
  support the network). Selection runs once per connection, so a UDP/QUIC session stays on one
  node; the existing health ticker is the single source of liveness; when nothing is live the
  first usable outbound is the fallback. `least_connection` is reserved (phase 2) and currently
  rejected at config time. Implemented as a separate branch in `DialContext`/`ListenPacket`
  (`protocol/group/urltest_balance_lx.go`) so the legacy `selectedOutbound*` cache path is
  untouched.
* **`sticky` binds one flow to one node.** Optional object `{mode, timeout, cap, hash}` for the
  balanced modes. `hash` selects key components (`process`, `domain`, `source_ip`, `dest_ip`,
  `dest_port`), concatenated in order; an absent component contributes `""`, and an all-empty
  key maps to a single fixed node so keyless flows never rotate. `mode` is `jumphash` (default,
  stateless consistent hash over the live set — adding/removing a node remaps only ~1/n of keys)
  or `ttlmap` (a `key→node` table with lazy + ticker eviction, a `2000`-entry LRU cap, and a
  `10m` TTL; a dead bound node re-pins to a survivor). `Now()` reports the last-picked tag in
  balanced modes.
* **SPEC 016 — `sync.Mutex` on `Connections`** (libbox `command_types.go`). The client-side
  connections accumulator raced its `connectionMap` across per-subscriber goroutines
  (`ApplyEvents` iterating while another writes → `fatal error: concurrent map iteration and map
  write` → process abort with ≥2 `CommandConnections` subscribers). Guarded all map/slice
  mutators; `FilterState` split into public-locks / private-body for non-reentrancy; `Iterator`
  returns a copy so the gomobile caller walks a snapshot, not a live slice. Verified with a
  writer-∥-3-readers race test.

#### v1.14.0-lx.1-rc.10

**Pre-release — not device-verified.** Adds the DNS server + outbound channel to the
DNS-query stream (SPEC 018, LxBox feedback) and gates event construction on an active
subscriber. Behind `with_lx_command`; no data-path change.

* **`DnsQuery` now carries `dnsServer` / `dnsServerType` / `outbound`.** A DNS rule selects a
  *server* (`dns/router.go` matchDNS by `action.Server`), not an outbound; the channel a query
  goes out through is the server's own detour, fixed at config time. `dnsServer` = the
  resolving transport's `Tag()`, `dnsServerType` its `Type()` (udp/tls/https/quic) — both
  available on every emit path because `transport` is the `Exchange` parameter. `outbound` is
  the server's detour tag, captured once at transport creation (`TransportAdapter.OutboundTag()`
  from `DialerOptions.Detour`); the server expands a selector tag to its live node via `Now()`
  when streaming to a subscriber (consistent with `Connection.Detour`, SPEC 017). Empty on
  cached/optimistic — the query never left the device.
* **Events are built only when a profiler is attached.** `dnstrack.Manager` now tracks live
  subscriptions; the emit sites check `HasSubscribers()` before constructing anything. Without
  an open `SubscribeDNSQueries` stream the DNS hot path does zero work — no event, no answers
  slice, no outbound lookup (previously every resolution built an event that was then dropped
  for lack of a listener). The selector `Now()` resolution thus never touches the hot path.
* Wire: additive `dnsServer`/`dnsServerType`/`outbound` on `DnsQueryEvent`; new `OutboundTag()`
  on the `DNSTransport` interface (satisfied by the embedded `TransportAdapter`, no per-transport
  change). libbox `DnsQuery.DNSServer`/`DNSServerType`/`Outbound()`. No client change needed for
  §180 — fields the client reads as soon as the core fills them.

#### v1.14.0-lx.1-rc.9

**Pre-release — not device-verified.** Fixes DNS-query attribution (LxBox §180-2: 0/119
events had a package) and the answer `rdata` format. SPEC 018; behind `with_lx_command`.

* **DNS queries now carry `ProcessInfo`.** On a TUN VPN, DNS is hijacked on a fast-path
  (`route/route.go` — TUN+DNS protocol returns before `matchRule`), and `searchProcessInfo`
  (which fills `metadata.ProcessInfo`) lives *inside* `matchRule`. So every fast-path DNS
  query reached the resolver — and the `SubscribeDNSQueries` emit — with a nil ProcessInfo,
  i.e. unattributed (the bulk of DNS on a VPN, especially UDP). Now `searchProcessInfo` is
  called before both fast-path hijacks (stream + packet). It's idempotent and cached
  (`findProcessInfoCached`), so the cost is one lookup per flow. This corrects SPEC 018's
  пункт 3, whose earlier "cached attribution is correct" claim was wrong — it checked the
  ctx was consistent inside `Exchange` but not that ProcessInfo was populated before it.
* **`DnsAnswer.rdata` is now the bare value.** It was the full RR string
  (`"google.com. 29 IN A 64.233.165.139"`) from `RR.String()`; now the header prefix is
  stripped so clients get `"64.233.165.139"` / the CNAME target directly, no last-field
  parsing. CNAME chain order unchanged.
* No proto/wire change; rc.7 contract intact. LxBox §180 needs no client change — both fixes
  populate fields the client already reads.

#### v1.14.0-lx.1-rc.8

**Pre-release — not device-verified.** Fixes SPEC 018 `SubscribeDNSQueries` returning
`Unimplemented` on device (LxBox §180) — the DNS stream never delivered a single event in
rc.7. Behind `with_lx_command`; no data-path change.

* **DNS query stream was dead in rc.7 — service-registry key mismatch.** `dnstrack.Manager`
  is registered with `service.MustRegisterPtr` (keys on `*dnstrack.Manager`), but both the
  server (`SubscribeDNSQueries`) and the emit sites (`dns/client_log.go`) read it with
  `service.FromContext[*dnstrack.Manager]`, which keys on `**dnstrack.Manager` — so the
  lookup always returned nil. Server-side that surfaced as `codes.Unimplemented "DNS query
  tracking not available"`; emit-side it silently dropped every event (double failure).
  Fixed all three readers to `service.PtrFromContext[dnstrack.Manager]`, the pair of
  `MustRegisterPtr` — exactly how `trafficManager` is resolved (`daemon/instance.go`).
  Verified: the manager now resolves to the same pointer box.go registered.
* No proto/wire change — rc.7's `DnsQueryEvent`/`SubscribeDNSQueries` contract is intact;
  this only fixes the core wiring so the stream actually starts. LxBox §180 needs no client
  change (subscription already in place, was catching the Unimplemented gracefully).

#### v1.14.0-lx.1-rc.7

**Pre-release — not device-verified.** Adds a structured, process-attributed DNS-query
stream (SPEC 018) so a client can observe DNS resolutions with app attribution, instead of
parsing the text log. New `SubscribeDNSQueries` RPC behind `with_lx_command`; no data-path
change.

* **`SubscribeDNSQueries` — live DNS-query stream with `processInfo`.** Hijacked DNS (the
  norm on an Android VPN) is answered before a connection becomes a traffic tracker, so DNS
  queries never appear in the connections stream — the only egress was the text log, which
  carries no package attribution. New `common/dnstrack` (a `Subscriber[QueryEvent]` mirror
  of `trafficcontrol`) emits one event per resolution from `dns/client.go`, attributed via
  `adapter.ContextFrom(ctx).ProcessInfo` (already populated by the router's process search,
  so cache-hits are attributed too, not just cache-misses).
* **Failures are first-class.** Timeout / loopback / rejected-cached / SERVFAIL-reject emit
  a `failed=true` event with `error` and `rcode=-1` (no response) — without this the stream
  would be blind to DNS failures, the primary "DNS is being throttled" signal. Successful
  resolutions carry `source` (exchanged/cached/optimistic/refreshed), `qtype`, `rcode`,
  `ttl`.
* **CNAME chains preserved.** With `includeAnswers` on the subscription, each event carries
  the full `response.Answer` in wire order — CNAME hops AND final A/AAAA, not filtered to
  IPs — so a client rebuilds the CNAME chain from one event. Off by default (size); the
  field exists in proto from v1 for later DNS↔TCP IP-attribution without a proto bump.
* Wire: `rpc SubscribeDNSQueries(SubscribeDNSQueriesRequest) returns (stream DnsQueryEvent)`
  plus `DnsAnswer` (additive); server stream mirrors `SubscribeConnections` (event-driven,
  no ticker); libbox `SubscribeDNSQueries(includeAnswers, handler) → DnsQuerySubscription`.
  Tag-less core answers `codes.Unimplemented` (graceful fallback). `Detour`/`Chain` and all
  other streams unchanged.
* Docs: `SPECS/018-DNS_QUERY_STREAM`.

#### v1.14.0-lx.1-rc.6

**Pre-release — not device-verified.** Adds the transport detour tail of a connection's
final outbound as a new connection field (SPEC 017), so the client can show the real
physical packet path. Additive proto field; `Chain` / Clash-API unchanged. **Also merges
upstream `v1.14.0-alpha.35`** (114 commits since alpha.33) — the lx layer rebased on top.

* **Merged upstream `testing` → `v1.14.0-alpha.35`.** Conflicts resolved keeping all lx
  patches: `box.go` observable gate (`needObservable`, the rc.3 Android-fatal fix),
  trimmed AAR build tags (no `with_clash_api` / `with_usbip`; keeps `with_lx_command` /
  `with_awg` / `with_xhttp`), the lx command-RPC block, and the new `detourList` field.
  AmneziaWG submodule re-graft (`wireguard-go` → `./submodules`, pinned `e5feca7`) was
  preserved against upstream's "Rebase wireguard-go to official". Dependency versions
  (`sing` 0.8.12, `sing-tun`, `sing-cloudflared`) taken from upstream. Generated `.pb.go`
  regenerated; `go build ./...` green, package tests pass. Notable upstream additions now
  in tree: TLS spoof, optimistic DNS cache, USB/IP service (build-tag-gated off for the
  AAR), hysteria2 realm, certificate CGO JNI bridge, oom-killer improvements.
* **Verify on device:** the trimmed AAR omits `with_usbip` by design — confirm no lx
  config references a usbip endpoint (a missing tag fails fast at runtime, no silent
  fallback). Also exercise the WG/AWG path (submodule + dependency churn) and the
  connection stream (`detourList`).

* **`Connection.Detour` — the detour tail `Chain` omits by design.** Upstream `chain`
  answers "how routing picked the final outbound" (selector groups + the chosen node) and
  stops there (`common/trafficcontrol/tracker.go`): its loop only unwinds `OutboundGroup`
  via `Now()` and breaks on the first non-group, so a node's own `detour` (e.g. a node
  detouring through WARP) never appears. That is by design, not a bug — `detour` is a
  transport detail, not a routing choice. New `Detour []string` on `TrackerMetadata`
  unwinds the final outbound's detour tail via `Dependencies()` (which for a non-group
  outbound is exactly its detour, `adapter/outbound/adapter.go`), descending into groups
  through `Now()` against the **same atomic snapshot** so a detour-into-a-group reflects
  the live selection. A `seen` guard prevents detour cycles.
* **Resolved in the core, not reassembled on the client.** Because a detour can point at a
  group whose active node changes at runtime, building the path client-side would mean
  stitching `chain` + `GetOutbounds` + `GetGroups` snapshots that can drift between calls.
  The core resolves it once at connection-creation time (consistent `Now()` across all
  groups); the per-tick `SubscribeConnections` stream just carries the ready field. Wire
  cost is +1 short tag list per connection (usually 1 element); no extra RPC or channel.
* Wire: `repeated string detourList = 23` added to the `Connection` proto message
  (additive — old clients ignore it); mapped in `connectionToProto`; surfaced on the
  libbox `Connection` as `Detour() StringIterator` next to `Chain()`. Order: final
  outbound → outward; full path from the node = `Chain().first ⊕ Detour()`.
* Docs: `SPECS/017-CONNECTION_DETOUR_CHAIN`.

#### v1.14.0-lx.1-rc.5

**Pre-release — not device-verified.** Fixes in-flight cancellation of the per-node
delay test (SPEC 015 §3.6). Behind `with_lx_command`; client/command-side only — no
data-path change.

* **`URLTestOutbound` now honours cancellation of the gRPC call.** The handler parented
  the delay test to the long-lived `boxService.ctx` instead of the gRPC per-call ctx, so
  a cancelled call could not reach the in-flight dial — the test outlived it and the only
  lever was tearing down the whole connection. Now parented to the call ctx (`testCtx :=
  ctx`): dropping the call aborts that single test at its `DialContext`/`client.Do`,
  before `C.TCPTimeout`, without touching other streams. This restores the granular
  per-node cancel the Clash API had implicitly via `r.Context()` (there was never a
  `cancelDelays` endpoint — the cancel lived in the per-request HTTP context).
* **Mass-cancel of a ping batch** is unblocked on the existing gomobile binding with no
  native-surface change: run the ping worker-pool on a *separate* `CommandClient` instance
  and call `Disconnect()` on it — its `cancel()` + `conn.Close()` reach the test ctx and
  kill the in-flight dials, while the main client's Connections/Groups streams stay up.
  No per-call cancel handle and no server-side batch RPC are added (see SPEC 015 §3.6/§5).
* Docs: SPEC 015 §3.6 (cancellation), SPEC 014 (#4240 was deleted upstream — switch the
  `box.go` seam-removal criterion from issue-status to upstream-code).

#### v1.14.0-lx.1-rc.4

**Pre-release — not device-verified.** Completes the command-protocol work (SPEC 015)
needed for the Clash-API → CommandClient migration. All behind `with_lx_command`.

* **`GetGroups` / `GetOutbounds` — unary pull-snapshots of outbound groups and the
  flat outbound/endpoint list.** The native CommandClient was push-only: groups arrived
  solely via the `SubscribeGroups` stream, whose initial snapshot is lost if the stream
  never opened (service not yet `STARTED` at subscribe) or broke. The client then had no
  cheap way to re-read group state — the main screen could stay empty (tunnel connected,
  traffic flowing, `groups=[]`). These two getters fetch the current snapshot on demand,
  Clash-`GET /proxies`-style, without recreating the whole client / tearing down other
  streams. Both are needed: `SubscribeGroups` covers only in-group nodes, whereas
  standalone outbounds and endpoints (WG/AWG/Tailscale) appear only in the flat list.
* **Single-node / empty groups no longer disappear.** `readGroups()` silently dropped
  any group with fewer than 2 items (upstream commit `5bc0dfa9`), hiding single-node
  selectors — a regression vs Clash, whose `/proxies` returned `group.All()` unfiltered.
  `readGroups()` is the single source feeding both the `SubscribeGroups` startup
  broadcast and the new `GetGroups`, so the fix covers both paths.

#### v1.14.0-lx.1-rc.3

**Pre-release — not device-verified.** Fixes a fatal Android start regression
introduced by the `with_clash_api` drop (`rc.1`).

* **Android start no longer fails with "clash api is not included in this build".**
  Upstream `box.go` forced `needClashAPI` whenever a `PlatformLogWriter` was set
  (always true on Android/libbox), because the Clash server used to be the only
  log/traffic observer. With `with_clash_api` dropped that turned every Android
  start into a fatal — even with no `clash_api` in the config. Split the concern
  (`// lx:` seam): the platform writer now requests *observability* (Observable
  log factory + connection/traffic tracker), served by the native CommandClient
  (`SubscribeLog` / `SubscribeConnections`), **not** the Clash server. Only an
  explicit `experimental.clash_api` block still creates the Clash server (and
  still fails fast without the tag). The daemon is already nil-safe to a missing
  Clash server, so Clash-mode degrades gracefully. Desktop was unaffected.

#### v1.14.0-lx.1-rc.2

**Pre-release — not device-verified.** Adds the SPEC 014 libbox command-protocol
extensions on top of `rc.1`; the §010 Android download-stall path is still pending
on-device re-verification, so this stays a `-rc` tag. Client/command-side only — no
data-path change.

* **Native `CommandClient` per-node delay test + rule-table snapshot** (`SPECS/014`).
  Two new RPCs restore, over the native libbox `CommandClient`, what upstream only
  exposed through the (now-dropped) Clash API:
    * **`URLTestOutbound`** — measures the latency of a single node (an outbound **or**
      a WG/AWG/Tailscale endpoint) with a caller-supplied URL and timeout, returning a
      synchronous `{delay, error}`. Unlike the group-level `URLTest` it never requires
      an `OutboundGroup`; mass-pinging stays a client-side worker pool. Errors travel in
      the response payload (not as a gRPC error), and `delay==0 && error==""` is a
      successful 0 ms test — parity with Clash `/proxies/{name}/delay`.
    * **`GetRules`** — a snapshot of the routing rule table, **route and DNS** rules,
      split by `isDNS`. Route fields match Clash `/rules`; DNS rules go beyond Clash,
      which never exposed them (needs a new `adapter.DNSRouter.Rules()` getter).
  Both handlers are gated by the **`with_lx_command`** build-tag (real handler vs a
  `codes.Unimplemented` stub twin, mirroring `started_service_usbip{,_stub}.go`); the
  tag is baked into the Android AAR (`build_libbox` `sharedTags`) and the desktop
  `LX_TAGS`. A tag-less build is behaviourally equivalent to upstream.
* **Pinned, reproducible proto regeneration** (`Makefile.lx`: `lx-proto` /
  `lx-proto-install`). The codegen plugins are pinned — `protoc-gen-go` v1.36.11
  (= go.mod) and `protoc-gen-go-grpc` v1.5.1 — so `*.pb.go` regenerates idempotently
  across a rebase instead of drifting on `@latest`. The `.proto` seam sits under a
  `// lx:` marker; the generated code carries no markers.

#### v1.14.0-lx.1-rc.1

**Pre-release — not device-verified.** First build on the upstream 1.14 base. The
WireGuard-endpoint GRO fix (§010) now lands at the submodule source on AmneziaWG
`v0.0.3` (no downstream guard), but the Android download-stall path has **not** been
re-verified on real hardware yet — hence the `-rc.1` tag. Promote to a plain
`v1.14.0-lx.1` tag once on-device verification passes.

* **Migrated onto upstream sing-box 1.14** (merge of `v1.14.0-alpha.33` into the lx
  layer + AmneziaWG `wireguard-go` submodule re-grafted on `v0.0.3`). Brings the full
  1.14 feature set: the native sing-box API service / remote control, the DNS rework
  (`evaluate` action + `match_response`, optimistic cache, `store_dns`,
  per-evaluation `ip_version`/`query_type`), native Apple/Windows TLS engines, TLS
  spoof, Hysteria2 BBR/NAT-traversal, and closed-connection history in the
  CommandClient connection tracker (1000 entries / 5 min). See upstream
  [changelog.md](../docs/changelog.md) for the per-alpha breakdown.
* **`with_clash_api` dropped from the Android AAR and the desktop `LX_TAGS`.** LxBox
  is moving to manage the core over the native libbox `CommandClient`
  (group / url-test / select / connections streams), so the Clash REST API is dead
  weight on the client. A config that references `experimental.clash_api` now fails
  fast (`clash api is not included in this build, rebuild with -tags with_clash_api`)
  rather than silently degrading. lx configs do not use it.
* **`lx-release.yml`: `-rc.N` / `-alpha.N` / `-beta.N` tags publish as pre-releases**
  (`gh release create --prerelease`), so an unverified build can never displace the
  stable `lx` release as "Latest".

#### v1.13.13-lx.15

* **`package_name_regex` route/DNS/headless rule item.** Backport of the upstream
  1.14 feature onto the stable 1.13.13 base — matches the Android package name by
  regular expression (e.g. `"package_name_regex": ["^com\\.termux.*"]`), the regex
  counterpart of the existing exact-match `package_name`. Works in route rules, DNS
  rules and rule-set headless rules. See `SPECS/013`.
* The full 1.14 migration is **deferred to v1.14.0 stable** (the feature exists only
  in 1.14-alpha upstream; a feasibility pass put the migration at ~1.5–2 days, the
  main cost being the AmneziaWG `wireguard-go` submodule rebase). `lx-rebase.yml`
  excludes alpha/beta/rc by design, so it will pick up 1.14 only once it is stable.

#### v1.13.13-lx.14

* **WireGuard-endpoint GRO split-brain on Android — fixed.** A WireGuard *endpoint*
  without a `detour` killed download throughput on Android (UDP_GRO was enabled, but
  the GRO receive path is linux-only — packets coalesced on send, never un-coalesced
  on receive). Fix gates `UDP_GRO` behind `!android` in the `wireguard-go` submodule
  (`conn/`). Device-verified on real hardware (download 0.44 → 20.7 Mbps). UDP/WG-only.
  See `SPECS/010`.

#### v1.13.13-lx.13

* `ip=quic`: **`ib` now drives a real browser TLS fingerprint.** `ib=chrome`/`firefox`
  build the ClientHello via uTLS (the lib Reality uses) — genuine browser JA3/JA4
  (Chrome 120 / Firefox 120 shape, ALPN h3, PQ key_share stripped to fit one Initial).
  `ib=""`/`curl` keep the generic device-proven ClientHello. Without the `with_utls`
  build tag, `ib` degrades gracefully to the generic CH.
* `ip=quic`: stays a **single** Initial. The brief two-Initial (i1+i2) experiment was
  reverted — two different DCIDs read as two abandoned connections, more anomalous to a
  DCID-tracking DPI, not less; realism comes from the ClientHello, not packet count.
* `ip=dns`: `id` is now **optional** — when absent, the QNAME is a generated
  pronounceable pseudo-domain (no IP, no `sip.`), removing the hardcoded-default
  signature. `id` is now required only for `quic`.
* `ip=sip`: rebuilt as an INVITE + matching `100 Trying` (one dialog, shared
  branch/tag/Call-ID/CSeq) across i1+i2. Still expected to be blocked on the WARP DPI
  (protocol-to-destination class); kept for other providers.
* Docs: dropped internal/AI-voice phrasing from the 009 spec & comments.

#### v1.13.13-lx.12

* **AmneziaWG masquerade (`id`/`ip`/`ib`) — `ip=quic` reworked for real DPI bypass.**
* `ip=quic`: out-of-order fragmented QUIC Initial (RFC 9001) — a realistic
  ClientHello (`id` = SNI) split across CRYPTO frames in a shuffled wire order
  (first frame offset≠0), so a line-rate DPI parses garbage and fails open.
  Replaces the device-blocked 1-RTT short header. **Device-proven on a real
  LTE/WARP DPI (~330 ms)** — the only profile that passes there.
* `ip=quic`: per-call randomized fragment layout (no fixed cross-user signature)
  plus robustness knobs (fragment/PING count, packet size).
* `ip=quic`: multi-packet — fills **both `i1` and `i2`** with two independent
  fragmented Initials, so the flow reads as a developing QUIC session.
  Device-verified to bring the WARP tunnel up with no latency regression.
* `ip=dns`/`stun`/`sip`: rebuilt into correct client-initiated requests (DNS query,
  WebRTC STUN Binding Request, SIP INVITE+SDP with pseudo names). These time out on
  the WARP DPI (it blocks raw DNS/STUN/SIP to a datacenter IP as a protocol class),
  so they are kept for other providers whose DPI only checks packet well-formedness.
* `id` is required for `quic`/`dns` (SNI / QNAME), optional for `sip` (pseudo-host
  generated when absent) and `stun`.
* No submodule changes. See `SPECS/009-WIRESOCK_MASQUERADE_PROFILES`.

#### v1.13.13-lx.11

* AmneziaWG masquerade `id`/`ip`/`ib` (009): declarative WireSock-style sugar over
  `I1` for quic/dns/stun/sip. Live-verified (tunnel + traffic). First release of the
  feature; `ip=quic` later reworked in lx.12.
