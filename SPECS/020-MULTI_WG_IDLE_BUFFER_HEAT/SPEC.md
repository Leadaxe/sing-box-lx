# SPEC 020 — Idle-suspend простаивающих WireGuard/AmneziaWG эндпоинтов

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) — проверено вживую (desktop + Android device) и юнит-тестами |
| Тег сборки | `with_lx_idle_suspend` (mobile-only, в AAR; НЕ в desktop/CLI) |
| Фича-флаг | `route.lx_idle_suspend: "<duration>"` (отсутствует/`0` = выключена) |

Фича `lx_idle_suspend` выборочно **гасит** (`device.Down()`) WG/AWG-эндпоинт, который одновременно **недостижим** из активного дерева маршрутизации и **простаивает** дольше порога. Пробуждение — лениво, на следующем дайле через эндпоинт (`device.Up()`).

Первопричина нагрева и heap A/B — в [RESEARCH.md](RESEARCH.md). Хронология (найденный баг, отвергнутые альтернативы, эволюция путей) — в [HISTORY.md](HISTORY.md).

---

## Что это

На Android ядро греется и пинит CPU: **каждый** живой WG/AWG-эндпоинт работает 24/7 независимо от трафика. При N эндпоинтах все N держат:
- **recv-воркеры** со своими `bufsArrs` (`make([]*[65535]byte, BatchSize)`) — при `BatchSize=128` это ~8 МБ/воркер, 2 воркера/устройство. **Главный держатель GC-scan нагрева** (доказано heap A/B, [RESEARCH.md](RESEARCH.md)).
- **per-peer таймеры** (keepalive, handshake-retransmit) + для AWG junk-machinery — будят радио, жгут CPU/батарею на простое.
- **gvisor `stack.Stack`** netstack на каждый эндпоинт.

До фичи не было понятия «этот эндпоинт сейчас недостижим из активного маршрута» — только глобальная пауза (экран off, гасит ВСЕ), полный `Close()` и AWG-detour-guard. Выборочного засыпания простаивающих нод не было.

## Архитектура

Три части: (1) **модель достижимости** — кто сейчас достижим; (2) **кэш + инвалидация** — считаем редко; (3) **сторона эндпоинта** — учёт простоя, Down/Up.

### Слой 1 — модель достижимости (reachability)

Эндпоинт **достижим**, если трафик может прямо сейчас на него попасть. Динамический обход активного дерева (НЕ статический `ConsumersOf`/`dependByTag`, который перечисляет ВСЕ члены селектора — нам нужен *активный* выбор).

**Сиды** (точки входа):
- **final**: `outboundManager.Default()`.
- **Правила**: каждый outbound-тег из действия правила (`route`/`bypass`) в `router.Rules()`. Действия reject/sniff/dns/resolve/hijack-dns сида не дают.

**Обход вниз** от каждого сида (visited-set = дедуп + защита от циклов):

| Тип узла | Достижимые дети |
|---|---|
| selector | **только** `Now()` (текущий выбор), НЕ `All()` |
| urltest round_robin | **весь** текущий пул — `ActiveTags()` |
| urltest legacy (least_test) | `Now()` |
| обычный outbound | его `Dependencies()` (detour-цепочка) |

Обход транзитивный (selector→urltest→эндпоинты и т.д.). Реализация: `reachableSet`/`walkReachable` в [`route/reachability_lx.go`](../../route/reachability_lx.go) — чистые функции, развязанные с `*Router` через `resolve`-замыкание (юнит-тестируемы со стабами).

**Семантика urltest — «дайл будит, idle-тик усыпляет».** Health-check probe идёт через `DialContext` узла — то есть обычный дайл. Тот же ленивый `Up()`-на-дайле, что обслуживает трафик, будит спящий узел под probe. Узел в пуле probe'ится каждый `interval` → не простаивает → не усыпляется; выбывший из пула перестаёт probe'иться → простоит > порога → усыпляется тиком. Probe сам узел обратно не гасит — это работа тика по таймауту.

