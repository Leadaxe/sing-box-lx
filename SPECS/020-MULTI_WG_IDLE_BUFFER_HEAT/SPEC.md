# SPEC 020 — Idle-suspend простаивающих WireGuard/AmneziaWG эндпоинтов

> Авторитетный документ по первопричине нагрева — [`RESEARCH.md`](RESEARCH.md) (heap A/B на
> реальном устройстве: держатель GC-нагрева — `bufsArrs` recv-воркеров). Этот файл —
> **as-built запись реализованной фичи `lx_idle_suspend`**: что сделано, как работает,
> какой баг был найден и исправлен, и чем всё покрыто.
>
> Статус: **реализовано, исправлено, проверено вживую и юнит-тестами.**
> Ветка: `lx-spec020-idle-suspend`. Целевая база: lx-1.14.
> Связанные памятки: [[android-cpu-heat-multi-wg-gc]], [[awg-detour-guard-must-be-at-start]],
> [[wg-suspend-cost-and-gc-source]], [[spec020-idle-tick-misses-endpoints]].

---

## 1. Проблема

На Android ядро греется и пинит CPU, потому что **каждый** живой WireGuard/AmneziaWG
эндпоинт работает 24/7 независимо от того, идёт ли через него трафик. При N
сконфигурированных эндпоинтах все N держат:

- **recv-воркеры** со своими `bufsArrs` (`make([]*[65535]byte, BatchSize)`) — на
  Android при `BatchSize=128` это ~8 МБ на воркер, 2 воркера на устройство. Это
  **главный держатель GC-scan нагрева** (доказано heap A/B в [`RESEARCH.md`](RESEARCH.md)).
- **per-peer таймеры** (keepalive, handshake-retransmit) + для AWG junk-handshake
  машинерию — они срабатывают по своему расписанию даже на простое, **будят радио и
  жгут CPU/батарею**.
- **gvisor `stack.Stack`** netstack-граф на каждый эндпоинт.

До этой фичи единственные способы остановить эндпоинт — глобальная пауза
(`onPauseUpdated` при выключении экрана, гасит ВСЕ сразу), полный `Close()` (на
остановке ядра) и AWG-detour-guard (узкая защита корректности). **Не было понятия
«этот эндпоинт сейчас недостижим из активного маршрута»**, поэтому не было и
выборочного засыпания простаивающих нод.

## 2. Что реализовано (модель Down/Up)

Фича `lx_idle_suspend` выборочно **гасит** (`device.Down()`) любой WG/AWG эндпоинт,
который ОДНОВРЕМЕННО:

1. **недостижим** из активного дерева маршрутизации (не final, не цель правила, не
   текущий выбор активного селектора, не в активном пуле urltest, не detour-зависимость
   ничего из перечисленного), И
2. **простаивает** дольше порога `lx_idle_suspend`.

Механизм — **`device.Down()` / `device.Up()`** (не «лёгкий» timersStop, см. §7). Down
закрывает UDP-сокет, recv-воркеры выходят, их `bufsArrs` освобождаются (бьём прямо по
измеренному держателю нагрева из `RESEARCH.md`), per-peer таймеры останавливаются (экономия
радио/батареи). Пробуждение — лениво, на следующем дайле через эндпоинт.

Цена: Down зануляет крипто-сессию, поэтому пробуждение платит свежий WG-handshake на
первом пакете (см. §7 и §9). Это сознательный компромисс: спящая нода держит **ноль**
recv-памяти ценой ~1 RTT на первый пакет после пробуждения.

## 3. Конфигурация

```jsonc
"route": {
  "lx_idle_suspend": "30s"   // порог простоя; "0"/отсутствует = фича выключена
}
```

- Поле: `option.RouteOptions.LXIdleSuspend badoption.Duration`, `json:"lx_idle_suspend,omitempty"`
  (в [`option/route.go`](../../option/route.go), под `lx:begin/lx:end`).
- **По умолчанию (отсутствует / `"0s"` / `0`): фича выключена** — тик не запускается,
  нулевой оверхед, поведение как раньше (kill-switch для безопасного отката).
