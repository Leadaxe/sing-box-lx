# PLAN: 135 — LXD_HOOKS

**Feature:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · contract — [SPEC.md](SPEC.md)

## Files

| File | What changes |
|------|--------------|
| `lxd/hooks.go` | `HooksConfig` validation; `hookRunner` (directory and stub creation, "not configured" by byte-equality with the embedded stubs, ownership gate, per-event outcomes for `/admin/status`, `core_snapshot.json` write/remove, `recover` at boot); `coreFacts` (tun facts parsed from the config JSON tree, actual interface names from the box); the environment of §2.3; `hookOrigin` carried in the context (reason + client) |
| `lxd/hooks_unix.go` | `runHookProcess`: `exec.Command(path)` in its own process group, cwd `/`, stdin `/dev/null`, stdout/stderr to `<log dir>/hooks/<event>.log`, `flock` on `hooks/.lock`, `markInheritableCloseOnExec`, timeout → `SIGKILL` to the group |
| `lxd/hooks_other.go` | Windows and the rest: `hooksSupported = false`, no runner |
| `lxd/daemonconfig.go` | `Hooks *HooksConfig` (`enabled`, `timeout_seconds`, `unsafe_skip_owner_check`), validated in `LoadDaemonConfig` |
| `lxd/daemon.go` | `Options.Hooks`; runner creation in `Run` (Warn on Windows); `post_stop(recover)` at the top of `bootstrap`; `pre_start`/`post_start`/`post_stop(fatal)` around `startRetryingBusy`; `post_stop(shutdown)` inside `shutdown`; SIGHUP origin |
| `lxd/apply.go` | `controller.hooks`; `post_reload(apply|sighup|rollback)` and `post_stop(fatal)` in `Apply`; `Rollback` splits into reload (`post_reload`) and start (`pre_start`/`post_start`/`post_stop(fatal)`); `post_stop(stop)` in `Stop` |
| `lxd/admin.go` | request origin (client name) into the context for apply/start/stop/rollback; `hooks` object in `GET /admin/status` |
| `lxd/statsource.go` | `TunInterfaceNames(tags)` on `observableService` — the actual names from the box's inbound manager |
| `protocol/tun/inbound.go` | one `// lx:` accessor `InterfaceName() string` |
| `cmd/sing-box/cmd_lxd_lx.go` | `Hooks: fileConfig.Hooks` |
| `scripts-lx/openwrt/lxd-openwrt-setup.sh`, `-ru.sh` | `procd_set_param term_timeout 30` |
| `scripts-lx/openwrt/README.md`, `README.ru.md` | a paragraph on the hook directory |
| `docs-lx/lxd-daemon.md`, `.ru.md` | `hooks` in §3, a "Hooks" section after §6, the `hooks` field in §10 |
| `docs-lx/lx-changelog.md` | an item linking the SPEC |
| `lxd/hooks_test.go`, `lxd/daemonconfig_test.go`, `protocol/tun/inbound_lx_test.go` | tests of §3 |

## Decisions taken while implementing

- Reason and client travel in the request context (`withHookOrigin`), so the `Apply`/`Rollback`/`Stop` signatures stay as the 056/057 tests use them; a context without an origin means `apply` with no client.
- `POST /admin/rollback` and `POST /admin/start` share `Rollback`: with a live core it is a reload (`post_reload`, reason `rollback`, `LXD_ERROR` empty — a manual rollback), without one it is a start (`pre_start`/`post_start`, reason `start`).
- `post_stop(fatal)` after a failed apply carries the facts of the instance that was live before the swap (what no longer exists), not of the config that failed — that one never existed; the boot case keeps the spec wording (facts from the config, interface only explicit).
- Facts of a live instance are collected before the swap/close (`liveFacts`): config facts from the active content plus actual names via `TunInterfaceNames`.
- With `enabled: false` a leftover `core_snapshot.json` is removed silently: a disabled daemon makes no promise about hooks and must not call `post_stop(recover)` later when they are re-enabled for a core that is long gone.

## Not changed

- The admin gate, `/admin/info`, the gRPC plane; upstream files beyond the single accessor.

## Verification

- `gofmt`, `go vet -tags with_lxd` (darwin, `GOOS=linux`, `GOOS=windows`), `go test -tags with_lxd ./lxd/ ./protocol/tun/` with the toolchain of `go.version`.
