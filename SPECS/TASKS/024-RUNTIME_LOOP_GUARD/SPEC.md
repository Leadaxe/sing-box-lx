# SPEC 024 — Guard от runtime-колец в detour/selector-цепочках

**Фича:** [RESEARCH](../../FEATURES/011-RESEARCH/FEATURE.md)

> **Design record, не задание на реализацию.** Ядро sing-box-lx по этой спеке
> **не трогаем**. Документ фиксирует: (1) как ядро реально ведёт себя при кольце в
> графе outbound'ов, (2) полную событийную модель ядерного guard'а, которую мы
> проработали, (3) доказательство (адверсарской проверкой), что даже корректный
> ядерный guard **негерметичен** без покрытия ещё 6+ путей мутации, и (4) итоговое
> решение — защита на уровне UI (LxBox).
>
> Статус: **DEFERRED** (в ядре отложено, решение владельца 2026-07-06).
> Защита реализуется на уровне UI: LxBox валидирует выбор до вызова
> `SelectOutbound` через `CommandClient`.
> Целевая база: lx-1.14.
> Связанные памятки: [[spec024-loop-guard-app-level]],
> [[lx-commandclient-extensions-spec014]], [[lx-spec017-connection-detour]],
> [[spec020-idle-tick-misses-endpoints]], [[badjson-empty-slice-collapses-to-nil]].

| Поле | Значение |
|---|---|
| Тип | F (feature) — защитный guard |
| Статус | **DEFERRED** — дизайн проработан и верифицирован, в ядре не реализуется; защита уходит на UI |
| Ветка | *(нет — код в ядро не пишется)* |
| Файлы | *(нет — ядро не трогаем; при возврате в ядро зона касания перечислена в §8)* |
| Связано | SPEC 014 (CommandClient), SPEC 017 (Connection.Detour), SPEC 019 (urltest balancer), SPEC 020 (idle-suspend reachability) |

---

## 1. Проблема

Ядро трактует outbound'ы как ориентированный граф; рёбра берёт из
`Dependencies() []string` (`adapter/outbound.go:20`). Обычный outbound → его
`detour` (одно ребро), selector → **все** свои члены (`options.Outbounds`,
`protocol/group/selector.go:50`).

**Статическое кольцо ловится на старте.** `startOutbounds`
(`adapter/outbound/manager.go:96`) делает топосортировку по Кану: узел стартует
только когда все его `Dependencies()` запущены. Если проход не продвинулся —
`lintOutbound` (`adapter/outbound/manager.go:147`) идёт по незапущенным
зависимостям и на повторном теге в пути возвращает:

```
circular outbound dependency: sel1 -> sel2 -> sel1   (manager.go:153)
```

`box.Start()` → ошибка, инстанс не поднимается. Это покрывает и статический
`detour`, и кольцо со звеном-selector (selector объявляет всех членов
зависимостями, цикл виден независимо от `default`).

**Рантайм-кольцо НЕ ловится нигде.** `SelectOutbound`
(`protocol/group/selector.go:125`) применяет выбор безусловно — перед
`s.selected.Store(detour)` нет ни проверки достижимости самого себя, ни счётчика
хопов. Старторный детект в рантайме повторно не гоняется. Сценарий:

1. валидный (ацикличный) старт;
2. через Clash API / `CommandClient` селекторы переключаются так, что активные
   рёбра замыкаются (`sel1.Now → vpn(detour=sel2)`, `sel2.Now → sel1`);
3. следующий dial: `DialContext → DialContext → …` без терминатора;
4. **`fatal error: stack overflow`** — Go runtime-фатал, **не ловится
   `recover()`**, роняет весь процесс (всё ядро, не одно соединение).

`Selector.DialContext` (`protocol/group/selector.go:161`) просто форвардит в
выбранный узел без ограничения глубины. Единственный `visited`-guard в форке —
в трекере detour-chain (SPEC 017, `common/trafficcontrol/tracker.go:122`) — это
метаданные, не dial-путь; петлю в dial он не предотвращает.

## 2. Что реализуем (модель) — и почему в ядре отложено

