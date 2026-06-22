---
icon: material/alert-decagram
---

# lx changelog

Changes in the `sing-box-lx` fork (the `lx` features layered on top of upstream
sing-box). Upstream's own changelog is in [changelog.md](changelog.md); this file
tracks only the fork. Versions are tagged `vX.Y.Z-lx.N`; releases are built by
`lx-release.yml`.

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
