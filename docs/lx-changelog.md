---
icon: material/alert-decagram
---

# lx changelog

Changes in the `sing-box-lx` fork (the `lx` features layered on top of upstream
sing-box). Upstream's own changelog is in [changelog.md](changelog.md); this file
tracks only the fork. Versions are tagged `vX.Y.Z-lx.N`; releases are built by
`lx-release.yml`. Tags carrying an `-rc.N` / `-alpha.N` / `-beta.N` suffix publish
as GitHub **pre-releases** and never become "Latest".

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