### Слой 2 — кэш достижимости и инвалидация (event-driven)

Полный обход на каждый тик — расточительство (бьёт по цели экономии). Набор достижимых кэшируется, пересчитывается **только когда активное дерево изменилось**:
- `r.reachDirty atomic.Bool` — «грязно» (стартует `true`).
- `r.reachCache map[string]bool` под `r.reachMu sync.RWMutex`.
- `InvalidateReachability()` (реализует `adapter.ReachabilityInvalidator`) ставит флаг. Обход НЕ под локом кэша (зовёт группы с их локами → риск лок-ордер дедлока): считаем вне лока, публикуем под локом.

Точки инвалидации (через `service.FromContext[ReachabilityInvalidator]`, чтобы `protocol/group` не импортировал `route`):

| Точка | Файл |
|---|---|
| `Selector.SelectOutbound` | `protocol/group/selector.go` |
| urltest legacy auto-switch | `protocol/group/urltest.go` |
| перестроение пула `balancer.onChange` | `protocol/group/urltest_balance_lx.go` |
| перезагрузка конфига/роутера | пересоздание Router |

Между событиями тик читает кэш и делает одно сравнение простоя на эндпоинт — без обхода.

### Слой 3 — сторона эндпоинта (учёт простоя, Down/Up)

Состояние в [`protocol/wireguard/endpoint.go`](../../protocol/wireguard/endpoint.go) (`lx:begin/lx:end idle-suspend`):
- `lastActivity atomic.Int64` (unix-nanos) — штампуется в PostStart (базовая отметка) и на каждом входе в дайл.
- `IdleSince() time.Duration`, `idleAsleep atomic.Bool` (true пока усыплён по простою — **отличается** от `started`: guard-suspend ставит `started=false` БЕЗ `idleAsleep`), `resumeMu sync.Mutex` (сериализует «усыпить» тика против «разбудить» дайла).

**`SuspendIfIdle(reachable, threshold)`** — решение тика:
```
если reachable || IdleSince() < threshold: выход
если !started.Load(): выход              // уже down (guard / awg-chain / закрыт)
если idleAsleep.CAS(false→true):
    started.Store(false)
    endpoint.Suspend()                   // device.Down()
    log INFO "lx idle: suspend <tag>"    // edge-triggered
```

**`resumeOnDial()`** — в начале каждого дайла:
```
stampActivity()                          // всегда, закрывает гонку с тиком
если !idleAsleep: вернуть started        // быстрый путь
под resumeMu:
    endpoint.Resume()                    // device.Up()
    started.Store(true); idleAsleep.Store(false)
    log INFO "lx idle: wake <tag> by=dial"; вернуть true
```

Логирование **edge-triggered** — строка только внутри успешного CAS. Пара suspend↔wake в логе = сигнал флапа.

## Механизм Down/Up

`device.Down()→Up()` — **полный реконнект**, не переключение таймера (source-verified против сабмодуля).

`Down()` (`downLocked`): зануляет крипто-ключи + handshake (`peer.Stop()`→`ZeroAndFlushAll()`), закрывает UDP-сокет (`BindClose`+барьер `stopping.Wait()` до выхода recv-воркеров), сносит `2·N+1` горутин. recv-воркер выходит только по `net.ErrClosed` → «recv-воркеры → 0» = **детерминированное доказательство**, что сокет закрыт и `bufsArrs` освобождены.

`Up()` синхронный, не-сетевой (поднимает bind + *инициирует* handshake). Цену платит **первый пакет** — ждёт ~1 RTT, пока WG доделает handshake (плюс на AWG вся I1-обфускация + junk). Идентично холодному дайлу.

**Что переживает цикл** (это `changeState`, не `Close`): объекты Device/Peer, ClientBind, буферные пулы, очереди шифрования + воркеры, порт. **`Down()` НЕ освобождает gvisor netstack** (~5.9 МБ/устройство) — Tier B, не реализовано (см. § «Отложено»).

