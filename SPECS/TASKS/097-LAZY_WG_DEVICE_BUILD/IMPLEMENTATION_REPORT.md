# IMPLEMENTATION_REPORT: 097 — LAZY_WG_DEVICE_BUILD

Ветка `lx`, не запушена. Не выпущена: войдёт в v1.14.1-lx.13 вместе с 098.

## Коммиты

| Коммит | Что |
|---|---|
| `a40a880b8` | `transport/wireguard/endpoint_lazy_lx.go`: `newLazyEndpoint`, адреса порта из опций, заглушка-устройство `unbuiltDevice` в обёртке return-path, `newDeviceFn` + `SetNewDeviceFnForTest` |
| `d479a9739` | апстримные `transport/wireguard/endpoint.go` (ветка `LazyDevice` в конструкторе, `Rebuild` через `newDeviceFn`) и `endpoint_options.go` (поле `LazyDevice`) |
| `55f7bcdda` | `route/router.go`: поле `reachRefs` в блоке idle-suspend; тип `selectionRefs` |
| `53f7da2e6` | `walkReachable` считает рёбра, `SelectionRefs`; `adapter`: `SelectionRefs` в `ReachabilityReporter`, `IdleState`/`IdleStateReporter`, `LXBuildBudgetSlot` |
| `a160eb148` | `box_lx.go`: свежий слот бюджета на каждый box |
| `311084b52` | `protocol/wireguard`: `BuildBudget`, ленивый старт, `resumeOnDial(ctx)`, `TeardownForBudget`, `IdleState`, счёт дайлов в процессе |
| `d406f76ae` | `started_service.proto`: `GroupItem.endpointState = 5`, `idleSinceSeconds = 6`; регенерация `started_service.pb.go` |
| `f1f049e38` | `GetOutbounds` заполняет поля |
| `f3113d6bd` | libbox `OutboundGroupItem`: `EndpointState`, `IdleSinceSeconds` |
| `3dfee3bde` | тесты |
| `8d14ff398` | тик и `RebindStale` не ждут сборку, стоящую в очереди бюджета |
| (этот) | доки, FEATURE 008, changelog, статус, отчёт |

## Файлы

- **Новые:** `transport/wireguard/endpoint_lazy_lx.go`, `protocol/wireguard/build_budget_lx.go`,
  `protocol/wireguard/endpoint_lazy_lx.go`; тесты `transport/wireguard/endpoint_lazy_lx_test.go`,
  `protocol/wireguard/{endpoint_lazy,build_budget}_lx_test.go`, `route/reachability_refs_lx_test.go`,
  `box_lx_test.go`, `daemon/started_service_outbounds_lx_test.go`,
  `experimental/libbox/command_types_endpoint_state_lx_test.go`.
- **`// lx:` правки апстрима:** `transport/wireguard/endpoint.go` (3 строки кода под `lx:begin lazy-build` +
  одна строка в `Rebuild`), `transport/wireguard/endpoint_options.go` (поле под маркером),
  `protocol/wireguard/endpoint.go` (поля в блоке idle-suspend; `initLazyBuild` и `LazyDevice` в конструкторе;
  ветка `startLazy` в `Start` под `lx:begin lazy-build`; `budgetRegister` в PostStart; `defer
  w.leaveDial(w.enterDial())` в `DialContext`, `ListenPacketWithDestination`, `WritePackets`; `resumeOnDial(ctx)`
  в пяти точках; `Notify`/`Release` в `Close`), `route/router.go` (1 поле), `daemon/started_service.proto`
  (2 поля под `lx_command`) + регенерированный `.pb.go`, `experimental/libbox/command_types.go` (2 поля и 2
  строки конвертера под `lx_command`).
- **Правки наших файлов:** `adapter/idle_suspend_lx.go`, `route/reachability_lx.go`,
  `route/reachability_common_lx.go`, `route/idle_suspend_stub_lx.go`, `box_lx.go`,
  `daemon/started_service_command_lx.go`, `protocol/wireguard/endpoint_rebind_lx.go`.
- **deps / go.mod / build-теги / сабмодули:** без изменений. `daemon/started_service.go` не тронут.

## Отступления от PLAN

1. **Бюджет в контексте — через слот, не `*wireguard.BuildBudget`.** `box_lx.go` не может импортировать
   `protocol/wireguard`: пакет подтягивается только под `with_wireguard` (`include/wireguard.go`), а импорт из
   `box` затащил бы wireguard-go и gVisor в сборку без тегов. `box_lx.go` регистрирует пустой
   `adapter.LXBuildBudgetSlot` на каждый box (свежий и на общем реестре демона — как `LXResolved` в 098), первый
   WG-endpoint с `lazy_build` или `build_max > 0` создаёт в нём бюджет (`sync.Once`), остальные и клоны цепочек
   берут тот же.
2. **Дайлы в процессе.** В PLAN страж «живое не рвём» — только TCP-потоки и счётчики. Этого мало: бюджет
   выбирает жертву в окне между `resumeOnDial` дайла и установлением TCP (пробы urltest попадают именно в него), и
   разборка роняла бы пробу; кроме того, чтение `tunDevice` в транспортном `DialContext` гонялось бы с
   `Teardown`. Поэтому три входа дайла считают себя (`dialsInFlight`, по строке `defer` на вход), а
   `TeardownForBudget` у бодрствующей жертвы сначала публикует `idleAsleep=true`, потом перепроверяет счётчик
   (схема Деккера): дайл, прочитавший `idleAsleep=false`, уже виден в счётчике; прочитавший `true` уходит в
   медленный путь и ждёт `resumeMu`. Три строки в апстримном файле сверх плана.
