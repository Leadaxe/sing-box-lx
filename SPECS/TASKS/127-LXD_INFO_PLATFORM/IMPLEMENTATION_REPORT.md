# IMPLEMENTATION REPORT: 127 — LXD_INFO_PLATFORM

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `lxd/initsystem.go` | `platformInfo` (goos, goarch, platform, initSystem, service). `detectInitSystem(env) (string, bool)`: windows — `scm` / контекст SCM; darwin — `launchd` / `ppid 1` и непустой `XPC_SERVICE_NAME`; linux — `detectLinuxInit` / `ppid 1` при распознанном init; прочие — `""`/`false`. `detectLinuxInit`: `/proc/1/comm` (`systemd`, `procd`), затем `/etc/openwrt_release` → `procd`, каталог `/run/systemd/system` → `systemd`, каталог `/run/openrc` → `openrc`. `releasePlatform(goos, goarch, settings)`: `armv<GOARM>` (часть до запятой), `-<GOMIPS>`/`-<GOMIPS64>`, `windows-386-legacy-windows-7`; `buildSettings` читает `debug.ReadBuildInfo` — проверено, что GOARM/GOMIPS записываются всегда, дефолты включая |
| `lxd/initsystem_linux.go`, `_darwin.go`, `_windows.go`, `_stub.go` | `platformInitSystemEnv()`; linux — `var linuxInitProbe` (реальные `readPid1Comm`/`statPathKind`), тесты рецептов подменяют её |
| `lxd/service_linux.go` | `detectInit()` = обёртка над `detectLinuxInit(linuxInitProbe())` с маппингом в `initSystem` (`openrc` → `initUnknown`: рецепта нет); переменные `pid1CommPath`/`openwrtReleasePath`/`systemdRunPath` удалены |
| `lxd/service_linux_test.go` | `fakeInit` строит `linuxInitEnv` в памяти вместо temp-файлов; `TestDetectInitPrefersPID1` переехал |
| `lxd/hostinfo.go`, `lxd/hostinfo_linux.go` | `parseOSRelease` без изменений — в файл без платформенного тега |
| `lxd/apply.go`, `lxd/daemon.go`, `lxd/admin.go` | `controller.platform = readPlatformInfo()` в `Run`; `handleInfo` отдаёт `goos`, `goarch`, `platform`, `init_system`, `service` |
| `lxd/initsystem_test.go` | `TestDetectLinuxInit` (9: pid 1 бьёт метки, fallback-пробы, openrc под sysvinit, шим контейнера), `TestDetectInitSystem` (13: все строки §2.2, запуск руками на procd/systemd — init назван, `service=false`), `TestReleasePlatform` (12 имён матрицы) |
| `lxd/admin_test.go` | `TestAdminInfoEndpoint` — пять полей; `TestAdminInfoExecutableHash` — без карточки `""`/`false`, ключи есть |

Апстримных файлов ноль. Константы `initNameProcd`… — чтобы не путать с типом `initSystem` рецепта.

## Проверка

- `gofmt -l lxd/` — пусто.
- `go vet -tags with_lxd,with_lx_command ./lxd/` для `GOOS=darwin|linux|windows|freebsd` — чисто.
- `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8, darwin/arm64) — ok; `GOOS=linux go test … -exec /usr/bin/true` (компиляция linux-тестов рецептов) — ok.
- `go build ./cmd/sing-box` с полным набором тегов — ok.
- `debug.ReadBuildInfo` на кросс-сборках: `GOARM=7` без явного GOARM, `GOMIPS=hardfloat` без явного GOMIPS — настройки записаны всегда.

## Открыто

- Ручная проверка владельцем после тега (§3). Linux-тесты рецептов на маке только компилируются — прогон на linux-хосте в CI (`lx-ci`).
