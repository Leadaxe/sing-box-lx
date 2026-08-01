---
icon: material/alert-decagram
---

# lx changelog

Changes in the `sing-box-lx` fork (the `lx` features layered on top of upstream
sing-box). Upstream's own changelog is in [changelog.md](../docs/changelog.md); this file
tracks only the fork. Versions are tagged `vX.Y.Z-lx.N`; releases are built by
`lx-release.yml`. Tags carrying an `-rc.N` / `-alpha.N` / `-beta.N` suffix publish
as GitHub **pre-releases** and never become "Latest".

#### v1.14.0-lx.18

Adds the VLESS `encryption` layer on top of `lx.17`, which stays as described
below.

**VLESS nodes using `encryption: mlkem768x25519plus…` now connect (SPEC 032,
feature
[VLESS_ENCRYPTION](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md)).**
This is a post-quantum handshake that lives inside VLESS, beneath the transport
and independent of TLS — not to be confused with REALITY's key exchange. The
field did not exist in our schema at all, so such configs were rejected by the
decoder and the nodes were simply unreachable. Where the layer is absent or set
to `none`, behaviour is unchanged.

The failure mode gave nothing away: the transport came up normally — a
WebSocket upgrade completed with `101`, a gRPC server answered its SETTINGS
frame — and then the peer tore the connection down with not one line in the
core log. What settled it was reading the *other* client's stored
configuration, where the same servers appear twice: once plain, once carrying
`mlkem768x25519plus.native.0rtt` with an ML-KEM-768 key. The dead ones here
were exactly the latter.

Rather than implementing the handshake from scratch, the client half is ported
from the sing-box fork at `starifly/sing-box`, which carries the same GPL-3.0
license and the same upstream base as this tree. Provenance is recorded in the
file headers. The server half (`decryption`) is deliberately not included —
this fork is client-focused. No new external dependencies.

Verified on device against the subscription that prompted it: nodes that had
all been dead came back at **6/8 over WebSocket and 4/4 over gRPC**, with no
other transport group moving — so the gain is attributable to the layer itself
rather than to anything incidental. The nodes still not answering in those
groups are placeholders in the subscription rather than servers: three entries
address `0.0.0.0` and serve as section headings in the node list, and two carry
a truncated 43-character key where a working node carries 1579. Against the
subscription's real nodes the layer works everywhere it applies.

Note for client authors: supporting this end to end takes both halves. The
field arrives inside a subscription as `settings.vnext[0].users[0].encryption`
but belongs on the sing-box outbound as a flat `encryption` field beside
`uuid`; a config builder that drops it leaves the core with nothing to act on.

#### v1.14.0-lx.17

First stable tag of the `lx.17` line — a promotion of `rc.1`–`rc.5` plus the
XHTTP fixes below, which never shipped in an rc. The rc sections stay as
written; this entry describes what is new on top of `rc.5`.

The headline is XHTTP `mode: auto` on REALITY servers — the shape most
subscriptions ship, and until now the one that did not work.

**XHTTP `stream-one` no longer sends a path the server refuses to route (SPEC
043, feature
[XHTTP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/002-XHTTP/FEATURE.md)).**
An XHTTP server normalizes its own path to end in `/` whenever the session id or
sequence number is carried in the path — which is the default — and then serves
only requests whose path starts with that prefix. Our `stream-one` trimmed the
trailing slash, so a node configured as `/api/v1/feed` was dialed at exactly
that, while the server was matching against `/api/v1/feed/`. No prefix match, a
`404`, and the dial hung until the URL-test timeout with nothing logged: no
error, no status, just silence. Every XHTTP node in a subscription looked dead.

`packet-up` was unaffected throughout, because its path continues into
`/<sessionId>` and therefore does carry the slash — which is what made the bug
look like broken mode resolution. It never was: `auto` resolves to `stream-one`
under REALITY exactly as Xray and other sing-box forks do, and that resolution
was correct all along. It simply led into the one broken branch.

The trim dates back to SPEC 011, where dropping the session id out of the
`stream-one` path also dropped the slash. Only the session id belongs gone — an
empty session id is precisely how the server selects the bidirectional branch —
whereas the slash is part of the normalized path. Both properties now hold at
once. Paths whose session id lives in a header, query or cookie are untouched
and still reach the wire exactly as configured, trailing slash included.

Confirmed on the wire against a prefix-checking HTTP/2 server: `REJECT 404:
"/api/v1/feed" lacks prefix "/api/v1/feed/"` before, `ACCEPT: "/api/v1/feed/"`
after — then verified on device against the live subscription that reported the
problem. Two unit tests had been asserting the broken shape and were corrected.

**Streamed-body XHTTP requests carry `Content-Type: application/grpc` (SPEC
042).** Xray sets this header on every request that carries a body — `stream-one`
and `stream-up` — and we never did. The `no_grpc_header` option, previously
accepted as a documented no-op, now genuinely suppresses it, matching Xray's
`NoGRPCHeader`. This restores parity with the Xray wire contract; on its own it
did not resolve the dead-nodes report above, and is shipped as a correctness fix
rather than as that cure.

**Also in this line, from `rc.1`–`rc.5`** (each described in its own section
below): `GetRunningConfig` no longer crashes the core on android/arm64 and
returns the config the running box was actually built from (SPEC 037/038);
report archives are pruned instead of growing without bound — 427 MB had
accumulated on one device (SPEC 039); `Endpoint.Close()` reports tun-device
close failures again; WG/AWG endpoints heal themselves after device sleep
instead of sitting in ERR until a manual reconnect (SPEC 041); system-stack TCP
survives having its listener closed underneath the core (SPEC 040); and roughly
245 upstream commits were merged across the line.

#### v1.14.0-lx.17-rc.5

Upstream sync on top of `rc.4`, which stays as described below. No `lx` changes
of our own — the fork's delta is untouched.

Five upstream fixes were picked up (`SagerNet/sing-box` `testing`, up to
`d2438c2`): a DNS race where a completed rule was blocked by an earlier armed
one, a routing loop to the exact TUN address on darwin, TLS fragment ACK waiting
on Windows without TCP estats, system-device DNS configuration for WireGuard
interfaces, and a naiveproxy bump to `v150.0.7871.63-1`.

The apparent gap was much larger — 215 commits — but `upstream/testing` is
force-pushed, so already-merged work reappears under fresh hashes. Comparing by
commit subject rather than hash showed only six genuinely new commits, five of
which cherry-picked cleanly. The sixth, an `Update sing-tun` bump, was
deliberately **skipped**: it points at a revision older than the one our
`sing-tun` fork is based on, so taking it would have rolled the TCP self-heal
work backwards.

#### v1.14.0-lx.17-rc.4

Adds two fixes on top of `rc.3`, which stays as described below.

**WG/AWG endpoints heal themselves after device sleep instead of staying in ERR
until a manual reconnect (SPEC 041, feature
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)).**
While the phone sleeps, the per-flow state of the tunnel's UDP 5-tuple dies on
the path (the NAT mapping expires and/or a DPI flow entry goes stale). Upstream
wireguard-go then retries handshakes into that same dead socket forever — same
source port, same dead 5-tuple — which is exactly what the field dumps showed:
receive workers alive for half an hour, socket never reopened, zero replies.
Reconnecting "fixed" it purely by opening a new socket with a fresh ephemeral
port. The device now does that by itself: when a peer's handshake retry cycle
exhausts (~90 s of unanswered initiations — the existing give-up event, which
only fires under traffic demand), the bind is reopened once with a fresh
ephemeral port and a new handshake is kicked immediately. For masquerade
profiles the `i1` decoy rides out with the first initiation of the new 5-tuple,
re-opening the flow on the DPI. Debounced to one rebind per give-up cycle; an
explicitly pinned `listen_port` is preserved (self-heal via port change is then
unavailable, by design); both bind paths (plain and `detour`) heal through the
same mechanism. Zero cost while healthy, asleep or closed: no timers, no
goroutines, no traffic — on a down device the rebind degrades to a no-op, so it
never fights idle-suspend (SPEC 020).

**System-stack TCP no longer dies forever when its listener is killed out from
under the core (SPEC 040, feature
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)).**
With `stack: "system"` every new TCP connection from the TUN is NAT-rewritten
onto a local forwarder listener. Its accept loop treated *any* `Accept` error as
terminal and silently returned — so when something else in the shared Android
process closed the listener's fd (a stray close on a reused descriptor number —
the LxBox §047 "browser dead, QUIC alive" failure), the stack kept running and
kept rewriting every new SYN onto a dead port. The OS answered each one with an
instant RST: every app got `ECONNREFUSED` in ~16 ms until the VPN was restarted,
while UDP, QUIC and DNS worked fine. Reproduced on device: ~1 in 8–36 fast VPN
restarts, worse on a "dirty" process — which is why it had floated uncaught for
months.

