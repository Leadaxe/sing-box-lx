# SPEC 139 — StartConnectionTest: active connection test with a stage trace

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (a Russian copy for reference; this English file is primary).

**Feature:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | F (feature). The RPC is a §3.6 bridge; the trace (`common/lxtrace` and its insertions) is lx logic isolated under CONSTITUTION §3.2–3.3, with its merge cost fixed in §10 |
| Status | I (implemented) — release 1 on `lx` 2026-10-27, release 2 on `lx` 2026-10-28; see [IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md) |
| Branch | `lx` |
| Related | [#59](https://github.com/Leadaxe/sing-box-lx/issues/59) (request: DNS through the router and rule match — closed by this task) · [Leadaxe/LxBox#176](https://github.com/Leadaxe/LxBox/issues/176) ("Connection Check" screen) · [[SPECS/TASKS/058-GET_URL_VIA_OUTBOUND]] (HTTP probe through a node — the model for errors and body limits) · [[SPECS/TASKS/018-DNS_QUERY_STREAM]] · [[SPECS/TASKS/035-DNS_GROUP_OBSERVABILITY]] (DNS attribution and group probes) · [[SPECS/TASKS/017-CONNECTION_DETOUR_CHAIN]] · [[SPECS/TASKS/038-GOMOBILE_STRING_RETURN_FRAME_KILL]] (libbox return shape) |

## 1. Problem

When a user reports "it does not open", the core answers today in pieces,
and no piece says **which path the request takes and where it breaks**:

- `URLTestOutbound` and `GetURLViaOutbound` check a single node and bypass
  rules, sniffing and group selection.
- The DNS stream shows queries that already happened, but does not tie them
  to the connection they were made for.
- A connection error arrives as one glued string. The root often lies
  **outside the route**: the node address is resolved by a DNS server that
  itself goes through another node, and that node is dead. The user sees
  "node A does not work" and fixes the wrong node.
- Connections rejected by a rule or failed before tracking never appear in
  the connection list.

Request #59 asked for two calls: a DNS lookup through the router and a rule
match without dialing. A match on synthetic input is inaccurate: without
sniffing, the app, `network` and port, the rules give a different answer
than for a real connection. This task replaces both calls with one test
that has a selectable depth: `DEPTH_DNS` answers the first call of #59
(addresses, latency, the answering server and the matched DNS rule, per
query type); `DEPTH_ROUTE` answers the second (rule, action, outbound, and
the chain with groups and detours unfolded — the `group`, `chain` and
`detour` stages).

## 2. Summary

`StartConnectionTest` is an **active** test: the core makes a real request
through its whole pipeline (DNS → rule → group → node → its detours →
tunnel → HTTP) on behalf of a given source, and records every stage: what
was done, inside which stage, how long it took and how it ended. Stages are
streamed as they run. The last message of the stream is the result: the
HTTP outcome, the root cause and the findings.

Retrospection (analysis of past connections) is not part of this task.

## 3. Contract

### 3.1 Protocol

```proto
// lx:begin lx_command
rpc StartConnectionTest(ConnectionTestRequest) returns (stream ConnectionTestProgress) {}

// On whose behalf the test runs. All fields are optional; fill those the
// rules use to tell sources apart on the given platform.
message ConnectionTestSource {
  string inbound = 1;      // inbound tag; empty — no inbound
  string packageName = 2;  // Android
  int32 uid = 3;           // 0 — not set; on Android derived from packageName when the platform provides a package manager (§7)
  string processPath = 4;  // desktop
  string sourceIP = 5;     // router: address of a LAN device
  string sourceMAC = 6;
  string authUser = 7;
}

message ConnectionTestRequest {
  string link = 1;                      // http/https URL
  string outboundTag = 2;               // empty — through the router; a tag — directly through that node or endpoint
  ConnectionTestSource source = 3;
  bool appDNS = 4;                      // first issue the DNS queries the app would issue
  string connectIP = 5;                 // connect to this address instead of the DNS answer
  bool fresh = 6;                       // false — as in operation (cache, live connections); true — everything from scratch
  ConnectionTestDepth depth = 7;
  string network = 8;                   // "tcp" (default) or "udp"
  uint32 timeout = 9;                   // ms; 0 — default 15000; ceiling 60000
  uint32 maxBytes = 10;                 // as in GetURLViaOutbound: default 256 KiB, ceiling 1 MiB
  repeated HttpHeaderPair headers = 11;
}

enum ConnectionTestDepth {
  DEPTH_HTTP = 0;      // the full path up to the HTTP response
  DEPTH_DNS = 1;       // only the app's DNS queries (implies appDNS)
  DEPTH_ROUTE = 2;     // DNS (if appDNS) + rule, group and node selection; nothing is dialed
  DEPTH_CONNECT = 3;   // up to an established tunnel, no request to the target
}

message ConnectionTestAttr {
  string key = 1;
  string value = 2;
}

enum ConnectionTestStageResult {
  STAGE_RUNNING = 0;
  STAGE_OK = 1;
  STAGE_FAILED = 2;
  STAGE_SKIPPED = 3;        // not run: an earlier stage failed or the depth does not reach it
  STAGE_REUSED = 4;         // not run: something ready was used (cache, live connection, tunnel already up)
  STAGE_NO_ACK = 5;         // run, but the protocol does not confirm its success
  STAGE_NOT_EVALUATED = 6;  // not computed: data was missing (e.g. the address is unknown)
}

message ConnectionTestStage {
  uint32 id = 1;            // unique within the test, increasing
  uint32 parent = 2;        // 0 — top-level stage
  string kind = 3;          // vocabulary — §4
  string subject = 4;       // what the stage is about: tag, domain, address
  uint32 startMs = 5;       // since the start of the test
  uint32 durationMs = 6;
  ConnectionTestStageResult result = 7;
  string error = 8;         // the cause as is; empty unless FAILED
  repeated ConnectionTestAttr attrs = 9;
}

enum ConnectionTestFindingLevel {
  FINDING_INFO = 0;
  FINDING_WARNING = 1;
  FINDING_PROBLEM = 2;
}

message ConnectionTestFinding {
  string code = 1;                      // vocabulary — §6
  ConnectionTestFindingLevel level = 2;
  repeated uint32 stages = 3;           // stages it refers to
  repeated ConnectionTestAttr attrs = 4;
}

message ConnectionTestResult {
  string error = 1;         // error of the test itself (invalid request); empty — the test ran
  uint32 rootCause = 2;     // stage id; 0 — the path was completed
  repeated ConnectionTestFinding findings = 3;
  uint32 httpStatus = 4;
  bytes body = 5;
  bool truncated = 6;
  string contentType = 7;
  uint32 elapsedMs = 8;
  bool stagesTruncated = 9; // the stage cap (§5.15) was hit; the tree is incomplete
}

message ConnectionTestProgress {
  oneof event {
    ConnectionTestStage stage = 1;    // a new stage (RUNNING) or its completion — same id
    ConnectionTestResult result = 2;  // the last message of the stream
  }
}
// lx:end lx_command
```

### 3.2 Error model

| Outcome | Response |
|---------|----------|
| The path was completed and the target server answered with any status | `result.error == ""`, `rootCause == 0`, HTTP fields filled. Non-2xx is data, not an error (as in `GetURLViaOutbound`). A redirect is not followed: the test answers whether this URL opens, so a 3xx is the answer, with its `Location` on the `http` stage |
| The path broke (DNS, `reject` rule, dial, TLS, timeout…) | `result.error == ""`, `rootCause` points to the stage, HTTP fields empty. A connection failure is a test result, not a call error |
| Invalid request: malformed URL, unknown node or inbound tag, `network = "udp"` deeper than `DEPTH_ROUTE` | A stream of one `result` with `error`; no stages |
| The limit of concurrent tests is reached | `result.error = "busy"` |
| The service is not STARTED | transport error (`os.ErrInvalid`), as in `URLTestOutbound` |
| Build without `with_lx_command` | `Unimplemented` (stub twin) |

Cancellation — the client closes the stream. All connections of the test
are closed, no result is sent.

### 3.3 libbox shape

```go
func NewConnectionTestRequest() *ConnectionTestRequest
func (r *ConnectionTestRequest) SetLink(link string)
func (r *ConnectionTestRequest) SetOutboundTag(tag string)
func (r *ConnectionTestRequest) SetInbound(tag string)
func (r *ConnectionTestRequest) SetPackageName(name string)
func (r *ConnectionTestRequest) SetUID(uid int32)
func (r *ConnectionTestRequest) SetProcessPath(path string)
func (r *ConnectionTestRequest) SetSourceIP(ip string)
func (r *ConnectionTestRequest) SetSourceMAC(mac string)
func (r *ConnectionTestRequest) SetAuthUser(user string)
func (r *ConnectionTestRequest) SetAppDNS(enabled bool)
func (r *ConnectionTestRequest) SetConnectIP(ip string)
func (r *ConnectionTestRequest) SetFresh(fresh bool)
func (r *ConnectionTestRequest) SetDepth(depth int32)
func (r *ConnectionTestRequest) SetNetwork(network string)
func (r *ConnectionTestRequest) SetTimeout(ms int32)
func (r *ConnectionTestRequest) SetMaxBytes(n int32)
func (r *ConnectionTestRequest) SetHeaders(headers *HTTPHeaders)

type ConnectionTestHandler interface {
	OnStage(stage *ConnectionTestStage)
	OnResult(result *ConnectionTestResult)
	OnError(message string)
}

func (c *CommandClient) StartConnectionTest(request *ConnectionTestRequest, handler ConnectionTestHandler) (*ConnectionTestSession, error)
func (s *ConnectionTestSession) Cancel()
```

- The request is an object with setters: there are many parameters, and
  gomobile supports neither optional arguments nor nested structs.
  `HTTPHeaders` is the builder from `GetURLViaOutbound`.
- Stage, result and finding are objects with getters; attributes and lists
  go through iterators. No `CommandClient` method returns a bare string (038).
- The handler and session shape follows upstream `StartNetworkQualityTest`
  and `StartSTUNTest`.
- `OnError` fires only on a transport failure of the stream. An error of
  the test itself (`result.error`: invalid request, `busy`) arrives in
  `OnResult`, so a client must not wait for `OnError` on it.

### 3.4 CLI

`sing-box api connection-test <url>` with flags for the request fields.
It prints the stage tree as it arrives and then the result. Needed for lxd
machines, desktop and test stands.

## 4. Stages

Each stage is one step of the pipeline. A stage started inside another one
is its descendant at any depth: resolving the node address is a descendant
of dialing the node, the DNS server of that lookup is a descendant of the
lookup, the detour of the DNS server is a descendant of the DNS server, and
so on.

**The vocabulary is open.** The core may add stage kinds and attributes
without changing the protocol. A client renders an unknown kind as `kind` +
`subject` + attributes.

| `kind` | What | Main attributes |
|--------|------|-----------------|
| `app_dns` | DNS query of the app (with `appDNS`), one per type: A, AAAA, HTTPS | type, rule (`dns[i]` or `final`), server, rcode; one `answer` per answer record in answer-section order, presentation format without the class; the owner name leads only when it differs from the queried name (`A 300 192.0.2.1`, `CNAME 300 a.example.`, then `a.example. A 300 192.0.2.1`; `HTTPS 300 1 . alpn="h2,h3" ech="…"`, SvcParam values quoted as `dig` prints them); on NXDOMAIN / NODATA one `authority` per authority record in the same format (`example. SOA 30 …`, TTL as cached: min(SOA TTL, minimum)); `source` = `network` / `cache` with `cache_age` in seconds (and `stale` for an optimistic answer) / `local` (fakeip, hosts: the core answered without a query); absent when no DNS server was asked: a rule answered (`predefined`, `reject`), or the domain strategy dropped the type (`ipv4_only` answers AAAA, `ipv6_only` answers A with an empty NOERROR before any `dns` stage) |
| `route` | Route rule selection | destination, where the domain came from (`request` / `fakeip` / `reverse_mapping` / `sniff` / `none`), sniff result, non-final actions applied in order, rule (`route[i]` + description or `final`), action, outbound, rules that could not be evaluated and why |
| `group` | Selection in a group | group type and mode, selected node; for per-connection selection — a marker and the slot |
| `chain` | Unfolding of a `chain` | positions, resolved nodes |
| `dial` | Connection through a node | tag, type, server address |
| `detour` | Handing the connection to a detour | detour tag, what was passed: domain or address |
| `lookup` | Resolving the server address of a node or DNS server | domain, who resolves (`domain_resolver` / `default_domain_resolver` / `dns_rules` with the rule), server, answer |
| `dns` | Query to a DNS server | tag, type, group path, group probes (as in 035), rcode; `connection=new` / `reused` (a pooled server connection); a reused one adds a child `connection` stage, `REUSED`, subject = server address, `reuse=pooled` (idle in the pool) / `joined` (dialed by a parallel query, e.g. A and AAAA of one lookup), `age` in seconds; under `fresh` `connection=new`, `pool=detached`; a cache hit is `REUSED` with `source=cache`, `ttl` (remaining) and `cache_age` in seconds |
| `wake` | Waking an idle-suspended WG/AWG endpoint, recorded by the dial that wakes it | subject = endpoint tag; `slept` in seconds, `mode=resume` (device up) / `rebuild` (torn-down device rebuilt); `FAILED` when the endpoint stays down |
| `tcp_connect` | TCP connection | address, local address; `tfo=deferred` when TCP Fast Open connects on the first write; `inside` = the WG/AWG endpoint tag for a connection opened inside its tunnel |
| `udp` | UDP socket (a packet connection or a connected UDP socket) | address, local address; `inside` as for `tcp_connect` |
| `quic` | QUIC connection | address, obfuscation; MASQUE h3 and TrustTunnel h3: server, `sni` / `protocol`; a reused TrustTunnel session is `REUSED` with `session=existing` and `age` |
| `tls` | TLS handshake | SNI, fingerprint, ALPN, version, key share |
| `reality` | REALITY verdict, a child of `tls` | `accepted` / `dest_cert` (the handshake completed without verification: the server did not recognize the client and served the dest certificate) / `invalid_cert` (the certificate failed verification). No verdict when the handshake broke otherwise (reset, EOF) |
| `transport` | Transport over TLS (XHTTP, gRPC, WS, HTTPUpgrade, H2) | type, mode, method, path, HTTP status |
| `auth` | Authentication where the protocol confirms it (Hysteria2, TUIC, naive, TrustTunnel, MASQUE, SOCKS, HTTP CONNECT) | response status. MASQUE: `vhttp=h3` / `h2`, `status` (`2xx` on success: the transport does not return the exact code), `refused=access_denied` for a Cloudflare Access TLS alert; a dial over the live tunnel is `REUSED` with `tunnel=existing` and `age`. TrustTunnel: one stage per CONNECT, subject = authority (incl. `_udp2`, `_check`), `status`, `refused=authentication_required` when the server closes the session instead of answering 407; the session itself is `tls` (h2) or `quic` (h3) |
| `wg_handshake` | WG/AWG handshake, recorded before each TCP connect inside the tunnel | subject = endpoint tag; `detour` = the endpoint's detour tag, when it has one; `handshake=new` with `peer`, the stage ending at the handshake time; `handshake=existing`, `REUSED` with `age`; `FAILED` "no handshake with the peer" when there is no session and the dial failed; `NOT_EVALUATED` with `handshake=unknown` when the peer status cannot be read |
| `proxy_header` | Protocol header with no confirmation (VLESS, VMess, Trojan, Shadowsocks) | target; result is always `NO_ACK` |
| `target_tls` | The test's TLS with the target through the tunnel | SNI, ALPN |
| `first_byte` | First bytes of the target's response | — |
| `http` | HTTP response | status, Content-Type, bytes; `location` on a redirect |

The stages of a test are the ones the pipeline actually ran. If a node sent
the connection somewhere other than the user expected, the trace shows the
actual path, not the expected one.

## 5. Rules and guarantees

1. **Real path.** Through the router, the test enters as an ordinary
   inbound connection with the source metadata: rules, sniffing, group
   selection and detours are the production ones. Through a node
   (`outboundTag`) — the same path as `GetURLViaOutbound`, plus the trace.
2. **Root cause.** Among `FAILED` stages, excluding lost-race siblings
   (§5.13) and failures inside a stage that ended OK after them (the step
   recovered: a transport retried on its own, another server answered),
   take the one that finished first and has no failed
   descendants: the deepest failure that stopped the pipeline. Stages
   that did not run because of it are `SKIPPED`. PLAN §4 implements this
   rule and no other.
3. **Skips are not hidden.** A stage not run because of a cache, a live
   connection or a tunnel already up is `REUSED` with an age, not `OK`
   with zero milliseconds.
4. **No confirmation is not success.** A step whose success the protocol
   does not confirm is `NO_ACK`. Only a later stage (`first_byte`)
   confirms it.
5. **A resolve cycle** is caught at once, not by timeout: re-entering the
   same "resolver, domain" pair among the ancestors gives `FAILED` with an
   attribute pointing to the ancestor stage.
6. **`fresh = true`:**
   - the DNS cache is bypassed at every level, and the test's answers are
     not written to the cache;
   - DNS servers open new connections;
   - node transports open new connections where the protocol allows it.

   The test does not tear down or restart WG/AWG endpoints, MASQUE tunnels,
   TrustTunnel sessions and the connections of external modules: their
   stages are `REUSED` with the age of the connection or of the last
   handshake. Where reuse cannot be controlled at all (Hysteria, Hysteria2,
   TUIC, naive, AnyTLS, `multiplex.enabled`, the gRPC transport, MASQUE,
   TrustTunnel), the
   stage carries `reuse=uncontrolled` under `fresh`.
7. **The test does not change group state:** selector choice, urltest
   history, failover penalties and degradation, `round_robin` stickiness.
8. **Through the router the test connection is visible** in the connection
   list and is told apart from production ones by its inbound type.
   Through a node (`outboundTag`) it is not tracked, as with
   `GetURLViaOutbound`.
9. **Depth is respected:** `DEPTH_DNS` and `DEPTH_ROUTE` open no outward
   connections except the DNS queries the depth itself requires.
10. **Only on an explicit action.** The test is real traffic: on metered
    nodes it costs data, and idle WG/AWG endpoints wake up. At most 4 tests
    run at once.
11. **One overall timeout.** When it expires, unfinished stages become
    `FAILED` with cause `timeout`; the root is the deepest of them.
12. **Without a test the production path pays nothing** except checking
    for a trace in the context.
13. **Parallel dials.** When the dialer races several addresses or
    interfaces, the siblings that lost the race are `SKIPPED` with the
    attribute `lost_race=<winner stage id>`, not `FAILED`, and are never
    the root cause.
14. **The test owns its connections.** Every connection the test opened,
    including those forced by `fresh`, is closed when the test ends and
    never enters a production pool (XHTTP transport pool, DNS connection
    pool).
15. **Stage cap.** A test records at most 512 stages. Beyond the cap new
    stages are counted but not recorded, and the result carries
    `stagesTruncated`.

## 6. Findings

The core computes findings from the trace and the core state and returns
them as codes with stage references and attributes. The client writes the
text. The vocabulary is open, like the stage vocabulary.

Levels: `info` — a fact about the path; `warning` — the path works, but
may break for another source, after a restart or later; `problem` — a
cause of the failure.

| Code | Level | When |
|------|-------|------|
| `excluded_from_vpn` | warning | The source does not enter the tunnel (`include_package` / `exclude_package` of TUN). The path in the trace is hypothetical |
| `resolve_via_rules_in_source_context` | info | The node's server address is resolved by DNS rules in the source context (the node has a detour and no `domain_resolver`). For another source the resolve path may differ |
| `dependency_outside_route` | info; `problem` when the root cause is inside that node's subtree | The path depends on a node that is not on the route (e.g. a DNS server's detour). Attributes: the node and what depends on it |
| `resolve_cycle` | problem | A resolve cycle was found (§5.5) |
| `ip_rules_not_evaluated` | warning | Address rules were not evaluated: the address is unknown (fakeip, a domain without `resolve`) |
| `fakeip_not_persisted` | warning | fakeip is not persisted across restarts: after a restart, apps with cached addresses get a refusal |
| `fakeip_pool_wrapped` | warning | The fakeip pool wrapped faster than the answer TTL: an old address may have gone to a new domain. Attributes: range size, wrap time, TTL |
| `fakeip_unknown_address` | problem | `connectIP` is in the fakeip range and has no record |
| `fakeip_address_reassigned` | problem | `connectIP` currently belongs to another domain |
| `fakeip_ipv6_without_tun_ipv6` | warning | AAAA returns IPv6 fakeip, but TUN has no IPv6 address |
| `https_query_rejected` | info | The HTTPS DNS query is rejected by a rule: the client gets no ECH and no ALPN hints |
| `quic_rejected` | info | `udp/443` is rejected by a rule: the browser falls back to TCP |
| `reality_not_recognized` | problem | Verdict `dest_cert`. Possible causes (indistinguishable): `public_key`, `short_id`, SNI outside `server_names`, client version |
| `failed_after_no_ack` | problem | The connection broke after a `NO_ACK` stage. Possible causes: wrong UUID/password, the server could not reach the target, large packets lost (MTU) |
| `partially_reused` | info | The path was completed, but some stages are `REUSED`: the result does not prove the path comes up from scratch |

