# SPEC: 103 — LXD_WINDOWS_SERVICE

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — `lxd` как служба Windows (SCM) с защищённой копией ядра, перенос модели SPEC 100 |
| Статус | I (implemented) — код, тесты, CI и доки в ветке; выйдет в `v1.14.2-lx.2`, живой прогон §5 п. 8 — за лаунчер-сессией на Windows |
| Ветка | `spec103-lxd-windows-service` (от `lx`) |
| База | `85c19039f` (после `v1.14.2-lx.1`) |
| Релиз | цель `v1.14.2-lx.2` |
| Связано | [100](../100-LXD_ROOT_OWNED_BINARY/SPEC.md) (модель копии, сайдкар, вердикты, коды выхода), [057](../057-LXD_MTLS_SERVICE/SPEC.md) (`--service`, `daemon.json`, приглашение), [065](../065-LXD_OBSERVABILITY_PLANE/SPEC.md) (`/admin/logs`); лаунчер: SPEC 141 (пара этой задачи, §3 — согласованный интерфейс), SPEC 139 (лаунчер без прав), SPEC 140 (установщик) |

Решение владельца 2026-09-24: «делаем как вин служба». Интерфейс с лаунчером
согласован сессией ядра 2026-09-24 (SPEC 141 лаунчера, §3).

**Цена мержа:** ноль апстримных файлов. Правки — пакет `lxd/` (новые файлы с тегом
`with_lxd && windows`, развязка имён в `execcopy.go`, ветка отмены в `daemon.go`),
`cmd/sing-box/cmd_lxd_lx.go` и новые `cmd/sing-box/*_windows_lx.go`,
`.github/workflows/lx-ci.yml`, документация.

---

## 1. Мотивация

На Windows `lxd` собирается (релизные `windows-amd64`/`windows-arm64` несут `with_lxd`), но
всё, что делает его службой, — заглушки:

- `lxd/service_stub.go` (`with_lxd && !darwin && !linux`): `--service=install|copy|uninstall|status`
  возвращают «macOS only», `ServiceInstallIsAdvisory = true`.
- `lxd/logredirect_stub.go`: `logRotationSupported = false`, демон не владеет логом.
- `lxd/execsafe_other.go`: `lstatOwner` всегда ошибка; `CheckServiceExecutable` читает
  `os.Geteuid()`, на Windows это `-1`, поэтому самопроверка не выполняется вовсе.

Лаунчер на Windows запускает ядро с правами администратора из
`%LOCALAPPDATA%\singbox-launcher\bin` — каталога, куда пишет любой процесс пользователя
(SPEC 141 лаунчера §1). Подмена `sing-box.exe` или `libcronet.dll` там даёт код с высокой
целостностью на ближайшем старте VPN: тот же класс дефекта, что закрыл SPEC 100 на macOS.
Вторая причина — VPN до входа пользователя: нужна служба с автозапуском, которая поднимает
last-good без лаунчера.

## 2. Решение: служба SCM исполняет защищённую копию

Семантика SPEC 100 переносится один к одному: launchd → SCM, uid 0 и режим → владелец и
DACL, `ProgramArguments[0]` → argv[0] из `BinaryPathName`, копия одного файла → набор файлов.
Состояния, вердикты, коды выхода и тексты строк `lxd:` — те же, где не сказано иначе.

### 2.1 Пути

