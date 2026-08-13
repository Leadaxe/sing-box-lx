# SPEC: 065 — LXD_OBSERVABILITY_PLANE

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — третья полезная нагрузка admin-плоскости: диагностика самого демона (лог, метрики, профили) |
| Статус | C (complete) — в дереве, юниты зелёные (`go test -tags with_lx_command ./lxd/ ./daemon/`), бинарник `make -f Makefile.lx lx-build` собран, живой прогон на macOS проведён: все девять маршрутов, `go tool pprof` открывает и heap, и CPU-профиль с символизацией |
| Ветка | `lx` |
| Base | `0fca919b5` |
| Связанные | [SPEC 056](../056-LXD_APPLY_ROLLBACK/SPEC.md) (admin-плоскость), [SPEC 057](../057-LXD_MTLS_SERVICE/SPEC.md) (mTLS-пин), [SPEC 063](../063-LXD_RESOURCE_STORE/SPEC.md) (паттерн регистрации маршрутов), [FEATURE 014](../../FEATURES/014-LXD_DAEMON/FEATURE.md) |

**Touches:** пакет `lxd/` — новые файлы `debugplane.go` (метрики + pprof),
`logtail.go` (хвост лога) + маршруты в [lxd/admin.go](../../../lxd/admin.go).
Читает `logRotator.path` из [lxd/logrotate.go](../../../lxd/logrotate.go) и
`trafficManager`/`startedAt` через `controller.service`. Апстримные файлы —
не трогаются.

## Why

Демон существует ради того, чтобы управление жило, когда data-plane лежит. Но
сегодня, когда лежит **сам демон** (течёт память, встал в дедлок, ест CPU),
диагностировать его снаружи нечем.

**Лог демона недостижим по сети.** `SubscribeLog` по gRPC отдаёт логи **ядра** —
то, что прошло через `PlatformLogWriter` инстанса
([daemon/instance.go:128](../../../daemon/instance.go)). А `log.Info("lxd: …")`,
ошибки бутстрапа и паники идут в глобальный логгер, то есть в `lxd.log` через
dup2 stdout/stderr. По сети их не достать: `GET /admin/info` отдаёт лишь
`log_path` ([lxd/admin.go:320](../../../lxd/admin.go)) — путь к файлу на
удалённом хосте, для лаунчера бесполезный. Именно в худшем сценарии — ядра нет,
`SubscribeLog` пуст, причина в `lxd.log` — канала нет.

**Профилировать можно только через дыру.** Единственный путь сегодня — вписать
`experimental.debug.listen` в конфиг ядра и поднять **второй порт без всякой
аутентификации** ([debug_http.go](../../../debug_http.go)). На сервере, смотрящем
в сеть, это неприемлемо.

**Трафик и соединения не видны без gRPC.** Числа есть в `readStatus()`
([daemon/started_service.go:473](../../../daemon/started_service.go)), но только
через gRPC-стрим. Клиенту, которому нужен разовый снимок, приходится
подписываться на поток.

## Design

Всё живёт на **существующем mTLS-порту**, в основном `mux`
([lxd/admin.go:27](../../../lxd/admin.go)) — рядом с `/admin/resources`, под тем
же пином клиентского сертификата. Отдельного debug-listener'а нет по построению:
ручки нужны именно **удалённо** (лаунчер снимает профиль с сервера), а второй
незащищённый порт — ровно та дыра, которую фича закрывает.

Клиентский сертификат = полный доступ к профилям. Осознанный размен: тот же серт
уже позволяет `POST /admin/apply` с произвольным конфигом, то есть исполнять код
в контексте демона. Профиль — строго меньшая привилегия.

Все ручки профилируют **процесс демона целиком**. Разделить «память ядра» и
«память демона» они не могут — ядро живёт внутри того же процесса, у Go нет
per-subsystem учёта.

### Эндпоинты

