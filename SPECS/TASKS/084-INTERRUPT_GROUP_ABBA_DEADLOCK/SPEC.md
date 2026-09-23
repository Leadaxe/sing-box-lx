# SPEC: 084 — INTERRUPT_GROUP_ABBA_DEADLOCK

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — **наш регресс** поверх SPEC 064 v2: обёртка входящего соединения дала вложенным selector'ам второй порядок захвата замков; апстримный примитив `interrupt.Group` (c320be75a) закрывает соединения под своим мьютексом и был безопасен лишь потому, что у апстрима такой вложенности нет |
| Статус | D (done, device-verified) — red/green на юните (три вида обёрток) и e2e-стенде вложенных selector'ов с настоящим `ConnectionManager`, `-race`; полевой прогон на конфиге репортёра — закрыта решением владельца 2026-09-24: релизы более недели в поле без жалоб |
| Связанные | [064](../064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md) — источник inbound-обёртки; issue [#20](https://github.com/Leadaxe/sing-box-lx/issues/20) — отчёт с goroutine-дампами и верным диагнозом |

Переключение вложенного selector'а (`global-auto-out → eu-auto-out → узел`) при
живом соединении, поднятом через внешнюю группу (DoH с `detour` на
`global-auto-out`), глушит весь проксируемый трафик до рестарта ядра. Корень —
ABBA-дедлок двух `interrupt.Group`: `Interrupt` внутренней группы держит свой
замок и закрывает обёртку внешней, `Close` DoH-соединения держит внешний замок и
ждёт внутренний. После этого каждое новое соединение через любую из групп встаёт
в `NewConn` / `NewSingPacketConn` на том же замке. Фикс — в примитиве: замок
группы больше не удерживается на время чужого `Close`.

Build-tag: нет (базовый код групп). Scope: **client + core**, все платформы.
Затронуты выпуски с `v1.14.0-lx.25` (коммит v2-обёртки e3ebbbaf9, 2026-08-13).

---

## 1. Проблема

### 1.1 Симптом (issue #20)

`v1.14.0-lx.35`, OpenWrt под procd, TUN с `auto_route`/`strict_route`. Цепочка
`global-auto-out (selector) → eu-auto-out (selector) → eu-auto-out-failsafe
(urltest) → нода`; `dns-global` ходит через `detour: global-auto-out`. У обоих
selector'ов `interrupt_exist_connections: true`.

Шаг: в `eu-auto-out` выбрать конкретную ноду, затем вернуться на
`eu-auto-out-failsafe`. Проксируемый трафик перестаёт ходить и не
восстанавливается; последующие переключения тоже виснут; помогает только
рестарт. Goroutine-дампы репортёра:

| Метрика | До | После |
|---|---:|---:|
| Goroutine | 609 | 981 |
| В `sync.Mutex.Lock` со стеком в `common/interrupt` | 0 | 67 |

Из 67: 47 в `NewConn`, 11 в `NewSingPacketConn`, 6 в `Conn.Close`, 1 в
`SingPacketConn.Close`, 2 на входе в `Interrupt`. 58 ждут замок A (внешняя
группа), 9 — замок B (внутренняя). Два ключевых стека:

```text
goroutine 3178 [sync.Mutex.Lock, 3 minutes]:            ← держит B, ждёт A
interrupt.(*Conn).Close(...)                  conn.go:32
interrupt.(*Group).Interrupt(B, true)         group.go:55
group.(*Selector).SelectOutbound(...)         selector.go:142
clashapi.updateProxy(...)

goroutine 1058 [sync.Mutex.Lock, 3 minutes]:            ← держит A, ждёт B
interrupt.(*Conn).Close(...)                  conn.go:32   (внутренняя обёртка)
interrupt.(*Conn).Close(...)                  conn.go:35   (внешняя обёртка)
crypto/tls.(*Conn).Close(...)
http2.(*ClientConn).closeConn(...)            ← HTTP/2-клиент DoH закрывает соединение
```

### 1.2 Корень: два порядка вложенности обёрток

Примитив (`common/interrupt`, до фикса) закрывал нижележащее соединение **под**
мьютексом группы — и в `Interrupt`, и в трёх Close-обёртках:

```go
func (g *Group) Interrupt(interruptExternalConnections bool) {
	g.access.Lock()
	defer g.access.Unlock()
	for element := g.connections.Front(); element != nil; element = element.Next() {
		if !element.Value.isExternal || interruptExternalConnections {
			element.Value.conn.Close()          // ← чужой Close под нашим замком
			...
func (c *Conn) Close() error {
	c.group.access.Lock()
	defer c.group.access.Unlock()
	c.group.connections.Remove(c.element)
	return c.Conn.Close()                       // ← то же
}
```

Пока зарегистрированный `io.Closer` — сырой сокет, это безобидно. Но у вложенных
selector'ов зарегистрированное соединение само оказывается обёрткой **другой**
группы, причём в двух противоположных порядках:

- **Входящее из inbound.** `outer.NewConnection` оборачивает сырой conn группой
  A (SPEC 064 v2), затем — раз `selected` реализует `ConnectionHandler` — отдаёт
  его `inner.NewConnection`, который оборачивает уже `A(raw)` группой B. Итог
  `B(A(raw))`: `Close` берёт **B, затем A**. В списке B лежит запись с
  `conn = A(raw)` — её и закрывает `Interrupt(B)`.
- **Исходящее через `detour`.** `outer.DialContext → inner.DialContext → узел`;
  обёртки ставятся на обратном пути: `A(B(raw))`. `Close` берёт **A, затем B**.

Отсюда классический ABBA: `Interrupt(B)` (переключение `eu-auto-out` по Clash
API) держит B и в `A(raw).Close()` ждёт A; HTTP/2-клиент DoH, закрывая
`A(B(raw))`, держит A и ждёт B. Оба стоят навсегда, а за ними — все
`NewConn`/`NewPacketConn`/`NewSingPacketConn` на A и B, то есть каждое новое
соединение через любую из групп. UI при этом уже показывает новый тег:
`SelectOutbound` сохраняет выбор до `Interrupt`.

Окно есть и без `Interrupt`: обычный `Close` входящего (`B→A`) против `Close`
исходящего (`A→B`) — тот же ABBA, только окно узкое (два `Close` в один момент).
`Interrupt` делает столкновение почти гарантированным: замок удерживается на
время закрытия десятков соединений подряд. Симметрично, `Interrupt(A)`
(переключение внешнего selector'а, `A→B`) сталкивается с естественным `Close`
входящего (`B→A`).

### 1.3 Почему это наш регресс, а не апстримный

У апстрима inbound-сторона не оборачивается вовсе (в этом и был дефект 064:
`interrupt_exist_connections` не доставал до трафика из inbound). Единственная
вложенность обёрток у него — dial-сторона `A(B(raw))`, и все пути берут замки в
одном порядке `A→B`: `Close` внешней обёртки, `Interrupt(A)`; `Interrupt(B)`
закрывает только сырые сокеты. Один порядок → дедлока нет, и «Close под замком»
апстриму ничего не стоит. Апстримный коммит `515a73e4e` (ветка B: `s` вместо
`selected`) картину не меняет — регистрация там идёт через `DialContext`, то есть
в тот же порядок.

Обёртка входящего из 064 v2 (e3ebbbaf9, lx.25) добавила второй порядок. 064 не
виновата в том, что сделала: без неё inbound-трафик не рвётся. Виноват примитив,
который держит замок на время чужого `Close` — с любой обёрткой поверх обёртки
это ставка на порядок, который ему не принадлежит. Поэтому фикс в примитиве, а
не в selector'е.

### 1.4 Зона поражения

- **Топология:** selector, у которого `selected` — другой selector (ветка
  handler), любой глубины. Именно так собирают «регион → авто-группа → нода».
  `selector → urltest` и `chain` не затронуты: они не оборачивают входящее, и
  порядок остаётся один.
- **Триггер:** `Interrupt` внутреннего selector'а (Clash API / `SelectOutbound`
  из LxBox и лаунчера) при `interrupt_exist_connections: true` — вместе с
  закрытием любого соединения, поднятого через внешний selector: DoH с `detour`,
  `download_detour` rule-set'ов, любой outbound с `detour` на внешнюю группу.
  Плюс узкое окно «Close против Close» при любом переключении и без опции.
- **Последствие:** оба замка мертвы; весь новый TCP и UDP через эти группы стоит
  в регистрации, старые соединения живут до своего таймаута; лечит только
  рестарт ядра.
- **Обход до фикса** (репортёр угадал): `interrupt_exist_connections: false` у
  внутреннего selector'а закрывает путь через `Interrupt` — запись входящего
  помечена `isExternal`, и без флага её не трогают. Узкое окно «Close против
  Close» обход не закрывает.
- **Выпуски:** `v1.14.0-lx.25` … `lx.37`.

## 2. Решение

Инвариант: **замок группы никогда не удерживается на время нижележащего
`Close`.** Тогда порядок обёрток перестаёт иметь значение — под замком остаются
только операции со списком, а они ни в какой другой замок не ведут.

- `Group.Interrupt`: под замком пройти список, снять подходящие записи и собрать
  их `io.Closer`; отпустить замок; закрыть собранное.
- `Conn.Close` / `PacketConn.Close` / `SingPacketConn.Close`: под замком снять
  запись; отпустить; закрыть нижележащее.

Маркер `// lx: SPEC 084` на всех четырёх точках (`common/interrupt/group.go`,
`common/interrupt/conn.go`).

Что стало гонкой и почему это безопасно:

- `Interrupt` и `Close` обёртки на одной записи теперь могут идти
  одновременно. Снятие из списка защищено замком, а `list.Remove` у sing —
  no-op для уже снятого элемента (`e.list == l`). Нижележащее закрывается
  дважды — так было и до фикса (`Interrupt` закрывал, потом обёртка закрывала
  повторно), только теперь второй `Close` может прийти параллельно; контракт
  `net.Conn` допускает одновременные вызовы.
- Регистрация после `Interrupt`: соединение, сдиаленное через **старый** выбор,
  может встать в список уже после снятия. Так было и раньше — dial никогда не
  шёл под замком группы; на следующем `Interrupt` его снимут.

Семантика `isExternal`, порядок закрытия и состав списка не изменились.

## 3. Верификация

**`common/interrupt/group_lx_test.go`** — примитив, без selector'ов:

- `TestLxGroupNestedInterruptCloseNoDeadlock` — две группы, `B(A(raw))` для
  входящего и `A(B(raw))` для исходящего, для всех трёх обёрток (`Conn`,
  `PacketConn`, `SingPacketConn`). Детерминированность — «затвором»: первая
  запись B блокирует свой `Close` до сигнала, чтобы `Interrupt(B)` и `Close`
  исходящего гарантированно встретились. Red-check на коде до фикса: все три
  подтеста падают по таймауту «`Interrupt(B)` не завершился за 3 с».
- `TestLxGroupInterruptExternalOnlyOnRequest`, `TestLxGroupCloseAfterInterruptIsIdempotent`
  — семантика `isExternal` и допустимость `Close` после `Interrupt`.

**`protocol/group/interrupt_nested_deadlock_lx_test.go`** — сценарий репортёра
на настоящих `Selector`'ах и `route.ConnectionManager`: `global-auto-out →
eu-auto-out → узел`, входящее через `outer.NewConnection`, «DoH» через
`outer.DialContext`, затем `inner.SelectOutbound` одновременно с `Close`
DoH-соединения. Red-check: «`SelectOutbound` не завершился за 3 с». Green:
переключение прошло, сокет узла, входящее и DoH закрыты, новый dial после
переключения не виснет.

`go test -race` по `common/interrupt`, `protocol/group`, `protocol/chain` —
зелёные; сборка со всем `LX_TAGS` — чистая.

### 3.1 Полевая проверка — открыта

Конфиг репортёра (OpenWrt, `lx.35`): под трафиком с живым DoH переключить
`eu-auto-out` на ноду и обратно на `failsafe` несколько раз подряд. Ожидание:
трафик ходит, в goroutine-дампе ноль записей `sync.Mutex.Lock` со стеком в
`common/interrupt`.

### 3.2 Открыто

- Репортёр отдельно упоминает «ранее наблюдавшиеся таймауты прямого bootstrap»
  — этими стеками они не объясняются; отдельная тема, если повторится после
  фикса.
- Клиентам (LxBox, лаунчер) делать ничего не нужно: ядровой фикс, конфиг не
  меняется.

## 4. Границы

- Порядок обёрток в `Selector.NewConnection`/`DialContext` не трогаем: 064 v2
  остаётся как есть, фикс делает порядок безразличным.
- Потолок/дедлайн на `Close` нижележащих соединений не вводим — `Interrupt`
  по-прежнему синхронен и ждёт каждого `Close`; медленный `Close` — отдельная
  тема, не этого дефекта.
- Серверные пути и `urltest`/`chain` кодом не затронуты, но пользуются тем же
  примитивом и получают инвариант бесплатно.

## 5. Условие снятия

Фикс живёт в апстримных файлах `common/interrupt/{group,conn}.go`. Снимается в
двух случаях: (а) апстрим сам вынесет нижележащий `Close` из-под `access` —
тогда наши маркеры уходят в пользу апстримной формы; (б) уйдёт inbound-обёртка
064 v2 — тогда второго порядка нет и исходный примитив снова безопасен, но
только до следующей обёртки поверх обёртки. На каждом мерже проверять, что ни
`Interrupt`, ни один из трёх `Close` не держат `access` на время нижележащего
`Close` (встречная правка апстрима в этих функциях вернёт дедлок молча — тесты
§3 это поймают).
