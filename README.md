**English** · [Русский](README.ru.md)

# sing-box-lx

> **A thin downstream fork of [SagerNet/sing-box](https://github.com/SagerNet/sing-box).**
> A small set of client-side features on top of upstream — **XHTTP**, **AmneziaWG 2.0**, **MASQUE** (CONNECT-IP / Cloudflare WARP), plus an **observability layer** (CommandClient extensions) and **round_robin load balancing** — each behind its own build tag.
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
| **➡ sing-box-lx** (this repo) | **small set (XHTTP, AWG2, observability, load balancing)** | **thin: new files behind build tags, minimal upstream touch** | **rebase of atomic `// lx` commits onto upstream tags** |

**How we differ:**

- **Minimal divergence.** New code lives in new files. Existing upstream files are touched only inside tiny marked seams `// lx:begin … // lx:end`. → cheap rebases.
- **Build-tag isolation.** Features turn on via `with_xhttp` / `with_awg`. A build **without** them is byte-for-byte the upstream behavior — features break nothing by default.
- **Identity preserved.** The Go module stays `github.com/sagernet/sing-box`, the binary is still named `sing-box`. The `-lx` suffix lives only in the version string (`1.13.13-lx.N`).
- **Build tags are sing-box's own convention**, not our invention (`with_quic`, `with_wireguard`, …). We just apply it with maximum discipline.

> We do **not** depend on the "kitchen-sink" forks — they are used only as a wire-protocol reference.

---

## Features & status

| # | Feature | What it is | Status |
|---|---------|------------|--------|
| **XHTTP** | client transport | Xray-compatible "splithttp" (modes `auto`/`packet-up`/`stream-up`/`stream-one`) over Reality/TLS/h2c | ✅ **live-validated** against a real Xray (3x-ui) server (packet-up/auto): handshake + DNS + HTTPS + download. `stream-one` has a known framing bug |
| **AmneziaWG 2.0** | client endpoint | WireGuard obfuscation: `Jc/Jmin/Jmax`, `S1–S4`, `H1–H4` + **2.0**: `I1–I5` (CPS — decoy packets) | ✅ builds, passes `check`; dependency **activated** ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet base + obfuscation); **validated against a real AWG2 server**: handshake + keepalive + outbound traffic |
| **Masquerade `id/ip/ib`** | AWG sugar | WireSock-style declarative masquerade over `I1`: name a domain (`id`) + protocol (`ip`: `quic`/`dns`/`stun`/`sip`) + browser (`ib`) and the core builds the client-initiated `I1` decoy for you — `quic` = out-of-order fragmented Initial (i1+i2), `dns`/`stun`/`sip` = query/Binding-Request/INVITE | ✅ **`ip=quic` device-proven against a real LTE/WARP DPI** (~330 ms, eases Cloudflare WARP); `dns`/`stun`/`sip` build & pass `check` but are blocked as a protocol class to the WARP edge — for other providers |
| **Observability (CommandClient)** | libbox gRPC | Native `CommandClient` extensions (SPEC 014–018, build tag `with_lx_command`): `URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `GetPool`, `SubscribeDNSQueries` (structured live DNS stream — domain, qtype, rcode, CNAME chain, process attribution, dnsServer/outbound) + `Connection.detourList` (detour tail as its own field) | ✅ shipped across the rc series and consumed by the Android consumer (LxBox) |
| **Load balancing (`round_robin`)** | urltest mode | Group-level load balancing on `urltest` (SPEC 019): `mode: round_robin` + `balancer{ pool, pool_tolerance, sticky_hash }`; FNV-64a slot binding with `sticky_hash` components `process\|domain\|source_ip\|dest_ip\|dest_port` (default `["process","domain"]`, `["none"]` = off) — `GetPool` exposes the live slots (behind `with_lx_command`) | ✅ builds, passes `check`; even rotation locally (10/10/10) and **device-verified end to end** on a real multi-node pool — rc.15 fixed the `domain`-key collapse (reads `metadata.Domain`, which survives the router's domain→IP resolve), taking on-device per-domain uniformity from ~0.27 to 0.95+ |
| **MASQUE** (`type: masque`) | client outbound | CONNECT-IP (RFC 9484) over HTTP/3 **or** HTTP/2 for **Cloudflare WARP** (SPEC 021): tunnels whole IP packets through a userspace gVisor stack; `profile` (`cloudflare`/`standard`), `network` (`h3`/`h2`), ECDSA public-key pinning, idle-suspend + self-healing reconnect. h2 is a hand-rolled framer over `x/net/http2` (no extra dep); `connect-ip-go` vendored | ✅ **device-verified end to end on Wi-Fi and LTE** (`warp=on`, real traffic on both `h3` and `h2`); on networks that filter inbound UDP:443 the `h3` handshake hangs — use `network: h2` (TCP:443) there |

Detailed reports: [`SPECS/002-…`](SPECS/002-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md), [`SPECS/003-…`](SPECS/003-AWG2_CLIENT_ENDPOINT/IMPLEMENTATION_REPORT.md) and [`SPECS/009-…`](SPECS/009-WIRESOCK_MASQUERADE_PROFILES/IMPLEMENTATION_REPORT.md). Full config reference — **[docs-lx/lx-config.md](docs-lx/lx-config.md)**.

> **Not supported (Reality layer, deferred):** post-quantum Reality (`pqv` / ML-DSA-65) and Xray's `spiderX`. These are Xray-specific Reality features absent from sing-box, and Reality is the upstream TLS layer we keep untouched (it is not one of our features). Classic X25519 Reality works; a server that *mandates* post-quantum Reality won't connect. This is a sing-box limitation — best addressed upstream (we'd inherit it on rebase).

---

## Build

Building goes through a separate **`Makefile.lx`** (the upstream `Makefile` is untouched):

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build
# → ./sing-box binary with a version like 1.13.13-lx.1
```