## 7. Boundaries

- **Only GET over http/https.** No arbitrary methods or bodies (as in
  `GetURLViaOutbound`). `network = "udp"` goes only as far as node
  selection: the test makes no HTTP/3 request.
- **App behavior is not reproduced.** The app's own DNS, ECH, its own
  ClientHello and connections opened before the VPN are not repeated: the
  test reproduces the rules for the source, not its network behavior.
- **Not a retrospective.** Past failures, including connections rejected
  before tracking, are a separate task.
- **The config is not changed,** and the test does not propose changes.
  Findings describe the state; the decision is the user's.
- **Beyond the node's server** the test sees only the end-to-end HTTP
  response.
- **Channel quality is not measured:** speed and NAT type are upstream
  `StartNetworkQualityTest` and `StartSTUNTest`.
- **The indistinguishable is not distinguished.** Causes within one class
  (`reality_not_recognized`, silence after `NO_ACK`) are listed, not chosen.
- **Stage granularity depends on the protocol.** Where a protocol
  implementation in an external module does not report intermediate steps
  (for example, QUIC and Hysteria2 authentication happen in one call),
  stages are merged and the outcome is classified by the error.
- **UID from a package** needs the platform package manager, which exists
  only with a `tun` inbound on a platform build (Android). Without it
  `uid` stays 0 and `user_id` rules do not match.
