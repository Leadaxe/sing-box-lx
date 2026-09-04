**English** · [Русский](README.ru.md)

# sing-box-lx

> **A thin downstream fork of [SagerNet/sing-box](https://github.com/SagerNet/sing-box).**
> A small set of client-side features on top of upstream — **XHTTP**, **AmneziaWG**, **MASQUE** (CONNECT-IP / Cloudflare WARP), the post-quantum **VLESS `encryption`** layer, a **DNS server group**, an **observability layer** (CommandClient extensions), **round_robin load balancing**, **idle-suspend energy saving**, the headless **`lxd` daemon** and the **`chain` outbound** (a virtual multi-hop path of groups and nodes) — isolated in lx-owned files, most behind their own build tags.
> The set may grow; the philosophy doesn't: live by rebasing onto every upstream tag, not by drifting into a separate life.

> 📄 The upstream sing-box README — **[on GitHub](https://github.com/SagerNet/sing-box/blob/main/README.md)** (always current).

This is not a separate project and not an "improved sing-box". It is upstream sing-box **plus a few features**, implemented so they can be carried onto new sing-box versions for years with almost no conflicts. More features may land over time — other protocols, new capabilities — but every one of them must live by the same thin-fork rules ([CONSTITUTION](SPECS/CONSTITUTION.md)).

---

## What makes it different

In the sing-box ecosystem, forks that add XHTTP / AmneziaWG fall into two camps — and `sing-box-lx` is in neither:

| Fork | Features | Approach | Upstream sync |
|------|----------|----------|---------------|
| **SagerNet/sing-box** (upstream) | baseline | — | — |
| **shtorm-7/sing-box-extended** | dozens (WARP, MASQUE, MTProxy, XHTTP, AWG2, …) | "kitchen sink", edits everywhere | separate branch, no rebasing onto tags |
| **amnezia-vpn/amnezia-box**, **hoaxisr/amnezia-box** | AWG only | heavy fork, in-place edits | branch sync (`dev-next`/`stable-next`) |
| **➡ sing-box-lx** (this repo) | **small set (XHTTP, AWG2, MASQUE, VLESS PQ encryption, DNS group, observability, balancing, energy, `chain`)** | **thin: new files behind build tags, minimal upstream touch** | **rebase of atomic `// lx` commits onto upstream tags** |

**How we differ:**

- **Minimal divergence.** New code lives in new files. Existing upstream files are touched only inside tiny marked seams `// lx:begin … // lx:end`. → cheap rebases.
- **Build-tag isolation.** Features turn on via `with_xhttp` / `with_awg`. A build **without** them is byte-for-byte the upstream behavior — features break nothing by default.
- **Identity preserved.** The Go module stays `github.com/sagernet/sing-box`, the binary is still named `sing-box`. The `-lx` suffix lives only in the version string (`1.14.0-lx.N`).
- **Build tags are sing-box's own convention**, not our invention (`with_quic`, `with_wireguard`, …). We just apply it with maximum discipline.

> We do **not** depend on the "kitchen-sink" forks — they are used only as a wire-protocol reference.

---

## Features & status

| # | Feature | What it is | Status |
|---|---------|------------|--------|
| **XHTTP** | client transport | Xray-compatible "splithttp" (modes `auto`/`packet-up`/`stream-up`/`stream-one`) over Reality/TLS/h2c, with `xmux` connection reuse (SPEC 059) | ✅ **live-validated** against real Xray servers: packet-up/auto (handshake + DNS + HTTPS + download), and `stream-one` (the `auto`+REALITY path) **device-verified** since `v1.14.0-lx.17` — SPECs [042](SPECS/TASKS/042-XHTTP_STREAM_GRPC_CONTENT_TYPE/SPEC.md)/[043](SPECS/TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md) fixed the gRPC Content-Type parity and the trailing-slash 404 that used to hang it. `xmux` restores parity with Xray servers whose configs carry an `xmux` section (previously ignored silently) and saves a full TCP+TLS handshake per stream |
| **AmneziaWG** | client endpoint | Full obfuscation set: junk packets `Jc/Jmin/Jmax`, junk headers `S1–S4`, magic headers `H1–H4` (single or ranged), controlled packet sequences `I1–I5`, plus WireSock-style `Id/Ip/Ib` masquerade sugar over `I1`; **AWG 3.x**: `header_protection_key`, `content_padding_addition`, `random_trailers`, `disable_cookies`, ranged timings and a ranged `persistent_keepalive_interval` | ✅ builds, passes `check`; dependency **activated** ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet base + obfuscation); **validated against a real AWG server**: handshake + keepalive + outbound traffic. Parity audited against `amneziawg-tools` and the kernel module's netlink contract — every obfuscation parameter the official implementations accept is implemented ([SPEC 031](SPECS/TASKS/031-AWG_PARITY_AUDIT_ADVANCED_SECURITY/SPEC.md)); **AWG 3.1 verified against a live `amnezia-awg2` server** — first-try handshake, HTTPS, 1 MB download, in-range rekey ([SPEC 080](SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md)) |
| **Masquerade `id/ip/ib`** | AWG sugar | WireSock-style declarative masquerade over `I1`: name a domain (`id`) + protocol (`ip`: `quic`/`dns`/`stun`/`sip`) + browser (`ib`) and the core builds the client-initiated `I1` decoy for you — `quic` = out-of-order fragmented Initial (i1+i2), `dns`/`stun`/`sip` = query/Binding-Request/INVITE | ✅ **`ip=quic` device-proven against a real LTE/WARP DPI** (~330 ms, eases Cloudflare WARP); `dns`/`stun`/`sip` build & pass `check` but are blocked as a protocol class to the WARP edge — for other providers |
| **Observability (CommandClient)** | libbox gRPC | Native `CommandClient` extensions (SPEC 014–018, 035, 037; build tag `with_lx_command`): `URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `GetPool`, `GetDNSGroups`, `GetRunningConfig` (the canonical JSON the running box was actually built from), `GetChains` (state of `chain` outbounds), `SubscribeDNSQueries` (structured live DNS stream — domain, qtype, rcode `-1`=failure, CNAME chain, process attribution, dnsServer/dnsServerType/outbound) + `Connection.detourList` (detour tail as its own field) | ✅ shipped in stable tags, consumed by the Android consumer (LxBox). Feature — [OBSERVABILITY](SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md) |
| **Load balancing (`round_robin`)** | urltest mode | Group-level load balancing on `urltest` (SPEC 019): `mode: round_robin` + `balancer{ pool, pool_tolerance, sticky_hash }`; FNV-64a slot binding with `sticky_hash` components `process\|domain\|source_ip\|dest_ip\|dest_port` (default `["process","domain"]`, `["none"]` = off) — `GetPool` exposes the live slots (behind `with_lx_command`) | ✅ builds, passes `check`; even rotation locally (10/10/10) and **device-verified end to end** on a real multi-node pool — rc.15 fixed the `domain`-key collapse (reads `metadata.Domain`, which survives the router's domain→IP resolve), taking on-device per-domain uniformity from ~0.27 to 0.95+. Feature — [URLTEST_BALANCE](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) |
| **MASQUE** (`type: masque`) | client outbound | CONNECT-IP (RFC 9484) over HTTP/3 **or** HTTP/2 for **Cloudflare WARP** (SPEC 021): tunnels whole IP packets through a userspace gVisor stack; `profile` (`cloudflare`/`standard`), `vhttp` (`h3`/`h2`), the standard `tls` block, ECDSA public-key pinning, idle-suspend + self-healing reconnect. h2 is a hand-rolled framer over `x/net/http2` (no extra dep) whose TLS goes through the shared `common/tls`; `connect-ip-go` vendored | ✅ **device-verified end to end on Wi-Fi and LTE** (`warp=on`, real traffic on both `h3` and `h2`); on networks that filter inbound UDP:443 the `h3` handshake hangs — use `vhttp: "h2"` (TCP:443) there. ⚠️ Config shape changed in SPEC 062: `network`→`vhttp`, flat `sni`/`skip_cert_verify`/`fragment*` → the `tls` block (old shape deprecated until `v1.14.0-lx.30`) |
| **VLESS `encryption`** | outbound field | Post-quantum `mlkem768x25519plus` layer *inside* VLESS (SPEC 032) — beneath the transport, independent of TLS/REALITY; spec string `mlkem768x25519plus.<native\|xorpub\|random>.<0rtt\|1rtt>….<key>`, `""`/`"none"` = off; client half only (ported from `starifly/sing-box`, same GPL-3.0 + upstream base) | ✅ shipped in `v1.14.0-lx.18`, **device-verified**: +10 previously dead subscription nodes (6/8 WS, 4/4 gRPC), no other transport group moved. Feature — [VLESS_ENCRYPTION](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) |
| **DNS server group** | DNS server type | `type: group` DNS server (SPEC 033/035): modes `stable`/`fastest`/`parallel` as facets of one TTL model (error/win TTLs, fan-out with a guaranteed budget, `survival` visibility of degradation); live state via `GetDNSGroups` (behind `with_lx_command`) | ✅ code + tests + DoD, shipped; adversarial review (24 agents) clean. Field verification on device pending. Feature — [DNS_GROUP](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md) |
| **Idle-suspend (energy)** | route options | Three sleep levels for idle WG/AWG endpoints — `route.lx_idle_suspend` / `lx_idle_suspend_reachable` / `lx_idle_teardown` (SPEC 020) plus `urltest.passive_check` (SPEC 019): battery, heat and RAM savings on multi-node mobile profiles; tag `with_lx_idle_suspend` (baked into the Android AAR) | ✅ **device-verified** (SPEC 020): recv-workers 16→0, RSS −31%; guide — [docs-lx/lx-energy.md](docs-lx/lx-energy.md). Feature — [ENERGY](SPECS/FEATURES/008-ENERGY/FEATURE.md) |
| **`lxd` daemon** | headless subcommand | `sing-box lxd` keeps the core **in-process behind a management channel that outlives every config change** (SPEC 055–057): gRPC + admin-REST on one port, `apply` validated in a subprocess with automatic rollback to last-good, mTLS where the daemon is its own CA and clients enrol by one-time code, system service install, `.srs`/geo resource store, host telemetry (CPU per core, memory, thermal, disks, interfaces) and an IP→device directory; build tag `with_lxd` | ✅ device-verified on macOS (enrolment, both service roles, rollback). Guide — [docs-lx/lxd-daemon.md](docs-lx/lxd-daemon.md); gRPC reference — [lxd-grpc-api.md](docs-lx/lxd-grpc-api.md). Feature — [LXD_DAEMON](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md) |
| **`chain` outbound** (`type: chain`) | client outbound | A virtual multi-hop path assembled at runtime from groups and nodes (SPEC 073): positions in packet order, any position a node/endpoint/group; groups are never copied — picked nodes are served by runtime links dialing through the previous position (lazy, warmed up, evicted by `idle_timeout`); `direct` at position ≥ 1 is a transparent off-switch; tunnel links get MTU lowered automatically; `strip` (one-sided DPI tricks off by default) / `rewrite` (merge-patch per type); path in `detourList`, `GetChains` RPC, per-layer latency via URLTest on hop tags; build tag `with_lx_chain` | ✅ code + unit tests + live stand on real shadowsocks hops (`lx-test/chain`); on-device verification of WireGuard links pending. Feature — [CHAIN](SPECS/FEATURES/015-CHAIN/FEATURE.md) |
| **Failover on dial errors** | urltest behaviour | A `least_test` group now reacts to **live dial failures**, not just probe results (SPEC 054): a dead-path error penalises the node and retries once through the best candidate; ranking degrades to penalties-then-latency; penalties reset only on proof of life | ✅ shipped, consumer of the 15 s netstack deadline (SPEC 052). Feature — [URLTEST_BALANCE](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) |

Detailed reports: [`SPECS/TASKS/002-…`](SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md), [`SPECS/TASKS/003-…`](SPECS/TASKS/003-AWG2_CLIENT_ENDPOINT/IMPLEMENTATION_REPORT.md) and [`SPECS/TASKS/009-…`](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/IMPLEMENTATION_REPORT.md). Config overview — **[docs-lx/lx-config.md](docs-lx/lx-config.md)**; full parameter reference — **[docs-lx/lx-protocols-transports.md](docs-lx/lx-protocols-transports.md)**.

> **Not supported (Reality layer, deferred):** post-quantum Reality (`pqv` / ML-DSA-65) and Xray's `spiderX`. These are Xray-specific Reality features absent from sing-box, and Reality is the upstream TLS layer we keep untouched (it is not one of our features). Classic X25519 Reality works; a server that *mandates* post-quantum Reality won't connect. This is a sing-box limitation — best addressed upstream (we'd inherit it on rebase).

---

## Build

Building goes through a separate **`Makefile.lx`** (the upstream `Makefile` is untouched):

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build
# → ./sing-box binary with a version like 1.14.0-lx.18
```

> `--recurse-submodules` is required for `with_awg`: the AmneziaWG runtime is wired in as the submodule `submodules/wireguard-go` → [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx).

Under the hood it is a plain `go build` with this tag set (`make -f Makefile.lx lx-print-tags` is the single source of truth):

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg,with_lx_command,with_lxd,with_openvpn,with_openconnect,with_lx_chain
```

That is upstream's client feature-set **minus** the server/irrelevant tags — `with_acme` (server-side cert issuance), `with_ccm`/`with_ocm` (AI-proxy services) — **plus** `with_purego` (CGO-free cross-compile, so `with_naive_outbound`/cronet builds at `CGO=0` on every desktop target except the Windows 7 / 32-bit legacy build, which drops naive — `cronet-go` has no windows/386), upstream's `with_openvpn` / `with_openconnect` / `with_tailscale` (the `tailscale` endpoint is in the desktop and router binaries, not in the Android AAR), and our features `with_xhttp` / `with_awg` / `with_lx_command` / `with_lxd` / `with_lx_chain`. Everything else is exactly upstream.

Our two tags are independent by design (SPEC 067): **`with_lx_command`** carries the libbox command-protocol extensions (`URLTestOutbound`, `GetRules`, `GetGroups`, … — what LxBox lives on), **`with_lxd`** carries the `lxd/` package and the daemon subcommand. The Windows 7 legacy build ships without `with_lxd` (no Windows service and no log rotation there, so the subcommand would exist without what makes it a daemon) while keeping the RPC. The Android AAR never had the daemon: gomobile builds `experimental/libbox`, which does not import `lxd/`.

The Go toolchain is pinned in **`go.version`** (SPEC 049) — read by every `setup-go` step across the `lx-*.yml` workflows. It is deliberately **not** `go-version-file: go.mod`: that resolves to 1.24.x, which kills every quic-go outbound in an Android AAR (SPEC 044). The upstream version the fork is based on lives in **`upstream.version`**, bumped by hand on re-graft.

Validate configs:

```bash
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
```

> `lx-test/config/` holds our samples (upstream `test/` is a separate Go module — we don't use it).

**Android (`libbox.aar`).** `make lib_install && make lib_android` builds the gomobile AAR — `libbox.aar` (SDK 23) + `libbox-legacy.aar` (SDK 21) — with `with_xhttp`/`with_awg`/`with_lx_command`/`with_lx_idle_suspend`/`with_lx_chain` baked in (and `tailscale`/`clash_api` dropped — external Clash dashboards are a desktop concern), for embedding in an Android consumer app (needs NDK r28 + OpenJDK 17). `Libbox.version()` reports `…-lx.N`.

---

## Feature configuration

> Full field tables, defaults and an `awg-quick`→JSON mapping — **[docs-lx/lx-config.md](docs-lx/lx-config.md)**. A quick taste below.

### XHTTP (outbound transport)

```jsonc
"transport": {
  "type": "xhttp",
  "host": "example.com",
  "path": "/xhttp",
  "mode": "auto"          // auto | packet-up | stream-up | stream-one
}
```

### AmneziaWG (endpoint)

AWG fields are promoted directly onto `WireGuardEndpointOptions`:

```jsonc
{
  "type": "wireguard",
  // … standard wireguard fields (private_key, address, peers, …) …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": "1000-2000", "h4": 4,   // single value or "N-M" range
  "i1": "<b 0x...><r 12>", "i2": "", "i3": "", "i4": "", "i5": ""   // CPS
}
```

> `I1–I5` are configuration (not negotiated on the wire): values must **match on client and server**, and are case-sensitive.

**Masquerade sugar (`id`/`ip`/`ib`).** Instead of hand-writing `i1`, name a domain,
protocol and browser — the core builds the `I1` decoy (WireSock-style). Great for
easing **Cloudflare WARP**:

```jsonc
{
  "type": "wireguard",
  // … standard wireguard fields …
  "id": "www.google.com", "ip": "quic", "ib": "chrome"   // quic: id carried as the ClientHello SNI
  // or: "ip": "dns",  "id": "www.google.com"   // dns/sip: id carried as QNAME/host
}
```

`ip` ∈ `quic|dns|stun|sip`; `id` is required only for `quic` (it becomes the ClientHello SNI);
for `dns`/`sip` it is optional (a pseudo name is generated when absent; where set it goes on
the wire as QNAME / host) and `stun` ignores it. `ib` ∈ `chrome|firefox|curl` (quic only,
minimal — no JA3 fingerprint). Mutually exclusive with an explicit `i1`.

For **`quic`** the core emits an out-of-order fragmented QUIC Initial (RFC 9001) — a real
ClientHello split across CRYPTO frames in a shuffled order so a line-rate DPI parses garbage
and fails open. The layout is randomized per call (no cross-user signature), and `ip=quic`
now sends **two** independent Initials (i1+i2) so the flow reads as a developing QUIC session.
This is the **only profile device-proven against a real LTE/WARP DPI** (~330 ms). `dns`/`stun`/
`sip` are implemented as correct client-initiated requests but are blocked as a protocol class
toward the Cloudflare WARP edge (raw DNS/STUN/SIP to a datacenter IP is itself anomalous) —
they are kept for other providers whose DPI only checks packet well-formedness. See
[docs-lx/lx-protocols-transports.md §2](docs-lx/lx-protocols-transports.md#2-amneziawg-20-awg2) and [AWG2 feature](SPECS/FEATURES/003-AWG2/FEATURE.md) · [examples](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md).

### MASQUE (outbound — Cloudflare WARP)

A `masque` outbound tunnels whole IP packets over **CONNECT-IP (RFC 9484)**, HTTP/3 or HTTP/2,
to **Cloudflare WARP**. Not to be confused with the AWG `id/ip/ib` *masquerade* sugar above —
different feature, same word.

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "profile": "cloudflare",       // cloudflare (WARP) | standard (RFC 9484)
  "vhttp": "h3",                 // HTTP version: h3 (QUIC) | h2 (HTTP/2). tcp/udp is network_list
  "tls": {
    "server_name": "www.microsoft.com"   // domain-fronting; auth is public-key pinning, not SNI
  },
  "private_key": "<base64 DER EC>",
  "public_key":  "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Key material (`private_key`/`public_key`/`ip`/`ipv6`) comes ready from config — the client does
the WARP device registration. On networks that filter inbound UDP:443 the `h3` handshake hangs;
switch that node to `vhttp: "h2"` (TCP:443).

> **Config shape changed in SPEC 062.** The HTTP version is `vhttp` (it used to be `network`,
> which means the opposite everywhere else), and TLS settings live in the standard `tls` block
> (`sni` → `tls.server_name`, `skip_cert_verify` → `tls.insecure`, …). The old fields still work
> and report a deprecation until **v1.14.0-lx.30** — migration table in
> [docs-lx/lx-protocols-transports.md §3.10](docs-lx/lx-protocols-transports.md#310-migrating-from-the-pre-spec-062-shape). The default SNI is `www.cloudflare.com`, not
> the endpoint hostname: naming the MASQUE endpoint in the ClientHello is what DPI filters on.

Full reference —
[docs-lx/lx-protocols-transports.md §3](docs-lx/lx-protocols-transports.md#3-masque-outbound-connect-ip--warp) and [MASQUE_WARP feature](SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md).

### VLESS `encryption` (post-quantum layer)

A flat `encryption` field on a `vless` outbound enables the `mlkem768x25519plus`
handshake *inside* VLESS — beneath the transport and independent of TLS/REALITY:

```jsonc
{
  "type": "vless",
  "uuid": "…",
  "encryption": "mlkem768x25519plus.native.0rtt.<ML-KEM-768 key>"
}
```

Absent or `"none"` = layer off, behavior identical to upstream. Client half only
(`decryption` is server-side and deliberately not ported). See
[VLESS_ENCRYPTION feature](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md).

### DNS server group

A `group` DNS server wraps several upstream DNS servers into one with modes
`stable` / `fastest` / `parallel` on a TTL model (separate error/win TTLs,
fan-out with a guaranteed budget). State is exposed to the UI via `GetDNSGroups`
(behind `with_lx_command`). See [docs-lx/lx-config.md §5](docs-lx/lx-config.md)
and [DNS_GROUP feature](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md).

---

### `chain` (outbound — virtual multi-hop path)

A multi-hop route assembled at runtime from what the groups pick right now; positions are
listed **in packet order** (entry first, exit last) and any position may be a node, an
endpoint or a group of any nesting. Groups are never copied — a node picked at position ≥ 1
is served by a runtime **link** that dials through the previous position; links are lazy,
warmed up for deterministic positions, removed after `idle_timeout` with no live
connections. `direct` at position ≥ 1 is transparent (a runtime off-switch for a hop).

```jsonc
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],   // entry → exit
  "idle_timeout": "5m",
  "strip": { "multiplex.padding": false },        // one-sided DPI tricks are stripped from links by default
  "rewrite": { "wireguard": { "mtu": 1200 } }      // merge-patch per node type, links only
}
```

Tunnel links (WireGuard, MASQUE) get their MTU lowered automatically by the exact overhead
of IP tunnels below them. Observability: path in `detourList`, `GetChains` RPC / Clash API
`chain` field, per-layer latency via URLTest on the internal hop tags `<tag>#0`, `<tag>#1`, ….
See [docs-lx/lx-config.md §9](docs-lx/lx-config.md) and the
[CHAIN feature](SPECS/FEATURES/015-CHAIN/FEATURE.md).