- Период тика: `max(порог / idleTickDivisor, idleTickFloor)` =
  `max(XX/2, 5s)`.

| `lx_idle_suspend` | период тика | определяется |
|---|---|---|
| `30s` | 15s | делителем |
| `60s` | 30s | делителем |
| `8s` | 5s | полом (floor) |
| `0` | — | тик не запущен (фича off) |

Константы `idleTickDivisor = 2` и `idleTickFloor = 5s` живут в
[`route/reachability_lx.go`](../../route/reachability_lx.go) как именованные
константы (не «магические» литералы).

## 4. Модель достижимости (reachability)

Эндпоинт **достижим**, если трафик может прямо сейчас на него попасть. Это отдельный
динамический обход дерева (НЕ статический граф `ConsumersOf`/`dependByTag`, который
перечисляет ВСЕ члены селектора как зависимости — нам же нужен текущий *активный*
выбор).

### 4.1 Сиды (точки входа в дерево)

- **final**: `outboundManager.Default()`.
- **Правила**: каждый outbound-тег, на который ссылается действие правила (`route` /
  `bypass`) из `router.Rules()`. Действия reject/sniff/dns/resolve/hijack-dns не дают
  сида (никуда не маршрутизируют).

### 4.2 Обход вниз от каждого сида (с защитой от циклов через visited-set)

| Тип узла | Достижимые дети |
|---|---|
| selector | **только** `Now()` (текущий выбор), НЕ `All()` |
| urltest round_robin | **весь** текущий пул — `ActiveTags()` (= `poolTags()`) |
| urltest legacy (least_test) | `Now()` (текущий выбранный узел) |
| обычный outbound (vless/http/…) | его `Dependencies()` (честная detour-цепочка) |

Обход транзитивный: selector→urltest→эндпоинты, selector→sub-selector и т.д. Узел,
посещённый по любому пути, помечается один раз (visited-set = дедуп + защита от циклов).

Реализация: `reachableSet` / `walkReachable` в
[`route/reachability_lx.go`](../../route/reachability_lx.go) — чистые функции,
развязанные с `*Router` через `resolve`-замыкание, поэтому юнит-тестируемы со стабами.

### 4.3 Семантика urltest — «дайл будит, idle-тик усыпляет»

Никакого спец-обхода «пул = всегда достижим» не нужно. Health-check probe идёт через
собственный `DialContext` узла (`urltest.URLTest`), то есть probe — это обычный дайл.
Поэтому ТОТ ЖЕ ленивый `Up()`-на-дайле, что обслуживает пользовательский трафик (§7),
будит спящий узел под probe автоматически.

Следствия:
- Узел **в** пуле probe'ится каждый `interval` → каждый probe штампует активность →
  пока `interval < XX`, он не простаивает → не усыпляется (живая нода остаётся живой).
- Узел, **выбывший** из пула, перестаёт probe'иться; как только он ещё и простоит > XX,
  он становится законным кандидатом на сон и усыпляется idle-тиком.
- Health-check **сам не гасит** узел обратно — гашение это работа idle-тика по таймауту.
  probe только `Up()`'ит (через дайл), завершается, и узел просто отпускается; тик
  усыпит его через XX, если он снова простаивает и недостижим.

## 5. Кэш достижимости и инвалидация (event-driven)

Полный обход на каждый тик — расточительство (бьёт по цели экономии). Поэтому набор
достижимых кэшируется и пересчитывается **только когда активное дерево реально
изменилось**.

- `r.reachDirty atomic.Bool` — флаг «грязно». Стартует `true` (первый тик считает).
- `r.reachCache map[string]bool` под `r.reachMu sync.RWMutex` — опубликованный набор.
- `InvalidateReachability()` (реализует `adapter.ReachabilityInvalidator`) ставит
  флаг. Обход НЕ выполняется под локом кэша (он зовёт группы с их собственными локами —
  иначе риск лок-ордер дедлока); считаем вне лока, публикуем под локом.

Три точки-события дёргают инвалидацию (через `service.FromContext[ReachabilityInvalidator]`,
так что `protocol/group` не импортирует `route`):

