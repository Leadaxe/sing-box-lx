# Xray-family protocols: how they work and how sing-box-lx supports them

> 🌐 Русская версия: **[xray-protocols-explained.ru.md](xray-protocols-explained.ru.md)**.

> 🧭 **Where to look.** The fork's documentation has three levels, by the reader's question:
> [lx-config](lx-config.md) — what the fork has and how to enable it;
> [protocols-transports](protocols-transports.md) — every field, type, default, error text;
> [xray-protocols-explained](xray-protocols-explained.md) and [amneziawg-explained](amneziawg-explained.md) —
> how it works, why, how the fork does it and how it differs from vanilla.

REALITY, Vision, XHTTP and VLESS `encryption` were invented and are
developed by the Xray project. They are proxy protocols (not VPN): they carry
connections, not IP packets. The servers in subscriptions almost always run
Xray. Vanilla sing-box supports this set incompletely and with a delay: some
parts are missing entirely, others exist but no longer work against a recent
Xray server. `sing-box-lx` closes this gap on the client side.

For each protocol the document answers three questions. **How it works**:
why it exists, what it does to the bytes on the wire, and why it does it that
way. **How the fork supports it**: where the code lives and which decisions
were made. **How this differs from vanilla sing-box**: the summary table is
in [§7](#7-differences-from-vanilla-sing-box). The same section explains why
the base engine is sing-box and not Xray itself
([§7.1](#71-why-the-base-is-sing-box-not-xray)).
Fields, defaults and error texts are not listed here. They are in
[lx-config.md](lx-config.md) (overview by feature) and
[protocols-transports.md](protocols-transports.md) (parameter
reference). The current state of each area is in the feature specs:
[017-REALITY](../SPECS/FEATURES/017-REALITY/FEATURE.md),
[002-XHTTP](../SPECS/FEATURES/002-XHTTP/FEATURE.md),
[012-VLESS_ENCRYPTION](../SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md).

Audience: a core or client developer who opens
`common/tls/reality_client.go`, `transport/v2rayxhttp/` or
`protocol/vless/encryption/` for the first time and wants to understand what
they are reading. TLS knowledge at the level of "there is a ClientHello, there
is a certificate" is assumed.

Callouts in the text are marked by type:

- ⚠️ pitfall: something that fails silently and does not explain itself;
- 🔀 differs from vanilla sing-box;
- 🧭 TL;DR: the section's conclusion in a few sentences;
- 📖 normative source.

## Contents

- [§0 The whole picture: layers and threats](#0-the-whole-picture-layers-and-threats)
- [§1 Foundation: TLS, ClientHello and the fingerprint](#1-foundation-tls-clienthello-and-the-fingerprint)
  - [1.1 What the ClientHello reveals](#11-what-the-clienthello-reveals)
  - [1.2 Fingerprint and uTLS](#12-fingerprint-and-utls)
  - [1.3 Hybrid key share and ClientHello size](#13-hybrid-key-share-and-clienthello-size)
  - [1.4 Fragmentation](#14-fragmentation)
  - [1.5 Example: Xray and sing-box-lx](#15-example-xray-and-sing-box-lx)
- [§2 REALITY](#2-reality)
  - [2.1 What problem it solves](#21-what-problem-it-solves)
  - [2.2 The idea: someone else's site as cover](#22-the-idea-someone-elses-site-as-cover)
  - [2.3 How the server recognises its own clients: session id](#23-how-the-server-recognises-its-own-clients-session-id)
  - [2.4 Two handshake outcomes](#24-two-handshake-outcomes)
  - [2.5 How the client verifies the server](#25-how-the-client-verifies-the-server)
  - [2.6 What our core does differently from upstream](#26-what-our-core-does-differently-from-upstream)
  - [2.7 Typical failures](#27-typical-failures)
  - [2.8 Example: Xray and sing-box-lx](#28-example-xray-and-sing-box-lx)
  - [2.9 Upstream position](#29-upstream-position)
- [§3 Vision](#3-vision)
  - [3.1 The TLS-in-TLS problem](#31-the-tls-in-tls-problem)
  - [3.2 How Vision works](#32-how-vision-works)
  - [3.3 What Vision combines with](#33-what-vision-combines-with)
  - [3.4 Example: Xray and sing-box-lx](#34-example-xray-and-sing-box-lx)
- [§4 XHTTP](#4-xhttp)
  - [4.1 Why another HTTP transport](#41-why-another-http-transport)
  - [4.2 Two directions and three modes](#42-two-directions-and-three-modes)
  - [4.3 The HTTP version is derived, not set](#43-the-http-version-is-derived-not-set)
  - [4.4 Session, sequence numbers, padding](#44-session-sequence-numbers-padding)
  - [4.5 xmux: a connection pool like a browser's](#45-xmux-a-connection-pool-like-a-browsers)
  - [4.6 Pitfalls where the connection stays silent](#46-pitfalls-where-the-connection-stays-silent)
  - [4.7 Example: Xray and sing-box-lx](#47-example-xray-and-sing-box-lx)
  - [4.8 Upstream position](#48-upstream-position)
- [§5 VLESS encryption: the post-quantum layer](#5-vless-encryption-the-post-quantum-layer)
  - [5.1 The threat: record now, decrypt later](#51-the-threat-record-now-decrypt-later)
  - [5.2 What ML-KEM is and why a hybrid](#52-what-ml-kem-is-and-why-a-hybrid)
  - [5.3 Where the layer lives](#53-where-the-layer-lives)
  - [5.4 Handshake: two keys, one secret](#54-handshake-two-keys-one-secret)
  - [5.5 0-RTT and tickets](#55-0-rtt-and-tickets)
  - [5.6 Wire appearance and padding](#56-wire-appearance-and-padding)
  - [5.7 Popular misconceptions](#57-popular-misconceptions)
  - [5.8 Example: Xray and sing-box-lx](#58-example-xray-and-sing-box-lx)
- [§6 How the layers stack](#6-how-the-layers-stack)
- [§7 Differences from vanilla sing-box](#7-differences-from-vanilla-sing-box)
  - [7.1 Why the base is sing-box, not Xray](#71-why-the-base-is-sing-box-not-xray)
  - [7.2 How the fork delta is structured](#72-how-the-fork-delta-is-structured)
  - [7.3 Summary by protocol](#73-summary-by-protocol)
- [§8 Glossary](#8-glossary)
- [See also](#see-also)

---

# 0. The whole picture: layers and threats

A single outgoing VLESS connection in the core passes through a stack of
layers. From top to bottom, from application data to the network:

```
application data (most often someone else's TLS to the target site)
        │
        ▼
VLESS ─── flow: xtls-rprx-vision        (§3)  removes double encryption
   └── encryption: mlkem768x25519plus   (§5)  post-quantum payload encryption
        │
        ▼
transport ── tcp (no block) / ws / grpc / httpupgrade / xhttp    (§4)
        │
        ▼
tls ── std / utls (fingerprint) / reality                         (§1, §2)
        │
        ▼
TCP or QUIC → network
```

Each layer answers its own middlebox. There are three middleboxes, and they
differ:

| Middlebox | How it acts | Which layer answers |
|---|---|---|
| **Passive classifier** (DPI) looks at the shape of the traffic: packet lengths, timing, SNI, the cipher set in the ClientHello, characteristic sequences | Network equipment on the path, in real time | uTLS fingerprint (§1), Vision (§3), XHTTP (§4), padding everywhere |
| **Active server probing** connects to the server itself and looks at what it answers | Against a list of addresses flagged by the classifier | REALITY (§2) |
| **Traffic recording for future decryption** by a quantum computer | Anyone with a large disk | VLESS `encryption` (§5), hybrid key share in TLS (§1.3) |

> 🧭 **TL;DR:** REALITY and VLESS `encryption` **do not replace each
> other**. REALITY makes sure the server accepts the connection at all and that
> a prober finds nothing. `encryption` makes sure recorded traffic cannot be
> opened later. The only thing they share is the mathematical primitive
> ML-KEM-768 + X25519.

---

# 1. Foundation: TLS, ClientHello and the fingerprint

## 1.1 What the ClientHello reveals

TLS 1.3 encrypts almost everything except the client's first message. The
ClientHello travels in plaintext and carries:

- **SNI**: the name of the server the client wants to connect to;
- the **cipher list** and **extensions** in a specific order;
- **supported_groups**: which curves the client supports for key exchange;
- **key_share**: public halves for one or more of these groups, so that the
  server can answer at once;
- **session id**: 32 bytes that carry no meaning in TLS 1.3 and are filled
  randomly for compatibility with TLS 1.2;
- **random**: 32 random bytes.

For DPI this is a business card. Each library has its own set and order of
fields, and they tell Chrome from Firefox, and a Go program from any
browser.

## 1.2 Fingerprint and uTLS

The standard Go `crypto/tls` sends a ClientHello that does not resemble any
browser. The uTLS library builds the ClientHello from a preset: `chrome`,
`firefox`, `safari`, `edge`, `ios` and so on. The `tls.utls.fingerprint` key
selects the preset.

> ⚠️ The core **never silently substitutes** the fingerprint, under any
> conditions: what was requested is what goes out (rule of feature
> [017](../SPECS/FEATURES/017-REALITY/FEATURE.md)).
> A node with a preset without the hybrid share dies on a new Xray server in
> our core exactly as it does in the Xray client itself, and this is not a
> core defect (§1.3, §2.6).

Our uTLS is the fork submodule `Leadaxe/utls-lx` on top of `metacubex/utls`.
The fork is needed because of §1.3: two of the three presets with a hybrid
key share were added by us. It also carries Chrome 155 under the explicit
name `chrome_155`; `chrome` stays Chrome 133 (SPEC 118).

## 1.3 Hybrid key share and ClientHello size

Since 2024 browsers send two entries in `key_share`: the classical X25519 and
the hybrid **X25519MLKEM768**, where 1184 bytes of an ML-KEM-768 public key
are appended to the 32 bytes of X25519 (ML-KEM is covered in §5.2). The goal
is post-quantum protection of the key exchange of TLS itself.

For resistance to middleboxes this has two consequences.

1. **A fingerprint without the hybrid no longer looks like a browser.** The
   presets `edge`, `ios`, `android`, `360`, `qq` carry only X25519. Live
   browsers no longer send such ClientHellos. The hybrid is present in
   `chrome`, `firefox`, `safari`.
2. **The ClientHello has grown threefold**: 1720 bytes for `chrome` instead
   of 594. It does not fit into one TCP segment and goes out as two. Some
   networks lose the second segment, and the handshake never completes.

> 🔀 **Only in sing-box-lx:** the per-node key `tls.reality.key_share` ([SPEC 089](../SPECS/TASKS/089-REALITY_KEY_SHARE_OPTION/SPEC.md)). `classical`
> strips the hybrid and returns a single-segment ClientHello. `hybrid`
> requires the hybrid and fails with a clear error on a preset that lacks it.
> An empty value keeps what the fingerprint carries. Why this matters for
> REALITY specifically is in §2.6.

## 1.4 Fragmentation

The goal of fragmentation is to keep the classifier from seeing the SNI in
one piece. Equipment that matches plaintext against a pattern usually looks
at each packet separately and does not reassemble the TCP stream. If the
server name is split across packets, the pattern does not match.

**How Xray does it: manually.** Fragmentation is a property of the `freedom`
outbound (`settings.fragment`) or of a `finalmask.tcp` element. The user sets
three numbers: which packets to split (`packets: "tlshello"` or a range of
packet numbers), the chunk length (`length: "100-200"`), and the pause
between chunks (`interval` / `delay: "10-20"` ms). The new form also has
`maxSplit`. Lengths are picked randomly from the range **without regard to
the content**. Where a boundary falls relative to the SNI is a matter of
chance, and with an unlucky range the whole name can go into one chunk. The
pauses are fixed. Too short, and the OS kernel merges the chunks back into
one segment. Too long, and every handshake slows down. Tuning for a specific
network is trial and error.

**How sing-box does it: by content.** The `common/tlsfragment` package
(upstream, since 1.12) parses the first packet as a ClientHello and finds the
SNI extension in it. The server name is split into labels without the public
suffix (for `www.example.com` these are `www` and `example`), and a random
cut point is chosen **inside each label**. So no chunk contains the whole
name, or even any significant label whole, whatever network this runs in.
There are no manual lengths: the structure of the ClientHello defines the
boundaries.

Then there are two modes. They can be enabled separately or together:

- **`tls.fragment`**: each chunk goes out as a separate TCP segment with
  Nagle's algorithm disabled. After each segment the core **waits for the
  ACK** from the server by reading the socket state (`TCP_INFO` on Linux,
  equivalents on Apple and Windows), and only then sends the next one. This
  guarantees that the chunks are not merged either in the OS kernel or on
  the path. The pause is exactly as long as the network needs, not picked at
  random. If the ACK arrives faster than 20 ms, the target is considered
  local or a transparent proxy, and the core switches to the fixed pause
  `fragment_fallback_delay` (500 ms by default). The same happens on
  platforms without access to the socket state. The cost is a delay of as
  many round trips as there are chunks, so upstream advises enabling this
  mode only for names known to be filtered.
- **`tls.record_fragment`**: the same cut points, but each chunk is wrapped
  into its **own TLS record** with a header, and all records go out as a
  single TCP write. One packet, no pauses, no delay. It works against a
  classifier that parses TLS records one at a time and expects the SNI in
  the first one. It does not work against one that reassembles the stream.
  This is the first thing to try.

Both modes touch only the first packet of the connection. Traffic after the
handshake does not change. Xray has no equivalent of `record_fragment` as a
separate mode: splitting into TLS records happens there only as a side
effect, when a chunk boundary coincides with a record boundary.

### Nested protocols: why the ClientHello gets lost and what the core does

A separate case, and the reason fragmentation in `sing-box-lx` turns on by
itself. When one protocol is wrapped in another (an outbound dials through
`detour`, a `chain` link goes through the previous link, VLESS runs inside
WireGuard or MASQUE), our ClientHello goes out **inside** someone else's
tunnel. The lower leg forwards it further on its own behalf. The path behind
that server has its own MTU, usually smaller than ours, because the tunnel
used up part of the frame for its headers.

While the first packet is small, this goes unnoticed. After
[SPEC 083](../SPECS/TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md) the hybrid
ClientHello weighs 1.5–1.9 KB and goes out as two TCP segments. On some lower
legs it no longer fits. The ICMP *Fragmentation Needed* from an intermediate
router does not reach us: it is addressed to the server of the lower leg, not
to the client inside the tunnel. The packet just disappears. From the outside
this is `tls handshake: EOF` after 12–17 seconds without a single hint. It
reproduces with plain `curl` through the same leg, so the cause is in the
path, not in the core.

> 🧭 **TL;DR:** a nested ClientHello can hit the MTU. Losing it is not an
> option: without it the nested connection cannot be established. So the core
> fragments it by itself.

> 🔀 **Vanilla sing-box has no such mechanism.** There `fragment` and
> `record_fragment` are manual keys, and the core does not know that an
> outbound dials through another outbound. A ClientHello under `detour` goes
> out whole, as it does on a direct connection, and on a leg with a smaller
> MTU the connection silently fails to come up. The user has to work out
> on their own that the size of the first packet is the problem, and enable
> fragmentation by hand on every node that goes through a tunnel.
> For REALITY nodes in vanilla this does not help even by hand; see below
> about [SPEC 088](../SPECS/TASKS/088-REALITY_FRAGMENT_BYPASS/SPEC.md).

So `sing-box-lx` does it by itself
([SPEC 060](../SPECS/TASKS/060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md),
overview: [lx-config §9](lx-config.md#9-automatic-clienthello-fragmentation-under-detour-spec-060)):

- When the TLS client is built, the outbound gets a "dials through detour"
  flag. If the flag is set and the user has set neither `fragment` nor
  `record_fragment`, the core enables **`record_fragment`**
  (`applyDetourFragmentDefault` in `common/tls/client.go`). This mode was
  chosen on purpose: it cuts by TLS records without pauses, and each record
  is always smaller than any reasonable MTU. Measured through a broken
  leg: without fragmentation, failure after 12 s; `fragment`, 0.6 s;
  `record_fragment`, 0.1 s.
- The default is applied **before** a specific engine sees the options, so
  the standard client, uTLS and REALITY get it the same way.
  For REALITY this started working only with
  [SPEC 088](../SPECS/TASKS/088-REALITY_FRAGMENT_BYPASS/SPEC.md). Before it,
  the REALITY client built the uTLS connection on a bare socket, and
  fragmentation, even an explicit one, silently had no effect on it. Upstream
  sing-box still works this way.
- **An explicit value in the config always wins.** `fragment: true` is not
  upgraded to `record_fragment`: if packet-level cutting was chosen, it
  stays the user's choice.
- **Handshake only.** Traffic after it is not touched, so there is no
  permanent tax on the tunnel.
- **`h3`/QUIC is not affected**: there is no TLS over TCP there, and QUIC
  itself keeps the Initial below the threshold.
- **Nested chains are covered automatically**: each
  [`chain`](../SPECS/FEATURES/015-CHAIN/FEATURE.md) link has its own
  `detour`, and [MASQUE](../SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md) over
  `h2` through a detour takes the same path.

> ⚠️ **Known limitation:** an explicit `"record_fragment": false` cannot be
> told apart from "not set", so under `detour` the default turns on anyway.
> There is currently no way to dial through a detour with no fragmentation at
> all. The way to change the mode is `"fragment": true`. The task lives in
> the [004-HOTFIXES](../SPECS/FEATURES/004-HOTFIXES/FEATURE.md) registry: it
> is a patch for path behaviour, not a protocol feature, and it has a removal
> condition.

Whether fragmentation helps in a network that loses a large first packet
**without** a tunnel is a property of that network. The second lever there
is `key_share: classical` (§1.3).

## 1.5 Example: Xray and sing-box-lx

Plain VLESS + TLS over bare TCP, without REALITY: a browser fingerprint, ALPN
and ClientHello fragmentation. In Xray fragmentation exists in two forms, and
in our core both reduce to one `tls` block.

Xray, new form (`finalmask`, Xray ≥ 25.x):

```jsonc
{
  "protocol": "vless",
  "settings": {
    "vnext": [{
      "address": "example.com",
      "port": 443,
      "users": [{ "id": "00000000-0000-0000-0000-000000000000", "encryption": "none" }]
    }]
  },
  "streamSettings": {
    "network": "tcp",
    "security": "tls",
    "tlsSettings": {
      "serverName": "example.com",
      "fingerprint": "chrome",
      "alpn": ["h2", "http/1.1"],
      "allowInsecure": false
    },
    "finalmask": {
      "tcp": [{
        "type": "fragment",
        "settings": { "packets": "tlshello", "length": "20-40", "delay": "3-10", "maxSplit": "3-6" }
      }]
    }
  }
}
```

Xray, old form (a helper `freedom` with `fragment`, which the node goes
through via `sockopt.dialerProxy`):

```jsonc
[
  {
    "tag": "proxy",
    "protocol": "vless",
    "settings": { "vnext": [ /* as above */ ] },
    "streamSettings": {
      "network": "tcp",
      "security": "tls",
      "tlsSettings": { "serverName": "example.com", "fingerprint": "chrome" },
      "sockopt": { "dialerProxy": "fragment" }
    }
  },
  {
    "tag": "fragment",
    "protocol": "freedom",
    "settings": { "fragment": { "packets": "tlshello", "length": "100-200", "interval": "10-20" } }
  }
]
```

sing-box-lx, one outbound for both forms:

```jsonc
{
  "type": "vless",
  "tag": "tls-out",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "alpn": ["h2", "http/1.1"],
    "insecure": false,
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "fragment": true,
    "record_fragment": false
  }
}
```

Key mapping:

| Xray | sing-box-lx | Where described |
|---|---|---|
| `security: "tls"` + `tlsSettings` | `tls.enabled: true` | [TLS](../docs/configuration/shared/tls.md), [`enabled`](../docs/configuration/shared/tls.md#enabled) |
| `tlsSettings.serverName` | `tls.server_name` | [`server_name`](../docs/configuration/shared/tls.md#server_name), §1.1 |
| empty `serverName` | `tls.disable_sni: true` | [`disable_sni`](../docs/configuration/shared/tls.md#disable_sni); an empty `server_name` in our core does **not** remove the SNI, it substitutes the address |
| `tlsSettings.fingerprint` | `tls.utls.enabled: true` + `tls.utls.fingerprint` | [`utls`](../docs/configuration/shared/tls.md#utls), §1.2; the hybrid key share is carried by `chrome`, `firefox`, `safari` (§1.3) |
| `tlsSettings.alpn` | `tls.alpn` | [`alpn`](../docs/configuration/shared/tls.md#alpn); for XHTTP it decides the HTTP version (§4.3) |
| `tlsSettings.allowInsecure` | `tls.insecure` | [`insecure`](../docs/configuration/shared/tls.md#insecure) |
| `tlsSettings.minVersion`, `cipherSuites` | `tls.min_version`, `tls.cipher_suites` | [`min_version`](../docs/configuration/shared/tls.md#min_version), [`cipher_suites`](../docs/configuration/shared/tls.md#cipher_suites); in Xray `cipherSuites` is a colon-separated string, in our core an array |
| `tlsSettings.echConfigList` | `tls.ech.enabled: true` + `tls.ech.config` | [ECH Fields](../docs/configuration/shared/tls.md#ech-fields); incompatible with REALITY (§6) |
| `finalmask.tcp[type=fragment]` | `tls.fragment: true` (TCP segments) or `tls.record_fragment: true` (TLS records) | [`fragment`](../docs/configuration/shared/tls.md#fragment), [`record_fragment`](../docs/configuration/shared/tls.md#record_fragment), §1.4 |
| `freedom` + `settings.fragment` + `sockopt.dialerProxy` | the same `tls.fragment` on the node that goes out directly | §1.4; the helper `freedom` does not become a separate outbound |
| `length`, `delay` / `interval`, `maxSplit` | none | the core finds boundaries by SNI and pauses by ACK (§1.4); there is nothing to carry over except the fact that fragmentation is on |
| n/a | `tls.fragment_fallback_delay` | [`fragment_fallback_delay`](../docs/configuration/shared/tls.md#fragment_fallback_delay): the wait time used when the core cannot compute it itself; 500 ms by default |
| n/a | `record_fragment` turns on by itself under `detour` | [lx-config §9](lx-config.md#9-automatic-clienthello-fragmentation-under-detour-spec-060) |
| n/a | `tls.reality.key_share` | ours only, §1.3; applies to REALITY, example in §2.8 |

> 📖 Normative description of the Xray fields: [TLS in the Project X documentation](https://xtls.github.io/config/transport.html#tlsobject).

---

# 2. REALITY

## 2.1 What problem it solves

Classic VLESS + TLS requires a domain and a certificate on the server itself.
This gives a middlebox three hooks:

- the certificate shows a fresh domain issued for a cheap VPS;
- the SNI in the ClientHello is matched to the IP address, and the address
  goes onto a list;
- active probing: the middlebox connects by itself, gets the certificate and
  sees an empty placeholder site.

ECH would close the first two hooks, but ECH needs a DNS record and server
support, and it does not close the third one at all.

## 2.2 The idea: someone else's site as cover

REALITY (RPRX, Xray 1.8, 2023) removes the certificate from the server. The
server pretends to be someone else's large site: in Xray these are `dest` and
`serverNames`, for example `www.microsoft.com`. Anyone who connects without
presenting the secret gets the **real** TLS response of that site: the server
simply forwards the bytes both ways. To a prober the server is
indistinguishable from a proxy in front of Microsoft.

A genuine client presents the secret in a way that keeps the ClientHello,
seen from the outside, an ordinary browser ClientHello to that same site.

## 2.3 How the server recognises its own clients: session id

The secret hides in the **session id** field of the ClientHello: the same 32
bytes that are random in TLS 1.3 anyway. The client
(`common/tls/reality_client.go`) builds them like this:

```
bytes  0..2   REALITY client version (ours: 26.3.27)
byte   3      reserved
bytes  4..7   unix time, seconds
bytes  8..15  short_id (8 bytes from the config)
bytes 16..31  AES-GCM tag
```

The first 16 bytes are encrypted with AES-GCM. The key is derived as follows:

1. The client takes the **ephemeral** X25519 pair that uTLS has already
   generated for the `key_share` of this ClientHello. There is no separate
   key: the same public key that goes into `key_share` is used by the server
   both for TLS and for checking the secret.
2. ECDH of this private key with the server's `tls.reality.public_key` gives
   a shared secret.
3. HKDF-SHA256 with salt `random[0..20]` and label `REALITY` turns it into
   `AuthKey`.
4. AES-GCM with key `AuthKey`, nonce `random[20..32]` and the whole raw
   ClientHello as associated data seals 16 bytes into 32.

From the outside the session id still looks random. The server knows its
private key and the public one from `key_share`. It repeats steps 2–4,
decrypts the block and checks: the version is not below `minClientVer`, the
time is within the `maxTimeDiff` tolerance, the `short_id` is in the list.

## 2.4 Two handshake outcomes

```
ClientHello ──► REALITY server
                   │
         session id decrypted, time and short_id match?
                   │
        ┌── yes ───┴──── no ────┐
        ▼                       ▼
  server finishes          server forwards everything
  TLS itself with a        to dest; the client talks
  temporary cert           to the real site
  under AuthKey (§2.5)     and gets real
        │                  pages
        ▼                       │
  proxy connection              ▼
                        "reality verification failed"
```

> ⚠️ The right-hand outcome is the main property of REALITY and also its main
> diagnostic trap. The server **never reports** why it rejected the client:
> a stale clock, a wrong `short_id`, an outdated version, an unsuitable
> `key_share` all end the same way, with a page of someone else's site. To
> the client this looks like a certificate verification error.

## 2.5 How the client verifies the server

A genuine server answers with a temporary certificate with an ed25519 public
key. The signature of this certificate is not a real signature but
`HMAC-SHA512(AuthKey, public key)`. An ordinary TLS client would reject such a
certificate, but an ordinary client never gets here: it does not know
`AuthKey`.

Our client does two things in `VerifyPeerCertificate` (`realityVerifier`):

1. If the first certificate in the chain is ed25519 and its signature matches
   the HMAC, this is a genuine server, and the connection is marked
   `verified`.
2. Otherwise the chain is verified as usual, against the SNI. If it is
   **valid**, the client is talking to the real cover site: the server
   rejected it (or someone sits between them). Proxy data does not go into
   such a connection. Outwardly this is the same `reality verification failed`.

So that a rejected client does not stand out among browsers, the Xray client
then browses the cover site (spider). Our core has this in a simplified
form: one GET with the fingerprint's User-Agent and a random cookie. A full
`spiderX` is intentionally not implemented.

## 2.6 What our core does differently from upstream

> 🔀 **Vanilla differs.** The REALITY server evolves together with Xray and
> periodically tightens which ClientHello it accepts as its own. Upstream
> sing-box does not keep up with this, and subscription nodes silently end up
> on the cover site.

Feature 017 keeps the client side compatible:

- **The hybrid key share is mandatory for Xray ≥ v26.9.8** ([SPEC 083](../SPECS/TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md)).
  Commit `XTLS/REALITY@8cdf7bf` rejects a ClientHello without
  `X25519MLKEM768` before `X25519`. Upstream sing-box itself stripped this
  share from the ClientHello (a workaround for old uTLS), so every one of its
  nodes dies on a new server. We send what the preset carries and compute
  `AuthKey` from **the same key the server will choose**: pure X25519 if it
  is present in `key_share`, otherwise the X25519 part of the hybrid. The
  selection order mirrors the Xray server code.
- **The hybrid comes before the classical entry, exactly once** in
  `key_share` and `supported_groups`. This is the server's acceptance rule; a
  guard test holds it for all three presets.
- **The client version** in the session id is exactly the required minimum ([SPEC 053](../SPECS/TASKS/053-REALITY_MIN_CLIENT_VER/SPEC.md)).
  A server with `minClientVer` rejects clients below the threshold.
- **`key_share: classical | hybrid`** ([SPEC 089](../SPECS/TASKS/089-REALITY_KEY_SHARE_OPTION/SPEC.md), §1.3) is for networks that
  lose a two-segment ClientHello. Classical works only with Xray
  < v26.9.8. There is no automatic fallback between the modes: a server
  rejection cannot be told apart from a network loss without a second
  attempt, and after a loss the network drops connections to the same address
  for about a minute.
- **Fragmentation also applies to REALITY** ([SPEC 088](../SPECS/TASKS/088-REALITY_FRAGMENT_BYPASS/SPEC.md)).

Outside the feature: post-quantum **signatures** of the REALITY certificate
(`mldsa65Seed` / `mldsa65Verify` in Xray) and `spiderX`. The core does not
accept a config with these fields.

## 2.7 Typical failures

| Symptom | Cause |
|---|---|
| `reality verification failed` on all nodes of one subscription | The server was updated to Xray ≥ v26.9.8, and the client sends a ClientHello without the hybrid: preset `edge`/`ios`/`android` or `key_share: classical` |
| The same on one node, the others work | Wrong `short_id` or `public_key`; the client clock has drifted beyond `maxTimeDiff` |
| The handshake hangs until timeout, no error | The network lost the second segment of the hybrid ClientHello. Fixed with `key_share: classical` (old server) or `record_fragment` / `detour` (new one) |
| Works on Wi-Fi, fails on a mobile network | The same segment loss; mobile networks cut it more often |

## 2.8 Example: Xray and sing-box-lx

A client VLESS + Vision + REALITY outbound over bare TCP. First, how it is
written for Xray; then the same node for our core.

Xray (`outbounds[]`):

```jsonc
{
  "protocol": "vless",
  "settings": {
    "vnext": [{
      "address": "203.0.113.10",
      "port": 443,
      "users": [{
        "id": "00000000-0000-0000-0000-000000000000",
        "flow": "xtls-rprx-vision",
        "encryption": "none"
      }]
    }]
  },
  "streamSettings": {
    "network": "tcp",
    "security": "reality",
    "realitySettings": {
      "serverName": "www.microsoft.com",
      "fingerprint": "chrome",
      "publicKey": "<reality-public-key-base64url>",
      "shortId": "0123abcd",
      "spiderX": "/"
    }
  }
}
```

sing-box-lx (`outbounds[]`):

```jsonc
{
  "type": "vless",
  "tag": "reality-out",
  "server": "203.0.113.10",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "flow": "xtls-rprx-vision",
  "tls": {
    "enabled": true,
    "server_name": "www.microsoft.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": {
      "enabled": true,
      "public_key": "<reality-public-key-base64url>",
      "short_id": "0123abcd",
      "key_share": ""
    }
  }
}
```

Key mapping:

| Xray | sing-box-lx | Where described |
|---|---|---|
| `vnext[].address`, `port` | `server`, `server_port` | [vless outbound](../docs/configuration/outbound/vless.md) |
| `users[].id` | `uuid` | [`uuid`](../docs/configuration/outbound/vless.md#uuid) |
| `users[].flow` | `flow` | [`flow`](../docs/configuration/outbound/vless.md#flow), §3 |
| `users[].encryption: "none"` | field absent | This is a legacy Xray literal, not the post-quantum layer; that one is in §5.8 |
| `security: "reality"` | `tls.enabled: true` + `tls.reality.enabled: true` | [Reality Fields](../docs/configuration/shared/tls.md#reality-fields) |
| `realitySettings.serverName` | `tls.server_name` | [`server_name`](../docs/configuration/shared/tls.md#server_name) |
| `realitySettings.fingerprint` | `tls.utls.enabled: true` + `tls.utls.fingerprint` | [`utls`](../docs/configuration/shared/tls.md#utls), §1.2 |
| `realitySettings.publicKey` | `tls.reality.public_key` | [`public_key`](../docs/configuration/shared/tls.md#public_key) |
| `realitySettings.shortId` | `tls.reality.short_id` | [`short_id`](../docs/configuration/shared/tls.md#short_id) |
| `realitySettings.spiderX` | none | §2.5, §2.6 |
| `realitySettings.mldsa65Verify` | none | §2.6 |
| n/a | `tls.reality.key_share` | ours only: [lx-config §7](lx-config.md#7-reality-key_share--hybrid-or-classical-clienthello-spec-089), §1.3 |
| n/a | `tls.fragment`, `tls.record_fragment` | [`fragment`](../docs/configuration/shared/tls.md#fragment), [`record_fragment`](../docs/configuration/shared/tls.md#record_fragment); automatic under `detour`: [lx-config §9](lx-config.md#9-automatic-clienthello-fragmentation-under-detour-spec-060) |

> 📖 Normative description of the Xray fields: [REALITY in the Project X documentation](https://xtls.github.io/config/transport.html#realityobject).

## 2.9 Upstream position

REALITY itself has been in sing-box since 1.3 (2023), and upstream does not
reject it. What is rejected is not the protocol but the **pace**: the
REALITY server changes its acceptance rules, and the sing-box client does not
follow them.

- The Xray ≥ v26.9.8 cut-off by hybrid key share was reported upstream on
  2026-09-11: [SagerNet/sing-box#4520](https://github.com/SagerNet/sing-box/issues/4520).
  The report is complete: the root cause is named (`XTLS/REALITY@8cdf7bf`),
  and it includes a comparison with mihomo on the same `metacubex/utls`
  v1.8.7, which does connect. A repeat run on 2026-09-19: broken on both the
  stable 1.14.1 and 1.15.0-alpha.6. No maintainer reaction, no labels, the
  issue is open.
- The previous cut-off of the same kind, by client version
  (`minClientVer`), was closed in our fork by
  [SPEC 053](../SPECS/TASKS/053-REALITY_MIN_CLIENT_VER/SPEC.md) the same
  way: field analysis and a patch in the fork.

> 🔀 So REALITY in the fork is not a new feature but **catch-up
> maintenance**. Every time Xray tightens the server, nodes in vanilla
> sing-box silently die, and the fix happens here
> ([SPEC 083](../SPECS/TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md),
> [088](../SPECS/TASKS/088-REALITY_FRAGMENT_BYPASS/SPEC.md),
> [089](../SPECS/TASKS/089-REALITY_KEY_SHARE_OPTION/SPEC.md)).

---

# 3. Vision

## 3.1 The TLS-in-TLS problem

Almost all user traffic is already encrypted with TLS. Inside a VLESS tunnel
over TLS or REALITY this gives TLS inside TLS. From the outside it is an
ordinary TLS connection in which the first records after the handshake repeat
the **shape** of a second handshake: ClientHello, ServerHello with a
certificate, Finished. Their lengths and timing are characteristic, and DPI
recognises them without decrypting anything.

The first generations of XTLS (`xtls-rprx-origin`, `direct`, `splice`) solved
this radically. Once the inner TLS was established, the outer encryption was
switched off entirely, and the inner records went over the wire as they
were. The speed was excellent, but the real lengths of the inner records
became visible from the outside, and in 2022 DPI learned to tell the
scheme apart. RPRX retired it.

## 3.2 How Vision works

The `flow: xtls-rprx-vision` flow does the same thing more carefully. The
implementation is in `sing-vmess`, an upstream dependency; we have no Vision
code of our own.

1. **Handshake padding.** Vision pads the first packets of the connection,
   which carry someone else's TLS handshake inside, to a random length. From
   the outside the ClientHello, certificate and Finished of the inner
   connection cannot be told apart.
2. **Direct forwarding after the handshake.** As soon as Vision sees that the
   inner TLS 1.3 is established, it stops re-encrypting its records with a
   second layer. The outer connection stays alive, but the data inside it
   goes straight to the lower layer. For this Vision needs access to the
   "raw" connection under the outer TLS, hence the restrictions in §3.3.
3. **Splice on the server.** The server does the same in the reverse
   direction and on Linux hands the copying over to the OS kernel. Almost no
   CPU is spent.

> 🧭 **TL;DR:** the speed is higher than with double encryption, and there is no TLS-in-TLS fingerprint.

## 3.3 What Vision combines with

Vision needs a connection it can unwrap down to the raw layer.
`sing-vmess` keeps a registry of such types (`tlsRegistry`):

- standard `tls.Conn`, uTLS and REALITY over **bare TCP**: yes;
- WebSocket, gRPC, HTTPUpgrade, XHTTP over TLS: **no**. A transport sits
  between Vision and the raw layer, so there is nowhere to forward to. With
  such nodes `flow` must be empty;
- **VLESS `encryption` over any transport: yes** ([SPEC 105](../SPECS/TASKS/105-VISION_OVER_VLESS_ENCRYPTION/SPEC.md)). The
  encryption layer (`*encryption.CommonConn`) plays the role of "TLS" for
  Vision: it is registered in the same registry via `go:linkname`, and
  direct forwarding goes into the connection under the encryption. This
  mirrors Xray, where Vision over `encryption` works with any transport,
  XHTTP included.

Vision is pointless for traffic that is not encrypted itself: such
connections simply stay entirely under the outer layer.

## 3.4 Example: Xray and sing-box-lx

Vision is a single field, and it has the same name on both sides. The full
outbound with REALITY is in §2.8; here is only what concerns the flow.

Xray:

```jsonc
"users": [{
  "id": "00000000-0000-0000-0000-000000000000",
  "flow": "xtls-rprx-vision"
}]
```

sing-box-lx:

```jsonc
"uuid": "00000000-0000-0000-0000-000000000000",
"flow": "xtls-rprx-vision",
"packet_encoding": "xudp"
```

| Xray | sing-box-lx | Where described |
|---|---|---|
| `users[].flow: "xtls-rprx-vision"` | `flow: "xtls-rprx-vision"` | [`flow`](../docs/configuration/outbound/vless.md#flow) |
| `users[].flow` empty or absent | `flow` empty or absent | mandatory for ws / grpc / httpupgrade / xhttp without `encryption`, §3.3 |
| `xtls-rprx-vision-udp443` | not supported | an Xray suffix that forbids UDP on port 443; in our core this is handled by routing rules |
| (xudp is on by default) | `packet_encoding: "xudp"` | [`packet_encoding`](../docs/configuration/outbound/vless.md#packet_encoding); needed for UDP over VLESS, compatible with Vision |
| `streamSettings.network: "tcp"` | no `transport` block | Vision without `encryption` lives only on bare TCP, §3.3 |

> ⚠️ Compatibility rule for sing-box `multiplex`: with `flow` set, it must
> be off. Vision does not survive multiplexing.

> 📖 Normative description: [VLESS outbound in the Project X documentation](https://xtls.github.io/config/outbounds/vless.html).

---

# 4. XHTTP

## 4.1 Why another HTTP transport

The transport decides how to pack the bytes of a proxy connection into the
network. When the server IP is not directly reachable, the only path is
through a middlebox: a CDN or a reverse proxy. The middlebox understands only
HTTP, so the tunnel must look like HTTP requests.

WebSocket and gRPC can do this, but each has its own flaw. WebSocket is
identified by the `Upgrade` header and lives as one long connection. gRPC
requires HTTP/2 along the whole chain and carries characteristic headers.
XHTTP (Xray, late 2024, successor of SplitHTTP) looks like a set of ordinary
GET and POST requests, which a CDN passes without a second thought.

> ⚠️ Xray defines the transport contract. Our client works against an Xray
> server, and a mismatch even in a detail silently breaks the connection: the
> request goes through, data does not flow (feature
> [002](../SPECS/FEATURES/002-XHTTP/FEATURE.md)). Implementation: `transport/v2rayxhttp/`.

## 4.2 Two directions and three modes

The key idea of XHTTP: uplink and downlink are **different HTTP requests**.

```
packet-up / stream-up
  client ──GET /path/<session>──────────► server     downlink: a long response,
         ◄── byte stream down ──────────           like an endless download

  client ──POST /path/<session>/<seq>──► server     uplink (packet-up):
         ──POST /path/<session>/<seq+1>►            chunks in order
  or
  client ──POST /path/<session> (stream)► server    uplink (stream-up):
                                                     one body with no end

stream-one
  client ──POST /path ───────────────► server       one connection:
         ◄── response body = downlink ──            request body up,
         ── request body = uplink ───►              response body down
```

| Mode | When | Cost |
|---|---|---|
| `packet-up` | Behind a CDN that buffers request bodies and cannot do a streaming uplink. Gets through the most paths | Each chunk is a separate request with headers; higher latency and overhead |
| `stream-up` | The middlebox passes streaming bodies | Two long requests per connection |
| `stream-one` | Directly to the server, usually under REALITY | Needs HTTP/2 and no buffering |
| `auto` | Default: with REALITY → `stream-one`, otherwise → `packet-up` | n/a |

The server joins the directions by the session identifier (§4.4). What this
gives beyond getting through: the directions can be sent **along
different paths**. In Xray this is done by `downloadSettings`. Our core does
**not** have this field: the downlink always goes where the uplink goes.

## 4.3 The HTTP version is derived, not set

There is no separate key. The version is computed from the `tls` block by the
Xray rule (`decideHTTPVersion`, [SPEC 104](../SPECS/TASKS/104-XHTTP_HTTP_VERSION_PARITY/SPEC.md)):

- without TLS: HTTP/1.1 over TCP;
- REALITY: always HTTP/2. An `alpn` without `h2` is replaced with `["h2"]`
  with a warning, because REALITY over QUIC does not exist;
- TLS with `alpn: ["h3"]`: HTTP/3 over QUIC/UDP;
- TLS with `alpn: ["http/1.1"]`: HTTP/1.1;
- everything else: HTTP/2.

HTTP/3 sometimes gets through where TCP is throttled, and vice versa. This is
the only lever for choosing between TCP and UDP for XHTTP.

## 4.4 Session, sequence numbers, padding

- **Session identifier**: by default, the path segment after `path`.
  In `stream-one` it is **absent**: the server routes this mode precisely by
  the absence of the segment. `session_placement` moves it to the query, a
  header or a cookie.
- **Packet number** (`seq`) is needed only by `packet-up`: the server
  reassembles the chunks in order, because POSTs through a CDN arrive in any
  order.
- **X-Padding**: a random number of bytes from the `x_padding_bytes` range,
  appended to each request in the query, a header or a cookie according to
  `x_padding_placement`. Without it, requests of the same length would reveal
  the structure of the tunnel.
- `sc_max_each_post_bytes` and `sc_min_posts_interval_ms` set the chunk size
  and the pause between POSTs: tuning the timing for a specific CDN.
- Requests with a body are marked with a gRPC stream header, for parity with
  Xray. `no_grpc_header` removes the mark if the middlebox objects to it.

## 4.5 xmux: a connection pool like a browser's

A browser keeps several HTTP/2 connections to a site and opens new ones from
time to time. A single HTTP/2 connection that lives for hours and carries
hundreds of streams does not look like a browser.

`xmux` describes the pool: how many connections to keep (`max_connections`),
how many streams to allow in one (`max_concurrency`), after how many
requests or seconds to close a connection and open a new one
(`h_max_request_times`, `h_max_reusable_secs`, `c_max_reuse_times`), how
often to send keepalive (`h_keep_alive_period`). Values can be set as a
range, and each connection gets its own random limit.

The reuse limit in our core is counted **in requests, not in streams**.
In `packet-up` one stream produces dozens of POSTs, and counting by streams
would underestimate connection wear many times over ([SPEC 059](../SPECS/TASKS/059-XHTTP_XMUX/SPEC.md)). After a series of failures the pool
trips its breaker ( [SPEC 076](../SPECS/TASKS/076-XHTTP_XMUX_BREAKER/SPEC.md)), so as not to hammer a dead server
with hundreds of requests.

## 4.6 Pitfalls where the connection stays silent

All of them come from field investigations (tasks [043](../SPECS/TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md), [061](../SPECS/TASKS/061-XHTTP_DIAL_DOWNLOAD_DEADLOCK/SPEC.md), [094](../SPECS/TASKS/094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md), [104](../SPECS/TASKS/104-XHTTP_HTTP_VERSION_PARITY/SPEC.md)). They
share one trait: the HTTP request goes through, there is no error, there is
no data.

- **An extra path segment in `stream-one`**: the server serves the downlink
  with a different protocol, and the connection falls apart without
  diagnostics.
- **A lost trailing slash**: the server normalises the path to the form with
  a slash and serves only that prefix. Without the slash, the result is "not
  found" and a hang until timeout.
- **Explicit `packet-up` or `stream-up` against a REALITY server**: Xray on
  `auto` with REALITY picks `stream-one`, and a server configured for this
  pair does not expect another mode.
- **Waiting for the server response while establishing the connection**: in
  modes with a separate downlink the server holds the GET until the first
  uplink packet. If the client waits for the GET response before sending the
  POST, both sides wait for each other
  ([SPEC 061](../SPECS/TASKS/061-XHTTP_DIAL_DOWNLOAD_DEADLOCK/SPEC.md)). The connection is handed to the caller immediately.
- **A local close is not a failure** ([SPEC 094](../SPECS/TASKS/094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md)): when our own client
  closes the connection, the downlink reader sees `context.Canceled`, and
  the core must not count this as a node failure.

The same table by symptom, moved here from the parameter reference:

| Symptom | Likely cause |
|---------|--------------|
| Server replies **`400`** on every request | missing/short `x_padding` — the server enforces the length; check `x_padding_bytes` and that the mode matches the server |
| Server replies **`404`** | `path` prefix mismatch — a truncated trailing slash was the root cause of a real `stream-one` failure (SPEC 043); confirm the exact `path` the server expects |
| `stream-one` dial **hangs until timeout**, no error | a proxy/CDN buffered the response because the gRPC content type was absent — leave `no_grpc_header` **off** (SPEC 042). Conversely, if the server rejects the gRPC type, turn it on |
| Works intermittently, breaks after a while | Xray client/server version skew — XHTTP's wire format changes fast; align versions |
| Server with `alpn: ["h3"]` does not come up, dial times out or fails with `HTTP/3 needs UDP to the server` | HTTP/3 runs over UDP: the `detour` chain must carry UDP, and the path must not drop QUIC. If the server also listens on TCP, drop `h3` from `tls.alpn` to use HTTP/2 |
| Upload payload rejected | `uplink_data_placement: header`/`cookie` used outside `packet-up`, or `uplink_http_method: GET` outside `packet-up` — both are load-time errors, so this shows at start, not at runtime |

## 4.7 Example: Xray and sing-box-lx

VLESS + XHTTP `packet-up` + TLS through a CDN with an `xmux` pool. The variant
with REALITY and `stream-one` is in the reference,
[protocols-transports §1.10](protocols-transports.md#110-examples).

Xray:

```jsonc
{
  "protocol": "vless",
  "settings": {
    "vnext": [{
      "address": "cdn.example.com",
      "port": 443,
      "users": [{ "id": "00000000-0000-0000-0000-000000000000", "encryption": "none" }]
    }]
  },
  "streamSettings": {
    "network": "xhttp",
    "security": "tls",
    "tlsSettings": {
      "serverName": "cdn.example.com",
      "fingerprint": "chrome",
      "alpn": ["h2"]
    },
    "xhttpSettings": {
      "host": "cdn.example.com",
      "path": "/xhttp",
      "mode": "packet-up",
      "extra": {
        "xPaddingBytes": "100-1000",
        "scMaxEachPostBytes": 1000000,
        "scMinPostsIntervalMs": 30,
        "xmux": {
          "maxConcurrency": "16-32",
          "maxConnections": 0,
          "cMaxReuseTimes": 0,
          "hMaxRequestTimes": "600-900",
          "hMaxReusableSecs": "1800-3000",
          "hKeepAlivePeriod": 0
        }
      }
    }
  }
}
```

sing-box-lx:

```jsonc
{
  "type": "vless",
  "tag": "xhttp-cdn",
  "server": "cdn.example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "cdn.example.com",
    "alpn": ["h2"],
    "utls": { "enabled": true, "fingerprint": "chrome" }
  },
  "transport": {
    "type": "xhttp",
    "mode": "packet-up",
    "host": "cdn.example.com",
    "path": "/xhttp",
    "x_padding_bytes": "100-1000",
    "sc_max_each_post_bytes": 1000000,
    "sc_min_posts_interval_ms": 30,
    "xmux": {
      "max_concurrency": "16-32",
      "max_connections": 0,
      "c_max_reuse_times": 0,
      "h_max_request_times": "600-900",
      "h_max_reusable_secs": "1800-3000",
      "h_keep_alive_period": 0
    }
  }
}
```

Key mapping. In Xray some fields live in a nested `extra`. In our core
everything is flat inside `transport`, and names are converted to snake_case.

| Xray | sing-box-lx | Where described |
|---|---|---|
| `network: "xhttp"` | `transport.type: "xhttp"` | [transports §1](protocols-transports.md#1-xhttp-transport) |
| `xhttpSettings.mode` | `transport.mode` | [§1.1 Modes](protocols-transports.md#11-modes), §4.2 |
| `xhttpSettings.host`, `path` | `transport.host`, `path` | [§1.2 Core fields](protocols-transports.md#12-core-fields-v1) |
| `xhttpSettings.headers` | `transport.headers` | same place |
| `extra.xPaddingBytes` | `x_padding_bytes` | [§1.5 X-Padding](protocols-transports.md#15-x-padding-obfuscation-v2), §4.4 |
| `extra.scMaxEachPostBytes`, `scMinPostsIntervalMs` | `sc_max_each_post_bytes`, `sc_min_posts_interval_ms` | [§1.6 Packet-up tuning](protocols-transports.md#16-packet-up-tuning-v2) |
| `extra.noGRPCHeader` | `no_grpc_header` | [§1.2](protocols-transports.md#12-core-fields-v1) |
| `extra.xmux.*` | `xmux.*` (snake_case) | [§1.7 xmux](protocols-transports.md#17-connection-reuse--xmux), §4.5 |
| `extra.downloadSettings` | none | §4.2 |
| `tlsSettings.alpn` | `tls.alpn`, decides the HTTP version | [HTTP version](protocols-transports.md#http-version), §4.3 |
| `tlsSettings.serverName`, `fingerprint` | `tls.server_name`, `tls.utls.fingerprint` | as in §2.8 |
| `security: "reality"` + `realitySettings` | `tls.reality` | as in §2.8; `mode: auto` then gives `stream-one` |
| ranges `"16-32"` | the same strings or a number | [§1.9 Range value forms](protocols-transports.md#19-range-value-forms) |

Xray server fields (`scMaxBufferedPosts`, `scStreamUpServerSecs`,
`noSSEHeader`) are accepted and ignored:
[§1.8](protocols-transports.md#18-accepted-but-ignored-fields).

> 📖 Normative description: [XHTTP in the Project X documentation](https://xtls.github.io/config/transports/xhttp.html).

## 4.8 Upstream position

XHTTP is the only protocol in this document for which upstream has a
**direct refusal**, not a lag. There is no public text with arguments; the
position is read from actions.

- The transport request, [SagerNet/sing-box#3550](https://github.com/SagerNet/sing-box/issues/3550),
  was **deleted** by the maintainer (the API answers `410 This issue was deleted`).
  The fork's [CONSTITUTION](../SPECS/CONSTITUTION.md) and
  [SPEC 002](../SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/SPEC_v1.md) refer to it as the refusal.
- Ready implementations were submitted twice, and both were **closed without
  review**. [PR #3879](https://github.com/SagerNet/sing-box/pull/3879) (kindestone,
  XHTTP + KCP + mieru, 92 files) was closed two minutes after it was opened,
  on 2026-03-09. [PR #4326](https://github.com/SagerNet/sing-box/pull/4326)
  (flyzstu, XHTTP only: all modes, XMUX, tests, bilingual documentation,
  28 files) was closed after 46 minutes, on 2026-07-22. Zero comments and
  zero reviews on both.
- Upstream has no extension mechanism: the v2ray transport dispatcher is a
  hard `switch` on type in `transport/v2ray/transport.go`. A transport cannot
  be added without touching the upstream file, hence the `// lx` seam and the
  `with_xhttp` build tag.

> 🔀 Consequence for the ecosystem: XHTTP exists only in forks: ours,
> [starifly/sing-box](https://github.com/starifly/sing-box) (the NekoBox+ core)
> and [shtorm-7/sing-box-extended](https://github.com/shtorm-7/sing-box-extended).
> [lx-reference-cores](lx-reference-cores.md) describes them as cross-checks.
> There is nowhere to merge the transport back into, so the fork is
> permanent.

AmneziaWG got the same refusal, in the same way:
[SagerNet/sing-box#4045](https://github.com/SagerNet/sing-box/issues/4045)
was closed by the maintainer as `not planned` on 2026-04-30 with a single
comment that pointed the author to Hiddify and Karing. This is outside the
scope of this document, but it shows that the refusal is not about XHTTP in
particular. It covers Xray-specific and Amnezia-specific transports as a
class.

---

# 5. VLESS encryption: the post-quantum layer

## 5.1 The threat: record now, decrypt later

All asymmetric cryptography in TLS rests on two problems: integer
factorisation (RSA) and the discrete logarithm on elliptic curves (X25519,
ECDSA, Ed25519). Shor's algorithm on a large enough quantum computer solves
both. Symmetric ciphers suffer less: Grover's algorithm only halves the
effective strength, and AES-256 stays safe.

Such a computer does not exist yet. But traffic can be recorded today and
opened when it appears. For VPN traffic this is not a theoretical threat:
recording it is cheap, and its content stays sensitive for a long time. This
is exactly what is called **harvest now, decrypt later**.

## 5.2 What ML-KEM is and why a hybrid

In August 2024 NIST approved post-quantum standards:

- **ML-KEM** (FIPS 203, formerly Kyber): lattice-based key exchange.
  Levels 512, 768, 1024. An ML-KEM-768 public key is 1184 bytes, a
  ciphertext is 1088 bytes. This is *encapsulation*: the sender uses someone
  else's public key to obtain a shared secret and a ciphertext, and the
  recipient recovers the secret with its private key. There is no "key for
  key" exchange as in Diffie-Hellman.
- **ML-DSA** (FIPS 204, formerly Dilithium): lattice-based signatures. It is
  not needed for key exchange. In Xray it signs the REALITY certificate; our
  core does not implement it (§2.6).

New algorithms are not fully trusted: their mathematics has not been studied
for long. So everyone uses a **hybrid**: X25519 and ML-KEM-768 at once, with
the shared secret derived from both. To open a connection, both have to be
broken. This is how X25519MLKEM768 in TLS works (§1.3), and this is how
`mlkem768x25519plus` works.

## 5.3 Where the layer lives

```
VLESS client (header, UUID, command, Vision)
        │
        ▼
encryption.CommonConn ── mlkem768x25519plus          ◄── this layer
        │
        ▼
transport: tcp / ws / grpc / xhttp
        │
        ▼
tls: may or may not be present
```

`protocol/vless/outbound.go` wraps the transport connection in
`*encryption.CommonConn` before the VLESS client starts writing. The layer
depends neither on TLS nor on REALITY. Such nodes often come with
`security=none`, because they do not need outer TLS: the encryption is
already inside.

> 🧭 **Why, when there is TLS already.** A middlebox can strip TLS: a CDN terminates
> it on its side and sees the payload in the clear. The inner layer passes
> through the middlebox untouched and reaches the server.

The server half (`decryption` in Xray) is intentionally not ported: the fork
is client-side. The client implementation is `protocol/vless/encryption/`,
the string parser is `protocol/vless/lx_encryption.go`.

> 🔀 **Vanilla differs.** The symptom this layer fixes: a server with `decryption` silently drops ordinary
> VLESS. The transport comes up (WS answers `101`, gRPC sends SETTINGS), and
> then the peer closes the connection without a single log line. Before [SPEC 032](../SPECS/TASKS/032-VLESS_ENCRYPTION_MLKEM768/SPEC.md) such
> nodes did not work in any sing-box-based client. The upstream position is
> the same as for XHTTP (§4.8): the request
> [SagerNet/sing-box#4179](https://github.com/SagerNet/sing-box/issues/4179)
> was deleted, and the question [#3599](https://github.com/SagerNet/sing-box/issues/3599)
> was closed as `not planned` on 2026-01-04 with the answer "Not supported"
> from a third-party participant. There is no word from the maintainer in
> either. The layer was taken from
> [starifly/sing-box](https://github.com/starifly/sing-box), see
> [lx-reference-cores](lx-reference-cores.md).

## 5.4 Handshake: two keys, one secret

After the mode, the config string contains the **server's public keys**:
X25519 (32 bytes) or ML-KEM-768 (1184 bytes), one or several. These are
long-lived keys, called **NFS** (non-forward-secret). They do not change from
connection to connection, and their future compromise is dangerous. So they
are used only for the first step.

```
client                                            server
  │
  │ 1. for each server NFS key:
  │    X25519  → ECDH with an ephemeral pair → nfsKey
  │    ML-KEM  → encapsulation → nfsKey + ciphertext
  │    (relay = public key or ciphertext)
  │
  │ 2. ephemeral PFS pair for this connection:
  │    ML-KEM-768 (1184 bytes) + X25519 (32 bytes)
  │
  │──── IV · relays · AEAD(nfsKey){PFS public keys} · padding ────────►
  │
  │                               3. the server encapsulates to the PFS keys,
  │                                  replies with its own PFS halves
  │◄─── AEAD(nfsKey){PFS reply} · ticket · padding ────────────────────
  │
  │ 4. pfsKey = ML-KEM secret (32) ‖ X25519 secret (32)
  │    unitedKey = pfsKey ‖ nfsKey
  │    AEAD(unitedKey): the working cipher of the connection
  │
  │═══ then the VLESS stream, record by record ═════════════════════════
```

- **The NFS key** protects the first packet and authenticates the server:
  whoever does not hold the private half cannot read the PFS exchange or
  answer it.
- **PFS keys** (perfect forward secrecy) are ephemeral: a new pair for every
  connection, thrown away after it. Recorded traffic cannot be opened even
  by someone who later obtains the server's private NFS key.
- Both exchanges are hybrid: both NFS and PFS contain ML-KEM-768 and X25519.
  The working key depends on all four secrets.
- AEAD is AES-GCM if the CPU has hardware AES, otherwise
  ChaCha20-Poly1305.

## 5.5 0-RTT and tickets

The `1rtt` mode runs the full handshake above on every connection: one
round trip before the first data.

The `0rtt` mode allows skipping it on reconnect. In its reply the server
sends a **ticket** (16 bytes) and its lifetime in seconds. The client stores
the ticket together with `pfsKey`. The next connection within the lifetime
carries the ticket instead of the PFS exchange, and the server recovers the
same `pfsKey` from the ticket. Data goes right after the first packet. The
ticket is reused until it expires, but the NFS step (§5.4, item 1) runs
again on every connection, so each connection has its own `unitedKey`.
Protection against replay of the first packet is on the server side.

> 🧭 **The cost of 0-RTT:** several connections share one `pfsKey` while the ticket is alive.
> The field form on the servers seen so far is `native.0rtt`.

## 5.6 Wire appearance and padding

The second segment of the string sets how the layer looks from the outside:

- `native`: records are formatted as TLS 1.3 `application_data`. Without
  outer TLS the traffic looks like TLS; over TLS, like TLS inside TLS (this
  is where Vision comes in, §3.3);
- `xorpub`: the same, but the relays of the first packet are additionally
  masked with a stream cipher keyed by the server's public key. An X25519
  public key and an ML-KEM ciphertext have a recognisable structure, and
  after XOR they look like random bytes;
- `random`: on top of `xorpub`, the whole connection is wrapped in
  `XorConn`. A stream cipher keyed by `unitedKey` is applied to every
  record, and no TLS-like headers remain visible from the outside. The
  stream is fully random.

Padding and delays (`100-111-1111.75-0-111`: probability, from, to) add
random bytes to the first packet and send them **in chunks with pauses**, so
that the handshake has no constant length and timing. The server can also
send its padding slowly. This makes sense only for `1rtt`: in `0rtt` there
is no handshake, and padding is rejected during config validation.

An unparsed string is an error at `check` that names the segment. The layer
is never silently disabled: otherwise the user would think the traffic is
protected when it is not.

## 5.7 Popular misconceptions

- **«This is post-quantum REALITY.»** No. REALITY protects the TLS handshake
  and the server's selection of its own clients. `encryption` protects the
  payload. They are enabled together or separately, and neither depends on
  the other.
- **«I have `chrome` with X25519MLKEM768, so the traffic is already post-quantum.»**
  The hybrid key share in the ClientHello (§1.3) protects the key of the
  outer TLS, which a CDN terminates on its side. `encryption` protects the
  key of the inner layer, which reaches the server. The primitive is the
  same, ML-KEM-768, but the keys and layers differ.
- **«With `encryption`, Vision is not needed.»** It is needed for the same
  reason as over TLS: someone else's TLS handshake still travels inside the
  tunnel, and its shape is visible. Vision over `encryption` works on any
  transport (§3.3).
- **«`security=none` in a link means the node is unencrypted.»** It only
  means that there is no outer TLS. If the link has `encryption`, the
  encryption lives inside VLESS (§5.3).

## 5.8 Example: Xray and sing-box-lx

VLESS + `encryption` over WebSocket without outer TLS: a typical subscription
node with `security=none`, because the encryption is already inside.

Xray:

```jsonc
{
  "protocol": "vless",
  "settings": {
    "vnext": [{
      "address": "203.0.113.20",
      "port": 80,
      "users": [{
        "id": "00000000-0000-0000-0000-000000000000",
        "encryption": "mlkem768x25519plus.native.0rtt.<base64url ML-KEM-768 key>"
      }]
    }]
  },
  "streamSettings": {
    "network": "ws",
    "security": "none",
    "wsSettings": { "path": "/ws" }
  }
}
```

sing-box-lx:

```jsonc
{
  "type": "vless",
  "tag": "pq-ws",
  "server": "203.0.113.20",
  "server_port": 80,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ML-KEM-768 key>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

| Xray | sing-box-lx | Where described |
|---|---|---|
| `users[].encryption: "mlkem768x25519plus…"` | `encryption`, a flat field next to `uuid` | [lx-config §6](lx-config.md#6-vless-encryption--post-quantum-layer-spec-032), §5.4–§5.6 |
| `users[].encryption: "none"` or empty | field absent, empty or `"none"` | layer off, §5.6 |
| `security: "none"` | no `tls` block | outer TLS not needed, §5.3 |
| `security: "tls"` / `"reality"` | `tls` as in §2.8 | two independent layers, §5.7 |
| `users[].flow: "xtls-rprx-vision"` | `flow` | works over `encryption` on any transport, §3.3 |
| `network: "ws"` + `wsSettings` | `transport.type: "ws"` + `path` | [WebSocket](../docs/configuration/shared/v2ray-transport.md#websocket); any transport, `xhttp` included (§4.7) |
| server-side `decryption` | none | not ported, §5.3 |

> ⚠️ The `encryption` string is carried over **verbatim**: the appearance,
> mode and padding segments and the keys are the same in both cores, and no
> names are translated. A client config builder that drops this field leaves
> the node dead without a single log line.

> 📖 Normative description: [VLESS outbound in the Project X documentation](https://xtls.github.io/config/outbounds/vless.html).

---

# 6. How the layers stack

| Combination | Works | Remark |
|---|---|---|
| VLESS + Vision + REALITY, bare TCP | yes | the classic combination, maximum speed |
| VLESS + Vision + plain TLS, bare TCP | yes | REALITY is not required for Vision |
| VLESS + Vision + ws / grpc / xhttp | **no** | a transport sits between Vision and TLS; `flow` must be empty |
| VLESS + XHTTP + REALITY | yes | `auto` → `stream-one`, always HTTP/2 |
| VLESS + XHTTP + TLS through a CDN | yes | `packet-up`, `h3` optional |
| VLESS + `encryption` + any transport, no TLS | yes | `security=none`; the encryption is inside |
| VLESS + `encryption` + Vision + any transport | yes | [SPEC 105](../SPECS/TASKS/105-VISION_OVER_VLESS_ENCRYPTION/SPEC.md); Vision forwards into the layer under the encryption |
| VLESS + `encryption` + REALITY | yes | two independent layers, two hybrid exchanges |
| sing-box `multiplex` + Vision | no | Vision cannot multiplex |
| sing-box `multiplex` + XHTTP | not needed | XHTTP has its own `xmux` |
| `tls.reality` + `tls.ech` | no | REALITY replaces the handshake entirely |

Typical stacks, from simple to cautious:

1. **Direct access to the server.** VLESS + Vision + REALITY on `chrome`.
   Against active server probing and TLS-in-TLS. The hybrid key share is
   already in the preset.
2. **Server IP not directly reachable.** VLESS + XHTTP `packet-up` + TLS through a CDN.
   Without Vision (it cannot be used there), with `xmux`. If the server requires
   `encryption`, it is added, and the outer TLS can then be dropped.
3. **Maximum.** VLESS + `encryption` + Vision + XHTTP + REALITY.
   Post-quantum protection of the payload and of the outer handshake. Vision
   removes TLS-in-TLS, XHTTP gives the shape of web traffic, REALITY covers
   active probing.

---

# 7. Differences from vanilla sing-box

## 7.1 Why the base is sing-box, not Xray

If Xray defines the protocols and the fork is always catching up with it, the
natural question is: why not take Xray as a whole? The answer is that Xray
and sing-box are different classes of software.

**Xray is a protocol core.** Its strength is VLESS, REALITY, Vision, XHTTP
and everything described in this document. Around them there is a minimum:
routing by domain and IP, simple DNS, outbounds for VMess, Trojan,
Shadowsocks, WireGuard. Xray has no TUN stack suitable for a mobile client.
Apps built on it put a separate tun2socks next to it and stitch it together
with `VpnService`, the lifecycle and statistics themselves.

**sing-box is a platform.** Protocols are one of its parts, along with:

- a **TUN stack** (system and gVisor) with the full interface lifecycle,
  routes, and auto-detection of the default interface;
- a **routing engine** with rules by process, packet and domain, rule-sets
  from remote sources, and protocol sniffing;
- a **DNS engine** with multiple servers, strategies and rules;
- **protocols that Xray does not have and will not have**: WireGuard and
  AmneziaWG endpoints, Tailscale, MASQUE and WARP, Hysteria2, TUIC, ShadowTLS, SSH, naive;
- **`libbox`**: a ready binding for Android and iOS with the service
  lifecycle, CommandClient for statistics and control, and direct integration
  with `VpnService`.

For a client that needs VLESS subscriptions, WireGuard and AmneziaWG, WARP
and Tailscale at the same time, in one process and one TUN, Xray cannot serve as
the base at all: half of this would have to be written from scratch.

**The delta is asymmetric.** The client halves of the Xray protocols are a
transport (`transport/v2rayxhttp/`), an encryption layer
(`protocol/vless/encryption/`) and a few seams in the TLS client. These are
dozens of files that fit into the sing-box architecture with its transport
and outbound registries behind build tags. The reverse, bringing the
sing-box platform into Xray, is not feasible and nobody needs it. So the
port goes towards sing-box, and Xray remains the **reference**: the normative
source for each protocol is its source code, not its documentation
([lx-reference-cores.md](lx-reference-cores.md)).

> 🧭 **The cost of the choice** is plainly visible in this document. The
> protocols change on the Xray side, and the fork has to catch up with every
> server tightening (§2.6), otherwise subscription nodes silently die. This
> is ongoing work, not a one-off port.

## 7.2 How the fork delta is structured

All downstream code lives either in its own files or in seams of upstream
files marked `// lx`, behind build tags. A build without tags is upstream
byte for byte. Each `upstream/stable` release is merged within days.
Hotfixes for upstream bugs have a removal condition (the
[004-HOTFIXES](../SPECS/FEATURES/004-HOTFIXES/FEATURE.md) registry). Where an
upstream dependency does not provide what is needed, it is replaced by a fork
submodule. For this document that is `utls-lx` (§1.2).

## 7.3 Summary by protocol

"Vanilla" means SagerNet/sing-box at the version from `upstream.version`.

| What | Vanilla sing-box | sing-box-lx | Where |
|---|---|---|---|
| **XHTTP** | No transport: only `ws`, `grpc`, `httpupgrade`, `http`, `quic`. A node with `xhttp` does not load | Native `xhttp` transport behind `with_xhttp`: all modes, HTTP version by the Xray rule, `xmux`, padding, session placement | §4, [002](../SPECS/FEATURES/002-XHTTP/FEATURE.md) |
| **REALITY: hybrid key share** | Strips `X25519MLKEM768` from the ClientHello (a workaround for old uTLS, [SagerNet/sing-box#4520](https://github.com/SagerNet/sing-box/issues/4520)). Against Xray ≥ v26.9.8 every node ends up on the cover site | Sends what the preset carries; `AuthKey` from the key the server will choose | §2.6, [083](../SPECS/TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md) |
| **REALITY: client version** | An outdated constant; a server with `minClientVer` rejects it | Exactly the required minimum | [053](../SPECS/TASKS/053-REALITY_MIN_CLIENT_VER/SPEC.md) |
| **REALITY: `key_share`** | No such field | `classical` / `hybrid` per node, for networks that lose a two-segment ClientHello | §1.3, [089](../SPECS/TASKS/089-REALITY_KEY_SHARE_OPTION/SPEC.md) |
| **REALITY: fragmentation** | `fragment` / `record_fragment` leave REALITY untouched | Apply to REALITY too; under `detour`, `record_fragment` turns on by itself | §1.4, [088](../SPECS/TASKS/088-REALITY_FRAGMENT_BYPASS/SPEC.md), [060](../SPECS/TASKS/060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) |
| **uTLS fingerprints** | `metacubex/utls`: hybrid share only in `chrome` | Fork `utls-lx`: adds `firefox` (Firefox 148) and `safari` (Safari 26.3) with the hybrid, and the opt-in `chrome_155` (Chrome 155) | §1.2, [086](../SPECS/TASKS/086-UTLS_FORK_FIREFOX148/SPEC.md), [087](../SPECS/TASKS/087-UTLS_SAFARI_26_3/SPEC.md), [118](../SPECS/TASKS/118-UTLS_CHROME_155/SPEC.md) |
| **Vision** | Present (`sing-vmess`), only over TLS/REALITY on bare TCP | The same, plus over VLESS `encryption` on any transport | §3.3, [105](../SPECS/TASKS/105-VISION_OVER_VLESS_ENCRYPTION/SPEC.md) |
| **VLESS `encryption`** | None; the field is rejected as unknown, and nodes with it are dead across the whole sing-box ecosystem | The client half of `mlkem768x25519plus`: all wire appearances, `0rtt`/`1rtt`, padding | §5, [012](../SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) |
| **VLESS `decryption`** (server) | None | None, intentionally: the fork is client-side | §5.3 |
| **ML-DSA-65 in REALITY**, `spiderX` | None | None, outside the feature | §2.6 |
| **XHTTP `downloadSettings`** | No transport | No field: the downlink takes the same path as the uplink | §4.2 |

> 🔀 **Common denominator:** vanilla sing-box is enough for a REALITY server
> on old Xray and for WS/gRPC through a CDN. Any subscription with XHTTP, with
> `encryption`, or with a server on Xray ≥ v26.9.8 silently fails in it,
> without a log line, because REALITY does not explain rejections and a
> server with `decryption` simply closes the connection.

---

# 8. Glossary

| Term | Meaning |
|---|---|
| **Middlebox** | Any equipment between client and server that looks at traffic or interferes with it: classifier, reverse proxy, CDN |
| **DPI** | Deep packet inspection: equipment that classifies traffic by shape and content without having the keys |
| **Active server probing** | A middlebox connects to a server from its list by itself and looks at the answer |
| **SNI** | Server Name Indication, the server name in the plaintext ClientHello |
| **Fingerprint** | The set and order of ClientHello fields by which a library or browser is recognised |
| **uTLS** | A library that builds the ClientHello from a browser preset |
| **key_share** | A ClientHello extension with the public halves of the key exchange |
| **X25519** | Classical elliptic-curve key exchange; 32-byte keys |
| **ML-KEM-768** | Post-quantum key encapsulation (FIPS 203, formerly Kyber); 1184-byte key, 1088-byte ciphertext |
| **ML-DSA-65** | Post-quantum signature (FIPS 204, formerly Dilithium); not implemented in our core |
| **Hybrid** | Two key exchanges of different nature with one derived secret |
| **PFS** | Perfect forward secrecy: compromise of long-lived keys does not open past traffic |
| **NFS key** | The server's long-lived public key in the `encryption` string |
| **0-RTT / 1-RTT** | How many round trips before the first data: zero with a ticket, one with a full handshake |
| **AuthKey** | The REALITY key derived from ECDH of the client's ephemeral key and the server's public key |
| **short_id** | 8 bytes in the REALITY session id by which the server selects its clients |
| **dest / cover site** | The real site the REALITY server uses to answer outsiders |
| **TLS-in-TLS** | A nested handshake inside a tunnel, visible to DPI by record lengths |
| **Splice** | Copying between sockets inside the OS kernel without passing through the process |
| **CDN** | A middlebox that terminates TLS and forwards HTTP to the server |
| **xmux** | The XHTTP pool of HTTP connections with reuse limits |

---

## See also

- **[lx-config.md](lx-config.md)**: field overview by feature: §6 VLESS
  `encryption`, §7 REALITY `key_share`, §9 fragmentation.
- **[protocols-transports.md](protocols-transports.md)**: §1
  XHTTP: every field, default and error.
- Feature specs: [017-REALITY](../SPECS/FEATURES/017-REALITY/FEATURE.md),
  [002-XHTTP](../SPECS/FEATURES/002-XHTTP/FEATURE.md),
  [012-VLESS_ENCRYPTION](../SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md).
- Key tasks: [083](../SPECS/TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md)
  (hybrid key share in REALITY),
  [089](../SPECS/TASKS/089-REALITY_KEY_SHARE_OPTION/SPEC.md) (`key_share`),
  [032](../SPECS/TASKS/032-VLESS_ENCRYPTION_MLKEM768/SPEC.md) (encryption
  layer), [105](../SPECS/TASKS/105-VISION_OVER_VLESS_ENCRYPTION/SPEC.md)
  (Vision over encryption), [059](../SPECS/TASKS/059-XHTTP_XMUX/SPEC.md)
  (`xmux`), [104](../SPECS/TASKS/104-XHTTP_HTTP_VERSION_PARITY/SPEC.md)
  (HTTP version).
- Code: `common/tls/reality_client.go`, `common/tls/utls_client.go`,
  `transport/v2rayxhttp/`, `protocol/vless/encryption/`,
  `protocol/vless/lx_encryption.go`.