## The `lxd` daemon

`sing-box lxd` (build tag `with_lxd`) hosts the core **in-process** behind a control channel
that belongs to the daemon rather than to the box instance — so it survives every config change
and stays reachable exactly when the data plane is down:

```bash
sing-box lxd --state-dir ./lxd-state -c config.json
```

- **Reload without losing the channel.** `POST /admin/apply` writes the candidate, validates it
  in a **subprocess** (a crash cannot take the daemon with it), swaps the instance, and promotes
  it to *last-good* only after a successful start. A failed start rolls back automatically; an
  interrupted apply is remembered and never booted.
- **One port, two planes.** `application/grpc` goes to the same `daemon.StartedService` the
  Android line uses; everything else is admin-REST (plain stdlib client, Windows 7 friendly).
- **mTLS with enrolment.** The daemon is its own CA and prints an `address#fingerprint#code`
  invite; a client pins the server, enrols with the one-time code, and is known by its
  certificate afterwards. `client add / list / remove` manages trust, loopback-only.
- **Observability without a second port** — `/admin/memory`, `/admin/stats`, `/admin/logs`,
  `/admin/pprof/*`, plus **host** telemetry (`/admin/host`: CPU per core, memory measured against
  *available* rather than *free*, thermal zones, disks, file descriptors; `/admin/host/interfaces`)
  and an **IP → device** directory for a network inspector (`/admin/clients-info`).
