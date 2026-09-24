# SPEC: 100 — LXD_ROOT_OWNED_BINARY

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — повышение привилегий через системную службу SPEC 057 |
| Статус | I (implemented) — код и тесты в ветке, `go test -race ./lxd/ ./cmd/sing-box/` зелёные на macOS; ручная проверка install/status/uninstall под sudo (§5 п. 8) — за владельцем |
| Ветка | `spec100-lxd-root-owned-binary` (от `lx`) |
| База | `4146bc8ce` |
| Релиз | `v1.14.1-lx.11`; имя файла копии `sing-box-lxd` — `v1.14.1-lx.12` |
| Связано | [057](../057-LXD_MTLS_SERVICE/SPEC.md) (служба `--service`), [065](../065-LXD_OBSERVABILITY_PLANE/SPEC.md) (`/admin/info`); лаунчер: SPEC 137 (classic TUN на macOS через `--service=copy`) |

Решение владельца 2026-09-24, нормы согласованы с сессией-владельцем ядра.

**Цена мержа:** ноль апстримных файлов. Правки — пакет `lxd/`,
`cmd/sing-box/cmd_lxd_lx.go`, `.github/workflows/lx-ci.yml`, документация. Обёртка
`sing-box run` (§2.8) ставится из `init()` в `cmd_lxd_lx.go`, `cmd_run.go` не тронут.

---

## 1. Дефект

`sing-box lxd --service=install` (системный LaunchDaemon, root) записывал в
`ProgramArguments[0]` plist'а путь, из которого его запустили, — `os.Executable()`.
На практике это бинарь в бандле лаунчера (`/Applications/singbox-launcher.app/…/bin/sing-box`,
владелец — пользователь) или в `~/Library/Application Support/…`. launchd исполняет
этот файл от root при каждом старте, KeepAlive-рестарте и загрузке машины. Любой
процесс пользователя, который может переписать файл (или любой каталог на пути к
нему — `/Applications` сам `root:admin 0775`), получает исполнение кода от root:
локальное повышение привилегий.

## 2. Решение: root исполняет только root-owned копию

### 2.1 Канонический путь

| Что | Путь | Владелец / режим |
|---|---|---|
| копия бинаря | `/Library/PrivilegedHelperTools/sing-box-lxd` | `root:wheel 0755` |
| сайдкар | `/Library/PrivilegedHelperTools/sing-box-lxd.install.json` | `root:wheel 0644` |

Соглашение Apple для привилегированных помощников: плоский файл прямо в
`/Library/PrivilegedHelperTools`, без своего каталога. Имя файла — `sing-box-lxd`, а не
ярлык: macOS усекает имя процесса (`comm`) до 16 символов, `sing-box-lxd` влезает целиком
и содержит `sing-box`, так что `pgrep`, `pkill` и `ps -c` находят демон без `-f`; по
конституции форка бинарь — `sing-box`, это его производная. Ярлык службы, plist и
`XPC_SERVICE_NAME` остаются `com.leadaxe.sing-box-lxd`.

Файлы, которые ставил `v1.14.1-lx.11` (`/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd`
и `….install.json` рядом), не принадлежат этому ядру: оно их не читает, не переписывает и не
удаляет; plist, указывающий на такой файл, status считает не нашей копией (`MISMATCH`, 2),
переустановка переводит службу на `sing-box-lxd`. Миграции нет: установок lx.11 в поле нет.

`--exec-dir <dir>` — каталог, куда кладутся оба файла с теми же именами; дефолт —
`/Library/PrivilegedHelperTools`. Инвариант (2.2) проверяется по цепочке от `/` до
каталога и на самом файле. Дефолтный каталог принадлежит macOS и не создаётся: его
отсутствие — ошибка с причиной. Каталог из `--exec-dir` при отсутствии создаётся
(недостающие компоненты `root:wheel 0755`).

**Старая раскладка.** Если по пути копии лежит **каталог** (остаток пре-релиза с
раскладкой «каталог с бинарём внутри»), install и copy отказывают:
`target is a directory (legacy layout); remove it: sudo rm -rf <путь>` — и ничего не
удаляют сами; status даёт `MISMATCH` (2) с той же причиной, uninstall и `--keep-copy`
оставляют каталог с сообщением `… is a directory (legacy layout), left in place; remove
it: sudo rm -rf <путь>`.