sing-tun is now a fork submodule (`submodules/sing-tun`, pinned at the exact
upstream revision from go.mod) with a single-file patch: an unexpected `Accept`
error is logged with the errno (which names the killer path), the listener is
recreated on the same address, the forwarder port is republished atomically, and
the loop keeps serving. A deliberate `System.Close()` stays silent as before.
If the rebind itself fails, the loop logs an error and gives up — no worse than
upstream. A recovery counter is kept as telemetry: if it ever ticks, the
fd-closing trigger on the client side is still alive.

#### v1.14.0-lx.17-rc.3

One-line fix on top of `rc.2`, which stays as described below.

**`Endpoint.Close()` reports tun-device close failures again.** Our teardown
support (SPEC 020) wrapped the final `tunDevice.Close()` in a nil check — the
device may already be gone when a torn-down endpoint is closed — but discarded
its error and returned `nil`, where upstream returns it. A device that failed to
close therefore looked like a clean shutdown in the logs. The nil guard stays;
only the error is propagated now, matching upstream. The endpoint manager already
wraps and logs this error, so the sole visible change is that a real failure is
no longer silent.

#### v1.14.0-lx.17-rc.2

Adds one fix on top of `rc.1`, which stays as described below.

**Report archives grew without bound — 427 MB of them on one device (SPEC 039,
feature
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)).**
Every OOM and crash report goes into a fresh directory under `files/oom_reports`
/ `files/crash_reports`, and nothing ever deleted the old ones — upstream leaves
that to the client, which only cleans up what it has exported. A single report
is heavy (two pprof profiles plus a config copy, ~750 KB), so a recurring fault
quietly eats the disk. Found on device: **575 directories / 427 MB accumulated
over 19 days**, peaking at 94 reports in one day, none of them ever removed.

The archive is now trimmed before each new report is written, on both the OOM
and the crash path:

- **32 directories** and **64 MB**, whichever bites first. The count limit is
  what usually holds (32 × ~750 KB ≈ 24 MB); the byte budget covers a handful of
  unusually fat reports.
- Trimming targets `cap-1`, so the archive holds exactly the cap once the
  incoming report lands, rather than overshooting by one every time.
- Oldest go first, ordered by **modification time, not by name** — collision
  suffixes (`-1`…`-1000`) break lexicographic order, since `…-05-2` sorts after
  `…-05-10`, and a name sort would delete the wrong reports.
- Best-effort by design: this runs while the process is already dying, so any
  failure is skipped rather than propagated. Losing one report beats losing the
  report that mattered. Loose files sharing the directory are neither deleted nor
  counted against the budget.

Report format, naming and export are unchanged — only deletion of old reports is
new. Note this does **not** reclaim what has already piled up: rotation runs when
the next report is written, so an archive that grew before this build shrinks on
the next OOM or crash, not at upgrade time.

**240 upstream commits merged** — drift accumulated since the `beta.2` base and
is now closed (upstream has not tagged a release past `beta.2` yet, so the
reported base stays `v1.14.0-beta.2`). Notable for this fork: URLTest now *requires* a history
storage in the context instead of silently creating one, DNS gained
namespace/parallel `evaluate` support and client-subnet-aware caching,
`rule_set` matching semantics were simplified, JSON schema generation landed, and
a TUN dispatcher deadlock plus a local-DNS block on cancelled queries were fixed.
New upstream protocols (snell, usbip, openvpn, openconnect) are present in the
tree but stay out of the AAR tag set, as before.

Most of the 56 merge conflicts were textual, not semantic: our branch already
carried the upstream code from earlier merges, and upstream force-pushes
`testing`, so the same commits reappeared under new hashes against a stale
merge base. Two were real and are fixed here — a dropped `time` import that
broke the whole tree, and a duplicated DNS log function that upstream added
independently of ours. Fork-specific behaviour was re-verified against the merge
rather than assumed: idle-suspend still iterates endpoints, the detour tail still
reaches the client, WireGuard addresses still come from the cache rather than a
possibly-torn-down device, and the AAR tag set is unchanged.

One config-schema addition rides along: the `servers` field of a `group` DNS
server is now declared as a DNS-server reference, so upstream's new JSON schema
cross-links it instead of emitting a plain string list.

#### v1.14.0-lx.17-rc.1

Single fix, and it is a hard one: on Android, calling `GetRunningConfig`
killed the core outright. Cut as a release candidate because the fix changes
the libbox API and wants a device check from the client before promotion.

**`GetRunningConfig` crashed the core on android/arm64 (SPEC 038, feature
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).**
Every call ended in `fatal error: bulkBarrierPreWrite: unaligned arguments`
— a runtime `throw`, not a recoverable panic, so the process died and the
tunnel dropped. The RPC shipped in `rc.3` and in stable `v1.14.0-lx.16`,
which means the feature was unusable on Android in both.

The cause is the **return type**, not the RPC logic. `GetRunningConfig` was
the only exported `CommandClient` method returning a bare `(string, error)`:

- gomobile encodes a Go string as the C struct `nstring{void *chars; jsize
  len}` — a value **carrying a pointer**.
- cgo builds the callback's combined argument/result frame and marks it
  `__attribute__((__packed__))`, which drops the struct's alignment
  requirement to 1, so the C local lands 4-byte aligned on arm64.
- The generated Go wrapper assigns that pointer-bearing result slot, which
  compiles to a GC write barrier — and the barrier requires 8-byte
  alignment. 4 ≠ 8, so the runtime throws.

Every other method returns a refnum, an iterator or a scalar, none of which
puts a pointer in the frame — hence no barrier and no crash. Strings in
*struct fields* (`Rule.Type`, `PoolSlot.Tag`) are equally safe: those cross
as objects with getters.

**API change (breaking for clients):**

```go
// before — killed the process on Android
func (c *CommandClient) GetRunningConfig() (string, error)

// now
func (c *CommandClient) GetRunningConfig() (*RunningConfig, error)
func (c *RunningConfig) Content() string
```

Callers read the document via `.Content()`. The wire protocol, the proto
definition and the whole `daemon/` side are unchanged — the defect lived
purely in the libbox binding.

Note for anyone hitting this elsewhere: returning `[]byte` does **not** fix
it. That binds to `nbyteslice{void *ptr; jsize len}` — the same
pointer-in-a-packed-frame shape, the same crash. Only returning an object
removes the pointer from the frame. A reflection test now guards the entire
`CommandClient` surface against both return shapes, since any future method
with one would reintroduce the same kill.

Upstream `testing` had no new commits at cut time.

#### v1.14.0-lx.16

First stable tag of the `lx.16` line — a promotion of `rc.1`–`rc.3` with no
code changes on top of `rc.3`. Upstream is now on the **v1.14.0 beta** line
(`v1.14.0-beta.2` merged), so the fork resumes cutting non-prerelease tags.
Two features land here.

**DNS server type `group`: DNS resolution no longer dies with one failed
server (SPEC 033/034/035, feature
[DNS_GROUP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)).**
Previously `dns.final` was a *default*, not a fallback, and a rule routing to
a server returned its transport directly — any network error, timeout or
SERVFAIL failed the query outright even with healthy servers in the config.
A `group` puts several servers behind one tag with a selection strategy:

```json
{ "type": "group", "tag": "public",
  "servers": ["google", "cloudflare", "quad9"],
  "mode": "stable", "error_ttl": "2m", "win_ttl": "5m" }
```

Servers carry no states; two expiring record tables drive everything: an
**error** record (`error_ttl`, default 2m — written by any failed exchange,
erases the server's live wins) and a **win** record (`win_ttl`, default 5m —
only the first success of a fan-out; any success erases the server's live
errors). *Clean* = zero live errors; a network change amnesties both tables.

- `mode: stable` (default) — stickiness before randomness: stay on the
  current server while it is clean, re-elect a random clean one only when it
  is not. Server order is NOT meaningful; there is no return-to-primary.
- `mode: fastest` — the clean server with the most live wins; when nobody
  has one, the query becomes an election fan-out to all clean members
  (single-flight — a burst never multiplies fans). Re-election rhythm is
  `win_ttl` expiry. No timers, no synthetic probes.