3. **Повторная выборка трафика.** Первая выборка счётчиков у свежесобранного узла включает трафик его пробы
   (≥ 4096 Б), и он выглядел бы живым. Кандидат, отвергнутый только по трафику (`evictionTraffic`), проверяется
   ещё раз через 250 мс, и только потом `build` уходит сверх потолка. В режиме `wait` ожидающий ещё и опрашивает
   раз в секунду: конец потока ничего не сигналит.
4. **Порядок жертв:** первым ключом — устройства, собранные сверх потолка (PLAN, раздел `build`), затем ручные →
   авто → давность. Реестр ведёт `building` отдельно от `built`: сборка в процессе занимает место, иначе два
   просителя разом превысили бы потолок.
5. **Текст ошибки/лога.** Ошибка `Acquire` — `build budget exhausted (N built, none idle)`, лог —
   `lx idle: build <tag>: <err>` (в PLAN — `lx idle: build budget: <err>`, с дублем префикса). WARN —
   `lx idle: build over budget N+1/N <tag>`.
6. **Состояние `down`** добавлено к пяти из SPEC: не стартовал, остановлен или закрыт.
7. **`ChainInfo` не расширялся**, поток `SubscribeOutbounds` полей не заполняет (апстримный builder, PLAN
   запрещает правку). SPEC §2.3 приведена к факту.
8. **Тесты без `with_gvisor`:** устройства в тестах endpoint'а — подделки через `SetNewDeviceFnForTest`, поверх них
   работает настоящий wireguard-go (в Down, без сети). Стенд `lx-test/lazywg` (PLAN тест 7) не заведён: без
   живых серверов он проверил бы то же, что юнит `TestLazyBudget_probesKeepAtMostN` (6 ленивых узлов, потолок 2,
   параллельные дайлы — живых устройств никогда больше двух, собраны все).
9. Сверх плана: `SuspendIfIdle`, `TeardownIfSlept` и `RebindStale` выходят до `resumeMu`, если endpoint
   собирается (`building`) или не стартовал: сборка может ждать бюджет до 15 с под `resumeMu`, и тик по всем
   узлам и пробуждающий вызов LxBox стояли бы за ней.

## Порядок блокировок

`requester.resumeMu → budget.mu (коротко) → victim.resumeMu → budget.mu (Release)`. `budget.mu` не держится при
вызове в endpoint: ранжирование (`SelectionRefs` → кеш роутера, может пройти обход с замками групп) и
`TeardownForBudget` идут без него. Проситель — всегда разобранный endpoint, жертва — всегда из `built`, поэтому
чужой `resumeMu` берёт только тот, кто сам не может оказаться жертвой. Жертва помечается `evicting` под
`budget.mu`, второй проситель её пропускает. Закрытие просителя (`closing` + `Notify`) будит ожидание сразу.
Проверено тестом двух одновременных просителей с одной жертвой (50 раундов, `-race`).

## Тесты и проверки (go1.26.8, `GOTOOLCHAIN=go1.26.8`)

| Команда | Результат |
|---|---|
| `go build ./...` | ok |
| `go build -tags with_lx_idle_suspend ./...` | ok |
| `go build -ldflags "-checklinkname=0" -tags "$LX_TAGS,with_lx_idle_suspend" ./...` | ok |
| `make -f Makefile.lx lx-check` (lx-build + 9 конфигов) | ok |
| `go test -ldflags "-checklinkname=0" ./transport/wireguard/ ./protocol/wireguard/ ./route/ ./adapter/ ./daemon/ ./experimental/libbox/ ./option/ .` | ok |
| то же с `-tags with_lx_idle_suspend` | ok |
| `go test … -tags with_lx_command ./daemon/ ./experimental/libbox/` | ok |
| `go test -p 1 … -tags "$LX_TAGS,with_lx_idle_suspend"` по тем же пакетам и `./lx-test/...` | ok |
| `go test -p 1 … -tags "$LX_TAGS"` по тем же пакетам и `./lx-test/...` | ok |
| `go test -race -count=3..5` `./protocol/wireguard/ ./transport/wireguard/ ./route/` | ok |
| `go vet -unsafeptr=false -tags "$LX_TAGS,with_lx_idle_suspend"` и без тегов по затронутым пакетам | ok |
| `gofmt -l` по всем тронутым `.go` | чисто |

Полный набор тегов прогонялся с `-p 1`: на машине кончилось место на диске (`no space left on device` при
параллельной линковке), это не дефект кода.

## Не сделано / вне скоупа

- П. 9 TASKS: замер на эмуляторе (конфиг LxBox 519, `build_max: 3`) и прогон раннбука §1.4 — за владельцем.
- LxBox: показ `endpointState` («узел не поднят») и выставление `build_max` для probe-сессий — задача приложения.
- Бюджет-осведомлённая очередь проб urltest (PLAN, «Риски») — отдельная задача по итогам замера.