- **`source_port` rules are not covered:** the test's source port is
  ephemeral.
- **Inside a WG/AWG endpoint.** Its outer UDP is dialed by `wireguard-go`
  on the endpoint's own context and its peer domains resolve at start, so
  the endpoint's own detour and peer lookup never show in a trace: `wake`
  is a leaf, `wg_handshake` its sibling. `wg_handshake` names the detour
  (`detour`), so a handshake that times out points at the detour to test
  next. `wg_handshake` is recorded only
  for TCP: a UDP socket sends nothing to trigger a handshake. Inner TCP
  connects inside MASQUE's userspace stack are not traced.
- **Hysteria2 and TUIC** (sing-quic) open the QUIC connection on the
  client's own long-lived context, so the hop's socket, its detour and its
  `domain_resolver` lookup never show: the hop is one opaque `dial` stage,
  classified by the error on failure (`class=auth` / `quic`; Hysteria2
  answers a wrong password with 404 to look like a web server). Not planned:
  the client keeps that connection alive, so a test usually runs through an
  existing one with nothing to trace; the error classification answers
  the two common questions (wrong password, QUIC blocked); and seeing
  inside would mean forking sing-quic as a fifth submodule for the trace
  alone. To be revisited if field cases show the classification sending
  users the wrong way.