| Точка | Файл |
|---|---|
| `Selector.SelectOutbound` (переключение селектора) | `protocol/group/selector.go` |
| urltest legacy auto-switch | `protocol/group/urltest.go` |
| перестроение пула `balancer.onChange` ← `setSlots` | `protocol/group/urltest.go` + `urltest_balance_lx.go` |
| перезагрузка конфига / роутера | через пересоздание Router |

Хук-обёртка `invalidateReachability(ctx)` — в
[`protocol/group/reachability_lx.go`](../../protocol/group/reachability_lx.go).

Между событиями тик читает кэш и делает по одному сравнению простоя на эндпоинт —
никакого обхода.

## 6. Учёт простоя и засыпание/пробуждение (сторона эндпоинта)

Состояние и логика — в [`protocol/wireguard/endpoint.go`](../../protocol/wireguard/endpoint.go),
под `lx:begin/lx:end idle-suspend`:

- `lastActivity atomic.Int64` (unix-nanos) — штампуется в PostStart (базовая отметка,
  чтобы никогда-не-дайленный эндпоинт не выглядел «простаивающим 55 лет») и на каждом
  входе в дайл.
- `IdleSince() time.Duration` — сколько прошло с последнего дайла.
- `idleAsleep atomic.Bool` — true пока эндпоинт усыплён по простою. **Отличается** от
  `started`: guard-suspend ставит `started=false` БЕЗ `idleAsleep`, поэтому
  guard-усыплённый эндпоинт никогда не будится idle-логикой.
- `resumeMu sync.Mutex` — сериализует решение тика «усыпить» против конкурентного
  дайла «разбудить».

**`SuspendIfIdle(reachable, threshold)`** — решение тика на эндпоинт:
```
если reachable || IdleSince() < threshold: выход (тихо)
если !started.Load(): выход            // уже down (guard / awg-chain / закрыт)
если idleAsleep.CAS(false→true):
    started.Store(false)
    endpoint.Suspend()                 // device.Down(): recv-воркеры выходят, bufsArrs освобождаются
    log INFO "lx idle: suspend <tag> idle=<dur>"   // edge-triggered: одна строка на переход
```

**`resumeOnDial()`** — в начале каждого входа в дайл:
```
stampActivity()                        // всегда, закрывает гонку с тиком
если !idleAsleep: вернуть started      // быстрый путь (бодр / down по не-idle причине → не воскрешать)
под resumeMu:
    endpoint.Resume()                  // device.Up(): пере-открыть сокет, пере-поднять recv-воркеры
    started.Store(true); idleAsleep.Store(false)
    log INFO "lx idle: wake <tag> by=dial"
    вернуть true
```

Логирование **edge-triggered**: строка пишется только внутри успешного CAS-перехода
состояния. Тик, который перепроверяет уже-спящий или всё ещё достижимый эндпоинт —
молчит. Пара suspend↔wake в логе и есть сигнал флапа.

## 7. Цена Down/Up (почему именно Down, а не «лёгкий» timersStop)

Source-verified против сабмодуля wireguard-go. `device.Down()→Up()` — это **полный
реконнект**, не переключение таймера:

`Down()` (`downLocked`): зануляет все крипто-ключи + handshake-состояние
(`peer.Stop()` → `ZeroAndFlushAll()`), закрывает UDP-сокет (`BindClose` →
`netc.bind.Close()` + барьер `stopping.Wait()` до выхода recv-воркеров), отбрасывает
staged-пакеты, синхронно сносит 2·N+1 горутин. recv-воркер может выйти только по
`net.ErrClosed` — то есть «recv-воркеры → 0» это **детерминированное доказательство**,
что сокет реально закрыт.

`Up()` платит **полный handshake** на первом пакете (свежий Curve25519 + DH + KDF/AEAD,
плюс на этом форке вся AWG I1-обфускация + junk). Резюм-пути сессии в цикле Down/Up нет.

Что переживает цикл (это `changeState`, не `Close`): объекты Device/Peer, ClientBind,
буферные `WaitPool`'ы, очереди шифрования/дешифрования + их воркеры, и порт. **`Down()`
НЕ освобождает gvisor netstack** (~5.9 МБ/устройство) — это территория «Tier B»
(не реализовано).

