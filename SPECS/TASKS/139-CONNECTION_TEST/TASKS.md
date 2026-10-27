# TASKS 139 — StartConnectionTest

> 🌐 Русская версия: **[TASKS.ru.md](TASKS.ru.md)** (a Russian copy for reference; this English file is primary).

Two releases (SPEC §9). Release 1 closes #59;
release 2 is scoped from its field experience.

## Release 1

### Stage 0 — before code

- [x] FEATURE 006: the call in the table, rules and boundaries of the test as a black box (CONSTITUTION §3.1 в)

### Stage 1 — wire and skeleton

- [x] `started_service.proto`: `rpc StartConnectionTest` and the messages of SPEC §3.1 inside `lx:begin/end lx_command`
- [x] `make -f Makefile.lx lx-proto`; revert protogen noise and gofumpt in `submodules/wireguard-go`
- [x] `common/lxtrace`: holder, `Begin`/`End`/`Mark`/`Attr`/`LostRace`/`Routed`, nesting via ctx, stream emit, resolve-cycle check, stage cap 512, root cause per SPEC §5.2; stub without the tag
- [x] Handler + stub: validation, semaphore of 4, timeout, source metadata (`PackageManager` only when present), cancellation
- [x] Through a node: the 058 path + trace
- [x] Through the router: `net.Pipe` → `RouteConnectionEx`, error from `onClose`
- [x] Insertion in `route/route.go`: `lxtrace.Routed` before the trackers (`route` stage + `DEPTH_ROUTE` stop, TCP and UDP)

- [x] Insertion in `route/conn.go`: first-hop `dial` around `DialContext` / `DialSerialNetwork` (TCP and UDP) — separate commit
- [x] HTTP client with `httptrace`: `target_tls`, `first_byte`, `http`
- [x] Root, result, `stagesTruncated`; all test connections closed at the end (SPEC §5.14); `OnError` only for transport failures (SPEC §3.3)

### Stage 2 — path to the node

- [x] Insertions: `default.go` (`tcp_connect`, conn wrapper), `detour.go` (`detour`, nested `dial`), `resolve.go` (`lookup` + cycle)
- [x] Insertions in `protocol/wireguard/endpoint.go` `DialContext`: `lookup` by rules, `tcp_connect` inside the tunnel
- [x] DNS: `client_log.go` → `dns` stage; insertion in `dns/router.go` (matched rule) — separate commit
- [x] TLS: insertion in `common/tls/client.go` (`tls`)
- [x] Our packages: XHTTP `transport`, `chain`, groups (choice, slot, `IsProbe` without penalties, history, degradation and stickiness)
- [x] `proxy_header` `NO_ACK` by node type
- [x] External modules: one `quic + auth` / `auth` stage per dial, classified by the error

### Stage 3 — depth and appDNS

- [x] `app_dns` for A, AAAA, HTTPS with the source metadata; `DisableCache` from `fresh` passed directly
- [x] `DEPTH_DNS` (implies `appDNS`), `DEPTH_ROUTE` (via `Routed`), `DEPTH_CONNECT` (end of the first-hop `dial`)
- [x] `network = "udp"` up to the route
- [x] `connectIP` without fakeip findings

### Stage 4 — libbox and CLI

- [x] Request with setters, handler, session, getters, iterators
- [x] 038 guard with no exceptions
- [x] `api connection-test`

### Stage 5 — verification

- [x] Stand `lx-test/conntest`: in-process shadowsocks ×2 + trojan/TLS servers, test DNS over TCP, httptest target (Appendix A's AWG/Hysteria2/REALITY/XHTTP servers are not in-process — see IMPLEMENTATION_REPORT)
- [x] SPEC acceptance criteria 1, 3, 8, 9, 10, 11; criterion 2 without the finding
- [x] `-race` on `lxtrace`, `daemon` and the touched packages; build with and without the tags
- [x] lx-ci entries for the new packages: lx-ci runs tests only from an explicit list
- [x] `gofmt` on lx files

### Stage 6 — documents

- [x] FEATURE 006: sync with what landed
- [x] SPEC §10: insertion counts as landed
- [x] IMPLEMENTATION_REPORT.md
- [ ] Reply in #59 with a link to the commit, close it

## Release 2

### Stage 7 — reuse and fresh at nested levels

- [x] `app_dns` attrs from the field run (#59): one `answer` per record, `authority` on NXDOMAIN / NODATA, `source` / `cache_age`; `cache_age` on the cached `dns` stage (2 insertions in `dns/client.go`)
- [ ] Insertions in `dns/client.go` (`DisableCache` from `lxtrace.Fresh`) and `conn_pool.go` (`created == false` → `REUSED`, `fresh` → new connection)
- [ ] WG/AWG: `wake`, `wg_handshake`; `reuse=uncontrolled` for external modules
- [ ] MASQUE and TrustTunnel `auth`

### Stage 8 — REALITY and fakeip

- [ ] Verdict in `reality_client.go` (`accepted` / `dest_cert` / `invalid_cert`)
- [ ] lx access to the fakeip store; wrap-counter insertion in `store.go`
- [ ] `connectIP` from the fakeip range: `route` `FAILED` + findings

### Stage 9 — findings

- [ ] `findings_lx.go`: all codes of SPEC §6 (`ip_rules_not_evaluated` from the selected rule index, no `matchRule` insertion)
- [ ] Unit tests on synthetic traces

### Stage 10 — verification and documents

- [ ] Stand: add Hysteria2 to `lx-test/conntest`
- [ ] SPEC acceptance criteria 2 (the finding), 4, 5, 6, 7
- [ ] `-race`, builds, lx-ci, `gofmt` as in stage 5
- [ ] FEATURE 006, SPEC §10, IMPLEMENTATION_REPORT.md updated