- **Approximate findings.** `quic_rejected` for a TCP test matches the
  rules against `udp/443` without sniffing or resolving; `fakeip_pool_wrapped`
  compares with the default answer TTL (600 s), not `rewrite_ttl`.
- **Early-handshake protocols at `DEPTH_CONNECT`.** VLESS, Trojan and
  Shadowsocks send their header together with the first payload. With no
  request there is no payload, so the trace has no `proxy_header` stage at
  this depth; the tunnel is established up to the node's transport.

## 8. Acceptance criteria

1. Through the router: the rule, sniffing, group selection and chain of the
   test match a production connection with the same source metadata.
   Measured on the stand by comparing the `route` and `group` stages with
   the connection-list entry (`rule`, `outbound`, `chain`, sniffed
   `protocol` and `domain`) of a real connection from the same source to
   the same URL.
2. A node with two detours of different protocols, whose address is
   resolved through a DNS server that goes through a node outside the
   route: a tree with correct nesting; with the DNS detour node down, the
   root is its `tcp_connect`, the route nodes' stages are `SKIPPED`, and
   `dependency_outside_route` is reported.
3. A resolve cycle yields `resolve_cycle` before the timeout.
4. A warm path with `fresh = false` gives `REUSED` stages with ages; the same
   path with `fresh = true` runs them, and the DNS cache is unchanged after
   the test.
