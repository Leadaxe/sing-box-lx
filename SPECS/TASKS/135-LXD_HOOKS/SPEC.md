# SPEC: 135 — LXD_HOOKS

> 🌐 Русская версия: **[SPEC.ru.md](SPEC.ru.md)** (auxiliary copy; this English file is primary — owner decision 2026-10-17).

**Feature:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | F (feature) — admin scripts at the lifecycle boundaries of the core inside the daemon |
| Status | I (implemented) 2026-10-17 — see [IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md); the manual owner checks of §3 are open. Design approved by the owner 2026-10-17 (event model, variables, `hooks/core_snapshot.json` written by the daemon, stubs created by the daemon, explicit `on/off`, logging). This English file is the primary spec; [SPEC.ru.md](SPEC.ru.md) is the auxiliary Russian copy, both are maintained together (owner decision 2026-10-17) |
| Branch | `lx` |
| Build tag | `with_lxd` (lx files `lxd/hooks*.go`, insertions in `lxd/apply.go`, `lxd/daemon.go`, `lxd/daemonconfig.go`, `lxd/admin.go`; one `// lx:` accessor in `protocol/tun/inbound.go` — §2.3) |
| Origin | Discussion with the owner 2026-10-17. Trigger — issue #12: the system-DNS automation on darwin was rolled back out of the core; the policy "where to point DNS and how to restore it" must live with the admin; `commands` (SPEC 128) do not fit — a client runs them, not an event |
| Related | [128](../128-LXD_COMMANDS/SPEC.md) (the same `LXD_*` environment mechanism, `markInheritableCloseOnExec`, the outcome directory next to the log), [056](../056-LXD_APPLY_ROLLBACK/SPEC.md) (the `Apply`/`Rollback`/`Start`/`Stop` boundaries the hooks attach to), [100](../100-LXD_ROOT_OWNED_BINARY/SPEC.md) (`checkRootOwnedChain` — the ownership gate), [129](../129-LXD_UPDATE/SPEC.md) (the race between the old and the new daemon under procd, §2.6 there — §2.5 here), [127](../127-LXD_INFO_PLATFORM/SPEC.md) (platform facts in the environment) |

The admin puts four scripts into `<state_dir>/hooks/` — `pre_start`, `post_start`, `post_reload`,
`post_stop` — and enables them in `daemon.json`. The daemon calls them at the lifecycle boundaries
of the core **in its own process**: before the core comes up, after it is up, after the config is
swapped, after it stops. The script receives facts about the core in the environment (`LXD_EVENT`,
`LXD_TUN_ADDRESS`, `LXD_TUN_INTERFACE`, …) and decides for itself what to do with the system:
pointing the system DNS at the tun and restoring it afterwards is the main scenario. The daemon
guarantees only one thing: every boundary is announced, including after a process crash and
after `update`. What to do at the boundary is entirely the admin's business.

## 1. Problem

The core on a host with TUN changes routing but does not touch the system DNS: on macOS, on
Linux without `systemd-resolved` and on routers the system resolver keeps pointing at the ISP.
An attempt to do this automatically inside the core (`networksetup` on darwin) was rolled back
in issue #12: the core cannot know all network services, configurations and the order in which
to restore them, and a failed rollback leaves the system without DNS. The right place for such
a policy is an admin script; the daemon knows enough to call it at the right moment with the
right facts.

`commands` (128) do not cover this role: an authenticated client runs a command by pressing a
button, a core event will not trigger it, and the facts about the core (the tun address, the
actual `utunN` name) are not passed to it.

Rejected:

- **DNS automation in the core or in the daemon** — issue #12, reason above. The daemon does not
  know the system state and does not keep a snapshot of it.
- **A `pre_*` counterpart for every event** (eight hooks) — a script cannot rely on any `pre_*`:
  a panic in the core takes the daemon process down, and the only thing guaranteed to run is the
  `post_*` at the next boundary. Recovery logic must live in `post_*`, be idempotent and proceed
  from the script's own state on disk. `pre_stop` is dropped also because it spends the stop
  budget under SIGTERM (§2.5) for nothing: the snapshot of the system state is taken in
  `post_start`, not before the stop. The only `pre_*` is `pre_start`, and it is a gate, not a
  recovery.