Корни берутся через `windows.KnownFolderPath` (`FOLDERID_ProgramFiles`,
`FOLDERID_ProgramData`); `C:\` ниже — только пример.

| Что | Путь | Владелец / DACL |
|---|---|---|
| каталог копии | `<ProgramFiles>\sing-box-lxd\` | Administrators; `D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;AU)` |
| исполняемый файл | `…\sing-box-lxd\sing-box-lxd.exe` | Administrators; `D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FRFX;;;AU)` |
| библиотека | `…\sing-box-lxd\libcronet.dll` — только если лежит рядом с источником | как у exe |
| сайдкар | `…\sing-box-lxd\sing-box-lxd.install.json` | как у exe |
| каталог данных | `<ProgramData>\sing-box-lxd\` | Administrators; `D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)` (наследуемый) |
| state | `<ProgramData>\sing-box-lxd\state\` = `DefaultServiceStateDir(false)`: `daemon.json`, tls, клиенты, `resources`, `tailscale` | наследует |
| логи | `<ProgramData>\sing-box-lxd\logs\`: `lxd.log` демона (+ `lxd.log.1`); `classic.log` там же пишет лаунчер | наследует |

Чтение и исполнение набора для Authenticated Users (`FRFX`, без битов записи) нужно
лаунчеру без прав: он считает sha набора и читает сайдкар (SPEC 141 §6). Инварианту 2.2 это
не противоречит: чужому SID запрещена запись, чтение разрешено.

`--exec-dir <dir>` переопределяет каталог копии, как на macOS. В отличие от
`/Library/PrivilegedHelperTools`, дефолтный каталог — наш: install и copy его создают.
`wintun.dll` в набор не входит: `sing-tun` несёт его через `go:embed`
(`internal/wintun/dll_windows_*.go`) и загружает из памяти (`memmod`), не с диска.

Имя службы SCM — `sing-box-lxd` (совпадает с базовым именем копии; ярлык launchd
`com.leadaxe.sing-box-lxd` на Windows не используется). В `lxd/execcopy.go` сайдкар сейчас
называется `execCopyName + ".install.json"`; с `.exe` в имени копии это дало бы
`sing-box-lxd.exe.install.json`, поэтому базовое имя и имя файла копии нужно развязать
(на macOS имена не меняются).

### 2.2 Инвариант «защищённый путь»

Модель — `experimental/boxdd/security_windows.go` апстрима; код не копируется, пишется своя
реализация на тех же вызовах `x/sys/windows`.

Разрешённые SID (allowlist): SYSTEM (`S-1-5-18`), Administrators (`S-1-5-32-544`),
TrustedInstaller (`S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464`).
Любой другой SID — «чужой».

| Звено | Владелец | Чужому SID в DACL запрещено |
|---|---|---|
| предки: от корня тома до родителя каталога копии | из allowlist | `DELETE`, `WRITE_DAC`, `WRITE_OWNER`, `GENERIC_WRITE`, `GENERIC_ALL`, `FILE_DELETE_CHILD` |
| каталог копии и каждый файл набора | SYSTEM или Administrators | сверх того `FILE_WRITE_DATA`/`FILE_ADD_FILE`, `FILE_APPEND_DATA`/`FILE_ADD_SUBDIRECTORY`, `FILE_WRITE_EA`, `FILE_WRITE_ATTRIBUTES` |

Правила обхода:

- каждое звено открывается без следования ссылкам (`FILE_FLAG_OPEN_REPARSE_POINT`,
  `FILE_FLAG_BACKUP_SEMANTICS`), владелец и DACL читаются с этого же дескриптора; reparse
  point в любом звене — нарушение;
- каталоги — каталоги, файлы набора — обычные файлы;
- учитываются ACE типа `ACCESS_ALLOWED` без `INHERIT_ONLY`; `ACCESS_DENIED` не мешает;
  любой иной разрешающий тип (object, callback) — нарушение (fail closed); NULL DACL —
  нарушение;
- том каталога копии — `DRIVE_FIXED` и NTFS.

Маска предков пропускает `FILE_ADD_SUBDIRECTORY` у Authenticated Users на корне тома:
создание папок в `C:\` не даёт подменить наш путь.

Ошибка называет путь и субъект: `<путь>: owner <имя> (<SID>) is not SYSTEM, Administrators
or TrustedInstaller`; `<путь>: <имя> (<SID>) is granted <права>, must not be writable by a
non-administrative principal`; `<путь>: is a reparse point, must be a real file or
directory`; `<путь>: not on a fixed NTFS volume`.

Предикат отделён от чтения ACL: он принимает строку SID владельца и список ACE (тип,
флаги, маска, строка SID) и не трогает систему, поэтому его табличный тест идёт на Linux в
CI. Портируемые части SPEC 100 — `ServiceVerdict`, порядок тяжести, коды выхода,
`evaluateSelfCheck` — остаются общими; на Windows вместо `execsafe_other.go` собирается
`execsafe_windows.go`.

### 2.3 Каталог данных: захват владения

install и copy приводят `<ProgramData>\sing-box-lxd` к норме до любой записи в него
(в том числе до `daemon.json`: файл с секретом, созданный в каталоге с чужим владельцем,
унаследовал бы чужой DACL). По дереву, каждое звено — с дескриптора без следования ссылкам:

1. Включить в токене `SeTakeOwnershipPrivilege` и `SeRestorePrivilege`.
2. Корень, затем `state\`, `logs\` (создаются, если нет) и всё содержимое: корень —
   владелец Administrators и защищённый наследуемый DACL (2.1); узел ниже, выбившийся из
   нормы, получает владельца Administrators и собственный явный защищённый DACL (каталог —
   `D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)`, файл — `D:P(A;;FA;;;SY)(A;;FA;;;BA)`), а не
   наследуемые ACE: промежуточный пустой или только наследуемый DACL мог бы читаться как
   NULL DACL до применения наследования. Владелец SYSTEM у файлов и каталогов, созданных демоном с наследуемыми ACE
   корня, — норма, такие узлы не трогаются.
3. Строки вывода — по одной на звено, где что-то поменялось:
   `lxd: took ownership of <путь> (was <имя> (<SID>))`, `lxd: replaced DACL on <путь>`.
   Уже приведённое дерево — одна строка `lxd: data dir <путь> is protected`.

Отказ — если владение забрать нельзя (привилегия не включилась, ошибка API), а также:
reparse point внутри дерева (`<путь> is a reparse point; remove it: Remove-Item <путь>` —
смена ACL через ссылку поменяла бы чужой объект, а запись демона под SYSTEM ушла бы по ней)
и файл с числом жёстких ссылок больше одного (тот же довод). Сами install и copy такое не
удаляют.

### 2.4 Размещение набора (общее для install и copy)

1. Источник — `EvalSymlinks(os.Executable())`, обычный файл не больше 512 МиБ; набор
   источника = он плюс `libcronet.dll` из того же каталога, если файл есть.
2. Каталог копии: предки проходят инвариант 2.2 (нарушение — отказ до любых изменений);
   сам каталог создаётся с DACL 2.1, существующий приводится к норме (владелец, DACL) со
   строками вывода; reparse point на его месте — отказ.
3. Остатки прошлого прогона удаляются: `<член набора>.old` и временные файлы
   `.<член набора>.tmp-<hex>`. Не удалился (образ ещё исполняется) — строка
   `lxd: <путь> is still in use, left for the next install`.
4. Лишние файлы: всё, что не член текущего набора, не сайдкар и не остаток п. 3. Файл,
   который назван в прошлом сайдкаре и выпал из набора (например, `libcronet.dll`
   прошлого билда), удаляется. Незнакомый файл — отказ:
   `<путь>: unknown file in the copy directory; remove it: Remove-Item <путь>` (аналог
   «legacy layout» SPEC 100 §2.1).
5. Идемпотентность по набору: имена членов и sha256 каждого совпадают с источником —
   файлы не трогаются, `lxd: binary set unchanged (sha256 <hex exe>[, libcronet.dll <hex>]), copy skipped`.
6. Замена члена набора: временный файл в том же каталоге (`CREATE_NEW`) → копия байтов →
   `FlushFileBuffers` → DACL 2.1 → sha256 копии == источника (иначе временный файл
   удаляется, отказ) → если целевой файл есть: `rename` в `<имя>.old` → `rename` временного
   на место → удалить `<имя>.old`. Перезаписи на месте нет: исполняемый образ нельзя
   открыть на запись и нельзя удалить, но можно переименовать.
7. Сайдкар (2.5) — после набора, временным файлом и `rename`.

### 2.5 Сайдкар Windows

```json
{
  "source": "C:\\Users\\u\\AppData\\Local\\singbox-launcher\\bin\\sing-box.exe",
  "version": "1.14.2-lx.2",
  "installed_at": "2026-09-24T12:00:00Z",
  "service": "sing-box-lxd",
  "files": [
    {"name": "sing-box-lxd.exe", "sha256": "<hex>"},
    {"name": "libcronet.dll", "sha256": "<hex>"}
  ],
  "warnings": [
    {"code": "state_dir_foreign_before_install", "text": "<текст предупреждения 4.2 п. 6>"}
  ]
}
```

`warnings` — предупреждения последнего install/copy, которые оператор в консоли не увидит
(лаунчер запускает install через `runas` без консоли и читает сайдкар без прав): массив
`{code, text}`; отсутствие поля или пустой массив — предупреждений нет. Каждый install/copy
переписывает поле заново (сайдкар при этом переписывается, даже если набор не менялся).
Коды: `state_dir_foreign_before_install` (4.2 п. 6). Новые коды добавляются в этот список.

`service` — имя службы SCM, к которой привязана копия; `""` — копия без службы. `files` —
весь набор, `sha256` — хеш файла копии (равен хешу источника). Сайдкар macOS (`sha256`,
`plist_path`, `label`) не меняется ни по ключам, ни по значениям: Windows пишет отдельную
структуру (PLAN, «ключевые решения»). Если набор не менялся и сайдкар говорит то же
(`files`, `service`), он не переписывается: `lxd: already up to date <hex exe>`. При установке
из самой копии `source` берётся из прежнего сайдкара.

### 2.6 `--service=install` (elevated)

1. Проверка прав: токен процесса повышен (`windows.GetCurrentProcessToken().IsElevated()`),
   иначе `--service=install needs an elevated token (run as administrator)`.
2. Каталог данных — 2.3.
3. `daemon.json` — как сейчас (`prepareServiceConfig`: адрес сохраняется или первый
   свободный порт с 19091, `tls: true`, секрет сохраняется или генерируется) плюс
   `log_file` = `<ProgramData>\sing-box-lxd\logs\lxd.log`, если ключа нет (2.12).
4. Служба есть и не `STOPPED` — stop и ожидание `STOPPED` до 30 с
   (`lxd: stopping service sing-box-lxd (Ns)`); не остановилась — отказ, файлы не тронуты.
5. Набор — 2.4 с `service: "sing-box-lxd"`.
6. Служба создаётся (`CreateService`) или обновляется (`UpdateConfig`):
   `BinaryPathName = windows.ComposeCommandLine([<копия>, "lxd", "--state-dir", <abs>, …])` —
   аргументы как на macOS (`daemonArgsForService`), argv[0] с пробелом получает кавычки;
   `LocalSystem`, `StartAutomatic`, зависимость `Tcpip`; recovery — restart ×3 с задержкой
   5 с, сброс счётчика 86400 с, `FailureActionsOnNonCrashFailures = true` (иначе остановка
   с ненулевым кодом, как у ошибки `Run` в 2.10, restart не вызывает).
7. DACL службы — `D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x2008d;;;AU)`. `0x2008d` = `READ_CONTROL`,
   `SERVICE_QUERY_CONFIG`, `SERVICE_QUERY_STATUS`, `SERVICE_ENUMERATE_DEPENDENTS`,
   `SERVICE_INTERROGATE`; без `START`, `STOP`, `CHANGE_CONFIG`, `WRITE_DAC`, `WRITE_OWNER`,
   `DELETE`.
8. Start и ожидание `RUNNING` до 30 с.

Если после остановки службы (п. 4) размещение набора или конфигурация службы не удались,
install запускает службу обратно на прежнем образе (`.old` возвращается на место, если
замена дошла до `rename`) и только потом отдаёт ошибку: неудачное обновление не должно
оставлять хост без VPN. Строка `lxd: install failed, previous service image restarted`.
9. Отчёт status (2.9) по службе; вердикт не `OK` — ошибка install (код 1).
10. Сводка (`printServiceSummary`) и приглашение — 2.13.

Повторный install с тем же бинарём файлы не трогает, `daemon.json`, секрет и клиентов
сохраняет и перезапускает службу (как bootout/bootstrap на macOS): для установщика SPEC 140
это моргание VPN.

`--service=install-user` на Windows — ошибка
`--service=install-user is not supported on Windows; use --service=install`.

### 2.7 `--service=copy` (elevated)

2.3 и 2.4 без службы: SCM не трогается. Сайдкар получает `service: ""`, а если служба
`sing-box-lxd` существует и прежний сайдкар привязан к ней — привязка сохраняется
(SPEC 100 §2.5). Работающая служба продолжает исполнять прежний образ до рестарта:
`lxd: the service still runs the previous image until restarted`. Повтор с тем же
бинарём — no-op. Для classic лаунчера с правами (SPEC 141 §8): копию затем запускает сам
лаунчер.

### 2.8 `--service=uninstall` (elevated)

1. Служба есть: из `BinaryPathName` берётся каталог argv[0] (кандидат на копию, если имя
   файла — `sing-box-lxd.exe`); stop с ожиданием `STOPPED`; `DeleteService`;
   `lxd: uninstalled service sing-box-lxd`.
2. Для кандидатов (каталог argv[0] и каталог копии — `--exec-dir` или дефолтный) набор
   удаляется только если сайдкар есть, его `service` — `sing-box-lxd` или пусто, и sha256
   каждого файла набора равен сайдкару. Хоть одно расхождение — не удаляется ничего, с
   причиной (`lxd: copy left in place: sha differs from sidecar …`, `… no sidecar …`,
   `… sidecar … belongs to …`). После набора удаляются сайдкар и остатки `.old`; дефолтный каталог
   `<ProgramFiles>\sing-box-lxd` удаляется, если пуст (он наш, в отличие от
   `/Library/PrivilegedHelperTools`); каталог из `--exec-dir` остаётся.
3. `--keep-copy`: служба снята, набор и сайдкар остаются, сайдкар при тех же проверках
   переписывается с `service: ""` (copy only); строка
   `lxd: copy kept for non-service use: <путь>; remove with --service=uninstall without --keep-copy`.
4. `--purge`: сверх того удаляется весь `<ProgramData>\sing-box-lxd` (state и logs, в том
   числе `classic.log` лаунчера).
5. `--dry-run` печатает те же решения со словом `would`.

### 2.9 Состояния и `--service=status` (без прав)

Таблица состояний — SPEC 100 §2.7 с заменой plist на службу SCM.

status не требует повышения и ничего не меняет. SCM открывается с `SC_MANAGER_CONNECT`,
служба — с `SERVICE_QUERY_CONFIG | SERVICE_QUERY_STATUS | READ_CONTROL`: `mgr.Connect()` из
`x/sys` просит `SC_MANAGER_ALL_ACCESS` (`svc/mgr/mgr.go`) и без прав падает, поэтому
дескрипторы открываются напрямую и оборачиваются в `mgr.Service`.

Отчёт печатает блоки: служба (имя, есть ли, `BinaryPathName` как есть и argv[0], тип
старта, учётная запись, состояние, pid, проверка DACL службы); копия (каталог, инвариант
цепочки, по каждому файлу набора — владелец, sha256 против соответствующего файла набора
вызывающего, лишние файлы); сайдкар; каталог данных (владелец, защищён ли DACL —
информационно, на вердикт не влияет). Последняя строка — `lxd: verdict: <ВЕРДИКТ>`, для
MISMATCH/UNSAFE с причиной и командой исправления после ` — `.

Набор вызывающего — его exe и `libcronet.dll` рядом, если есть. Сравнение путей — без
учёта регистра после `filepath.Clean`. Каноническая копия — `<каталог копии>\sing-box-lxd.exe`
с учётом `--exec-dir`: установку в нестандартный каталог status проверяет с тем же флагом.

| Вердикт | Когда | Код |
|---|---|---|
| `OK` | служба есть, argv[0] — каноническая копия, DACL службы и инвариант в порядке, сайдкар привязан к `sing-box-lxd`, файлы == сайдкар == набор вызывающего, лишних файлов нет, `CurrentState == SERVICE_RUNNING` | 0 |
| `MISMATCH` | инвариант соблюдён, но: набор ≠ вызывающему (разное присутствие `libcronet.dll` тоже); сайдкара нет или он чужой; файл ≠ сайдкару; сайдкар есть, файла нет; сайдкар привязан к несуществующей службе; лишний файл в каталоге копии | 2 |
| `UNSAFE` | `QueryServiceConfig` не читается (в том числе отказ в доступе); `BinaryPathName` не разбирается `DecomposeCommandLine`; argv[0] ≠ канонической копии; путь с пробелом без кавычек; DACL службы даёт чужому SID `SERVICE_CHANGE_CONFIG`, `WRITE_DAC`, `WRITE_OWNER`, `DELETE`, `GENERIC_WRITE` или `GENERIC_ALL`; цепочка или набор нарушают инвариант 2.2 | 2 |
| `NOT INSTALLED` | `OpenService` → `ERROR_SERVICE_DOES_NOT_EXIST` (1060), копии и сайдкара нет | 3 |
| `COPY ONLY` | службы нет, копия проходит инвариант, сайдкар с `service: ""`, файлы == сайдкар == набор вызывающего | 4 |
| `NOT RUNNING` | на диске и в конфигурации службы всё как у `OK`, но `CurrentState != SERVICE_RUNNING` (включая `START_PENDING`); в причине — `sc.exe start sing-box-lxd` (от администратора) или `--service=install` | 5 |
| ошибка | SCM не открылся, вызывающий бинарь не читается | 1 |

Отличие от macOS: argv[0], указывающий не на каноническую копию, — `UNSAFE`, а не
`MISMATCH` (так согласовано с лаунчером, SPEC 141 §6.2). Общий вердикт — самый тяжёлый
(`OK` < `COPY ONLY` < `NOT RUNNING` < `MISMATCH` < `UNSAFE`).

### 2.10 Демон под SCM

`lxd` при `svc.IsWindowsService() == true` идёт через `svc.Run("sing-box-lxd", handler)`;
без этого SCM снимает старт по таймауту (ошибка 1053). `Execute` обработчика:

1. `StartPending`.
2. `daemon.json` (как в `lxdMain`), затем лог (2.12), затем самопроверка (2.11): отказ
   самопроверки попадает в `lxd.log`.
3. `lxd.Run` в горутине с контекстом, который отменяет обработчик; сразу `Running` с
   `AcceptStop | AcceptShutdown` (bootstrap ядра может идти дольше таймаута старта SCM, а
   управляющий канал поднимается первым и так).
4. `Interrogate` — ответ текущим статусом. `Stop`/`Shutdown` — `StopPending` с `WaitHint`,
   отмена контекста, ожидание возврата `Run`; сторож 10 с, по истечении — строка в лог и
   `os.Exit(1)`; затем `Stopped`.
5. `Run` вернул ошибку сам — она пишется в лог, `Execute` возвращает код 1 (служба
   остановлена с ошибкой, recovery 2.6 п. 6 её перезапускает).

Ошибки до открытия лога (не читается `daemon.json`) — только код выхода 1: Event Log не
используется, SCM пишет код в системный журнал сам.

Сейчас цикл `lxd.Run` (`lxd/daemon.go`, `for { select … }` со строки 241) ждёт только
`serveErr` и сигналы и на отмену контекста не реагирует: нужно добавить ветку
`<-ctx.Done()` с тем же разбором, что у SIGTERM (`applyAccess`, `closed`, `shutdown`).
Ветка общая для всех платформ.

### 2.11 Самопроверка при старте

`CheckServiceExecutable` на Windows проверяет собственный бинарь (`EvalSymlinks(os.Executable())`)
инвариантом 2.2 — цепочку предков, каталог и файлы набора рядом с ним.

| Контекст | Нарушение инварианта |
|---|---|
| команда `lxd` при `svc.IsWindowsService()` | отказ старта (текст в `lxd.log`, код 1) |
| повышенный токен вне SCM (`lxd` или `run`, в том числе classic лаунчера) | `WARN`, старт |
| `lxd --allow-unsafe-exec` | `WARN`, старт |
| токен не повышен | проверки нет |

Текст отказа по образцу SPEC 100 §2.8: ``lxd: refusing to run as a Windows service from
<бинарь>: <нарушение>; run `sing-box lxd --service=install` to reinstall from a protected
copy``. Обёртка `sing-box run` (уже есть в `cmd_lxd_lx.go`, `init`) на Windows даёт `WARN`
при повышенном токене и бинаре не из защищённой копии.

При пройденной проверке в контексте службы — одна строка `INFO`, на обеих платформах
(пожелание SPEC 100 §5 п. 8): macOS — `lxd: self-check ok: root-owned <путь>, launchd
service com.leadaxe.sing-box-lxd`; Windows — `lxd: self-check ok: protected <путь>, windows
service sing-box-lxd`.

### 2.12 Лог демона

`logredirect_windows.go`: `logRotationSupported = true`. Схема Unix (dup2 на fd 1/2 и
`rename` живого файла) на Windows не переносится:

- Go открывает файлы без `FILE_SHARE_DELETE` (`syscall.Open`, `sharemode =
  FILE_SHARE_READ | FILE_SHARE_WRITE`), поэтому `rename` файла, который кто-то держит, падает;
- писатели держат `*os.File`: стандартный логгер создан в `init` пакета `log` над
  `os.Stderr` (`log/export.go`), логгер ядра — в `box.New` над `os.Stderr` на момент
  создания инстанса (`log/log.go`). После `rename` они писали бы в старый файл.

Поэтому на Windows один дескриптор на всё время жизни процесса и ротация копированием:

1. Ротатор открывает `lxd.log` на дозапись (share read/write) — дескриптор `H`.
2. `SetStdHandle(STD_OUTPUT_HANDLE, H)`, `SetStdHandle(STD_ERROR_HANDLE, H)`;
   `os.Stdout = os.Stderr = os.NewFile(H)`; `log.SetStdLogger` на логгер над ним (под SCM
   исходный `os.Stderr` — пустой дескриптор). Паники и фатальные ошибки рантайма идут в `H`
   сами: рантайм Windows берёт `GetStdHandle` на каждую запись (`runtime/os_windows.go`,
   `write1`), `debug.SetCrashOutput` не нужен.
3. Ротация (по размеру и возрасту, лимиты из `daemon.json` как сейчас): содержимое
   `lxd.log` копируется во временный файл → сдвиг бэкапов → `rename` временного в
   `lxd.log.1` → усечение `lxd.log` до нуля вторым дескриптором. Строки, записанные между
   копированием и усечением, теряются.
4. Удалить `lxd.log`, пока демон жив, нельзя (нет `FILE_SHARE_DELETE`): ветка «лог удалили
   из-под демона» на Windows не нужна.

Редирект, как и сейчас, только когда stdout не терминал (`lxd/daemon.go:79`). Под SCM
`os.Stdout` — `NewFile` над пустым дескриптором, `Stat` возвращает ошибку,
`stdoutIsTerminal()` даёт `false` — подтвердить живым прогоном (§5 п. 8).

Путь: install пишет в `daemon.json` ключ `log_file` =
`<ProgramData>\sing-box-lxd\logs\lxd.log`, если его нет; `cmd_lxd_lx.go:194-200` уже
предпочитает `log_file` пути `DefaultLogPath(stateDir)` (тот дал бы
`<ProgramData>\sing-box-lxd\lxd.log`, мимо `logs\`). `DefaultLogPath` не меняется.
`/admin/logs` читает `lxd.log` и `lxd.log.1` как сейчас.

### 2.13 Приглашение в файл (все платформы)

Новые флаги:

- `lxd --service=install --invite-out <файл> [--invite-name <имя>]` — имя по умолчанию
  `singbox-launcher`; без `--invite-out` приглашение минтится как сейчас, с именем из
  `--invite-name`, если флаг задан, иначе пустым (поведение macOS не меняется);
- `lxd client add --invite-out <файл>` — имя через существующий `--name`.

Файл создаётся до минта: Unix — `O_CREAT|O_EXCL|O_NOFOLLOW`, `0600`; Windows —
`CREATE_NEW`, после создания `GetFinalPathNameByHandle` обязан совпасть с запрошенным путём
(junction в родителе увёл бы запись повышенного процесса в чужой каталог) — иначе файл
удаляется, отказ. Файл уже есть — отказ до любых изменений (install проверяет это первым
шагом). Содержимое — строка приглашения `адрес#отпечаток#код` и перевод строки. С флагом
приглашение в stdout не печатается, вместо него `lxd: invite written to <файл>`.

