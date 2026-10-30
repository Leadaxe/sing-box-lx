# SPEC: 142 — XHTTP_CLIENT_AUDIT

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — шесть расхождений клиента XHTTP с эталоном Xray, найденные инспекцией пакета 2026-10-29 |
| Статус | I (implemented) — 2026-10-29: правки в нашем пакете `transport/v2rayxhttp` (апстримных файлов ноль), тест на каждую находку в `audit_test.go`, пакет под `with_xhttp,with_quic` и `-race` зелёный; в поле 2026-10-30 подтверждены §1 (живой Xray `stream-one` отвечает 400, поток закрыт за <0,5 с) и §4 (пинги 45 с не мешают живому `stream-one`), §5 только против REALITY без CDN; §2, §3, §6 в поле нет (§7) |
| Ветка | `lx` |
| Build-tag | `with_xhttp` |
| Источник | Инспекция `transport/v2rayxhttp` против Xray-core `1b1647cd` (`transport/internet/splithttp/`), проведённая параллельно с [141](../141-XHTTP_PACKET_UP_PIPELINE/SPEC.md); решение владельца чинить — 2026-10-29 |
| Связано | [002](../002-XHTTP_CLIENT_TRANSPORT/SPEC.md) §9 (слэш и 301), [043](../043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md) (слэш stream-one), [059](../059-XHTTP_XMUX/SPEC.md) (пул), [077](../077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) (raise stream-up), [094](../094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md) (наш Close нейтрален), [104](../104-XHTTP_HTTP_VERSION_PARITY/SPEC.md) (версии HTTP), [139](../139-CONNECTION_TEST/SPEC.md) (fresh-пробы) |

Нумерация §1–§6 совпадает с тестами `audit_test.go`. Седьмая находка
инспекции — ротация соединения внутри `packet-up`-потока — не делается и
записана границей в [141 §4](../141-XHTTP_PACKET_UP_PIPELINE/SPEC.md).

## 1. stream-up: отказ или конец upload-POST не рвал пайп

**Было.** Горутина upload-POST в `dialStreamUp` после `roundTrip` дренировала
ответ, не глядя на статус и не реагируя на его конец. Сервер Xray в режиме
`packet-up` отвечает `400` на POST без seq (`hub.go`, «stream-up mode is not
allowed»), `409` — на повторный; dial при этом проходил (SPEC 077: x/net
принимает тело сразу после заголовков), а каждый следующий `Write` висел на
пайпе до дедлайна. То же при обрыве долгого POST за CDN.

**Стало.** Ответ upload-POST решает судьбу аплинка так же, как ответ GET —
даунлинка: не-200 → `uploadFailed(unexpected upload status)` (и в канал
raise, если dial ещё ждёт), конец ответа → `uploadFailed(upload stream ended
by server)`, если закрытие не наше (`localClosed`, SPEC 094). У Xray клиент
закрывает тело в обоих случаях (`client.go` `OpenStream`, `uploadOnly`).
Сервер Xray держит ответ открытым всю жизнь сессии (`hub.go` ждёт
`request.Context().Done()` или закрытия очереди), периодический паддинг
`sc_stream_up_server_secs` идёт внутри этого ответа и его не завершает.

## 2. Слэш пути при session не в пути и seq в пути

**Было.** Правило Xray `GetNormalizedPath` (слэш в конце, если session ИЛИ seq
размещены в пути) применялось только к голому пути stream-one
(`barePathForStreamOne`, SPEC 043). При `session_placement`
header/query/cookie и `seq_placement: path` (дефолт) с `path: "/up"`
download-GET уходил на `/up`, upload — на `/up/7`; сервер Xray сверяет
`HasPrefix(path, "/up/")` → `404` на каждом dial. При session в пути слэш давал
`appendPathSegment`, поэтому дефолтные конфиги не задевало.

**Стало.** `normalizedBasePath` применяется в `applyMeta` ко всем запросам.
Размещения вне пути по-прежнему оставляют путь как в конфиге, слэш включая
(SPEC 002 §9: nginx `location /upload/` отвечает 301 на голый `/upload`, а
download-RoundTrip редиректы не ходит).

## 3. HTTP/2: нет `IdleConnTimeout`, вытесненное соединение могло не закрыться

**Было.** `http2.Transport` без `IdleConnTimeout`; `http2XmuxConn.Close`
зовёт только `CloseIdleConnections`. Если последний `release` выведенного из
пула (или `fresh`, SPEC 139) транспорта приходил при незавершённом запросе на
нём, ClientConn не был idle, не закрывался, и после — никогда: таймера
простоя нет, ссылок на транспорт нет. Случаи: `Close` packet-up во время
POST на соединении, вытесненном по reuse/requests/expired/failing; fresh-проба
к узлу, не отвечающему на GET, — соединение на каждую пробу.

**Стало.** `IdleConnTimeout: 300 s` (Xray `net.ConnIdleTimeout`): такое
соединение закрывается, как только запрос завершится и соединение простоит.
Транспорт собирается в `http2.go` (`newHTTP2Transport`).

## 4. `h_keep_alive_period` 0 на HTTP/2 = без пингов

