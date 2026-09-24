# SPEC: 100 — LXD_ROOT_OWNED_BINARY

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — повышение привилегий через системную службу SPEC 057 |
| Статус | O (open) |
| Ветка | `spec100-lxd-root-owned-binary` (от `lx`) |
| База | `4146bc8ce` |
| Релиз | `v1.14.1-lx.11` |
| Связано | [057](../057-LXD_MTLS_SERVICE/SPEC.md) (служба `--service`), [065](../065-LXD_OBSERVABILITY_PLANE/SPEC.md) (`/admin/info`) |

Решение владельца 2026-09-24, нормы согласованы с сессией-владельцем ядра.

**Цена мержа:** ноль апстримных файлов. Правки — пакет `lxd/`,
`cmd/sing-box/cmd_lxd_lx.go`, `.github/workflows/lx-ci.yml`, документация.

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

## 2. Решение: служба исполняет root-owned копию

### 2.1 Канонический путь

| Что | Путь | Владелец / режим |
|---|---|---|
| каталог копии | `/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/` | `root:wheel 0755` |
| копия бинаря | `/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box` | `root:wheel 0755` |
| сайдкар-маркер | `/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/install.json` | `root:wheel 0644` |

`/Library/PrivilegedHelperTools` — соглашение Apple для привилегированных помощников.
Имя бинаря остаётся `sing-box` (граница форка); ярлык службы — имя каталога.
Каталоги создаются, если их нет: `root:wheel 0755`.

### 2.2 Инвариант root-owned

Путь проходит инвариант, если **каждый компонент от `/` до него включительно**
(по `Lstat`, без следования симлинкам):

- не симлинк;
- принадлежит uid 0;
- не имеет записи для group и other (`mode & 022 == 0`; sticky/setuid не мешают).

Нарушение — отказ с текстом `<путь>: owned by uid N, mode NNNN, must be root-owned
and not group/world-writable` (для симлинка — `<путь>: is a symbolic link, …`).
Предикат платформенно-нейтрален (uid, режим, признак симлинка) — его табличный тест
идёт в CI на Linux без root. ACL не проверяются (у `/Library/PrivilegedHelperTools`
их нет по умолчанию).

### 2.3 Установка (`--service=install`, root)

1. Источник = `filepath.EvalSymlinks(os.Executable())`; он обязан быть обычным файлом
   (`Lstat`, не симлинк) не больше 512 МиБ.
2. Каталог копии — `--exec-dir <dir>` или канонический. Путь проходится от `/`:
   существующие компоненты проверяются инвариантом, недостающие создаются
   `root:wheel 0755`. Нарушение — отказ до любых изменений на диске.
3. Копирование: если источник и есть копия (install запущен из неё) — пропуск.
   Если копия существует и её sha256 совпадает с источником — пропуск с сообщением
   `lxd: binary unchanged (sha256 <hex>), copy skipped` (владелец/режим при этом
   приводятся к `root:wheel 0755`). Иначе: временный файл в том же каталоге
   (`O_EXCL`, `0600`) → копия байтов → `fsync` → `chown root:wheel` → `chmod 0755` →
   sha256 копии == sha256 источника (иначе временный файл удаляется, отказ) →
   `rename` на место → `fsync` каталога.
   - Перезапись на месте запрещена: работающий демон держит старый inode, а ядро macOS
     убивает процесс, у которого поменялись страницы подписанного кода. `rename`
     атомарен и старый inode не трогает.
   - Никаких xattr (копируются только байты, карантина нет) и никакой повторной
     подписи: ad-hoc подпись встроена в Mach-O, переподпись сломала бы sha.
   - Копия самодостаточна: darwin-релиз линкует libcronet статически (CGO), соседних
     библиотек не требуется.
4. Сайдкар `install.json` перезаписывается на каждой установке (атомарно, `root:wheel 0644`,
   читается без root):
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
   При установке из самой копии `source` берётся из прежнего сайдкара.