Почему Down, а не light-sleep (timersStop): измерение в `RESEARCH.md` доказало, что
держатель GC-нагрева — recv-воркерные `bufsArrs`, а light-sleep их НЕ освобождает (он
экономит только батарею/таймеры). Поэтому реализован Down — он бьёт по **измеренному**
держателю.

## 8. Взаимодействие с AWG-detour-guard (инвариант)

AWG-detour-guard ([[awg-detour-guard-must-be-at-start]]) гасит через `device.Down()` на
старте AWG-эндпоинт, чья detour-цепочка достигает WireGuard-узла (AWG-over-WG вешает
ядро Android). Он ставит `started=false` БЕЗ `idleAsleep`.

idle-suspend это уважает **без отдельного флага**:
- `SuspendIfIdle` рано выходит на `!started` → guard-усыплённый эндпоинт никогда не
  трогается idle-тиком (не логирует suspend, он и так down).
- `resumeOnDial` идёт ПОСЛЕ существующего `!started`-гейта дайла, который уже возвращает
  «не готов» и обрывает дайл → guard-усыплённый эндпоинт никогда не воскрешается
  idle-логикой. Тот самый AWG-over-WG hang остаётся предотвращённым.

Проверено вживую (см. §12) и юнит-тестами `TestSuspendIfIdle_guardSuspendedNotTouched`,
`TestResumeOnDial_guardSuspendedNotWoken`.

## 9. Пользовательский опыт: цена пробуждения

`Up()` синхронный и не-сетевой (поднимает bind + *инициирует* handshake), поэтому сам
резюм мгновенный. Цену платит **первый пакет** — ждёт ~1 RTT, пока WG доделает
handshake (WG стейджит пакет и шлёт по готовности ключа). Это идентично холодному дайлу
на любой не-усыплённый эндпоинт или реконнекту после смены сети — не баг.

Замер (50 дайлов, реальная нода parnas WG, RTT до сервера ~4.8 мс — см. §12):
- COLD (нода спала → wake+handshake): медиана **~50–57 мс**.
- WARM (уже живая): медиана **~36 мс**.
- **Цена пробуждения ≈ +14–21 мс ≈ ~3 RTT** = один WG-handshake.

**Каверза (далёкие серверы):** handshake масштабируется с RTT. На близком сервере это
незаметные +14 мс; на межконтинентальном (RTT ~150 мс) тот же handshake даст ~+450 мс на
первый пакет — разовый ощутимый «затык», только латентность (пропускная не страдает,
batch активной ноды не меняется). Keys-safe путь без handshake (BindUpdate, §13) убрал бы
и это, но он не реализован — текущая модель Down/Up выбрана ради нулевой памяти спящей
ноды.

## 10. Файлы

Новые (lx-owned):
- [`route/reachability_lx.go`](../../route/reachability_lx.go) — walk, кэш, инвалидация,
  idle-тик, `suspendIdleEndpoints`.
- [`protocol/group/reachability_lx.go`](../../protocol/group/reachability_lx.go) —
  хук-обёртка `invalidateReachability(ctx)`.

Изменённые:
- [`option/route.go`](../../option/route.go) — поле `LXIdleSuspend`.
- [`route/router.go`](../../route/router.go) — состояние idle-тика, поле `endpoint
  adapter.EndpointManager` (тянется из ctx через `service.FromContext`), старт тика в
  PostStart / стоп в Close.
- [`adapter/outbound.go`](../../adapter/outbound.go) — узкие интерфейсы
  `IdleSuspendable` (`Tag`, `SuspendIfIdle`) и `ReachabilityInvalidator`.
- [`protocol/wireguard/endpoint.go`](../../protocol/wireguard/endpoint.go) —
  `lastActivity`, `IdleSince`, `stampActivity`, `idleAsleep`, `resumeMu`,
  `SuspendIfIdle`, `resumeOnDial`, вставка `resumeOnDial` в начало гейтов дайла.
- `protocol/group/selector.go`, `urltest.go`, `urltest_balance_lx.go` — точки
  инвалидации (по одной вставке).

