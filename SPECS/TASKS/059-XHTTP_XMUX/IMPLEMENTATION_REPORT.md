# SPEC 059 — отчёт о реализации

**Статус:** код + тесты + сборка готовы; остаётся полевая проверка (§7.8 спеки).

## Что сделано

| Файл | Содержание |
|------|------------|
| `transport/v2rayxhttp/xmux.go` | пул целиком: `xmuxClient`, `xmuxManager`, `xmuxRelease`, `normalizeXmux`, адаптеры соединений |
| `option/v2ray_xhttp.go` | поле `Xmux` + структура `V2RayXHTTPXmuxOptions` (6 полей) |
| `option/v2ray_xhttp_xmux_range.go` | тип `XmuxRange` — приём трёх форм записи диапазона |
| `transport/v2rayxhttp/client.go` | транспорт стал фабрикой пула; `DialContext` берёт соединение; debug-лог через `log.Factory` из контекста |
| `transport/v2rayxhttp/conn.go` | три conn'а получили `xmuxRelease`; все шесть `RoundTrip` идут через пул |

Тесты: `xmux_test.go` (13 кейсов), `xmux_packetup_test.go` (счётчик запросов),
`option/v2ray_xhttp_xmux_range_test.go` (11 кейсов форм записи).

## Отклонения от спеки — и почему

**1. Выбор кандидата — `math/rand`, не `crypto/rand`.** Референс берёт
`crypto/rand`. Выбор соединения из пула не секрет, а пакет уже использует
`math/rand` для session-id и паддинга; держим один источник. Спека §4
исправлена.

**2. `h_keep_alive_period` — поле есть, спека сначала утверждала обратное.**
Черновик §6 говорил «в Xray и extended отсутствует, не вводим», опираясь на
перечень геттеров `GetNormalized*`. При чтении исходника поле нашлось
(`option/v2ray_transport.go:500`, `int64`, без геттера — читается напрямую в
`createHTTPClient`). Реализовано как `ReadIdleTimeout` HTTP/2-транспорта;
спека исправлена до написания кода.

**3. Что является «соединением».** У нас был один `http2.Transport` на
клиента, у референса — пул `DialerClient`-ов. Поэтому элемент пула = отдельный
`*http2.Transport` со своим дайлером: разные транспорты дают разные TCP+TLS
соединения, что и требуется. `IsClosed` отслеживается нами (у `http2.Transport`
нет своего сигнала смерти — он молча передиаливает); остальные три причины
вытеснения делают настоящую работу по ротации.

**4. `singleTransportXmux`.** Понадобился шов для сборки `Client` вокруг
готового `RoundTripper` — им пользуются существующие тесты SPEC 050 и
`dial_deadlock_test`. Пул при этом вырождается в одно нератируемое соединение.

**5. `timeNow` в `meta.go` стала переменной** (была функцией) — чтобы тест
старения соединений не спал реальные секунды. Поведение не изменилось.

## Три опасных места из спеки — как закрыты

**Отложенное закрытие.** `xmuxClient.close()` при `openUsage > 0` только
помечает `closed`; реальный `conn.Close()` — в `addOpenUsage(-1)`, когда уходит
последний поток. Тесты `TestXmuxDeferredClose` и
`TestXmuxManagerCloseKeepsLiveStreams` проверяют, что ни ротация, ни остановка
пула не рвут живой поток.

**Идемпотентность.** `xmuxRelease` держит `sync.Once`; тройной `release()` в
`TestXmuxReleaseIsIdempotent` даёт `openUsage == 0` и ровно один `Close`.
Вызывается из `closeOnce` каждого conn'а — то есть двойная защита.

**Асинхронный download (`ead099674`).** Счётчик поднимается в `DialContext` до
запуска горутин и опускается в `Close()` conn'а — то есть после того, как обе
половины закрыты. Порядок внутри `closeOnce`: сначала закрыть читателя, потом
`release()`.

## Проверки

- `go test -race ./transport/v2rayxhttp/ ./option/` — зелено, гонок нет;
- существующие тесты SPEC 050 (дедлайны, отмена диала) — зелены, регрессии нет;
- сборка `cmd/sing-box` под `with_xhttp with_lx_command with_gvisor with_quic with_utls with_wireguard`;
- **живое ядро**: `sing-box check` принимает конфиг с `xmux` в Xray-форме
  (`"max_concurrency": [2,8]`) и отвергает `max_concurrency` вместе с
  `max_connections` с текстом из спеки. Это снимает риск
  «`badjson` молча схлопнул поле» (память `badjson-empty-slice-collapses-to-nil`).

## Что осталось

Полевая проверка на живом Xray-сервере (§7.8): сессия поднимается, переживает
ротацию по `h_max_request_times`, вытеснение не рвёт активные потоки. До неё
статус задачи — I, не C.
