# IMPLEMENTATION REPORT: 129 — LXD_UPDATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `lxd/update.go` | `parseLxVersion` (`vX.Y.Z-lx.N[-rc.M]`), `compareLxVersion` (semver → lx → rc, без rc новее). `parseChecksums`: строка `sing-box-<ver>-<platform>.tar.gz` по точному суффиксу (не подстроке — `mips` ≠ `mipsle`), `*` binary-mode допускается. `extractBinary`: `TeeReader` в sha256 → gzip → tar, пишется только регулярная запись с базовым именем `sing-box` (вторая — отказ), поток дочитывается до конца, несовпадение суммы удаляет `.new`. `serviceCommands`: procd/systemd/openrc. `runUpdate` — §2.2 целиком: гварды goos/euid, очистка `.new`/`.old`, `SHA256SUMS` (stable — `releases/latest/download`, `--prerelease` — тег из API), сравнение версий (не-релизная установленная — предупреждение и продолжение), место ≥ размер + 10 %, `--dry-run`, скачивание `releases/download/v<ver>/<asset>`, `version`/`check` новым бинарём (без `last_good.json` — строка `skipping check`), `link`+`rename`, рестарт, провал команды restart → бинарь остаётся, выход 0; здоровье 15×1 с + 3 с; откат `rename(.old)` + повторный restart, выход 2. `Update()`: HTTP-клиент с таймаутом 10 мин, `User-Agent`, не-200 — ошибка; прокси — дефолтный транспорт (`ProxyFromEnvironment`) |
| `lxd/update_unix.go`, `_windows.go` | `freeBytes` через `Statfs` (`Bfree×Bsize` — процесс root); `runCombined` |
| `lxd/initsystem.go` | `PlatformSummary()` |
| `cmd/sing-box/cmd_lxd_lx.go` | `commandLxdUpdate`: `--dry-run`, `--prerelease`, `--no-restart`, `--service` (дефолт `sing-box-lxd`); state-dir — явный `--state-dir`, иначе `FindServiceStateDir()`, иначе пусто (check пропускается); `os.Exit(code)`, ошибка в stderr как `update: ERROR: …`; `Long` с кодами выхода и примером `commands`. `Long` у `lxd` упоминает `update` и `commands` |
| `cmd/sing-box/cmd_version.go` | `// lx:begin lxd` … `version += versionExtraLx()` … `// lx:end lxd` — единственная правка апстримного файла |
| `cmd/sing-box/cmd_version_lx.go`, `cmd_version_nolx.go` | `Platform:` всегда, `Init:` при распознанном, `Update:` только на linux; без `with_lxd` — пусто |
| `scripts-lx/openwrt/lxd-openwrt-setup.sh`, `-ru.sh` | `"commands": {"update": "$BIN lxd update"}` в heredoc `daemon.json`; README EN/RU — две строки в «Служба и логи» |
| `lxd/update_test.go` | порядок версий и отказ мусора; `SHA256SUMS` (mips vs mipsle, `*`, нет платформы, zip не ассет); распаковка (содержимое, `0755`, несовпадение удаляет, два бинаря); `fakeUpdateHost` (URL→байты, скриптованные ответы `running`, падения команд): успех (точная последовательность вызовов, `done`, без хвостов), откат (старое содержимое, второй restart, `skipping check`), restart упал (новый бинарь, без health-проб), `--no-restart`/неизвестный init (init не трогается), `--dry-run`/актуально/даунгрейд, `--prerelease`, гварды goos/euid/место/нет ассета ничего не меняют; `serviceCommands` |

## Довесок 2026-10-12: гонка рестарта под procd (§2.6)

Живой прогон lx.15-test128 → lx.16 на RouteRich: update вернул `done`, а ядро нового демона не
поднялось (`TUNSETIFF: device or resource busy` — старый процесс ещё держал `lxd-tun0`), VPN
лежал ~80 с до ручного `restart`.

| Файл | Содержимое |
|------|-----------|
| `lxd/daemon.go` | `startRetryingBusy`: в `bootstrap` старт повторяется каждые 500 мс до 10 с, пока `isDeviceBusy(err)` (`errors.Is(err, syscall.EBUSY)` по цепочке `E.Cause` либо текст `device or resource busy`); первая попытка — `WARN … retrying for up to 10s`; пауза — `bootBusyRetrySleep` (ctx-aware, тесты подменяют); другая ошибка — окончательна сразу |
| `lxd/admin.go` | `GET /admin/status` на операторском mux тоже; корневой demux `pinnedOrOperator`: без сертификата с loopback-пира → операторский путь (секрет), иначе → пин (удалённый без сертификата получает прежний `401`); `isLoopbackPeer` вынесен из `requireLoopback` |
| `lxd/localclient.go` | `CoreStatus{Status, LastError}`, `(*LocalClient).Status()` — `GET /admin/status`, не-200 — ошибка с текстом демона |
| `lxd/update.go` | `updateEnv.coreStatus`/`coreStatusSkip`; шаг 8b `waitForCore`: 21 проба с паузой 1 с (счётчик, не часы — тесты мгновенны), `started` → ok, `fatal` → ошибка с `last_error`, `idle` до конца → `still idle after 20s`, без единого ответа → `core status unknown`, выход 0; провал ядра идёт в общий откат шага 8 с текстом `<ver> is up but the core did not start: …`. `coreProbe(stateDir)`: `daemon.json` → `loopbackAdminAddress` (loopback как есть, `0.0.0.0`/`::`/пустой хост → `127.0.0.1`/`::1`, только LAN → нет пробы), run-state `stopped` → нет пробы; `NewLocalClient(address, secret, tls)` |
| `lxd/bootstrap_test.go` | `TestBootstrapRetriesBusyDevice` (`busyReloader` со скриптованными ошибками) |
| `lxd/admin_test.go` | `TestAdminStatusOperatorRoute` |
| `lxd/update_test.go` | `TestUpdateWaitsForCore` (4 сценария + «пробы нет»), `TestLoopbackAdminAddress` (+ `coreProbe`) |
| `cmd/sing-box/cmd_lxd_lx.go` | `Long`: ожидание ядра |
| `docs-lx/lxd-daemon.md`, `.ru.md` | §8.6 шаг 7 → 7–8; операторские маршруты — `/admin/status`; `lx-changelog.md` Unreleased |

## Проверка

- `gofmt -l lxd/ cmd/` — пусто.
- `go vet -tags with_lxd,with_lx_command ./lxd/ ./cmd/sing-box/` для `GOOS=darwin|linux|windows`; `go build ./cmd/sing-box` без тегов (заглушка `versionExtraLx`) — чисто.
- `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8) — ok.
- Полная сборка на маке: `sing-box version` печатает `Platform: darwin-arm64`, `Init: launchd`, без `Update:`; `sing-box lxd update --dry-run` отказывает: «works on linux only», выход 1.

## Открыто

- Живой прогон lx.15-test128 → lx.16 на RouteRich 2026-10-12 — прошёл (подмена, сумма, procd, `/admin/commands` на lx.16 = 200, паспорт платформы пришёл), вскрыл §2.6. Ретрай `EBUSY` и шаг 8b на роутере ещё не прогонялись — следующий update (lx.16 → lx.17) должен показать `core started` в `update.log` и ни одной строки `TUNSETIFF` в `lxd.log` либо один `WARN … retrying`.
- `Bfree` считает место для root; на ФС с резервом для root это верно для нашего процесса.