## 11. БАГ, который был найден и исправлен (главное)

**Симптом.** Первый live-прогон (selector-сценарий, 8 WG/AWG эндпоинтов,
`lx_idle_suspend: 8s`) дал **0 строк `lx idle:` за 2+ минуты простоя**, где ожидалось 7
suspend'ов. Бокс стартовал чисто, конфиг декодировался — фича просто была инертной.

**Первопричина (source-verified).** `idleSuspendLoop` перебирал
`r.outbound.Outbounds()` и type-assert'ил каждый к `adapter.IdleSuspendable`. Но WG/AWG
**эндпоинты живут в endpoint-менеджере, а не в outbound-менеджере**.
`outbound.Manager.Outbounds()` возвращает ТОЛЬКО `m.outbounds` — он НЕ включает
`m.endpoint.Endpoints()`. (Контраст: `Outbound(tag)` специально делает fallback на
`m.endpoint.Get(tag)` — поэтому *walk* достижимости резолвил теги эндпоинтов нормально,
что и маскировало щель.) Список, по которому шёл тик, не содержал **ни одного**
`IdleSuspendable` → `SuspendIfIdle` не вызывался никогда → фича мертва.

**Почему юниты были зелёными.** Две половины тестировались изолированно: walk
(`reachability_lx_test.go`) и решение на эндпоинт (`endpoint_idle_lx_test.go`, прямой
вызов `SuspendIfIdle`). Кэш-тест стабил `Outbounds()` → nil. **Ничто не проверяло, что
петля реально ДОСТАЁТ эндпоинты.** Этот интеграционный шов и был слепым пятном.

**Фикс.** Router получает доступ к endpoint-менеджеру (`endpoint adapter.EndpointManager`,
тянется из ctx в `NewRouter` через `service.FromContext` — без правок `box.go`, менеджер
там уже зарегистрирован) и перебирает его в тике. Тело петли вынесено в
`suspendIdleEndpoints(reachable)`, который сканирует **оба** списка:
`r.endpoint.Endpoints()` (где IdleSuspendable реально есть) и `r.outbound.Outbounds()`
(для полноты — будущий не-endpoint IdleSuspendable; сегодня no-op). Nil-guard на
`r.endpoint` для стаб/без-эндпоинтов случая. Семантика публичного `Outbounds()` не
тронута.

**Регрессионный тест.** `route/idle_tick_endpoints_lx_test.go` гоняет
`suspendIdleEndpoints` через стаб endpoint-менеджера и проверяет, что каждый эндпоинт
посещён один раз с правильным флагом `reachable`. **Падает на до-фиксовом коде**
(`wg-1=0 wg-2=0` — тик слеп к эндпоинтам), проходит после. Это тот самый шов, что
пропустил исходный набор.

## 12. Покрытие тестами

### Юнит-тесты (29)

`route/reachability_lx_test.go` — обход:
`TestReachableFinalSeedOnly`, `TestReachableDetourChain`, `TestReachableSelectorOnlyNow`,
`TestReachableURLTestWholePool`, `TestReachableSelectorToSubSelector`,
`TestReachableCycleGuard`, `TestReachableUnknownTag`, `TestReachableEmptySelectorNow`,
`TestReachableMultipleSeeds`, `TestReachableSelectorToURLTestPool` (вложенная
selector→urltest, весь пул), `TestReachableDualPathDedup` (узел достижим по двум сидам),
`TestReachableSelectorMemberIsURLTestNotSelected` (дремлющее вложенное поддерево).

`route/reachability_cache_lx_test.go` — кэш:
`TestReachCache_recomputesOnlyWhenDirty`, `TestReachCache_resultIsCorrect`,
`TestReachCache_invalidateIsLockFree`.

`route/idle_tick_endpoints_lx_test.go` — интеграционный шов тика:
`TestIdleTick_iteratesEndpointManager` (регрессия на баг), `TestIdleTick_scansBothManagers`,
`TestIdleTick_nilEndpointManagerSafe`.

