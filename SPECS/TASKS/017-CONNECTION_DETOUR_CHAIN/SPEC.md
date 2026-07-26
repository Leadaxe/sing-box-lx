# SPEC 017 — Connection: detour-хвост outbound'а отдельным полем `detourList`

**Фича:** [OBSERVABILITY](../../FEATURES/OBSERVABILITY/FEATURE.md)

**Тип:** lx-фича (расширение connection-метаданных)
**Статус:** C (complete) — реализовано и отгружено
**Приоритет:** Medium (UI: показать реальный физический путь пакета)
**Файлы ядра:** `common/trafficcontrol/tracker.go`, `daemon/started_service.go`,
`daemon/started_service.proto`, `experimental/libbox/command_types.go`
**Связано:** SPEC 014 (Clash→CommandClient), SPEC 015 (RPC extensions), SPEC 016
(connections map race)

## Задача

`chain` соединения отдаёт цепочку **маршрутизации** (`selector → … → выбранный
outbound`), но НЕ разворачивает `detour`-хвост самого outbound. Реальный путь
пакета:

```
правило → vpn-1(selector) → [BL]-3(node) → WARP(detour) → интернет
          └──────── chain отдаёт досюда ────────┘ └─ НЕ в chain ─┘
```

Для UI нужен полный физический путь. Это надо вывести клиенту (LxBox), не ломая
существующую семантику `chain` и Clash-API.

## Корень: это дизайн upstream, не баг

Цикл сборки цепочки в `newTrackerMetadata` (`common/trafficcontrol/tracker.go:84-97`)
разворачивает цепочку **только через `OutboundGroup`** и обрывается на первом
не-группе:

```go
for {
    detour, loaded := m.outbound.Outbound(next)
    if !loaded { break }
    chain = append(chain, next)
    outbound = detour.Tag()
    outboundType = detour.Type()
    outboundGroup, isGroup := detour.(adapter.OutboundGroup)
    if !isGroup { break }          // ← финальный outbound; detour НЕ читается
    next = outboundGroup.Now()
}
```

`Now()`/`All()` — методы интерфейса `OutboundGroup` (`adapter/experimental.go:127-131`),
их нет у обычного outbound. На `isGroup == false` цикл упирается в стену: следующий
шаг брать нечем. `break` — маркер выхода за пределы группового интерфейса, а не
«данные есть, но опущены».

`chain` по дизайну отвечает на вопрос «как роутинг выбрал финальный outbound»
(динамика групп, Clash-контракт), а `detour` обычного outbound — деталь транспорта
(`common/dialer/detour.go:19-21`: поле `DetourDialer`, резолвится лениво при первом
`Dial`). Это две разные оси; upstream намеренно их не смешивает. Поэтому detour-хвост
не «забыли» — он вне модели `chain`.

## Данные в ядре уже есть

На каждом `Outbound` есть `Dependencies() []string` (`adapter/outbound.go:20`). Для
обычного outbound с `detour` оно возвращает **ровно `[detour-tag]`** — это видно по
конструктору `NewAdapterWithDialerOptions` (`adapter/outbound/adapter.go:23-29`):

```go
var dependencies []string
if dialOptions.Detour != "" {
    dependencies = []string{dialOptions.Detour}
}
```

Семантика по типам узла (проверено по всем `protocol/*/outbound.go`):

| Узел | конструктор | `Dependencies()` |
|---|---|---|
| обычный outbound (vless/trojan/ss/wg/…) | `NewAdapterWithDialerOptions` | `[detour]` или пусто |
| группа (selector/urltest) | `NewAdapter(…, options.Outbounds)` | **члены группы**, не detour |
| block / dns | `NewAdapter(…, nil)` | пусто |

Вывод: `Dependencies()[0]` для **не-группы** = именно detour (ноль/один элемент,
составных транспортов нет). Для группы по `Dependencies()` ходить нельзя — там члены;
группа резолвится через `Now()`.

`Dependencies()` уже используется ядром для топологического старта
(`adapter/outbound/manager.go:107-113`), детектора циклов (`:148-153`) и AWG-detour
guard (`protocol/group/awg_selector_guard.go:51`) — это надёжный внутренний граф
detour-рёбер. Но наружу (chain/GetGroups/GetOutbounds) detour-связь не выведена.

## Почему резолв обязан быть в ядре, а не в LxBox

`detour` может вести **в группу**: `[BL]-3 --detour--> some-selector --Now()--> WARP`.
Активный узел такого хвоста — рантайм-`Now()` группы, который переключается. Чтобы
собрать путь на клиенте, LxBox склеивал бы три источника (`chain` соединения +
`Dependencies` из GetOutbounds + `Now()` из GetGroups), а между этими вызовами группа
может переключиться → клиент покажет путь, которого не было (distributed-state
reassembly).