Компромисс: спящая нода держит **ноль** recv-памяти ценой одного handshake на пробуждении. На близком сервере это +14–21 мс (≈ 1 handshake, ≈ 3 RTT); на межконтинентальном (RTT ~150 мс) — разовый ~+450 мс на первый пакет (только латентность, throughput не страдает). Keys-safe путь без handshake (`BindUpdate`) отложен.

## Совместимость с wireguard-go v0.0.5

Idle-suspend опирается на стабильный device-API вендоренной базы (см. [SPEC 003](../003-AWG2_CLIENT_ENDPOINT/SPEC.md)). На текущей базе v0.0.5:
- **`Down()`/`Up()`/`BindUpdate()` присутствуют** ([device.go:237/241/507](../../submodules/wireguard-go/device/device.go)) — механизм работает без изменений.
- **`bufsArrs`** по-прежнему recv-worker аллокация из фиксированного `messageBuffers`-пула ([receive.go:89](../../submodules/wireguard-go/device/receive.go)); `Down()` освобождает её выходом воркеров. Held-держатель, ради которого фича сделана, не сдвинулся.
- **`SetSinglePeerMode`** (Darwin-only оптимизация из v0.0.5, ломает роуминг пира) sing-box **не вызывает** — не пересекается с Suspend/Resume.

## Конфигурация

```jsonc
"route": { "lx_idle_suspend": "30s" }   // порог простоя; "0"/отсутствует = выключено
```

- Поле `option.RouteOptions.LXIdleSuspend badoption.Duration` в [`option/route.go`](../../option/route.go) (`lx:begin/lx:end`).
- **Build-тег `with_lx_idle_suspend` (mobile-only).** Тик компилируется только с тегом; добавлен в mobile AAR ([`cmd/internal/build_libbox`](../../cmd/internal/build_libbox/main.go)), НЕ в desktop `LX_TAGS` (на десктопе `BatchSize` мал, экономить нечего). Бинарь без тега, получивший конфиг с `lx_idle_suspend`, **падает при старте** с явной ошибкой (`rebuild with -tags with_lx_idle_suspend`) — никакого молчаливого no-op. Гейт — `startIdleSuspend` ([`route/reachability_lx.go`](../../route/reachability_lx.go) / stub [`route/idle_suspend_stub_lx.go`](../../route/idle_suspend_stub_lx.go)). Hot-path дайла (`resumeOnDial`) НЕ расщепляется (без тега `idleAsleep` никогда не true).
- **По умолчанию (отсутствует/`0`) — выключено** (kill-switch): тик не запускается, нулевой оверхед. Ортогонально build-тегу.
- Период тика: `max(порог / idleTickDivisor, idleTickFloor)` = `max(XX/2, 5s)` (константы в [`route/reachability_lx.go`](../../route/reachability_lx.go)).

| `lx_idle_suspend` | период тика |
|---|---|
| `30s` | 15s |
| `60s` | 30s |
| `8s` | 5s (floor) |
| `0` | тик не запущен |

## Инвариант: взаимодействие с AWG-detour-guard

AWG-detour-guard ([awg-detour-guard-must-be-at-start]) гасит `device.Down()` AWG-эндпоинт, чья detour-цепочка достигает WG-узла (AWG-over-WG вешает ядро Android). Он ставит `started=false` БЕЗ `idleAsleep`. idle-suspend это уважает **без отдельного флага**:
- `SuspendIfIdle` рано выходит на `!started` → guard-усыплённый эндпоинт тик не трогает.
- `resumeOnDial` идёт ПОСЛЕ существующего `!started`-гейта дайла (который уже обрывает дайл) → guard-усыплённый эндпоинт idle-логикой не воскрешается. AWG-over-WG hang остаётся предотвращённым.

Проверено юнит-тестами `TestSuspendIfIdle_guardSuspendedNotTouched`, `TestResumeOnDial_guardSuspendedNotWoken` и вживую.

## Файлы

