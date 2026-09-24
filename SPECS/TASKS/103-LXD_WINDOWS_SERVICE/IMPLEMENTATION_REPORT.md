# IMPLEMENTATION_REPORT: 103 — LXD_WINDOWS_SERVICE

Ветка `spec103-lxd-windows-service` от `lx` (база `85c19039f`, после `v1.14.2-lx.1`), запушена
в `origin`, в `lx` не влита. Цель — `v1.14.2-lx.2`; тег режет ведущая сессия.

## Коммиты

| Коммит | Что |
|---|---|
| `9123230f3` | спека: `warnings[]` в сайдкаре для install без консоли; норма имени клиента на минте (контракт лаунчера) |
| `51256d547` | портируемое: предикат ACL, набор копии и сайдкар `files[]`, файл приглашения, предикат «привилегирован» |
| `de27b27bc` | портируемая половина в существующих файлах демона: развязка имён копии и сайдкара, ветка `ctx.Done()` в `lxd.Run`, строка `INFO` самопроверки, норма имени и замена клиента именным enroll, шов ротации копированием |
| `baf8f097f` | служба Windows: SCM за интерфейсом, захват `ProgramData`, размещение набора, install/copy/uninstall/status, обработчик `svc.Run`, лог одним дескриптором |
| `7ca5e67a1` | заглушки сужены до `!darwin && !linux && !windows` |
| `2930abff5` | CLI: `--invite-out`, `--invite-name`, `client add --invite-out`, ветка SCM в `lxdMain`, платформенные тексты флагов и строки рестарта |
| `a628aa155` | `SetDefaultDllDirectories` и закрепление `libcronet.dll` для привилегированного ядра |
| `48862e84e` | lx-ci: `GOOS=windows go vet` в lint, джоба `test-windows` |
| `b55311bbe` | узлы каталога данных ниже корня получают явный защищённый DACL; владелец SYSTEM у них допустим |
| `7caf5afc1` | файл приглашения принимает короткие имена 8.3 в запрошенном пути |
| `e92704231` | тесты с чтением файла `chmod 0` пропускаются на Windows |
| (этот) | доки `lxd-daemon(.ru).md`, фича 014, changelog `v1.14.2-lx.2`, SPEC/TASKS/README, отчёт |

## Файлы

- **Новые, портируемые (`with_lxd`):** `lxd/aclcheck.go` (предикат инварианта 2.2, DACL службы,
  норма узла каталога данных) + тест; `lxd/execset.go` (набор, сайдкар `installSetMarker`,
  классификация файлов каталога копии, размещение с `.old`/temp/sha/rename и откатом, решения
  uninstall и `--keep-copy`) + тест; `lxd/invitefile.go` (`NormalizeClientName`, запись и
  удаление файла приглашения) + `invitefile_unix.go`/`_windows.go`/`_other.go` + тест;
  `lxd/privilege_unix.go`/`_windows.go`/`_other.go`; `lxd/service_hooks_other.go` (хуки команды
  вне Windows с прежним поведением); `lxd/svcrun_other.go`; `lxd/selfcheck_test.go`.
- **Новые, Windows (`with_lxd && windows`):** `lxd/service_windows.go` (пути, `windowsServiceEnv`,
  install/copy/uninstall/status, судья вердиктов), `lxd/scm_windows.go` (`scmAPI`, `systemSCM`:
  query без прав, stop/start с ожиданием, configure с recovery и DACL, remove),
  `lxd/datadir_windows.go` (захват `ProgramData`), `lxd/execsafe_windows.go` (чтение владельца и
  DACL с дескриптора, цепочка от тома, NTFS/fixed, самопроверка), `lxd/svcrun_windows.go`
  (обработчик SCM) + тест, `lxd/logredirect_windows.go`; `cmd/sing-box/dllsearch_windows_lx.go`,
  `cmd/sing-box/cronetpin_windows_lx.go` (плюс `with_purego && with_naive_outbound`).