- `mode: parallel` — every query fans to all clean members (N× traffic by
  design; no wins recorded).
- Unified flow: the single target gets HALF the remaining request budget —
  the rescue fan is guaranteed the rest, so a blackholed server can no
  longer eat the whole deadline. With **no clean member** every mode makes
  exactly one attempt via the least dirty server and never fans (anti-storm
  on a dead network).
- A group is a first-class server: accepted in `final`, in rules and inside
  other groups (cycles are rejected at load, not at runtime). `fakeip` and
  `hosts` members are rejected — local sources cannot fail over.
- Observability: the DNS query stream attributes each answer to the member
  that actually produced it, events carry `fanned` and `survival` flags on
  top of the probe trace, and `GetDNSGroups` returns the live records per
  member (clean, live errors + age, live wins, current, last rtt).
- The implementation survived a 24-agent adversarial review; all six
  confirmed defects (nil fan result, leaky Reset amnesty, election-window
  target trashing, and more) are fixed with regression tests.

Note for anyone who ran the **rc.1** pre-release: the rc.1 config contract
(`mode: failover|race`, `interval`, `down_time`) was replaced during the rc
line and such configs **fail to load** with an explicit error. The group was
redesigned around the TTL record model before any consumer shipped, so no
compatibility bridge is kept. Stable users are unaffected — `group` is new
in this tag.

**New RPC `GetRunningConfig`: the core now answers "what is actually
running" (SPEC 037, feature
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).**
Until now the core kept no config after start — `GetOutbounds` returns only
tag/type/delay, so after a profile edit without restart the client had no
source of truth for node details. The new unary RPC returns the canonical
JSON of the options the running box was actually built from: a **post-override
snapshot** (including what the service layer injected at start), captured
**once at service start**, so serving the RPC is a plain string handoff with
zero per-request work. Per-node JSON is derived client-side by extracting the
tag from this document. The document is a **re-marshal**, not the original
bytes (field order, omitempty, `[] → null`) — compare it with the stored
profile semantically, not as a textual diff. Behind `with_lx_command`; a
tag-less build answers `Unimplemented`, not-started → `FailedPrecondition`,
started without a snapshot → `Unavailable`.

Also in this line: the `lx_idle_teardown` explicit-`"0"` kill switch is now
distinguishable from an absent key (SPEC 020), docs were reconciled with the
code (AWG2 masquerade `id`/`ib`/`sip`, XHTTP GET fallback, DNS stream rcode
semantics), and upstream **v1.14.0-beta.2** is merged — upstream left alpha
(platform options restored, TLS/acme fixes, JSON schema, client-subnet DNS
cache, rule-level race/speculative evaluate actions).

#### v1.14.0-lx.16-rc.3

**New RPC `GetRunningConfig`: the core now answers "what is actually
running" (SPEC 037, feature
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).**
Until now the core kept no config after start — `GetOutbounds` returns only
tag/type/delay, so after a profile edit without restart the client had no
source of truth for node details. The new unary RPC returns the canonical
JSON of the options the running box was actually built from:

- **Post-override snapshot** — includes what the service layer injected at
  start (tun `auto_redirect`/package lists, the OOM-killer service), i.e.
  exactly what went into the box, not the profile text the client sent.
- Captured **once at service start** (same encoder as config formatting);
  serving the RPC is a plain string handoff — zero per-request work, zero
  cost when never called.
- **Per-node JSON is derived client-side** by extracting the tag from this
  document — "View details" / "Copy JSON" need no per-tag RPC.
- The document is a **re-marshal**, not the original bytes (field order,
  omitempty, `[] → null`): compare with the stored profile semantically,
  not as a textual diff.
- Behind `with_lx_command` as usual; a tag-less build captures nothing and
  answers `Unimplemented`. Not-started → `FailedPrecondition`; started but
  no snapshot (the attached-service path) → `Unavailable`.

No other changes vs rc.2; upstream `testing` had no new commits at cut time.

#### v1.14.0-lx.16-rc.2

**⚠️ BREAKING (vs rc.1 only): the DNS group config contract is replaced.**
`mode: failover|race` and the `interval`/`down_time` fields from rc.1 are
GONE and such configs now **fail to load** with an explicit error. The group
was redesigned around a TTL record model before any consumer shipped — no
compatibility bridge is kept.

**DNS group v2 — TTL record model (SPEC 033/035, feature
[DNS_GROUP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)).**
Servers carry no states; two expiring record tables drive everything: an
**error** record (`error_ttl`, default 2m — written by any failed exchange,
erases the server's live wins) and a **win** record (`win_ttl`, default 5m —
only the first success of a fan-out; any success erases the server's live
errors). *Clean* = zero live errors; a network change amnesties both tables.

```json
{ "type": "group", "tag": "public",
  "servers": ["google", "cloudflare", "quad9"],
  "mode": "stable", "error_ttl": "2m", "win_ttl": "5m" }
```

- `mode: stable` (default) — stickiness before randomness: stay on the
  current server while it is clean, re-elect a random clean one only when it
  is not. Server order is NOT meaningful; there is no return-to-primary.
- `mode: fastest` — the clean server with the most live wins; when nobody
  has one, the query becomes an election fan-out to all clean members
  (single-flight — a burst never multiplies fans). Re-election rhythm is
  `win_ttl` expiry. No timers, no synthetic probes.
- `mode: parallel` — every query fans to all clean members (N× traffic by
  design; no wins recorded).
- Unified flow: the single target gets HALF the remaining request budget —
  the rescue fan is guaranteed the rest, so a blackholed server can no
  longer eat the whole deadline. With **no clean member** every mode makes
  exactly one attempt via the least dirty server and never fans (anti-storm
  on a dead network).
- Observability: events now carry `fanned` and `survival` flags on top of
  the probe trace; `GetDNSGroups` returns the live records per member
  (clean, live errors + age, live wins, current, last rtt). The rc.1 trace
  field `racer` and the v2 state fields (winner/ranking/…) are replaced —
  they never shipped to a consumer.
- The implementation survived a 24-agent adversarial review; all six
  confirmed defects (nil fan result, leaky Reset amnesty, election-window
  target trashing, and more) are fixed with regression tests.

Also in this build: the `lx_idle_teardown` explicit-`"0"` kill switch is now
distinguishable from an absent key (SPEC 020), docs were reconciled with the
code (AWG2 masquerade `id`/`ib`/`sip`, XHTTP GET fallback, DNS stream rcode
semantics), and upstream **v1.14.0-beta.2** is merged — upstream left alpha
(platform options restored, TLS/acme fixes, JSON schema, client-subnet DNS
cache, rule-level race/speculative evaluate actions).

#### v1.14.0-lx.16-rc.1

**New DNS server type `group`: DNS resolution no longer dies with one failed
server (SPEC 033/034/035, feature
[DNS_GROUP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)).**
Previously `dns.final` was a *default*, not a fallback, and a rule routing to
a server returned its transport directly — any network error, timeout or
SERVFAIL failed the query outright even with healthy servers in the config.
A `group` puts several servers behind one tag with a selection strategy:

```json
{ "type": "group", "tag": "public",
  "servers": ["google", "cloudflare"],
  "mode": "failover", "down_time": "30s" }
```

- `mode: failover` (default) walks `servers` in order; a transport error,
  timeout or SERVFAIL marks the member down for `down_time` (subsequent
  queries skip it), NXDOMAIN and empty answers are valid responses. With
  every member down, each query makes exactly one attempt via the member
  whose failure is the oldest.
- `mode: race` picks the fastest member by racing a **real** query: the
  first query after the previous race aged past `interval` (default 3m)
  fans out to all live members, the first success answers the query and
  becomes the pinned winner; arrival order forms the fallback ranking.
  No timers, no synthetic probe traffic — idle costs nothing (ENERGY
  invariant), and only the winner's answer is cached.
- A group is a first-class server: accepted in `final`, in rules and inside
  other groups (cycles are rejected at load, not at runtime). `fakeip` and
  `hosts` members are rejected — local sources cannot fail over.
- The DNS query stream (`SubscribeDNSQueries`) now attributes each answer to
  the member that actually produced it; cache hits and total-failure events
  keep the group tag. The protocol schema is unchanged — existing clients
  are compatible as-is.

