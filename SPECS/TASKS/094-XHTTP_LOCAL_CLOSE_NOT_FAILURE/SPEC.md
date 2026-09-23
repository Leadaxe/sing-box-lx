# SPEC: 094 — XHTTP_LOCAL_CLOSE_NOT_FAILURE

**Фича:** [XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — два дефекта на границе xhttp-conn, оба наши (форк-нативный пакет `transport/v2rayxhttp`) |
| Статус | I (implemented) — обе правки в дереве, стражи в `breaker_test.go` red/green, пакет под `-race`, vet и тесты как в CI, стенды `lx-test` зелёные, `lx-build` + `check`; живой стенд (§3.4) и полевое подтверждение репортёра (§3.5) впереди |
| Ветка | `lx` |
| База | `4ca8e8974` (v1.14.1-lx.8) |
| Связано | [076](../076-XHTTP_XMUX_BREAKER/SPEC.md) (брейкер, в котором дефект 1), [082](../082-H2_STREAM_ERROR_TYPE_LEAK/SPEC.md) (образец для дефекта 2: h2-специфику не выпускать из conn), [077](../077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) (conn-scoped контекст запросов), [064](../064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md) (массовый `Close()` через `interrupt_exist_connections`) |

Заявка: [LxBox #148](https://github.com/Leadaxe/LxBox/issues/148) (theyusa, 2026-09-19, ядро lx.4): «XHTTP-узлы под urltest в selector'е с `interrupt_exist_connections: true` циклически вытесняются, лог полон `connection download closed: http2: response body closed`, WS на том же сервере чист». Разведка стороны приложений 2026-09-24 (стенд на Mac с двумя живыми `vless+xhttp+reality`, юнит-проба брейкера) сняла подозрения с LxBox и `lx_idle_suspend` и нашла два дефекта в ядре. Между lx.4 и lx.8 `transport/v2rayxhttp` не менялся, поэтому апгрейд сам по себе жалобу не закрывает.

---

## 1. Дефекты

### 1.1 Брейкер считает нашу же отмену сбоем стрима

`xmuxClient.roundTrip` (`transport/v2rayxhttp/xmux.go`) отмечает `noteFailure()` на **любую** ошибку `RoundTrip`. Запросы `stream-up` (download GET и upload POST) и upload-POST `packet-up` живут на conn-scoped контексте (SPEC 077); `Close()` conn'а ставит `localClosed`, закрывает reader и в конце вызывает `cancel()`. Ещё живой `RoundTrip` возвращает `context.Canceled`, и брейкер записывает его как сбой. Три таких отмены подряд — `evictCause = "failing"`: здоровое пуловое соединение вытесняется, у менеджера взводится backoff на открытие нового.

`localClosed` защищает только путь чтения тела (`connBreaker.noteRead`); `roundTrip` о нём не знает. Массовый `Close()` в поле даёт `interrupt_exist_connections: true` у selector'а и urltest: каждая смена выбора закрывает пачку conn'ов (SPEC 064). У репортёра шесть вариантов одного сервера под urltest с почти равной задержкой, поэтому выбор флапает и отмены идут сериями.

У Xray брейкера нет вовсе: причина `failing` и backoff — наш механизм (SPEC 076), значит и дефект наш.

### 1.2 Наш `Body.Close()` виден релею как ошибка

`streamConn.Read` / `splitConn.Read` / `packetConn.Read` отдают ошибку чтения тела наружу как есть (после `HideStreamError`). Когда тело закрыли мы (`Close()` conn'а, истёкший read-deadline), заблокированный `Read` просыпается с `http2: response body closed` (h2, `errClosedResponseBody`) или `http: read on closed response body` (h1). `E.IsClosedOrCanceled` из sing этих sentinel'ов не знает, и апстримный релей (`route/conn.go`) пишет `connection download closed: …` на **ERROR**. На живом узле каждый успешный запрос (HTTP 204, 6 из 6 на стенде) даёт одну такую строку при нуле вытеснений. У WS тело закрывается путём, который релей распознаёт, поэтому по логу «XHTTP сломан, WS чист» — это и есть источник впечатления репортёра.

---

## 2. Решение

### 2.1 Предикат брейкера

В `xmuxClient.roundTrip` ошибка `errors.Is(err, context.Canceled)` не считается сбоем стрима. Ошибка этого вида по построению рождается только у нас: conn-scoped `cancel()` из `Close()` или отмена вызывающего. Сервер, CDN и DPI дают `http2.StreamError`, `ECONNRESET`, `io.EOF`, GOAWAY-ошибки — они остаются сбоями. `context.DeadlineExceeded` тоже остаётся сбоем: `RoundTrip`, не получивший заголовков до дедлайна на пуловом h2-соединении, и есть то, что брейкер ловит (полуживой пир, SPEC 076).

Не делается: протаскивание `localClosed` в `roundTrip`. Это четыре call-site ради того же результата, и такая проверка ошибочно считала бы сбоем отмену вызывающего, которая к здоровью пулового соединения не относится.

Форма ошибки проверена: `x/net/http2` при `ctx.Done()` возвращает `cancelRequest(cs, ctx.Err())`, то есть `context.Canceled` доходит без обёртки; h1 (`net/http`) и h3 (`quic-go/http3`) отдают `ctx.Err()` по тому же контракту.

### 2.2 Закрытое нами тело = `net.ErrClosed`

В трёх `Read` после `noteRead` (брейкер классифицирует сырую ошибку, порядок из SPEC 082 сохраняется) ошибка проходит через `connBreaker.outboundErr`: если `err != nil && localClosed`, наружу уходит `net.ErrClosed`, а когда тело закрыл истёкший read-deadline — `os.ErrDeadlineExceeded` (таймаут обязан остаться `net.Error` с `Timeout() == true`, SPEC 050; `readDeadline.dead` закрывается только при истечении, `stop()` его не трогает, поэтому это различимо). `io.EOF` не подменяется и под гейтом: чистое завершение сервера в гонке с нашим `Close()` остаётся чистым завершением. Гейт по `localClosed`, а не по тексту sentinel'а: `errClosedResponseBody` и `errReadOnClosedResBody` не экспортированы, а флаг уже ставится **до** закрытия reader'а именно ради этой гонки. Удалённая ошибка (`StreamError`) под гейт не попадает: при ней `localClosed` не взведён.

Правка на границе conn, не в `route/conn.go`: файл апстримный, новый шов ради классификации чужого sentinel'а противоречит §2 конституции, а прецедент SPEC 082 уже держит h2-специфику внутри пакета. Заодно закрываются варианты `dl=h1`/`dl=h3`, до которых правка релея не дотянулась бы единообразно.

`v2rayhttp` и `v2raygrpclite` читают тела теми же путями; у них тот же симптом возможен, но заявки нет — вне объёма, отмечено в FEATURE 002 как «за чем следить».

### 2.3 Границы

- `option/` не меняется: ни новых ключей, ни дефолтов; провод не меняется.
- Брейкер, backoff и остальные причины вытеснения (SPEC 059/076) не меняются.
- `lx_idle_suspend` к XHTTP не применяется и не трогается.
- Текст сообщения LxBox «re-syncing tunnel (heartbeat/streams were paused)» — сторона приложения, решение владельца.

---

## 3. Критерии приёмки

1. `breaker_test.go`: три `roundTrip` с `context.Canceled` подряд не меняют `evictCause()` и не взводят backoff; тот же сценарий с `context.DeadlineExceeded` и с удалённой ошибкой (`ECONNRESET`, `http2.StreamError`) по-прежнему даёт `failing` (red-check: без правки первый тест красный).
2. Юнит на `Read` каждого из трёх conn'ов: reader, чей `Read` после `Close()` возвращает `errors.New("http2: response body closed")`, наружу даёт `net.ErrClosed`; после истёкшего read-deadline — `os.ErrDeadlineExceeded`; без `Close()` та же ошибка проходит как есть; `io.EOF` (с `Close()` и без) и `StreamError` не подменяются.
3. `go test -race ./transport/v2rayxhttp/...` и `lx-test` зелёные; `make -f Makefile.lx lx-build`; CI под всеми наборами тегов.
4. Стенд из разведки (два живых `vless+xhttp+reality` под urltest в selector'е, `interrupt_exist_connections: true`): переключения селектора на живом стриме — ноль строк ERROR `connection download closed` при успешных запросах и ноль `evicted … cause=failing` без реальных сбоев.
5. Полевое подтверждение репортёра #148 на ядре с фиксом (через LxBox после бампа пина) — статус D.

## 4. За чем следить

- `golang.org/x/net/http2` и `net/http`: возврат `ctx.Err()` из `RoundTrip` без обёртки — на каждом бампе прогонять `breaker_test.go`; если библиотека начнёт оборачивать, предикат перейти на `errors.Is` по цепочке (он и так `errors.Is`) — ломается только при замене sentinel'а.
- `common/interrupt` и SPEC 064: массовый `Close()` остаётся штатным путём; новый способ закрытия conn'а мимо `Close()` (например, прямой `reader.Close()`) обойдёт `localClosed` и вернёт дефект 1.2.
