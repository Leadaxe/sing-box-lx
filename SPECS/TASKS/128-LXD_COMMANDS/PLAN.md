# PLAN: 128 — LXD_COMMANDS

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `lxd/commands.go` | `validateCommands`, `commandRunner` (каталог исхода, `view` из файлов, `start` под мьютексом), `commandEnv`, `commandOutcomeDir`, три handler'а, `clientNameOf` |
| `lxd/commands_unix.go`, `lxd/commands_windows.go` | `spawnDetached` (`/bin/sh` обёртка, `Setsid`, переменные обёртки не утекают в команду); Windows — `commandsSupported = false` |
| `lxd/daemonconfig.go` | `Commands map[string]string`, валидация в `LoadDaemonConfig` |
| `lxd/daemon.go`, `lxd/apply.go`, `lxd/admin.go` | `Options.Commands`, `controller.commands`, маршруты под `if c.commands != nil` |
| `lxd/clients.go` | `nameOf(fingerprint)` для аудита и `LXD_CLIENT` |
| `cmd/sing-box/cmd_lxd_lx.go` | `Commands: fileConfig.Commands` |
| `lxd/commands_test.go` | тесты §3 |
| `docs-lx/lxd-daemon.md`, `.ru.md` | строка `commands` в §3, три маршрута в §10, §8.5 |
| `docs-lx/lx-changelog.md`, `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `SPECS/README.md` | строки 128 |

## Что не меняется

- Гейт admin-плоскости, `/admin/apply`, `/admin/logs`; апстримных файлов ноль.

## Проверка

- `gofmt`, `go vet` (darwin, `GOOS=linux/windows`), `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/`.