- **Apply as a full stop and start** (`post_stop` → `pre_start` → `post_start` on every config
  apply) — DNS blinks on every apply from the launcher, two extra hooks under `applyAccess`, and
  `post_stop` does what the next hook immediately undoes. The semantics "the core is there
  continuously, only the address changed" is more precise — it gets its own event, `post_reload`.
- **Script paths in `daemon.json`** (`{"post_start": "/path"}`) — the files live at fixed paths
  under `<state_dir>/hooks/`, which the daemon creates. The ownership chain is root-owned by
  construction, the question "a path or a quoted shell string" disappears, and only `on/off` keys
  remain in the config.
- **Execution via `/bin/sh -c "<string>"`**, as `commands` do — the ownership gate would have no
  subject: what is there to check in `VAR=1 /path/x` or `cd /opt && ./x`? A hook is an executable
  file run directly; the admin's shell is inside the file via `#!`.
- **Detached execution** (`spawnDetached` from 128) — the daemon needs the exit code for
  fail-closed on `pre_start` and for the mark in the status; hooks are synchronous.
- **Hook state written by the script** — the decision "whether to run recovery at boot" is made
  by the daemon, and it must not depend on whether the script ran (timed out, not configured);
  the actual interface name after a crash can come from nowhere but the daemon's record; one
  writer and one reader — the format stays internal. Therefore `hooks/core_snapshot.json` is written by
  the daemon (§2.4), and the snapshot of the system state by the script, in the same directory,
  in its own format.

## 2. Behaviour

### 2.1 Config and files

```jsonc
"hooks": {
  "enabled": true,                 // default false: no key = no hooks
  "timeout_seconds": 15,           // per hook, integer seconds; 0/absent = 15; negative = the daemon refuses to start, naming the key
  "unsafe_skip_owner_check": false // default false; true = the §2.5 gate is off, WARN on every daemon start
}
```

| Path | Written by | Purpose |
|---|---|---|
| `<state_dir>/hooks/` | the daemon at start (if absent), `root:root 0755` | hook directory; `LXD_HOOK_DIR` |
| `<state_dir>/hooks/pre_start`, `post_start`, `post_reload`, `post_stop` | the daemon at start, if the file is absent: a stub, `0755` | admin scripts |
| `<state_dir>/hooks/core_snapshot.json` | the daemon only | facts of the last core start (§2.4) |
| `<state_dir>/hooks/.lock` | the daemon | inter-process `flock` around every hook (§2.5) |
| `<dirname(log_file)>/hooks/<event>.log` | the hook (redirected by the daemon) | output of the last run; without `log_file` — `<state_dir>/hooks/log/`, a daemon-owned subdirectory, not part of the script's area |
| everything else in `<state_dir>/hooks/` (except `core_snapshot.json`, `.lock`, `log/`) | the script | snapshot of the system state, format at the script's discretion |

**The stub** is not an empty file (an empty `exec` returns `ENOEXEC`, indistinguishable from a
broken hook) but `#!/bin/sh`, `exit 0` and a comment listing the environment variables of **this**
event, one line each (§2.3). The variable documentation lives where the admin will write the
script. The reference stubs are embedded in the binary; a hook whose file is byte-for-byte equal
to the reference, or absent, counts as **not configured**. The daemon creates the stubs for the
missing files on every start — this covers all platforms and ways of installation (on Linux
`--service=install` only prints an instruction); the installers (`--service=install` on macOS,
`lxd-openwrt-setup.sh`) do nothing extra, but their documentation names the directory.

With `enabled: false` or no key the daemon still creates the directory and the stubs (so that the
admin sees where to write), logs one Info line `lxd: hooks disabled` at start and says nothing
more about hooks. Changing the keys = editing the file + restarting the service, as with every
`daemon.json` key.

### 2.2 Events and failure semantics

Four events. The "current" core is what exists after the event; `_PREV` is what no longer exists.