`protocol/wireguard/endpoint_idle_lx_test.go` — сторона эндпоинта:
`TestIdleSinceNeverDialed`, `TestIdleSinceAfterStamp`, `TestSuspendIfIdle_reachableNeverSuspends`,
`TestSuspendIfIdle_notIdleEnough`, `TestSuspendIfIdle_suspendsWhenIdleAndUnreachable`,
`TestSuspendIfIdle_thresholdBoundary`, `TestSuspendIfIdle_idempotentCAS`,
`TestSuspendIfIdle_guardSuspendedNotTouched` (инвариант §8),
`TestResumeOnDial_wakesAndStamps`, `TestResumeOnDial_dialBeforeTickRace`,
`TestResumeOnDial_guardSuspendedNotWoken`.

Все adversarially проверены: слом walk (`Now()`→`All()`) роняет dedup/dormant-тесты; слом
тика (только `Outbounds()`) роняет both-managers/iterates-тест.

### Live-проверка (macOS desktop, реальные ноды пользователя — детали в [TEST_PLAN](TEST_PLAN_idle_suspend.md))

Подтверждено вживую:
- **suspend срабатывает** — недостижимые+простаивающие эндпоинты гаснут (edge-triggered,
  одна строка на переход), достижимые — никогда.
- **wake by=dial** — дайл/health-check-probe будит спящий эндпоинт, проходит реальный
  трафик (HTTP 204); switch селектора → старый выбор уснул, новый проснулся (динамическая
  инвалидация кэша).
- **матрица достижимости** — final, rule-target, detour-цепочка, selector `Now`/switch,
  urltest round_robin пул целиком, legacy `Now`, dual-path дедуп, вложенные группы
  (selector→urltest→пул на **реальном production-конфиге** пользователя: switch
  `vpn-1`→`vpn-1-auto` разбудил ровно pool:4 ноды).
- **AWG-guard** — guard-усыплённая нода не появляется ни в одной `lx idle:` строке.
- **нет флапа** — пары suspend/wake всегда равны (15/15, 22/22…); +30s простоя без новых
  строк.
- **kill-switch** — `lx_idle_suspend: 0s` → ноль `lx idle:` строк.
- **ресурсы (A/B, 8 нод все усыплены)**: recv-воркеры `RoutineReceiveIncoming` **16→0**,
  RSS **39.3→27.0 МБ (−31%)**. `RoutineDecryption` остаётся 64 (крипто-пул переживает
  Down/Up by design). netstack Down не освобождает (Tier B).

## 13. Что НЕ сделано (отложено, сознательно)

- **Tier B — снос netstack.** Единственный способ срезать и GC-нагрев от gvisor
  `stack.Stack` (~5.9 МБ/устройство) — освободить netstack (`Close`+rebuild, не `Down`).
  Пробуждение = холодный реконнект (rebuild стека + handshake, in-flight потоки умирают).
  Нужен длинный отдельный порог + гистерезис; отложено до данных с устройства.
- **Keys-safe / reduced-bind wake (путь A).** Bind можно урезать на ЖИВОМ устройстве БЕЗ
  зануления ключей через `Device.BindUpdate()` — даёт пробуждение БЕЗ handshake (убрал бы
  «далёкий-сервер» затык §9). Но RAM спящей ноды тогда не ноль (~0.5 МБ), плюс три острых
  ловушки (мутабельный `BatchSize()`, нужно резать и TUN BatchSize, иначе `max()` клампит
  обратно к 128; открывать с GRO off иначе паника при batch<128). Реализован путь B (Down)
  — RAM в ноль, проще, безопаснее; путь A эскалируем по данным, если флап handshake'ов на
  пробуждении реально начнёт мешать. См. [[wg-bindupdate-keys-safe]].
- **Замер батареи на устройстве.** Эффект на радио/батарею (остановка keepalive-таймеров
  у спящих нод) — обоснованный прогноз по исходникам, не замер; нужен Android
  batterystats до/после. Аналогично heap A/B на Android (где `bufsArrs` ~8 МБ/воркер,
  эффект кратно больше десктопных −31% RSS) — единственное более сильное доказательство
  экономии памяти, не обязательное для поставки.