Имя клиента (`--invite-name`, `--name`, поле `name` в `/admin/client-code`) нормируется
на минте: после обрезки пробелов по краям — от 1 до 64 символов, только печатные
(без управляющих), иначе `400` с текстом `client name: …`; пустое имя допустимо и значит
«без имени» (как сейчас). Лаунчер на Windows ставит имя на учётную запись
(`singbox-launcher-<user>`), чтобы вторая учётная запись не выбивала пару первой (4.2 п. 7).

Имя доходит до записи клиента: `MintClientCode(name)` шлёт его в `/admin/client-code`,
реестр хранит его как `activeCodeName`, и при enroll оно побеждает имя, предложенное
клиентом (`lxd/clients.go`, `enroll`). Ожидание демона — до 15 с, как в
`printServiceSummary`; не дождались при `--invite-out` — код выхода 1 (служба остаётся
установленной, файл удаляется): лаунчер судит по коду. Повторный install без флага
клиентов не трогает.

### 2.14 Поиск DLL

Новый файл `cmd/sing-box/dllsearch_windows_lx.go`, тег `with_lxd && windows`: `init()`
вызывает `SetDefaultDllDirectories(LOAD_LIBRARY_SEARCH_APPLICATION_DIR |
LOAD_LIBRARY_SEARCH_SYSTEM32)`. `init` пакета `main` выполняется после `init` всех
зависимостей и до `main`; что `init` зависимостей не грузят DLL без пути, при реализации
проверить по дереву импортов. Процедура ищется заранее
(`NewLazySystemDLL("kernel32.dll").NewProc(…).Find()`): `windows.SetDefaultDllDirectories`
паникует на отсутствующей процедуре (Windows 7 без KB2533623). Вызов закрывает и
classic-запуск из копии.