5. REALITY: a wrong `public_key` gives `dest_cert` and
   `reality_not_recognized` when the dest certificate is publicly trusted
   (with an untrusted one — e.g. a self-signed stand — the verdict is
   `invalid_cert`); a correct one gives `accepted`.
6. A package excluded in TUN gives `excluded_from_vpn`.
7. `connectIP` from the fakeip range without a record gives `route`
   `FAILED` and `fakeip_unknown_address`; with another domain's record —
   `fakeip_address_reassigned`.
8. `DEPTH_ROUTE` opens no outward connections.
9. After a test through a group, the selector choice, urltest history,
   penalties and stickiness are unchanged.
10. Cancellation closes all connections of the test; no goroutine leaks
    (`-race`).
11. Without `with_lx_command` — `Unimplemented`; the 038 guard over the
    `CommandClient` surface passes.

## 9. Delivery in two releases

The task ships in two releases, so that the first result reaches
the user without waiting for the whole stage vocabulary.

| Release | Content | Acceptance criteria |
|-------|---------|---------------------|
| 1 | Protocol, trace holder, both paths (router and node), all depths, `appDNS`, `connectIP` without fakeip findings; stages `app_dns`, `route`, `group`, `chain`, `dial`, `detour`, `lookup`, `dns`, `tcp_connect`, `udp`, `tls`, `transport` (XHTTP), `proxy_header`, `target_tls`, `first_byte`, `http`; root cause, resolve-cycle detection, libbox, CLI. No findings | 1, 3, 8, 9, 10, 11; criterion 2 without the finding |
| 2 | `fresh` at nested levels with `REUSED` ages, `reality` verdict, `wake`, `wg_handshake`, `auth` and `quic` stages, fakeip state, all findings of §6 | 2 (the finding), 4, 5, 6, 7 |