`newTrackerMetadata` держит **согласованный снимок**: `Now()` всех групп берётся в один
момент создания трекера. Поэтому detour-хвост резолвится там же, атомарно. Стоимость —
разовая (при создании соединения), не на каждый тик подписки.

## Решение: отдельное поле `Detour`, `Chain` нетронут

`Chain` остаётся байт-в-байт upstream (маршрут: группы + финальный outbound).
Новое поле `Detour []string` несёт транспортный хвост финального outbound, развёрнутый
тем же правилом, что и основной цикл (группа → `Now()`, иначе → detour), со сторожем
от циклов.

```
Chain  = ["[BL]-3", "vpn-2", "vpn-1"]   // маршрут — нетронут (N селекторов + node)
Detour = ["WARP"]                        // транспортный хвост — отдельная ось
```

`Detour` — массив: detour может быть цепочкой (`node → WARP → …`) и/или содержать
группу с её активным узлом. На практике 1, реже 2 звена.

### Точки правки

1. **`common/trafficcontrol/tracker.go`** — поле `Detour []string` в `TrackerMetadata`;
   в `newTrackerMetadata` после выхода из upstream-цикла развернуть detour-хвост
   финального outbound (lx-блок, `chain`/`outbound`/`outboundType` не трогаем).

2. **`daemon/started_service.proto`** — `repeated string detourList = 23;` в
   `message Connection` (additive, после `processInfo = 22`); реген `make proto`.

3. **`daemon/started_service.go`** — `DetourList: metadata.Detour` в `connectionToProto`
   (рядом с `ChainList: metadata.Chain`, ~`:964`).

4. **`experimental/libbox/command_types.go`** — поле `detourList []string` в `Connection`,
   геттер `Detour() StringIterator`, маппинг из gRPC `conn.DetourList`.

5. **LxBox** — читать `connection.Detour()` (отдельно от `Chain()`). Клиент ничего не
   склеивает.

Clash-API (`experimental/clashapi/connections.go`) и upstream-цикл — **не трогаем**.

### Канал

`Detour` едет в горячем потоке `SubscribeConnections` (тик ~1с,
`daemon/started_service.go:709`) рядом с `ChainList`. Но: резолв считается один раз при
создании соединения (читается из готового `TrackerMetadata` на тик), а по проводу это
+1 короткий список тегов (обычно 1 элемент) на соединение. Второго канала/подписки не
требует — в отличие от варианта «`Dependencies` в GetOutbounds + живой граф групп».

## Алгоритм detour-walk (эскиз)

После upstream-цикла `detour` указывает на финальный не-групповой outbound:

```go
// lx: развернуть detour-хвост финального outbound отдельным полем
var detourChain []string
cur := detour                       // финальный outbound из upstream-цикла
seen := map[string]bool{next: true} // next = тег финального outbound
for {
    deps := cur.Dependencies()
    if len(deps) == 0 || seen[deps[0]] { break }
    step, ok := m.outbound.Outbound(deps[0])
    if !ok { break }
    detourChain = append(detourChain, deps[0])
    seen[deps[0]] = true
    // detour может вести в группу — спуститься к активному узлу
    if g, isGroup := step.(adapter.OutboundGroup); isGroup {
        now := g.Now()
        if now == "" || seen[now] { break }
        nb, ok := m.outbound.Outbound(now)
        if !ok { break }
        detourChain = append(detourChain, now)
        seen[now] = true
        cur = nb
        continue
    }
    cur = step
}
// Detour: detourChain   // порядок node→наружу
```

**Порядок элементов (зафиксировано в реализации):** `Detour` идёт от финального
outbound наружу (`["WARP", …]`), `Chain` после `common.Reverse` идёт от финального
outbound к правилу (`["[BL]-3", "vpn-2", "vpn-1"]`). Оба начинаются с финального
outbound-узла: `Chain[0]` = `[BL]-3`, `Detour[0]` = его непосредственный detour `WARP`.
Полный физический путь от ноды наружу для LxBox = `Chain[0]` ⊕ `Detour`. `Chain` сам
по себе не реверсится под `Detour` — семантика `Chain` остаётся upstream.

## Критерии готовности

- `Chain` идентичен upstream (Clash-API `/connections` и gRPC `ChainList` не изменились).
- `Detour` содержит detour-хвост: для `node → WARP` = `["WARP"]`; для detour-в-группу —
  тег группы + её активный `Now()`.
- detour-цикл (теоретический `a→b→a`) не зацикливает (seen-guard).
- `block`/`dns`/без-detour outbound → `Detour` пустой.
- proto additive: старые клиенты, не знающие поля 23, продолжают работать.
- gofmt чист на всех тронутых lx-файлах.

## Референс клиентской стороны

LxBox: добавить чтение `connection.Detour()` в connection-модель и отрисовку
полного пути рядом с `chains`.
