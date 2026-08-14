# SPEC: 069 — WG_V6_BIND_FAIL_KILLS_V4

**Feature:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)
**Touches:** P10 — this task owns the promise (a per-family bind failure must not kill the sibling family); P1 — the registry entry carries the removal condition. Adjacent [041](../041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) is not modified, but every one of its rebind triggers goes through the same `Open` path this task fixes.

| Field | Value |
|------|----------|
| Type | B (bug) — upstream `wireguard-go` behaviour gap (`StdNetBind.Open`), triggered by an upstream `sing` asymmetry on Windows; kills all WG/AWG endpoints on affected machines |
| Status | I (implemented) — fix + unit tests green (darwin, `-race`), `GOOS=windows` cross-build and vet clean (vet warnings are pre-existing upstream RIO code); **pending field validation** on the reporting client's machine |
| Branch | `lx` |
| Base | superproject `53a0a51d7`, submodule `wireguard-go` `334cad0` (`lx-awg2-v005`) |
| Related | [041](../041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) (its give-up/early/nudge triggers multiply the exposure of this bug), [026](../026-AWG_MAGIC_VS_RESERVED_CLEAR/SPEC.md) (same file, both bind paths), [010](../010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (prior `conn/` hotfix, since removed) |

**Touches (code):** fork submodule `submodules/wireguard-go` only —
`conn/bind_std.go` (`Open`, marker `// lx: SPEC 069`), new
`conn/lx_family_default.go` + `conn/lx_family_windows.go` (platform
predicate), tests `conn/lx_family_test.go`,
`conn/lx_family_default_test.go`, `conn/lx_family_windows_test.go`.
**The sing-box tree and the `sing` submodule are not touched.**

## Problem (field report, 2026-08-14)

A Windows client (десктоп launcher, core `1.14.0-lx.25-rc.1`) reports that
**every** WireGuard/AmneziaWG endpoint is dead from the moment the core
starts. Log signature, always this pair, one per endpoint:

```
ERROR endpoint/wireguard[ansi-sweden-awg]: unable to update bind: listen udp6 [::]:55449: An invalid argument was supplied.
ERROR endpoint/wireguard[ansi-sweden-awg]: peer(CCF8…30V0) - failed to send handshake initiation: address family not supported by protocol
```

The handshake never leaves the machine. The profile is IPv4-only (server
`13.140.10.218:32535`, client address `10.8.1.35/32`). An info-level capture
tied the failure to the very first route event, *before* `sing-box started`:

```
15:44:50 INFO  network: updated default interface Беспроводная сеть, index 9
15:44:50 ERROR endpoint/wireguard[ansi-sweden-awg]: unable to update bind: listen udp6 [::]:52593: An invalid argument was supplied.
15:44:52 INFO  sing-box started (1.84s)
```

### Machine portrait (established from `netsh`, two captures)

- Default interface is Wi-Fi «Беспроводная сеть», **ifIndex 9** — and that
  index is **absent from `netsh interface ipv6 show interface`** in both
  captures. The adapter has the IPv6 protocol unchecked: it does not exist
  in the v6 stack at all.
- No interface on the machine has a global IPv6 address (`show address`:
  link-locals and one Amnezia ULA only). The host has no external IPv6
  connectivity whatsoever.
- This is not an exotic setup: corporate images and "network optimizer"
  tools unchecking IPv6 on the adapter are common in the field.

### Ruled out experimentally during diagnosis

- `"::/0"` in `peers[].allowed_ips` — removing it changes nothing (two
  endpoints, with and without, fail identically). Allowed-IPs are inner
  routing; the failing socket is the outer bind.
- TUN without an IPv6 address — adding `fdfe:dcba:9876::1/126` to the TUN
  did **not** help (the failing setsockopt targets the Wi-Fi adapter, not
  the TUN) and made things worse: the OS started resolving AAAA and dialing
  v6 (`dial tcp [2a00:…]: An invalid argument was supplied` on direct-out —
  0 such dials in the log before the address, 8 and 6 after).
- `SetSinglePeerMode()` — a no-op on Windows (`msgx_default.go`), the
  darwin-only implementation is irrelevant here.
- "The monitor picked our own TUN as default" — refuted by the info log
  (index 9 is the physical Wi-Fi).
- SPEC 041 self-heal as the driver — refuted: all bind errors in all
  captured logs are the `InterfaceUpdated` format (`unable to update
  bind`); the self-heal path logs `Failed self-heal rebind (trigger=…)`
  and there are **zero** such lines. The failure reproduces on the plain
  upstream path; 041 merely adds more visits to the same broken code.

## Root cause (full chain)

1. **sing-box wires an interface-bind control into the WG listener.**
   `DefaultDialer.UDPListenerControl()` (upstream-identical code) hands the
   dialer's listener control — which includes
   `NetworkManager.AutoDetectInterfaceFunc()` — to
   `conn.NewStdNetBind(listenerControl)`
   (`transport/wireguard/endpoint.go:271`). Every socket the bind opens is
   bound to the current default interface.

2. **On Windows that bind is `setsockopt(IPV6_UNICAST_IF, ifIndex)`** —
   `sing/common/control/bind_windows.go`. The syscall requires the target
   interface to be part of the v6 stack. Index 9 is not → **WSAEINVAL**
   («An invalid argument was supplied»), deterministically, on every call.

3. **An upstream `sing` asymmetry lets the error escape only for `[::]`.**
   The same `bind_windows.go` swallows a `bind6` failure when the listener
   address is the `""` wildcard (comment: *"workaround for windows disable
   interface ipv6"* — the author knew this exact machine state), but
   **returns** it for an explicit address. `listenNet` in `wireguard-go`
   formats the v6 wildcard as `"[::]:port"` → the hard branch → the error
   reaches `Open`.

4. **`StdNetBind.Open` tolerated only `EAFNOSUPPORT` as a per-family
   failure** (`conn/bind_std.go`). WSAEINVAL is not it, so `Open` closed
   the **already-open, perfectly healthy v4 socket** and returned the
   error.

5. **`Device.BindUpdate` closes the old bind before opening the new one**
   (`device/device.go:748`). By the time `Open` fails, the previous
   sockets are gone; `BindUpdate` sets `netc.port = 0` and returns. The
   device is left with **no sockets at all**. Every subsequent
   `Send` finds `s.ipv4 == nil && s.ipv6 == nil` → `EAFNOSUPPORT` → the
   second log line. Handshake retries hammer a dead bind forever; the next
   route event repeats the same failure.

6. **Exposure.** Upstream walks this path only on route events — rare, and
   the first one arrives during startup on this machine, so the tunnel is
   dead from t=0. Our SPEC 041 adds give-up/early/nudge rebinds on top:
   more visits to the same broken path (though, per the logs, the field
   incident is fully explained by `InterfaceUpdated` alone).

Secondary latent defects found in the same block while fixing:

- **Unconditional `v4conn.Close()`** in both the `EADDRINUSE` retry branch
  and the v6-error branch — a nil dereference whenever v4 had itself
  degraded away (possible upstream too: v4 `EAFNOSUPPORT` + v6 `EADDRINUSE`).
- **Port clobber on the degradation path**: `v6conn, port, err =
  listenNet(…)` returns `(nil, 0, err)` on failure, so the multi-assignment
  wipes the surviving v4 socket's port with 0 and `Open` reports port 0 to
  the device. Latent in the upstream `EAFNOSUPPORT` path as well; caught
  red-handed by our own unit test.

## Fix

One mechanism: a platform predicate deciding "this error means the address
family is unavailable here", used symmetrically for both families.

```
conn/lx_family_default.go   (!windows)  EAFNOSUPPORT               — bit-for-bit upstream
conn/lx_family_windows.go   (windows)   EAFNOSUPPORT | WSAEINVAL | WSAEADDRNOTAVAIL
```

- `WSAEINVAL` — the reported case: interface-bind control on an adapter
  outside the v6 stack.
- `WSAEADDRNOTAVAIL` — the same machine state reached via a globally
  disabled v6 stack (registry `DisabledComponents`), where the wildcard
  bind itself fails.
- Non-Windows behaviour is intentionally unchanged: on Unix an `EINVAL`
  from bind is a real error and must keep failing the whole `Open`
  (pinned by `TestOpenStillFailsOnEINVAL`).

`Open` changes (`// lx: SPEC 069`):

- both family filters go through `bindFamilyUnavailable(err)`;
- `v4conn.Close()` in both error branches is nil-guarded;
- the surviving v4 port is captured before the v6 listen and restored when
  the v6 listen fails on the degradation path (the clobber above);
- if **both** families are unavailable, `len(fns) == 0` → the existing
  `return EAFNOSUPPORT` — `BindUpdate` reports exactly as before.

Degradation is silent, matching the upstream `EAFNOSUPPORT` precedent
(`Open` has no logger). The failed family stays loudly visible at the
point of use: a `Send` toward a v6 endpoint returns `EAFNOSUPPORT`.

### Behaviour after the fix on the reported machine

`Open` keeps the v4 socket, handshakes leave on IPv4, the tunnel comes up
with no user-side configuration change. The v6-family sockets simply do
not exist — the same observable state as running on a host with no v6
stack at all, which WireGuard handles routinely.

## Deliberately not done, and why

- **No error-swallowing wrapper around `UDPListenerControl` in sing-box**
  (earlier candidate, rejected). Swallowing the control error would
  produce a v6 socket that exists but is *not bound to the default
  interface*; under `auto_route` its egress follows the routing table into
  our own TUN — a silent routing loop for any future v6 endpoint. Not
  creating the unusable socket is strictly safer than creating an unbound
  one.
- **The `sing` submodule asymmetry (`""` vs `"[::]"`) is left to
  upstream.** With the predicate in place the asymmetry is harmless to us,
  and we keep zero patches in that submodule.
- **v6 dial errors on direct-out are not touched.** `dial tcp [2a00:…]:
  An invalid argument` is an honest "no v6 through this interface" signal
  on this machine; masking it belongs to DNS strategy (`ipv4_only`), not
  to the dialer.
- **No retry-on-failed-`BindUpdate` hardening** (both `InterfaceUpdated`
  and SPEC 041's `selfHealRebind` log and give up, leaving the device
  socketless until the next trigger). Real, but out of scope: a retry
  goroutine has to negotiate the `resumeMu` idle-suspend machine
  (SPEC 020/030) and deserves its own design. This fix removes the known
  deterministic cause of `BindUpdate` failure, taking the pressure off.

## Verification

- **Unit, cross-platform** (`conn/lx_family_test.go`, darwin `-race`
  green):
  - `TestOpenKeepsV4WhenV6FamilyUnavailable` — injected family failure on
    `udp6`; `Open` succeeds, v4 socket present and *actually carries
    packets* (loopback sink), v6 `Send` returns `EAFNOSUPPORT`, port ≠ 0
    (this assertion is what caught the port clobber);
  - `TestOpenKeepsV6WhenV4FamilyUnavailable` (env-gated on v6 presence);
  - `TestOpenFailsWhenBothFamiliesUnavailable` — both gone →
    `EAFNOSUPPORT` as before.
- **Unit, per-platform predicate:**
  - `!windows`: `EAFNOSUPPORT` true; `EINVAL`/`EADDRNOTAVAIL`/`EADDRINUSE`
    false; plus `TestOpenStillFailsOnEINVAL` pinning the unchanged Unix
    contract;
  - `windows`: `WSAEINVAL`/`WSAEADDRNOTAVAIL`/`EAFNOSUPPORT` true (wrapped
    forms included), `WSAECONNREFUSED`/`EADDRINUSE` false; plus
    `TestOpenKeepsV4OnWSAEINVAL` — the field scenario end-to-end, **red on
    the pre-fix base** (runs on a Windows host/CI).
- **Builds:** darwin `go build`/`go test ./conn/ -race` green;
  `GOOS=windows go build ./conn/... ./device/...` green; `GOOS=windows go
  vet ./conn/` — only pre-existing upstream RIO `unsafe.Pointer` warnings
  (`bind_windows.go`, untouched).
- **Superproject:** `go build ./transport/wireguard/ ./protocol/wireguard/`
  and `go test ./transport/wireguard/` green via the local `replace`.
- **Pending (field):** a build on the reporting client's machine — expect
  the error pair gone and the handshake leaving on IPv4 with no adapter
  changes. Until shipped, the client-side workaround is: re-enable the
  IPv6 protocol checkbox on the default adapter (puts it back into the v6
  stack, making `IPV6_UNICAST_IF` valid); with no external v6 on that
  network, `"strategy": "ipv4_only"` in DNS is the honest configuration
  either way, and the `fdfe::` TUN address should be reverted.

## Removal condition (P1)

Upstream `wireguard-go` makes `StdNetBind.Open` survive a per-family bind
failure beyond `EAFNOSUPPORT` (or stops closing the sibling socket on
per-family errors) — watch the error handling around `listenNet` calls in
`conn/bind_std.go` on every submodule bump; an upstream rewrite of that
block silently drops this patch (three-way merge, our authorship). The
`sing`-side asymmetry healing (`"[::]"` treated like `""`) would also
defuse the reported case but does not by itself lift the predicate: any
other stable per-family error would recreate the same socketless device.
