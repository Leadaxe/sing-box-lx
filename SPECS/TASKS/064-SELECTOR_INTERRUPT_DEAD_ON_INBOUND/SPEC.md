# SPEC: 064 — SELECTOR_INTERRUPT_DEAD_ON_INBOUND

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный дефект, ровесник самой фичи interrupt (c320be75a, 2023-09-15, v1.10.0) |
| Статус | I (implemented, v2) — v1 field-verified на `rc.7` (2026-08-13), затем вытеснен v2 (обёртка входящего, покрывает и вложенные группы); **полевой прогон v2 открыт** |

`interrupt_exist_connections` у `selector` не работает для пользовательского трафика. Переключение узла внутри группы не разрывает активные соединения — они продолжают жить на старом узле до собственного таймаута. Помогает только полный перезапуск ядра.

Scope: **client + core**, все платформы. Build-tag: нет (базовый код групп).

---

## 1. Проблема

Жалоба (2026-08-13, десктопный лаунчер и LxBox): при смене узла внутри группы трафик продолжает идти через прежний узел. Чтобы переключение вступило в силу, приходится делать Stop/Start.

Опция `interrupt_exist_connections: true` при этом выставлена — и в шаблоне, и в доехавшем до ядра конфиге. То есть настройка была корректной, а поведения не давала.

### 1.1 Что должна делать опция

`Selector.SelectOutbound` при смене узла вызывает разрыв накопленных соединений группы:

```go
// protocol/group/selector.go:142
s.interruptGroup.Interrupt(s.interruptExternalConnections)
```

`interrupt.Group` закрывает то, что лежит в его списке `connections` (`common/interrupt/group.go:39`). Флаг `interruptExternalConnections` решает лишь, трогать ли соединения, помеченные external — то есть пользовательские. При `true` должны рваться все.

### 1.2 Корень: список, по которому идёт разрыв, всегда пуст

Пополняется список ровно в двух местах — и оба находятся в методах селектора как **диалера**:

```go
// selector.go:154 (DialContext)
return s.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
// selector.go:162 (ListenPacket)
return s.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
```

Других точек регистрации в кодовой базе нет (проверено grep по всему продакшн-коду: пакет `interrupt` не используется ни в `route/`, ни в `adapter/`, ни в `common/dialer/`).

Но трафик из inbound до `Selector.DialContext` не доходит. Роутер, выбрав аутбаунд, предпочитает интерфейс `ConnectionHandler`, если тот реализован:

```go
// route/route.go:175
if outboundHandler, isHandler := selectedOutbound.(adapter.ConnectionHandler); isHandler {
	outboundHandler.NewConnection(ctx, conn, metadata, onClose)
} else {
	r.connection.NewConnection(ctx, selectedOutbound, conn, metadata, onClose)
}
```

`*Selector` его реализует (`selector.go:28`), поэтому для селектора **всегда** берётся первая ветка → `Selector.NewConnection`. А там соединение передаётся дальше мимо самого селектора:

```go
// selector.go:165-173
func (s *Selector) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	selected := s.selected.Load()
	if outboundHandler, isHandler := selected.(adapter.ConnectionHandler); isHandler {
		outboundHandler.NewConnection(ctx, conn, metadata, onClose)   // ветка A
	} else {
		s.connection.NewConnection(ctx, selected, conn, metadata, onClose)  // ветка B ← корень
	}
}
```

В ветке B первым аргументом-диалером уходит **`selected`**, а не `s`. `ConnectionManager` дёргает диал именно у переданного:

```go
// route/conn.go:95, 105
func (m *ConnectionManager) NewConnection(ctx context.Context, this N.Dialer, conn net.Conn, ...) {
	…
	remoteConn, err = this.DialContext(ctx, N.NetworkTCP, metadata.Destination)
```

`this == selected` → вызывается `DialContext` конечного узла, **минуя `Selector.DialContext`**. Сокет создан, но в `interruptGroup` селектора не попал.

Итог цепочки: список пуст → `Interrupt` обходит пустоту → ни одно соединение не рвётся. Опция мертва.

Симметрично для UDP: `route.go:309` → `Selector.NewPacketConnection` (`selector.go:181`) с той же передачей `selected`.

### 1.3 Почему у `urltest` тот же механизм работает

Различие — одно слово. `URLTest.NewConnection` передаёт **себя**:

```go
// protocol/group/urltest.go:320-323
func (s *URLTest) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)   // ← s, не selected
}
```

`this == s` → `URLTest.DialContext` → регистрация на `urltest.go:277` состоялась. Поэтому авто-переключение узла в urltest соединения рвёт, а ручное переключение в selector — нет.

Это же объясняет, почему дефект жил незамеченным: у групп с автовыбором наблюдаемое поведение корректное.

### 1.4 Почему это не заметили раньше

Опция появилась в `c320be75a` (2023-09-15, вошла в v1.10.0) и уже в том коммите содержала обход:

```go
+	ctx = interrupt.ContextWithIsExternalConnection(ctx)
 	return s.selected.NewConnection(ctx, conn, metadata)
```

То есть регистрации в `interruptGroup` для inbound-пути не было **никогда**. Дефект присутствует и в актуальном `upstream/stable` — проверено сверкой (`git show upstream/stable:protocol/group/selector.go`), форк здесь от апстрима не отклонялся.

