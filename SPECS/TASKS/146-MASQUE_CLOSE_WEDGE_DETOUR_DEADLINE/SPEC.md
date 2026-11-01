# SPEC: 146 — MASQUE_CLOSE_WEDGE_DETOUR_DEADLINE

**Фича:** [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — `Box.Close` виснет навсегда за `masque.Outbound.Close`, если в момент остановки идёт сборка h3-туннеля через detour |
| Статус | I (implemented) — 2026-11-01: код и юниты red/green в дереве; на устройстве репортёра не прогонялось |
| Ветка | `lx` |
| Build-tag | — (ядро `protocol/masque`, собирается всегда) |
| Источник | Два пака поддержки LxBox 2026-11-01 (ядро `v1.14.4-lx.26`, Android): дамп горутин сторожа остановки §668, один и тот же стек в обоих |
| Связано | [#61](https://github.com/Leadaxe/sing-box-lx/issues/61) (следствие: `initialize cache-file: timeout` на следующем старте — второй слой [147](../147-CACHEFILE_RELEASE_TAKEOVER/SPEC.md)), [#50](https://github.com/Leadaxe/sing-box-lx/issues/50) (тот же класс отказа, репортёр называл WARP-endpoint'ы — по решению владельца считается этим же случаем), [074](../074-MASQUE_VHTTP_AUTO/SPEC.md) (в `auto` h3-нога отвязана от вызывающего ровно по этой причине — фиксированный `vhttp: h3` оставался синхронным), [108](../108-MASQUE_UNBOUNDED_WAITS/SPEC.md) (соседние ожидания без предела), [136](../136-REFERENCE_MANAGER_START_MUX_LOCK/SPEC.md) (та же семья: мьютекс поверх сетевого вызова на пути жизненного цикла) |

Остановка ядра не завершается: `Box.Close` стоит на `runMu` masque-outbound'а, который держит
dial, а dial не вернётся никогда. Приложение по потолку бросает остановку, старый Box остаётся
в процессе, `cache.db` под его блокировкой, и следующий старт падает (#61).

## 1. Картина из дампа

Конфиг: outbound `masque` с `vhttp: h3` и `detour` на selector, выбранный член — vless + WebSocket.
За несколько секунд до остановки пользователь запустил пинг этого узла (`StartedService.URLTestOutbound`),
пинг ещё шёл, когда нажали Stop. Единственная заблокированная горутина и две сопутствующие:

| Горутина | Где стоит | Что это |
|---|---|---|
| `Box.Close → Scope.Close → masque.(*Outbound).Close` | `sync.Mutex.Lock` на `runMu` | остановка ядра |
| `URLTestOutbound → urltest → masque.DialContext → ensureSession → connect → connectH3WithBudget → quic.DialEarly → Transport.doDial` | `select` в ожидании результата `conn.run()` | держатель `runMu`, пинг |
| `quic-go Transport.doDial.func1 → Transport.Close` | `chan receive` на `t.listening` | горутина `conn.run()` уже вернулась (хендшейк не прошёл или отменён) и закрывает одноразовый транспорт |
| `quic-go Transport.listen → ReadPacket → UnbindPacketConn → XUDPConn → vless.Conn → WebsocketConn.Read → utls → TCP` | `IO wait` | слушатель транспорта, живой TCP к relay |

Цепочка замыкается так:

1. `ensureSession` держит `runMu` на весь сетевой `connect` (так задумано SPEC 021: одна сборка
   туннеля на разрыв).
2. При фиксированном `vhttp: h3` `connect` зовёт `connectH3WithBudget(ctx, 0)` синхронно
   (отвязана от вызывающего только h3-нога `auto`, SPEC 074).
3. quic-go получает чужой `PacketConn` (`createdConn == false`) и **никогда его не закрывает**.
   Когда одноразовый транспорт гаснет (`conn.run()` вернулся — хендшейк не прошёл за дефолтные 5 с
   или dial отменён), `Transport.Close` делает `conn.SetReadDeadline(time.Now())` и ждёт
   `<-t.listening`, пока слушатель выйдет из `ReadPacket`.
4. Через detour «сокет» — поток: `UnbindPacketConn(XUDPConn(vless.Conn(WebsocketConn(utls))))`.
   `WebsocketConn.SetReadDeadline` возвращает `os.ErrInvalid`, остальные слои пробрасывают вызов
   как есть. Дедлайн до TCP не доходит, слушатель спит на живом сокете.
5. `Transport.Close` не возвращается → `errChan` в `doDial` не получает результат → `DialEarly`
   не возвращается (и отмена контекста не помогает: после `ctx.Done()` `doDial` всё равно ждёт
   `errChan`) → `connectH3WithBudget` не доходит до `udpConn.Close()`, который разбудил бы
   слушателя → `runMu` не отпускается → `Outbound.Close` стоит вечно.

XUDPConn и vless.Conn честно сообщают `NeedAdditionalReadDeadline() == true`; транспорты
(`v2raywebsocket.Client.upgrade`, gRPC, XHTTP) по этому признаку заворачивают conn в
`deadline.NewConn` для себя. masque признак не проверял.

Гипотеза #50 про `bindAccess` WG-endpoint'а в дампах не подтвердилась: WG в них не фигурирует.

## 2. Фикс

Две правки в `protocol/masque/outbound.go`, обе `// lx: SPEC 146`.

### 2.1 Рабочий дедлайн чтения на detour-conn

`connectH3WithBudget`: UDP-conn от `o.dialer` перед передачей в `bufio.NewUnbindPacketConn`
оборачивается в `deadline.NewConn`, если `deadline.NeedAdditionalReadDeadline(udpConn)`.
`deadline.Reader` читает всегда через фоновую горутину и просыпается по `pipeDeadline`, поэтому
`SetReadDeadline(now)` от quic-go прерывает уже идущее чтение: слушатель получает
`os.ErrDeadlineExceeded` (temporary), видит `closeErr != nil` и выходит, `Transport.Close`
возвращается, `doDial` отдаёт ошибку, `connectH3WithBudget` закрывает `udpConn`, фоновый
читатель обёртки завершается на закрытом сокете. Прямой `*net.UDPConn` признак не несёт,
горячий путь без detour не меняется.

### 2.2 `Close` не ждёт dial

`runMu` остаётся сериализатором сборки туннеля и по-прежнему держится через сеть. Состояние
(`sess`, `closed`, новый `connectCancel`) переезжает под отдельный `stateMu`, который никогда не
держится через I/O:

- `ensureSession` строит `connectCtx` от контекста вызывающего, регистрирует его отмену в
  `connectCancel`, ведёт `connect` под ним и снимает регистрацию по выходу;
- `Close` под `stateMu` ставит `closed`, забирает `sess` и `connectCancel`, отпускает замок,
  отменяет connect и сносит сессию. `runMu` не берётся;
- сессия, которая успела собраться после `Close` (гонка между успехом `connect` и установкой
  `closed`), сносится самим `ensureSession`, вызывающий получает `errOutboundClosed`;
- `teardownSession` чистит `sess` под `stateMu`.

Для dial'а это означает отмену контекста при остановке ядра: вызывающий получает ошибку, а не
туннель, что и ожидается от закрывающегося outbound'а. В `auto` отмена вызывающим по-прежнему не
считается вердиктом о ноге (SPEC 108).

Корень в quic-go (`Transport.Close` одноразового транспорта ждёт слушателя, не закрывая чужой
conn) не трогаем: пин не правим, апстриму не предлагаем (бан). Апстримные hysteria2/tuic отдают
quic-go detour-conn тем же способом и несут тот же латентный дефект — вне скоупа форка.

## 3. Проверка

- `protocol/masque/close_wedge_lx_test.go`:
  - `TestH3DialReturnsWhenDetourConnLacksReadDeadline` — продакшен-путь `connectH3WithBudget`
    на conn без дедлайнов (net.Pipe + `NeedAdditionalReadDeadline() == true`), никто не отвечает,
    контекст 300 мс: вызов обязан вернуться. Без обёртки §2.1 — висит (красный по таймауту теста).
  - `TestCloseDoesNotWaitForDialInFlight` — h3-нога заблокирована до отмены контекста;
    `Close()` возвращается за секунду, `ensureSession` получает ошибку, `sess == nil`. Со старым
    `Close` через `runMu` — дедлок (красный).
  - `TestSessionBuiltAfterCloseIsTornDown` — сессия, собранная после `Close`, сносится,
    вызывающий получает `errOutboundClosed`.
- `go test -race ./protocol/masque/` под тегами сборки — зелёный; `go vet`, gofmt.
- Устройство (LxBox): конфиг репортёра — пинг узла «WARP (MASQUE) h3» с `detour` через vless+ws,
  Stop во время пинга → `Stopped` без `stuck at closeCommandServer` в журнале, следующий Start без
  `initialize cache-file: timeout`. За репортёром.

## 4. Файлы

- `protocol/masque/outbound.go` — поля `stateMu`/`connectCancel`, `ensureSession`, `teardownSession`,
  `Close`, `connectH3WithBudget` (`// lx: SPEC 146`), `errOutboundClosed`
- `protocol/masque/lifecycle_test.go` — `runMu` → `stateMu` в проверках состояния
- `protocol/masque/close_wedge_lx_test.go` — регресс-тесты (новый)
