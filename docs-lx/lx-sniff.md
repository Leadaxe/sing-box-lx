# sing-box-lx — protocol sniffers for LAN traffic (SPEC 078 / 079)

> RU: [lx-sniff.ru.md](lx-sniff.ru.md) · Feature: [SNIFF](../SPECS/FEATURES/016-SNIFF/FEATURE.md)

The core sniffs the first packet of every connection that enters through a `tun`
(or any inbound with a `sniff` rule action) and labels it with a protocol name that
routing rules can match. Upstream sing-box recognises web and torrent protocols
(`http`, `tls`, `quic`, `stun`, `dns`, `bittorrent`, `dtls`, `ssh`, `rdp`, `ntp`).
The fork adds the protocols a home router actually sees from other devices on the
LAN: VPN tunnels and voice calls.

## What the fork recognises

| Name | Network | Sets `domain` | First client packet that is matched | Task |
|------|---------|---------------|-------------------------------------|------|
| `wireguard` | UDP | — | handshake initiation / response / cookie by exact size (148 / 92 / 64 B); transport data `>= 32 B` with `(len-32) % 16 == 0`. Reserved bytes are not checked, so Cloudflare WARP matches too | [078](../SPECS/TASKS/078-WIREGUARD_PACKET_SNIFFER/SPEC.md) |
| `openvpn` | UDP, TCP | — | `P_CONTROL_HARD_RESET_CLIENT_V2/V3`, key id 0: plain (14 B), tls-auth (HMAC of a known digest length, replay id 1, plausible net-time), tls-crypt / tls-crypt-v2 (replay id 1, net-time, 32-byte HMAC). TCP: the same frame behind a 2-byte length | [079](../SPECS/TASKS/079-VPN_VOIP_PACKET_SNIFFERS/SPEC.md) |
| `ike` | UDP | — | ISAKMP header: initiator SPI set, responder SPI zero, message id 0, length equal to the datagram; IKEv2 `IKE_SA_INIT` with the Initiator flag, or IKEv1 Main / Aggressive mode. On UDP 4500 the non-ESP marker is skipped | 079 |
| `tailscale` | UDP | — | disco ping: magic `TS💬`, 32-byte disco key, 24-byte nonce, box. The tunnel itself is WireGuard and shows up as `wireguard` | 079 |
| `sip` | UDP, TCP | host of the Request-URI | request-line `METHOD sip:… SIP/2.0` (INVITE, REGISTER, OPTIONS, …). `sip:user@host` → `domain: host`; IP literals and `tel:` URIs set no domain | 079 |

All names work in both places: the `sniffer` list of a `sniff` rule action, and the
`protocol` matcher of a routing rule.

## Default order

Packet (UDP) sniffers, in order: `dns`, `quic`, `stun`, **`wireguard`, `openvpn`, `ike`,
`tailscale`, `sip`**, `bittorrent` (uTP, tracker), `dtls`, `ntp`.
Stream (TCP): `tls`, `http`, `dns`, `bittorrent`, `ssh`, `rdp`, **`sip`, `openvpn`**.

The fork's sniffers sit **before** uTP on purpose. Upstream's uTP check accepts any
packet of 20+ bytes whose first two bytes are `01 00` — which is exactly how a
WireGuard handshake initiation starts — so plain WireGuard from a LAN device used to
be labelled `bittorrent` and routed by the torrent rule. No upstream sniffer file is
modified; every fork sniffer is a separate `*_lx.go` with a stricter shape.

## The one thing to know: only the first packet counts

The router looks at **one** datagram per flow (it reads more only to reassemble a
fragmented QUIC ClientHello). Consequences:

- **AmneziaWG with junk (`jc > 0`)** — the first datagram is random junk, nothing
  matches, the flow stays unnamed. The handshake that follows is never inspected.
- **AmneziaWG with a decoy (`ip=quic` / `dns` / `stun` / `sip`)** — the first packet
  *is* a valid QUIC Initial / DNS query / STUN request / SIP INVITE, so the flow is
  labelled `quic` (with the SNI from `id`), `dns`, `stun` or `sip`. That is the
  point of the decoy, and it hides the tunnel from your own router as well: a
  `.ru` SNI will hit a "Russian domains go direct" rule.
- **Non-default `h1`–`h4` or `s1`–`s4`** — the shape is gone by design; not recognised.
- A flow that started **before the core restarted** is seen from its middle: WireGuard
  transport packets are still recognised (type 4 rule), the others are not.

For obfuscated tunnels route by destination instead (`ip_cidr` of your VPN servers).

## Router example

Send every tunnel from the LAN through the WARP outbound, keep calls direct, and
stop the torrent rule from swallowing VPNs — order matters, the VPN rule goes first:

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

## Deliberately not recognised

RTP/RTCP (shape too weak, false positives), L2TP / PPTP (obsolete), KCP (generic
header), VNC (server speaks first), and every obfuscated transport — Shadowsocks,
VMess, MTProto, Hysteria/salamander, AmneziaWG with changed headers. Recognising
those would be the DPI's job, and anything the fork could find, an ISP could too.

## Diagnostics

With `log.level: debug` each sniffed flow logs `router: sniffed packet protocol: <name>`
(plus `domain: …` for `sip`), followed by the rule it matched. A flow with no
`sniffed` line was not recognised by any sniffer — see "only the first packet counts".