| Эндпоинт | Семантика |
|---|---|
| `GET /admin/memory` | снимок памяти процесса: heap, goroutines, RSS |
| `GET /admin/stats` | uptime **ядра**, трафик total, активные соединения |
| `GET /admin/logs?tail=N` | хвост `lxd.log` (лог демона) |
| `GET /admin/pprof` | список профилей со счётчиками и статусом |
| `GET /admin/pprof/{name}` | снимок: `heap`\|`allocs`\|`goroutine`\|`threadcreate`\|`block`\|`mutex` |
| `GET /admin/pprof/profile?seconds=N` | CPU-профиль, держит соединение N секунд |
| `GET /admin/pprof/trace?seconds=N` | runtime trace, держит соединение N секунд |
| `POST /admin/pprof/block?rate=N` | вкл/выкл block-профилирования (0 = выкл) |
| `POST /admin/pprof/mutex?fraction=N` | вкл/выкл mutex-профилирования (0 = выкл) |

### `GET /admin/memory`

Дешёвый снимок, пригодный для периодического опроса — график в лаунчере, детект
утечки, ответ на «почему демон съел 800 МБ».

```json
{
  "heap_inuse_bytes": 41943040,
  "heap_idle_bytes": 12582912,
  "stack_inuse_bytes": 2359296,
  "sys_bytes": 98566144,
  "inuse_bytes": 54525952,
  "goroutines": 412,
  "num_gc": 1843,
  "gc_pause_total_ns": 284917653,
  "rss_current_bytes": 73400320,
  "rss_peak_bytes": 91226112
}
```

**Числа — в байтах, не отформатированные строки.** Расхождение с
[debug_http.go:36](../../../debug_http.go), где `byteformats.FormatMemoryBytes`
даёт `"40.0 MiB"`, — намеренное: там ручка для оператора в браузере, здесь
потребитель — программа, строящая график. Парсить `"40.0 MiB"` обратно — работа
на ровном месте плюс потеря точности.

**Единица — в имени поля.** Раз числа сырые, суффикс `_bytes` снимает вопрос
«байты или мегабайты» на месте, без чтения доков; поля без суффикса (`goroutines`,
`num_gc`) единицы не имеют, а `gc_pause_total_ns` несёт свою. Тот же принцип, что
у `_seconds`/`_ns` в остальных ответах admin-плоскости.

Источники: `runtime.MemStats` (heap/stack/sys/gc), `runtime.NumGoroutine()`,
`memory.Inuse()` из `sing/common/memory` (та же формула
`Stack+Heap+Idle-Released`, что у OOM-киллера libbox).

**Ловушка RSS — два разных числа.** `rusageMaxRSS()`
([debug_unix.go:10](../../../debug_unix.go)) отдаёт `ru_maxrss` — **пик за жизнь
процесса**, монотонно неубывающий. Сегодня это поле называется `"rss"`, и
читатель почти наверняка понимает его неверно. Для графика оно негодно: утечка
визуально неотличима от разового всплеска. Поэтому отдаём оба, под честными
именами:

- `rss_peak_bytes` — `Getrusage(RUSAGE_SELF).Maxrss`, как сейчас;
- `rss_current_bytes` — **текущий** RSS: на linux `/proc/self/statm`, второе поле ×
  `os.Getpagesize()` (чистое чтение файла, без cgo). На darwin честный
  `task_info` требует cgo, а `lxd` сейчас чистый Go — отдаём `-1`
  (не поддержано); сервера всё равно Linux, darwin у нас dev-хост. Windows —
  `-1`, как и весь остальной lxd-стаб.

**Троттлинг.** `runtime.ReadMemStats` делает stop-the-world (десятки микросекунд
на здоровом процессе, но не бесплатно), `NumGoroutine` берёт блокировку
планировщика. Результат кешируется на 200 мс — тот же порядок, что
`minSubscribeInterval`
([daemon/started_service.go:431](../../../daemon/started_service.go)), где мы уже
ловили клиента, спалившего ядро CPU кривым интервалом (наносекунды vs
миллисекунды, фикс `fca1d367e`). Защита от повторения того же класса бага,
клиенту невидима.

### `GET /admin/stats`

```json
{
  "core_uptime_seconds": 3612,
  "uplink_total": 184320917,
  "downlink_total": 2947183640,
  "connections": 47
}
```

Источники — те же, что у `readStatus()`
([daemon/started_service.go:473](../../../daemon/started_service.go)):
`trafficManager.Total()`, `trafficManager.ConnectionsLen()`,
`StartedService.startedAt`.

**Uptime здесь — ядра, не демона.** Uptime демона уже отдаёт
`GET /admin/info` (`uptime_seconds`).