### 2.2 Инвариант root-owned

Путь проходит инвариант, если **каждый компонент от `/` до него включительно**
(по `Lstat`, без следования симлинкам):

- не симлинк;
- принадлежит uid 0;
- не имеет записи для group и other (`mode & 022 == 0`; sticky/setuid не мешают).

Нарушение — отказ с текстом `<путь>: owned by uid N, mode NNNN, must be root-owned
and not group/world-writable` (для симлинка — `<путь>: is a symbolic link, must be a
real file or directory owned by root`). Предикат платформенно-нейтрален (uid, режим,
признак симлинка) — его табличный тест идёт в CI на Linux без root. ACL и флаги
файловой системы не проверяются (у `/Library/PrivilegedHelperTools` их нет по умолчанию).

### 2.3 Размещение копии (общее для install и copy)

1. Источник = `filepath.EvalSymlinks(os.Executable())`; он обязан быть обычным файлом
   (`Lstat`, не симлинк) не больше 512 МиБ.
2. Каталог копии проходится от `/`: каждый компонент — каталог, проходящий инвариант.
   Дефолтный каталог обязан существовать; у `--exec-dir` недостающие компоненты
   создаются `root:wheel 0755`. Нарушение — отказ до любых изменений на диске. Каталог
   на месте копии — отказ «legacy layout» (2.1).
3. Бинарь:
   - источник и есть копия (запуск из неё) — пропуск;
   - копия существует и её sha256 совпадает с источником — пропуск,
     `lxd: binary unchanged (sha256 <hex>), copy skipped` (владелец/режим приводятся к
     `root:wheel 0755`, если сбиты);
   - иначе: временный файл в том же каталоге с уникальным случайным именем (`O_EXCL`,
     `0600`) → копия байтов → `fsync` → `chown root:wheel` → `chmod 0755` → sha256 копии
     == sha256 источника (иначе временный файл удаляется, отказ) → `rename` на место →
     `fsync` каталога; `lxd: copied <src> -> <dst> (sha256 <hex>, root:wheel 0755)`.
   - Перезапись на месте запрещена: работающий демон держит старый inode, а ядро macOS
     убивает процесс, у которого поменялись страницы подписанного кода. `rename`
     атомарен и старый inode не трогает. Два одновременных вызова не оставляют
     полукопии: у каждого свой временный файл, на место встаёт целый.
   - Никаких xattr (копируются только байты, карантина нет) и никакой повторной
     подписи: ad-hoc подпись встроена в Mach-O, переподпись сломала бы sha.
   - Копия самодостаточна: darwin-релиз линкует libcronet статически (CGO).
4. Сайдкар — после бинаря, тем же способом (временный файл + `rename`), `root:wheel 0644`,
   читается без root:
   ```json
   {
     "source": "/Applications/singbox-launcher.app/Contents/MacOS/bin/sing-box",
     "sha256": "<sha256 копии, hex>",
     "version": "1.14.1-lx.11",
     "installed_at": "2026-09-24T12:00:00Z",
     "plist_path": "/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist",
     "label": "com.leadaxe.sing-box-lxd"
   }
   ```
   `version` — константа версии ядра: status показывает версию копии без её запуска.
   `plist_path` — plist, который исполняет копию; `""` у копии без службы. При установке
   из самой копии `source` берётся из прежнего сайдкара.
   Если бинарь не менялся и сайдкар уже говорит то же самое (sha, label, plist_path) —
   ничего не пишется, `lxd: already up to date <sha256>`.

### 2.4 `--service=install` (root)

Размещение копии (2.3) с `plist_path` = системный plist, затем plist, в котором меняется
**только** `ProgramArguments[0]` — на путь копии. Аргументы, пути state-dir/конфига/лога,
`daemon.json` (адрес, секрет), доверенные клиенты — как были. Support-каталог
(`/Library/Application Support/sing-box-lxd/` и `state/` в нём) приводится к `root:wheel`
явно: по BSD-семантике он наследовал группу `admin` родителя. В конце — отчёт status по
системной области; вердикт не `OK` — ошибка install.

`--service=install-user` (LaunchAgent, без root) не меняется: агент исполняется от того
же пользователя, которому принадлежит бинарь, повышения нет. Отчёт status в конце
печатается и там.