Новые (lx-owned):
- [`route/reachability_lx.go`](../../route/reachability_lx.go) — `//go:build with_lx_idle_suspend` — walk, кэш, idle-тик, `suspendIdleEndpoints`, `startIdleSuspend`/`stopIdleSuspend`.
- [`route/idle_suspend_stub_lx.go`](../../route/idle_suspend_stub_lx.go) — `//go:build !with_lx_idle_suspend` — заглушки (ошибка если опция задана).
- [`route/reachability_common_lx.go`](../../route/reachability_common_lx.go) — без тега: только `InvalidateReachability`.
- [`protocol/group/reachability_lx.go`](../../protocol/group/reachability_lx.go) — хук `invalidateReachability(ctx)`.

Изменённые:
- [`option/route.go`](../../option/route.go) — `LXIdleSuspend`.
- [`route/router.go`](../../route/router.go) — состояние тика, `endpoint adapter.EndpointManager` (из ctx), PostStart→`startIdleSuspend()`, стоп в Close.
- [`cmd/internal/build_libbox/main.go`](../../cmd/internal/build_libbox/main.go) — тег в `sharedTags` (mobile).
- [`adapter/outbound.go`](../../adapter/outbound.go) — интерфейсы `IdleSuspendable`, `ReachabilityInvalidator`.
- [`protocol/wireguard/endpoint.go`](../../protocol/wireguard/endpoint.go) — состояние + `SuspendIfIdle`/`resumeOnDial`, вставка `resumeOnDial` в начало гейтов дайла (без тега — дёшево).
- `protocol/group/selector.go`, `urltest.go`, `urltest_balance_lx.go` — точки инвалидации.

## Проверено

- **Юнит-тесты**: обход (final/detour/selector-Now/urltest-пул/циклы/дедуп/вложенные), кэш (recompute-only-when-dirty, lock-free invalidate), интеграционный шов тика (перебор обоих менеджеров), сторона эндпоинта (idle/threshold/CAS-идемпотентность/guard-инвариант/дайл-против-тика гонка). Adversarially: слом walk (`Now()`→`All()`) роняет дедуп/dormant; слом тика (только `Outbounds()`) роняет both-managers.
- **Live desktop** (реальные ноды): suspend недостижимых, wake by dial/probe, switch селектора (старый уснул, новый проснулся), матрица достижимости на production-конфиге, guard-инвариант, no-flap, kill-switch. Ресурсы A/B (8 нод усыплены): recv-воркеры 16→0, RSS −31%.
- **Android device** (CPH2411, Android 15): heap A/B на целевой платформе — `bufsArrs` (`PopulatePools.func3`) **223.93→89.89 МБ (−60%, −134 МБ)**, recv-воркеры 18→2. ~8.4 МБ/воркер, совпадает с оценкой `BatchSize=128` из RESEARCH.md. Артефакты — [ANDROID_RESEARCH](ANDROID_RESEARCH/README.md).

Детали прогонов — [TEST_PLAN](TEST_PLAN_idle_suspend.md).

## Отложено (сознательно)

- **Tier B — снос netstack.** Единственный способ срезать GC-нагрев от gvisor `stack.Stack` (~5.9 МБ/устройство) — `Close`+rebuild (не `Down`). Пробуждение = холодный реконнект (rebuild + handshake, in-flight потоки умирают). Нужен длинный отдельный порог + гистерезис.
- **Keys-safe / reduced-bind wake (путь A).** `Device.BindUpdate()` урезает bind на ЖИВОМ устройстве БЕЗ зануления ключей → пробуждение БЕЗ handshake (убрал бы «далёкий-сервер» затык). Но RAM спящей ноды тогда ~0.5 МБ (не ноль), плюс три ловушки (мутабельный `BatchSize()`, надо резать и TUN BatchSize, GRO off иначе паника при batch<128). Реализован путь B (Down) — RAM в ноль, проще; путь A эскалируем по данным. См. [wg-bindupdate-keys-safe].
- **Замер батареи на устройстве.** Эффект на радио/батарею (остановка keepalive-таймеров) — прогноз по исходникам, не замер; нужен Android batterystats до/после. Heap A/B на Android уже снят.