5. plist: меняется **только** `ProgramArguments[0]` — на путь копии. Аргументы,
   пути state-dir/конфига/лога, `daemon.json` (адрес, секрет), доверенные клиенты —
   как были.
6. Support-каталог (`/Library/Application Support/sing-box-lxd/` и `state/` в нём)
   приводится к `root:wheel` явно: по BSD-семантике он наследовал группу `admin`
   родителя.
7. В конце печатается отчёт `--service=status` по системной области; вердикт не `OK` —
   ошибка install.

`--service=install-user` (LaunchAgent, без root) не меняется: агент исполняется от
того же пользователя, которому принадлежит бинарь, повышения нет. Отчёт status в конце
печатается и там.

`--dry-run` печатает план без изменений: проверку каталога (что создалось бы),
источник → цель с sha256, `chown root:wheel` / `chmod 0755`, пропуск при совпадении
sha, путь сайдкара, plist с новой `ProgramArguments[0]` и итоговую строку
`lxd: dry run result: …`. Нарушение инварианта в dry-run — та же ошибка, что и в install.

### 2.4 Удаление (`--service=uninstall`)

Копия и сайдкар удаляются **только** если: сайдкар есть, его `label` и `plist_path`
относятся к снимаемому plist, файл — обычный и его sha256 равен sha256 из сайдкара.
Иначе файл остаётся с сообщением (`lxd: copy left in place: sha differs from sidecar …`,
`… no sidecar …`, `… sidecar belongs to …`). Произвольный `ProgramArguments[0]` не
удаляется никогда. Кандидаты — каталог `ProgramArguments[0]` (если файл называется
`sing-box`) и каталог копии (`--exec-dir` или канонический); копия без plist
(осиротевшая) снимается тем же правилом. Сайдкар без файла удаляется как устаревший.
Пустой каталог с именем ярлыка удаляется. `--purge` — как раньше, про support-каталог.
`--dry-run` печатает те же решения со словом `would`.

### 2.5 Самопроверка демона при старте

Демон, запущенный под root (euid 0), проверяет собственный бинарь
(`EvalSymlinks(os.Executable())`) инвариантом 2.2.

| Контекст | Нарушение инварианта |
|---|---|
| служба: `ppid == 1` **и** `XPC_SERVICE_NAME == com.leadaxe.sing-box-lxd` | отказ старта |
| `ppid == 1` без метки (nohup, двойной fork), `sudo` из терминала | `WARN` в лог, старт |
| флаг `--allow-unsafe-exec` (отладка) | `WARN` в лог, старт |
| не root | проверки нет |

Текст отказа: `lxd: refusing to run as a root service from <бинарь> (uid N, mode NNNN):
<нарушивший компонент>: owned by uid N, mode NNNN, must be root-owned and not
group/world-writable; run `sing-box lxd --service=install` to reinstall from a
root-owned copy`. launchd пишет его в `lxd.log` (StandardErrorPath) при каждой попытке
рестарта.

**Существующие системные установки** (plist указывает на бинарь в бандле) после
обновления бинаря до этой версии перестают стартовать на ближайшем рестарте, пока не
выполнена переустановка `sudo sing-box lxd --service=install`. Это намеренно: служба,
исполняющая от root чужой для root файл, — и есть дефект.

### 2.6 `--service=status`

Не требует root, ничего не меняет. Для каждой области (system, user) печатает:
путь plist и есть ли он; `ProgramArguments[0]` (или ключ `Program`); существует ли
файл, uid/gid, режим; проходит ли инвариант (для user-области — информационно);
sha256 программы и sha256 вызывающего бинаря; содержимое сайдкара; состояние launchd
(`launchctl print <domain>/<label>`: state, pid, загруженная программа); вердикт
области. Затем общий вердикт.