- **Service install** on macOS (`--service=install` / `install-user`); on Linux the daemon prints
  the recipe instead of touching the disk (systemd and OpenWrt/procd), by design.

📖 Operator's guide — **[docs-lx/lxd-daemon.md](docs-lx/lxd-daemon.md)** ([RU](docs-lx/lxd-daemon.ru.md));
observability plane for clients (same contract over the gRPC daemon **and** the Android AAR) — [docs-lx/lxd-grpc-api.md](docs-lx/lxd-grpc-api.md) ([RU](docs-lx/lxd-grpc-api.ru.md));
an OpenWrt walkthrough (VPN on a dedicated SSID) — [docs-lx/openwrt-vpn-ssid.md](docs-lx/openwrt-vpn-ssid.md).

---

## Maintenance model

```
upstream tag (vX.Y.Z)
        │
        └─►  branch lx = upstream + N atomic // lx commits
                 ├─ FORK_BOOTSTRAP (Makefile.lx, CI, version)
                 ├─ XHTTP client transport
                 ├─ AWG2 client endpoint
                 └─ … (future features — same atomic // lx commits)
```

- **Rebase only, never merge.** On a new upstream tag, the `lx` branch is rebased on top of it.
- Each feature is atomic commit(s) marked `// lx`. New files never conflict; the seams in upstream files are small and re-applied by hand.
- Development follows **Spec Kit** (`SPECS/NNN-T-S-NAME/`: SPEC → PLAN → TASKS → IMPLEMENTATION_REPORT).

