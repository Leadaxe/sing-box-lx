# SPEC: 057 — LXD_MTLS_SERVICE

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — mTLS-канал с регистрацией клиентов, системная служба, start/stop, память run-состояния |
| Статус | C (complete) — в дереве, юниты зелёные, device-verified на macOS: mTLS-enrollment, обе роли службы (user без sudo + system под sudo), крэш-цикл на cwd-relative пути пойман и починен |
| Ветка | `lx` |
| Base | `02357244a` (SPEC 055) + дифф SPEC 056 |
| Связанные | [SPEC 055](../055-LXD_DAEMON_SKELETON/SPEC.md), [SPEC 056](../056-LXD_APPLY_ROLLBACK/SPEC.md), [FEATURE 014](../../FEATURES/014-LXD_DAEMON/FEATURE.md) |

**Touches:** только пакет `lxd/` (новые файлы tlsca/clients/localclient/service_*) и
`cmd/sing-box/cmd_lxd_lx.go`. Ни одной правки в апстримных файлах.

## Why

Дизайн-сессия с владельцем (2026-08-08) зафиксировала CLI-контракт демона и
модель доступа. Скелет (055) + apply (056) давали локальный loopback-канал без
шифрования; для удалённого сервера нужен взаимно-аутентифицированный канал,
регистрация клиентов по модели WG-пиров, управление жизнью ядра отдельно от
конфига и восстановление состояния после рестарта. Плюс — установка демона
системной службой одной командой.

## Design (владельца, 2026-08-08)

**CLI-контракт.** Демон — голая команда `sing-box lxd` (не `run`): порт вшит
(`--listen`, дефолт `127.0.0.1:9091`), `-c` необязателен. Управление клиентами —
`sing-box lxd client add|list|remove`.

**Источник конфига и запуск.** `--config-force <файл>` — всегда бутиться с него;
иначе last-good; иначе `-c`-затравка; иначе idle. `--run` форсит подъём ядра;
без него запуск определяет память run-состояния (`was_running` в state-dir).
Апстрим-модель boxdd: рестарт восстанавливает «как было».

**start/stop.** `/admin/start` и `/admin/stop` управляют жизнью ядра, не трогая
демон и канал; stop пишет «stopped» в память (рестарт не поднимет ядро).

**mTLS + регистрация.** Демон — сам себе CA (`--tls`): на первом старте
самоподписывает серверную пару (ECDSA P-256, стабильный отпечаток в state-dir).
Нет зарегистрированных клиентов → печатает приглашение `адрес#отпечаток#код`.
Лаунчер пинит сервер по отпечатку из строки, генерит свою клиентскую пару,
`POST /admin/enroll` с кодом → сервер пинит клиентский серт. Код одноразовый
(сгорает после регистрации одного клиента), без TTL (не подобрать —
`ABCDEFGHJKLMNPQRSTUVWXYZ23456789`, 3 группы по 4). Все плоскости (admin-REST
и gRPC-наблюдение) требуют пиннингованного клиентского серта; enroll — единственный
маршрут до доверия, за одноразовым кодом. `client add` минтит новый код у живого
демона (день N, без рестарта), `list`/`remove` — просмотр и отзыв.

**Секрет.** `--secret-file` (0600) / env поверх argv-флага `--secret`
(виден в ps); сравнение constant-time. Bearer — второй фактор поверх пина.

**Служба.** `--service=install|install-user|uninstall|print`. `install` —
системный LaunchDaemon (root, для сервера: TUN, до логина, `/Library/…`);
`install-user` — пользовательский LaunchAgent (без sudo, десктоп-UX как у
обычных .app, `~/Library/…`, `gui/<uid>`). Обе роли переносят командную строку
демона (минус `--service`) в plist (`RunAtLoad`+`KeepAlive` = launchd-аналог
`Restart=always`), **абсолютизируя все пути** (state-dir/config-force/`-c`) — иначе
launchd-юнит с cwd `/` крэш-циклит на относительном `--state-dir`; дефолтный
state-dir службы = `<support>/state` (абсолютный). install сам создаёт каталоги
лога/состояния. `print` — сухой прогон plist. `uninstall` снимает любой найденный
scope (сначала user, потом system) и по умолчанию **оставляет** state (клиенты/
last-good/ключи — чтобы переустановка сохранила enrollment), печатая подсказку
про `--purge`; `--purge` сносит support-каталог. Non-interactive (без Y/N) —
uninstall идёт из скриптов/сервиса без TTY. Linux/Windows — заглушки.

## Verification

- Юниты (зелёные): пайплайн apply/rollback (056), store (атомарность,
  pending/was_running, ReadFile-ошибка ≠ отсутствие файла), admin-auth.
- Сборка ± тег (стаб без тега пуст), gofmt, go vet чисты.
- Сквозное mTLS-демо на macOS (сборка `1.14.0-lx.23-lxd3`, порт 19092):
  голый старт → idle; plain-HTTP отбит; приглашение напечатано; пин сервера по
  отпечатку совпал; enroll по коду + клиентский серт; без серта → «client
  certificate not trusted»; повторный enroll тем же кодом → «no active
  enrollment code» (сгорел); apply/stop по mTLS, канал жив после stop; память
  run-состояния (stop→рестарт idle; `--run`→started); приглашение не печатается
  при наличии клиента; `client list/add/remove` через живой демон;
  `--service=print` даёт корректный plist (командная строка → ProgramArguments).
- **Служба device-verified на macOS (обе роли):** `install-user` без sudo →
  launchd `state = running`, `runs = 1`, `never exited`, порт слушается,
  приглашение в логе; `KeepAlive` перезапустил после `kill -9` (новый pid);
  `uninstall` без `--purge` оставил state + подсказку, с `--purge` снёс каталог.
  Системный `install` (sudo, владельцем): первый прогон **поймал крэш-цикл**
  (`runs=5`, `last exit code=1`, `mkdir lxd-state: read-only file system` —
  относительный state-dir резолвился в `/`); после фикса абсолютных путей —
  `state = running`, `runs = 1`, `never exited`, pid от root, порт 19093 LISTEN,
  приглашение в логе, `uninstall --purge` снял plist и вычистил каталог.

## Из ревью 056 (внесено в этот же код)

Bootstrap под `applyAccess` (гонка с первым apply); shutdown под мьютексом +
буфер сигналов 4 (потеря SIGTERM); ошибки стора → отдельный исход `applyError`
(500, не 422); `setFatal` чистит active (config не отдаёт мёртвый конфиг);
ReadFile различает «нет файла» от IO-ошибки; http.Server с таймаутами
(ReadHeader/Idle); мёртвый тест temp-файлов починен.