- **Правки наших файлов:** `lxd/execcopy.go` (`execCopyBase`, `installMarkerName`), `lxd/execsafe.go`
  (`selfCheckEnv` обобщён, `verdictLine`, `reportLine`), `lxd/execsafe_unix.go`,
  `lxd/execsafe_other.go`, `lxd/daemon.go` (ветка отмены контекста), `lxd/clients.go` (замена
  клиента именным enroll), `lxd/admin.go` (400 на имя вне нормы), `lxd/logrotate.go` (стратегия
  копированием), `lxd/logredirect_stub.go`, `lxd/service_stub.go`, `lxd/service_darwin.go`
  (хуки вынесены), `cmd/sing-box/cmd_lxd_lx.go`; тесты `admin`, `clients`, `daemon`,
  `daemonconfig`, `execcopy`, `execsafe`, `logrotate`, `store`, `tlsca`, `cmd_lxd_lx`.
- **`// lx:` правки апстрима:** нет.
- **deps / go.mod:** нет (`x/sys/windows/svc`, `svc/mgr` уже в модуле).
- **CI:** `.github/workflows/lx-ci.yml` — шаг `GOOS=windows go vet` в lint, джоба `test-windows`.
- **Доки:** `docs-lx/lxd-daemon.md`, `docs-lx/lxd-daemon.ru.md` (раздел 7a, ключи §4, логи §6,
  сопряжение §9), `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `docs-lx/lx-changelog.md`,
  `SPECS/README.md`.

## Проверки

- lx-ci, прогон `36028821033` (`workflow_dispatch` на `e92704231`): зелёные `lint (vet + gofmt)`
  (включая `GOOS=darwin` и `GOOS=windows` vet), `build-check`, `test-windows (lxd + cli)` на
  `windows-latest`, `cross` windows/amd64, windows/arm64, darwin/amd64, darwin/arm64, linux/amd64,
  linux/arm64, `linux-musl` ×4. `android libbox.aar` на момент записи ещё шёл.
- Тесты на Linux (портируемые): предикат ACL (`TestACLPredicate`, `TestServiceACL`,
  `TestDataNodeProtected`, `TestCopyNodeInNorm`, `TestACLForeignReaders`), набор и сайдкар
  (`TestSetSidecarRoundTripAndKeys`, `TestSetPlacementAndIdempotency`,
  `TestSetDroppedLibraryAndLeftovers`, `TestSetUnknownFileRefuses`,
  `TestSetVerifyFailureAndRollback`, `TestSetImageStillInUse`, `TestSetUninstallDecisions`,
  `TestSetKeepCopyDecisions`, `TestSetReason`), golden сайдкара macOS (`TestSidecarGoldenJSON`),
  приглашение (`TestInviteFileExclusive`, `TestInviteFileRefusesLink`, `TestNormalizeClientName`,
  `TestInstallInviteName`), `TestRunReturnsOnContextCancel`, `TestSelfCheckContexts`,
  `TestLogRotateByCopy`, `TestEnrollNamedInviteReplaces`, `TestAdminClientCodeNameNorm`.
- Тесты на Windows (`test-windows`): те же портируемые под семантикой Windows плюс
  `TestServiceHandlerExecute` (порядок статусов, `Interrogate`, сторож остановки) и
  `TestServiceCommandLineRoundTrip` (`ComposeCommandLine` → `DecomposeCommandLine` для путей с
  пробелами).
- Локально на Mac владельца сборка и тесты не гонялись (диск полон), проверка — через CI.

## Не проверено без живой Windows (§4.3 SPEC)

- `stdoutIsTerminal()` под SCM (ожидается `false`: пустой дескриптор, `Stat` с ошибкой) и
  перехват лога в `logs\lxd.log`;
- время до `RUNNING` против таймаута старта SCM (1053);
- `rename` работающего `sing-box-lxd.exe` и загруженной `libcronet.dll` при повторном install;
- захват `<ProgramData>\sing-box-lxd`, заранее созданного обычным пользователем, и строка `WARN`
  с записью в `warnings` сайдкара;
- ротация `lxd.log` копированием на живом демоне;
- `GetFinalPathNameByHandle` при junction в пути `--invite-out`;
- права администратора у раннера `windows-latest` (живой SCM-тест не заведён, см. ниже);
- весь сценарий §5 п. 8: install → status `OK` → повторный install → copy → uninstall
  `--keep-copy` → `COPY ONLY` → uninstall → `NOT INSTALLED`, `sc qc`/`qfailure`/`sdshow`,
  `icacls`, `Get-FileHash`, `sc stop` → `NOT RUNNING`, `sc config binPath=` на чужой exe →
  `UNSAFE`, сопряжение лаунчера через `--invite-out`. За лаунчер-сессией на Windows (TASKS п. 10);
  после него — статус D.

## Отклонения от SPEC

- **§2.3, DACL узлов ниже корня.** Вместо «у файлов — наследуемые ACE родителя» узел вне нормы
  получает явный защищённый DACL (`dataDirSDDL`/`dataFileSDDL`) одним вызовом: промежуточный
  пустой или только наследуемый DACL мог бы читаться как NULL DACL. Владелец SYSTEM ниже корня —
  норма, файлы демона не переписываются на каждом install. SPEC §2.3 поправлен.
- **§5 п. 2, табличный тест переходов состояний на Windows** через подменные `scmAPI`/`windowsFS`
  не написан: интерфейсы есть (`windowsServiceEnv`), но `lxd/service_windows_test.go` в ветке нет.
  Переходы и аномалии вердиктов покрыты только косвенно (портируемые решения по набору и
  `setReason`); судья `judge` Windows-тестом не покрыт.
- **§4.2 п. 9 и §5 п. 7, живой SCM-тест под `LX_WINDOWS_SCM_LIVE=1`** в `test-windows` не
  заведён: джоба гоняет `go vet` и `go test` без реальной службы, служба подтверждается живым
  прогоном §5 п. 8.
- **§2.13, `--invite-out` у install на Linux** — отказ (`--invite-out needs a platform where
  --service=install installs the service (macOS, Windows)`): install на Linux печатает рецепт, и
  демона для минта нет. `client add --invite-out` работает на всех платформах.
- **§2.13, короткие имена 8.3.** Сверка итогового пути файла приглашения принимает запрошенный путь
  в форме 8.3 (`GetLongPathName` раскрывает имена, не проходя reparse points), иначе раннер
  `windows-latest` с `C:\Users\RUNNER~1` ложно отказывал. Junction по-прежнему отказ.
- **status при отказе в `READ_CONTROL`.** Если службу нельзя открыть с `READ_CONTROL`, она
  открывается только с правами запроса, а DACL службы считается нечитаемым → `UNSAFE` с причиной
  (у нашей службы AU имеет `READ_CONTROL`, так что это случай чужого DACL).
- **Нумерация в `lxd-daemon(.ru).md`.** Раздел Windows — `7a` (с `7a.1`, `7a.2`), а не новый §8:
  рецепт Linux в `lxd/service_linux.go` печатает ссылки на `#82-…`/`#83-…` (проверены
  `service_linux_test.go`), и уже выпущенные бинари ведут на них же; перенумерация Linux-разделов
  сломала бы эти ссылки. Стиль повторяет существующие `10a`/`10b`.