| Event | When | `LXD_REASON` | Hook failure (non-zero exit, timeout, gate failure) |
|---|---|---|---|
| `pre_start` | before `StartOrReloadService`, when there is no core | `boot` (daemon boot, `bootstrap`), `start` (`POST /admin/start`, `POST /admin/rollback` with no core), `apply` / `sighup` (an apply that brings the core up from nothing — implementation clarification 2026-10-17: such an apply is a start, not a reload) | **fail-closed**: the core does not start, `status: fatal`, `last_error` = `hook pre_start: …`; on `boot` the daemon stays alive without a core, as with any start failure |
| `post_start` | after a successful return from `StartOrReloadService` for the same reasons — tun and routes already exist | `boot`, `start`, `apply` / `sighup` (the same case) | the core keeps running; Error in the log, a mark in `/admin/status` (§2.6) |
| `post_reload` | after the instance is swapped in the live process | `apply` (REST), `sighup` (file re-read), `rollback` (the new config did not come up, last-good is up; also `POST /admin/rollback` over a live core — then `LXD_ERROR` is empty) | the core keeps running; Error in the log, a mark in the status |
| `post_stop` | when the core is gone | `stop` (`POST /admin/stop`), `shutdown` (SIGTERM — including `update` and a service restart), `fatal` (the start failed after a passed `pre_start`, or the rollback failed too), `recover` (the new daemon found a `core_snapshot.json` from the previous run at boot — §2.4) | log only; nothing blocks the stop |

Rules:

- **Every entry into start ends with either `post_start` or `post_stop`.** `pre_start` passed, the
  core did not come up → `post_stop` with reason `fatal` (owner decision): the script has no
  snapshot and exits silently, but the symmetry has no exceptions: every `pre_start` is answered
  by exactly one `post_*`, and the script never has to reason about a start that vanished.
- **A rollback inside Apply** is a `post_reload` with reason `rollback`: the core never
  disappeared, the current facts describe last-good, `LXD_ERROR` is the reason the new config was
  rejected. If last-good did not come up either — `post_stop` with reason `fatal`.
- **`pre_start` has no side effects.** It is a precondition gate (the uplink interface exists, the
  disk is not full); a crash is harmless to it: if it did not run, the core did not start either.
  It has no counterpart.
- **`post_*` are idempotent and recovering.** The script cannot assume that `post_start` preceded
  `post_stop`, or that nothing crashed between two `post_reload`s; it reads its own snapshot and
  acts on it. This is a contract: the spec requires it, the daemon cannot verify it.
- **A hook that is not configured** (stub or no file) with `enabled: true` — the event is skipped
  with a Warn in the log (owner decision 2026-10-17: a skipped event is a Warn): `lxd: hook post_reload skipped: not configured
  (stub unchanged)`. `pre_start` not configured — no gate, the start proceeds.
- **No hook waits for the outcome of a previous event's hook** — a failed `post_start` does not
  hold back the next `post_reload`. Concurrency is a separate matter: within one process hooks are
  serialized by the same `applyAccess` under which the transition runs, across processes by the
  `flock` of §2.5. The transition waits for the hook:
  `POST /admin/apply` may take up to `timeout_seconds` longer.

### 2.3 Environment

The variables are facts from the daemon, never client input. An empty string = the fact is
unknown or not applicable.

**Common to all events**

| Variable | Value |
|---|---|
| `LXD_EVENT` | `pre_start` / `post_start` / `post_reload` / `post_stop` |
| `LXD_REASON` | the transition reason from the §2.2 table |
| `LXD_CLIENT` | the client name if the transition was started by a REST request (as in the 128 audit: by certificate, `operator` for Bearer); empty for `boot`, `sighup`, `shutdown`, `fatal`, `recover` |
| `LXD_PLATFORM`, `LXD_VERSION`, `LXD_INIT_SYSTEM`, `LXD_STATE_DIR`, `LXD_BIN`, `PATH` | as for `commands` (128 §2.3, `commandEnv`) |
| `LXD_LOG_FILE` | path of the daemon log; empty when run in a terminal |
| `LXD_HOOK_DIR` | `<state_dir>/hooks` — the place for the script's snapshot |
| `LXD_HOOK_TIMEOUT` | the budget in seconds, so that the script fits its own calls into it |

**Facts about the core** — current ones without a suffix, previous ones with `_PREV`:

| Variable | Value | Source |
|---|---|---|
| `LXD_CONFIG_SHA` / `_PREV` | sha256 of the active content, as in `/admin/status` | controller |
| `LXD_TUN_ADDRESS` / `_PREV` | addresses of all tun inbounds, comma-separated, v4 and v6 together, in config order (`inbounds[type=tun].address`) | config parsing |
| `LXD_TUN_INTERFACE` / `_PREV` | actual interface names, comma-separated, in the same order | accessor on the instance; in `pre_start` — only an explicit `interface_name`, otherwise empty |
| `LXD_TUN_MTU` / `_PREV` | mtu, comma-separated | config parsing (the core default 9000 if not set) |
| `LXD_TUN_AUTO_ROUTE` / `_PREV` | `1` / `0`, comma-separated | config parsing; with `0` pointing the system DNS at the tun is pointless, the script must see this without `jq` |
| `LXD_ERROR` | the core error text when the transition is caused by a failure (`rollback`, `fatal`) | `lastError` |