This build also merges upstream `testing` (31 commits), including rule-level
`race`/`speculative` evaluate actions, client-subnet-aware DNS cache, a TCP
DNS retry fix, search-domain expansion fix, JSON schema support and the
openconnect/openvpn DNS server types. Upstream's `race` orchestrates queries
*within one resolution* and holds no state between queries; the lx `group` is
a composite *server* with health memory (`down_time`, pinned winner) — the
mechanisms live on different layers and compose.

#### v1.14.0-lx.15

**XHTTP no longer breaks behind a reverse proxy when the session id is carried
off-path (SPEC 002).** A VLESS + XHTTP config routed through nginx/CDN with
`mode: packet-up`, a trailing-slash `path` (e.g. `/upload/`) and the session id
placed in a header (`session_placement: header`) failed to connect with
`unexpected download status: 301 Moved Permanently`, while the same config
worked in v2rayNG. The client was unconditionally stripping the path's trailing
slash for every mode. That is only needed for stream-one's bare path; when the
session id is not placed in the path, the base path reaches the wire verbatim,
so `/upload/` became `/upload` — and an nginx `location /upload/ {}` answers a
301 redirect to the bare path, which the download request (a raw HTTP/2
round-trip that does not follow redirects) surfaces as a dial error. Default
configs (session id in the path) were unaffected. The fix keeps the configured
path as-is and trims the trailing slash only on stream-one's bare-path request,
so reverse-proxy routing matches for every other mode. Covered by a new
url_test case; details in
[SPEC 002 §9](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/002-XHTTP_CLIENT_TRANSPORT/SPEC.md).

This build also merges upstream `testing` (13 commits: async DNS refactor, a
WireGuard detour fix that converges with our SPEC 029, the OpenConnect
auth-challenge rework, and assorted fixes). The lx observability, detour and
urltest layers were reconciled on top and verified against their test suites.

#### v1.14.0-lx.14

**Stopping the tunnel no longer hangs 10+ seconds with many WireGuard endpoints
(SPEC 030).** On Android, stopping an instance with ~30 WG/AmneziaWG endpoints —
especially just after a health-check ping woke them from idle-suspend — could
hang for ten seconds or more. The cause was an ordering interaction:
`box.Close()` tore endpoints down while the idle/urltest tick was still issuing
wakes, so each endpoint's close blocked waiting for an in-flight ping-wake to
finish a full device rebuild and handshake (up to several seconds each), and
those waits added up serially across every endpoint. The fix quiesces the tick
and closes every WireGuard UDP socket up front (so the per-endpoint teardown no
longer blocks on a socket read), makes an in-flight wake abort the moment its
endpoint starts closing, and closes endpoints concurrently instead of one at a
time. No teardown step is skipped — sessions are still closed cleanly, keys
zeroed, sockets and the userspace netstack released — only the pointless waiting
is removed, so there is no risk of the crash a hard "drop everything" would
cause. Stop now completes in a fraction of a second. Covered by unit tests for
the close-abort gate (red without the fix, green with it) and a smoke stand that
times the concurrent close of many live endpoints; the SPEC 020 idle-suspend and
SPEC 028/029 detour stands still pass.

#### v1.14.0-lx.13

**Fixed a WireGuard/AmneziaWG endpoint dying permanently when its `detour`
provider is declared later in the config (SPEC 029).** An endpoint with
`detour: X` resolved that detour eagerly inside its own constructor — a
UDP-egress-anchor type-assertion (SPEC 020) walks the dialer's `Upstream()`
chain, and the detour dialer's `Upstream()` resolves the detour on the spot.
Endpoints are constructed in config-array order and only registered at the end
of their own construction, so an endpoint whose detour pointed at a provider
declared *after* it resolved that provider before it existed, and the dialer
cached "outbound detour not found" forever — the tunnel then never sent a single
packet (repeating `connect to server: outbound detour not found: X` in the log).
Reordering the config so the provider came first masked it by luck. The core
already starts nodes in dependency order (a node isn't started until every tag
in its `detour` chain has started), but the resolution was leaking out of that
barrier into the construction phase. The fix stops resolving the detour at
construction (the egress-anchor probe is skipped when a detour is set — it never
applies through a detour anyway) and instead resolves it in `Start`, after the
dependency topo-sort has brought the provider up. Config array order no longer
matters; a genuinely missing detour now fails loudly at startup instead of being
silently cached. Covered by an end-to-end stand that declares the consumer
before the provider and asserts traffic flows (red without the fix, green with
it); the SPEC 020 idle-suspend and SPEC 028 nested-tunnel stands still pass.

**Nested tunnels through `detour` now work — stop forcing DF on the outer UDP
socket (SPEC 028).** A `wireguard`/AmneziaWG endpoint or a `masque` outbound
opens its real (bottom-of-chain) UDP socket through `common/dialer`, which by
default disables IP fragmentation (`IP_MTU_DISCOVER=IP_PMTUDISC_DO` on
Linux/Android, `IP_DONTFRAG` on macOS). For a nested tunnel the outer datagram
is routinely oversize — encapsulation adds WireGuard's ~32 bytes plus AWG's
per-packet `s4` junk on top of an already full-size inner packet — so with DF
forced the OS silently drops it (`sendmsg: message too long`) instead of
fragmenting, and the inner tunnel never comes up or comes up and cannot carry
data. This is why `masque`/`wireguard`/AWG chained through `detour` failed on
device while direct nodes over the same detour worked. The endpoint and the
MASQUE outbound now default to `UDPFragmentDefault=true` (the same opt-out
direct/hysteria2/tuic already use), so the OS fragments an oversize outer
datagram and the far end reassembles it. An explicit `"udp_fragment": false` on
the node restores DF; other protocols and listener inbounds are unchanged.
Covered by a socket-flag unit test (both bind paths, both directions of the
explicit override) and an end-to-end AmneziaWG-over-AmneziaWG-through-detour
stand exercising both the fits and the IP-fragmenting (same-MTU) regime with
TCP/UDP ping-pong and large-data in both directions.

#### v1.14.0-lx.11

**Removed the AmneziaWG-over-WireGuard guard (SPEC 007).** The guard refused to
start an AmneziaWG endpoint whose `detour` chain reached a WireGuard-based
endpoint, because that combination used to hang the kernel on Android. That root
cause is gone on the current graft: the re-graft of AmneziaWG 2.0 onto
`sagernet/wireguard-go` v0.0.5 (lx.8), the transport-padding overrun fix (SPEC
025), and the reserved-clear gate that repaired the detour path (SPEC 026, lx.9)
together let AmneziaWG-over-WireGuard come up and carry traffic. Verified
end-to-end on a two-process loopback stand (upper AWG endpoint with `jc/s4` +
ranged `h1..h4` detouring through a plain WireGuard endpoint; handshake,
keepalive and HTTP-over-socks all flow). Both guards are deleted — the static
Start-guard (`protocol/wireguard`) and the runtime selector/urltest guard
(`protocol/group/awg_selector_guard.go`), along with the `OutboundManager.ConsumersOf`
and `AmneziaWGSuspendable` adapter hooks and every guard test. SPEC 020
idle-suspend is untouched: it shares the transport `Suspend`/`Resume` and the
`suspended` flag, and the "a deliberately-stopped endpoint is never resurrected
by a dial" invariant still holds (its tests were reworded from `guardSuspended`
to `stopped`). An Android field-test is still owed — the historical hang was an
Android-only symptom the mac stand cannot fully reproduce. The matching app-side
gate in LxBox (§130) is removed in step.

#### v1.14.0-lx.10

**Merge: upstream `testing` (tun udpnat, endpoint-listen refactor, OpenVPN /
OpenConnect).** Pulled 15 upstream commits. The load-bearing part is an upstream
WireGuard endpoint refactor: the dialer interface was generalised
(`WireGuardListener` → `UDPListener`, now returning an egress flag), a UDP-NAT
config surface was added (`udp_mapping` / `udp_filtering` / `udp_nat_max`), and an
egress-anchoring path (`EgressPool` / `SetEgressProvider`) was introduced. Our AWG
`NewClientBind` path and SPEC 020 idle-teardown guards are preserved; the new
UDP-NAT fields flow through the SPEC 020 rebuild recipe. OpenVPN and OpenConnect
land as new upstream transports but are **not** enabled in the fork's tag set
(endpoint/server-capable, out of the client-focused scope — same stance as
`with_usbip` / `with_clash_api` on the AAR).

