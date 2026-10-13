# PLAN: 127 — LXD_INFO_PLATFORM

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `lxd/initsystem.go` | `platformInfo`, `initSystemEnv`/`linuxInitEnv`, чистые `detectInitSystem` (init хоста + `service`), `detectLinuxInit` (общий детектор), `releasePlatform` по build info, `readPlatformInfo` |
| `lxd/initsystem_{linux,darwin,windows,stub}.go` | `platformInitSystemEnv()`: ppid, `XPC_SERVICE_NAME`, `IsWindowsService()`; linux — `linuxInitProbe` (подменяемая в тестах рецептов) |
| `lxd/service_linux.go` | `detectInit` — обёртка над `detectLinuxInit`; path-переменные проб удалены |
| `lxd/service_linux_test.go` | `fakeInit` подменяет `linuxInitProbe`; `TestDetectInitPrefersPID1` переехал в `initsystem_test.go` |
| `lxd/hostinfo.go`, `lxd/hostinfo_linux.go` | `parseOSRelease` — в платформонезависимый файл (тесты os-release на любом хосте) |
| `lxd/apply.go`, `lxd/daemon.go`, `lxd/admin.go` | `controller.platform`, считается в `Run`; пять полей в `handleInfo` |
| `lxd/initsystem_test.go`, `lxd/admin_test.go` | табличные тесты трёх чистых функций; поля в тестах `/admin/info` |
| `docs-lx/lxd-daemon.md`, `.ru.md` | строка `GET /admin/info`, фраза в §6 |
| `docs-lx/lx-changelog.md`, `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `SPECS/README.md` | строки 127 |

## Что не меняется

- `/admin/host`, `daemon.json`, флаги; рецепты `--service` (только источник детекции).

## Проверка

- `gofmt`, `go vet` (darwin и `GOOS=linux/windows/freebsd` с `with_lxd`), `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8), компиляция linux-тестов, сборка `./cmd/sing-box`.