**Когда ядра нет — все поля `null`, ответ 200.** Ручка про демон и обязана
работать всегда: в `idle` и в `fatal` она сама и есть источник ответа «ядра
нет». 503 на всю ручку сделал бы её бесполезной ровно тогда, когда клиент хочет
понять состояние.

Скорость (rate) **не отдаём** — только накопленные total. Rate клиент считает
сам из двух опросов, если он ему нужен; хранить в демоне счётчик скорости ради
этого незачем.

### `GET /admin/logs?tail=N`

Снимок хвоста файла `lxd.log` — то, чего нет в `SubscribeLog`. Работает всегда,
даже когда ядра нет вовсе; win7-клиент забирает обычным stdlib-HTTP (grpc-go там
недоступен — см. [ограничения лаунчера](../../../SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md)).

- `tail=N` — последние N строк, дефолт 200, потолок 5000.
- Путь берётся из того же `DefaultLogPath(stateDir)`, что отдаёт
  `/admin/info.log_path`; клиент путь не собирает.
- Файла нет (лог в терминал, dev-режим) → 404 с внятным текстом, не 500.
- Ротированный `lxd.log.1` дочитывается, когда в текущем меньше N строк.
- `Content-Type: text/plain`.

Живой `tail -f` по REST (SSE/chunked) — **вне объёма**: разовый снимок плюс
живой gRPC `SubscribeLog` покрывают сценарии, а стриминг требует своей
дисциплины таймаутов на сервере, у которого `IdleTimeout: 120s`.

### `GET /admin/pprof/*`

Ключевое различие, определяющее семантику маршрутов: профили Go делятся на два
класса.

**Снимки** — `heap`, `allocs`, `goroutine`, `threadcreate`. Рантайм ведёт их
непрерывно с самого старта процесса, без включения (heap-сэмплирование —
`MemProfileRate`, одна проба на 512 КБ, всегда). `GET` **ничего не запускает**,
он сериализует накопленное и отдаёт за миллисекунды. Профиль, снятый через час
работы, покрывает весь этот час.

**Записи за интервал** — `profile` (CPU) и `trace`. Здесь `GET` действительно
запускает запись, держит соединение `seconds` секунд, потом отдаёт. Данные до
момента запроса не покрываются вообще.

**Особый случай** — `block` и `mutex`: устроены как снимки, но копятся только при
включённом rate, а по умолчанию он нулевой (проверено на этом бинаре:
`pprof.Profiles()` → `block count=0`, `mutex count=0`). В отличие от
heap-сэмплирования, встроенного в аллокатор почти бесплатно, учёт блокировок
берёт метку времени на каждой операции с примитивом синхронизации — на горячем
пути заметно.

Отсюда — фонового налога от самих ручек нет: `heap`/`goroutine` читают то, что
рантайм пишет и так, а дорогие `block`/`mutex` выключены, пока их явно не
включили.

**Whitelist имён, а не `pprof.Index`.** Стандартный `net/http/pprof` отдаёт
HTML-страницу и обслуживает `/debug/pprof/*` рефлексивно
([debug_http.go:54](../../../debug_http.go)). Нам нужен API для программы:
неизвестное имя → `404 {"error": "no such profile"}`, а не индекс-страница.

**`/symbol` и `/cmdline` не переносим.** `cmdline` выдаёт полную командную строку
демона, `symbol` — резолвер адресов; оба не нужны для `go tool pprof` над
скачанным файлом (символизация происходит на машине разработчика, где лежит
бинарь с DWARF) и оба — лишняя поверхность.

**Формат.** Сырой вывод `p.WriteTo(writer, 0)` — gzip'нутый protobuf, ровно то,
что ест `go tool pprof`:

```
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="heap-20260813T141530Z.pb.gz"
X-Content-Type-Options: nosniff
```

`debug=1` → человекочитаемый текст (`text/plain`); `debug=2` для `goroutine` →
полный дамп стеков, тот самый, что печатается при панике. Последнее — самая
полезная ручка при «демон завис».

**CPU-профиль: три ограничения.**

1. **Потолок `seconds`** — дефолт 30, максимум 120, больше → 400. Причина не в
   CPU (сэмплирование стоит единицы процентов), а в удержании соединения: у
   HTTP-сервера lxd `IdleTimeout: 120s`
   ([lxd/daemon.go:64](../../../lxd/daemon.go)), и профиль длиннее упрётся в
   собственный таймаут инфраструктуры. Потолок делает отказ внятным вместо
   загадочного обрыва.
