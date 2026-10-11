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

## Проверка

- `gofmt -l lxd/ cmd/` — пусто.
- `go vet -tags with_lxd,with_lx_command ./lxd/ ./cmd/sing-box/` для `GOOS=darwin|linux|windows`; `go build ./cmd/sing-box` без тегов (заглушка `versionExtraLx`) — чисто.
- `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8) — ok.
- Полная сборка на маке: `sing-box version` печатает `Platform: darwin-arm64`, `Init: launchd`, без `Update:`; `sing-box lxd update --dry-run` отказывает: «works on linux only», выход 1.

## Открыто

- Живой прогон на роутере (lx.N-1 → новый) — до тега, вместе со 127/128.
- `Bfree` считает место для root; на ФС с резервом для root это верно для нашего процесса.