### Remotes

```bash
origin    git@github.com:Leadaxe/sing-box-lx.git   # default branch: lx
upstream  https://github.com/SagerNet/sing-box.git
```

---

## Layout of the lx-specific bits

| Path | Purpose |
|------|---------|
| `Makefile.lx` | build with lx tags and the `-lx` version |
| `.github/workflows/lx-ci.yml` | CI: feature matrix (baseline/xhttp/awg/full) + negative check + cross-platform + android AAR |
| `.github/workflows/lx-release.yml` | release on `v*-lx.*`: desktop ×6 + `libbox.aar` → GitHub Release |
| `SPECS/` | Spec Kit (constitution, tasks, reports) |
| `lx-test/config/` | sample configs for `sing-box check` |
| `transport/v2rayxhttp/` | XHTTP client (new package) |
| `transport/wireguard/device_awg.go` | AWG IpcSet parameters (behind `with_awg`) |
| `submodules/wireguard-go` | submodule: merged AmneziaWG runtime fork ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx)) |
| `option/v2ray_xhttp.go`, `option/wireguard_awg.go`, `option/masque.go` | feature options |
| `include/v2rayxhttp.go` | transport registration behind a build tag |
| `submodules/gvisor` | submodule: pinned gVisor snapshot with our handshake nil-guard ([Leadaxe/gvisor-lx](https://github.com/Leadaxe/gvisor-lx)) |
| `submodules/sing-tun` | submodule: sing-tun fork with the acceptLoop self-heal ([Leadaxe/sing-tun-lx](https://github.com/Leadaxe/sing-tun-lx)) |
| `protocol/chain/` | `chain` outbound: hops, runtime links, strip/rewrite/MTU (behind `with_lx_chain`) |
| `lxd/` | the `lxd` daemon: admin-REST, mTLS, service install, host telemetry (behind `with_lxd`) |
| `go.version` / `upstream.version` | pinned Go toolchain (read by every CI `setup-go`) / the upstream version the fork is based on |

Find every upstream-file edit: `grep -rn "// lx"`.

---

## Consumer

The core is built for the desktop launcher **singbox-launcher** (which bundles `bin/sing-box`). On Android, the consumer embeds **`libbox.aar`** (gomobile) instead of the binary — the same config JSON applies. Mapping `type=xhttp` and AWG fields in the wizard are consumer-side tasks, not here.

---

## Links

| | |
|---|---|
| Upstream | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [docs](https://sing-box.sagernet.org/) |
| This fork | [Leadaxe/sing-box-lx](https://github.com/Leadaxe/sing-box-lx) |
| AmneziaWG runtime | [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet base + obfuscation (3-way merge) |
| AmneziaWG upstream | [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [docs.amnezia.org](https://docs.amnezia.org/documentation/amnezia-wg/) |
| XHTTP origin | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — `transport/internet/splithttp` |
| Config overview | [docs-lx/lx-config.md](docs-lx/lx-config.md) ([RU](docs-lx/lx-config.ru.md)) — every downstream feature, build tags, short examples |
| Protocols & transports | [docs-lx/lx-protocols-transports.md](docs-lx/lx-protocols-transports.md) ([RU](docs-lx/lx-protocols-transports.ru.md)) — full parameter reference for XHTTP, AmneziaWG, MASQUE |
| Fork changelog | [docs-lx/lx-changelog.md](docs-lx/lx-changelog.md) — the source `lx-release.yml` extracts release notes from |
| Energy guide | [docs-lx/lx-energy.md](docs-lx/lx-energy.md) — idle-suspend levels, passive_check, tuning |
| `lxd` operator's guide | [docs-lx/lxd-daemon.md](docs-lx/lxd-daemon.md) ([RU](docs-lx/lxd-daemon.ru.md)) — install, daemon.json, mTLS, admin REST |
| Observability API | [docs-lx/lxd-grpc-api.md](docs-lx/lxd-grpc-api.md) ([RU](docs-lx/lxd-grpc-api.ru.md)) — the observability contract clients speak (gRPC daemon + Android AAR) |
| OpenWrt walkthrough | [docs-lx/openwrt-vpn-ssid.md](docs-lx/openwrt-vpn-ssid.md) ([RU](docs-lx/openwrt-vpn-ssid.ru.md)) — VPN on a dedicated SSID |
| Reference cores | [docs-lx/lx-reference-cores.md](docs-lx/lx-reference-cores.md) — where to look for wire-protocol answers |
| Release runbook | [docs-lx/lx-release-runbook.md](docs-lx/lx-release-runbook.md) — upstream merge + tagging ritual |
| Spec Kit | [SPECS/](SPECS/) — [README](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [IMPLEMENTATION_PROMPT](SPECS/IMPLEMENTATION_PROMPT.md) |

---

## License

Inherits the upstream sing-box license (**GPL-3.0**). All edits are marked `// lx` and distributed under the same license. This is an unofficial fork, not affiliated with SagerNet.
