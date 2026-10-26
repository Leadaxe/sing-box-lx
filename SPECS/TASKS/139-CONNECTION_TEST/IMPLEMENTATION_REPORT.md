# IMPLEMENTATION REPORT 139 — StartConnectionTest, release 1

> 🌐 Русская версия: **[IMPLEMENTATION_REPORT.ru.md](IMPLEMENTATION_REPORT.ru.md)** (a Russian copy for reference; this English file is primary).

**Status:** I (implemented) — release 1 of SPEC §9 is on `lx` (2026-10-27,
`a0ec238b8` … `cdf9a297a`). Builds with and without the lx tags, `go vet`,
the unit tests of every touched package and the acceptance stand pass under
`-race`. Not done: a device run (LxBox screen, router), the reply in #59 and
the field check of criterion 1 outside the stand. Release 2 (§9) is not
started.

## What landed

| Layer | Files | Content |
|-------|-------|---------|
| Trace holder | `common/lxtrace/` (`trace_lx.go`, `trace_stub.go`, `doc.go`, tests) | ctx-carried stage tree: `Begin`/`End`/`Mark`/`LostRace`, `BeginLookup` with the resolve-cycle check, `Routed` (route stage + selector unfolding + `DEPTH_ROUTE` stop), `WithStage`, `Options{Fresh, Probe, Depth, MaxStages, Emit, Outbounds}`, `Finish` with the §5.2 root-cause rule, stage cap 512, `Summary.Dropped` for end snapshots the stream buffer dropped. The stub twin costs one `ctx.Value` lookup |
| Wire | `daemon/started_service.proto` + regenerated `*.pb.go` | `rpc StartConnectionTest` and the ten messages of §3.1 inside the existing `lx_command` seam, plus `stagesTruncated` |
| Handler | `daemon/started_service_conntest_lx.go`, `daemon/conntest_lx.go`, `_stub.go`, tests | validation (§3.2), semaphore of 4, timeout 15 s / 60 s, source metadata (`InboundType = lx-connection-test`, package manager → uid when present), `app_dns` for A/AAAA/HTTPS, router path over `net.Pipe` → `RouteConnectionEx` (route and dial errors via `onClose`), node path, `httptrace` stages `target_tls`/`first_byte`/`http`, depths, synthesized `proxy_header` (NO_ACK) and `class=auth|quic` on failed hysteria2/tuic/naive dials, `RootCause = 0` on a completed path, every test connection closed at the end |
| Stage helpers (lx files) | `route/conntest_lx.go`, `common/dialer/conntest_lx.go`, `common/tls/conntest_lx.go`, `protocol/wireguard/conntest_lx.go`, `dns/client_trace_lx.go`, `dns/transport/conntest_lx.go`, `protocol/group/conntest_lx.go`, `protocol/chain/conntest_lx.go`, `transport/v2rayxhttp/conntest_lx.go` | `dial` (leaf node through selectors, `via_group`), `detour`, `lookup`, `tcp_connect`/`udp` (lost-race marking), `tls`, `dns` (rule, cache source, group attribution), `group` (mode, selected, slot) with `IsProbe` guards on penalties, history, retest, `lastSelected` and the round-robin counter, `chain`, `transport` (XHTTP: mode, method, path, late status as `transport_response`; pooled reuse marked `REUSED` with age; `fresh` opens a detached transport) |
| `// lx: SPEC 139` insertions | see SPEC §10 | one- or two-line calls into the helpers |
| Client | `experimental/libbox/command_client_conntest_lx.go`, `command_types_conntest_lx.go`, tests | request with setters, handler, session with `Cancel`, getters and iterators, gomobile-safe (`ErrorMessage()` instead of `Error()`), 038 guard passes |
| CLI | `cmd/sing-box/cmd_api_connection_test_lx.go` | `sing-box api connection-test <url>` with a flag per request field, tree output by `parent` |
| Stand | `lx-test/conntest/` | release 1 acceptance criteria (below) |
| CI | `.github/workflows/lx-ci.yml` | one `-race` step over every touched package and the stand |
| Hotfix | `protocol/group/urltest.go` + `urltest_selected_lx.go` | `selectedOutboundTCP/UDP` behind `selectedAccess`: a data race between `performSelectionUpdate` and `Select`/`Now`/`References`, exposed by the SPEC 136 async update; registered in [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) |

## Acceptance (stand `lx-test/conntest`, `-race`)

Topology: a server box with two `shadowsocks` inbounds (S1, S3) and a `trojan`
over TLS inbound (S2, self-signed), a test DNS server over TCP and an
`httptest` target; a client `StartedService` with N1 → S1, N2 → S2 (`server:
node2.test`, `detour: N1`, `domain_resolver: dns-detour`), N3 → S3,
`dns-detour` with `detour: N3`, selector `proxy` [N2, N1], urltest `balanced`,
a `mixed` inbound, a `reject` rule and `domain_suffix target.test → proxy`.

