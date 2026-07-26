# SPEC 020 — Idle-suspend простаивающих WireGuard/AmneziaWG эндпоинтов

**Фича:** [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) — проверено вживую (desktop + Android device) и юнит-тестами |
| Тег сборки | `with_lx_idle_suspend` (mobile-only, в AAR; НЕ в desktop/CLI) |
| Фича-флаг | `route.lx_idle_suspend: "<duration>"` (отсутствует/`0` = выключена) |

Фича `lx_idle_suspend` выборочно **гасит** (`device.Down()`) WG/AWG-эндпоинт, который одновременно **недостижим** из активного дерева маршрутизации и **простаивает** дольше порога. Пробуждение — лениво, на следующем дайле через эндпоинт (`device.Up()`). Опциональный второй порог `lx_idle_suspend_reachable` гасит и **достижимые** эндпоинты после более длинного окна простоя (см. «Конфигурация»).

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
- **DNS-detour'ы**: `OutboundTag()` каждого DNS-транспорта (`dnsTransportManager.Transports()`). DNS-сервер дайлится на каждой резолюции — его detour достижим по определению; без сида DNS-only-WG эндпоинт флапал бы Down/Up вокруг каждой паузы, добавляя handshake к первому DNS-запросу каждой сессии.

**Обход вниз** от каждого сида (visited-set = дедуп + защита от циклов):

| Тип узла | Достижимые дети |
|---|---|
| selector | **только** `Now()` (текущий выбор), НЕ `All()` |
| urltest round_robin | **весь** текущий пул — `ActiveTags()` |
| urltest legacy (least_test) | `Now()` |
| обычный outbound | его `Dependencies()` (detour-цепочка) |

Обход транзитивный (selector→urltest→эндпоинты и т.д.). Реализация: `reachableSet`/`walkReachable` в [`route/reachability_lx.go`](../../../route/reachability_lx.go) — чистые функции, развязанные с `*Router` через `resolve`-замыкание (юнит-тестируемы со стабами).

**Семантика urltest — «дайл будит, idle-тик усыпляет».** Health-check probe идёт через `DialContext` узла — то есть обычный дайл. Тот же ленивый `Up()`-на-дайле, что обслуживает трафик, будит спящий узел под probe. Узел в пуле probe'ится каждый `interval` → не простаивает → не усыпляется; выбывший из пула перестаёт probe'иться → простоит > порога → усыпляется тиком. Probe сам узел обратно не гасит — это работа тика по таймауту.

**Гейтинг проб по достижимости группы.** Обратная связь тоже стоит: после ухода селектора с urltest-группы её тикер продолжал бы пробы до `idle_timeout` (дефолт 30m = 10 циклов по N узлов), каждым probe будя усыплённых членов (handshake) впустую. Поэтому `loopCheck` перед пробой спрашивает `adapter.ReachabilityReporter` (Router, из ctx): **недостижимая группа пропускает цикл проб** (тикер жив, `idle_timeout` его добьёт; вернувшийся трафик через `Touch` — или возвращение достижимости — немедленно возобновляет пробы). С выключенной фичей (`lx_idle_suspend: 0`) или без build-тега reporter отвечает `true` для всего — гейт не вмешивается, поведение бит-в-бит апстримное. Цена согласована: history недостижимой группы стареет, первый выбор после возвращения идёт по устаревшей истории — ровно как сегодня после `idle_timeout`.

**Инвалидация первого выбора.** `performUpdateCheck` инвалидирует reach-кэш при **любой** смене выбранного узла, включая переход nil→первый выбор (раньше первый выбор кэш не инвалидировал: кэш, посчитанный на cold-start fallback-узле, зависал устаревшим до следующего auto-switch — не тот узел держался живым, а реальный усыплялся).

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

Состояние в [`protocol/wireguard/endpoint.go`](../../../protocol/wireguard/endpoint.go) (`lx:begin/lx:end idle-suspend`):
- `lastActivity atomic.Int64` (unix-nanos) — штампуется в PostStart (базовая отметка) и на каждом входе в дайл.
- `IdleSince() time.Duration`, `idleAsleep atomic.Bool` (true пока усыплён по простою — **отличается** от `started`: guard-suspend ставит `started=false` БЕЗ `idleAsleep`), `resumeMu sync.Mutex` (сериализует «усыпить» тика против «разбудить» дайла и против `Close`).
- `listenMode bool` — эндпоинт с `listen_port` (входящие пиры) **никогда не усыпляется**: у него нет дайл-пути, который бы его разбудил.
- `lastTransferSum uint64` — rx+tx устройства, увиденные предыдущим решением тика (только под `resumeMu`).

**`SuspendIfIdle(reachable, threshold, reachableThreshold)`** — решение тика (целиком под `resumeMu`):
```
если listenMode: выход                          // нет пути пробуждения
eff = threshold
если reachable:
    если reachableThreshold <= 0: выход         // достижимые по умолчанию не трогаем
    eff = reachableThreshold                    // opt-in длинное окно
если IdleSince() < eff: выход
если !started.Load(): выход                     // уже down (guard / awg-chain / закрыт)
если endpoint.ActiveTCPFlows() > 0: выход       // живые TCP-потоки в gVisor-стеке (без stamp)
delta = endpoint.TransferTotals() - lastTransferSum   // rx+tx из IpcGet, только для кандидатов
lastTransferSum += delta
если delta >= 4096:                             // настоящий (не-TCP) трафик established-потоков
    stampActivity(); выход
если idleAsleep.CAS(false→true):
    started.Store(false)
    endpoint.Suspend()                          // device.Down()
    log INFO "lx idle: suspend <tag>"           // edge-triggered
```