Проверено по коду:

- `libcronet.dll` грузится лениво (`checkLibrary()` → `cronet.LoadLibrary("")` при
  создании naive-outbound), по полному пути: `findLibrary` в
  `cronet-go/internal/cronet/loader_windows.go` перебирает каталог exe, затем каталоги
  `PATH`; текущий каталог не смотрит. `SetDefaultDllDirectories` на загрузку по полному
  пути не влияет, но управляет поиском зависимостей самой библиотеки.
- `wintun.dll` загружается из памяти (`memmod`), его импорты — системные DLL.

Остаётся дыра: набор без `libcronet.dll` (источник без неё) плюс naive-outbound в конфиге
→ поиск по `PATH` под SYSTEM, а каталог в системном `PATH`, открытый пользователю на запись,
даёт подсадку DLL. Закрывается закреплением отказа — §4.2 п. 2.

### 2.15 Права и платформенные мелочи

- `prepareServiceConfig` проверяет `os.Getuid() != 0` (`cmd_lxd_lx.go:372`); на Windows это
  всегда `-1`. Нужен платформенный предикат «привилегирован»: Unix — euid 0, Windows —
  повышенный токен; он же в install/copy/uninstall.
- `printServiceSummary`: строка `restart command` платформенная. Windows —
  `Restart-Service sing-box-lxd` (PowerShell от администратора): пара
  `sc.exe stop … && sc.exe start …` не ждёт остановки, и `start` падает с 1056, пока служба
  в `STOP_PENDING`. Подсказка при провале минта (`sudo <бинарь> lxd client add`) — тоже
  платформенная.
