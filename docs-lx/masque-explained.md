# MASQUE: how it works and how sing-box-lx supports it

> 🌐 Русская версия: **[masque-explained.ru.md](masque-explained.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md): what the fork has and how to enable it;
> [protocols-transports](protocols-transports.md): every field, type, default, error text;
> the explained documents: how it works, why, how the fork does it and how it differs from vanilla.

MASQUE in its CONNECT-IP form ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)) carries whole IP packets inside an HTTP connection.
Cloudflare WARP works this way, and the `masque` outbound of sing-box-lx connects to it and to
other CONNECT-IP servers. This document explains what happens on the wire, why the defaults are
what they are and how a node behaves behind a relay, on a poor network and after idling.

The node keys and config examples are in [§7](#7-keys-and-examples). Load-time error texts and the
migration table from the old config shape are in the reference
[protocols-transports §3](protocols-transports.md#3-masque-outbound-connect-ip--warp).

## Table of contents

- [1. CONNECT-IP: IP packets inside HTTP](#1-connect-ip-ip-packets-inside-http)
  - [1.1 One request brings the tunnel up](#11-one-request-brings-the-tunnel-up)
  - [1.2 CONNECT-IP, not CONNECT-UDP](#12-connect-ip-not-connect-udp)
  - [1.3 The core does not register the device](#13-the-core-does-not-register-the-device)
  - [1.4 Two profiles: WARP and an RFC server](#14-two-profiles-warp-and-an-rfc-server)
- [2. What an intermediary sees](#2-what-an-intermediary-sees)
  - [2.1 The SNI is a neutral popular host](#21-the-sni-is-a-neutral-popular-host)
  - [2.2 A sniffing relay sends the tunnel to someone else's host](#22-a-sniffing-relay-sends-the-tunnel-to-someone-elses-host)
- [3. HTTP/3 or HTTP/2](#3-http3-or-http2)
  - [3.1 Why `vhttp: auto`](#31-why-vhttp-auto)
  - [3.2 The node remembers what worked](#32-the-node-remembers-what-worked)
  - [3.3 When to set the version explicitly](#33-when-to-set-the-version-explicitly)
- [4. Packet size](#4-packet-size)
  - [4.1 WARP drops inner IPv6 fragments](#41-warp-drops-inner-ipv6-fragments)
  - [4.2 The outer QUIC starts with an `mtu + 51` packet](#42-the-outer-quic-starts-with-an-mtu--51-packet)
  - [4.3 The tunnel does not break over a stream `detour`](#43-the-tunnel-does-not-break-over-a-stream-detour)
  - [4.4 UDP fragmentation is allowed](#44-udp-fragmentation-is-allowed)
- [5. Idling and waking](#5-idling-and-waking)
  - [5.1 `idle_timeout` puts the tunnel to sleep](#51-idle_timeout-puts-the-tunnel-to-sleep)
  - [5.2 keepalive keeps the tunnel alive](#52-keepalive-keeps-the-tunnel-alive)
  - [5.3 The tunnel follows the group chosen in `detour`](#53-the-tunnel-follows-the-group-chosen-in-detour)
- [6. MASQUE in upstream 1.15](#6-masque-in-upstream-115)
- [7. Keys and examples](#7-keys-and-examples)
  - [7.1 Node keys](#71-node-keys)
  - [7.2 WARP with the defaults](#72-warp-with-the-defaults)
  - [7.3 HTTP/2 only, and sleep on a phone](#73-http2-only-and-sleep-on-a-phone)
  - [7.4 WARP through a relay](#74-warp-through-a-relay)
  - [7.5 A QUIC proxy over WARP](#75-a-quic-proxy-over-warp)
  - [7.6 Your own RFC 9484 server](#76-your-own-rfc-9484-server)
- [8. Verification](#8-verification)
- [9. Typical failures](#9-typical-failures)

---

## 1. CONNECT-IP: IP packets inside HTTP

### 1.1 One request brings the tunnel up

The client opens an HTTP/3 connection over QUIC or an HTTP/2 connection over TLS to port 443. In
it, it sends an Extended CONNECT: with `:protocol` `cf-connect-ip` for WARP, with `connect-ip` for
RFC servers. The server answers, and the tunnel is ready. From then on every IP packet of the
client goes inside this connection: as a datagram over HTTP/3, as a separate DATA frame over
HTTP/2.

On the client side the core builds its own userspace stack on the tunnel, as for a WireGuard
endpoint. An application opens an ordinary TCP or UDP connection, the stack turns it into IP
packets, and the packets go into the tunnel. So both TCP and UDP go through `masque`, and the
outbound resolves domains beforehand through the DNS router: the stack works with IP addresses
and knows no names. Without a top-level `dns` block, domain traffic does not go through the
tunnel.

### 1.2 CONNECT-IP, not CONNECT-UDP

MASQUE also has CONNECT-UDP ([RFC 9298](https://www.rfc-editor.org/rfc/rfc9298)), which carries UDP datagrams to a single destination. WARP
works with whole IP packets, so the fork implements only CONNECT-IP. With the AmneziaWG
`id`/`ip`/`ib` fields ("masquerade sugar") MASQUE shares only the word.

### 1.3 The core does not register the device

To connect to WARP, a device registers with Cloudflare and gets a key pair and addresses inside
the WARP network. The application does this — LxBox, the launcher — and the core takes the result
ready from the config: `private_key`, `public_key`, `ip`, `ipv6`. `ip` and `ipv6` are the client's
addresses inside the tunnel, and they do not change. The exit address on the internet is
different and changes with every new connection: WARP hands out the address of whichever node of
its anycast network the client reached.

### 1.4 Two profiles: WARP and an RFC server

`profile: cloudflare` (the default) fits WARP: the `cf-connect-ip` token, the server is checked
against the pinned `public_key` rather than a certificate chain, and the request address is
preset. `profile: standard` is for your own [RFC 9484](https://www.rfc-editor.org/rfc/rfc9484) server: ordinary certificate checking, a
required `uri` with the template the server publishes, and HTTP/3 only. `standard` does not
connect to WARP.

## 2. What an intermediary sees

### 2.1 The SNI is a neutral popular host

From the outside the tunnel looks like TLS or QUIC to Cloudflare. The name in the ClientHello is
`www.cloudflare.com` by default, not the MASQUE endpoint's name. By the endpoint's name
(`consumer-masque.cloudflareclient.com`) such a connection is easy to tell apart and cut off: in
measurements with it, CONNECT-IP silently timed out on some uplinks, while with
`www.cloudflare.com`, `www.google.com` and other neutral names the same endpoint connected.

Swapping the name is safe because the server is checked against the pinned `public_key`. That is
why `tls.insecure: true` removes the only protection and is for debugging only. Some endpoints
present their own certificate only to a ClientHello without a name; `tls.disable_sni: true` is
for them.

### 2.2 A sniffing relay sends the tunnel to someone else's host

If `masque` goes through a `detour` to an Xray relay with sniffing (`destOverride` without
`routeOnly`), the relay reads the name from the first packet and sends the connection to that
name. For WARP that is `www.cloudflare.com` or another neutral name — someone else's host. The
tunnel breaks or fails with "different public key".

The fix on the relay is `routeOnly: true`. On the client side,
[`lx.sniff_guard`](lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni) hides the first
packet from the sniffer: over HTTP/2 it splits the ClientHello, over HTTP/3 it puts a decoy byte
ahead of the QUIC Initial.

## 3. HTTP/3 or HTTP/2

### 3.1 Why `vhttp: auto`

HTTP/3 is faster and is tried first by default. But on some paths QUIC does not get through, and
there is no failure — only silence:

- the endpoint silently ignores QUIC from a particular exit address;
- the `detour` has a hop that carries only TCP: HTTP CONNECT, a VLESS or Trojan position below it in a chain;
- the network filters incoming UDP:443.

With `vhttp: h3` such a node hangs on every connection until the timeout. `auto` waits 3 s for the
QUIC handshake and brings the tunnel up over HTTP/2 on TCP:443. The `standard` profile has no
HTTP/2, so for it `auto` means HTTP/3.

### 3.2 The node remembers what worked

An HTTP/3 win is remembered. An HTTP/2 win is remembered for a window that grows with every next
win: 1 s → 10 s → 30 s → 5 min → 10 min. When the window runs out, HTTP/3 is tried first again;
its win resets the window to the first step. One random HTTP/3 failure costs one tunnel on
HTTP/2, and a path where QUIC is always closed converges to one HTTP/3 try every 10 minutes. If the
remembered version stops coming up, the memory is dropped, and the other one is tried in the same
attempt.

### 3.3 When to set the version explicitly

- The path is known to be clean — `vhttp: h3`: on such a path it is the fastest option.
- The network filters UDP:443 or the node is always behind a TCP-only hop — `vhttp: h2`: verified
  on a device.
- HTTP/2 carries one IP packet per DATA frame, so `mtu` can go up to 16000 on it.

The first HTTP/3 connection is slow: the QUIC handshake, Extended CONNECT, the route announcement
and building the stack. A short urltest timeout can mark a fresh node `-1` on its first probe,
although it works afterwards.

## 4. Packet size

### 4.1 WARP drops inner IPv6 fragments

The tunnel splits an inner packet larger than `mtu` into fragments. WARP passes IPv4 fragments
and drops IPv6 fragments. So a node above `masque` — through a `detour`, in a group or on a `chain`
position — whose packet does not fit `mtu` is silent over IPv6 and works over IPv4. The typical case
is a QUIC proxy (Hysteria2, TUIC) with a 1280-byte packet: over IPv6 that is 1328 bytes.
Measured on a clean link on 2026-10-09: with `mtu` 1280 at most 1232 bytes of UDP payload pass
over IPv6, with `mtu` 1340 at most 1280.

A node above `masque` gets its packet size fitted to `mtu` on its own:
[`lx.detour.mtu_align`](lx-block.md#lxdetourmtu_align--path-mtu--packet-size-alignment) does it. By hand —
`initial_packet_size: 1232` on the QUIC node or `mtu: 1340` on `masque`; the latter needs an
outer path of about 1440 bytes to WARP.

### 4.2 The outer QUIC starts with an `mtu + 51` packet

The connection to the WARP server runs over QUIC itself and searches for an allowed packet size
itself. The core starts right away with `mtu + 51` bytes (within 1200–1452; 1331 at `mtu` 1280),
so an inner packet of `mtu` bytes fits in one datagram from the first packet, not after the
first probe.

### 4.3 The tunnel does not break over a stream `detour`

WARP sends probe packets of 1453–1462 bytes into every HTTP/3 session, more than the client
announces. An ordinary UDP socket truncates such a packet, and QUIC discards it. Over a stream
`detour` — VLESS UDP over WebSocket, gRPC, XHTTP — the same packet gave a read error, and the
tunnel died 1–5 s after coming up. The core truncates such packets the way a socket does, so
HTTP/3 over these `detour`s works. The same holds for Hysteria2 and TUIC.

### 4.4 UDP fragmentation is allowed

An ordinary outbound marks a datagram "don't fragment". `masque` allows fragmentation by default:
the tunnel adds headers to each packet, and when nested in another tunnel the outer datagram
outgrows the MTU. With the flag set it would vanish silently. An explicit `udp_fragment: false`
sets the flag again.

## 5. Idling and waking

### 5.1 `idle_timeout` puts the tunnel to sleep

After `idle_timeout` without traffic the tunnel closes: the stack, the internal workers and the
keepalive are freed. The next connection brings it up again — a full QUIC handshake, CONNECT-IP
and a new stack on the first request. Sleep saves about 6 MB of memory and one keepalive packet
every 30 s. On a phone that pays off; on a router or a desktop waking costs more, so there is no
sleep by default. One window for every node is set by
`lx.masque.idle_timeout` (§7.3); the node's own key wins, and
`"0"` on a node keeps its tunnel up.

### 5.2 keepalive keeps the tunnel alive

While the tunnel is awake, a QUIC keepalive every `keep_alive_period` (30 s, HTTP/3 only) keeps
it alive: otherwise the server and the ISP's NAT drop a quiet connection, and the next connection
silently pays for a rebuild. Turning keepalive off makes sense only if `idle_timeout` is shorter
than the keepalive would ever be needed.

### 5.3 The tunnel follows the group chosen in `detour`

If the `detour` of `masque` points at a selector or urltest, the connection to the WARP server
goes through the node the group has chosen. After the choice changes, the tunnel moves to the new
node, even without `interrupt_exist_connections`.

## 6. MASQUE in upstream 1.15

Upstream sing-box 1.15 (still in alpha) added its own MASQUE: the `masque-client` and
`masque-server` endpoints per [RFC 9484](https://www.rfc-editor.org/rfc/rfc9484). Its client does not work with WARP without changes: it
uses a different token (`connect-ip` instead of `cf-connect-ip`), it expects the server to assign
an address while WARP addresses are static from registration, and it has no key pinning and no
neutral SNI by default.

In the fork's stable line WARP connects through the `masque` outbound described here. On the 1.15
line the fork moves the WARP profile onto the upstream transport; that line is not released.

## 7. Keys and examples

### 7.1 Node keys

| Key | Required | Default | What it sets |
|-----|----------|---------|--------------|
| `type` | yes | — | `"masque"` |
| `tag` | yes | — | the node name for routes and groups |
| `server`, `server_port` | yes | — | the endpoint address and port, usually `443` |
| `private_key`, `public_key` | for `cloudflare` | — | keys from the WARP registration, base64 of DER (§1.3) |
| `ip`, `ipv6` | at least one | — | the client's addresses inside the tunnel; an address without a mask becomes `/32` and `/128` |
| `profile` | — | `cloudflare` | `cloudflare` — WARP, `standard` — your own [RFC 9484](https://www.rfc-editor.org/rfc/rfc9484) server (§1.4) |
| `vhttp` | — | `auto` | `auto`, `h3` or `h2` (§3) |
| `uri` | for `standard` | `https://cloudflareaccess.com` for `cloudflare` | the CONNECT-IP request address; for `standard`, the template the server publishes |
| `tls` | — | `server_name`: `www.cloudflare.com` for `cloudflare`, `server` for `standard` | the ordinary TLS block: `server_name`, `disable_sni`, `insecure`, `fragment`, `record_fragment` (§2.1) |
| `mtu` | — | `1280` | the inner packet size; up to `16000` on `h2` (§4) |
| `idle_timeout` | — | off, or `lx.masque.idle_timeout` | idle time after which the tunnel sleeps; `"0"` keeps it up (§5.1) |
| `keep_alive_period` | — | `30s` | QUIC keepalive, `h3` only; a negative value turns it off (§5.2) |
| `network_list` | — | `tcp`, `udp` | which protocols go into the tunnel |
| `detour` | — | — | which node or group to go through to the endpoint (§2.2, §5.3) |
| `udp_fragment` | — | fragmentation allowed | `false` forbids fragmenting outer datagrams (§4.4) |

The other dial fields (`bind_interface`, `routing_mark`, `connect_timeout`, `domain_strategy`
and so on) work as on any outbound.

Outside the node:

- a top-level `dns` block is required (§1.1);
- `lx.masque.idle_timeout` is one sleep window for every `masque` node without its own
  `idle_timeout`.

### 7.2 WARP with the defaults

```jsonc
{
  "dns": {
    "servers": [{ "type": "local", "tag": "local" }]
  },
  "outbounds": [
    {
      "type": "masque",
      "tag": "warp",
      "server": "162.159.198.2",
      "server_port": 443,
      "private_key": "<base64 DER EC private key>",
      "public_key":  "<base64 DER PKIX public key>",
      "ip":   "172.16.0.2/32",
      "ipv6": "2606:4700:110:...::/128"
    }
  ],
  "route": { "final": "warp" }
}
```

Everything else is default: `profile: cloudflare`, `vhttp: auto`, SNI `www.cloudflare.com`, `mtu`
1280, no sleep, a keepalive every 30 s.

### 7.3 HTTP/2 only, and sleep on a phone

```jsonc
{
  "type": "masque",
  "tag": "warp-h2",
  "server": "162.159.198.2",
  "server_port": 443,
  "vhttp": "h2",            // the network filters UDP:443
  "idle_timeout": "5m",     // the tunnel sleeps after 5 minutes of silence
  "private_key": "<…>", "public_key": "<…>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

One sleep window for every `masque` node instead of a key on each:

```jsonc
"lx": { "masque": { "idle_timeout": "5m" } }
```

### 7.4 WARP through a relay

```jsonc
{
  "outbounds": [
    { "type": "vless", "tag": "relay", /* … */ },
    {
      "type": "masque",
      "tag": "warp",
      "detour": "relay",
      "server": "162.159.198.2",
      "server_port": 443,
      "private_key": "<…>", "public_key": "<…>",
      "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
    }
  ],
  "lx": {
    "sniff_guard": {
      "tls":  { "method": "record_fragment" },   // h2: the ClientHello is split
      "quic": { "method": "decoy_first_byte" }   // h3: a decoy byte ahead of the QUIC Initial
    }
  }
}
```

`lx.sniff_guard` is needed if the relay sniffs the SNI (§2.2). If the relay carries only TCP,
`auto` moves to HTTP/2 on its own (§3.1).

### 7.5 A QUIC proxy over WARP

```jsonc
{ "type": "hysteria2", "tag": "hy2", "server": "2001:db8::1", "detour": "warp", /* … */ }
```

The core fits the packet size of `hy2` to the `mtu` of `warp` on its own (§4.1). If alignment is
turned off (`"lx": { "detour": { "mtu_align": "off" } }`), set it by hand: `"initial_packet_size": 1232`.

### 7.6 Your own RFC 9484 server

```jsonc
{
  "type": "masque",
  "tag": "my-masque",
  "profile": "standard",
  "server": "masque.example.com",
  "server_port": 443,
  "uri": "https://masque.example.com/.well-known/masque/ip/*/*/",
  "ip": "10.0.0.2/32"
}
```

`standard` has no HTTP/2 and no key pinning: the server is checked by the ordinary certificate
chain for `server`.

## 8. Verification

The outbound is verified on a device on Wi-Fi and LTE: traffic goes over HTTP/3 and HTTP/2, the
tunnel sleeps and comes back up, and it recovers after a drop. The `cloudflare` profile can be
checked only against real WARP: a synthetic stand does not reproduce its behaviour.

## 9. Typical failures

**Domains through the tunnel do not open, the log says `masque: no DNS router available`.** The
config has no top-level `dns` block. Add one: the outbound resolves domains before sending into
the tunnel.

**Every connection hangs until the timeout with `vhttp: h3`.** The `detour` has a hop that
carries only TCP, or the network filters UDP:443. Remove `vhttp` (it becomes `auto`) or set `h2`.

**Through a relay the tunnel breaks or reports "different public key".** The relay sniffs the SNI
and sends the connection to the neutral name. On the relay — `routeOnly: true`; on the client —
`lx.sniff_guard` (§2.2).

**A QUIC node above `masque` works over IPv4 and is silent over IPv6.** Its packet does not fit
`mtu`, and WARP drops the fragments. Check that `lx.detour.mtu_align` is not turned off; by hand —
`initial_packet_size: 1232` on the node (§4.1).

**A fresh node shows `-1` on its first probe and works afterwards.** The first HTTP/3 connection
is slow (§3.3). This is not a failure; if it gets in the way, raise the probe timeout.

**The exit address changed after idling.** This is how WARP anycast works: a new connection, a
new node of the network. The addresses inside the tunnel (`ip`, `ipv6`) stay the same.