| Вердикт | Когда | Код выхода |
|---|---|---|
| `OK` | программа проходит инвариант (system) и её sha256 == sha256 вызывающего бинаря | 0 |
| `MISMATCH` | инвариант пройден, но sha256 программы ≠ sha256 вызывающего бинаря (или у user-агента программы нет) | 2 |
| `UNSAFE` | system-plist указывает не на root-owned копию (нарушение инварианта, файла нет, не обычный файл) | 2 |
| `NOT INSTALLED` | нет plist ни в одной области | 3 |
| ошибка | plist не читается/не разбирается, не читается вызывающий бинарь | 1 |

Общий вердикт — худший из областей (`UNSAFE` > `MISMATCH` > `OK`). Последняя строка —
`lxd: verdict: <ВЕРДИКТ>` (для не-`OK` — с причиной после ` — `).

### 2.7 `/admin/info`

Два новых поля:

- `executable` — `EvalSymlinks(os.Executable())` работающего демона;
- `executable_sha256` — sha256 этого файла, считается один раз при старте в фоне
  (управляющий канал не ждёт хеша на роутерах); пустая строка, пока хеш не готов или
  если файл не прочитан.

## 3. Интерфейс для лаунчера

- **Пути:** каталог `/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/`, бинарь
  `…/sing-box`, сайдкар `…/install.json` (JSON, поля 2.3 п. 4, читается без root);
  plist `/Library/LaunchDaemons/com.leadaxe.sing-box-lxd.plist`.
- **«Та же ли версия ядра у демона»** — сравнение sha256, не путей:
  `executable_sha256` из `/admin/info` (или `sha256` сайдкара) против sha256 своего
  бинаря. Путь демона теперь всегда отличается от пути в бандле.
- **Проверка без root:** `sing-box lxd --service=status` → код 0 OK / 2 нужна
  переустановка (MISMATCH, UNSAFE) / 3 не установлено / 1 ошибка.
- **Исправление:** `sudo sing-box lxd --service=install` из обновлённого бандла —
  копирует бинарь заново, `daemon.json`/клиенты не трогает.
- **Ключевые строки:** `lxd: binary unchanged (sha256 <hex>), copy skipped`;
  `lxd: copied <src> -> <dst> (sha256 <hex>, root:wheel 0755)`;
  `lxd: copy left in place: sha differs from sidecar …`; `lxd: verdict: …`;
  отказ самопроверки (2.5).
- Старый лаунчер (до бампа пина) сравнивает пути и покажет «another core» —
  косметика.

## 4. Критерии приёмки

1. Табличный тест предиката и цепочки (uid, режим, симлинк, отсутствующий компонент,
   sticky) — на Linux в CI без root.
2. Тест копирования во временном каталоге: не обычный файл, симлинк, расхождение sha
   (временный файл удалён), пропуск unchanged, пропуск «источник = копия», замена
   старой копии; chown пропускается без root.
3. Тест сайдкара: запись/чтение, решения uninstall (совпадение → удалено; расхождение
   sha, чужой plist, нет сайдкара → оставлено; сайдкар без файла → удалён).
4. Тест самопроверки по контекстам 2.5.
5. darwin: `buildPlist` ↔ разбор `ProgramArguments` туда-обратно; dry-run system-install
   печатает план копирования и plist с копией, ничего не создаёт; разбор
   `launchctl print`.
6. `admin_test`: `executable`, `executable_sha256` в `/admin/info`.
7. CI: `GOOS=darwin go vet` для `./lxd/ ./cmd/sing-box/` в lint-джобе.
8. Ручная проверка владельцем (нужен sudo): install → `ls -l`, `plutil -p`, sha256,
   `launchctl print`, `--service=status` = `OK`; повторный install = `copy skipped`;
   uninstall удаляет копию. После неё — статус D.

## 5. Границы

- Linux: служба печатает рецепт, бинарь в unit выбирает оператор; самопроверка на
  Linux в строгом режиме не срабатывает (нет метки launchd) — только `WARN` под root
  вне root-owned пути. `--exec-dir` на Linux игнорируется с пометкой.
- ACL и флаги файловой системы (`uchg`) инвариантом не проверяются.
- Подпись копии не проверяется и не меняется.
