# PLAN: 100 — LXD_ROOT_OWNED_BINARY

## Файлы

| Файл | Тег | Содержимое |
|------|-----|-----------|
| `lxd/execsafe.go` | `with_lxd` | предикат `rootOwnedViolation` (uid, режим → ошибка), обход цепочки `checkRootOwnedChain`, проход с созданием каталогов `ensureRootOwnedDir`, самопроверка `CheckServiceExecutable` / `evaluateSelfCheck`, `ServiceVerdict` и коды выхода, ярлык `launchdLabel` |
| `lxd/execsafe_unix.go` | `with_lxd && unix` | `lstatOwner`: uid/gid/режим из `syscall.Stat_t` |
| `lxd/execsafe_other.go` | `with_lxd && !unix` | `lstatOwner` — ошибка «не поддерживается» |
| `lxd/execcopy.go` | `with_lxd` | `sha256File`, `installExecCopy` (temp+fsync+chown+chmod+verify+rename, dry-run), сайдкар `installMarker` (запись/чтение), решение uninstall `removeInstalledCopy`, `executableIdentity` для `/admin/info` |
| `lxd/execsafe_test.go`, `lxd/execcopy_test.go` | `with_lxd` | табличные тесты предиката/цепочки/самопроверки; копирование, сайдкар, uninstall-сверка во временном каталоге |
| `lxd/service_darwin.go` | `with_lxd && darwin` | install: каталог копии, копия, сайдкар, chown support-каталога, plist с копией, отчёт status; dry-run с планом; uninstall с кандидатами копий; `ServiceStatus`; разбор plist (`ProgramArguments`/`Program`, binary plist через `plutil`) и `launchctl print` |
| `lxd/service_darwin_test.go` | `with_lxd && darwin` | plist туда-обратно, dry-run system-install, разбор `launchctl print` |
| `lxd/service_linux.go`, `lxd/service_stub.go` | | новые сигнатуры `InstallService`/`UninstallService` (+`execDir`), `ServiceStatus` — ошибка «macOS only» |
| `lxd/service_linux_test.go` | | вызовы под новую сигнатуру |
| `lxd/apply.go`, `lxd/daemon.go`, `lxd/admin.go`, `lxd/admin_test.go` | | поле `executable` у контроллера, фоновый хеш при старте, два поля `/admin/info` |
| `cmd/sing-box/cmd_lxd_lx.go` | `with_lxd` | флаги `--exec-dir`, `--allow-unsafe-exec`; `--service=status` с кодом выхода; самопроверка до старта демона |
| `.github/workflows/lx-ci.yml` | | шаг `GOOS=darwin go vet ./lxd/ ./cmd/sing-box/` в lint |

Апстримных файлов — ноль.

## Ключевые решения

- **Предикат отделён от stat.** `rootOwnedViolation(path, uid, mode)` не трогает диск;
  `checkRootOwnedChain(path, lstat)` получает функцию stat — тест подставляет таблицу.
  Реальный stat — `lstatOwner` за `unix`; для тестов darwin-кода есть переменная
  пакета `ownerLstat`.
- **Копирование пишет шаги в `io.Writer`**: install печатает в stdout, тест читает буфер.
  `chown` включается опцией (install под root), dry-run — опцией, крючок
  `beforeVerify` портит временный файл в тесте расхождения sha.
- **Сайдкар несёт `plist_path` и `label`**: uninstall снимает только копию, чей сайдкар
  указывает на снимаемый plist, и только при совпадении sha.
- **Контекст службы** = `ppid == 1 && XPC_SERVICE_NAME == label`: launchd выставляет эту
  переменную каждому job'у; `nohup`/двойной fork дают ppid 1 без метки. Строгий отказ
  только для службы — ручной запуск под sudo остаётся отладочным путём с `WARN`.
- **Хеш для `/admin/info` — в фоне**: на роутере sha256 40-МБ бинаря — секунды, а канал
  управления поднимается первым (FEATURE 014 §4).
- **status без root**: plist, сайдкар и копия читаются всеми; `launchctl print system/…`
  работает без root.
- **NOT INSTALLED = код 3**: отличим и от `OK`, и от «нужна переустановка».

## Риски

- Существующие системные установки не стартуют после обновления бинаря до
  переустановки — описано в релиз-нотах и доке; лаунчер после бампа пина видит
  `UNSAFE` в status и переустанавливает.
- Разбор `launchctl print` — текстовый формат Apple без контракта; используется
  только для печати (state, pid, program), вердикт от него не зависит.