No tun inbound — all `LXD_TUN_*` are empty. Config parsing is `json` over the tree without
`option.Options`: the daemon needs four fields, and a full decode would drag the registration of
all inbounds into the check.

**Per event**

| Event | Current | `_PREV` |
|---|---|---|
| `pre_start` | from the config; `LXD_TUN_INTERFACE` only explicit | none |
| `post_start` | full, `LXD_TUN_INTERFACE` actual | none |
| `post_reload` | full (the new instance) | full (the old instance; `LXD_TUN_INTERFACE_PREV` taken **before** it is closed) |
| `post_stop` | none | full: on `stop`/`shutdown` — from the live instance before it is closed; on `fatal` — from the instance that was live before the swap when there was one (an apply whose rollback also failed), else from the config that did not come up (interface only explicit); on `recover` — from `core_snapshot.json` |

**The actual interface name.** `protocol/tun/inbound.go` computes the name before the interface
is created (`CalculateInterfaceName` takes the next free `utunN`/`tunN`); after `tun.New` it sits
in `t.tunOptions.Name`. The core runs in the daemon process, so the daemon reads it directly:
one `// lx:` method `InterfaceName() string` on `*Inbound`; after the start the daemon walks the
box's inbounds (`adapter.InboundManager`) and collects the names with `Type() == C.TypeTun`. This
is the only insertion into an upstream file in this task; the alternative "from the config only"
would leave the variable empty on darwin without an explicit `interface_name`, and Linux scripts
with `resolvectl dns <iface>` without a name. Owner decision 2026-10-17 — the accessor.

### 2.4 `core_snapshot.json` and `recover`

The daemon writes `<state_dir>/hooks/core_snapshot.json` **after every successful start** (`post_start`,
`post_reload` — regardless of the outcome of the hook itself) and deletes it after `post_stop`
(any reason). The content is the facts of the last live core: `config_sha`, `tun_address`,
`tun_interface`, `tun_mtu`, `tun_auto_route`, `started_at`, the daemon `pid`. The file is written
atomically (`store.writeAtomic`); it is the same state dir as `last_good.json`: one write per
start, not in a loop, so the flash objection from 128 does not carry over here.

At daemon boot, **before** `bootstrap`: `core_snapshot.json` exists → the previous process did not reach
`post_stop` (core panic, `kill -9`, power loss) → the daemon calls `post_stop` with reason
`recover` and `_PREV` from the file, deletes the file and only then proceeds to the usual
`pre_start`. For the script there is no difference from a regular `stop`: the same variables, the
same snapshot. This is the **only** coverage for a core panic: the core lives in the daemon
process, there is no "child supervision", and nobody is there to run `post_stop` at the moment of
the crash.

### 2.5 Execution

- **Synchronous.** The daemon runs the file directly (`exec.Command(path)`, no shell), cwd `/`,
  stdin `/dev/null`, the daemon environment plus §2.3, waits for exit with the `timeout_seconds`
  timeout; on timeout — `SIGKILL` to the hook's process group, outcome "timeout".
- **Output goes to a file, not a pipe.** stdout/stderr are redirected to
  `<log directory>/hooks/<event>.log` (overwritten on every run). With a pipe `exec.Cmd.Wait`
  would wait for the pipe to close, and a background child of the script (`nohup … &`) would hold
  the daemon until the timeout. An asynchronous tail inside a hook is allowed, but the daemon sees
  only the exit code of the synchronous part; the background lives in the daemon's process group
  and dies with it under the service's SIGTERM unless the script called `setsid`; the flock is
  released when the script exits. Whatever DNS or the gate depends on must finish before `exit`.
  This is stated in the documentation and in the stub comment. The stub comment lists every variable
  of its event, marking the ones that are usually empty (`LXD_TUN_INTERFACE` in `pre_start` and
  `LXD_TUN_INTERFACE_PREV` in `post_stop(fatal)` are set only for an explicit `interface_name`).