| Criterion | Result | Observed |
|-----------|--------|----------|
| 1 route matches production | PASS | `route` (`rule=domain_suffix=target.test`, `outbound=proxy`) and `group` (`selected=N2`) equal the connection-list entry of a real SOCKS connection from the same source; the test connection is listed as `lx-connection-test` |
| 2 nested tree (finding is release 2) | PASS | `route → group proxy → dial N2 (via_group=proxy) → { lookup node2.test → dns dns-detour → detour N3 → tcp_connect S3; detour N1 → tcp_connect S1; tls node2.test; proxy_header NO_ACK } → first_byte → http 200`, `rootCause = 0`. With S3 down: root = `tcp_connect` under `detour N3`, `dial N2` FAILED, `first_byte`/`http` SKIPPED |
| 3 resolve cycle | PASS | about 1 ms; `lookup node4.test` FAILED with `cycle_with` |
| 8 `DEPTH_ROUTE` opens nothing | PASS | zero accepted connections on S1, S2, S3 and the target; stages `route`, `group` only |
| 9 group state unchanged | PASS | selected node and urltest history equal before and after a test through a dead node; a control real connection does change them |
| 10 cancellation | PASS | S1, S2, S3 and the target see their connections closed, the probe's DNS pool connection included; no result sent; goroutines settle |
| 11 stub | PASS | `codes.Unimplemented` without `with_lx_command`; 038 guard passes |

Criteria 2 (the finding), 4, 5, 6, 7 belong to release 2 (SPEC §9).

## Deviations from the SPEC

- **`lost_race` carries `0`, not the winner id** (§5.13): the `DefaultDialer`
  insertion sees only `context.Canceled`; the race winner is known to
  `DialParallelNetwork`, not to the loser. The root-cause rule is unaffected.
- **`proxy_header` for detours is synthesized at `DEPTH_CONNECT` too**, since
  a detour's header is part of tunnel setup; §7 keeps only the first hop out.
- **XHTTP `http_status` arrives after the `transport` stage ends**: a late
  non-200 becomes a FAILED child `transport_response`, which becomes the root.
  HTTP/1.1 records no `REUSED` stage (each long request dials); HTTP/3 reuse is
  kind `quic`.
- **The `dns` stage ends through the client's log paths** (`emitQueryEvent`
  / `emitFailedQuery`), which a running box always has.
- **`DEPTH_ROUTE` through a node** writes nothing beyond `app_dns`: there is no
  router decision on that path.
- **The stand's topology is not Appendix A**: no AWG, Hysteria2, REALITY or
  XHTTP server in-process; shadowsocks-2022 was replaced by `aes-128-gcm`
  because sing-shadowsocks2 has its own data race (`shadowaead_2022/method.go`
  `requestSalt`) that reddens every `-race` run.

## Found on the way

- **`tls.NewDialer` bypassed `tls.ClientHandshake`** and called
  `aTLS.ClientHandshake` directly, so the `tls` stage never fired for VLESS,
  Trojan, VMess, WS, gRPC, DoT and DoH until the handshake path in
  `defaultDialer.dialContext` got its own `// lx:` pair.
- **The review found two crash-class bugs before the stand did:** a send on
  the closed emit channel (`emit` vs `Finish` without a shared lock) and a
  double close of `routeDone` when `net/http` redials after GOAWAY. Both are
  fixed and covered by tests (`TestFinishRacesEmitNoPanic`, review tests in
  `daemon/conntest_review_lx_test.go`).
- **`net/http` hands `DialContext` a context with cancellation stripped**, so a
  via-node timeout was misfiled as a lost race; the node path dials on the
  test ctx now.
- **`first_byte` must wait for both `WroteRequest` and the top-level `dial`
  end**: on the router path the pipe write completes when the sniffer consumes
  the request, before route and dial.
- **`gofumpt -w .` from `lx-proto` reformats eight tracked files and all four
  submodules**; revert them with `git checkout --` after regeneration.
- **lx-ci runs tests only from an explicit list**: `daemon/` and
  `experimental/libbox/` had no test step before this task.
- **`TestFailover_moveOnDialFailure`** in `protocol/group` is timing-based and
  flaps under load (20 ms vs 80 ms node delays).
- **A full `go build ./...` with `LX_TAGS` on a cold cache takes over ten
  minutes** on the owner's Mac; run it in the background.

## Verification

```
go build ./...  (no tags / LX_TAGS, -checklinkname=0)                 rc=0 / rc=0
go vet (LX_TAGS; -unsafeptr=false for daemon, libbox)                 clean
go test -race -count=1 -tags LX_TAGS ./common/lxtrace/ ./common/dialer/ ./daemon/
  ./experimental/libbox/ ./dns/ ./dns/transport/... ./protocol/group/
  ./protocol/chain/ ./transport/v2rayxhttp/ ./route/ ./protocol/wireguard/
  ./cmd/sing-box/ ./lx-test/conntest/                                 ok
go test -count=1 (no tags) ./common/lxtrace/ ./daemon/ ./experimental/libbox/
  ./lx-test/conntest/ ./dns/transport/ ./route/                       ok
gofmt -l on lx files                                                   clean
```

## Open

- Reply in #59 and close it (after the owner's go-ahead, with the commit link).
- Device run: LxBox screen against this core, `api connection-test` on a
  router with a real DNS detour.
- Release 2 (SPEC §9): `fresh` at nested DNS levels, REALITY verdict,
  `wake`/`wg_handshake`, fakeip, findings.
