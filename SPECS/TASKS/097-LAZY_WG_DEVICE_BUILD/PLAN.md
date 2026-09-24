# PLAN: 097 — LAZY_WG_DEVICE_BUILD

Опирается на блок `lx.wg` из [098](../098-LX_ROOT_CONFIG_BLOCK/PLAN.md): ключи
`lazy_build`, `build_max`, `build_overflow` уже парсятся и валидируются, значения
доступны через `service.FromContext[*option.LXResolved](ctx)`.

## Файлы

| Файл | Тег | Содержимое |
|------|-----|-----------|
| `transport/wireguard/endpoint.go` | upstream, lx-строки | `EndpointOptions.LazyDevice` (lx-поле): `NewEndpoint` не зовёт `NewDevice`, хранит рецепт (`deviceOptions`), `tunDevice == nil` с рождения (уже означает `TornDown()`); `inet4/6Address` — из адресов опций, не из устройства; `returnDevice` — обёртка с nil-устройством, принимающая `AttachReturn` до первой сборки (как `Rebuild` переносит `state`) |
| `transport/wireguard/endpoint_lazy_lx.go` | — | вынос lx-логики ленивого конструктора, если строк в апстримном файле больше трёх |
| `protocol/wireguard/endpoint.go` | upstream, lx-блок idle-suspend | `lazy bool`, `neverBuilt atomic.Bool`, `building atomic.Bool`; `Start`: под `lazy` стадия Start только резолвит detour (SPEC 029), PostStart не собирает устройство, а ставит `torndown=true, idleAsleep=true, started=false, neverBuilt=true`, `sleepSince=now`, `stampActivity`; `resumeOnDial(ctx)` — путь rebuild берёт разрешение у бюджета до `Rebuild()`; `IdleState()` для наблюдаемости; `TeardownForBudget()` — принудительная разборка жертвы |
| `protocol/wireguard/build_budget_lx.go` | — | `BuildBudget` (реестр собранных endpoint'ов, `max`, `overflow`, `cond`); `Acquire(ctx, self) error`: под `max` — есть место → ок; нет → жертва (см. решения) → её `TeardownForBudget()` → ок; жертвы нет → `wait`: ждать сигнала до дедлайна ctx (или `buildWaitMax`), `build`: WARN и собрать сверх; `Release(ep)` при teardown/close; `Built()`/`Snapshot()` для наблюдаемости |
| `protocol/wireguard/build_budget_lx_test.go`, `endpoint_lazy_lx_test.go` | — | см. тесты |
| `box_lx.go` (из 098) | — | регистрирует `*wireguard.BuildBudget` в контексте, когда `build_max > 0` или `lazy_build` (бюджет с `max=0` = без потолка, но реестр ведётся — нужен для наблюдаемости) |
| `adapter/idle_suspend_lx.go` | — | `ReachabilityReporter.SelectionRefs(tag) (manual, auto int)`; `IdleStateReporter { IdleState() IdleState }`, `IdleState{State string; IdleSince, SleepSince time.Duration}` со строками `never_built` / `building` / `up` / `asleep` / `torn_down` |
| `daemon/started_service.proto` → `*.pb.go` | — | `GroupItem`: `string endpointState = 5; int64 idleSinceSeconds = 6;` (пусто/0 для не-endpoint'ов); регенерация `make -f Makefile.lx lx-proto` (см. память `lx-proto-regen-network-and-noise`) |
| `daemon/started_service_command_lx.go` | `with_lx_command` | `GetOutbounds`/`SubscribeOutbounds`-builder заполняет поля через `IdleStateReporter` |
| `experimental/libbox/command_client_command_lx.go` | — | `OutboundGroupItem` получает `EndpointState`, `IdleSinceSeconds` (gomobile: строка и int64 в объекте, не голый string-возврат — память `gomobile-string-return-packed-frame-kill`) |
| `route/reachability_lx.go`, `route/idle_suspend_stub_lx.go` | `with_lx_idle_suspend` / stub | `walkReachable` считает входящие рёбра двух классов (`map[tag]refs{manual, auto}`: selector/корень — manual; urltest/detour/chain/DNS-detour — auto) рядом с достижимостью, кеш тот же; `SelectionRefs`; stub отдаёт (0, 0) (уже отвергает `lazy_build`/`build_max`, 098) |
| `docs-lx/lx-energy.md`, `.ru.md`, `lx-config.md`, `.ru.md`, FEATURE 008, changelog | — | ленивая сборка, бюджет, состояния, «узел не поднят ≠ мёртв» |

## Ключевые решения

- **Холодный старт = уровень 3 SPEC 020.** Ничего нового в машине состояний: ленивый
  endpoint рождается в `torndown && idleAsleep`, и первый дайл идёт существующим
  путём `Rebuild → Start(false) → Start(true)`. Единственное отличие от «уснул и
  разобран» — метка `neverBuilt` для наблюдаемости и отсутствие `NewDevice` в
  конструкторе (иначе netstack ≈5.9 МБ на узел всё равно выделяется на старте).
- **Бюджет считает устройства, а не сон.** «Собран» = `tunDevice != nil` (уровни
  0–2 SPEC 020). Пробуждение уровня 1–2 (`Resume`) бюджета не спрашивает — узел уже
  в счёте. Разрешение нужно только на `Rebuild` (уровень 3 → 0).
- **Жертва — по ссылкам (решение владельца 2026-09-24: «по ссылкам, не по трафику»).**
  Кандидаты — собранные endpoint'ы без живых соединений (`ActiveTCPFlows() == 0` и
  счётчики `TransferTotals()` не сдвинулись больше `idleSuspendTransferThreshold`
  с прошлой выборки бюджета — это страж «живое не рвём», не ранг), кроме просителя
  и тех, кто в `building`/`evicting`. Спящие и бодрствующие кандидаты в одной
  очереди; флаг сна ключом не является. Жертва — кандидат с наименьшим приоритетом:
  1. **число ручных ссылок** — сколько рёбер ведёт в узел из ручного selector'а
     (его текущий `Now()`), плюс корни обхода: `final` и прямая цель правила
     маршрутизации (решение владельца 2026-09-24: «двухпроходная сортировка»);
  2. **число автоматических ссылок** — urltest (пул/выбранный), detour другого
     узла, `detour` DNS-сервера, звено chain — всё по +1;
  3. **давность последнего обращения** — `IdleSince`, время с последнего дайла
     через узел (подкручивается тиком при движении трафика); самый давний первым.
     Не «давность засыпания»: у бодрствующих кандидатов её нет, а очередь общая.
  Лексикографически, без суммирования: любое число авто-ссылок не перевесит одну
  ручную. Вес по ребру, ведущему непосредственно в узел, без наследования по дереву
  (selector → urltest → X даёт X одну авто-ссылку). Считает тот же обход, что
  строит достижимое множество (`walkReachable`): входящие рёбра считаются до
  проверки visited, раскрытие узла — один раз; кеш тот же (`reachCache`), бюджету
  отдаётся через `adapter.ReachabilityReporter.SelectionRefs(tag) (manual, auto int)`.
  Узлы со ссылками разбираются только когда кандидатов с (0, 0) не осталось
  (строгой неприкосновенности нет — иначе при `build_max` меньше числа выбранных
  узлов проситель ждал бы вечно).
  Трафик в ранжировании не участвует. Без гистерезиса по времени (решение
  владельца): свежесобранный узел без ссылок — законная жертва, пробы urltest
  ждать порога сна не могут (дедлайн `C.TCPTimeout`).
- **Порядок блокировок.** `self.resumeMu → budget.mu (коротко) → victim.resumeMu`.
  Цикл невозможен: жертвой не бывает разобранный endpoint, а чужой `resumeMu` берёт
  только проситель на пути rebuild (то есть разобранный). Жертва под своим
  `resumeMu` в `SuspendIfIdle`/`TeardownIfSlept`/`Resume` чужих замков не берёт.
  Бюджет помечает жертву `evicting` под `budget.mu`, чтобы два просителя не выбрали
  одну; после `TeardownForBudget` метка снимается, `Release` будит ожидающих.
- **`wait`** ждёт на `sync.Cond`/канале до дедлайна ctx дайла; L3-forward путь
  (`resumeOnDial` без ctx) получает `context.WithTimeout(w.ctx, buildWaitMax = C.TCPTimeout)`.
  Просыпающие события: `Release` (teardown/close), переход endpoint'а в сон
  (`SuspendIfIdle`), завершение чужого `building`. Таймаут → ошибка дайла
  `lx idle: build budget exhausted (N built, none idle)`; узел для urltest —
  недоступен на этой пробе, не «мёртв навсегда».
- **`build`** — WARN `lx idle: build over budget N+1/N` и сборка; сверхбюджетные
  endpoint'ы разбираются первыми при следующем `Acquire`.
- **`build_max` без `lazy_build`** легален: устройства собираются на старте (сегодняшнее
  поведение), а потолок начинает действовать с первой пересборки. Документировать.
- **Наблюдаемость — состояние, не ошибка.** `IdleState()` читает атомики без
  `resumeMu` (снимок может отставать на переход — допустимо). `building` — пока
  `Rebuild+Start` идут под `resumeMu`.
- **Chain-клоны (SPEC 073)** — те же `*wireguard.Endpoint`, создаются с тем же ctx →
  регистрируются в бюджете сами; тег для наблюдаемости — тег клона.
- **Без тега `with_lx_idle_suspend`** ключи отвергнуты stub'ом (098) — код ленивой
  сборки в `protocol/wireguard` компилируется всегда, но недостижим.

## Зона касания upstream

`transport/wireguard/endpoint.go` (конструктор: ветка `LazyDevice`, ≤ 6 строк под
маркером; остальное — lx-файл), `protocol/wireguard/endpoint.go` (внутри уже
существующего `lx:begin idle-suspend` блока + вызовы `resumeOnDial(ctx)`),
`daemon/started_service.proto` (два поля), `daemon/started_service.go` — не трогать
(builder `SubscribeOutbounds` апстримный: поля состояния заполняет только наш
`GetOutbounds`; если стрим тоже нужен LxBox — обёрткой в lx-файле, не правкой).

## Тесты

1. Ленивый старт: `NewDevice`-счётчик (через подменяемую `newDeviceFn` в lx-файле
   транспорта) = 0 после `Start` обеих стадий; `IdleState() == never_built`.
2. Первый дайл: ровно одна сборка; 10 параллельных дайлов — одна сборка; после —
   `up`; `neverBuilt=false`.
3. `Close` до первого дайла: без паники, без утечки горутин (`goleak`-подобная
   проверка, как в `endpoint_start_close_race_lx_test.go`).
4. Бюджет N=2, три endpoint'а: третья сборка разбирает кандидата с наименьшим
   приоритетом — таблица: (manual, auto) = (1,0) / (0,2) / (0,1) / (0,0); одна
   ручная ссылка сильнее любого числа авто; без наследования через вложенную
   группу; давность при равных; спящий со ссылкой переживает бодрствующий без
   неё; все со ссылками — разбирается с наименьшей парой; при живых TCP-потоках у всех — `wait` истекает по ctx с
   ошибкой, `build` собирает с WARN.
5. Порядок блокировок: два просителя одновременно + одна жертва — без дедлока
   (тест с таймаутом).
6. Наблюдаемость: `GetOutbounds` отдаёт `endpointState` для WG и пусто для
   прочих; переходы `never_built → building → up → asleep → torn_down`.
7. Стенд `lx-test` с 11 endpoint'ами и `build_max: 3`: `sing-box run` под
   `with_lx_idle_suspend`, urltest-проба всех — не больше 3 собранных одновременно
   (по логу `rebuild ... by=dial` / `teardown ... by=budget`).

## Риски

- Пробы urltest с `wait`: K узлов при бюджете N идут волнами; если волна не
  укладывается в `C.TCPTimeout`, хвост группы помечается недоступным на этой
  пробе. Замер на эмуляторе (§4 п. 2 SPEC) решает, нужна ли бюджет-осведомлённая
  очередь проб — отдельная задача, не этот SPEC.
- Разборка узла со ссылкой возможна только когда кандидатов без ссылок нет:
  следующий дайл в него платит сборку. При `build_max` ниже числа
  реально используемых узлов — трэш сборок; дока предупреждает, LxBox ставит
  `build_max` только на probe-сессиях.
- UDP/QUIC-поток через бодрствующую жертву между выборками счётчиков: окно —
  один `Acquire`; порог 4096 Б за окно. Принято.
- `returnDeviceWrapper` с nil-устройством до первой сборки: sing-tun может
  дёрнуть его до `AttachReturn` — проверить nil-безопасность всех методов.
- Регенерация pb.go: протоген правит все файлы (память), сверять дифф только по
  двум новым полям.
