# Method — Android idle-suspend run

Reproducible procedure. No credentials: the Debug API token, device serial, Wi-Fi
IP and synthetic WG private keys are intentionally omitted. Substitute your own.

## 1. Core build (rc.18 AAR)

At the time of this run there was **no GitHub release** for rc.18 (the core was a
pre-release), so the AAR was built locally from the `lx-1.14` branch with `HEAD` at
the `v1.14.0-lx.1-rc.18` tag:

```
cd sing-box-lx
make lib_install          # gomobile/gobind @ v0.1.13
make lib_android          # gomobile bind → libbox.aar
                          # bakes with_gvisor,with_wireguard,with_quic,with_utls,
                          #       with_awg,with_xhttp,...
                          # Libbox.version() is stamped from `git describe`
```

- Toolchain: Go 1.25.5, Android NDK r28, gomobile v0.1.13.
- Output `libbox.aar` (~97 MB) was dropped into the LxBox app's `libs/` and the app
  built as a normal release APK.
- **Version trap:** `strings` does **not** recover the version from a gomobile `.so`
  (the version lives in a Go string the linker inlines). The reliable check is the
  app's Debug API `/device` → `core_version`, which reported `1.14.0-lx.1-rc.18` —
  confirming the running core, not just the pinned tag.

For local config validation a desktop binary was also built. Note it must include
`with_awg`, else the real WARP-AWG node fails config load
(`AmneziaWG (awg) support is not included in this build`):

```
go build -tags "with_gvisor,with_wireguard,with_quic,with_utls,with_awg" \
  -o /tmp/sbox-rc18-awg ./cmd/sing-box
/tmp/sbox-rc18-awg check -c <test-config>.json      # schema-validates lx_idle_suspend
```

## 2. Test config shape (the key scenario)

9 WireGuard endpoints, `route.final = wg-1`, no rules → **only `wg-1` reachable**,
`wg-2..wg-9` unreachable:

- **`wg-1`** — a real reachable node (here: a WARP-AWG endpoint carrying live
  traffic). It is the `final`, so it must stay `Up` and must never appear in a
  `lx idle:` line.
- **`wg-2 … wg-9`** — 8 synthetic plain-WG endpoints: fresh valid X25519 keys, peer
  pointed at **TEST-NET-1 `192.0.2.x`** (RFC 5737, guaranteed unroutable). They never
  handshake → they stay `Up` with live recv-workers/`bufsArrs` → exactly the idle
  subject the feature targets.

```jsonc
{
  "log": { "level": "info", "timestamp": true },   // INFO needed for `lx idle:` lines
  "dns": { "servers": [ { "tag": "cf", "type": "udp", "server": "1.1.1.1" } ], "final": "cf" },
  "inbounds": [ { "type": "tun", ... } ],           // real VpnService tun on-device
  "endpoints": [ /* wg-1 (real, reachable) ... wg-9 (synthetic, unreachable) */ ],
  "outbounds": [ { "type": "direct", "tag": "direct-out" }, { "type": "block", "tag": "block" } ],
  "route": {
    "lx_idle_suspend": "30s",       // tick = max(30/2, 5) = 15 s
    "final": "wg-1",
    "rules": [],
    "default_domain_resolver": "cf"
  }
}
```

> **rc.18 gotcha (unrelated to this feature):** a DNS server with `detour` to a
> *settings-less* `direct` outbound now fails start:
> `start dns/udp[cf]: detour to an empty direct outbound makes no sense`. The test
> config therefore uses a plain UDP resolver with **no** `detour`. This bit both the
> desktop and the device run before it was simplified.

Synthetic endpoint template (keys elided):

```jsonc
{
  "type": "wireguard", "tag": "wg-2", "mtu": 1420,
  "address": ["10.66.0.2/32"], "private_key": "<throwaway>",
  "peers": [ {
    "address": "192.0.2.10", "port": 51820,            // TEST-NET-1, never reachable
    "public_key": "<throwaway>", "allowed_ips": ["0.0.0.0/0"],
    "persistent_keepalive_interval": 25
  } ]
}
```

## 3. Delivery + measurement (on-device, via the app's Debug API)

The config was pushed to the running app and the core reloaded, all over the app's
Debug HTTP API (localhost, adb-forwarded). Endpoints used (method names only, no
token shown):

| Step | Debug API call | Purpose |
|---|---|---|
| enable core-log forwarding | `PUT /settings/core_logs_enabled {enabled:true}` then **force-stop + reopen** the app | `lx idle:` lines land in `/logs/core` (Libbox.setup is one-shot per process, so a full app restart is required) |
| pin the pushed config | `PUT /settings/config_locked {locked:true}` | stop an auto-rebuild from wiping the test config |
| push + reload | `PUT /config` (raw sing-box JSON) | overwrite config.json + reload the core |
| clean tunnel restart | `POST /action/stop-vpn` → `POST /action/start-vpn-headless` | fresh establish so all recv-workers start (18) and log forwarding is reinstalled |
| read transitions | `GET /logs/core` | collect `lx idle: suspend/wake` lines |
| recv-worker count | `GET /diag/pprof?profile=goroutine&query=debug=1` | count `RoutineReceiveIncoming` goroutines |
| heap A/B | `GET /diag/pprof?profile=heap&query=gc=1` | `.pb` for `go tool pprof -inuse_space` |
| wake-by-dial | `POST /action/urltest?tag=wg-2` | a dial through a sleeping endpoint → `wake … by=dial` |

`/diag/pprof` is the app's passthrough to the libbox `PProfServer` — i.e. the core's
own pprof, so the numbers are the core's real live heap, not an app-side estimate.

### Measurement discipline for the heap A/B

The A/B needs a clean "before" (18 recv-workers, all endpoints `Up`) and a clean
"after" (2 recv-workers, 8 suspended):

1. `stop-vpn` → `start-vpn-headless` for a fresh establish.
2. At **T+~5 s** (endpoints up, before the 30 s threshold): capture heap + goroutine
   → this is **before**. Confirmed `RoutineReceiveIncoming = 18`.
3. Idle-hold past the threshold; poll goroutine until `RoutineReceiveIncoming` drops
   to 2 (the 8 synthetic endpoints suspended).
4. Capture heap + goroutine → this is **after**.
5. `go tool pprof -top -inuse_space` on both `.pb` files; compare
   `PopulatePools.func3`.

> Caveat learned the hard way: a `PUT /config` **in-place reload** does not give a
> clean 18-worker baseline (it rebuilds the running core mid-flight). Only a real
> `stop → start` yields the fresh 18. Use that for the A/B.

## 4. Cleanup

After the run: `PUT /settings/config_locked {locked:false}` →
`POST /action/rebuild-config` (regenerates the real subscription config from
settings) → `PUT /settings/core_logs_enabled {enabled:false}`. The tunnel returns to
the user's real subscription. (Note: prefer an explicit `stop → start` after
`rebuild-config` so the UI's selected channel re-binds immediately; a soft
`rebuild-config` alone can briefly show a "Config changed — restart to apply" banner
and an empty channel until the command-client re-syncs.)