Маскирующие факторы:
- без разрыва соединения всё равно постепенно переезжают (keep-alive истекает, приложение переподключается) — разница заметна только при внимательном наблюдении;
- у `urltest` поведение корректное, а это самый ходовой тип группы;
- клиенты компенсировали на своём уровне: LxBox реализовал разрыв через трекер соединений (`CloseConnection` → `connTracker.Close`, `common/trafficcontrol/tracker.go:180`), и симптом у пользователей был скрыт.

### 1.5 Что причиной **не** является

Проверено и отброшено — зафиксировано, чтобы версии не возвращались:

- **Не отсутствие флага в конфиге.** `interrupt_exist_connections: true` присутствовал во всех группах, включая пришедшие из подписки.
- **Не семантика `isExternal`.** Гипотеза «external-соединения не рвутся при `false`» верна по коду (`group.go:44`), но к делу не относится: список пуст при любом значении флага.
- **Не трекер соединений.** Профайлер LxBox видит полную цепочку узлов, но берёт её из `trafficcontrol.Manager` — независимого списка, наполняемого роутером **до** передачи в аутбаунд (`route.go:172`). `Chain` там не записывается при проходе, а **вычисляется** обходом групп по `Now()` (`tracker.go:106-121`). Наличие узлов в профайлере не говорит ничего о содержимом `interruptGroup`.
- **Не вложенность групп.** Воспроизводится на прямом селекторе над обычными узлами.

---

## 2. Решение (v2 — обёртка входящего соединения)

Регистрировать в `interruptGroup` не исходящий сокет, а **входящее** соединение — до развилки веток:

```go
// Selector.NewConnection / NewPacketConnection
conn = s.interruptGroup.NewConn(conn, true)              // TCP
conn = s.interruptGroup.NewSingPacketConn(conn, true)    // UDP (N.PacketConn)
```

Разрыв входящей стороны валит копирующий цикл `ConnectionManager`, тот закрывает исходящий сокет — соединение умирает целиком. `isExternal=true` честен: входящий трафик — пользовательский по определению, при `interrupt_exist_connections: false` он переживает переключение, как задумано семантикой опции.

Порт: `SingPacketConn` + `NewSingPacketConn` в `common/interrupt` (обёртка над `N.PacketConn` — штатный `NewPacketConn` принимает только `net.PacketConn`).

Подход взят из апстримного [PR #4285](https://github.com/SagerNet/sing-box/pull/4285) (xxspa). Сам PR в апстриме завис: его ветка отстала от `testing`, дифф раздут до +69922/−7087 в 300+ файлах, и месяц висит без реакции. Баг там же заведён дважды — [#4281](https://github.com/SagerNet/sing-box/issues/4281) (полный разбор с тем же корнем и минимальным репро) и [#2625](https://github.com/SagerNet/sing-box/issues/2625) (с 1.11.3). Контрибуция от нас невозможна.

### 2.1 История: v1 (`selected` → `s`) — вытеснен

Первый фикс (`bbbf186df`, ушёл в `rc.7`) передавал `s` вместо `selected` в ветке B, заводя исходящий сокет через `Selector.DialContext` с его регистрацией. Работал, но покрывал только ветку B: в ветке A (`selected` сам `ConnectionHandler` — вложенный `selector`/`urltest`, `protocol/dns`) соединение уходило хендлеру напрямую и регистрации не получало. Перевести ветку A на диал нельзя — `protocol/dns` не диалит вообще (`DialContext` → `os.ErrInvalid`, `dns/outbound.go:42`).

v2 ставит обёртку до развилки и покрывает обе ветки, а ветку B возвращает к апстримной форме (`selected`) — меньше дрейф на мержах.

---

## 3. Верификация

Регрессионный тест: `protocol/group/interrupt_selector_lx_test.go`.

Поднимается настоящий `Selector` над мок-узлами с настоящим `route.ConnectionManager` в контексте; `interrupt_exist_connections: true`.

| Тест | Без фикса | v1 | v2 |
|---|---|---|---|
| `TestLxSelectorInterruptViaNewConnection` — соединение из inbound, ветка B, затем `SelectOutbound` | **FAIL** | PASS | PASS |
| `TestLxSelectorInterruptHandlerBranch` — `selected` сам `ConnectionHandler` (ветка A) | FAIL | **FAIL** | PASS |
| `TestLxSelectorInterruptViaDialContext` — селектор как диалер (путь был исправен всегда) | PASS | PASS | PASS |
| `TestLxSelectorHandlerPathSanity` — контроль окружения | PASS | PASS | PASS |

Red-check прогнан в обе стороны: handler-branch-тест на v1 падает, на v2 проходит — тест действительно различает подходы, а не проходит от любой правки.

### 3.1 Полевая проверка

- **v1, `v1.14.0-lx.25-rc.7`, 2026-08-13 — пройдена.** Переключение узла вступает в силу мгновенно, Stop/Start не нужен. Симптом устранён.
- **v2 — открыта.** Механизм разрыва другой (рвётся входящая сторона, а не исходящий сокет); симптом тот же, но полевой прогон нужен заново на следующем rc.

### 3.2 Открыто

- **Полевая проверка v2** (см. выше).
- **UDP-путь** покрыт обёрткой `NewSingPacketConn`, но отдельным тестом не закреплён.
- **LxBox.** Ядро теперь рвёт соединения само, и клиентский обход через трекер
  (`CloseConnection` → `connTracker.Close`) стал дублирующим. Снятие обхода — отдельная
  задача на стороне клиента; до неё разрыв происходит с двух сторон.
