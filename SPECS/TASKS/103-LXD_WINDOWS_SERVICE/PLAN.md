# PLAN: 103 — LXD_WINDOWS_SERVICE

## Файлы

| Файл | Тег | Содержимое |
|------|-----|-----------|
| `lxd/aclcheck.go` | `with_lxd` | портируемый предикат инварианта 2.2: `aclViolation(path, ownerSID, aces, level)` над строками SID и масками (свои константы, без `x/sys/windows`); уровни «предок» и «копия»; allowlist SID; тексты ошибок |
| `lxd/aclcheck_test.go` | `with_lxd` | табличный тест предиката — на Linux в CI |
| `lxd/execset.go` | `with_lxd` | набор копии: сайдкар `installSetMarker` (`source`, `version`, `installed_at`, `service`, `files[{name, sha256}]`), чтение/запись temp+rename, хеш набора, сравнение наборов, классификация файлов каталога (член, сайдкар, остаток `.old`/temp, лишний), решения uninstall/`--keep-copy` по набору |
| `lxd/execset_test.go` | `with_lxd` | сайдкар, идемпотентность, решения uninstall/keep-copy, лишние файлы — на Linux |
| `lxd/execcopy.go` | `with_lxd` | развязка имён: `execCopyBase = "sing-box-lxd"`, `execCopyName` = база + суффикс exe платформы, `installMarkerName = execCopyBase + ".install.json"`; на darwin значения прежние |
| `lxd/execcopy_test.go` | `with_lxd` | golden-тест JSON сайдкара macOS |
| `lxd/execsafe.go` | `with_lxd` | `selfCheckEnv` обобщается: `privileged`, `serviceContext`, `serviceLabel`, `check func(path) error` вместо `euid`/`ppid`/`xpcService`; `evaluateSelfCheck` возвращает и строку `INFO` для пройденной проверки в контексте службы; `ServiceVerdict`, тяжесть, коды — без изменений |
| `lxd/execsafe_unix.go` | `with_lxd && unix` | `lstatOwner` как есть; сборка `selfCheckEnv` из euid, ppid, `XPC_SERVICE_NAME` (семантика SPEC 100 §2.8 не меняется) |
| `lxd/execsafe_windows.go` | `with_lxd && windows` | чтение владельца и DACL с дескриптора без следования ссылкам, перевод в вход предиката; цепочка от корня тома; проверка тома (`GetDriveType`, `GetVolumeInformation`); включение привилегий токена; `selfCheckEnv` из `IsElevated()` и `svc.IsWindowsService()` |
| `lxd/execsafe_other.go` | `with_lxd && !unix && !windows` | заглушка как сейчас |
| `lxd/privilege_unix.go`, `lxd/privilege_windows.go`, `lxd/privilege_other.go` | `with_lxd` + платформа | `ServiceActionPrivileged() bool` и текст подсказки (`sudo` / `run as administrator`) |
| `lxd/service_windows.go` | `with_lxd && windows` | пути (`KnownFolderPath`), `windowsServiceEnv` с интерфейсами `scmAPI` (open/create/update/delete/start/stop/query/config/security) и `aclAPI`; `PrepareServiceHome` (2.3); размещение набора (2.4); `InstallService`, `InstallServiceCopy`, `UninstallService`, `ServiceStatus`, `InstallUserService` (ошибка), `DefaultServiceStateDir`, `ServiceInstallIsAdvisory = false`, `ServiceRestartCommand`, `ServiceDefaultLogFile`; судьи вердиктов; dry-run-планы |
| `lxd/service_windows_test.go` | `with_lxd && windows` | переходы состояний и аномалии через подменённые `scmAPI`/`aclAPI` во временном каталоге; `ComposeCommandLine`/`DecomposeCommandLine`; живой тест под `LX_WINDOWS_SCM_LIVE=1` |
| `lxd/svcrun_windows.go` | `with_lxd && windows` | `IsWindowsService()`, обработчик `svc.Handler` (2.10), `RunService(prepare, run)` |
| `lxd/svcrun_other.go` | `with_lxd && !windows` | `IsWindowsService() = false`, `RunService` не вызывается |
| `lxd/logredirect_windows.go` | `with_lxd && windows` | `logRotationSupported = true`; открытие одного дескриптора, `SetStdHandle`, подмена `os.Stdout`/`os.Stderr`, `log.SetStdLogger`; ротация копированием с усечением |
| `lxd/logrotate.go` | `with_lxd` | шов стратегии ротации: `rename` (Unix, как сейчас) или копирование с усечением (Windows); проверка «лог удалили» только при `rename` |
| `lxd/logredirect_stub.go`, `lxd/service_stub.go` | `with_lxd && !darwin && !linux && !windows` | сужение тегов |
| `lxd/service_darwin.go`, `lxd/service_linux.go` | | `ServiceRestartCommand`, `ServiceDefaultLogFile` (пусто), `PrepareServiceHome` (no-op) |
| `lxd/daemon.go` | `with_lxd` | ветка `<-ctx.Done()` в цикле `Run` с разбором как у SIGTERM |
| `lxd/invitefile_unix.go`, `lxd/invitefile_windows.go` | `with_lxd` + платформа | `CreateInviteFile(path)`: `O_EXCL|O_NOFOLLOW` / `CREATE_NEW` + сверка `GetFinalPathNameByHandle` |
| `cmd/sing-box/cmd_lxd_lx.go` | `with_lxd` | флаги `--invite-out`, `--invite-name` (у `lxd`) и `--invite-out` (у `client add`); `ServiceActionPrivileged` вместо `os.Getuid()`; `PrepareServiceHome` до `prepareServiceConfig`; `log_file` по `ServiceDefaultLogFile`; строка рестарта и подсказка минта по платформе; код 1 при провале минта с `--invite-out`; ветка SCM в `lxdMain` (`RunService`); ошибка доступа к `daemon.json` в `clientStateDir`; `install-user` на Windows — ошибка |
| `cmd/sing-box/cmd_lxd_lx_test.go` | `with_lxd` | флаги приглашения, выбор имени, решения печати |
| `cmd/sing-box/dllsearch_windows_lx.go` | `with_lxd && windows` | `init()`: `SetDefaultDllDirectories` с предварительным `Find()` процедуры |
| `cmd/sing-box/cronetpin_windows_lx.go` | `with_lxd && windows && with_purego && with_naive_outbound` | §4.2 п. 2: закрепление отказа `cronet.LoadLibrary` при отсутствии `libcronet.dll` рядом с exe |
| `.github/workflows/lx-ci.yml` | | шаг `GOOS=windows go vet` в lint; джоба `test-windows` на `windows-latest` (§4.2 п. 9) |
| `docs-lx/lxd-daemon.md`, `docs-lx/lxd-daemon.ru.md`, `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `docs-lx/lx-changelog.md` (секция `v1.14.2-lx.2`) | | документация (TASKS п. 9) |

Апстримных файлов — ноль. `x/sys/windows/svc` и `svc/mgr` уже в модуле (`golang.org/x/sys
v0.47.0`, их использует `experimental/boxdd`); новых зависимостей нет. Привилегии токена
включаются через `x/sys/windows` (`LookupPrivilegeValue`, `AdjustTokenPrivileges`), без
`go-winio`.

## Ключевые решения

- **Отдельный сайдкар для Windows.** Общая структура с `omitempty` на новых полях оставила
  бы macOS-JSON прежним, но Windows-сайдкар нёс бы пустые `sha256`, `plist_path`, `label`,
  которых нет в контракте SPEC 141 §3 п. 3. Отдельная `installSetMarker` в портируемом
  `execset.go`: macOS-сайдкар и его решения (`removeInstalledCopy`, `unbindInstalledCopy`)
  не трогаются, решения по набору тестируются на Linux. Цена — две параллельные ветки
  решений uninstall; общие у них `sha256File`, запись temp+rename и тексты строк.
- **Предикат ACL на строках.** SID как строки, маски — свои константы. Тест идёт на
  Linux без Windows, как у предиката SPEC 100. Чтение ACL и перевод в эти типы — в
  `execsafe_windows.go`, покрыт Windows-тестом.
- **`windowsServiceEnv`** повторяет `serviceEnv` darwin: SCM и чтение ACL — интерфейсы,
  файловые операции — настоящие во временном каталоге. Табличный тест переходов идёт на
  `windows-latest` без реальной службы.
- **status открывает SCM сам** (`OpenSCManager(SC_MANAGER_CONNECT)`,
  `OpenService(QUERY_CONFIG|QUERY_STATUS|READ_CONTROL)`) и оборачивает дескриптор в
  `mgr.Service`: `mgr.Connect()` без прав не работает.
- **Порядок install на Windows:** права → каталог данных (2.3) → `daemon.json` → stop →
  набор → конфигурация службы → start → status → приглашение. Каталог данных раньше
  `daemon.json`, потому что `prepareServiceConfig` пишет секрет.
- **Лог — один дескриптор и ротация копированием.** `rename` живого файла на Windows
  невозможен без `FILE_SHARE_DELETE` и оставил бы писателей на старом файле; канал через
  pipe отвергнут: при `throw` рантайм останавливает мир, горутина-насос не читает, запись
  паники в заполненный pipe вешает процесс, и SCM не видит падения.
- **Контекст службы на Windows** = команда `lxd` и `svc.IsWindowsService()`. Имя службы
  по процессу не определяется дёшево; `lxd`, поставленный службой под другим именем,
  тоже получает строгий режим.
- **Ветка `ctx.Done()` в `Run`** общая для всех платформ: на Unix отмену никто не
  вызывает, поведение не меняется.
- **`--invite-name` только у install.** У `client add` имя уже задаёт `--name`
  (`cmd_lxd_lx.go:117`); лаунчер вызывает `client add --name singbox-launcher --invite-out`
  (SPEC 141 §5.1). Дефолт `singbox-launcher` действует только с `--invite-out`, чтобы
  приглашение install на macOS без флагов не меняло имя клиента.

## Риски

- **Таймаут старта SCM (1053).** Инициализация пакетов sing-box до `main` и чтение
  `daemon.json` должны уложиться в 30 с до `StartPending`; проверяется живым прогоном.
- **Потеря строк при ротации** в окне между копированием и усечением — принятая цена
  схемы копирования.
- **Захват `ProgramData` через TOCTOU.** Обход идёт по дескрипторам, открытым без
  следования ссылкам, владение забирается раньше обхода детей; жёсткие ссылки и reparse
  points — отказ. Остаётся открытый чужой дескриптор, взятый до захвата: он переживает
  смену DACL. Вопрос секрета — §4.2 п. 6.
- **Джоба `test-windows`.** Windows-раннер медленнее Linux; сборка с полным набором тегов и
  тесты — по оценке 10–15 мин на прогон. Репозиторий публичный, минуты бесплатны; дольше
  15 мин — сузить до `workflow_dispatch` и тегов (§4.2 п. 9). Права администратора у
  раннера проверяются первым шагом, живой SCM-тест без них пропускается.
- **Лаунчер и `classic.log`, остатки `.old`** — требуют правок SPEC 141 (§4.2 п. 4, 5);
  до них лаунчер может показывать `Stale` после обновления при живом classic.
- **Повторный install при открытой оснастке «Службы»** (1072) — ошибка с подсказкой,
  автоматического обхода нет.