Перезагрузка job'а: `launchctl bootout` возвращается, как только запрос принят, а job с живым ядром выгружается ещё несколько секунд (на живой проверке 2026-09-24 — около 3 с); `bootstrap` в это окно падает с `Bootstrap failed: 5: Input/output error` (в логе launchd — `37: Operation already in progress`), и служба остаётся незагруженной. Поэтому install после bootout ждёт исчезновения job'а (`launchctl print` → not found, опрос каждые 250 мс, до 10 с, с сообщением `waiting for the old service to unload (Ns)`), затем делает bootstrap и повторяет его до 10 с на ошибках «already in progress» / «Input/output error»; другие ошибки bootstrap финальны.

### 2.5 `--service=copy` (root)

Только размещение копии (2.3) с `plist_path: ""` — plist не пишется, launchd не
трогается. Те же пути, `--exec-dir` и проверки, что у install. Для лаунчера, который сам запускает ядро от root (classic TUN на macOS,
SPEC 137 лаунчера). Повторный вызов при совпадении sha — no-op. Если системная служба
уже исполняет эту копию, `copy` освежает бинарь и сохраняет привязку `plist_path`.
Последующий `install` привязывает ту же копию к plist, не копируя повторно.
Обновление ядра лаунчером = снова `--service=copy` (и `install`, если служба есть);
ядро само ничего не отслеживает.

### 2.6 `--service=uninstall`

Копия и сайдкар удаляются **только** если сайдкар есть, его `label` — этот ярлык,
`plist_path` — снимаемый системный plist или пусто (копия без службы), файл — обычный и
его sha256 равен sha256 из сайдкара. Иначе файл остаётся с сообщением
(`lxd: copy left in place: sha differs from sidecar …`, `… no sidecar …`,
`… sidecar … belongs to …`). Произвольный `ProgramArguments[0]` не удаляется никогда.
Кандидаты — каталог `ProgramArguments[0]` (если файл называется
`sing-box-lxd`) и каталог копии (`--exec-dir` или дефолтный). Сайдкар без
файла удаляется как устаревший. Сам каталог не удаляется никогда; каталог старой
раскладки на месте копии остаётся с сообщением (2.1). `--purge` — как раньше, про
support-каталог. `--dry-run` печатает те же решения со словом `would`.

`--keep-copy` снимает plist и launchd, но копию и сайдкар оставляет: сайдкар,
привязанный к снимаемому plist, при тех же проверках (ярлык, plist, sha файла ==
сайдкар) переписывается с `plist_path: ""` — состояние copy only, status даёт
`COPY ONLY` (4). Печатается `lxd: copy kept for non-service use: <путь копии>; remove
with --service=uninstall without --keep-copy`. `--purge` и здесь только про state.
Без установленной службы при живой копии без привязки — no-op с тем же сообщением,
выход 0. Копия, не прошедшая проверки, остаётся как была, с причиной.

### 2.7 Состояния и `--service=status`

| Состояние | Что на диске | Переходы |
|---|---|---|
| none | ни plist, ни копии | `copy` → copy only; `install` → installed |
| copy only | копия + сайдкар (`plist_path: ""`) | `install` → installed (без повторного копирования); `uninstall` → none (копия и сайдкар) |
| installed | plist → копия, сайдкар с `plist_path` этого plist | `copy` → installed (бинарь освежён); `uninstall` → none (plist, копия, сайдкар); `uninstall --keep-copy` → copy only (plist снят, сайдкар отвязан) |

status не требует root и ничего не меняет. Для системной стороны (LaunchDaemon, а без
него — каталог копии) и для user-агента печатает: путь plist и есть ли он;
`ProgramArguments[0]` (или ключ `Program`); существует ли файл, uid/gid, режим; проходит
ли инвариант (для агента — информационно); sha256 программы против sha256 вызывающего
бинаря; содержимое сайдкара; состояние launchd (`launchctl print <domain>/<label>`:
state, pid, загруженная программа); вердикт блока. Последняя строка —
`lxd: verdict: <ВЕРДИКТ>`, для MISMATCH/UNSAFE — с причиной и командой исправления
после ` — `.