**Live-traffic-гейты** закрывают blackhole: активность считалась только по дайлам, а данные established-соединения идут app→conn→netstack→device, не входя в протокольный Endpoint — эндпоинт с живой закачкой, выпавший из активного дерева (селектор ушёл, пул перестроился), гасился посреди передачи, ломая обещание `interrupt_exist_connections=false`. Два гейта, оба вне hot-path (только для кандидатов на suspend):
1. **`ActiveTCPFlows()`** — gauge `TCP.CurrentEstablished` gVisor-стека устройства: точный признак живых TCP-потоков (закачки, push-сокеты), **иммунный к WG keepalive/rekey**. Без stamp'а — как только последний поток закрылся, эндпоинт снова idle. Для system-interface устройства (нет стека) gauge = 0.
2. **Порог по дельте счётчиков** (`delta >= 4096` байт между решениями) — для не-TCP-трафика (QUIC/UDP-потоки). Именно **порог**, не «счётчики сдвинулись»: keepalive (32 Б/интервал) и периодический rekey (~240 Б/~2 мин) двигают rx/tx постоянно — голая проверка «изменилось» никогда бы не усыпила пир с `persistent_keepalive`, похоронив главную экономию (глушение keepalive-таймеров). Шум остаётся сильно ниже порога; живой поток пробивает его мгновенно. Согласованная цена: **молчащий** QUIC-поток (редкие ping'и) порога не наберёт и будет усыплён — QUIC переустанавливается/мигрирует, кейс принят.

**`resumeOnDial()`** — в начале каждого дайла:
```
stampActivity()                          // всегда, закрывает гонку с тиком
если !idleAsleep: вернуть started        // быстрый путь
под resumeMu:
    endpoint.Resume()                    // device.Up()
    started.Store(true); idleAsleep.Store(false)
    log INFO "lx idle: wake <tag> by=dial"; вернуть true
```

Для domain-назначений `DialContext`/`ListenPacket` сначала резолвят через `dnsRouter` и только потом зовут `resumeOnDial` — неудачный DNS-lookup не платит Up+handshake зря (DNS-detour через сам эндпоинт безопасен: он re-enter'ит `DialContext` уже с IP и будит его сам).

`Close()` эндпоинта берёт `resumeMu` и гасит оба флага до `endpoint.Close()`: box закрывает endpoints **раньше**, чем Router останавливает тик, — без мьютекса решение тика гонялось бы с teardown'ом (`Suspend` по закрытому device).

Логирование **edge-triggered** — строка только внутри успешного CAS. Пара suspend↔wake в логе = сигнал флапа.

## Полная модель работы — состояния, гейты, таймлайны

> Развёрнутый гайд с рецептами конфигурации (RU/EN) — [docs-lx/lx-energy.ru.md](../../../docs-lx/lx-energy.ru.md) / [lx-energy.md](../../../docs-lx/lx-energy.md). Здесь — каноническая модель для разработчика ядра.

### Состояния эндпоинта

У WG/AWG-эндпоинта два флага (`started`, `idleAsleep`) и четыре достижимых состояния:

```mermaid
stateDiagram-v2
    direction LR
    AWAKE: AWAKE (started=true, idleAsleep=false) — device Up, воркеры живы
    IDLE_ASLEEP: IDLE-ASLEEP (started=false, idleAsleep=true) — device Down по простою
    IDLE_TORNDOWN: IDLE-TORNDOWN (idleAsleep=true, torndown=true) — device Closed, netstack освобождён
    GUARD_DOWN: GUARD-DOWN (started=false, idleAsleep=false) — AWG-guard / awg-chain-blocked
    CLOSED: CLOSED (started=false, idleAsleep=false)

    [*] --> AWAKE: PostStart (stampActivity)
    AWAKE --> IDLE_ASLEEP: тик - SuspendIfIdle (все гейты пройдены)
    IDLE_ASLEEP --> IDLE_TORNDOWN: тик - спит дольше lx_idle_teardown (Close)
    IDLE_ASLEEP --> AWAKE: дайл - resumeOnDial (device.Up, +1 RTT handshake)
    IDLE_TORNDOWN --> AWAKE: дайл - rebuild (новый Endpoint + Start, ~0.5-1 с)
    AWAKE --> GUARD_DOWN: SuspendAmneziaWG (guard, чистит idleAsleep)
    IDLE_ASLEEP --> GUARD_DOWN: SuspendAmneziaWG
    GUARD_DOWN --> [*]: только Close (guard one-way, by design)
    AWAKE --> CLOSED: Close (под resumeMu)
    IDLE_ASLEEP --> CLOSED: Close
    IDLE_TORNDOWN --> CLOSED: Close (идемпотентен, device уже nil)
```

Инварианты переходов:
- **IDLE-ASLEEP → AWAKE** делает ТОЛЬКО настоящий дайл. Pause/wake экрана и смена сети видят транспортный флаг `suspended` и эндпоинт не поднимают; guard-состояние не воскрешается ничем.
- **GUARD-DOWN терминально** до Close — `resumeOnDial` возвращает `started` (false) на быстрый путь, тик выходит на `!started`.
- Все переходы, трогающие device (`Suspend`/`Resume`/`Close`), сериализованы `resumeMu`.

### Решение тика — цепочка гейтов `SuspendIfIdle`

```mermaid
flowchart TD
    T[тик: каждые max её порога/2, 5s\npause-aware] --> R{"reachable[tag]?\n(кэш достижимости)"}
    R -- нет --> W1[окно = lx_idle_suspend]
    R -- да --> RT{lx_idle_suspend_reachable > 0?}
    RT -- нет --> OUT1([выход: достижимый не гасится])
    RT -- да --> W2[окно = lx_idle_suspend_reachable]
    W1 --> LM{listen_port-эндпоинт?}
    W2 --> LM
    LM -- да --> OUT2([выход: некому будить])
    LM -- нет --> IDLE{"IdleSince() >= окна?\n(часы = последний дайл)"}
    IDLE -- нет --> OUT3([выход: рано])
    IDLE -- да --> ST{started?}
    ST -- нет --> OUT4([выход: уже down - guard/closed])
    ST -- да --> TCP{"ActiveTCPFlows() > 0?\n(gVisor CurrentEstablished)"}
    TCP -- да --> OUT5([выход: живые TCP-потоки])
    TCP -- нет --> DELTA{"delta rx+tx >= 4096 Б\nс прошлого решения?"}
    DELTA -- да --> STAMP[stampActivity - сброс часов] --> OUT6([выход: живой UDP/QUIC-трафик])
    DELTA -- нет --> CAS["idleAsleep.CAS(false→true)\nstarted=false\ndevice.Down()"]
    CAS --> LOG[лог: lx idle suspend]
```

Пояснения к гейтам:
- Порядок дёшево→дорого: map-lookup и атомики до `IpcGet`/stats. Гейты 5–6 (TCP/delta) выполняются только для реальных кандидатов на suspend — вне hot-path.
- **TCP-гейт без stamp'а**: закрылся последний поток — эндпоинт снова кандидат уже на следующем тике.
- **Порог 4096 Б** отделяет живой поток от keepalive/rekey-шума (32 Б/интервал + ~240 Б/~2 мин): пир с `persistent_keepalive` ЗАСЫПАЕТ (глушение его таймеров — цель фичи), молчащий QUIC-поток — согласованная жертва.

### Ночной таймлайн (канонический сценарий)

Конфиг: round_robin pool=3, `interval=15m`, `idle_timeout=30m`, `lx_idle_suspend=30s`, `lx_idle_suspend_reachable=30m`.

```
T=0        последний пользовательский дайл (Touch обновил lastActive группы)
T=0..30m   ХВОСТ ПРОБ: тикер группы жив (Since(lastActive) <= idle_timeout),
           пробы в T=15m, T=30m дайлят членов пула -> их IdleSince сбрасывается
T~45m      тик loopCheck: Since(lastActive)=45m > 30m -> ТИКЕР ГАСНЕТ (проб больше нет)
T~60m      IdleSince членов пула (от последней пробы T=30m) > reachable(30m)
           -> тик гасит все 3 узла: воркеры выходят, таймеры молчат, радио спит
...ночь... полная тишина: ни проб, ни keepalive, ни recv-воркеров
УТРО       первый дайл: pick -> слот -> resumeOnDial будит ОДИН узел (+1 RTT);
           Touch заводит тикер; loopCheck делает НЕМЕДЛЕННЫЙ дотест (lastActive стар)
           -> пул/выбор обновлены свежими замерами за секунды
```

Гарантия отсутствия гонки «проба будит засыпающего»: пробы молкнут в `T = idle_timeout`, сон достижимых наступает не раньше `T = последняя проба + reachable`; при `reachable >= idle_timeout` будильника к моменту сна физически не существует. Жёсткая валидация — только `reachable >= lx_idle_suspend`; `>= idle_timeout` — рекомендация конфигу (нарушение = 1–2 цикла флапа в хвосте, не поломка).

### Кто кого будит и глушит — сводная матрица

| Событие | Спящий (idle) эндпоинт | Guard-down AWG | Тикер проб группы |
|---|---|---|---|
| Дайл трафика через эндпоинт | **будит** (`resumeOnDial`) | не будит («not ready») | `Touch` заводит/держит |
| Проба urltest (это дайл) | **будит** — потому пробы гейтятся | не будит | — |
| Screen on / смена сети (pause-wake) | не будит (флаг `suspended`) | не будит | pause-registered: возобновляется |
| `idle_timeout` группы | — | — | **гасит насовсем** (до Touch) |
| Группа недостижима (гейт SPEC 020) | — | — | циклы **пропускаются** (тикер жив) |
| `passive_check`: выбранный жив | — | — | циклы **пропускаются** |
| Ручной тест (force) | будит всех, кого пробует | не будит | — |

## Механизм Down/Up

`device.Down()→Up()` — **полный реконнект**, не переключение таймера (source-verified против сабмодуля).

`Down()` (`downLocked`): зануляет крипто-ключи + handshake (`peer.Stop()`→`ZeroAndFlushAll()`), закрывает UDP-сокет (`BindClose`+барьер `stopping.Wait()` до выхода recv-воркеров), сносит `2·N+1` горутин. recv-воркер выходит только по `net.ErrClosed` → «recv-воркеры → 0» = **детерминированное доказательство**, что сокет закрыт и `bufsArrs` освобождены.

`Up()` синхронный, не-сетевой (поднимает bind + *инициирует* handshake). Цену платит **первый пакет** — ждёт ~1 RTT, пока WG доделает handshake (плюс на AWG вся I1-обфускация + junk). Идентично холодному дайлу.

**Что переживает цикл** (это `changeState`, не `Close`): объекты Device/Peer, ClientBind, буферные пулы, очереди шифрования + воркеры, порт. **`Down()` НЕ освобождает gvisor netstack** (~5.9 МБ/устройство) — Tier B, не реализовано (см. § «Отложено»).

Компромисс: спящая нода держит **ноль** recv-памяти ценой одного handshake на пробуждении. На близком сервере это +14–21 мс (≈ 1 handshake, ≈ 3 RTT); на межконтинентальном (RTT ~150 мс) — разовый ~+450 мс на первый пакет (только латентность, throughput не страдает). Keys-safe путь без handshake (`BindUpdate`) отложен.

## Совместимость с wireguard-go v0.0.5

Idle-suspend опирается на стабильный device-API вендоренной базы (см. [SPEC 003](../003-AWG2_CLIENT_ENDPOINT/SPEC.md)). На текущей базе v0.0.5:
- **`Down()`/`Up()`/`BindUpdate()` присутствуют** ([device.go:237/241/507](../../../submodules/wireguard-go/device/device.go)) — механизм работает без изменений.
- **`bufsArrs`** по-прежнему recv-worker аллокация из фиксированного `messageBuffers`-пула ([receive.go:89](../../../submodules/wireguard-go/device/receive.go)); `Down()` освобождает её выходом воркеров. Held-держатель, ради которого фича сделана, не сдвинулся.
- **`SetSinglePeerMode`** (Darwin-only оптимизация из v0.0.5, ломает роуминг пира) sing-box **не вызывает** — не пересекается с Suspend/Resume.

## Конфигурация

```jsonc
"route": {
  "lx_idle_suspend": "30s",             // порог простоя НЕдостижимых; "0"/отсутствует = фича выключена
  "lx_idle_suspend_reachable": "5m",    // опционально: порог простоя ДОСТИЖИМЫХ; "0"/отсутствует = достижимые не усыпляются
  "lx_idle_teardown": "5m"              // опционально: сколько СПАТЬ до полного сноса (Close);
                                        //   дефолт = lx_idle_suspend_reachable; "0" = сноса нет
}
```

- Поля `option.RouteOptions.LXIdleSuspend` / `LXIdleSuspendReachable badoption.Duration` в [`option/route.go`](../../../option/route.go) (`lx:begin/lx:end`).
- **`lx_idle_teardown`** — третий уровень (снос netstack), см. §«Третий уровень». Отсчёт от засыпания; дефолт = `lx_idle_suspend_reachable`; требует включённого `lx_idle_suspend`. Поле — указатель (`*badoption.Duration`), потому что явный `"0"` (сноса нет) обязан отличаться от отсутствия (наследовать окно достижимых): у значимого типа оба схлопываются в один ноль, и kill-switch перестаёт существовать. Предусловия (`требует lx_idle_suspend`, отказ в сборке без тега) проверяются по факту присутствия поля, а не по величине окна, — иначе явный `"0"` проскакивал бы мимо них.
- **`lx_idle_suspend_reachable`** — ответ на «пул round_robin жив 24/7 при нулевом трафике»: достижимый эндпоинт (член пула, выбранный узел, final), простоявший дольше этого окна, тоже гасится и лениво будится следующим дайлом (~1 handshake RTT на первый пакет — потому окно ДОЛЖНО быть заметно больше основного порога: цена wake платится на каждом «холодном» заходе). Валидация: требует `lx_idle_suspend`; должен быть `>= lx_idle_suspend`; **рекомендация** — `>= idle_timeout` всех urltest-групп над эндпоинтами (пробы уже заглохли к моменту сна, иначе probe-флап; с `passive_check` у группы, SPEC 019, требование мягче — пробы при живом трафике и так не ходят). Live-traffic-гейты защищают живые соединения через достижимый эндпоинт от гашения.
- **Build-тег `with_lx_idle_suspend` (mobile-only).** Тик компилируется только с тегом; добавлен в mobile AAR ([`cmd/internal/build_libbox`](../../../cmd/internal/build_libbox/main.go)), НЕ в desktop `LX_TAGS` (на десктопе `BatchSize` мал, экономить нечего). Бинарь без тега, получивший конфиг с `lx_idle_suspend`, **падает при старте** с явной ошибкой (`rebuild with -tags with_lx_idle_suspend`) — никакого молчаливого no-op. Гейт — `startIdleSuspend` ([`route/reachability_lx.go`](../../../route/reachability_lx.go) / stub [`route/idle_suspend_stub_lx.go`](../../../route/idle_suspend_stub_lx.go)). Hot-path дайла (`resumeOnDial`) НЕ расщепляется (без тега `idleAsleep` никогда не true).
- **По умолчанию (отсутствует/`0`) — выключено** (kill-switch): тик не запускается, нулевой оверхед. Ортогонально build-тегу.
- Период тика: `max(порог / idleTickDivisor, idleTickFloor)` = `max(XX/2, 5s)` (константы в [`route/reachability_lx.go`](../../../route/reachability_lx.go)). Период считается от **основного** порога; reachable-порог ловится с тем же шагом (опоздание пол-тика на многоминутном окне несущественно).
- **Тик pause-aware**: тикер регистрируется в `pause.Manager` (`pause.RegisterTicker`, как тикер urltest) — при паузе устройства (screen off / нет сети) тик молчит; pause-callbacks и так гасят все WG-устройства, тикать сквозь паузу нечего. Стоп-канал передаётся в горутину тика **параметром** (Close закрывает и nil-ит поле; перечитывание поля из лупа было гонкой: nil-канал блокирует навсегда — утечка горутины и тикера).

| `lx_idle_suspend` | период тика |
|---|---|
| `30s` | 15s |
| `60s` | 30s |
| `8s` | 5s (floor) |
| `0` | тик не запущен |

## Инвариант: взаимодействие с pause.Manager (screen off / смена сети)

Апстримный `onPauseUpdated` транспортного эндпоинта делает `device.Down()` на паузе и `device.Up()` на wake. Безусловный `Up()` **воскрешал** suspended-устройства за спиной state-machine: протокольные флаги оставались `started=false` (+`idleAsleep=true` для idle-кейса), `SuspendIfIdle` навсегда выходил на `!started`, а недостижимый эндпоинт не получает дайлов — один цикл screen-off/on или wifi↔cellular **необратимо** (до рестарта) отменял всю экономию по всем спящим эндпоинтам. Для guard-suspended AWG это ещё и re-handshake в WG-цепь мимо guard'а (SPEC 007 §3.3(d)).

Фикс: транспортный `Endpoint` держит `suspended atomic.Bool` (ставится `Suspend()`, снимается `Resume()`); `onPauseUpdated` на `DeviceWake`/`NetworkWake` **пропускает** suspended-устройство. Кто усыпил — тот и будит: idle-suspend через `resumeOnDial`, guard — никогда.

## Инвариант: взаимодействие с AWG-detour-guard

AWG-detour-guard ([awg-detour-guard-must-be-at-start]) гасит `device.Down()` AWG-эндпоинт, чья detour-цепочка достигает WG-узла (AWG-over-WG вешает ядро Android). Он ставит `started=false` БЕЗ `idleAsleep`. idle-suspend это уважает **без отдельного флага**:
- `SuspendIfIdle` рано выходит на `!started` → guard-усыплённый эндпоинт тик не трогает.
- `resumeOnDial` идёт ПОСЛЕ существующего `!started`-гейта дайла (который уже обрывает дайл) → guard-усыплённый эндпоинт idle-логикой не воскрешается. AWG-over-WG hang остаётся предотвращённым.

Проверено юнит-тестами `TestSuspendIfIdle_guardSuspendedNotTouched`, `TestResumeOnDial_guardSuspendedNotWoken` и вживую.

## Файлы

Новые (lx-owned):
- [`route/reachability_lx.go`](../../../route/reachability_lx.go) — `//go:build with_lx_idle_suspend` — walk, кэш, idle-тик (pause-registered), `suspendIdleEndpoints`, `startIdleSuspend`/`stopIdleSuspend`, `OutboundReachable`.
- [`route/idle_suspend_stub_lx.go`](../../../route/idle_suspend_stub_lx.go) — `//go:build !with_lx_idle_suspend` — заглушки (ошибка если опция задана; `OutboundReachable`→true).
- [`route/reachability_common_lx.go`](../../../route/reachability_common_lx.go) — без тега: только `InvalidateReachability`.
- [`protocol/group/reachability_lx.go`](../../../protocol/group/reachability_lx.go) — хук `invalidateReachability(ctx)`.

Изменённые:
- [`option/route.go`](../../../option/route.go) — `LXIdleSuspend`, `LXIdleSuspendReachable`.
- [`route/router.go`](../../../route/router.go) — состояние тика (+pause-callback), `endpoint adapter.EndpointManager` (из ctx), PostStart→`startIdleSuspend()`, стоп в Close.
- [`box.go`](../../../box.go) — регистрация Router как `ReachabilityInvalidator` и `ReachabilityReporter`.
- [`cmd/internal/build_libbox/main.go`](../../../cmd/internal/build_libbox/main.go) — тег в `sharedTags` (mobile).
- [`adapter/outbound.go`](../../../adapter/outbound.go) — интерфейсы `IdleSuspendable` (3-арг `SuspendIfIdle`), `ReachabilityInvalidator`, `ReachabilityReporter`.
- [`protocol/wireguard/endpoint.go`](../../../protocol/wireguard/endpoint.go) — состояние (+`listenMode`, `lastTransferSum`) + `SuspendIfIdle`/`resumeOnDial`, DNS-lookup до wake, `Close` под `resumeMu`, вставка `resumeOnDial` в гейты дайла (без тега — дёшево).
- [`transport/wireguard/endpoint.go`](../../../transport/wireguard/endpoint.go) — `suspended`-флаг (pause-wake гейт), `TransferTotals()`, `ActiveTCPFlows()`.
- [`transport/wireguard/device_stack.go`](../../../transport/wireguard/device_stack.go) — `CurrentEstablished()` (gauge gVisor-стека).
- `protocol/group/selector.go`, `urltest.go`, `urltest_balance_lx.go` — точки инвалидации (включая первый выбор), probe-гейт по `ReachabilityReporter`.

## Проверено

- **Юнит-тесты**: обход (final/detour/selector-Now/urltest-пул/циклы/дедуп/вложенные/DNS-detour-сиды), кэш (recompute-only-when-dirty, lock-free invalidate), интеграционный шов тика (перебор обоих менеджеров), сторона эндпоинта (idle/threshold/CAS-идемпотентность/guard-инвариант/дайл-против-тика гонка/reachable-порог/listenMode/transfer-гейт), pause-wake гейт транспорта, `OutboundReachable` (feature-off→true, чтение кэша), первый-выбор-инвалидация, живой health-check пула (`balancePoolFirstLive`: replace-in-slot, dead-keeps-slot, полный пул не пробует вне пула). Adversarially: слом walk (`Now()`→`All()`) роняет дедуп/dormant; слом тика (только `Outbounds()`) роняет both-managers.
- **Live desktop** (реальные ноды): suspend недостижимых, wake by dial/probe, switch селектора (старый уснул, новый проснулся), матрица достижимости на production-конфиге, guard-инвариант, no-flap, kill-switch. Ресурсы A/B (8 нод усыплены): recv-воркеры 16→0, RSS −31%.
- **Android device** (CPH2411, Android 15): heap A/B на целевой платформе — `bufsArrs` (`PopulatePools.func3`) **223.93→89.89 МБ (−60%, −134 МБ)**, recv-воркеры 18→2. ~8.4 МБ/воркер, совпадает с оценкой `BatchSize=128` из RESEARCH.md. Артефакты — [ANDROID_RESEARCH](ANDROID_RESEARCH/README.md).
- **Device-verified (2026-07-15, CPH2411, LxBox v2.15.4 + ядро rc.2)**: владелец подтвердил вживую работу прежнего поведения и ревизии (suspend/wake/probe-семантика, `lx_idle_suspend_reachable` на дефолте клиента 5m, passive_check). Промоут rc.2 → стабильный v1.14.0-lx.5.
- **Уровень 3 (`lx_idle_teardown`, v1.14.0-lx.7)** — юнит-тесты: окно от засыпания (не от дайла), 0=выкл, awake/guard не сносятся, guard чистит teardown-состояние, шов тика (оба решения + порог), дефолт-резолвер порога; transport: РЕАЛЬНЫЙ цикл teardown→rebuild на живом gVisor-стеке (+ повторный цикл), PortAddresses на снесённом (кэш, без nil-паники), перенос L3 return-path через rebuild, откат частичного rebuild, идемпотентный Close, ошибка дайла вместо паники.
- **Device-verified уровня 3 (2026-07-15, CPH2411, rc.2)**: цикл suspend(43s)→teardown(slept=5m19s)→rebuild(by=dial) на 3 эндпоинтах, включая AWG с junk; горутины Device 0 после сноса и все вернулись (+2 recv-воркера) после пересборки; нода жива сквозь новый netstack (161–252 мс). Тайминги совпали с моделью. Промоут rc.2 → стабильный v1.14.0-lx.7. Детали и **поправка по RAM** (глобальный пул `buf` держит 63% кучи и переживает Close by design — выигрыш teardown = netstack, заметен при многих нодах) — [TEST_PLAN §L3 RESULT](TEST_PLAN_idle_suspend.md).

Детали прогонов — [TEST_PLAN](TEST_PLAN_idle_suspend.md).

## Третий уровень — `lx_idle_teardown` (снос netstack, Tier B)

`Down()` — заморозка: recv-воркеры и их буферы освобождены, таймеры молчат, но **gvisor `stack.Stack` (~5.9 МБ/устройство) остаётся жить** вместе с объектами Device/Peer. Третий порог добивает и его.

### Отсчёт и дефолт

`lx_idle_teardown` отсчитывается **от момента засыпания** (перехода в IDLE-ASLEEP), а НЕ от последнего дайла — так порог не зависит от того, каким из двух окон нода уснула, и остаётся честным «сколько она уже спит».

Три различимых состояния:

| Значение | Смысл |
|----------|-------|
| отсутствует | наследует `lx_idle_suspend_reachable` (а если и тот не задан — сноса нет) |
| `"0"` | **снос выключен**: ноды остаются просто усыплёнными, пробуждение стоит ~1 RTT вместо пересборки |
| `"5m"` | снос после 5 минут сна |

Пример на клиентских дефолтах (`30s` / `5m` / teardown по умолчанию `5m`): недостижимая нода засыпает через 30 с простоя, ещё через 5 мин сна — сносится. Достижимая: 5 мин простоя → сон → +5 мин → снос.

### Что происходит при сносе

`Teardown` = `device.Down()` (если ещё не) + `device.Close()` + освобождение объекта: `stack.Close()` + `Abort()` cleanup-эндпоинтов + `stack.Wait()`. Уходит netstack, Device, Peer'ы, bind, очереди шифрования — на спящую-снесённую ноду не остаётся **ничего**, кроме конфига в памяти.

**Объект одноразовый.** `stackDevice.Close` использует `closeOnce` и закрывает каналы `done`/`events` ([device_stack.go](../../../transport/wireguard/device_stack.go)) — повторный `Start` на нём невозможен. Поэтому пробуждение = **rebuild**: создать новый `wireguard.Endpoint` из сохранённых `EndpointOptions` и пройти обе стадии `Start` (включая резолв peer-домена). Отсюда требование: `protocol/wireguard.Endpoint` **хранит свои `wireguard.EndpointOptions`** (сегодня они живут только внутри `NewEndpoint`).

### Цена и почему она принята

| | Down (уровни 1–2) | Teardown (уровень 3) |
|---|---|---|
| Освобождается | recv-воркеры + буферы, таймеры | **всё**, включая netstack (~5.9 МБ/нода) |
| Пробуждение | `Up()` + handshake ≈ **+1 RTT** | rebuild device+netstack + резолв + handshake ≈ **0.5–1 с** |
| In-flight соединения | приостановлены (гейты не дают уснуть при живом трафике) | **рвутся** (но гейты те же — при живом трафике сноса не будет) |

Решение владельца (2026-07-15): 0.5–1 с на первый запрос после ≥5 мин сна пользователь не замечает; порванное соединение приложение поднимет заново; фича mobile-only, десктопа/роутера не касается. Выигрыш — не только RAM, но и **отдаление от потолка `SetMemoryLimit`** (§271): чем меньше живой кучи держат неиспользуемые ноды, тем реже GC-циклы у активных.

**Масштаб выигрыша — честно (замерено на устройстве, [TEST_PLAN §L3 RESULT](TEST_PLAN_idle_suspend.md)).** Уровень 3 освобождает ровно своё: netstack + объекты Device + горутины (проверено — все Device-горутины уходят в 0). Но **глобальный пул `sing/common/buf`** (`GetOutboundBuffer → buf.Get`, [pools.go:113](../../../submodules/wireguard-go/device/pools.go)) общий на процесс и переживает `Close` by design — на тестовом конфиге он держал 63% кучи (23.4 МБ), поэтому heap до/после сноса 3 нод почти не двинулся (36.7→36.3 МБ). Вывод: **основную RAM даёт уровень 1** (recv-буферы, −134 МБ при 8 нодах), уровень 3 добирает netstack (~5.9 МБ/нода) и заметен при МНОГИХ нодах. Не ждать от него чуда на конфиге из трёх эндпоинтов.

### Гейты и инварианты (те же, что у suspend, плюс два)

Решение о сносе принимается в том же тике и под тем же `resumeMu`, после всех гейтов suspend'а:

```
если lx_idle_teardown <= 0: выход
если !idleAsleep || torndown: выход        // сносим только УЖЕ спящих, один раз
если SleepSince() < lx_idle_teardown: выход
если torndown.CAS(false→true):
    endpoint.Teardown()                    // Close: netstack освобождён
    log INFO "lx idle: teardown <tag> slept=<...>"
```

Отдельный live-traffic-гейт здесь **не нужен** (и его нет в коде): узел с живыми потоками не проходит TCP/delta-гейты suspend'а и потому не бывает `idleAsleep`; проснуться, не сняв `idleAsleep` под `resumeMu`, невозможно — значит `idleAsleep=true` в момент решения уже гарантирует отсутствие трафика.

- **`torndown` — отдельный флаг**, не переиспользует `idleAsleep`: снесённая нода остаётся `idleAsleep=true` (она всё ещё «спит по простою»), но требует rebuild вместо `Up()`. Три состояния сна различимы: AWAKE / IDLE-ASLEEP / IDLE-TORNDOWN.
- **Guard-суспенд неприкосновенен.** `!started` гейт стоит раньше (см. §SuspendIfIdle), поэтому guard-опущенный AWG не сносится — иначе rebuild на дайле воскресил бы AWG-over-WG.
- **listen-mode не сносится** — как и не усыпляется (нет пути пробуждения).
- **pause-wake не воскрешает**: транспортный флаг `suspended` остаётся выставленным, а device вообще nil — `onPauseUpdated` не должен на нём падать (nil-guard обязателен).
- **`Close()` эндпоинта** (teardown box'а) поверх уже снесённой ноды — идемпотентен (device уже nil).

### Пробуждение (rebuild-on-dial)

`resumeOnDial` получает третью ветку ПЕРЕД обычным `Resume()`:

```
под resumeMu:
    если torndown:
        endpoint = wireguard.NewEndpoint(сохранённые options)   // новый объект
        endpoint.Start(false); endpoint.Start(true)             // резолв + поднятие
        torndown=false; idleAsleep=false; started=true
        log INFO "lx idle: rebuild <tag> by=dial"
        вернуть true
    иначе если idleAsleep: ... (существующий Resume-путь)
```

Rebuild синхронный и держит `resumeMu` — параллельные дайлы ждут один rebuild, а не запускают N. Ошибка любого шага (rebuild/Start/PostStart, например резолв peer-домена) → лог + **откат `Teardown()`** обратно в чисто-снесённое состояние + `false` («WireGuard is not ready yet»); флаги не сбрасываются, следующий дайл повторяет с нуля. Откат обязателен: у tun-девайса events-канал с буфером 1 (одно `EventUp` уже отправлено) — повторный `Start` поверх полусобранного девайса завис бы на этом канале под `resumeMu`, повесив все дайлы. Транспорт дополнительно: `PortAddresses()` отдаёт кэш (L3-слой может спросить у снесённого — nil-паника недопустима), а `Rebuild()` переносит привязанный L3 return-path в новый wrapper (sing-tun про наш цикл не знает и второй раз не attach'ится).

## Отложено (сознательно)

- **A/B-замер батареи** (абсолютный срез снят, парного прогона нет) — см. §«Замер батареи» ниже.

## Замер батареи (batterystats)

Процедура, baseline и границы применимости. Раздел заменяет прежний пункт «Отложено: замер батареи» — абсолютный срез на целевом устройстве **снят**, парный A/B (фича off/on) остаётся.

### Процедура

```bash
D=<serial|ip:port>
# uid приложения: в batterystats он пишется u0aXXX, где XXX = uid − 10000
adb -s $D shell "pm list packages -U | grep <package>"    # напр. uid:10574 → u0a574

adb -s $D shell dumpsys batterystats --reset              # обнулить перед окном
# ... наблюдаемое окно (VPN поднят, телефон в обычном использовании) ...
adb -s $D shell dumpsys batterystats | grep "UID u0aXXX:"  # оценка mAh
adb -s $D shell dumpsys batterystats | grep -A 22 "^  u0aXXX:"  # радио/wifi/wakelock детально
```

⚠️ **Обязательное условие**: телефон **не на зарядке** — на кабеле Android статистику не копит (`dumpsys battery` → `AC/USB powered: false`). Это первое, что надо проверить, иначе цифры пустые или врут.

### Как читать (важно)

`UID u0aXXX: <всего>` включает **screen** — подсветку экрана, пока открыт UI приложения. К стоимости ядра это отношения не имеет. Реальная цена фонового VPN — поле **`fgs:`** (foreground-service). Ключевые индикаторы механизма сна: **`wakelock`** (тик и таймеры), `Mobile radio AP wakeups` / `WiFi AP wakeups` (пробуждения радио), `WiFi Sleep time %`.

### Baseline (2026-07-15, CPH2411, LxBox v2.15.7 + ядро v1.14.0-lx.7, дефолты 30s/5m/teardown 5m, 3 WG/AWG-эндпоинта)

Окно ~3ч45м аптайма VPN, обычное использование, не на зарядке:

```
UID u0a574: 74.8 mAh  fg: 4.82  bg: 0.00749  fgs: 17.2  cached: 0.00403
  screen=50.6 (10m экрана)      ← НЕ расход ядра
  cpu=16.4 (из них fgs=10.5)
  mobile_radio=4.48 (24m активного радио, 19 AP wakeups)
  wifi=3.31 (WiFi Sleep time 3h44m = 99.5%)
  wakelock=0.000765 (290 мс за 4 часа)
```

Трактовка: фоновый VPN-сервис ≈ **17.2 mAh / 3ч45м ≈ 4.6 mAh/час ≈ 0.13%/час** от батареи 3432 mAh (≈3%/сутки). **`wakelock` 290 мс за 4 часа** — прямое подтверждение, что тик pause-aware и таймеры спящих нод заглушены; `bg ≈ 0.007 mAh` — фоновой работы вне сервиса практически нет.

### Чего этот замер НЕ доказывает

Это **абсолютный срез, а не A/B**: он показывает, что расход мал, но не измеряет вклад самой фичи. Для честной дельты нужен парный прогон в идентичных условиях, по 8+ часов каждый, с `--reset` перед каждым: `route.lx_idle_suspend: "0"` (kill-switch) против включённой лесенки. Сравнивать `fgs:` и `wakelock`, не полное значение (в него влезает экран).

## ОТВЕРГНУТО: путь A (keys-safe / reduced-bind wake)

**Решение 2026-07-15 (владелец): закрыт, не «отложен».** Идея: `Device.BindUpdate()` пересобирает сокеты на ЖИВОМ устройстве, НЕ трогая ключи (source-verified: [device.go:507](../../../submodules/wireguard-go/device/device.go) — `closeBindLocked` + `bind.Open`, крипто-состояние не участвует, в конце чистится кэш source-адресов). Значит спящую ноду можно держать с урезанным bind и будить БЕЗ handshake — сессия цела, «+1 RTT» исчезает, а достижимые ноды можно было бы усыплять агрессивно (секунды вместо минут).

**Почему закрыт — три независимых довода, каждый достаточен:**

1. **Rekey-окно убивает выигрыш при наших порогах.** Сессия WireGuard живёт `RekeyAfterTime = 120s`, после `RejectAfterTime = 180s` невалидна ([device/constants.go:17,22](../../../submodules/wireguard-go/device/constants.go)). Клиентский дефолт reachable-порога — **5 минут**, недостижимых — 30 секунд + реальные паузы трафика. То есть к моменту пробуждения сессия почти всегда мертва и handshake происходит В ЛЮБОМ случае: путь A не экономит ничего, но платит ~0.5 МБ на спящую ноду (bind жив) вместо нуля у пути B. **Чистый проигрыш.**
2. **Смена сети (Wi-Fi ↔ LTE) — норма на mobile, и она бьёт в точку продажи.** Ключи переживают смену интерфейса, но сервер помнит СТАРЫЙ endpoint пира. В лучшем случае roaming спасает: первый дата-пакет с нового адреса расшифровывается, сервер обновляет endpoint — wake без handshake состоялся. В худшем — за время сна протух NAT/CGNAT-маппинг, ответ не приходит, и вместо «+1 RTT» получаем **таймаут + retransmit**, то есть ХУЖЕ честного handshake. Для AWG добавляется непроверенный путь: junk/I1-обфускация завязана на handshake-фазу, переезд со старой сессией в проде не валидировался.
3. **Три ловушки реализации** (две обожгли нас в эксперименте «GRO off + batch 8», см. [HISTORY](HISTORY.md)): `Device.BatchSize()` = `max(bind, tun)` → резать надо СОГЛАСОВАННО bind и TUN, иначе `msgsPool` из 8 буферов получает срез `[:128]` → **паника при старте**; GRO при batch<128 обязан быть off; всё это — синхронные правки трёх слоёв сабмодуля.

**Что должно измениться, чтобы вернуть вопрос:** пороги сна станут заметно короче `RekeyAfterTime` (агрессивная дремота ~30s), и появится жалоба на латентность первого пакета. Тогда путь A — уже не оптимизация RAM, а UX-фича, и потребует ОБЯЗАТЕЛЬНО: (а) проверку возраста сессии (моложе 120s → keys-safe wake, иначе сразу handshake), (б) fallback по таймауту на протухший NAT-маппинг. Это +2 состояния к машине сна — цена, оправданная только измеренной жалобой. См. [wg-bindupdate-keys-safe].

## Известные согласованные издержки (документировано, не баги)

- **Legacy least_test probe-флап.** Активная least_test-группа пробует ВСЕХ членов каждый `interval` (апстрим-семантика, не трогаем); каждый probe будит усыплённого недостижимого члена (handshake), тик усыпляет обратно через ~порог. При N членах = N handshake'ов за interval, пока группа активна. Экономия recv-буферов между пробами остаётся; радио-цена растёт с N и частотой. Смягчения конфигом: **`passive_check: true`** (SPEC 019 — пока выбранный узел пассивно жив, циклы проб пропускаются целиком: probe-флап при здоровом трафике исчезает), `interval` 15m вместо дефолтных 3m для WG-групп на mobile (см. ТЗ LxBox), либо пул-режим (`round_robin`, пробует только пул).
- **`pool_tolerance > 0` пробует всех.** Отбор «лучших по delay» обязан мерить всех кандидатов каждый interval → будит всех спящих вне пула. Это цена самого режима; на mobile предпочитать `pool_tolerance: 0` (first-live) или длинный `interval`.
- **MASQUE + пробы.** MASQUE-outbound самоуправляет своим idle (stateless idle 5m, SPEC 021) и не участвует в тике SPEC 020; urltest-проба каждые `interval < 5m` не даёт его туннелю уснуть. На mobile ставить `interval` группы > masque idle_timeout.