**Модель (событийный guard).** Инвариант: активный граф маршрутизации ацикличен.
Активное ребро — то, по которому реально пойдёт dial *сейчас*: selector → `Now()`,
urltest(round_robin) → `ActiveTags()` (весь пул), urltest(legacy) →
`selectedOutboundTCP/UDP`, обычный outbound → статический `detour`.

Лемма: цикл не появляется «сам» — его достраивает **одно** событие мутации
(замыкающее ребро). Если каждое событие перед коммитом проверяет «не достигаю ли
я сам себя вниз по активным рёбрам» (`reachesDown(candidate, selfTag)`) —
замыкающее ребро не коммитится никогда. Обход **вниз** от кандидата; обход вверх
(`ConsumersOf`) избыточен — замыкатель ловит петлю раньше жертвы. Dial-путь не
трогается вовсе.

**Почему отложено (решение владельца 2026-07-06).** Адверсарская проверка (§6)
показала: даже корректный ядерный guard **негерметичен** без значительной
дополнительной работы — он покрывает прямые записи полей выбора, но **пропускает**
endpoint-manager, `Remove`, clash-mode, history-side-channels, и — критично —
доказуемо ломается на расхождении pointer-графа (по которому идёт dial) и
tag-графа (который обходит guard) после рантайм-`Create`. Закрыть всё это в ядре =
широкая дельта к upstream (переписать selector/urltest на tag-resolve, guard в
двух менеджерах, generation-aware DetourDialer, конвертация полей в атомики) при
том, что в практике LxBox кольцо создаёт только выбор, который инициирует само
приложение. Поэтому защита ставится там, где создаётся ребро — в UI.

## 3. Как ядро ведёт себя сегодня (справочная база)

| Фаза | Триггер | Защита в ядре | Поведение |
|---|---|---|---|
| Старт | Статическое кольцо `detour` | ✅ топосорт | `box.Start()` → `circular outbound dependency`, старт отклонён |
| Старт | Кольцо со звеном-`selector` | ✅ (selector = все члены в `Dependencies()`) | То же |
| Рантайм-dial | Ацикличный detour | ⚪ не нужна | Штатно, рекурсия конечна |
| Рантайм-API | Кольцо через `SelectOutbound` после старта | ❌ **нет** | Валидный старт → dial → `fatal stack overflow`, падение процесса |

## 4. Событийная модель E1–E5 (ядерный дизайн, для справки)

`reachesDown(from, targetTag)` — DFS по активным рёбрам от `from`, теги
резолвятся live через `OutboundManager.Outbound()`, с `visited`-множеством;
возвращает true, если встречен `targetTag`. Диспетчеризация — как в
`walkReachable` (`route/reachability_lx.go:115`): интерфейс `ActiveTags()` →
`adapter.OutboundGroup` (`Now()`) → `Dependencies()`. Порядок важен: urltest
реализует `OutboundGroup` тоже, и падение группы в `Dependencies()` дало бы
ложные срабатывания на ромбах.

| # | Событие | Коммит ребра | Guard | Реакция на петлю |
|---|---|---|---|---|
| E1 | `Selector.SelectOutbound(X)` | `s.selected.Store` (`selector.go:143`) | `reachesDown(X, self)` до Store | `return false` + **warn в лог** |
| E2 | `Selector.Start` cache-restore | `s.selected.Store` (`selector.go:93`, в обход E1) | тот же чек перед Store | откат на `default`/`tags[0]` + warn |
| E3 | urltest health-check → `setSlots` | `setSlots` (`urltest_balance_lx.go:146`) | из плана-occupancy выкинуть петлевые теги (I/O — вне лока) | тег не входит в пул + warn (с анти-спам dedup) |
| E4 | urltest legacy `performUpdateCheck` | `selectedOutboundTCP/UDP` (`urltest.go:552/558`) | тот же чек перед присваиванием | оставить прежний + warn |
| E5 | `Manager.Create` (в т.ч. replace) | статическое ребро, никого не зовёт | `lintOutbound`-стиль: `reachesDown(dep, newTag)` по каждой зависимости | `Create` → error, нода не добавляется |

