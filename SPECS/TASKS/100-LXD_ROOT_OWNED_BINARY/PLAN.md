# PLAN: 100 — LXD_ROOT_OWNED_BINARY

## Файлы

| Файл | Тег | Содержимое |
|------|-----|-----------|
| `lxd/execsafe.go` | `with_lxd` | ярлык `launchdLabel`; предикат `rootOwnedViolation` (uid, режим → ошибка); обход `checkRootOwnedChain`; проход с созданием каталогов `ensureRootOwnedDir`; `ServiceVerdict` с порядком тяжести и кодами выхода; самопроверка `CheckServiceExecutable` / `evaluateSelfCheck` |
| `lxd/execsafe_unix.go` | `with_lxd && unix` | `lstatOwner`: uid/gid/режим из `syscall.Stat_t` |
| `lxd/execsafe_other.go` | `with_lxd && !unix` | `lstatOwner` — ошибка «не поддерживается» |
| `lxd/execcopy.go` | `with_lxd` | `resolveOwnExecutable`, `sha256File`, `installExecCopy` (temp+fsync+chown+chmod+verify+rename, dry-run, крючок `beforeVerify`), сайдкар `installMarker` (запись temp+rename / чтение), решение uninstall `removeInstalledCopy`, `executableIdentity` (фоновый хеш для `/admin/info`) |
| `lxd/execsafe_test.go`, `lxd/execcopy_test.go` | `with_lxd` | предикат, цепочка, каталог, самопроверка; копирование, сайдкар, решения uninstall — на любом CI-хосте без root |
| `lxd/service_darwin.go` | `with_lxd && darwin` | `serviceEnv` (источник, root/chown, области, launchctl — подменяемы); `placeCopy` (общий шаг install/copy); `InstallService`, `InstallServiceCopy`, `UninstallService`, `ServiceStatus`; dry-run-планы; отчёт status и судьи `judgeDaemon`/`judgeCopyOnly`/`judgeUserAgent`; разбор plist (`Program`/`ProgramArguments`, binary plist через `plutil`) и `launchctl print` |
| `lxd/service_darwin_test.go` | `with_lxd && darwin` | табличный тест переходов состояний и аномалий через `serviceEnv` во временном каталоге; plist туда-обратно; dry-run install/copy; `launchctl print` |
| `lxd/service_linux.go`, `lxd/service_stub.go` | | сигнатуры `InstallService`/`UninstallService` (+`execDir`); `InstallServiceCopy`, `ServiceStatus` — «macOS only» |
| `lxd/service_linux_test.go` | | вызовы под новую сигнатуру |
| `lxd/apply.go`, `lxd/daemon.go`, `lxd/admin.go`, `lxd/admin_test.go` | | поле `executable` контроллера, фоновый хеш при старте, `executable`/`executable_sha256` в `/admin/info` |
| `cmd/sing-box/cmd_lxd_lx.go` | `with_lxd` | `--service=copy|status`, `--exec-dir`, `--allow-unsafe-exec`; код выхода status; самопроверка перед стартом демона; обёртка `commandRun.Run` с той же проверкой (только `WARN` вне службы) |
| `.github/workflows/lx-ci.yml` | | шаг `GOOS=darwin go vet ./lxd/ ./cmd/sing-box/` в lint |

Апстримных файлов — ноль (`cmd_run.go` не тронут: `Run` оборачивается из `init()` нашего файла).

## Ключевые решения

- **Предикат отделён от stat.** `rootOwnedViolation(path, uid, mode)` не трогает диск;
  обходы берут stat из переменной пакета `ownerLstat` — тесты подставляют таблицу или
  «root владеет всем» (реальное дерево с uid 0).
- **Копирование пишет шаги в `io.Writer`**: install печатает в stdout, тест читает буфер.
  `chown` — опцией (есть root), dry-run — опцией, `beforeVerify` портит временный файл
  в тесте расхождения sha.
- **Один шаг размещения** (`placeCopy`) для install и copy: разница только в
  `plist_path` сайдкара. Отсюда «copy → install без повторного копирования» и no-op
  повторного copy.
- **`serviceEnv`** отделяет state machine от машины: источник, флаги root/chown,
  области, `launchctl` — поля. Табличный тест гоняет настоящие install/copy/uninstall/
  status во временном каталоге без root и launchd.
- **Сайдкар несёт `plist_path` и `label`**: uninstall снимает только свою копию; status
  отличает installed, copy only и полуразобранные состояния.
- **Контекст службы** = `ppid == 1 && XPC_SERVICE_NAME == label`: launchd выставляет эту
  переменную каждому job'у; `nohup`/двойной fork дают ppid 1 без метки. Строгий отказ —
  только для службы.
- **Хеш для `/admin/info` — в фоне**: на роутере sha256 40-МБ бинаря — секунды, а канал
  управления поднимается первым (FEATURE 014 §4).
- **status без root**: plist, сайдкар и копия читаются всеми; `launchctl print system/…`
  работает без root.
- **Коды 3 и 4** (NOT INSTALLED, COPY ONLY) отделены и от `OK`, и от «нужна
  переустановка»: лаунчеру в режиме службы и в classic нужны разные ответы.

## Риски

- Существующие системные установки не стартуют после обновления бинаря до
  переустановки — релиз-ноты и дока; лаунчер после бампа пина видит `UNSAFE` в status и
  переустанавливает.
- Разбор `launchctl print` — текстовый формат Apple без контракта; используется только
  для печати, вердикт от него не зависит.