## DoD

1. Сборка без тегов форка — `lxd` отсутствует, апстрим не тронут. ✔ (`build-check`, negative)
2. Сборка с тегами на всех desktop-целях. ✔ (`cross`, `linux-musl`)
3. `go vet` и `go test` по затронутым пакетам на Linux, darwin-vet, Windows-vet и Windows-тесты. ✔ (CI)
4. `sing-box check` не затронут (новых ключей конфига нет; `log_file` в daemon.json существовал). ✔
5. Правок апстрим-файлов нет. ✔
6. TASKS.md отражает факт, отчёт заполнен. ✔
7. Статус — **I**: живой прогон на Windows (§5 п. 8) — за лаунчер-сессией.

## Зона конфликтов при мерже апстрима

Нет: код в `lxd/`, `cmd/sing-box/cmd_lxd_lx.go` и новых `cmd/sing-box/*_windows_lx.go`. Точки
внимания — апстримная `experimental/boxdd/security_windows.go` (модель инварианта; код не
копировался) и `golang.org/x/sys/windows/svc/mgr`: при бампе `x/sys` сверить `mgr.Service` над
своим дескриптором и `SetRecoveryActionsOnNonCrashFailures`.

## Вне скоупа

- Подпись Authenticode копии; блокировка одновременных install/copy; проверка предков
  `<ProgramData>` (§4.1).
- Event Log; служба в сессии пользователя; named pipe вместо TCP loopback.
- Linux-служба: по-прежнему печать рецепта.