**«Ошибки выбора кидаем в лог»** (требование владельца): на отклонённом E1/E2/E4
пишем `warn` и оставляем прежнее валидное состояние; на E3 петлевой тег молча не
входит в пул с warn-дедупом (health-check периодичен, иначе спам каждый тик); на
E5 — error из `Create` (внешний вызывающий видит его как HTTP 400 / gRPC
InvalidArgument).

## 5. Гонка и её решение (topologyMu) — верифицировано

**Гонка (TOCTOU).** Два события в разных группах проверяют каждый свой снимок,
оба видят «ацикличино», оба коммитят, вместе замыкают. Пример: параллельно
`sel1.Select(vpnA→sel2)` и `sel2.Select(vpnB→sel1)` — обе проверки проходят, оба
Store, кольцо.

**Решение.** Один мьютекс топологии `topologyMu` на Manager, удерживаемый через
всю пару **[walk + commit]** каждого события E1–E5. Коммиты линеаризуются; walk
события k видит коммит k-1. Индукция: граф ацикличен до коммита k → walk k видит
истинный активный граф → замыкающее ребро всегда детектится. Dial-путь
`topologyMu` **не берёт никогда** → ноль на соединениях.

**Отвергнутые альтернативы:** оптимистичный (generation + откат) — между коммитом
и откатом кольцо реально существует, dial в окне падает; per-group локи — кольцо
межгрупповое, локальные локи не линеаризуют кросс-групповые коммиты.

**Deadlock-вердикт: HOLDS** (адверсарий не смог построить инверсию) при трёх
условиях, подтверждённых лок-инвентарём:
1. Порядок `topologyMu` внешний, `{m.access, b.access, g.access, endpointMgr,
   resumeMu}` — внутренние листья; ни из-под одного внутреннего лока не достижим
   `topologyMu`.
2. `onChange` после `setSlots` остаётся lock-free (сейчас — атомик-стор,
   `urltest_balance_lx.go:152-157`); никогда не должен звать route-локи.
3. Сетевой I/O URL-тестов остаётся **вне** `topologyMu`: под локом только
   [фильтр плана + `setSlots`]. `balancePoolFirstLive`
   (`urltest.go:691`) — единственное место, требующее разбиения на фазы (I/O
   `:604/:647` перемешан с построением плана); переиспользовать чистый планировщик
   `planFirstLivePool` (`urltest_balance_lx.go:245`).
4. Существующую инверсию починить: PostStart держит `g.access` через
   `seedPool→setSlots` (`urltest.go:360-366`) — `topologyMu` брать в PostStart
   **до** `g.access`, не на слое `setSlots`.

Посткоммит-побочки (`cacheFile.StoreSelected` — bbolt disk write `selector.go:148`;
`Interrupt`; `NotifyUpdated`) вынести **после** релиза `topologyMu`.

## 6. Почему даже полный ядерный guard негерметичен (адверсарский вердикт: BREAKS)

Три атакующих агента (deadlock / TOCTOU / false-positives). **False-positives:
HOLDS** — ни одна легитимная конфигурация не отклоняется ложно (ромбы, shared
chains, over-approximation пула — все корректны; ловушка реализации: `reachesDown`
живёт в `adapter/outbound`, не может type-assert-ить `*group.Selector` из-за
import cycle → только интерфейс-диспетчеризация). **Deadlock: HOLDS** при фиксах
§5. Но **TOCTOU: BREAKS** по двум независимым осям:

**(A) Дыра покрытия — множество E1–E5 НЕ полно.** Вне набора (verified):
1. **endpoint Manager.Create/Remove** (`adapter/endpoint/manager.go:120/97`) —
   WG/AWG-эндпоинты hot-replace'ятся с теми же replace-семантиками, несут
   `Dependencies()`, `Outbound()` проваливается в `m.endpoint.Get`
   (`manager.go:208`) → эндпоинты равноправные узлы графа, но E5 их не покрывает.
   **Критично:** round_robin-пул резолвит теги live per-dial → кольцо через
   эндпоинт исполнится без staleness. Фикс: E5 дословно на endpoint-manager +
   регистрация endpoint-deps в `dependByTag`.
