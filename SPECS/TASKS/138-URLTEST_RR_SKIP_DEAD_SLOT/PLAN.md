# PLAN: 138 — URLTEST_RR_SKIP_DEAD_SLOT

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md) · правила — [ALGORITHM.md](ALGORITHM.md)

Как устроен код. Комментарии в коде называют правила алгоритма (D1, K2, Q1…); здесь — где что
лежит. Состояние самовосстановления живёт в `balancer` (он есть только у `round_robin`), знание
слота об узле — в трекере слота, который живёт, пока узел держит слот.

## protocol/group — lx-файлы

| Файл | Что внутри |
|------|-----------|
| `urltest_balance_lx.go` | Пул SPEC 019 и выбор слота: `pickKeyed` отдаёт узел и `rrPick` (ключ, сдвиг, момент начала); `usable` и `liveSlot` — обход мёртвых, карантинных и, в строгом заходе, подозрительных слотов; `slotAlive` — history под `RealTag`; `slotsSnapshot`, `replaceSlot` — точечная запись слота без двух узлов в одном слоте |
| `urltest_rr_errors_lx.go` | Классы ошибки открытия по цепочке обёрток: `LOCAL`, активный отказ, таймаут; Windows — по числу WSA-кода, gVisor — по тексту, `tun.ErrNoRoute` |
| `urltest_rr_heal_lx.go` | Исход неоткрывшегося соединения и повтор через другой слот; проба жизни с лимитами; проверка пула (пробы пула, пачки кандидатов от курсора, контроль, решающая проба, «контроль после»), вердикты D1/D3/D4/D5; замена под флагом `checking`; эпоха заморозки и сигналы N3 (пробуждение, смена сети) |
| `urltest_rr_track_lx.go` | `rrTrack` слота: незавершённые обмены по времени, фронт доказательств пути (правило половины), ячейки последнего ответа ключу, подозрение S1–S3, признаки Q1 в ожидании доказательства; осмотр слота на выборе; сверка настенных и монотонных часов |
| `urltest_rr_shift_lx.go` | Ключ таблицы (sticky-ключ плюс адресат), сдвиг K1/K2, память K2 (256 записей на группу), пометка K5; счётчики слота, карантин Q2 по медиане, деление пополам Q3 |
| `urltest_rr_firstbyte_lx.go` | Обёртка соединения до первого байта: исход `ANSWER`, неявный отказ, `CANCEL`; после первого байта и первой записи клиента уходит с пути (splice и векторные пути сохраняются) |
| `urltest_rr_udp_lx.go` | UDP: повтор открытия, обёртка потока, ожидание «отправил, ответа нет» как повод для пробы жизни |
| `urltest_rr_fill_lx.go` | SPEC 019: узел, ответивший на пробу во время прогона группы, встаёт в слот узла без history |
| `urltest_rr_status_lx.go` | Снимок для `GetPool`: карантин и счётчики слотов, сдвинутые и помеченные ключи, заморозка |
| `site_on_open_lx.go` | Ходит ли лист узла до сайта сам (`direct`, endpoint, MASQUE): от этого зависит, явный ли отказ |

## Вне protocol/group

| Файл | Зачем |
|------|-------|
| `protocol/socks/destination_refused_lx.go`, `protocol/http/destination_refused_lx.go` | Отказ назначения кодом SOCKS и статусом CONNECT отличается от отказа узла |
| `adapter/dials_destination_lx.go`, `protocol/masque/dials_destination_lx.go` | MASQUE объявляет, что открывает соединения до сайта сам |
| `adapter/actual_outbound_lx.go`, `route/group_resolve_lx.go`, `common/trafficcontrol/tracker_lx.go`, `daemon/started_service_actual_lx.go` | Ячейка фактического узла соединения: список соединений и демон показывают узел после выбора и повтора; демон повторяет «новое соединение» с приращением трафика тика |
| `route/conn_halfclose_lx.go` | Сообщает обёртке конец потока клиента, когда она уже ушла с пути записи |
| `common/urltest/measure_lx.go`, `common/urltest/history_lx.go` | Метка замера задержки (мимо самовосстановления); стирание history, только если запись не новее начала пробы |
| `daemon/started_service_command_lx.go`, `experimental/libbox/command_client_command_lx.go`, `cmd/sing-box/cmd_api_group_pool_lx.go` | `GetPool`, `GetPoolState`, `sing-box api group pool` |

## Вставки в апстримные файлы

Только блоки `// lx: SPEC 138`: `protocol/group/urltest.go` — однострочные вызовы
`urltest_hooks_lx.go` и `urltest_rr_*_lx.go` в `Start`, `DialContext`, `ListenPacket`,
`InterfaceUpdated` (выбор с ключом, отслеживаемое открытие, наблюдение первого байта, повтор);
состояние группы — во встроенных полях `lxURLTest` / `lxURLTestGroup`; `adapter/inbound.go` (поле ячейки),
`route/route.go` (две строки), `route/conn.go` (одна строка), `common/trafficcontrol/tracker.go`,
`common/urltest/urltest.go` (одна строка), `daemon/started_service.go`,
`daemon/started_service.proto`, `protocol/socks/outbound.go`.