2. **Взаимное исключение** — `runtime.StartCPUProfile` возвращает ошибку, если
   запись уже идёт. Второй параллельный запрос → `409` с текстом «CPU profile
   already running». Реализуется `atomic.Bool`, не мьютексом: ждать 30 секунд в
   очереди хуже, чем сразу получить внятный отказ. То же для `trace`.
3. **`WriteTimeout` не ставить.** Сейчас `http.Server`
   ([lxd/daemon.go:141](../../../lxd/daemon.go)) его не выставляет, так что
   проблемы нет — но это надо зафиксировать комментарием в коде: первый же
   добавленный `WriteTimeout` молча оборвёт долгие профили. Урок из
   [SPEC 050](../050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md) — дедлайны в
   этом коде ведут себя не так, как кажется.

**Включение block/mutex.** Два разных вызова с разной семантикой:
`runtime.SetBlockProfileRate(rate)` — rate в **наносекундах** (сэмплировать
блокировку такой средней длительности; `10000` ≈ рабочий выбор, `1` = каждое
событие, очень дорого); `runtime.SetMutexProfileFraction(n)` — записывать `1/n`
событий конкуренции. `0` выключает оба; для mutex `0` при этом **не сбрасывает**
уже накопленное.

Только `POST` — это изменение состояния процесса. Состояние **не переживает
рестарт демона**: никакой записи в `daemon.json`, включил → поработал → снял →
выключил.

**`GET /admin/pprof` — список со статусом**, чтобы клиент не гадал, почему пусто:

```json
{
  "profiles": [
    {"name": "heap",         "count": 4821, "enabled": true},
    {"name": "allocs",       "count": 4821, "enabled": true},
    {"name": "goroutine",    "count": 412,  "enabled": true},
    {"name": "threadcreate", "count": 12,   "enabled": true},
    {"name": "block", "count": 0, "enabled": false, "hint": "POST /admin/pprof/block?rate=10000"},
    {"name": "mutex", "count": 0, "enabled": false, "hint": "POST /admin/pprof/mutex?fraction=100"}
  ],
  "cpu_profile_running": false
}
```

`enabled: false` — прямой ответ на «почему пусто» без похода в документацию;
`cpu_profile_running` избавляет от попытки получить 409.

### Клиентский цикл

```
«демон ест CPU»        → GET  /admin/pprof/profile?seconds=30
«демон завис»          → GET  /admin/pprof/goroutine?debug=2
«память растёт»        → GET  /admin/memory  (график)
                       → GET  /admin/pprof/heap  (дважды, сравнить)
«подозрение на lock»   → POST /admin/pprof/mutex?fraction=100
                         … под нагрузкой …
                       → GET  /admin/pprof/mutex
                       → POST /admin/pprof/mutex?fraction=0
«что случилось»        → GET  /admin/logs?tail=500
```

## Out of scope

- **Отдельный флаг `debug` в daemon.json.** Обсуждался (профили раскрывают имена
  функций и стеки), отклонён владельцем: ручки за mTLS-пином, а тот же серт уже
  даёт `apply` произвольного конфига — то есть больше.
- **Живой стриминг лога по REST** (SSE/chunked) — снимок + gRPC `SubscribeLog`
  покрывают сценарии.
- **Слияние лога демона в `SubscribeLog`** — обсуждалось как способ дать единую
  живую ленту (демон + ядро); отдельная задача, требует хука в `log.Factory`.
- **Rate трафика** — клиент считает из двух опросов.
- **`connections_out`** (`connectionManager.Count()`), разбивка соединений по
  outbound, задержки urltest — доступны даром, но не запрошены.
- **`/symbol`, `/cmdline`** — см. Design.
- **Разделение памяти «ядро vs демон»** — невозможно, один процесс.
- `/admin/memory` **не заменяет** OOM-репорты libbox
  ([experimental/libbox/report.go](../../../experimental/libbox/report.go)): там
  снапшот снимается автоматически в момент, когда опрашивать снаружи уже поздно.
  Разные механизмы, оба нужны.

## Acceptance