2. **outbound Manager.Remove** — переназначает `m.defaultOutbound` на
   `outbounds[0]` (`manager.go:249-254`) до проверки `dependByTag`; отклонённый
   Remove всё равно размапливает тег → walk под-аппроксимирует. Фикс: проверять
   `dependByTag` первым, мутировать после.
3. **clash-mode switch** (`clashapi/server.go:218`) — меняет rule→outbound ребро
   (роутеры не узлы цикла, риск низкий).
4. **URL-test history side-channels** (`clashapi/proxies.go`, dial-error deletes
   `urltest.go:243/269`) — в cold-start окне (пул пуст, `selectedOutbound*` nil)
   эффективное ребро пересчитывается per-dial из history **без события Store**.
   Фикс: моделировать группу с nil/пустым committed-ребром как имеющую активные
   рёбра ко **всем** членам (безопасная над-аппроксимация в cold-окне).

**(B) Дыра soundness — pointer-граф ≠ tag-граф.** Race-claim доказывает
ацикличность **tag**-графа, но dial идёт по **pointer**-графу
(`selector.selected` / замороженные urltest-слайсы / `sync.Once` DetourDialer'ы),
который расходится с tag-графом после любого `Create`-replace. Verified staleness
(§ниже): после replace три держателя устаревают —
(1) `DetourDialer` (`common/dialer/detour.go:52-77`) резолвит тег один раз лениво
и кэширует указатель И ошибку навсегда;
(2) `Selector` (`selector.go:77-84`) снапшотит членов в `s.outbounds` на Start,
`SelectOutbound`/`DialContext` используют только кэш;
(3) urltest legacy замораживает `[]adapter.Outbound` на Start (`urltest.go:82-91`),
причём `testNodes` (`:518`) health-чекает **новый** объект по live-тегу, пока
трафик идёт в **старый** указатель.

Адверсарий даёт конкретную двух-`Create` последовательность, где все E1–E5
проходят, live tag-граф провабельно ацикличен, а первый dial через selector
рекурсирует в stack overflow. **Индукция «walk видит текущий граф» ложна как
заявлено** — верна только для tag-рёбер. Фикс: сделать dial-граф = tag-графу
(selector/urltest legacy хранят **теги**, резолвят per-dial — паттерн уже доказан
балансировщиком SPEC 019 `urltest.go:195-201`, стоимость — один map-lookup на
соединение) + generation-aware DetourDialer.

**Единственная смягчающая деталь:** оба критических сценария требуют рантайм-вызова
`Manager.Create`, которого в дереве сейчас нет ни одного. Но само существование E5
показывает, что рантайм-`Create` — в модели угроз дизайна, значит guard обязан
работать и там, а там он ломается.

## 7. Решение — защита на уровне UI (LxBox)

Ребро создаёт выбор; выборы в LxBox инициирует приложение. Значит валидация
ставится в приложении **до** вызова `SelectOutbound` через `CommandClient`:

- перед применением выбора LxBox строит активный граф из уже отдаваемых ядром
  данных (`GetGroups`/`GetOutbounds` — SPEC 014; `Connection.Detour` — SPEC 017) и
  проверяет `reachesDown(candidate, selfTag)`;
- при петле — не вызывает `SelectOutbound`, показывает пользователю, что выбор
  создаёт кольцо;
- ядро остаётся минимальной дельтой к upstream.

**Residual-риски UI-подхода (сознательно приняты):**
1. **urltest auto-switch** — health-check меняет пул внутри ядра, приложение не в
   петле. В практике не замыкает кольцо: замыкающее ребро — это *выбор члена*,
   ведущего назад, а auto-switch выбирает по задержке среди уже
   сконфигурированных членов; если конфиг ацикличен статически (гарантирует
   старторный `lintOutbound`), auto-switch новых обратных рёбер не создаёт.
2. **cache-restore при изменённом между запусками конфиге** — ядро восстанавливает
   выбор из кеша (`selector.go:93`) без чека; если конфиг менялся, выбор мог стать
   петлёй. Смягчение: LxBox чистит/валидирует сохранённый выбор при загрузке
   профиля.
3. **Прямой Clash REST в обход LxBox** (внешний дашборд по `with_clash_api` на
   desktop — см. [[desktop-keeps-clash-api-aar-drops]]) — UI-guard не покрывает;
   на этом пути защиты нет, ядро упадёт. Приемлемо: desktop-сценарий,
   пользователь-эксперт.

## 8. Зона касания upstream, ЕСЛИ guard вернётся в ядро

Оценка для будущего решения (сейчас не применяется):

- **Новые (lx-owned):** `adapter/outbound/loop_guard_lx.go` (`reachesDown` +
  `topologyMu`-обёртки), `protocol/group/loop_guard_lx.go` (врезки E1–E4).
- **Изменённые upstream (под `lx:begin/lx:end`):** `adapter/outbound/manager.go`
  (E5 + `topologyMu` + Remove-reorder), `adapter/endpoint/manager.go` (E5 на
  эндпоинтах), `protocol/group/selector.go` (E1/E2 + tag-resolve),
  `protocol/group/urltest.go` (E3/E4 + фазовый сплит `balancePoolFirstLive` +
  поля `selectedOutbound*` → `TypedValue`), `common/dialer/detour.go`
  (generation-aware invalidation).
- **Build-tag:** `with_lx_runtime_loop_guard` + `*_stub_lx.go` (per CONSTITUTION
  §3.2), т.к. добавляется компилируемая машинерия.

Это ~6 upstream-файлов и переписывание dial-резолва в двух группах — что и есть
причина отложить в пользу UI.

## 9. Принятые решения (журнал)

1. **Guard вниз, не вверх.** `reachesDown(candidate, self)` в точке мутации; обход
   вверх через `ConsumersOf` — **отвергнут** как избыточный (замыкатель ловит
   петлю раньше жертвы).
2. **`topologyMu` линеаризует [walk+commit], не оптимистичный откат.** Откат
   отвергнут: в окне коммит↔откат кольцо реально, dial падает.
3. **Dial-путь не трогаем.** Проверка только в событиях смены ребра, не на
   соединении → ноль оверхеда на dial. Подтверждено лок-инвентарём (dial берёт
   только атомик/`b.access`/один `m.access.RLock`).
4. **Ядерная реализация ОТЛОЖЕНА, защита на UI** (2026-07-06, владелец). Причина:
   адверсарский вердикт BREAKS для ядерного guard'а без покрытия endpoint-manager,
   Remove, history-side-channels и без устранения pointer/tag-расхождения —
   широкая дельта к upstream; в LxBox кольцо создаёт только app-инициированный
   выбор.
5. **E5 static-only asymmetry.** Ядерный E5 проверял бы только активный граф;
   `Create` со статическим-только циклом (через невыбранного члена) прошёл бы в
   рантайме, но упал бы `lintOutbound` при следующем рестарте. Приемлемо
   задокументировать (в UI-подходе неактуально).

## 10. Что НЕ в scope / отложено

- Реализация в ядре — **отложена** (этот документ; при возврате — §8 как
  стартовая точка, §5–6 как список обязательных фиксов).
- Покрытие прямого Clash REST в обход LxBox — вне scope UI-подхода (§7 риск 3).
- generation-aware DetourDialer, tag-resolve в selector/urltest legacy — нужны
  только для ядерного пути, здесь не делаются.

## 11. Верификация (проведена на этапе дизайна)

Не юнит-тесты (кода нет) — а адверсарская проверка дизайна (workflow из 7 агентов,
verified против дерева на коммите `8dc7e2ff`):

- **Факты:** staleness (3 stale-держателя после replace), лок-инвентарь (плоский
  набор листовых локов, единственная инверсия — PostStart/`g.access`), полнота
  точек мутации (E1–E5 **не** полно: +endpoint-mgr, +Remove, +clash-mode,
  +history), конвенции доков.
- **Атаки:** deadlock → **HOLDS** (при фиксах §5); false-positives → **HOLDS**
  (ложных отклонений нет); TOCTOU → **BREAKS** (дыра покрытия + pointer/tag
  soundness).

Итог верификации и есть обоснование решения §9.4: ядерный guard требует существенно
больше, чем «чек в `SelectOutbound`».