> `--recurse-submodules` is required for `with_awg`: the AmneziaWG runtime is wired in as the submodule `submodules/wireguard-go` → [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx).

Under the hood it is a plain `go build` with this tag set (`make -f Makefile.lx lx-print-tags` is the single source of truth):

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg
```

That is upstream's client feature-set **minus** the server/irrelevant tags — `with_acme` (server-side cert issuance), `with_tailscale`, `with_ccm`/`with_ocm` (AI-proxy services) — **plus** `with_purego` (CGO-free cross-compile, so `with_naive_outbound`/cronet builds at `CGO=0` on every desktop target except the Windows 7 / 32-bit legacy build, which drops naive — `cronet-go` has no windows/386) and our features `with_xhttp` / `with_awg`. Everything else is exactly upstream.

Validate configs:

```bash
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
```

> `lx-test/config/` holds our samples (upstream `test/` is a separate Go module — we don't use it).

**Android (`libbox.aar`).** `make lib_install && make lib_android` builds the gomobile AAR — `libbox.aar` (SDK 23) + `libbox-legacy.aar` (SDK 21) — with `with_xhttp`/`with_awg` baked in (and `tailscale` dropped), for embedding in an Android consumer app (needs NDK r28 + OpenJDK 17). `Libbox.version()` reports `…-lx.N`.

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

### AmneziaWG 2.0 (endpoint)

AWG fields are promoted directly onto `WireGuardEndpointOptions`:

```jsonc
{
  "type": "wireguard",
  // … standard wireguard fields (private_key, address, peers, …) …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,
  "i1": "<b 0x...><r 12>", "i2": "", "i3": "", "i4": "", "i5": ""   // 2.0 CPS
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

`ip` ∈ `quic|dns|stun|sip`; `id` is required only for `quic` (SNI); for `dns`/`sip` it is optional (a pseudo name is generated when absent) and `stun` ignores it. Where set it appears on the
wire — SNI / QNAME) and optional for `sip` (pseudo-host generated when absent) and `stun`;
`ib` ∈ `chrome|firefox|curl` (quic only, minimal — no JA3 fingerprint). Mutually exclusive
with an explicit `i1`.

For **`quic`** the core emits an out-of-order fragmented QUIC Initial (RFC 9001) — a real
ClientHello split across CRYPTO frames in a shuffled order so a line-rate DPI parses garbage
and fails open. The layout is randomized per call (no cross-user signature), and `ip=quic`
now sends **two** independent Initials (i1+i2) so the flow reads as a developing QUIC session.
This is the **only profile device-proven against a real LTE/WARP DPI** (~330 ms). `dns`/`stun`/
`sip` are implemented as correct client-initiated requests but are blocked as a protocol class
toward the Cloudflare WARP edge (raw DNS/STUN/SIP to a datacenter IP is itself anomalous) —
they are kept for other providers whose DPI only checks packet well-formedness. See
[docs-lx/lx-config.md](docs-lx/lx-config.md) and [SPECS/009 examples](SPECS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md).

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
  "network": "h3",               // TRANSPORT: h3 (QUIC) | h2 (HTTP/2). NOT tcp/udp — that's network_list
  "sni": "www.microsoft.com",    // domain-fronting; endpoint auth is public-key pinning, not SNI
  "private_key": "<base64 DER EC>",
  "public_key":  "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Key material (`private_key`/`public_key`/`ip`/`ipv6`) comes ready from config — the client does
the WARP device registration. On networks that filter inbound UDP:443 the `h3` handshake hangs;
switch that node to `network: h2` (TCP:443). Full reference —
[docs-lx/lx-config.md §4](docs-lx/lx-config.md) and [SPECS/021](SPECS/021-MASQUE_CONNECT_IP_OUTBOUND/CONFIG.md).

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
| `option/v2ray_xhttp.go`, `option/wireguard_awg.go` | feature options |
| `include/v2rayxhttp.go` | transport registration behind a build tag |

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
| Config reference | [docs-lx/lx-config.md](docs-lx/lx-config.md) |
| Spec Kit | [SPECS/](SPECS/) — [README](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [IMPLEMENTATION_PROMPT](SPECS/IMPLEMENTATION_PROMPT.md) |

---

## License

Inherits the upstream sing-box license (**GPL-3.0**). All edits are marked `// lx` and distributed under the same license. This is an unofficial fork, not affiliated with SagerNet.
