---
icon: material/alert-decagram
---

# lx changelog

Changes in the `sing-box-lx` fork (the `lx` features layered on top of upstream
sing-box). Upstream's own changelog is in [changelog.md](changelog.md); this file
tracks only the fork. Versions are tagged `vX.Y.Z-lx.N`; releases are built by
`lx-release.yml`. Tags carrying an `-rc.N` / `-alpha.N` / `-beta.N` suffix publish
as GitHub **pre-releases** and never become "Latest".

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
  [changelog.md](changelog.md) for the per-alpha breakdown.
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
