# sing-box-lx — every fork feature field in one config

> RU: [lx-config-example.ru.md](lx-config-example.ru.md)

One config carries every field of the outbound-side features: XHTTP transport,
AmneziaWG 2.0 endpoint, the `id`/`ip`/`ib` masquerade sugar, VLESS `encryption`, the
`urltest` `round_robin` balancer and the root `lx` block with every key. MASQUE and the DNS group have
their own examples in [§4](lx-config.md#4-masque-outbound--cloudflare-warp) and [§5](lx-config.md#5-dns-server-group).
The `lx` block is described in [lx-block.md](lx-block.md), its energy keys in [lx-energy.md](lx-energy.md).

This is a kitchen-sink reference, not a recommended config. Many fields are mutually exclusive
(the `id`/`ip`/`ib` sugar and a hand-written `i1`) or server-only and ignored by the client;
those fields are flagged in the comments. For a working setup, copy only the block
you need and read its document. Each comment shows the default and the allowed values.

```jsonc
{
  // ─────────────────────────────────────────────────────────────────────────
  // Root `lx` block (§13) — the fork's global knobs, grouped by subsystem.
  // lx.wg.idle acts only with the with_lx_idle_suspend build tag (mobile AAR);
  // a desktop binary refuses it at start. The rest of lx.wg works in every build.
  // ─────────────────────────────────────────────────────────────────────────
  "lx": {
    "wg": {
      "idle": {                                 // with_lx_idle_suspend builds only
        "suspend": "30s",                       // default: off. Endpoint outside the routing tree → Down after this idle
        "suspend_reachable": "5m",              // default: off. Same for endpoints in the tree; >= suspend, requires it
        "teardown": "10m",                      // default: = suspend_reachable. Sleep before full teardown; "0" = never
        "lazy_build": true,                     // default: false. Build the device on first dial (SPEC 097)
        "build_max": 4,                         // default: 0 (no cap). Max devices built at once (SPEC 097)
        "build_overflow": "wait"                // default: wait. wait | build (SPEC 097)
      },
      "buffers": {                              // any build (SPEC 158)
        "read_datagram_count": 8,               // default: 128. 1–128 datagrams reserved per socket read
        "send_by_mtu": true                     // default: true. false = 64 KiB stack-read buffers (diagnostics)
      },
      "connected_socket": true                  // default: false. Connect the tunnel socket in single-peer mode (SPEC 160)
    },
    "masque": {
      "idle_timeout": "5m"                      // default: off. Global idle window for masque nodes without their own key
    },
    "trusttunnel": {
      "idle_timeout": "5m"                      // default: 5m. Global idle window for trusttunnel nodes without their own key; only a node's "0" turns it off (SPEC 133)
    },
    "detour": {                                 // default: true (no key = true) = clamp + every strip filter; false = upstream behaviour (§13, SPEC 165)
      "mtu_align": "clamp",                     // default: clamp. off | fill | clamp
      "strip": {                                // default: the catalog. true | false | {"filters": …}
        "filters": {                            // an object replaces the catalog: an unnamed filter is off
          "tls": { "fragment": true, "spoof": true, "kernel": true, "engine": true }
        }
      },
      "except_outbounds": []                    // default: none. Tags neither aligned nor filtered; an unknown tag is a load error
    },
    "sniff_guard": {                            // default: no block = upstream behaviour. Guards the handshake against rerouting by SNI on a relay or router (§13, SPEC 060)
      "nodes": {
        "sni_mismatch": true,                   // default: true — only nodes whose SNI differs from server. false — no filter
        "outbound_type": ["vless", "masque"]    // default: no filter. A list of types or false; true, [] and an unknown type are load errors
      },
      "detour_type": {                          // default: {"direct": "clean", "ssh": "clean", "socks": "clean", "*": "suspect"}
        "direct": "clean",                      //   carrier type → never | clean | suspect; "*" — every other type; groups cannot be keys
        "*": "suspect"
      },
      "override": false,                        // default: false. true — the block wins over a node's fragment / record_fragment
      "tls": {
        "method": "record_fragment",            // default: off. record_fragment | fragment | delay | off
        "delay": "300ms",                       // default: 300ms. Pause of the delay method; > 0
        "persist": true,                        // default: false. Relay labels in experimental.cache_file
        "detour_type": { "ssh": "suspect" }     // default: the shared map. Override for TLS
      },
      "quic": {
        "method": "decoy_first_byte",           // default: off. decoy_first_byte | delay | off
        "delay": "300ms",                       // default: 300ms
        "persist": true,                        // default: false
        "detour_type": { "socks": "never" }     // default: the shared map. Override for QUIC
      }
    }
  },

  "outbounds": [
    // ─────────────────────────────────────────────────────────────────────────
    // XHTTP transport (§1) — attaches to a VLESS / VMess / Trojan outbound.
    // Needs the with_xhttp build tag.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "vless",
      "tag": "xhttp-out",
      "server": "example.com",
      "server_port": 443,
      "uuid": "00000000-0000-0000-0000-000000000000",
      "encryption": "",                         // default: "" (off). VLESS post-quantum layer (§6):
                                                //   "mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>….<key>"
      "tls": {
        "enabled": true,
        "server_name": "example.com",
        "utls": { "enabled": true, "fingerprint": "chrome" },
        "reality": {
          "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd",
          "key_share": ""                       // default: "" (as the fingerprint carries it). §7:
                                                //   "classical" = strip X25519MLKEM768 (Xray < v26.9.8 only)
                                                //   "hybrid"    = require it (error on edge/ios/…)
        }
      },
      "transport": {
        "type": "xhttp",                        // selector — must be "xhttp"

        // ── core (v1) ──
        "mode": "auto",                         // auto | packet-up | stream-up | stream-one.
                                                //   auto → stream-one on Reality, else packet-up
        "host": "example.com",                  // default: TLS SNI / server. Overrides Host header
        "path": "/xhttp",                       // default: "" (root). Session id / seq appended as path segments
        "headers": { "X-Foo": "bar" },          // default: none. Extra headers on every request
        "x_padding_bytes": "100-1000",          // default: "100-1000". "min-max" or single int — padding length
        "no_grpc_header": false,                // default: false. Omit Content-Type: application/grpc on stream-one/stream-up

        // ── session / seq placement (v2) ──
        "session_placement": "path",            // default: path. path | query | header | cookie
        "session_key": "",                      // default: X-Session (header) / x_session (query|cookie); unused for path
        "seq_placement": "path",                // default: path. path | query | header | cookie (packet-up)
        "seq_key": "",                          // default: X-Seq (header) / x_seq (query|cookie); unused for path
        "session_table": "",                    // default: unset (dashed UUID id). Alphabet name or literal ASCII set
        "session_length": "",                   // default: unset. "min-max" or "n"; set together with session_table

        // ── uplink-data placement (v2, packet-up) ──
        "uplink_data_placement": "auto",        // default: auto (== body). body | auto | header | cookie.
                                                //   header/cookie ONLY valid in packet-up
        "uplink_data_key": "",                  // default: X-Data (header) / x_data (cookie); "" for body
        "uplink_chunk_size": "",                // default: cookie 2048-3072 / header 3000-4000 / else = sc_max_each_post_bytes.
                                                //   "min-max" base64 chars, min floored to 64
        "uplink_http_method": "POST",           // default: POST. Upper-cased. GET only in packet-up

        // ── X-Padding obfuscation (v2) — all below apply only when obfs_mode=true ──
        "x_padding_obfs_mode": false,           // default: false. Master switch. false → legacy Referer x_padding
        "x_padding_placement": "queryInHeader", // default: queryInHeader. cookie | header | query | queryInHeader
        "x_padding_key": "x_padding",           // default: x_padding. Cookie/query param name
        "x_padding_header": "X-Padding",         // default: X-Padding. Header name (header / queryInHeader placement)
        "x_padding_method": "repeat-x",         // default: repeat-x. repeat-x ('X'*N) | tokenish (HPACK-tuned base62)

        // ── packet-up tuning (v2) ──
        "sc_max_each_post_bytes": "1000000-1000000", // default: "1000000-1000000". POST split threshold ("min-max")
        "sc_min_posts_interval_ms": "30-30",    // default: "30-30". Anti-burst delay between POSTs, ms ("min-max")

        // ── accepted but IGNORED by the client (config/link symmetry only) ──
        "sc_max_concurrent_posts": 0,           // IGNORED — legacy Xray knob; client serializes one POST in flight
        "server_max_header_bytes": 0,           // IGNORED — server-only (http.Server.MaxHeaderBytes)
        "no_sse_header": false,                 // IGNORED — server-only (SSE Content-Type on downlink)
        "sc_max_buffered_posts": 0,             // IGNORED — server-only (upload reorder buffer depth)
        "sc_stream_up_server_secs": ""          // IGNORED — server-only (stream-up keepalive interval)
      }
    },

    // ─────────────────────────────────────────────────────────────────────────
    // AmneziaWG 2.0 endpoint (§2) — a wireguard endpoint with obfuscation.
    // Needs the with_awg build tag. All AWG fields sit at the endpoint ROOT
    // (none on a peer), mirroring an awg-quick .conf [Interface] section.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "wireguard",
      "tag": "awg-out",
      "system": false,
      "mtu": 1280,                              // lower than plain WG (s4 transport junk); core defaults 1280 if s4 set
      "address": ["10.0.0.2/32"],
      "private_key": "<client-private-key-base64>",

      // ── junk packets before the handshake ──
      "jc": 10,                                 // default: 0 (unset). Count of junk packets
      "jmin": 50,                               // default: 0. Min size of each junk packet
      "jmax": 100,                              // default: 0. Max size of each junk packet (keep below path MTU)

      // ── handshake-message junk (s1/s2) + AWG 2.0 junk-size (s3/s4) ──
      "s1": 20,                                 // default: 0. Junk prepended to the handshake INIT message
      "s2": 20,                                 // default: 0. Junk prepended to the handshake RESPONSE message
      "s3": 60,                                 // default: 0. AWG 2.0 junk-size param
      "s4": 60,                                 // default: 0. AWG 2.0 junk-size param

      // ── magic headers: single uint32 (AWG 1.x) OR "min-max" range (AWG 2.0) ──
      // ranges must NOT overlap; 0 / "" = unset (counts as the WG default 1/2/3/4)
      "h1": 1234567890,                         // or "43613244-384550127"   — WG message type 1
      "h2": 1234567891,                         // or "826869626-2105069164" — type 2
      "h3": 1234567892,                         // or "2124774725-2141151992" — type 3
      "h4": 1234567893,                         // or "2144594503-2146278491" — type 4

      // ── CPS decoy packets i1..i5 (case-sensitive; sent in order pre-handshake) ──
      // tags: <b 0xHEX> bytes, <c> counter, <t> timestamp, <r N> random bytes,
      //       <rc N> random chars, <rd N> random digits.
      // i1 is MUTUALLY EXCLUSIVE with the id/ip/ib sugar below — use one or the other.
      "i1": "<b 0x000100002112a442><r 12>",     // default: "". CPS packet 1
      "i2": "<b 0x010100002112a442><r 12>",     // default: "". CPS packet 2
      "i3": "<r 24>",                            // default: "". CPS packet 3
      "i4": "",                                  // default: "". CPS packet 4
      "i5": "",                                  // default: "". CPS packet 5

      // ── masquerade sugar (§2 → id/ip/ib) — generates i1 for you.
      //    MUTUALLY EXCLUSIVE with an explicit i1 above. Shown here for reference;
      //    in a real config use EITHER i1..i5 OR id/ip/ib, not both. ──
      "id": "www.google.com",                   // default: "". Masquerade domain (SNI/QNAME/SIP host). Required for ip=quic
      "ip": "quic",                             // default: "". quic | dns | stun | sip
      "ib": "chrome",                           // default: "". chrome | firefox | curl. Only meaningful with ip=quic

      // ── AmneziaWG 3.x (amnezia-awg2 container, protocol_version 3.x; SPEC 080) ──
      // header_protection_key is SERVER-SIDE (must equal the server's); with it on,
      // every s1..s4 must be >= 12 (the padding carries the header-cipher nonce).
      "header_protection_key": "<base64-32-bytes>", // default: "" (off). `awg genkey` output; masks type/receiver/counter of every packet
      "content_padding_addition": "10-100",     // default: "" (off). int | "min-max" extra padding inside every data packet
      "rekey_after_time": "100-120",            // default: "" (WireGuard 120 s). int | "min-max" seconds
      "rekey_timeout": "3-7",                   // default: "" (WireGuard 5 s)
      "reject_after_time": "150-180",           // default: "" (WireGuard 180 s)
      "keepalive_timeout": "5-15",              // default: "" (WireGuard 10 s)
      "max_handshake_attempts": "15-20",        // default: "" (WireGuard 18)
      "random_trailers": true,                  // default: false. Random-length tail on every handshake message / inside data packets
      "disable_cookies": true,                  // default: false. Never send or demand cookie replies

      "peers": [
        {
          "address": "server.example.com",
          "port": 51821,
          "public_key": "<server-public-key-base64>",
          "pre_shared_key": "<preshared-key-base64>",
          "allowed_ips": ["0.0.0.0/0", "::/0"],
          "persistent_keepalive_interval": "25-35" // number (WireGuard) or "min-max" seconds (AWG 3.x)
        }
      ]
    },

    // ─────────────────────────────────────────────────────────────────────────
    // urltest round_robin load balancing (§3). Config fields always available;
    // the GetPool client method needs with_lx_command.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "urltest",
      "tag": "auto",
      "outbounds": ["xhttp-out", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
      "url": "https://www.gstatic.com/generate_204",
      "interval": "15m",
      "mode": "round_robin",                    // default: least_test. least_test | round_robin | failover
      "balancer": {                             // only valid with mode: round_robin
        "pool": 3,                              // default: 3. 0/omitted → 3; effective = min(pool, #outbounds)
        "pool_tolerance": 0,                    // default: 0 (ms). 0 = keep-live-fill; >0 = top-N-by-delay hysteresis
        "sticky_hash": ["process", "domain"]    // default: ["process","domain"]. Components:
                                                //   process | domain | source_ip | dest_ip | dest_port.
                                                //   DISABLE with the sentinel ["none"] — never a bare []
      }
    }
  ]
}
```

Field count: 26 XHTTP + 30 AmneziaWG (incl. `id`/`ip`/`ib` and the 9 AWG 3.x keys) + 1 VLESS (`encryption`) +
5 `urltest` (`mode` + `balancer{pool,pool_tolerance,sticky_hash}`) + 26 `lx` (9 `lx.wg` + `lx.masque.idle_timeout` + `lx.trusttunnel.idle_timeout` + 3 `lx.detour{mtu_align,strip,except_outbounds}` + 12 `lx.sniff_guard{nodes{sni_mismatch,outbound_type},detour_type,override,tls{method,delay,persist,detour_type},quic{method,delay,persist,detour_type}}`).
Mutually exclusive and ignored fields are flagged in the comments above. The feature documents give
the per-field semantics, gotchas and live verification status: [lx-config.md](lx-config.md) and its links.
