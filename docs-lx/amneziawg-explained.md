# AmneziaWG: how it works and how sing-box-lx supports it

> 🌐 Русская версия: **[amneziawg-explained.ru.md](amneziawg-explained.ru.md)**.

AmneziaWG (AWG) is WireGuard with a changed packet shape on the wire and
fully preserved cryptography. The protocol is defined by the Amnezia
project. The reference implementation is `amneziawg-go`, and the servers
behind `vpn://` and `.conf` exports are theirs too. Vanilla sing-box does
not support AWG and has declined to support it. `sing-box-lx` carries the
client half in a fork submodule of `wireguard-go`.

This document answers three questions. **How each AWG layer works**: why it
exists and what it does to the bytes. **How the fork implements support**:
where the code lives and which decisions were made. **How this differs from
vanilla sing-box** and from the reference implementation. Fields, defaults
and error texts are not listed here. For those, see
[protocols-transports §2](protocols-transports.md#2-amneziawg-203x-awg2-awg3)
(parameter reference) and [lx-config §2](lx-config.md#2-amneziawg-203x-awg2-awg3)
(overview). The current state of the area is the feature spec
[003-AWG](../SPECS/FEATURES/003-AWG/FEATURE.md).

Audience: a core or client developer who opens
`submodules/wireguard-go/device/` or `option/wireguard_awg.go` and wants to
understand what they are reading. WireGuard knowledge at the level of "there
is a handshake, there are transport packets" is assumed.

Callouts in the text are marked by type:

- ⚠️ pitfall: something that fails silently and does not explain itself;
- 🔀 differs from vanilla sing-box or from the reference implementation;
- 🧭 TL;DR: the section's conclusion in a few sentences;
- 📖 normative source.

## Table of contents

- [§0 The whole picture: three layers on top of WireGuard](#0-the-whole-picture-three-layers-on-top-of-wireguard)
- [§1 Why WireGuard is recognizable](#1-why-wireguard-is-recognizable)
- [§2 AWG 1.x: junk, padding, magic headers](#2-awg-1x-junk-padding-magic-headers)
  - [2.1 Junk packets before the handshake](#21-junk-packets-before-the-handshake)
  - [2.2 Padding S1–S4](#22-padding-s1s4)
  - [2.3 Magic headers H1–H4](#23-magic-headers-h1h4)
  - [2.4 Why the parameters are not independent](#24-why-the-parameters-are-not-independent)
- [§3 AWG 2.0: decoys and ranges](#3-awg-20-decoys-and-ranges)
  - [3.1 CPS: I1–I5 and the mini-language](#31-cps-i1i5-and-the-mini-language)
  - [3.2 Order on the wire](#32-order-on-the-wire)
  - [3.3 Masquerade sugar id / ip / ib](#33-masquerade-sugar-id--ip--ib)
  - [3.4 Why QUIC and not DNS or STUN](#34-why-quic-and-not-dns-or-stun)
- [§4 AWG 3.x: header cipher, content padding, timings](#4-awg-3x-header-cipher-content-padding-timings)
  - [4.1 Header protection](#41-header-protection)
  - [4.2 Content padding and random trailers](#42-content-padding-and-random-trailers)
  - [4.3 Timings and cookies](#43-timings-and-cookies)
  - [4.4 What must match the server](#44-what-must-match-the-server)
- [§5 MTU: where the bytes go](#5-mtu-where-the-bytes-go)
- [§6 How the fork does it](#6-how-the-fork-does-it)
  - [6.1 Two layers: graft and pass-through](#61-two-layers-graft-and-pass-through)
  - [6.2 Validation: the "value from the config" class](#62-validation-the-value-from-the-config-class)
  - [6.3 Receive: packet classification](#63-receive-packet-classification)
  - [6.4 All send paths](#64-all-send-paths)
- [§7 Example: awg.conf and sing-box-lx](#7-example-awgconf-and-sing-box-lx)
- [§8 Upstream position](#8-upstream-position)
- [§9 Popular misconceptions](#9-popular-misconceptions)
- [§10 Typical failures](#10-typical-failures)
- [§11 Glossary](#11-glossary)
- [See also](#see-also)

---

# 0. The whole picture: three layers on top of WireGuard

WireGuard is a fast and simple UDP tunnel with exemplary cryptography. Its
only weakness against a middlebox is **recognizability**. The protocol was
never designed to be covert, and a classifier identifies it by the very
first packet (§1). AmneziaWG adds obfuscation on top of it in three layers.
Each layer includes the previous one:

| Layer | What it adds | Keys |
|---|---|---|
| **AWG 1.x** | Junk packets before the handshake, padding before messages, substituted message types | `jc` `jmin` `jmax`, `s1` `s2`, single `h1`–`h4` |
| **AWG 2.0** | Decoy packets that imitate another protocol; padding of transport packets; message types as ranges | `i1`–`i5`, `s3` `s4`, `h1`–`h4` as `"min-max"`, sugar `id` `ip` `ib` |
| **AWG 3.x** | Cipher on the header of every packet, content padding, random trailers, randomized timings | `header_protection_key`, `content_padding_addition`, `random_trailers`, `disable_cookies`, ranged timings |

No layer touches the WireGuard cryptography (Noise IK, Curve25519,
ChaCha20-Poly1305). AWG does not make the tunnel "stronger". It makes it
**unlike WireGuard**.

The layer stack in the core, top to bottom:

```
application traffic
        │
        ▼
endpoint wireguard ── regular fields: keys, address, mtu, peers
   └── AWG fields at the endpoint root
        │
        ▼
wireguard-go fork device ── obfuscation on send, removal on receive
        │
        ▼
UDP → network  (or another outbound via detour)
```

> 🧭 **TL;DR:** AWG is WireGuard with no recognizable trait on the wire:
> no message type, no size, no rhythm. Inside it is the same WireGuard.
> Client and server must agree **byte for byte** on every obfuscation
> parameter: these are not negotiated, they are configured.

---

# 1. Why WireGuard is recognizable

A classifier does not need to break the cipher. The shape is enough:

- **The first byte** of every packet is the message type: `1` for handshake
  initiation, `2` for response, `3` for cookie reply, `4` for transport.
  Three zero bytes follow. Four words for the whole protocol.
- **Handshake sizes** are fixed: initiation is 148 bytes, response is 92,
  cookie is 64. A pair of UDP datagrams 148 → 92 to a new address is a
  signature by itself.
- **Transport packets** are aligned to 16 bytes, keepalive is exactly 32.
- **Rhythm**: a new handshake every 120 seconds, a retry after 5, keepalive
  on a timer.

All four traits sit in the cleartext part of the packet or in its
metadata. A classifier that knows them flags the flow from one packet,
without state. That is why AWG works on each of them separately: junk and
padding break the sizes, magic headers break the type, AWG3 breaks the
rhythm and the remaining low-entropy fields.

---

# 2. AWG 1.x: junk, padding, magic headers

## 2.1 Junk packets before the handshake

`jc` random datagrams of `jmin` to `jmax` bytes go to the server address
**before** the handshake initiation. Their content is random, and the
server drops them silently. The point is that the first packet of the flow
is not the 148-byte initiation, and a classifier that builds state from
the first N packets sees noise.

## 2.2 Padding S1–S4

Random bytes are inserted before a message: `s1` before initiation, `s2`
before response, `s3` before cookie reply, `s4` before **every transport
packet**. The receiver knows the padding size from its own config and
simply skips it. Message sizes stop being 148/92/64, and transport stops
being a multiple of 16.

`s4` stands apart: it is paid on every data packet and eats into the MTU
(§5). `s3` does not affect the MTU: cookie reply is a rare service message.

## 2.3 Magic headers H1–H4

The message type, a 4-byte word, is replaced by a value from the config:
`h1` instead of `1`, `h2` instead of `2`, and so on. The receiver compares
the first word after the padding with its own `h1`–`h4` and learns the
type that way. In AWG 2.0 each value can be a **range** `"min-max"`: a
random number from it is chosen for each message, and the receiver checks
that the value falls in the interval. The four intervals must not overlap,
otherwise the type cannot be recovered. The core rejects such a config at
load time.

## 2.4 Why the parameters are not independent

Padding and header are read from the same buffer: `s1` shifts the position
of the magic word in the packet. This gives a whole class of edge cases
caught in the fork. With small `s1`–`s4` (0–3 bytes) the magic word lands
on positions that other paths use for service bytes, and it is easy to
overwrite ([SPEC 026](../SPECS/TASKS/026-AWG_MAGIC_VS_RESERVED_CLEAR/SPEC.md)).
`s4` shifted the packet right in a buffer allocated without room for
padding, and the first data packet crashed the process
([SPEC 025](../SPECS/TASKS/025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md)).

> ⚠️ Obfuscation is symmetric: any parameter mismatch with the server gives
> the same symptom, "the node does not come up", with no diagnostics. A
> special case is an `s4` mismatch. The handshake **succeeds** (`s1`/`s2`
> matched), but data does not flow in either direction, because the
> receiver cuts transport packets by its own `s4`. It looks like a routing
> problem, not an obfuscation problem.

---

# 3. AWG 2.0: decoys and ranges

## 3.1 CPS: I1–I5 and the mini-language

Controlled Packet Sequence: up to five packets that the initiator sends
before the handshake in a fixed order. Unlike junk, their content is not
random. It is **described** by a mini-language string:

| Tag | Produces |
|---|---|
| `<b 0xHEX>` | static bytes |
| `<c>` | counter |
| `<t>` | timestamp |
| `<r N>` | N random bytes |
| `<rc N>` / `<rd N>` | N random characters / digits |

This is how a snapshot of a real protocol goes into `i1`: a STUN Binding
Request, a QUIC Initial, a DNS query. The first meaningful packet of the
flow then looks like someone else's legitimate traffic. The strings are
case-sensitive, and tag order matters. The server does not send them from
its config: it repeats what came from the client.

## 3.2 Order on the wire

From the graft code (`device/send.go`, `SendHandshakeInitiation`):

```
i1 → i2 → … → i5        decoys, in order, only those that are set
jc × [jmin..jmax]       junk datagrams
[s1 junk][h1][initiation]
        ◄── [s2 junk][h2][response]
[s4 junk][h4][transport]  …  then every data packet
```

Decoys go **before** junk, not after: the point of a decoy is to be the
first packet the classifier sees.

## 3.3 Masquerade sugar id / ip / ib

Writing a QUIC Initial by hand with `<b 0x…>` tags is impractical. And a
snapshot of one packet is the same for all users, so it becomes a
signature itself. So the fork, following WireSock Secure Connect, accepts a
declaration: `ip` is the decoy protocol (`quic`, `dns`, `stun`, `sip`),
`id` is the domain, `ib` is the client profile (`chrome`, `firefox`,
`curl`). The fork builds `i1` itself, with a random layout on each start
([SPEC 009](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md)).

The `quic` profile is not just a QUIC Initial. It is an Initial whose
ClientHello is **split into several CRYPTO frames and sent out of order**:
the first frame on the wire starts in the middle of the ClientHello. A
classifier that takes the first frame as the start parses garbage and lets
the packet through. A real QUIC server would reorder the frames. This is
the only profile proven against live DPI on a device.

> ⚠️ `id` **goes on the wire**: as SNI in QUIC, as QNAME in DNS, as host in
> SIP. The domain must be plausible and reachable on that network, not a VPN
> beacon. `id`/`ip`/`ib` are mutually exclusive with an explicit `i1`: a
> config with both is rejected.

## 3.4 Why QUIC and not DNS or STUN

All four profiles are valid packets of their protocols. But on a device in
a live network only `quic` passed. `dns`, `stun` and `sip` hit a timeout.
The cause is not packet quality but a **destination anomaly**. The
classifier expects DNS, STUN and SIP at their usual addresses, and a query
to a datacenter IP on a non-standard port is suspicious in itself. QUIC to
any address and port is normal, because that is how HTTP/3 behaves.

Hence the parameter reference recommends, for Cloudflare WARP: `ip=quic`,
`id=<popular domain>`, `ib=chrome`. The other profiles remain for
middleboxes that check only that the shape is correct.

---

# 4. AWG 3.x: header cipher, content padding, timings

AWG2 breaks sizes and types, but the packet header stays **low-entropy**:
after the padding there is a magic word from a narrow range, a receiver
index, and a counter that grows monotonically. A stateful classifier can
notice this. AWG 3.0 and 3.1 (amneziawg-go v3.0 and v3.1, summer 2026)
close the remainder
([SPEC 080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md)).

## 4.1 Header protection

`header_protection_key` is a 32-byte ChaCha20 key that encrypts the header
of every packet: type, receiver index, counter. The cipher nonce is taken
from the **first 12 bytes of the padding** of the same message. So each of
`s1`–`s4` must be at least 12, and a config with a key and short padding is
rejected. On the wire only random bytes remain, all the way to the AEAD.

## 4.2 Content padding and random trailers

- `content_padding_addition` adds zeros **inside** the AEAD of each data
  packet instead of aligning to 16. It is encrypted, so from the outside it
  cannot be told apart from data. Packet size stops correlating with
  payload size.
- `random_trailers` is a random tail of random length after handshake
  messages and, by the same rule, inside data packets.

Both additions are limited by the "UDP window": the largest datagram the
path has already carried. They do not change the MTU budget (§5), but they
do not remove the need to keep `mtu` at the server's value either.

## 4.3 Timings and cookies

The five WireGuard timings (`rekey_after_time` 120, `rekey_timeout` 5,
`reject_after_time` 180, `keepalive_timeout` 10, `max_handshake_attempts`
18) and the peer's `persistent_keepalive_interval` are set as ranges and
re-drawn each time the timer is armed. The rhythm stops being a constant.
`disable_cookies` removes the cookie exchange under load: it has a
recognizable shape, and the client does not need it.

## 4.4 What must match the server

AmneziaWG distinguishes **server** parameters (the value is the same on
both ends, otherwise there is no handshake) from **client** parameters
(local behavior, the server does not care). In AWG3 only
`header_protection_key` is a server parameter. The rest are client
parameters. Copying them from the server export is still worthwhile: they
are chosen together, and a meaningless pair such as `rekey_after_time`
above `reject_after_time` makes the tunnel flap.

> 🧭 **TL;DR:** AWG1 hides sizes and types, AWG2 adds a decoy and ranges,
> AWG3 encrypts everything that was still readable and randomizes timing.
> A byte-for-byte match with the server is required for `jc`/`jmin`/`jmax`,
> `s1`–`s4`, `h1`–`h4`, `i1`–`i5` and `header_protection_key`.

---

# 5. MTU: where the bytes go

This is the most common source of AWG failures, and it does not look like
an obfuscation failure.

**What happens.** `s4` adds junk before **every** transport packet. A data
packet on the wire = IP/UDP (28) + WireGuard (32) + `s4` + payload. If the
payload is sized by the usual WireGuard MTU, the packet becomes longer
than the path. The OS rejects the datagram, and the log shows:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

The handshake succeeded: its size does not depend on `mtu`. Data does not
flow. From the outside this is "VPN connected, websites do not open".

**The budget** against a 1500-byte path:

```
mtu ≤ 1500 − 28 (IP/UDP) − 32 (WireGuard) − s4
```

With `s4 = 60` this is 1380. Amnezia recommends **1280** for the client:
headroom for PPPoE, mobile networks and nested tunnels.

**What the core does by itself.**

- If `mtu` is not set and `s4` is, the default becomes **1280** instead of
  the usual WireGuard 1408.
- If `mtu` is set explicitly and does not fit the budget, the core logs a
  warning at startup. The check uses a conservative 1492 (PPPoE), so it
  can fire a few bytes below the Ethernet ceiling. The tunnel comes up
  anyway: the warning is advisory.
- The outer socket **does not force DF** ([SPEC 028](../SPECS/TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md)):
  an oversized datagram is IP-fragmented, not dropped. This is what lets
  nested tunnels work: AWG via `detour` over another WG or MASQUE, where
  the outer datagram is routinely oversized. An explicit
  `"udp_fragment": false` restores the old behavior. A correct `mtu` is
  still preferable: fragmentation is a safety net.
- In a [`chain`](../SPECS/FEATURES/015-CHAIN/FEATURE.md) the MTU of the
  tunnel links is lowered automatically.

`jmax` is also kept below the path MTU: a junk packet longer than the MTU
gets fragmented, and narrow paths then drop the fragments.

> 🔀 Vanilla sing-box has none of this, because it has no `s4`: the
> WireGuard endpoint MTU there defaults to 1408 and is not checked, and DF
> is forced on the outer socket. An AWG config moved mechanically into
> vanilla, even if vanilla accepted it, would hit `message too long` on the
> first data packet.

> 🧭 **TL;DR:** `s4` noise eats into the MTU on every data packet. The
> handshake passes, data does not. The core defaults `mtu` to 1280, warns
> when it is exceeded, and allows IP fragmentation for nested tunnels.

---

# 6. How the fork does it

## 6.1 Two layers: graft and pass-through

Obfuscation lives **not in sing-box** but in a fork of `wireguard-go`
([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx),
submodule `submodules/wireguard-go`, `replace` in `go.mod`). It is a
three-way graft: the `amneziawg-go` obfuscation is ported on top of
`sagernet/wireguard-go`, which sing-box is built on. The whole change is
confined to `device/`: ten new files (magic header generator, CPS chains,
junk, codecs) and six modified ones (AWG state in `device.go`, obfuscation
insertion in `send.go`, removal in `receive.go`, UAPI). `conn/` and `tun/`
are taken from upstream unchanged.

sing-box only **passes the parameters through**: fields at the endpoint
root (`option/wireguard_awg.go`) are read, validated and sent to the device
over UAPI, like regular WireGuard keys. The `transport/wireguard` ↔ device
contract does not change. A config without a single AWG field is a regular
WireGuard endpoint, **byte for byte as in upstream**. The build tag is
`with_awg`. Without it, an AWG field gives an explicit load error, not a
silent fallback to plain WireGuard that would cancel the obfuscation.

The fork submodule is part of the delta. When the reference implementation
moves, our patches are retired by bumping the submodule version, not by
merging the core
([005-UPSTREAM_SYNC](../SPECS/FEATURES/005-UPSTREAM_SYNC/FEATURE.md)).
The current port is amneziawg-go v3.1 (`b5928ef`, 2026-08-28),
byte-compatible in nonce layout, classification order and padding rules.

The graft invariant is `MessageEncapsulatingTransportSize = 8`, the
headroom before each datagram, as in upstream. It was once set to zero,
and Tailscale, which runs on the same fork, stopped coming up
([SPEC 112](../SPECS/TASKS/112-TAILSCALE_DIRECT_PATH_SEND_HEADROOM/SPEC.md)).

## 6.2 Validation: the "value from the config" class

The main class of AWG defects is a parameter that passes parsing but breaks
the runtime at a boundary: zero, maximum, an inverted range, padding
overlapping the header. The reference implementation validates such things
one field at a time. In the fork the rule is to **validate combinations**,
and at load time, not in a background goroutine:

- `jmin > jmax` is a config error, not a generator panic on the first
  handshake ([SPEC 008](../SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION/SPEC.md));
- CPS lengths without bounds: a negative one went out of slice range, a
  huge one caused OOM ([SPEC 025](../SPECS/TASKS/025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md));
- an `h` range spanning the full `uint32` width: generator overflow and
  panic;
- overlapping `h1`–`h4` ranges: `headers must not overlap`;
- `header_protection_key` with any `s1`–`s4` < 12: rejected with the field
  name, both at load and on a partial UAPI update when the key is already
  set and the padding is being narrowed;
- `id`/`ip`/`ib` together with `i1`, `id` without `ip`, `ib` with anything
  other than `quic`: rejected with a message. The `id` domain passes a
  strict LDH check, because it goes into SIP text and into the DNS QNAME.
  That is an injection boundary.

The verbatim texts are in the
[parameter reference §2.9](protocols-transports.md#29-validation-errors-verbatim).

## 6.3 Receive: packet classification

With `random_trailers` the receiver must try, as a handshake message, any
datagram **longer** than `s1`+148 / `s2`+92 / `s3`+64, based on the type
word. With single `h1`–`h4` a false match happens with probability 2⁻³².
With wide AWG2 ranges it is width/2³² for **every** data packet, which then
fails the MAC and is lost. The reference implementation lives with this.

The fork classifies differently
([SPEC 081](../SPECS/TASKS/081-AWG_RECEIVE_INDEX_FIRST_CLASSIFICATION/SPEC.md)):
a datagram whose transport type word is followed by one of **our live
receiver indexes** is treated as data before any handshake candidates.
Downlink does not lose packets this way. The wire format does not change,
so uplink depends on the server's receiver, and the advice stays: do not
combine `random_trailers` with wide `h1`–`h4` ranges.

## 6.4 All send paths

A data packet enters the device by more than one path: a read from TUN,
injection from the gVisor stack (`InputPacket`), a priority message,
keepalive. The reference implementation obfuscates the main path. In the
fork the AWG3 wrapper applies to all of them, including the first batch
after startup that was read before the config was applied. That batch is
re-laid out under the current padding instead of leaving in the old
layout.

> 🔀 Differences from the reference amneziawg-go that its client does not
> have: combination validation at load time (§6.2), classification by
> receiver index (§6.3), obfuscation on all send paths (§6.4), `id`/`ip`/`ib`
> sugar with a fragmented QUIC Initial (§3.3), MTU default and check (§5).
> Everything else is byte-for-byte parity: 16 AWG2 parameters and 9 AWG3
> keys.

---

# 7. Example: awg.conf and sing-box-lx

An Amnezia export (a `.conf` from the app, or `awg` → `last_config` →
`config` inside `vpn://`). On the left is an AWG 3.1 server as served by
the `amnezia-awg2` container with `protocol_version: "3.1"`. On the right is
the same node for our core. All keys are placeholders.

`awg.conf`:

```ini
[Interface]
PrivateKey = <client-private-key-base64>
Address = 10.8.1.7/32
MTU = 1376
Jc = 4
Jmin = 10
Jmax = 50
S1 = 55
S2 = 42
S3 = 40
S4 = 12
H1 = 1
H2 = 2
H3 = 3
H4 = 4
HeaderProtectionKey = <HeaderProtectionKey-base64>
ContentPaddingAddition = 10-100
RekeyAfterTime = 100-120
RekeyTimeout = 3-7
RejectAfterTime = 150-180
KeepaliveTimeout = 5-15
MaxHandshakeAttempts = 15-20
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = <server-public-key-base64>
PresharedKey = <preshared-key-base64>
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.30:30565
PersistentKeepalive = 25-35
```

sing-box-lx (`endpoints[]`):

```jsonc
{
  "type": "wireguard",
  "tag": "awg3-out",
  "system": false,
  "mtu": 1376,
  "address": ["10.8.1.7/32"],
  "private_key": "<client-private-key-base64>",

  "jc": 4, "jmin": 10, "jmax": 50,
  "s1": 55, "s2": 42, "s3": 40, "s4": 12,
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,

  "header_protection_key": "<HeaderProtectionKey-base64>",
  "content_padding_addition": "10-100",
  "rekey_after_time": "100-120",
  "rekey_timeout": "3-7",
  "reject_after_time": "150-180",
  "keepalive_timeout": "5-15",
  "max_handshake_attempts": "15-20",
  "random_trailers": true,
  "disable_cookies": true,

  "peers": [{
    "address": "203.0.113.30",
    "port": 30565,
    "public_key": "<server-public-key-base64>",
    "pre_shared_key": "<preshared-key-base64>",
    "allowed_ips": ["0.0.0.0/0", "::/0"],
    "persistent_keepalive_interval": "25-35"
  }]
}
```

The same server on AWG 2.0 would differ in ranged `H1`–`H4` of the form
`43613244-384550127` (a string in JSON, verbatim) and non-empty `I1`–`I5`.
There would be no AWG3 keys. A variant with masquerade sugar for
Cloudflare WARP is in the
[parameter reference §2.8](protocols-transports.md#28-examples).

Key mapping:

| `awg.conf` | sing-box-lx | Where it is described |
|---|---|---|
| `[Interface] PrivateKey`, `Address` | `private_key`, `address` | [`private_key`](../docs/configuration/endpoint/wireguard.md#private_key), [`address`](../docs/configuration/endpoint/wireguard.md#address) |
| `[Interface] MTU` | `mtu` | [`mtu`](../docs/configuration/endpoint/wireguard.md#mtu); budget: §5, [parameter reference §2.6](protocols-transports.md#26-mtu-budget) |
| `Jc`, `Jmin`, `Jmax` | `jc`, `jmin`, `jmax` | [§2.2 Junk](protocols-transports.md#22-junk--signature-fields), §2.1 |
| `S1`–`S4` | `s1`–`s4` | same, §2.2 |
| `H1 = N` | `"h1": N` (number) | [§2.3 Magic](protocols-transports.md#23-magic-headers-h1h4), §2.3 |
| `H1 = N-M` (AWG2 export) | `"h1": "N-M"` (string, verbatim) | same |
| `I1`–`I5` | `i1`–`i5` (verbatim, case-sensitive) | [§2.4 CPS](protocols-transports.md#24-cps-decoys-i1i5-and-the-tag-format), §3.1 |
| — | `id`, `ip`, `ib` | ours only: [§2.5 Sugar](protocols-transports.md#25-masquerade-sugar-id--ip--ib), §3.3; per-profile examples: [009/EXAMPLES](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md) |
| `HeaderProtectionKey` | `header_protection_key` | [§2.10 AWG3](protocols-transports.md#210-awg-3x-header-protection-padding-trailers-timings), §4.1 |
| `ContentPaddingAddition`, `RandomTrailers`, `DisableCookies` | `content_padding_addition`, `random_trailers`, `disable_cookies` | same, §4.2–§4.3 |
| `RekeyAfterTime` … `MaxHandshakeAttempts` | `rekey_after_time` … `max_handshake_attempts` | same, §4.3 |
| `AdvancedSecurity` | none | a server knob for parsing incoming traffic, not needed by the client: [SPEC 031](../SPECS/TASKS/031-AWG_PARITY_AUDIT_ADVANCED_SECURITY/SPEC.md) |
| `[Peer] PublicKey`, `PresharedKey` | `peers[].public_key`, `pre_shared_key` | [`peers`](../docs/configuration/endpoint/wireguard.md#peers) |
| `[Peer] Endpoint host:port` | `peers[].address` + `peers[].port` | [`peers.address`](../docs/configuration/endpoint/wireguard.md#peersaddress) |
| `[Peer] AllowedIPs` | `peers[].allowed_ips` | [`peers.allowed_ips`](../docs/configuration/endpoint/wireguard.md#peersallowed_ips) |
| `[Peer] PersistentKeepalive = N` or `N-M` | `peers[].persistent_keepalive_interval`: a number or `"N-M"` | [`persistent_keepalive_interval`](../docs/configuration/endpoint/wireguard.md#peerspersistent_keepalive_interval) |
| — | `udp_fragment` | §5, [SPEC 028](../SPECS/TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) |

> ⚠️ If `awg.conf` omits `MTU` or sets the WireGuard default 1420, lowering
> it during the transfer is mandatory (§5). Everything else transfers 1:1
> without renaming, except for case: `Jc` → `jc`, `H1` → `h1`.

> 📖 Normative description of the parameters:
> [AmneziaWG in the Amnezia documentation](https://docs.amnezia.org/documentation/amnezia-wg/).
> Reference code: [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go)
> (the port in the fork matches [v3.1.20260828](https://github.com/amnezia-vpn/amneziawg-go/releases/tag/v3.1.20260828)).
> Notation of the `id`/`ip`/`ib` sugar: [WireSock Secure Connect](https://www.wiresock.net/).

---

# 8. Upstream position

SagerNet/sing-box has given a **flat refusal** on AmneziaWG, of the same
kind as on XHTTP ([xray-protocols-explained §4.8](xray-protocols-explained.md#48-upstream-position)).

- Request [SagerNet/sing-box#4045](https://github.com/SagerNet/sing-box/issues/4045)
  (AmneziaWG 2.0, 2026-04-14) was closed by the maintainer as `not planned`
  on 2026-04-30 with a single comment that pointed the author to Hiddify
  and Karing. There are no arguments on the merits.
- The WireGuard endpoint cannot be extended from outside: upstream
  `sagernet/wireguard-go` has no hook points for obfuscation, and sing-box
  pulls it in as a regular dependency. Hence the fork submodule and the
  `replace` in `go.mod`.

This is one of the two transport features the fork started with
([CONSTITUTION](../SPECS/CONSTITUTION.md)). There is nowhere to merge them
back, so the fork is permanent.

> 🔀 The fork deliberately does not implement the server half: the server
> is `amneziawg-go` or `amneziawg-linux-kernel-module` from Amnezia, and a
> client fork of sing-box does not compete with it. The server-side
> `AdvancedSecurity` is not needed for the same reason.

---

# 9. Popular misconceptions

- **«AWG encrypts more securely than WireGuard.»** No. The cryptography is
  the same down to the byte: Noise IK, Curve25519, ChaCha20-Poly1305. AWG
  changes only the packet shape on the wire. The AWG3 header cipher
  protects metadata from the classifier; the payload is no harder to break.
- **«Enabling `jc` is enough, the rest is optional.»** Junk and decoys are
  configuration, not negotiation: everything set on the server must be set
  on the client with the same values. Omit `s4` and the handshake passes,
  data does not.
- **«MTU as in WireGuard, 1420.»** `s4` eats into it on every packet. With
  `s4 = 60` the ceiling is 1380, the recommendation is 1280 (§5).
- **«`h1`–`h4` hide everything that needs hiding.»** In AWG2 the header
  stays low-entropy: the receiver index and the counter are readable. Only
  `header_protection_key` in AWG3 hides them (§4.1).
- **«The client needs `AdvancedSecurity`.»** It is a server knob for
  parsing incoming connections. It has no effect on what the client sends.
- **«The `dns` or `stun` profile is safer than `quic`, the packet is more
  honest.»** Packet quality does not decide; the destination anomaly does
  (§3.4). On a device only `quic` passed.
- **«Without `with_awg` the AWG fields are simply ignored.»** No: in such a
  build a config with an AWG field is rejected with an explicit error. A
  silent fallback to plain WireGuard would mean a tunnel without
  obfuscation posing as an obfuscated one.

---

# 10. Typical failures

| Symptom | Cause | Where to look |
|---|---|---|
| Handshake does not complete, nothing in the log | Some server parameter did not match: `jc`/`jmin`/`jmax`, `s1`–`s4`, `h1`–`h4`, `i1`–`i5`, `header_protection_key` | compare with the server export byte for byte, §4.4 |
| `received handshake response`, then `message too long` | `mtu` above the budget with `s4` set | §5: `mtu ≤ 1500 − 60 − s4`, better 1280 |
| Handshake completes, data does not flow, no errors | `s4` does not match the server; or the upper tunnel's `allowed_ips` does not cover the addresses | §2.4; `allowed_ips: ["0.0.0.0/0", "::/0"]` |
| `headers must not overlap` at load | `h1`–`h4` ranges overlap, or not all four are set | §2.3 |
| `… is too short for header_protection_key` | One of `s1`–`s4` is below 12 | §4.1 |
| `AmneziaWG (awg) support is not included in this build` | Build without `with_awg` | §6.1 |
| Works directly, fails via `detour` | Oversized outer datagram; check whether `udp_fragment: false` is set | §5, [SPEC 028](../SPECS/TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) |
| Downlink loses packets with `random_trailers` | Wide `h1`–`h4` ranges on a reference-implementation server | §6.3: narrow the ranges |
| `id/ip/ib masquerade conflicts with an explicit i1` | Both the sugar and `i1` are set | §3.3: keep one |

---

# 11. Glossary

| Term | Meaning |
|---|---|
| **Middlebox** | Any equipment between client and server that inspects traffic or interferes with it |
| **Classifier (DPI)** | Equipment that flags a flow by packet shape, without keys |
| **WireGuard signature** | Type in the first byte, sizes 148/92/64, multiples of 16, handshake rhythm |
| **Junk** | Random datagrams before the handshake (`jc`, `jmin`, `jmax`) |
| **Padding S1–S4** | Random bytes before a message; `s4` goes before every data packet |
| **Magic header** | Substituted message type word, single or as a range |
| **CPS** | Controlled Packet Sequence: decoys `i1`–`i5` written in the tag mini-language |
| **Decoy** | A packet shaped like another protocol, sent first |
| **Masquerade sugar** | `id`/`ip`/`ib`: a decoy declaration instead of an `i1` string |
| **Header protection** | ChaCha20 cipher on the header of every packet, nonce from the padding (AWG3) |
| **UDP window** | The largest datagram the path has already carried; limits content padding and trailers |
| **Server parameter** | Must match on both ends, otherwise there is no handshake |
| **MTU budget** | `1500 − 28 − 32 − s4`; recommendation 1280 |
| **Graft** | The amneziawg-go obfuscation ported on top of sagernet/wireguard-go in a fork submodule |
| **UAPI** | The text configuration interface of a WireGuard device, through which sing-box passes parameters |
| **Receiver index** | Session identifier in a data packet; the fork classifies received packets by it |

---

## See also

- **[protocols-transports.md §2](protocols-transports.md#2-amneziawg-203x-awg2-awg3)**:
  every field, default, error; §2.6 MTU budget, §2.9 errors verbatim,
  §2.10 AWG3.
- **[lx-config.md §2](lx-config.md#2-amneziawg-203x-awg2-awg3)**: overview.
- **[xray-protocols-explained.md](xray-protocols-explained.md)**: the same
  format for REALITY, Vision, XHTTP and VLESS encryption.
- Feature spec: [003-AWG](../SPECS/FEATURES/003-AWG/FEATURE.md); tasks:
  [003](../SPECS/TASKS/003-AWG2_CLIENT_ENDPOINT/SPEC.md) (AWG2 endpoint),
  [005](../SPECS/TASKS/005-AWG2_RANGED_MAGIC_HEADERS/SPEC.md) (ranged `h`),
  [008](../SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION/SPEC.md),
  [009](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) (masquerade sugar),
  [025](../SPECS/TASKS/025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md),
  [026](../SPECS/TASKS/026-AWG_MAGIC_VS_RESERVED_CLEAR/SPEC.md),
  [028](../SPECS/TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) (DF and nested tunnels),
  [031](../SPECS/TASKS/031-AWG_PARITY_AUDIT_ADVANCED_SECURITY/SPEC.md) (parity audit),
  [080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md) (AWG3),
  [081](../SPECS/TASKS/081-AWG_RECEIVE_INDEX_FIRST_CLASSIFICATION/SPEC.md) (receive),
  [112](../SPECS/TASKS/112-TAILSCALE_DIRECT_PATH_SEND_HEADROOM/SPEC.md) (datagram headroom).
- Code: `submodules/wireguard-go/device/` (graft), `option/wireguard_awg.go`
  (fields and validation), `transport/wireguard/` (pass-through to device).