**Re-graft: wireguard-go egress-provider API onto the AWG2 base.** Upstream's
`StdNetBind` gained `SetEgressProvider`, so the fork's submodule (one revision
behind) failed to build. Applied the upstream delta and extended SPEC 026: the new
egress receive path re-introduced an unconditional reserved-byte clear, now gated
behind `hasReserved()` like the other five sites — so a small-padding AmneziaWG
magic survives when no WARP reserved value is set, while WARP-over-egress still
clears (its bind sets a reserved value). (SPEC 026 §3.2 registry: 5 → 6 sites.)

#### v1.14.0-lx.9

**Fix: the WARP reserved-byte clear destroyed AmneziaWG magic headers.** Bytes
1-3 of every received datagram (the Cloudflare WARP "reserved" field) were
zeroed unconditionally in every bind. AmneziaWG reads its magic header as
`Uint32(packet[padding:])`, so with a small `s1`/`s2`/`s4` padding (0-3) the
magic sits in bytes 1-3 and the clear collapses it out of the ranged `h1`-`h4`
window — the packet, handshake included, is dropped and the endpoint never comes
up. Plain WireGuard (types 1-4, bytes 1-3 already zero) and AWG with padding ≥ 4
are unaffected, which is why it went unnoticed. All five receive-side clears
(`bind_std` receiveIP, `msgx_darwin` ×2, `bind_windows` ×2) plus the detour
`ClientBind` receive now zero bytes 1-3 only when a WARP reserved value is
actually configured. WARP behaviour is unchanged. Red/green tests bring up a
device pair with zero padding over both bind paths and assert delivery. (SPEC 026.)

Also fixes a data race in `ClientBind.connect()`: the lock-free fast-path read
of the cached connection raced the locked write (the send and receive goroutines
call it concurrently at tunnel start). The field is now an `atomic.Pointer`; the
double-checked-locking logic is unchanged. `go test -race` is clean.

#### v1.14.0-lx.8

Promoted to stable. Same code as `v1.14.0-lx.8-rc.1`, device-verified on
Android (CPH2411): the AmneziaWG endpoint that crashed on every data packet
(`s4=60`) now carries traffic under load, and the AWG download counter no
longer double-counts. No code change from rc.1.

#### v1.14.0-lx.8-rc.1

**Fix: AmneziaWG transport padding (`s4`) crashed the whole process.** Any
AWG endpoint with `s4 > 0` aborted with `SIGABRT` on its first data packet —
device-verified (CPH2411, AWG endpoint with `s4=60`): `panic: runtime error:
index out of range [123] with length 76` in `RoutineSequentialSender`. The
injection paths (`InputPacket`/`InputPackets`) sized the outbound buffer with
no headroom for the random prefix that `s4` prepends in-buffer, so the
right-shift ran off the end. The alloc sites now reserve `paddings.transport`,
the manual byte loop is an overlap-safe `copy`, and a defensive grow drops
packets that cannot fit a single WG message instead of overrunning. A
red/green device-level test reproduces the exact crash and pins the fix.

Four further defects in the AWG graft, same "config value crashes a
send/receive goroutine" class, fixed alongside:

* **RX byte accounting doubled** — the re-graft pasted the `rxBytes` / timers
  block twice on the receive hot path, so every AWG download byte was counted
  twice (and `keepKeyFreshReceiving` fired twice per batch). Deduplicated.
* **Swapped `jmin`/`jmax` panicked the first handshake** — UAPI validates the
  junk fields only individually; an inverted pair fed a non-positive bound to
  `rand.Int`. Bounds are swapped when inverted.
* **Out-of-range `i1`–`i5` obfuscator lengths** — a negative length panicked
  on a slice bound, a huge one OOMed the handshake `make`. Lengths are bounded
  to `[0, MaxMessageSize]`.
* **Full-range magic header wrapped to a zero bound** — `end-start+1` computed
  in `uint32` wrapped to 0 for a `0-4294967295` range, panicking `rand.Int`;
  widened to `int64` before the arithmetic.

Plain WireGuard (`s4=0`) was never affected — the broken branch does not run.

#### v1.14.0-lx.7

**Idle-suspend level 3, promoted to stable.** Same code as `v1.14.0-lx.7-rc.2`,
device-verified on Android (CPH2411, 3 WG/AWG endpoints): the full cycle
`suspend (idle=43s) → teardown (slept=5m19s) → rebuild (by=dial)` ran exactly on
model, including an AmneziaWG endpoint with junk obfuscation. The goroutine
profile confirms each stage — every Device goroutine gone after the teardown,
all back (recv-workers included) after the rebuild — and the node serves traffic
through the fresh netstack (161–252 ms). Details: SPEC 020 `TEST_PLAN §L3
RESULT`; what the levels are and how to tune them: [lx-energy.md](lx-energy.md)
([RU](lx-energy.ru.md)).

* **Expectation check on RAM (measured, worth knowing).** Level 3 frees exactly
  what it owns — netstack, Device objects, goroutines. But the *global*
  `sing/common/buf` pool (`GetOutboundBuffer → buf.Get`) is process-wide and
  survives `Close` by design; on the test config it held 63% of the heap
  (23.4 MB), so tearing down 3 sleeping nodes barely moved the total
  (36.7 → 36.3 MB). The bulk of the RAM win still comes from **level 1**
  (recv-buffers: −134 MB with 8 nodes measured earlier); level 3 reclaims the
  netstack (~5.9 MB/node) and pays off with *many* nodes, not three.

#### v1.14.0-lx.7-rc.2

**Self-review of rc.1's level 3** — a line-by-line re-read of the teardown
implementation found three defects, all in paths a device test would hit:

* **Crash: `PortAddresses()` on a torn-down endpoint** — the L3 layer
  (sing-tun preferred routes) may ask for port addresses at any moment; they
  are now served from a cached copy instead of the released tun device
  (nil-dereference before).
* **Silent L3 downlink break after a rebuild** — the attached sing-tun return
  path lived in the device wrapper that `Rebuild()` recreated; it is now
  carried over (sing-tun knows nothing about our teardown cycle and never
  re-attaches).
* **Dial hang after a failed rebuild** — a half-rebuilt endpoint (fresh tun
  device, `Start` failed, e.g. peer-domain resolution offline) would block the
  retry forever on the device's one-slot event channel, under the wake mutex —
  hanging every dial through the node. A failed rebuild now rolls back to the
  clean torn-down state via an idempotent `Teardown()`, so the next dial
  retries from scratch.

Spec synced to as-built (the teardown gate needs no live-traffic re-check —
`idleAsleep` already guarantees it; documented why), docs gained the level-3
column (lx-energy RU/EN, lx-config). New tests pin all three fixes, including
the partial-rebuild rollback.

#### v1.14.0-lx.7-rc.1

**Idle-suspend level 3: sleeping endpoints are now released completely.**
`Down()` (levels 1–2) frees the recv-workers and silences the timers, but the
gVisor netstack (~5.9 MB per endpoint) stays alive. The new third window tears
that down too — a node that has been *asleep* long enough is closed outright and
rebuilt on the next dial. Model and timelines: [lx-energy.md](lx-energy.md)
([RU](lx-energy.ru.md)); mechanism: SPEC 020 §"Третий уровень".

* **New `route.lx_idle_teardown`** — how long an already-sleeping endpoint may
  stay asleep before it is torn down: device closed, netstack/peers/queues
  freed, only its config left in memory. Counted **from the moment it fell
  asleep** (not from the last dial), so the window does not depend on which
  threshold put it to sleep. Absent → defaults to `lx_idle_suspend_reachable`;
  `0` disables teardown; requires `lx_idle_suspend` (start error otherwise, like
  the reachable window).
* **Wake = rebuild.** The device and its netstack are one-shot objects (their
  Close runs under a `sync.Once` and closes channels), so waking a torn-down
  endpoint recreates them from the stored recipe and re-runs both Start stages —
  roughly 0.5–1 s on the first dial, versus the ~1 RTT a merely-suspended
  endpoint pays. Concurrent dials serialise on one rebuild; a failed rebuild
  (e.g. peer-domain resolution) leaves the state untouched so the next dial
  retries.
* **Invariants preserved.** A guard-suspended AmneziaWG endpoint is never torn
  down and never rebuilt by a dial (the AWG-over-WG guard now clears the
  teardown flag too, SPEC 007); pause/wake over a torn-down endpoint is a no-op
  rather than a nil-deref; `Close` is idempotent over any sleep depth; a dial
  reaching the transport with no device fails cleanly instead of panicking; the
  transport's `Close` now also releases the tun device (a teardown/rebuild cycle
  used to leak it).