Release 1 closes #59 and gives the LxBox screen its data. Release 2 is scoped
from the field experience of release 1 and may be re-scoped. The protocol
of §3.1 is complete in release 1, including the stage kinds and finding
codes that only release 2 produces: release 2 changes no `.proto`, so
LxBox can build against the release 1 protocol.

## 10. Merge cost (CONSTITUTION §3.1 a3)

The RPC itself is a §3.6 bridge: a seam in `daemon/started_service.proto`,
regeneration, handler and stub in `daemon/*_lx.go`, client in
`experimental/libbox/*_lx.go`. The trace is lx logic in the new package
`common/lxtrace` plus one-line `// lx:` insertions in upstream files, each
a nil-safe call that costs one `ctx.Value` check without a trace:

| File | Release | Insertions |
|------|-------|------------|
| `route/route.go` | 1 | 2 blocks of 4 lines (`lxtrace.Routed` before the trackers in the TCP and UDP paths: the `route` stage and the `DEPTH_ROUTE` stop) + import |
| `route/conn.go` | 1 | 5 lines (`dial` of the first hop around `DialContext` / `DialSerialNetwork`: begin + end in TCP, begin + 2 ends in UDP) |
| `common/dialer/default.go`, `detour.go`, `resolve.go` | 1 | 8 / 4 / 4 lines (`tcp_connect`/`udp` incl. interface-strategy paths; `detour` in `DialContext` and `ListenPacket`; `lookup` in four resolve paths) |
| `common/tls/client.go` | 1 | 2 lines (`tls` begin/end in `ClientHandshake`) |
| `dns/router.go` | 1 | 3 lines (matched DNS rule: rule walk, legacy loop, `final`) |
| `protocol/wireguard/endpoint.go` | 1, 2 | 6 lines (`lookup` by rules in `DialContext` and `ListenPacketWithDestination`; `tcp_connect`/`udp` inside the tunnel via `netstackLx` on four dial calls); 1 line (`wake` in `resumeOnDial`, inside the lx idle-suspend block) |
| `protocol/group/urltest.go` | 1 | 6 changed lines on already lx-marked calls (probe-aware pick, history and penalty reset; `group` stage) |
| `dns/client.go` | 5 | 1 line (`dns` stage begin in `beginExchange`), 2 lines (cache age at the cache hits of `beginExchange` and `questionCache`), 2 lines (`fresh`: `DisableCache` in `beginExchange` and `Lookup`) |
| `common/tls/reality_client.go` | 2 | 1 line after `HandshakeContext` (the file already carries lx edits) |
| `dns/transport/conn_pool.go` | 1, 2 | 1 field + 15 lines: probe-owned connections are not parked on release (§5.14, criterion 10); `fresh` takes a connection of its own (2 blocks of 3 lines in `acquireShared` / `acquireOrdered`); reuse marks at the pooled and joined paths (3 lines) |
| `protocol/group/urltest.go` (hotfix) | 1 | `selectedOutbound*` behind `selectedAccess`: a data race exposed by SPEC 136, registered in [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) |
| `dns/transport/fakeip/store.go` | 2 | 1 line in `Create` (pool-wrap counter; per new address one weak pointer and one map read, no trace needed) |
| `dns/transport/https.go`, `dns/transport/quic/http3.go` | 2 | 2 lines each where the exchange takes its client: under `fresh` a one-off DoH / DoH3 client, closed after the exchange (their connections live in the HTTP client, not in the pool); 1 changed line in `https.go`: a fresh query's timeout does not reset the production client |

