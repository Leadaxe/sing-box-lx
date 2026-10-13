# IMPLEMENTATION REPORT: 128 — LXD_COMMANDS

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `lxd/commands.go` | `validateCommands` (regexp имени, непустой текст). `commandRunner`: `newCommandRunner` создаёт каталог исхода и удаляет файлы имён, которых нет в конфиге; `view(name, withLog)` выводит состояние из файлов — `.json` нет → `idle`; `.exit` есть → `done` (`finished_at` = mtime, `exit_code` из содержимого); иначе pid жив → `running`, мёртв → `lost`; `start` под мьютексом: `404`/`409`, чистит `.exit`/`.log`, спавнит, пишет `.json` (`started_at`, `pid`, `client`), Info-строка аудита. `commandEnv`: `LXD_PLATFORM/VERSION/INIT_SYSTEM/STATE_DIR/BIN` + `PATH` при пустом; `LXD_CLIENT` добавляется в `start`. `commandOutcomeDir`: `dirname(log_file)/commands`, без лога — `<state>/commands`. Handlers: список (сортировка по имени, без лога), GET по имени (хвост 64 КиБ), POST (непустое тело → `400 "commands take no input"`). `clientNameOf`: имя по отпечатку сертификата, иначе `operator` |
| `lxd/commands_unix.go` | `spawnDetached`: `/bin/sh -c` обёртка с `Setsid`, cwd `/`, stdio закрыт; команда и пути передаются через `LXD_CMD*` в окружении обёртки, та переносит их в локальные переменные и `unset` — команда видит только `LXD_*` демона; горутина `Wait` снимает зомби |
| `lxd/commands_windows.go` | `commandsSupported = false`; `spawnDetached` — ошибка (не вызывается) |
| `lxd/daemonconfig.go` | `Commands map[string]string json:"commands"`; `LoadDaemonConfig` валидирует — демон не стартует на плохом имени |
| `lxd/daemon.go` | `Options.Commands`; в `Run`: при непустом — runner (каталог рядом с `options.LogFile`), на Windows — Warn «ignored on this platform» |
| `lxd/apply.go`, `lxd/admin.go` | поле `commands`; три маршрута на общем mux под общим гейтом |
| `lxd/clients.go` | `nameOf(fingerprint) (string, bool)` |
| `cmd/sing-box/cmd_lxd_lx.go` | `Commands: fileConfig.Commands` в `Options` (не гейтится `installed`) |
| `lxd/commands_test.go` | `TestValidateCommands`; `TestCommandRunRecordsOutcome` (list `idle`, `202`, `done`, `exit_code 3`, лог stdout+stderr, `finished_at`); `TestCommandEnvironment` (`env` → все шесть `LXD_*`, без утечки `LXD_CMD*`); `TestCommandConflictAndDetached` (`409` во время `sleep`, `Getsid(pid) == pid`, новый runner над тем же каталогом читает `running`, мёртвый pid без `.exit` → `lost`); `TestCommandRoutesAndInput` (`404` на чужое имя, `400` на тело, без ключа — все три маршрута `404`); `TestCommandOutcomeDirAndCleanup` |

Апстримных файлов ноль.

## Проверка

- `gofmt -l lxd/ cmd/` — пусто.
- `go vet -tags with_lxd,with_lx_command ./lxd/ ./cmd/sing-box/` для `GOOS=darwin|linux|windows` — чисто.
- `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8, darwin/arm64) — ok.

## Открыто

- Инсталлятор OpenWrt и живая проверка — после 129, одним прогоном до тега.
- Переиспользование pid после ребута может показать чужой процесс как `running` для старого `.json`; на роутере каталог исхода на tmpfs и очищается ребутом, в state-dir — редкий косметический случай.