- **Descriptors are not inherited.** `markInheritableCloseOnExec` (128 §2.3) before every run:
  otherwise `pre_start` or a backgrounded child would get a copy of the TUN fd and hold the
  interface until `EBUSY` on the next start — exactly the lx.17/lx.18 bug.
- **Ownership gate.** Before **every** run (not only at daemon start: four `lstat`s cost nothing,
  while a script replaced after the service started would otherwise slip through):
  `checkRootOwnedChain(path)` from `execsafe.go` — every component from `/` to the file is owned
  by root, no write for group/other, the file itself is regular (not a symlink) and executable.
  A gate failure = a hook failure with the §2.2 semantics (on `pre_start` the core does not
  start). `unsafe_skip_owner_check: true` disables the check entirely — for a dev run from a home
  directory where the state dir cannot be root-owned; WARN on every daemon start, not once.
- **Inter-process `flock`** on `<state_dir>/hooks/.lock` for the duration of every hook. The
  reason is the race documented in `daemon.go` at `startRetryingBusy`: procd on `restart` (and
  `update` is one) starts the new process before the old one has finished its teardown. Without
  the lock the new `post_start` would snapshot a system DNS that already holds the VPN DNS,
  because the old `post_stop` has not restored it yet — the snapshot is poisoned, DNS will not come
  back after the stop. The lock plus the order "the new daemon first completes `recover`, then
  `pre_start`" (§2.4) close the interleaving: the new process either waits for the old one or
  closes its tail itself.
- **Stop budget.** `post_stop` under SIGTERM runs inside `shutdown()` before the `CloseService` of
  the other planes and before the process exits; `shutdownGrace` (3 s) does not apply to it. The
  window is given by the init system: procd kills via `term_timeout` (default 5 s), launchd via
  `ExitTimeOut` (20 s). `SIGKILL` in the middle of `post_stop` = DNS on a dead tun, the log will
  not help. Therefore `lxd-openwrt-setup.sh` (both languages) adds `procd_set_param term_timeout
  30`, and the documentation requires fitting `post_stop` into the host init system's window.
- **Windows** — the `hooks` key is accepted with a Warn in the log (`hooks are ignored on this
  platform (unix only)`), no directory, no stubs (this overrides the `enabled: false` rule of §2.1), no calls: `flock`, `checkRootOwnedChain`, process groups
  are unix. Linux (including OpenWrt) and macOS share one code path.

### 2.6 Status, log, audit

Log levels (owner decision 2026-10-17: a skipped event is a Warn, a run is an Info):

| Level | When | Example |
|---|---|---|
| Info | hook started; hook finished successfully (with duration) | `lxd: hook post_start (reason boot) started`, `lxd: hook post_start done in 420ms` |
| Warn | event skipped: not configured; `unsafe_skip_owner_check` enabled (at daemon start); `hooks` on Windows | `lxd: hook post_reload skipped: not configured` |
| Error | hook ran and failed, timed out or did not pass the gate — with the exit code and the output tail (≤ 4 KiB) | `lxd: hook post_start failed: exit 2: networksetup: … (output tail)` |

`GET /admin/status` gets a `hooks` object — the last outcome per event, so that the launcher can
draw a yellow mark:

```jsonc
"hooks": {
  "enabled": true,
  "post_start": {"state": "ok" | "failed" | "timeout" | "skipped" | "never",
                 "reason": "boot", "exit_code": 0, "finished_at": "…", "detail": "…"},
  "pre_start": {…}, "post_reload": {…}, "post_stop": {…}
}
```

`detail` is the skip reason or the first line of the error; the log tail is in the §2.1 file, there
is no separate route (as with 128). `GET /admin/info` is not touched: it holds immutable facts.

### 2.7 Documentation and changelog

- `docs-lx/lxd-daemon.md` and `.ru.md`: the `hooks` key in §3; a new "Hooks" section after §6
  (Logs): the four events with their reasons, the §2.3 variable tables, the `post_*` idempotency
  contract, the rule about the asynchronous tail, the stop budget and `term_timeout`/`ExitTimeOut`,
  a sample `post_start`/`post_stop` for macOS (`networksetup -setdnsservers` from the snapshot in
  `$LXD_HOOK_DIR`) and for Linux (`resolvectl dns $LXD_TUN_INTERFACE …`); the `hooks` field in §10.
- `scripts-lx/openwrt/lxd-openwrt-setup.sh` and `-ru`: `term_timeout 30`; the README in both
  languages — a paragraph about the hook directory.
