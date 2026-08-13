# SPEC: 064 — SELECTOR_INTERRUPT_DEAD_ON_INBOUND

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный дефект, ровесник самой фичи interrupt (c320be75a, 2023-09-15, v1.10.0) |
| Статус | I (implemented) — воспроизведён тестом на живом `Selector`, устранён, регресс закреплён; **девайс-верификация открыта** |

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

## 2. Решение

В ветке B передавать `s` вместо `selected` — ровно как это делает `urltest`:

```go
s.connection.NewConnection(ctx, s, conn, metadata, onClose)
s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
```

Адресат трафика не меняется: `Selector.DialContext` внутри сам диалит через выбранный узел (`selector.go:150`), просто по пути кладёт сокет в свой `interruptGroup`.

### 2.1 Ветку A трогать нельзя

Ветка `ConnectionHandler` (`selector.go:168`) остаётся как есть. В неё попадают только три вида аутбаундов — проверено полным перебором реализаций интерфейса:

| Аутбаунд | Почему нельзя переводить на `DialContext` |
|---|---|
| вложенный `selector` | должен получить `NewConnection`, чтобы отработать собственную логику |
| вложенный `urltest` | там своя регистрация в собственном `interruptGroup` (`urltest.go:277`) |
| `protocol/dns` | не диалит наружу вообще: обслуживает соединение в цикле, а его `DialContext`/`ListenPacket` возвращают `os.ErrInvalid` (`dns/outbound.go:42`) |

Перевод любого из них на диал сломает поведение — для DNS это прямая поломка резолвинга.

Следствие: при вложенных группах разрыв по-прежнему обеспечивает **внутренняя** группа, а не внешний селектор. Для `urltest` внутри селектора это работает; для селектора внутри селектора внутренний разорвёт своё, внешний — своё. Полное покрытие вложенных цепочек выходит за рамки хотфикса.

---

## 3. Верификация

Регрессионный тест: `protocol/group/interrupt_selector_lx_test.go`.

Поднимается настоящий `Selector` над мок-узлами с настоящим `route.ConnectionManager` в контексте; `interrupt_exist_connections: true`.

| Тест | До фикса | После |
|---|---|---|
| `TestLxSelectorInterruptViaNewConnection` — соединение пришло как из inbound, затем `SelectOutbound` | **FAIL** — соединение живо | PASS — разорвано |
| `TestLxSelectorInterruptViaDialContext` — селектор как диалер (контроль: этот путь был исправен) | PASS | PASS |
| `TestLxSelectorHandlerPathSanity` — контроль окружения: узел сдиалил, сокет жив до переключения | PASS | PASS |

Контрольный тест обязателен: без него FAIL основного мог быть артефактом нерабочего стенда.

### 3.1 Открыто

- **Девайс-верификация.** Тест использует `net.Pipe` и мок-узлы. Полевая проверка на реальных TCP-сокетах и живых протоколах не проводилась.
- **UDP-путь** покрыт фиксом по симметрии, но тестом не закреплён.
- **LxBox.** После фикса ядро рвёт соединения само; клиентский обход через трекер становится дублирующим. Снимать его следует только после девайс-прогона — иначе риск двойного разрыва или регресса.