**Было.** Ноль передавался в `ReadIdleTimeout` как есть — у x/net это «без
health-check», то есть дефолт вёл себя как отрицательное значение: соединение,
молча забытое NAT, выдавалось потокам до TCP-таймаута. FEATURE обещала пинги.

**Стало.** 0 → 45 с (`net.ChromeH2KeepAlivePeriod` Xray), отрицательное → 0,
как уже было сделано для HTTP/3 (`http3KeepAlivePeriod`, 10 с).

## 5. Заголовки по умолчанию и `Content-Type` на POST

**Было.** Без `User-Agent` в `headers` на провод уходил `Go-http-client/2.0`
(на HTTP/1.1 — `/1.1`), на packet-up POST ставился
`Content-Type: application/octet-stream`. Xray без UA подставляет Chrome и
набор fetch() (`TryDefaultHeadersWith(header, "fetch")`), `Content-Type` на
POST не ставит (`FillPacketRequest`). Сервер Xray это не ломало; это
сигнатура для DPI и WAF на CDN.

**Стало.** `applyDefaultHeaders` в `NewClient` (`headers.go`): без UA или с
UA `chrome` — Chrome fetch()-набор: `User-Agent`, `Sec-CH-UA`,
`Sec-CH-UA-Mobile`, `Sec-CH-UA-Platform`, `DNT`, `Accept-Language`,
`Sec-Fetch-Mode: cors`, `Sec-Fetch-Dest: empty`, `Sec-Fetch-Site:
same-origin`, `Priority: u=1, i`, `Cache-Control`/`Pragma: no-cache`,
`Accept: */*`; заголовок, заданный пользователем, не перезаписывается; UA
`golang` оставляет дефолт net/http; любой другой UA — как задан (значения
Xray `firefox`/`safari`/`edge`/`curl` не отображаются). Версия Chrome
считается от даты (144 с 2026-01-13, +1 за 35 дней, с отставанием на один
цикл), чтобы сборка не застывала на Chrome дня релиза. `Content-Type` с
packet-up POST снят.

## 6. `%` в `path` разэкранировался

**Было.** `baseURL` ставил путь через `sHTTP.URLSetPath`: `/a%2Fb/` уходил
как `/a/b/` (сервер ждёт префикс `/a%2Fb/` → 404), `/100%/` давал `parse
path: invalid URL escape` на каждом dial. У Xray путь буквальный
(`requestURL.Path = GetNormalizedPath()`).

**Стало.** `u.Path = c.path` буквально; экранирование на провод — стандартное
для `url.URL`, как у Xray.

## 7. Проверка

`audit_test.go`: `TestStreamUpRefusedUploadFailsWrite` и
`TestStreamUpEndedUploadFailsWrite` (живой h2c: 400 на POST / ответ,
завершённый после первых байт — `Write` отдаёт причину в пределах бюджета, до
правки висел), `TestBasePathSlashFollowsSeqPlacement` (восемь раскладок, в том
числе «оба вне пути — как в конфиге» и корень), `TestHTTP2TransportParity`,
`TestDefaultHeadersChromeFetch` (набор, verbatim, `chrome`, `golang`, версия от
даты, запрос `NewClient` без `Content-Type`), `TestPathIsLiteral`.
`TestSessionPlacement/query` переведён на слэш по §2. Пакет под
`with_xhttp,with_quic` зелёный, `-race` ×2 чистый, сборка под `LX_TAGS` и без
тегов проходит.

В поле 2026-10-30 (`v1.14.4-lx.25`, узлы владельца, Xray-серверы Liberty):

- §1 — против живого сервера `stream-one` (Турция-БС-3, REALITY): копия узла с
  `mode: packet-up` получила `400 Bad Request` на upload, поток закрыт с
  `unexpected upload status: 400 Bad Request` за 0,1–0,5 с (standalone lx.25 на
  macOS, два dial'а подряд). До правки `Write` висел бы до дедлайна.
- §4 — `stream-one` + REALITY + xmux с `h_keep_alive_period: 0` (пинги 45 с) на
  роутере lx.25: URL-тест 359/398 мс, `StartNetworkQualityTest` через узел —
  аплинк 2,45 Мбит/с, даунлинк 2,76 Мбит/с (узел медленный сам по себе), холостая задержка 968 мс, ошибок нет; Xray владельца `stream-one` + REALITY на том же роутере — 24,6 Мбит/с аплинк ([141 §3](../141-XHTTP_PACKET_UP_PIPELINE/SPEC.md)).
- §5 — заголовки Chrome fetch() и `packet-up` POST без `Content-Type` дошли
  только до серверов с REALITY напрямую (Liberty и Xray 26.3.27 владельца) и
  приняты; за CDN не проверено: единственный такой узел мёртв
  ([141 §3](../141-XHTTP_PACKET_UP_PIPELINE/SPEC.md)).
- §2, §3, §6 — в поле не проверялись.

## 8. Границы

- Закрытие захваченных через `DialTLSContext` h2-соединений при `Close`
  транспорта с живыми стримами не делается — `IdleConnTimeout` закрывает их по
  простою, как у Xray.
- Отображение UA `firefox`/`safari`/`edge`/`curl` в наборы Xray не делается:
  TLS-отпечаток узла задаётся `utls`, UA не согласуется с ним и у Xray.
- Ротация соединения внутри `packet-up`-потока — граница 141 §4.
