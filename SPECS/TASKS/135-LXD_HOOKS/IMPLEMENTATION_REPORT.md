# IMPLEMENTATION REPORT: 135 — LXD_HOOKS

**Feature:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · contract — [SPEC.md](SPEC.md) · plan — [PLAN.md](PLAN.md)

Implemented 2026-10-17 on branch `lx`, behind `with_lxd`.

## What was done

| File | Content |
|------|---------|
| `lxd/hooks.go` (new) | `HooksConfig` + `validateHooks` (negative `timeout_seconds` refuses the start naming the key; 0/absent = 15 s). `hookRunner`: `newHookRunner` creates `<state_dir>/hooks` `0755`, a stub `0755` for every missing event, the log directory (`<dirname(log_file)>/hooks`, or `hooks/log` without a log file); Info `lxd: hooks disabled` when off, Warn on `unsafe_skip_owner_check`. `configured` = file present and not byte-equal to the embedded stub; `gate` = `checkRootOwnedChain` + regular + executable, before **every** run; `fire` builds the §2.3 environment (every `LXD_*` always set, empty when n/a), runs under the flock, records the outcome (`ok`/`failed`/`timeout`/`skipped`/`never`, `reason`, `exit_code`, `finished_at`, `detail`) and logs Info/Warn/Error per §2.6 with the output tail (≤ 4 KiB). `preStart` (returns the fail-closed verdict), `postStart`/`postReload` (write `core_snapshot.json` first, then run), `postStop` (run, then remove `core_snapshot.json`), `recoverAtBoot` (leftover `core_snapshot.json` → `post_stop(recover)` with `_PREV` from the file; with hooks disabled the file is removed silently). `configFacts`: tun facts from the config JSON tree (`sing/common/json` `UnmarshalExtended`, comments tolerated; `address` as string or list; mtu default 9000; tags as the box names them) plus actual names through `tunNameSource`. `hookStub`: reference stubs with the variables of their event in the comment. `hookOrigin` (reason + client) travels in the context |
| `lxd/hooks_unix.go` (new) | `runHookProcess`: `exec.Command(path)`, cwd `/`, stdin `/dev/null`, stdout/stderr to `<event>.log` (a file, not a pipe), `Setpgid`, `markInheritableCloseOnExec` before start, timeout → `SIGKILL` to the group; `withHookLock`: `flock(LOCK_EX)` on `hooks/.lock` (close-on-exec) |
| `lxd/hooks_other.go` (new) | `hooksSupported = false`; stubs |
| `lxd/daemonconfig.go` | `Hooks *HooksConfig` (`hooks`), validated in `LoadDaemonConfig` |
| `lxd/daemon.go` | `Options.Hooks`; runner creation in `Run` (Warn `hooks are ignored on this platform` when the key is present on a non-unix build); `shutdown` takes the controller: live facts → `CloseService` → `post_stop(shutdown)` → control planes; `bootstrap`: `recoverAtBoot` first, `pre_start(boot)` before `startRetryingBusy` (fail-closed → `setFatal`), `post_stop(fatal)` after a failed boot, `post_start(boot)` after a successful one; SIGHUP applies with origin `sighup` |
| `lxd/apply.go` | `controller.hooks`; `liveFacts`/`announceSwap`/`announceFatal`; `Apply`: facts of the live core taken before the swap, `pre_start` when no core runs, `post_reload(apply|sighup)` / `post_start` after the swap, `post_reload(rollback)` with `LXD_ERROR` after a successful rollback, `post_stop(fatal)` when nothing remains; `Rollback`: with a live core a reload (`post_reload`, reason `rollback`), without one a start (`pre_start`/`post_start`, reason `start`); `Stop` → `stopFrom(origin)` with `post_stop(stop)` after `CloseService` |
| `lxd/admin.go` | apply/start/stop/rollback handlers pass the origin with `clientNameOf`; `hooks` object in `GET /admin/status` |
| `lxd/statsource.go` | `TunInterfaceNames(tags)` on `observableService`: the box's inbound manager by tag, `Type() == tun`, the lx accessor |
| `protocol/tun/inbound.go` | `// lx:begin lxd_hooks` … `InterfaceName() string` … `// lx:end lxd_hooks` — the only upstream insertion |
| `cmd/sing-box/cmd_lxd_lx.go` | `Hooks: fileConfig.Hooks` |
| `scripts-lx/openwrt/lxd-openwrt-setup.sh`, `-ru.sh` | `procd_set_param term_timeout 30` with a comment |
| `scripts-lx/openwrt/README.md`, `README.ru.md` | a paragraph on the hook directory under "Service and logs" |
| `docs-lx/lxd-daemon.md`, `.ru.md` | `hooks` row in §3; new §6a "Hooks" (events, reasons, variables, idempotency contract, background tail, stop budget, samples for macOS and `systemd-resolved`); `hooks` in the §10 status row; TOC |
| `docs-lx/lx-changelog.md` | item under "Не выпущено (после v1.14.3-lx.21)" (the file is Russian by its own rule) |
| `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `SPECS/README.md` | 135 → I |
| `lxd/hooks_test.go` (new, unix), `protocol/tun/inbound_lx_test.go` (new) | tests of §3, see below |

## Spec clarifications made while implementing (both SPEC.md and SPEC.ru.md)

1. **An apply over no core is a start.** `POST /admin/apply` (or SIGHUP) when the daemon is idle, stopped or fatal brings the core up from nothing: `pre_start` gates it and `post_start` answers it, with `LXD_REASON` = `apply` / `sighup`. The spec listed only `boot`/`start` for those events; the alternative — `post_reload` with an empty `_PREV` — would break "the current core is what exists after the event, `_PREV` what no longer exists".
2. **`POST /admin/rollback` over a live core** is a `post_reload` with reason `rollback` and an empty `LXD_ERROR` (a manual rollback); with no core it is a start (`pre_start`/`post_start`, reason `start`).
3. **`post_stop(fatal)` after an apply whose rollback also failed** carries the facts of the instance that was live before the swap (what no longer exists), not of the config that failed — that one never existed. The boot case keeps the spec wording (facts from the config, interface only explicit).
4. **A leftover `core_snapshot.json` with hooks disabled** is removed with a Warn (silent at first; Warn by owner decision 2026-10-18, so the admin knows the system state may need a manual restore): a disabled daemon must not fire `post_stop(recover)` for a long-gone core once hooks are re-enabled.

None of these reopens an owner decision; they fill gaps the code met. The owner may overrule any of them.

## Verification

- `gofmt -l lxd/ protocol/tun/ cmd/sing-box/` — empty.
- `go vet -tags with_lxd ./lxd/ ./protocol/tun/` for `GOOS=darwin`, `GOOS=linux`, `GOOS=windows` — clean (go1.26.8 from `go.version`).
- `go test -tags with_lxd -count=1 ./lxd/ ./protocol/tun/` (darwin/arm64) — ok; the existing 056/057/128 tests included.
- `lxd/hooks_test.go` covers §3: config defaults and `daemon.json` validation (`TestHooksConfigDefaults`); stubs created `0755`, an existing file kept, stub/missing = `skipped: not configured` (`TestHooksStubsCreatedNotOverwritten`); the `boot → apply(REST, client) → stop` sequence with `LXD_REASON`, `LXD_CLIENT`, `LXD_CONFIG_SHA`/`_PREV`, `LXD_TUN_*`/`_PREV` for a two-tun config, empty `LXD_TUN_*` without a tun (`TestHooksEventSequenceBootApplyStop`); `sighup`, manual `rollback`, `start` reasons (`TestHooksSighupAndManualRollbackReasons`); `pre_start` exit 1 → `StartOrReloadService` not called, `status: fatal`, `last_error` names the hook, `hooks.pre_start` in the status, apply over no core gated too (`TestHooksPreStartFailClosed`); `post_start` exit 2 → core keeps running, `failed`, `exit_code 2`; `post_stop` exit 1 → the stop went through (`TestHooksPostStartFailureKeepsCore`); `sleep 60` with `timeout_seconds: 1` → `timeout`, the daemon waits ≈1 s, the hook's shell is dead (`TestHooksTimeoutKillsProcessGroup`); `pre_start` passed, reloader failed → `post_stop(fatal)` with `LXD_ERROR` and explicit-only interface (`TestHooksFatalAfterPreStart`); apply failed + rollback ok → one `post_reload(rollback)` with last-good facts and `LXD_ERROR`; rollback failed too → `post_stop(fatal)` with both errors (`TestHooksRollbackReasons`); `core_snapshot.json` present after `post_start` with the facts, gone after `post_stop`, a new controller over a leftover file runs `post_stop(recover)` with `_PREV` from it before `pre_start` and replaces the file; disabled hooks drop a stale file (`TestHooksStateJSONAndRecover`); `echo hi; (sleep 30 &)` returns at once with `hi` in `post_start.log` (`TestHooksOutputFileNotPipe`); `0777` file, group-writable directory, symlink → `failed` with the `checkRootOwnedChain` text and the start cancelled; with the check off the symlinked hook runs (`TestHooksOwnershipGate`, uid forced to 0 through an injected lstat so the modes are exercised without root); two runners over one directory never run concurrently (`TestHooksFlockSerializesProcesses`); the shutdown order facts → close → `post_stop(shutdown)` (`TestHooksShutdownAnnouncesPostStop`); config parsing with comments, a string `address`, a broken config (`TestHooksConfigFactsParsing`).
- `protocol/tun/inbound_lx_test.go`: `InterfaceName()` returns `tunOptions.Name`.

## Not verified / open

- The manual owner checks of §3: Mac launcher `post_start`/`post_stop` DNS round trip; `kill -9` → `post_stop(recover)` after a service restart; RouteRich `update` → `post_stop(shutdown)` of the old daemon and `post_start(boot)` of the new one without overlap. The flock and the recover order are unit-tested in-process only.
- The actual-name path (`TunInterfaceNames` through a real box) is exercised by type, not by a live tun: a tun needs root.
- Linux and Windows are vetted, not run.
- `launchd` `ExitTimeOut` is documented, not changed by `--service=install`.

## Next-rebase conflict zone

`protocol/tun/inbound.go` — one `// lx:` block after `Tag()`. Everything else is in lx files or `// lx:` lines of lx-owned `lxd/` files.
