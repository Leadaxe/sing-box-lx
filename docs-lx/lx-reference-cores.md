---
icon: material/compass-outline
---

# Where to look for the answer

> 🌐 Русская версия: **[lx-reference-cores.ru.md](lx-reference-cores.ru.md)**.

The fork lives inside someone else's ecosystem: Xray defines the protocols, and
next door there are clients that already solved the same problem. When a node
works in another app and not in ours, the answer is almost always in someone
else's source — finding it there is faster than deriving it from first
principles.

This page is a map: which projects are useful for what, where each one keeps the
part we care about, and how to check their **current** version rather than a
copy downloaded six months ago.

!!! warning "Don't keep clones next to the tree"
    A copy goes stale silently, and comparing against outdated code is worse
    than not comparing at all: the conclusion looks well-founded while resting
    on something the project no longer contains. Clone for the investigation,
    delete afterwards.

## Protocol references

### Xray-core — the normative source

<https://github.com/XTLS/Xray-core>

It defines the contract for XHTTP, VLESS and REALITY. When our behaviour
disagrees with Xray, they are usually right.

| What | Where |
|------|-------|
| XHTTP (`splithttp`) — modes, padding, path normalization | `transport/internet/splithttp/` |
| Client side: mode selection, request shaping | `.../dialer.go`, `.../client.go` |
| Server-side routing — what explains the rejections we get | `.../hub.go` |
| Shared fields and their normalization | `.../config.go` |
| VLESS encryption (`encryption`) | `proxy/vless/encryption/` |

Check against a release tag, not `main`: the format has changed after merges
(padding, for one, moved to `probability-from-to` in `v25.8.31`).

### Project X — field documentation

<https://xtls.github.io/config/> · [VLESS outbound](https://xtls.github.io/en/config/outbounds/vless.html)

The user-facing contract: which values are allowed, what each segment of a spec
string means. Useful when the source answers "how" and you need "what".

## Clients built on our own base

These matter because they are written on sing-box: the code ports almost
verbatim, and the licence matches (GPL-3.0, same upstream) — borrowing is legal
and needs no relicensing.

### starifly/sing-box — the NekoBox+ core

<https://github.com/starifly/sing-box>

The most valuable neighbour: it implements what we lack, in our own terms. The
`mlkem768x25519plus` layer (SPEC 032) came from here.

**Finding the right commit.** It is pinned by the app, not by the core itself:

```bash
git clone --depth 1 https://github.com/starifly/NekoBoxForAndroid
cat NekoBoxForAndroid/buildScript/lib/core/get_source_env.sh
# COMMIT_SING_BOX="…"  ← clone the core at this commit
```

| What | Where |
|------|-------|
| VLESS `encryption` (client and server) | `protocol/vless/encryption/` |
| Spec-string parsing, the seam into dialing | `protocol/vless/outbound.go` |
| XHTTP with XMUX — a second reference for ours (SPEC 059) | `transport/v2rayxhttp/` |

### shtorm-7/sing-box-extended

<https://github.com/shtorm-7/sing-box-extended>

A second independent take on the same transports. Useful as a cross-check: when
shtorm-7 and NekoBox do the same thing and we do another, the divergence is
almost certainly ours.

### mihomo — format cross-check

<https://wiki.metacubex.one/en/config/proxies/vless/>

A different codebase, so no code ports across, but its documentation confirms
the format independently of Xray.

## Another client's configuration on the device

The shortest path when a node lives in a different app and stays silent in ours:
look at **its configuration** rather than comparing core behaviour. Twice in one
investigation this gave the answer in a minute where reasoning through code cost
hours.

NekoBox+ keeps its nodes in SQLite, readable under root:

```bash
adb shell "ps -A -o NAME | grep nb4a"        # package: com.nb4a.plus
adb shell "su -c 'cat /data/data/com.nb4a.plus/databases/sager_net.db'" > sager.db
```

Table `proxy_entities`: the `*Bean` fields carry the node configuration, `ping`
and `status` hold the last check, and **`error` holds the reason for failure in
that client's own words**. The last one is the valuable part: another client has
already diagnosed what our log stays quiet about.

!!! danger "This is a dump of someone's credentials"
    It contains real keys, passwords and server addresses. Delete it right after
    the investigation.

## How to use all this

1. **Capture the fact first, reason second.** The wire, the configuration, a
   dump — each is cheaper than one build iteration. The "hangs with no error"
   symptom is especially deceptive: it looks identical for a bug of ours, a
   missing feature, and a dead server.
2. **Another client on the same device is the control measurement.** It brings
   the node up and we don't → the problem is ours. Both stay silent → the
   problem is outside the core. Measurements from a laptop behind an unrelated
   VPN prove nothing; measure only where the symptom reproduces.
3. **Compare against a release tag**, not a development branch.
4. **When borrowing code, check the licence and record the origin** in the file
   header (see `protocol/vless/encryption/`).

Investigations where this paid off: [SPEC 043](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md)
(path normalization found by comparing against both neighbours),
[SPEC 032](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/032-VLESS_ENCRYPTION_MLKEM768/SPEC.md)
(the cause found in another client's database dump, the fix ported from the same
place).
