# PLAN: 129 — LXD_UPDATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `lxd/update.go` | чистые `parseLxVersion`/`compareLxVersion`, `parseChecksums`, `extractBinary` (поток sha256 → gzip → tar → `.new`), `serviceCommands`; оркестратор `runUpdate(env, options)` с инжектируемым `updateEnv`; `Update()` — реальное окружение (HTTP-клиент с таймаутом 10 мин, прокси из env) |
| `lxd/update_unix.go`, `lxd/update_windows.go` | `freeBytes` (`Statfs`), `runCombined`, `runtimeGOOS/GOARCH` |
| `lxd/initsystem.go` | `PlatformSummary()` для `sing-box version` |
| `cmd/sing-box/cmd_lxd_lx.go` | подкоманда `lxd update` с флагами; `Long` у `lxd` про `commands` и `update` |
| `cmd/sing-box/cmd_version.go` | одна `// lx:`-строка-хук `versionExtraLx()` |
| `cmd/sing-box/cmd_version_lx.go`, `cmd_version_nolx.go` | тело хука под `with_lxd` / пустая заглушка |
| `scripts-lx/openwrt/lxd-openwrt-setup*.sh`, `README*.md` | `commands.update` в `daemon.json`; строки в «Служба и логи» |
| `lxd/update_test.go` | тесты §3 |
| `docs-lx/lxd-daemon.md`, `.ru.md` | строка в §4, §8.4, §8.6 |
| `docs-lx/lx-changelog.md`, `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `SPECS/README.md` | строки 129 и абзац «127→128→129» |

## Что не меняется

- `daemon.json`, state, ресурсы, init-скрипт; macOS/Windows пути установки.

## Проверка

- `gofmt`, `go vet` (darwin, `GOOS=linux/windows`), `go build ./cmd/sing-box` без тегов и с полным набором, `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/`.