A consumer-side alternative does not exist: the route decision, sniffing,
group selection and nested resolves are visible only inside the core, and
upstream offers neither a connection trace nor a rule-match call (#59).
The most conflict-prone spots are `exchangeWithRules` in `dns/router.go`
and `NewConnection` in `route/conn.go`; their insertions go in a separate
commit, so a merge conflict can drop and restore them without the rest.

## Appendix A. Example trace

Config: `proxy` (selector) → `vless-nl` (VLESS + XHTTP + REALITY,
`nl.example.net`, detour `awg-de`, no `domain_resolver`) → `awg-de`
(AWG, `de.example.net`, detour `hy2-fi`, `domain_resolver: dns-remote`)
→ `hy2-fi` (Hysteria2, salamander, `domain_resolver: dns-boot`).
`dns-remote` is DoH through `ss-pl` (Shadowsocks 2022, not on the route).
DNS rule `dns[2] domain_suffix example.net → dns-remote`.

Request: `https://www.youtube.com/generate_204`, through the router, source
`com.android.chrome` on `tun-in`, `appDNS`, `fresh`. A client could render
the trace like this (left column — `startMs`, right — `durationMs`):

```
   0  ✓ app_dns   A     www.youtube.com → 198.18.0.42      dns[3] → dns-fake
   0  ✗ app_dns   HTTPS www.youtube.com → reject           dns[0]
   1  ✓ route     198.18.0.42:443 tcp · fakeip ↦ www.youtube.com
                  sniff tls www.youtube.com · route[5] domain_suffix=youtube.com → proxy
   1  ✓ group     proxy (selector) → vless-nl
   2  ✓ dial      vless-nl
   2    ✓ detour  awg-de ← nl.example.net:443 (domain)
   2      ✓ lookup  nl.example.net → 198.51.100.20   dns_rules: dns[2] → dns-remote
   2        ✓ dns   dns-remote (DoH), new connection
   2          ✓ lookup       dns.example.org → 198.51.100.9   domain_resolver dns-boot     22
  24          ✓ dial         ss-pl
  24            ✓ tcp_connect  203.0.113.50:8388                                         37
  61            · proxy_header (NO_ACK)
  61          ✓ tls          dns.example.org · h2                                        79
 140          ✓ transport    POST /dns-query → 200                                       38
 178      ✓ wake          awg-de · slept 720 s · resume                                2
 180      ✓ wg_handshake  awg-de · new                                                81
 261      ✓ tcp_connect  198.51.100.20:443 inside awg-de                                66
 327    ✓ tls          uTLS chrome · SNI www.microsoft.com · X25519MLKEM768             74
 401      ✓ reality    accepted
 401    ✓ transport    XHTTP stream-one · POST /xh/ → 200                               67
 468    · proxy_header VLESS → www.youtube.com:443 (NO_ACK)
 468  ✓ target_tls    www.youtube.com                                                  147
 615  ✓ first_byte                                                                      67
 682  ✓ http          204

findings: resolve_via_rules_in_source_context (stage lookup nl.example.net),
          https_query_rejected, dependency_outside_route (ss-pl → address of vless-nl)
```

The detour `hy2-fi` of `awg-de` and the lookup of `de.example.net` are not
in the trace: the endpoint dials and resolves its peer on its own context
(§7).
