**English** · [Русский](README.ru.md)

# sing-box-lx

> **A thin downstream fork of [SagerNet/sing-box](https://github.com/SagerNet/sing-box).**
> Exactly two client-side features on top of upstream — **XHTTP** and **AmneziaWG 2.0** — and nothing else.
> Goal: live by rebasing onto every upstream tag, not by drifting into a separate life.

> 📄 The upstream sing-box README — **[on GitHub](https://github.com/SagerNet/sing-box/blob/main/README.md)** (always current).

This is not a separate project and not an "improved sing-box". It is upstream sing-box **plus two things**, implemented so they can be carried onto new sing-box versions for years with almost no conflicts.

---

## What makes it different

In the sing-box ecosystem, forks that add XHTTP / AmneziaWG fall into two camps — and `sing-box-lx` is in neither:

| Fork | Features | Approach | Upstream sync |
|------|----------|----------|---------------|
| **SagerNet/sing-box** (upstream) | baseline | — | — |
| **shtorm-7/sing-box-extended** | dozens (WARP, MASQUE, MTProxy, XHTTP, AWG2, …) | "kitchen sink", edits everywhere | separate branch, no rebasing onto tags |
| **amnezia-vpn/amnezia-box**, **hoaxisr/amnezia-box** | AWG only | heavy fork, in-place edits | branch sync (`dev-next`/`stable-next`) |
| **➡ sing-box-lx** (this repo) | **XHTTP + AWG2 only** | **thin: new files behind build tags, minimal upstream touch** | **rebase of atomic `// lx` commits onto upstream tags** |

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
| **XHTTP** | client transport | Xray-compatible "splithttp" (modes `auto`/`packet-up`/`stream-up`/`stream-one`) over Reality/TLS/h2c | 🟡 builds, passes `check`; `sessionId` cross-checked against Xray; **no live test against an Xray server yet** (no server) |
| **AmneziaWG 2.0** | client endpoint | WireGuard obfuscation: `Jc/Jmin/Jmax`, `S1–S4`, `H1–H4` + **2.0**: `I1–I5` (CPS — decoy packets) | ✅ builds, passes `check`; dependency **activated** ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet base + obfuscation); **validated against a real AWG2 server**: handshake + keepalive + outbound traffic |

Detailed reports: [`SPECS/002-…`](SPECS/002-F-O-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md) and [`SPECS/003-…`](SPECS/003-F-C-AWG2_CLIENT_ENDPOINT/IMPLEMENTATION_REPORT.md). Full config reference — **[docs/lx-config.md](docs/lx-config.md)**.

---

## Build

Building goes through a separate **`Makefile.lx`** (the upstream `Makefile` is untouched):

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build
# → ./sing-box binary with a version like 1.13.13-lx.1
```

> `--recurse-submodules` is required for `with_awg`: the AmneziaWG runtime is wired in as the submodule `submodules/wireguard-go` → [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx).

Under the hood it is a plain `go build` with this tag set:

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_xhttp,with_awg
```

Validate configs:

```bash
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
```

> `lx-test/config/` holds our samples (upstream `test/` is a separate Go module — we don't use it).

---

## Feature configuration

> Full field tables, defaults and an `awg-quick`→JSON mapping — **[docs/lx-config.md](docs/lx-config.md)**. A quick taste below.

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

---

## Maintenance model

```
upstream tag (vX.Y.Z)
        │
        └─►  branch lx = upstream + N atomic // lx commits
                 ├─ FORK_BOOTSTRAP (Makefile.lx, CI, version)
                 ├─ XHTTP client transport
                 └─ AWG2 client endpoint
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
| `.github/workflows/lx-ci.yml` | CI: feature matrix (baseline/xhttp/awg/full) + negative check + cross-platform |
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

The core is built for the desktop launcher **singbox-launcher** (which bundles `bin/sing-box`). Mapping `type=xhttp` and AWG fields in the wizard are launcher-side tasks, not here.

---

## Links

| | |
|---|---|
| Upstream | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [docs](https://sing-box.sagernet.org/) |
| This fork | [Leadaxe/sing-box-lx](https://github.com/Leadaxe/sing-box-lx) |
| AmneziaWG runtime | [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet base + obfuscation (3-way merge) |
| AmneziaWG upstream | [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [docs.amnezia.org](https://docs.amnezia.org/documentation/amnezia-wg/) |
| XHTTP origin | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — `transport/internet/splithttp` |
| Config reference | [docs/lx-config.md](docs/lx-config.md) |
| Spec Kit | [SPECS/](SPECS/) — [README](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [IMPLEMENTATION_PROMPT](SPECS/IMPLEMENTATION_PROMPT.md) |

---

## License

Inherits the upstream sing-box license (**GPL-3.0**). All edits are marked `// lx` and distributed under the same license. This is an unofficial fork, not affiliated with SagerNet.