| Вердикт | Когда | Код |
|---|---|---|
| `OK` | installed: копия проходит инвариант, сайдкар привязан к этому plist, sha файла == sha сайдкара == sha вызывающего бинаря; либо user-агент исполняет бинарь, равный вызывающему | 0 |
| `MISMATCH` | любое иное сочетание при соблюдённом инварианте: копия ≠ вызывающий бинарь; сайдкара нет или он чужой; файл ≠ сайдкар; сайдкар есть, файла нет; сайдкар привязан к исчезнувшему plist; plist исполняет root-owned файл, который не наша копия | 2 |
| `UNSAFE` | исполняемый файл (или каталог на пути к нему) не проходит инвариант | 2 |
| `NOT INSTALLED` | ни plist, ни копии | 3 |
| `COPY ONLY` | copy only: копия проходит инвариант, сайдкар без plist, sha файла == сайдкар == вызывающий бинарь | 4 |
| `NOT RUNNING` | на диске всё как у `OK`, но у launchd нет работающего job'а этого ярлыка (`launchctl print` → not loaded или state ≠ running): bootstrap упал или был только bootout; в причине — команда `launchctl bootstrap <domain> <plist>` (или `--service=install` для перезагрузки) | 5 |
| ошибка | plist не читается/не разбирается, не читается вызывающий бинарь | 1 |

Общий вердикт — самый тяжёлый из присутствующих блоков
(`OK` < `COPY ONLY` < `NOT RUNNING` < `MISMATCH` < `UNSAFE`).

### 2.8 Самопроверка при старте

Ядро, запущенное под root (euid 0) командой `lxd` или `run`, проверяет собственный
бинарь (`EvalSymlinks(os.Executable())`) инвариантом 2.2.

| Контекст | Нарушение инварианта |
|---|---|
| служба: `ppid == 1` **и** `XPC_SERVICE_NAME == com.leadaxe.sing-box-lxd` | отказ старта |
| `ppid == 1` без метки (nohup, двойной fork), чужая метка, `sudo` из терминала, лаунчер через AEWP | `WARN` в лог, старт |
| флаг `lxd --allow-unsafe-exec` (отладка) | `WARN` в лог, старт |
| не root | проверки нет |

Текст отказа: `lxd: refusing to run as a root service from <бинарь> (uid N, mode NNNN):
<нарушивший компонент>: owned by uid N, mode NNNN, must be root-owned and not
group/world-writable; run `sing-box lxd --service=install` to reinstall from a
root-owned copy`. launchd пишет его в `lxd.log` (StandardErrorPath) при каждой попытке
рестарта. `WARN` называет тот же путь, владельца, режим и контекст (`ppid`,
`XPC_SERVICE_NAME`).

**Существующие системные установки** (plist указывает на бинарь в бандле) после
обновления бинаря до этой версии перестают стартовать на ближайшем рестарте, пока не
выполнена переустановка `sudo sing-box lxd --service=install`. Это намеренно: служба,
исполняющая от root чужой для root файл, — и есть дефект.

### 2.9 `/admin/info`

Два новых поля:

- `executable` — `EvalSymlinks(os.Executable())` работающего демона;
- `executable_sha256` — sha256 этого файла, считается один раз при старте в фоне
  (управляющий канал не ждёт хеша на роутерах); пустая строка, пока хеш не готов или
  если файл не прочитан.

## 3. Интерфейс для лаунчера

- **Пути:** бинарь `/Library/PrivilegedHelperTools/sing-box-lxd`, сайдкар
  `/Library/PrivilegedHelperTools/sing-box-lxd.install.json` (JSON 2.3 п. 4,
  читается без root); plist `/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist`.
  С `--exec-dir <dir>` — те же имена в `<dir>`.
- **Команды** (из бинаря бандла):

  | Команда | root | Что делает | Выход |
  |---|---|---|---|
  | `sing-box lxd --service=copy` | да | копия + сайдкар, без службы | 0 / 1 ошибка |
  | `sing-box lxd --service=install` | да | копия + сайдкар + plist + launchd | 0 / 1 |
  | `sing-box lxd --service=status` | нет | отчёт | 0 OK, 2 MISMATCH/UNSAFE, 3 NOT INSTALLED, 4 COPY ONLY, 5 NOT RUNNING, 1 ошибка |
  | `sing-box lxd --service=uninstall [--purge]` | да (для системной области) | снять службу и/или копию по сайдкару | 0 / 1 |
  | `sing-box lxd --service=uninstall --keep-copy` | да | снять службу, копию оставить для запуска без службы (→ copy only) | 0 / 1 |

  Все принимают `--exec-dir <dir>`; `--dry-run` — все, кроме status.