- `docs-lx/lx-changelog.md`: an item linking the SPEC. `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`:
  a row in §2, a paragraph in §5 (the 100 boundary: hooks are root-owned files in a root-owned
  directory, the client does not touch them), a row in §7.

### 2.8 What not to do

- The daemon does not keep a snapshot of the system DNS and does not know its format — that is
  the script.
- Do not add `pre_stop`, `pre_reload` or other `pre_*`: see §1.
- Do not pass anything from the client to the hooks: no body, no parameters, no name other than
  `LXD_CLIENT`.
- Do not add routes for running hooks by hand — `commands` exist for that.
- Do not touch upstream files beyond the single §2.3 accessor.
- Do not plan Windows in this task.

## 3. Acceptance criteria

- Table-driven config test (`daemonconfig_test.go`): no key / `enabled: false` → no hooks and one
  Info line; `timeout_seconds` 0 → 15; `unsafe_skip_owner_check: true` → Warn at start.
- Stub test: on an empty state dir the daemon creates the directory `0755` and four files `0755`,
  byte-for-byte equal to the references; an existing file is not overwritten; a hook equal to the
  reference yields `skipped: not configured`.
- Event test on a fake `reloader` (like `fakeReloader` in `apply_test.go`) with a script that
  writes its environment to a file: the sequence `boot` → `apply` → `stop` yields
  `pre_start(boot)`, `post_start(boot)`, `post_reload(apply)`, `post_stop(stop)` with the correct
  `LXD_REASON`, `LXD_CONFIG_SHA`/`_PREV`, `LXD_TUN_ADDRESS`/`_PREV` (a config with two tun
  addresses), `LXD_TUN_AUTO_ROUTE`, empty `LXD_TUN_*` without a tun inbound; `LXD_CLIENT` only on
  REST.
- Failure test: `pre_start` with `exit 1` → `StartOrReloadService` not called, `status: fatal`,
  `last_error` names the hook; `post_start` with `exit 2` → the core keeps running,
  `hooks.post_start.state = failed`, `exit_code: 2`; `post_stop` with `exit 1` → the stop went
  through, log only; timeout (`sleep 60` with `timeout_seconds: 1`) → `state: timeout`, the daemon
  waits no longer than ~1 s, the child is killed.
- `fatal` test: `pre_start` passed, the `reloader` returned an error → `post_stop(fatal)` with
  `LXD_ERROR`.
- `rollback` test: apply failed, last-good came up → one `post_reload(rollback)` with the current
  last-good facts and `LXD_ERROR`; last-good failed too → `post_stop(fatal)`.
- `core_snapshot.json` test: after `post_start` the file exists and holds the facts; after `post_stop`
  there is no file; a new controller over a state dir with a leftover file calls
  `post_stop(recover)` with `_PREV` from the file **before** `pre_start`, the file is deleted.
- Output test: a hook with `echo hi; (sleep 30 &)` finishes without waiting for the background,
  `hi` is in `<event>.log`.
- Gate test: a file with `0777`, a file in a group-writable directory, a symlink → `failed` with
  the `checkRootOwnedChain` text; with `unsafe_skip_owner_check: true` — it runs. On `pre_start` —
  the start is cancelled.
- `flock` test: two simulated processes (or two controllers over one directory) do not run hooks
  concurrently.
- Accessor test: `InterfaceName()` on a tun inbound returns `tunOptions.Name` after start; the
  build under `with_lxd` on darwin/linux/windows is green, `go vet` over the lx files is clean.
- The existing 056/057/128 tests are green.
- Manual check by the owner: the Mac launcher, `post_start` points the network service's DNS at
  `LXD_TUN_ADDRESS`, `post_stop` restores it from the snapshot; `kill -9` of the daemon → after a
  service restart `post_stop(recover)` restored the DNS, then a regular start. On RouteRich:
  `update` via `commands` → the log shows `post_stop(shutdown)` of the old daemon and
  `post_start(boot)` of the new one without overlap.

## 4. Consumer (for reference, outside the core scope)

The launcher, Service window: `hooks` in `/admin/status` → a yellow mark next to the core state
when any `post_*` is `failed`/`timeout`, with `detail` in the tooltip; `skipped: not configured`
gives no mark. A collection of sample scripts for macOS/Linux/OpenWrt is on the documentation and
launcher side.