- [x] `GET /admin/memory` → 200 с числами в байтах; повторный запрос внутри
  200 мс отдаёт кеш (не вызывает `ReadMemStats` повторно).
  (`TestMemoryEndpointReportsRawBytes`, `TestMemoryCacheThrottles`,
  `TestMemoryCacheNilSafe`; живьём — все десять полей на работающем демоне)
- [x] `rss_current_bytes` на linux читается из `/proc/self/statm`; на
  darwin/windows = `-1`. `rss_peak_bytes` ≥ `rss_current_bytes` там, где оба
  реальны. (`TestMemoryRSSFieldsHonest`; **живьём на macOS**: `rss_current_bytes:
  -1`, `rss_peak_bytes: 30511104` — расхождение по платформе подтверждено)
- [x] `GET /admin/stats` при живом ядре → трафик ненулевой после прогона;
  `core_uptime_seconds` растёт. (`TestStatsWithLiveCore`; **живьём**: после
  запроса через mixed-inbound `uplink_total: 74`, `downlink_total: 874`)
- [x] `GET /admin/stats` без ядра → **200**, все поля `null` (не 503, не 500).
  (`TestStatsWithoutCoreIsNullNot503`, `TestStatsWithPlainReloaderReportsNoCore`;
  **живьём**: после `POST /admin/stop` все четыре поля `null`, `/admin/memory`
  продолжает отвечать)
- [x] `GET /admin/logs?tail=N` отдаёт последние N строк; `tail` клампится;
  файла нет → 404, не 500; при коротком текущем логе дочитывает `lxd.log.1`.
  (`TestLogTailReturnsLastLines`, `TestLogTailReadsRotatedGeneration`,
  `TestLogEndpointMissingFileIs404`, `TestLogTailClamp`; **живьём** под nohup
  видны строки демона `lxd: core started from seed`, которых нет в gRPC-потоке)
- [x] `GET /admin/pprof` перечисляет 6 профилей; `block`/`mutex` имеют
  `enabled:false` до включения и `true` после `POST`.
  (`TestProfileListReportsEnabledState`, `TestBlockAndMutexToggle`; **живьём**:
  `block` после `rate=1000` набрал `count=15` при нуле до включения)
- [x] `GET /admin/pprof/heap` отдаёт валидный pb.gz — **живьём**: `go tool pprof
  -top` открыл файл с символизацией (`runtime.allocm`, `observable.NewSubscriber`).
  (`TestProfileSnapshotServesGzip` — проверка gzip-магии и заголовков)
- [x] `GET /admin/pprof/goroutine?debug=2` отдаёт `text/plain` со стеками.
  (`TestProfileGoroutineDebugIsText`; живьём — дамп с именами функций)
- [x] `GET /admin/pprof/nosuch` → 404 JSON, **не** индекс-страница pprof;
  `cmdline`/`symbol` тоже 404. (`TestProfileUnknownNameIs404JSON`,
  `TestProfileNotWhitelistedIsRejected`; подтверждено живьём)
- [x] `GET /admin/pprof/profile?seconds=N` отдаёт профиль; `seconds=999` → 400;
  два параллельных запроса → второй 409. (`TestCPUProfileSecondsBounds`,
  `TestCPUProfileConcurrentIs409`; **живьём**: запись 3.05 с, параллельный
  запрос 409, `cpu_profile_running:true` во время записи)
- [x] `POST /admin/pprof/block?rate=…` включает, `rate=0` выключает; мусор → 400.
  (`TestBlockAndMutexToggle`, `TestBlockRateRejectsGarbage`)
- [x] Все маршруты требуют доверенный клиентский серт (без него → 401) и **не**
  на операторском loopback-only пути — доверенный клиент достаёт их с
  не-loopback адреса. (`TestObservabilityRoutesArePinned`,
  `TestObservabilityRoutesReachableRemotely`)
- [x] Юниты зелёные: `go test -tags with_lx_command ./lxd/ ./daemon/`; сборка
  без тега (`go build ./lxd/`) не ломается; `gofmt` чист.
- [x] Сборка `make -f Makefile.lx lx-build`; живой прогон на macOS проведён.
- [ ] Полевая проверка под настоящей службой (`--service=install`) с ротацией
  лога и mTLS-клиентом — **долг**.