* Why it pays: beyond the RAM, fewer live objects mean more headroom under
  `SetMemoryLimit` — i.e. less GC pressure for the endpoints that are actually
  in use.

⚠️ Pre-release: level 3 is unit-tested (including a real teardown→rebuild cycle
over a live gVisor stack) but **not yet device-verified** — live plan in SPEC 020
`TEST_PLAN §L3`. Levels 1–2 are unchanged and were device-verified in lx.5.

#### v1.14.0-lx.6

Small maintenance release: one reported log-noise fix, plus an upstream merge.

* **`urltest`: the legacy-`tolerance` warning no longer fires once
  `balancer.pool_tolerance` is set** ([#7](https://github.com/Leadaxe/sing-box-lx/issues/7)).
  The `round_robin` startup hint — *"tolerance is ignored in round_robin mode;
  use balancer.pool_tolerance"* — was unconditional: it appeared on every start
  even for configs that already carry `pool_tolerance`, i.e. exactly where the
  hint carries no information. It now fires only while `pool_tolerance` is unset
  (there the user plausibly still expects `tolerance` semantics). An explicit
  `pool_tolerance: 0` is indistinguishable from an absent one (numeric field) and
  keeps warning — deliberate: `0` means first-live-fill, where the delay ranking
  `tolerance` would imply genuinely does not happen. Behaviour of `round_robin`
  itself is unchanged; this is log noise only.
* **Upstream `testing` merged** — docs/changelog, tailscale doc touch-ups, a Farsi
  locale fix, and `boxdd` insecure-mode/locale churn (upstream's own desktop
  daemon, not shipped by this fork). No lx seam touched.

#### v1.14.0-lx.5

**Energy revision (stable)** — a multi-agent audit of the idle-suspend ×
urltest combination (26 adversarially verified findings) with every confirmed
defect fixed, plus two new opt-in knobs. The full model is documented in
[docs-lx/lx-energy.md](lx-energy.md) ([RU](lx-energy.ru.md)). Same code as
pre-release `v1.14.0-lx.4-rc.2`, promoted to stable after on-device
verification (CPH2411, LxBox v2.15.4: prior behaviour and the new
suspend/wake/probe semantics confirmed working live).

* **Fixed: screen-off/on (or a network change) permanently resurrected every
  suspended WG/AWG tunnel** — pause-wake now skips devices the idle/guard state
  machine holds down; waking stays dial-only.
* **Fixed: AWG-over-WG guard holes** — a cache-restored WireGuard selection
  slipped past the guard on app restart (Android kernel hang), and urltest
  groups (auto-switch, round_robin pool) had no guard at all. The Start walk
  now resolves groups through their current choice; both urltest paths guard
  before committing.
* **Fixed: idle-suspend could cut live connections** — an endpoint carrying an
  established download but evicted from the active route was downed
  mid-transfer. The tick now consults the device's established-TCP gauge
  (keepalive-immune) and a ≥4 KiB transfer delta before suspending; peers with
  `persistent_keepalive` still fall asleep.
* **Fixed: the 30-minute probe tail** — after a selector switched away, the
  abandoned group kept probing (and waking) its members until `idle_timeout`;
  probe cycles are now skipped while the group is unreachable.
* **New: `route.lx_idle_suspend_reachable`** — a second, longer idle window
  after which even *reachable* endpoints (pool members, the selected node,
  final, DNS detours) suspend; they wake lazily on the next dial (+1 RTT).
* **New: `urltest.passive_check`** — a recent successful TCP dial through a
  node counts as proof of liveness; while fresh, least_test skips whole probe
  cycles and the round_robin first-live path skips confirmed slots.
* Smaller fixes: first auto-selection now invalidates the reachability cache;
  DNS-server detours are seeded reachable (no Down/Up flap around quiet gaps);
  `listen_port` endpoints are never suspended; DNS resolves before wake;
  32-bit rotation-counter overflow (index panic) and uint16 `pool_tolerance`
  overflow; `pool_tolerance` validated ≤ 15000 ms; manual URLTest of a
  balancer group is force again; Touch/Close and tick-shutdown races.
* Specs 007/019/020 synced (incl. a full state-machine model with diagrams in
  SPEC 020); unit-tested with `-race` across tag combinations and
  device-verified via LxBox v2.15.4.
* Merges upstream `testing` (start-lifecycle fix, boxdd insecure mode /
  reworked data protection, tailscale Windows SSH sessions).

#### v1.14.0-lx.4

**NaïveProxy release** — it was broken on **both** desktop platforms and is now
fixed on both: Windows archives were missing `libcronet.dll`, and the darwin
binaries could never load cronet at all. Also merges upstream `testing`
(incl. a naive slow-open fix and a cronet-go bump).

* **darwin now builds with CGO on a macOS runner — NaïveProxy works there for
  the first time.** The darwin binaries were cross-compiled from ubuntu with
  `with_purego`, which loads `libcronet.dylib` at runtime — but cronet-go
  publishes **no macOS dylib at all** (its darwin lib modules carry only a
  static `libcronet.a`), so the tag was on and the outbound was dead. Darwin
  moved to its own `macos-latest` job with `CGO_ENABLED=1` and the tag set
  minus `with_purego` — upstream `build_darwin` parity — so `libcronet.a` is
  linked statically and there is nothing to install next to the binary. The
  job's verify step runs a `naive` config through `sing-box check` on the
  runner (arm64 natively, amd64 under Rosetta).
* **Windows archives now ship `libcronet.dll` next to `sing-box.exe`.** The
  desktop build uses `with_purego`: cronet is not baked into the binary — the
  loader dlopens `libcronet.dll` from the exe's directory at startup. The
  upstream "Extract libcronet.dll" packaging step was lost when the release
  workflow was ported, so **every previously released windows zip lacked the
  dll** and any config with a `naive` outbound failed at startup with
  `cronet: library not found`. The dll is now extracted from the cronet-go lib
  module pinned in `go.mod` (go.sum-verified — not upstream's `extract-lib`,
  which resolves the latest `go`-branch commit and can skew ahead of the purego
  bindings) and packed into the zip. Keep it next to `sing-box.exe`.
  Contract recorded in SPECS/004 (§2.4 per-target table + HISTORY.md).
* **Upstream `testing` merged** (real delta since the last integration; upstream
  force-pushed testing, so most of the 180 incoming commits were re-delivered
  content we already carried): netns/unshare support, windivert driver
  lifecycle hardening, `boxdd` platform daemon, OOM report, **naive slow-open
  fix**, hysteria2 realm `ip_version`/`port_mapping`, cronet-go bump
  (`CRONET_GO_VERSION` 98d539ce → 617d38f4) and dep bumps (sing-tun, sing-quic,
  sing-snell, tailscale, nftables), plus `boxdd` data protection (upstream's own
  desktop daemon — not part of what this fork ships). All lx seams re-anchored
  (SPEC 014/015 command extensions, SPEC 017 detour chain, SPEC 018 DNS stream,
  SPEC 020 idle-suspend, AWG plumbing); upstream's `with_usbip` AAR-tag addition
  not taken (client focus). The AmneziaWG graft needed no re-graft — upstream did
  not move the `wireguard-go` pin.

No runtime change to the AAR payload: this release is about how the desktop
binaries are built and packaged. Both `libbox.aar` and `libbox-legacy.aar` are
rebuilt from the merged base.

#### v1.14.0-lx.3

**Stable release** (published as "Latest", not a pre-release) — a promotion of
`v1.14.0-lx.3-rc.2`, **device-verified**. Functionally identical to that rc; no
runtime change since it.

This release carries two payloads accumulated across the rc line:

* **DNS-query stream re-architected onto the command multiplex** (from `rc.1`) —
  the §180 structured DNS stream (`SubscribeDNSQueries`) moved from a standalone
  client subscription to a first-class member of the CommandClient multiplex,
  laid out identically to `CommandConnections`. It now auto-reconnects with the
  profiler client and dies with it, fixing the field bug where the DNS stream went
  silent after the app was backgrounded (Doze) and never recovered while TCP/UDP
  kept flowing. Requires the matching LxBox client migration (task §261).
* **AmneziaWG re-grafted onto wireguard-go v0.0.5 + upstream merge** (from `rc.2`) —
  merges upstream `testing` (L3-forwarding, snell, bridge outbound) and rebases the
  AWG 2.0 obfuscation graft onto the wireguard-go bump it carried. SPEC 020
  idle-suspend was re-homed onto the new L3-forwarding endpoint API (the
  `resumeOnDial` wake guard moved to `WritePackets`, the single point every
  L3-forwarded packet transits). No config or behaviour change for AWG endpoints.
  The new `PlatformInterface` bridge methods (`usePlatformBridge`/`createBridge`)
  are stubbed off on the client — the Android VPN uses its single VpnService TUN,
  not a platform bridge.

Both `libbox.aar` and `libbox-legacy.aar` are shipped; the LxBox client builds
against this AAR (the bridge-interface stubs land with it).

#### v1.14.0-lx.3-rc.2

**Pre-release** — merges upstream `testing` (14 commits, incl. L3-forwarding,
snell, bridge outbound) and re-grafts AmneziaWG onto the wireguard-go bump that
came with it. Carries the `rc.1` DNS-multiplex payload forward unchanged. Ships a
new `libbox.aar`.

* **AmneziaWG re-grafted onto wireguard-go v0.0.5.** Upstream bumped
  `sagernet/wireguard-go` v0.0.3 → v0.0.5 for L3-forwarding (batched
  `InputPackets`, a size-based outbound buffer pool, Darwin batch UDP I/O). The
  AWG 2.0 obfuscation graft was rebased onto that base (submodule
  `e5feca7` → `1adc4c7`): 15 of 16 grafted files applied clean, only `send.go`
  conflicted on a single backpressure line. The `MessageEncapsulatingTransportSize
  = 0` invariant is preserved, so upstream's rewritten `InputPacket`/`InputPackets`
  and buffer pool compose with the obfuscation hooks without a manual re-weave.
  No config or behaviour change for AWG endpoints.
* **SPEC 020 idle-suspend re-homed onto the new L3-forwarding endpoint API.**
  Upstream replaced the direct-route endpoint interface
  (`PrepareConnection`/`NewDirectRouteConnection`) with a flow API
  (`PreMatchFlow`/`PortAddresses`/`PortMTU`/`AttachReturn`/`DetachReturn`/`JudgeFlow`).
  The idle-suspend wake guard (`resumeOnDial`) moved to `WritePackets` — the single
  point every L3-forwarded packet (including established flows that bypass
  `DialContext`) transits — so a suspended WG/AWG endpoint still wakes lazily on
  first traffic. `Down`/`Up`/`BindUpdate` are unchanged on v0.0.5; the mechanism is
  otherwise untouched.
* **Docs (SPEC 003, SPEC 020) rewritten to current-state methodology.** Both
  SPEC.md files now describe the current architecture top-down; the chronology
  (graft-base evolution, the idle-tick bug, the rejected GRO experiment, the
  v0.0.3→v0.0.5 delta) moved to per-spec `HISTORY.md`.

#### v1.14.0-lx.3-rc.1

**Pre-release** — DNS-query stream (SPEC 018) re-architected onto the command
multiplex. Ships a new `libbox.aar`; the LxBox client must migrate (task §261).

* **DNS stream is now a multiplexed command, uniform with connections** — the §180
  structured DNS-query stream (`SubscribeDNSQueries`) moved from a standalone
  client subscription to a first-class member of the CommandClient multiplex, laid
  out identically to `CommandConnections`: `addCommand(CommandDNS)`, a
  `handleDNSStream` on the shared client context, and `WriteDNSQuery` on
  `CommandClientHandler`. The stream now auto-reconnects with the profiler client
  and dies with it — no per-stream subscription, no bespoke reconnect. This fixes
  the field bug where the DNS stream went silent after the app was backgrounded
  (Doze) and never recovered, while TCP/UDP kept flowing. The core emission layer
  (`common/dnstrack`, `HasSubscribers` gate, event shape) is unchanged; only the
  client transport moved. Removes the standalone `SubscribeDNSQueries` client
  method, `DnsQuerySubscription` and `DnsQueryHandler`. Server/proto untouched.

#### v1.14.0-lx.2

**Stable release** (published as "Latest", not a pre-release) — a promotion of
`v1.14.0-lx.2-rc.1`, **device-verified**. Functionally identical to that rc; no
runtime change since it (only CI/docs commits — the SPEC 023 toolchain mirror).

The `rc.1` payload is a **correctness/stability pass** over the whole lx delta
(SPEC 022 deep-audit remediation) — no new features, no config changes. Two
behavioural fixes headline it:

* **MASQUE (h2): a stalled CONNECT could wedge the outbound forever** — the h2
  CONNECT-IP handshake ignored the dial context, so a peer that completed TCP+TLS
  but never returned the CONNECT HEADERS parked the dial with no timeout (and the
  outbound could then be neither reused nor closed). The handshake is now bounded
  by the dial ctx.
* **AmneziaWG: a guard-suspended endpoint could be resurrected by a dial** — the
  AmneziaWG-over-WireGuard guard (which prevents an Android kernel hang) now clears
  the SPEC 020 idle flag under the shared lock, so a guard-suspended endpoint stays
  down.

Plus the full SPEC 022 batch (IPv4 checksum over header options, an XHTTP reader
data race, DNS query events on fresh cache hits, `GetPool` nested-group delay, the
AmneziaWG `s4`-only MTU budget, robust MASQUE h3 login-failure matching) and doc /
comment / dead-code hygiene. Full register:
[`SPECS/TASKS/022-LX_DEEP_AUDIT`](../SPECS/TASKS/022-LX_DEEP_AUDIT/SPEC.md).

Build infrastructure: the musl router builds now restore the Chromium toolchain
from a durable release-asset mirror before falling back to `snapshot.debian.org`,
so a Debian-snapshot outage no longer blocks releases
([`SPECS/TASKS/023`](../SPECS/TASKS/023-MUSL_TOOLCHAIN_MIRROR/SPEC.md)).

#### v1.14.0-lx.2-rc.1

**Pre-release.** SPEC 022 deep-audit remediation — a correctness/stability pass
over the whole lx delta, no new features and no config changes. A full audit of
the fork's code (10 axes, adversarial verification) surfaced 27 real findings; 24
are fixed here, 3 skipped by design. Upstream base is unchanged (already carries
`v1.14.0-alpha.37`). **Not yet device-verified** — staged as a pre-release before
promotion to a stable `lx.2`; the two behavioural fixes below want a live check.

* **MASQUE (h2): a stalled CONNECT could wedge the outbound forever.** The h2
  CONNECT-IP handshake ignored the dial context, so a peer that completed TCP+TLS
  but never returned the CONNECT `:status` HEADERS parked the dial in `ReadFrame`
  with no timeout — and because establishment is serialized, the outbound could no
  longer be reused *or* closed. The handshake is now bounded by the dial ctx (a
  watcher trips the conn deadline on timeout/cancel and is retired before the
  long-lived read loop starts).
* **AmneziaWG: a guard-suspended endpoint could be resurrected by a dial.** The
  AmneziaWG-over-WireGuard guard (which keeps such an endpoint down because that
  combination hangs the Android kernel) shared state with SPEC 020 idle-suspend;
  if the endpoint was idle-suspended *before* the guard fired, the next dial woke
  it back up. `SuspendAmneziaWG` now clears the idle flag under the shared lock, so
  a guard-suspended endpoint stays down.
* **Other fixes.** IPv4 checksum recomputed over the full header incl. options
  (CONNECT-IP TTL path); a data race in the XHTTP stream reader removed; DNS query
  events now emitted on a fresh cache hit in the Lookup path (SPEC 018 parity);
  `GetPool` reads a nested-group member's delay under the right history key; the
  AmneziaWG MTU budget counts only `s4` (transport padding), not `s3` (cookie
  padding); the MASQUE h3 login-failure hint matches the TLS alert robustly; plus
  documentation corrections (`ip=sip` decoy shape, `s4`/MTU, `no_grpc_header`) and
  a batch of comment/dead-code hygiene. Full register in
  [`SPECS/TASKS/022-LX_DEEP_AUDIT`](../SPECS/TASKS/022-LX_DEEP_AUDIT/SPEC.md).

#### v1.14.0-lx.1

**First full release of the 1.14 lx line** (published as "Latest", not a
pre-release) — a promotion of the `rc.1`–`rc.22` series. Functionally identical
to rc.22 plus the new MIPS asset below; carries the whole rc-line feature set:
MASQUE CONNECT-IP outbound (Cloudflare WARP), AmneziaWG 2.0, XHTTP, native
CommandClient extensions, `urltest` round_robin + sticky, WG idle suspend, and
the static musl router builds. Upstream base is still `v1.14.0-alpha.*` —
pinned at the merge noted in the release notes header.

* **New release asset: `linux-mips-softfloat`** (big-endian MIPS — OpenWrt
  `mips_24kc`, e.g. Atheros AR93xx) — requested in
  [#6](https://github.com/Leadaxe/sing-box-lx/issues/6). Pure-Go static build:
  runs on musl/OpenWrt as-is, but **without NaïveProxy** — Chromium/cronet has
  no big-endian MIPS toolchain, so `with_naive_outbound`/`with_purego` are
  dropped for this target only. Everything else (AWG, XHTTP, MASQUE, Clash API,
  …) matches the desktop tag set.

#### v1.14.0-lx.1-rc.22

**Pre-release.** MASQUE diagnostics + the lx branch is now the release branch. No
behaviour change to the tunnel itself vs rc.21 — MASQUE (CONNECT-IP over h3/h2)
works exactly as before.

* **MASQUE: transport-phase debug logging.** The dial path now logs its phases
  (`establishing <h3|h2> tunnel to <server> (sni=…)` → `udp socket up, starting QUIC
  handshake` → `tunnel established` / `tunnel failed: <err>`), so a stuck dial is
  diagnosable from the core log alone — no goroutine dump needed. This pinpoints
  whether a hang is the UDP socket or the QUIC handshake: on networks that filter
  inbound UDP:443, h3 hangs in the handshake (ClientHello left, no ServerHello back)
  while h2 (CONNECT-IP over TCP:443) works — use `network: "h2"` there.

* **Release branch moved to `lx`.** rc-tags are now cut from the `lx` branch (which
  carries the full 1.14 line + all SPECs); release notes/links reference `lx`.

#### v1.14.0-lx.1-rc.21

**Pre-release.** New outbound: **MASQUE (CONNECT-IP / RFC 9484)** for Cloudflare WARP
(SPEC 021). Tunnels whole IP packets over HTTP/3 or HTTP/2 to a WARP endpoint via a
userspace gVisor stack — device-verified end-to-end (`warp=on`) on both transports.
No change to any existing feature; MASQUE is a new `type: masque` outbound, gated on the
already-shipped `with_quic` + `with_gvisor` tags (both in `LX_TAGS`).

* **`type: masque` outbound — CONNECT-IP over h3 and h2.** One outbound with a `profile`
  field (`cloudflare` default | `standard`) and a `network` transport selector
  (`h3` QUIC default | `h2` HTTP/2). The Cloudflare profile carries WARP's non-RFC
  quirks (`cf-connect-ip`, tolerates the missing Extended-CONNECT settings, ECDSA
  public-key pinning, WARP SNI/URI defaults). Key material (`private_key` / `public_key`
  / `ip` / `ipv6`) is taken ready from config — device registration stays client-side.
  Full config reference in [SPECS/021/CONFIG.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/021-MASQUE_CONNECT_IP_OUTBOUND/CONFIG.md).

* **h2 via a manual HTTP/2 framer, no extra dependency.** WARP never advertises
  `SETTINGS_ENABLE_CONNECT_PROTOCOL`, so the high-level h2 clients refuse the request;
  the h2 path is driven directly on `golang.org/x/net/http2`'s public `Framer` + `hpack`
  (already a dependency). `connect-ip-go` is vendored under `transport/masque/connectip`
  (ported onto `sagernet/quic-go`) rather than pulled as an external module.

* **Stateless when idle + self-healing.** The tunnel is an ephemeral session: after
  `idle_timeout` (default 5m) of no traffic it is fully torn down — gVisor netstack,
  pump goroutines and QUIC keepalive all released — and rebuilt lazily on the next dial.
  A dropped tunnel (WARP idle-timeout, GOAWAY, network change) likewise self-heals on
  the next dial. Both `idle_timeout` and `keep_alive_period` are configurable.

* **Hardening (post-implementation audit).** A single malformed inbound datagram no
  longer blackholes the tunnel (drop-and-continue); the paired pump can no longer leak
  on teardown; the ICMP "packet too big" reply quotes the pre-mutation header; the h2
  path bounds peer-declared capsule sizes and uses real flow-control backpressure; and
  the hot path reuses send scratch buffers instead of allocating per packet.

#### v1.14.0-lx.1-rc.20

**Pre-release.** A small XHTTP robustness fix, an upstream sync, and documentation
closing out the SPEC 020 no-GRO experiment. No behaviour change to the shipped
idle-suspend (rc.19) — `route.lx_idle_suspend` works exactly as before.

* **XHTTP: `uplink_http_method: "GET"` outside packet-up no longer aborts.** When a
  config sets `GET` as the uplink method but the stream mode is not packet-up (where
  GET is meaningful), the core now soft-falls-back to POST instead of failing the
  config load. A misconfigured method degrades gracefully rather than taking the
  whole instance down.

* **Docs — SPEC 020 no-GRO experiment recorded and REJECTED.** The idea of a global
  "GRO off + receive batch 8" as a simpler alternative to Down/Up idle-suspend was
  measured on a real device and rejected for three reasons (SPEC.md §14): the main
  Android RAM holder is `messageBuffers` (`PreallocatedBuffersPerPool`, ~100 MB),
  which does **not** depend on the receive batch (the batch-sized `bufsArrs` held
  only ~14 MB); the env switch never reaches Go's `os.Getenv` on Android; and a
  hardcoded batch=8 crashed at start (SIGABRT — `device.BatchSize()=max(bind,tun)`
  clamped back to 128 via the TUN offload). Down/Up (rc.19) stays the only viable
  mechanism. Report + raw pprof/crash artifacts under
  `SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/ANDROID_RESEARCH/nogro-experiment/`; the
  experiment code was NOT merged (kept only on the now-deleted `-nogro-*` branches,
  documented for the record).

* **Upstream sync.** Merged `upstream/testing`: "Fix udpnat2 buffer size" (go.mod/go.sum
  bump) and "release: Fix update apple version script". No lx zones touched.

#### v1.14.0-lx.1-rc.19

**Pre-release.** Gates **idle-suspend (SPEC 020) behind a new build tag
`with_lx_idle_suspend`** and confirms the memory win **on real Android hardware**.
Idle-suspend (rc.18) frees the recv-worker `bufsArrs` of idle+unreachable WG/AmneziaWG
endpoints — but those buffers are only large where `BatchSize=128` (Android/Linux). This
release makes that platform scope explicit in the build.

* **`with_lx_idle_suspend` — mobile-only build tag.** The idle-suspend tick now compiles
  only with this tag, which is baked into the **Android/iOS AAR** (`build_libbox`) but
  **not** the desktop/CLI `LX_TAGS` (`Makefile.lx`). A desktop build has a small
  `BatchSize`, so the feature would save almost nothing there; to prevent a silent
  mismatch, a binary built **without** the tag that is handed a config with
  `route.lx_idle_suspend` now **fails fast at start** — `route.lx_idle_suspend is set but
  this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend
  (mobile-only feature)` — instead of a silent no-op. The gate is a single function
  (`startIdleSuspend`), so the dial hot path and the upstream group files are untouched.
  When the option is unset the tag is a clean no-op either way (byte-for-byte upstream
  behaviour).

* **On-device Android verification (closes the RESEARCH.md device gap).** Measured on a
  physical CPH2411 (Android 15, arm64) via the app's pprof passthrough: with 9 WG
  endpoints (1 reachable + 8 idle/unreachable), suspending the 8 dropped
  `PopulatePools.func3` (`bufsArrs`) live heap from **223.9 MB → 89.9 MB (−134 MB,
  −60 %)** and recv-worker goroutines **18 → 2**, matching the `~8.4 MB/worker` model
  exactly — roughly 10× the desktop RSS delta, on the platform the feature was built for.
  Suspend/wake/no-flap/kill-switch all confirmed on-device. Full report + raw pprof
  artifacts in `SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/ANDROID_RESEARCH/`.

* Docs: `SPEC.md` rewritten as the as-built implementation spec; the original
  root-cause/measurement doc renamed `SPEC.md → RESEARCH.md`; `lx-config.md` (+ ru)
  document the new tag and its mobile-only scope. Added unit tests for the no-tag stub
  (option set → error, unset → no-op) plus reachability tests for the production
  nested-group topology.

* CI: pin `gh release create --repo` to this fork — the base-version step adds an
  `upstream` remote for git-describe, which made `gh` target SagerNet/sing-box (403).

* Upstream: synchronised — `upstream/testing` is 0 commits ahead of this base.

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
