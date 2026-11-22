# Who uses sing-box-lx

[Русская версия](consumers.ru.md)

Projects that ship, embed or build on [sing-box-lx](https://github.com/Leadaxe/sing-box-lx), a client core based on sing-box with XHTTP, AmneziaWG, MASQUE / Cloudflare WARP, VLESS encryption, TrustTunnel, the `lxd` daemon and observability.

## First-party consumers

| Consumer | Platform | What it takes from here |
|---|---|---|
| [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) | desktop | the `sing-box` binary (bundled as `bin/sing-box`), optionally the `lxd` daemon |
| [LxBox](https://github.com/Leadaxe/LxBox) | Android | `libbox.aar` and the `CommandClient` extensions |
| OpenWrt routers | mips / musl builds | the binary as `lxd` with the installer scripts |

## Projects built on sing-box-lx

Independent projects that ship, embed or build on this core. They are not maintained here; the list reflects their public repositories as of 2026-11-23.

**Clients and apps**

| Project | What it is | How it uses sing-box-lx |
|---|---|---|
| [KeenProxy](https://github.com/ordo-systems/keen-proxy) | selective routing with a web panel for Keenetic / Netcraze (Entware) and OpenWrt routers | embeds the core as a Go module: `replace github.com/sagernet/sing-box => github.com/Leadaxe/sing-box-lx` pinned to a release tag, with the sing-tun, gVisor and uTLS forks |
| [Tunor](https://github.com/Vexorter42/Tunor) | GUI for Windows, macOS, Linux and Android with AmneziaWG 1.0–3.x | downloads the release binaries and the Android `libbox` from this repository's releases |
| [NyameBox](https://github.com/qr243vbi/nekobox) (the original NekoBox, rebranded) | cross-platform Qt desktop client | its core, [qr243vbi/sing-box](https://github.com/qr243vbi/sing-box), is a fork of sing-box-lx prepared for NekoBox 6; the TrustTunnel outbound here started from that project's request |
| [Hiddify](https://github.com/hiddify/hiddify-app) | multi-platform client with its own sing-box fork | the [Hiddify core documentation](https://github.com/hiddify/hiddify.com/blob/main/docs/core/features/index.md) names sing-box-lx as the source of its URL test additions, REALITY `key_share`, MASQUE failover and DNS group modes, and [utls-lx](https://github.com/Leadaxe/utls-lx) as the source of its browser TLS fingerprints |
| [VPN Direct](https://github.com/TT450/vpn-direct-app) | native Apple client (iOS / macOS / tvOS) | its core is a thin fork that starts from sing-box-lx (XHTTP, AWG, MASQUE, VLESS encryption, idle suspend) |
| [CELERITY panel](https://github.com/ClickDevTech/CELERITY-panel) | self-hosted panel for Hysteria 2 and Xray VLESS servers | its node probes install and run sing-box-lx built with `with_xhttp`, the same core its clients run |

**Router scripts**

| Project | What it is | How it uses sing-box-lx |
|---|---|---|
| [zirgozar](https://github.com/dreamboxone/zirgozar) | VPN center for OpenWrt | installs sing-box-lx as one of its cores; AmneziaWG and naive nodes run on it |
| [sing-box4asus](https://github.com/Dr4tez/sing-box4asus) | sing-box script for Asus routers on Merlin firmware | accepts sing-box-lx as a selectable core |
| [SKeen](https://github.com/jinndi/SKeen) | transparent proxy for Keenetic / Netcraze routers | its XHTTP example is built on sing-box-lx as the core |

**Tools and compatible implementations**

| Project | What it is | Relation to sing-box-lx |
|---|---|---|
| [tachyon-core](https://github.com/Dushnilin/tachyon-core) | proxy core in Rust for OpenWrt routers | declares sing-box-lx config compatibility and benchmarks against a pinned sing-box-lx release |
| [cfwarp-converter](https://github.com/juerson/cfwarp-converter) | converts Cloudflare WARP WireGuard and MASQUE profiles for third-party cores | has a dedicated sing-box-lx output |
| [F-Droid](https://github.com/f-droid/fdroiddata) | app catalogue | builds LxBox from source with sing-box-lx as a source library |

Shipping this core and not listed? Open an issue with a link to the place where the dependency is pinned.
