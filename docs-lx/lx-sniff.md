# sing-box-lx — protocol sniffers for LAN traffic

> RU: [lx-sniff.ru.md](lx-sniff.ru.md) · Feature: [SNIFF](../SPECS/FEATURES/016-SNIFF/FEATURE.md)

The core sniffs the first packet of every connection that enters through a `tun`
(or any inbound with a `sniff` rule action) and labels it with a protocol name that
routing rules can match. Upstream sing-box recognises web and torrent protocols
(`http`, `tls`, `quic`, `stun`, `dns`, `bittorrent`, `dtls`, `ssh`, `rdp`, `ntp`).
The fork adds five sniffers for what a home router sees from devices on the LAN:
VPN tunnels and voice calls.

## Table of contents

- [The fork recognises five protocols](#the-fork-recognises-five-protocols)
- [Fork sniffers run before bittorrent](#fork-sniffers-run-before-bittorrent)
- [A sniffer sees only the first packet of a flow](#a-sniffer-sees-only-the-first-packet-of-a-flow)
- [Router example](#router-example)
- [Obfuscated transports and RTP are not recognised](#obfuscated-transports-and-rtp-are-not-recognised)
- [Diagnostics](#diagnostics)

---

## The fork recognises five protocols

| Name | Network | Sets `domain` | First client packet that is matched |
|------|---------|---------------|-------------------------------------|
| `wireguard` | UDP | — | handshake initiation / response / cookie by exact size (148 / 92 / 64 B); transport data `>= 32 B` with `(len-32) % 16 == 0`. Reserved bytes are not checked, so Cloudflare WARP matches too |
| `openvpn` | UDP, TCP | — | `P_CONTROL_HARD_RESET_CLIENT_V2/V3`, key id 0: plain (14 B), tls-auth (HMAC of a known digest length, replay id 1, plausible net-time), tls-crypt / tls-crypt-v2 (replay id 1, net-time, 32-byte HMAC). TCP: the same frame behind a 2-byte length |
| `ike` | UDP | — | ISAKMP header: initiator SPI set, responder SPI zero, message id 0, length equal to the datagram; IKEv2 `IKE_SA_INIT` with the Initiator flag, or IKEv1 Main / Aggressive mode. On UDP 4500 the non-ESP marker is skipped |
| `tailscale` | UDP | — | disco ping: magic `TS💬`, 32-byte disco key, 24-byte nonce, box. The tunnel itself is WireGuard and shows up as `wireguard` |
| `sip` | UDP, TCP | host of the Request-URI | request-line `METHOD sip:… SIP/2.0` (INVITE, REGISTER, OPTIONS, …). `sip:user@host` → `domain: host`; IP literals and `tel:` URIs set no domain |

All names work in both places: the `sniffer` list of a `sniff` rule action, and the
`protocol` matcher of a routing rule. Per-sniffer details are in the
[SNIFF feature](../SPECS/FEATURES/016-SNIFF/FEATURE.md).

## Fork sniffers run before bittorrent

Packet (UDP) sniffers, in order: `dns`, `quic`, `stun`, `wireguard`, `openvpn`, `ike`,
`tailscale`, `sip`, `bittorrent` (uTP, tracker), `dtls`, `ntp`.
Stream (TCP): `tls`, `http`, `dns`, `bittorrent`, `ssh`, `rdp`, `sip`, `openvpn`.

In the UDP list the fork's sniffers run before `bittorrent` because upstream's uTP
check accepts any packet of 20+ bytes whose first bytes are `01 00`. A WireGuard
handshake initiation starts with the same bytes. If `bittorrent` ran first, plain
WireGuard from a LAN device would be routed by the torrent rule.

No upstream sniffer file is modified: every fork sniffer is a separate `*_lx.go`
with its own, stricter check of the packet shape.

## A sniffer sees only the first packet of a flow

The router looks at one datagram per flow. It reads further only to reassemble a
fragmented QUIC ClientHello. What follows from that:

- **AmneziaWG with junk (`jc > 0`)** — the first datagram is random junk, nothing
  matches, the flow stays unnamed. The handshake that follows is never inspected.
- **AmneziaWG with a decoy (`ip=quic` / `dns` / `stun` / `sip`)** — the first packet
  *is* a valid QUIC Initial / DNS query / STUN request / SIP INVITE, so the flow is
  labelled `quic` (with the SNI from `id`), `dns`, `stun` or `sip`. That is the
  point of the decoy, and it hides the tunnel from your own router as well: a
  `.ru` SNI will hit a "Russian domains go direct" rule.
- **Non-default `h1`–`h4` or `s1`–`s4`** — the WireGuard headers are changed, there
  is no recognisable shape, and the flow is not recognised.
- **A flow that started before the core restarted** is seen from its middle: WireGuard
  transport packets are recognised by the type 4 rule, other protocols are not.

For obfuscated tunnels route by destination instead (`ip_cidr` of your VPN servers).

## Router example

The example sends every tunnel from the LAN through the WARP outbound, and calls and
torrents direct. The VPN rule goes first.

```jsonc
{
  "route": {
    "rules": [
      { "inbound": ["tun-in"], "action": "sniff", "timeout": "1s" },
      { "protocol": ["wireguard", "openvpn", "ike", "tailscale"], "outbound": "🔥🎭 WARP (MASQUE)" },
      { "protocol": "sip", "outbound": "direct-out" },
      { "protocol": "bittorrent", "outbound": "direct-out" }
    ]
  }
}
```

A narrower `sniff` action, when you only care about tunnels:

```jsonc
{ "action": "sniff", "sniffer": ["wireguard", "openvpn", "ike", "tailscale"] }
```

Because `sip` sets `domain`, domain rules apply to calls the same way they apply to
TLS: `{ "protocol": "sip", "domain_suffix": "sip.example.com", "outbound": "…" }`.

## Obfuscated transports and RTP are not recognised

| Protocol | Why it is not recognised |
|---|---|
| RTP / RTCP | the shape is too weak, too many false positives |
| L2TP / PPTP | obsolete |
| KCP | the header has no distinguishing features |
| VNC | the server speaks first, the client sends nothing to sniff |
| Shadowsocks, VMess, MTProto, Hysteria/salamander, AmneziaWG with changed headers | the transport is obfuscated; if a fork sniffer could recognise it, so could the ISP's DPI |

## Diagnostics

With `log.level: debug` each sniffed flow logs `router: sniffed packet protocol: <name>`
(plus `domain: …` for `sip`), followed by the rule it matched. A flow with no
`sniffed` line was not recognised by any sniffer; the common reasons are in
[A sniffer sees only the first packet of a flow](#a-sniffer-sees-only-the-first-packet-of-a-flow).