- **«Та же ли версия ядра у демона»** — сравнение sha256, не путей:
  `executable_sha256` из `/admin/info` (или `sha256` сайдкара) против sha256 своего
  бинаря. Путь демона теперь всегда отличается от пути в бандле. Версия копии без
  запуска — поле `version` сайдкара.
- **Classic TUN (root без launchd):** `sudo sing-box lxd --service=copy`, затем запуск
  `/Library/PrivilegedHelperTools/sing-box-lxd run …` от root.
  Обновление ядра — снова `--service=copy`.
- **Ключевые строки:** `lxd: copied <src> -> <dst> (sha256 <hex>, root:wheel 0755)`;
  `lxd: binary unchanged (sha256 <hex>), copy skipped`; `lxd: already up to date <hex>`;
  `lxd: copy left in place: sha differs from sidecar …`; `lxd: verdict: …`; отказ и
  `WARN` самопроверки (2.8).
- Старый лаунчер (до бампа пина) сравнивает пути и покажет «another core» —
  косметика.

## 4. Границы

- Linux: служба печатает рецепт, бинарь в unit выбирает оператор; `--exec-dir`
  игнорируется с пометкой, `copy` и `status` — «macOS only». Самопроверка на Linux не
  срабатывает строго (нет метки launchd) — только `WARN` под root вне root-owned пути.
- Одновременные `copy`/`install` с **разными** бинарями: каждый бинарь и сайдкар встаёт
  атомарно, но последний `rename` бинаря и последний `rename` сайдкара могут принадлежать
  разным вызовам — status покажет `MISMATCH` (файл ≠ сайдкар), повтор команды чинит.
  Блокировки нет.
- ACL и флаги файловой системы (`uchg`) инвариантом не проверяются.
- Подпись копии не проверяется и не меняется.

## 5. Критерии приёмки

1. Табличный тест предиката и цепочки (uid, режим, симлинк, отсутствующий компонент,
   sticky) — на Linux в CI без root. Выполнено (`TestInvariant*`).
2. Копирование во временном каталоге: не обычный файл, симлинк, расхождение sha
   (временный файл удалён, старая копия цела), пропуск unchanged, пропуск «источник =
   копия», замена по `rename` (старый inode читается), dry-run; chown без root
   пропускается; каталог старой раскладки на месте копии — отказ, uninstall его не трогает.
   Выполнено (`TestCopy*`).
3. Сайдкар: запись/чтение, ключи JSON, решения uninstall (совпадение, копия без plist →
   удалено; расхождение sha, чужой plist, нет сайдкара → оставлено; сайдкар без файла →
   удалён) и `--keep-copy` (отвязка своей копии; no-op для отвязанной; без root, dry-run,
   расхождение sha, чужой plist, сайдкар без файла — без изменений). Выполнено (`TestSidecar*`).
4. Самопроверка по контекстам 2.8. Выполнено (`TestInvariantSelfCheck`).
5. darwin: табличный тест переходов none → copy only → installed → (обновление ядра) →
   copy only (`--keep-copy`, повтор — no-op) → installed → none и copy only → none с вердиктом и кодом после каждого шага; аномалии → код 2 с
   причиной; plist ↔ `ProgramArguments` туда-обратно; dry-run install/copy; разбор
   `launchctl print`; дефолтный каталог не создаётся, старая раскладка — отказ и exit 2.
   Выполнено (`TestServiceStateTransitions`, `TestServiceExecDirAndLegacyLayout`,
   `TestServiceLeavesEarlierBuildFiles`,
   `TestServiceStatusAnomalies`, `TestBuildPlist*`, `TestDryRun*`, `TestServiceLaunchctlPrint`).
6. `admin_test`: `executable`, `executable_sha256`. Выполнено (`TestAdminInfo*`).
7. CI: `GOOS=darwin go vet` для `./lxd/ ./cmd/sing-box/` в lint-джобе. Выполнено.
8. Ручная проверка владельцем (нужен sudo) — docs-lx/lxd-daemon.md §7.1: install →
   `ls -l`, `plutil -p`, `shasum -a 256`, `launchctl print`, `--service=status` = `OK`;
   повторный install и copy = пропуск; uninstall удаляет plist, копию и сайдкар; copy →
   status `COPY ONLY` → uninstall. После неё — статус D.