- `client add`/`list`/`remove` без прав: `FindServiceStateDir` не может прочитать
  `daemon.json` под DACL 2.1 и молча откатывается на `lxd-state`; ошибка доступа должна
  давать `run as administrator`.
- Текст флагов `--service`, `--exec-dir` (дефолтный путь) — по платформе.

### 2.16 Под SYSTEM

- tailscale: лаунчер ставит `state_directory` узла в `<StateDir>\tailscale\<тег>`; каталог
  создаёт сам апстримный endpoint (`protocol/tailscale/endpoint.go`, `Start`,
  `filemanager.MkdirAll`), DACL наследуется от `state\`. Дописывать ничего не нужно.
- `find_process` без ограничений; системный DNS ядро не трогает; системный прокси ставит
  лаунчер в профиле пользователя (SPEC 141 §7).
- Канал — TCP loopback + mTLS из `daemon.json`, как сейчас; named pipe не заводится.

## 3. Интерфейс для лаунчера

Нумерация — SPEC 141 лаунчера §3.

| # | Что | Ядро |
|---|---|---|
| 1 | служба | `sing-box-lxd`, `LocalSystem`, `StartAutomatic`, `Tcpip`, restart ×3/5 с, сброс 86400 с, restart и при остановке с ошибкой; `BinaryPathName` — `ComposeCommandLine`, argv[0] = каноническая копия (2.6) |
| 1a | DACL службы | `D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x2008d;;;AU)`; у AU сверх `QUERY_*` и `READ_CONTROL` есть `ENUMERATE_DEPENDENTS` и `INTERROGATE` — безвредны |
| 2 | команды | `lxd --service=install\|copy\|status\|uninstall [--keep-copy] [--purge] [--exec-dir] [--dry-run]`; install/copy/uninstall — повышенный токен, status — без прав |
| 2a | захват `ProgramData` | 2.3; строки `took ownership of …`, `replaced DACL on …` |
| 3 | набор и сайдкар | 2.4, 2.5 |
| 3a | замена образа | 2.4 п. 6; install останавливает службу до замены (2.6 п. 4) |
| 4 | инвариант | 2.2 |
| 5 | самопроверка | 2.11 |
| 6 | status | 2.9, коды 0/2/3/4/5/1 |
| 7 | приглашение | 2.13; для «fresh invite» — `client add --name singbox-launcher --invite-out <файл>` |
| 8 | логи | `logs\lxd.log` с ротацией 2.12; `classic.log` — файл лаунчера |
| 9 | `/admin/info` | `executable`, `executable_sha256` — уже есть (SPEC 100 §2.9) |
| 10 | под SYSTEM | 2.16 |
| 11 | канал | TCP loopback + mTLS |
| 12 | поиск DLL | 2.14 |

Строки вывода: `lxd: copied <src> -> <dst> (sha256 <hex>, …)`,
`lxd: binary set unchanged (…), copy skipped`, `lxd: already up to date <hex>`,
`lxd: took ownership of …`, `lxd: replaced DACL on …`, `lxd: copy left in place: …`,
`lxd: invite written to <файл>`, `lxd: verdict: …`; отказ и `WARN` самопроверки (2.11).

Что лаунчеру нужно учесть сверх SPEC 141 (решения §4.2): install заменяет DACL и у
`logs\classic.log` (п. 4); остатки `.old` и временные файлы в каталоге копии не
`Stale` (п. 5); повторное сопряжение под тем же именем заменяет запись клиента (п. 7);
предупреждения install — в сайдкаре `warnings` (2.5); имя клиента ≤ 64 печатных символов
(2.13); `daemon.json` и state лежат в `<ProgramData>\sing-box-lxd\state\`, лог — в `logs\lxd.log`
через `log_file`; провал минта при `--invite-out` — код выхода 1 при установленной
службе; `--invite-name` есть только у install, у `client add` имя задаёт `--name`.

## 4. Границы и открытые точки

### 4.1 Границы

- Linux — без изменений (печать рецепта). macOS — поведение прежнее; добавляются только
  флаги 2.13 и строка `INFO` 2.11. Android, libbox, AAR — ноль изменений.
- Win7 (`windows-386-legacy-windows-7`) — без службы: релиз сбрасывает `with_lxd`
  (`lx-release.yml`, шаг Build).
- Подпись (Authenticode) копии не проверяется и не ставится.
- Одновременные install/copy с разными бинарями — без блокировки, как на macOS: status
  покажет `MISMATCH`, повтор команды чинит.
- Event Log не используется; служба в сессии пользователя не заводится.
- Предки каталога данных (`C:\ProgramData`) инвариантом ядра не проверяются: ядро
  приводит к норме только своё дерево (2.3). Цепочку `logs\` для `classic.log` проверяет
  лаунчер (SPEC 141 §8).
- `ERROR_SERVICE_MARKED_FOR_DELETE` (1072): install сразу после uninstall при открытой
  оснастке «Службы» не создаст службу — ошибка с подсказкой закрыть оснастку или
  перезагрузиться.

### 4.2 Решения ядра по открытым точкам (2026-09-24)

1. **Рабочий каталог службы.** `Execute` делает `os.Chdir(<state-dir>)` до `Run`:
   относительные пути конфига (`cache.db`, дефолтный `state_directory` tailscale)
   разрешаются в `state\`, а не в `System32`. Аргументы службы остаются как на macOS.
   Ошибка `Chdir` — отказ старта в лог.
2. **`libcronet.dll` из `PATH`.** Закрепляется: в контексте службы и при повышенном `run`,
   если `<каталог exe>\libcronet.dll` нет, на старте вызывается
   `cronet.LoadLibrary(<каталог exe>\libcronet.dll)` — `loadOnce` запоминает ошибку, и
   naive-outbound получает её вместо поиска по `PATH`. Файл с тегом
   `with_lxd && windows && with_purego && with_naive_outbound`.
3. **Тег `dllsearch_windows_lx.go`** — `with_lxd && windows` (CONSTITUTION §3.2: сборка
   без тегов форка = апстрим). Защищённая копия, из которой classic стартует с правами,
   существует только в сборках с `with_lxd`.
4. **DACL `classic.log`.** Вариант (а): ядро приводит всё дерево без исключений, лаунчер
   восстанавливает своё чтение для пользователя на каждом повышенном старте. Передано
   лаунчер-сессии для правки SPEC 141 §8.
5. **Остатки в каталоге копии.** `<член набора>.old` и временные файлы 2.4 п. 3 — не
   лишние: строка в отчёте status, вердикт не меняют. Лаунчер повторяет правило
   (SPEC 141 §6.2); передано лаунчер-сессии.
6. **Секрет после захвата.** Секрет и серверная пара не перегенерируются: лаунчер хранит
   `daemon_secret` в `settings.json`, смена сломала бы его операторские маршруты, а
   перевыпуск пары рассопрягает клиентов. Если 2.3 забрало владение у SID вне allowlist
   или чужой SID имел чтение `state\`, install печатает `WARN: <путь> was readable by
   <имя> (<SID>) before this install; rotate the admin secret in daemon.json and re-pair
   clients if the host is shared`, и то же предупреждение кладёт в сайдкар (`warnings`,
   код `state_dir_foreign_before_install`, 2.5) — консоли у install под `runas` нет.
   Свежая установка без прежнего `daemon.json` — без предупреждения.
7. **Повторные клиенты.** Enroll по приглашению с именем (`activeCodeName` не пустой)
   заменяет запись клиента с тем же именем: старый сертификат отзывается, новый занимает
   место. Приглашение без имени (`client add` без `--name`, install без флагов) ведёт
   себя как сейчас — добавляет. Так повторный «Install or update service» лаунчера не
   копит записи `singbox-launcher`. Передано лаунчер-сессии.
8. **DACL `state\` в вердикте** остаётся информационным: status без прав не может
   прочитать DACL каталога, закрытого для всех, кроме SYSTEM и Administrators, и
   `UNSAFE` на этом основании был бы ложным.
9. **CI на `windows-latest`** — заводится: репозиторий публичный, Windows-раннеры для
   него бесплатны. Джоба `test-windows`: `go vet` и `go test` с полным набором тегов для
   `./lxd/ ./cmd/sing-box/`, плюс живой SCM-тест под `LX_WINDOWS_SCM_LIVE=1`, если у
   раннера есть права администратора (проверяется первым шагом джобы; нет прав — тест
   пропускается с пометкой). На каждый push в `lx` и на теги, как остальные джобы lx-ci;
   если прогон окажется дольше 15 мин — сузить до `workflow_dispatch` и тегов.

### 4.3 Проверяется только на живой Windows

`stdoutIsTerminal()` под SCM; время до `Running` против таймаута 1053; `rename`
работающего `sing-box-lxd.exe` и загруженной `libcronet.dll`; захват `ProgramData`,
заранее созданного обычным пользователем; ротация `lxd.log` копированием;
`GetFinalPathNameByHandle` при junction в пути `--invite-out`; права администратора у
раннера `windows-latest`.

## 5. Критерии приёмки

1. Портируемые табличные тесты (Linux в CI): предикат ACL (владелец, маски предков и
   копии, `INHERIT_ONLY`, NULL DACL, неизвестный разрешающий тип ACE); сайдкар набора
   (запись/чтение, ключи JSON 2.5); решения uninstall и `--keep-copy` по набору (всё
   совпало → удалено; один файл расходится → оставлено всё; чужая служба; нет сайдкара;
   сайдкар без файлов → удалён как устаревший); идемпотентность набора; лишние файлы;
   `--invite-out` (файл есть → отказ, создан эксклюзивно, в stdout не печатается);
   golden-тест сайдкара macOS — JSON прежний байт в байт; `lxd.Run` возвращается по отмене
   контекста.
2. Windows-тесты (`with_lxd && windows`) через подменяемые SCM и чтение ACL (аналог
   `serviceEnv` darwin), без реальной службы: переходы none → copy only → installed →
   (обновление ядра) → copy only (`--keep-copy`, повтор — no-op) → installed → none с
   вердиктом и кодом после каждого шага; аномалии: argv[0] не копия, путь без кавычек,
   AU с `CHANGE_CONFIG` → `UNSAFE`; лишний файл, разное присутствие `libcronet.dll` →
   `MISMATCH`; `STOPPED`/`START_PENDING` → `NOT RUNNING`; `ComposeCommandLine` →
   `DecomposeCommandLine` для путей с пробелами; `Execute` обработчика на подставных
   запросах (порядок статусов, `Interrogate`, сторож).
3. Самопроверка по контекстам 2.11 на обеих платформах, включая строку `INFO`.
4. CI: `GOOS=windows go vet ./lxd/ ./cmd/sing-box/` с полным набором тегов в lint;
   darwin-vet и Linux-тесты зелёные без правок семантики.
5. Сборки darwin, linux, android и сборка без тегов форка — поведение прежнее.
6. Доки: `docs-lx/lxd-daemon.md` и `.ru.md` — раздел о Windows по образцу §7/§7.1 macOS;
   фича 014 — ручки `--invite-out`, `--invite-name`, пути Windows; changelog
   `v1.14.2-lx.2`.
7. Джоба `test-windows` (§4.2 п. 9) зелёная; живой SCM-тест под
   `LX_WINDOWS_SCM_LIVE=1` — install → status `OK` → uninstall.
8. Живой прогон на Windows (машина владельца или сессия лаунчера): install → status `OK`
   → повторный install = пропуск копии → copy → uninstall `--keep-copy` → `COPY ONLY` →
   uninstall → `NOT INSTALLED`; `sc qc`, `sc qfailure`, `sc sdshow`, `icacls` копии и
   `ProgramData\sing-box-lxd`, сайдкар, `Get-FileHash` (SPEC 141 §12 п. 1); `sc stop` →
   `NOT RUNNING`; `sc config binPath=` на чужой exe → `UNSAFE`, служба не стартует из
   чужого бинаря; ротация `lxd.log`; лаунчер сопрягается через `--invite-out`; пункты §4.3.
   После него — статус D.
