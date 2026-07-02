# XHTTP PARAM_MAP — карта параметров Xray XHTTP («splithttp»)

> Сопровождающий документ к [SPEC.md](SPEC.md) (002 — XHTTP_CLIENT_TRANSPORT).
> Детальная карта **«какой параметр что делает в Xray»** по всем расширенным полям XHTTP,
> собранная глубоким аудитом исходников **XTLS/Xray-core** (`transport/internet/splithttp/*`,
> ветка `main`) и форка **shtorm-7/sing-box-extended** (`transport/v2rayxhttp/*` +
> `option/v2ray_transport.go`, ветка `extended`), с adversarial-верификацией каждой карточки
> против исходника.

## 0. Как читать эту карту

- **Xray-поле** — имя в `config.proto` / сгенерированном `config.pb.go` (camelCase) и Go-поле.
- **JSON (наш)** — рекомендованное `snake_case` имя в конфиге sing-box-lx (как в `sing-box-extended`).
- **Клиент?** — потребляет ли поле **клиентская** сторона. Мы строим **client-only** транспорт,
  поэтому это главный столбец: server-only поля документируются, но **не реализуются**.
- **Тир** — `core` (нужно для базовой совместимости), `obfs` (анти-DPI), `tuning` (производительность
  packet-up), `server-only` (нереализуемо на клиенте).
- Цитаты дают **функцию/символ**, а не номер строки: номера строк в обоих апстримах дрейфуют между
  ревизиями (верификация это подтвердила), а имена функций стабильны.

### Сводная таблица (16 полей из списка NekoBox+/sing-box-extended)

| # | Параметр (camelCase) | JSON (наш snake_case) | Клиент? | Тир | Одной строкой |
|---|----------------------|------------------------|:-------:|-----|----------------|
| 1 | `sessionPlacement`     | `session_placement`      | ✅ | core   | Куда класть session id: path/query/header/cookie |
| 2 | `sessionKey`*          | `session_key`            | ✅ | core   | Имя ключа для session id (когда не path) |
| 3 | `seqPlacement`         | `seq_placement`          | ✅ | core   | Куда класть номер пакета (packet-up): path/query/header/cookie |
| 4 | `seqKey`               | `seq_key`                | ✅ | core   | Имя ключа для seq (когда не path) |
| 5 | `uplinkDataPlacement`  | `uplink_data_placement`  | ✅ | obfs   | Куда класть payload upload (packet-up): body/header/cookie/auto |
| 6 | `uplinkDataKey`        | `uplink_data_key`        | ✅ | obfs   | Базовое имя header/cookie для chunked-payload |
| 7 | `uplinkChunkSize`      | `uplink_chunk_size`      | ✅ | tuning | Размер чанка (в base64-символах) для header/cookie-payload |
| 8 | `uplinkHTTPMethod`     | `uplink_http_method`     | ✅ | core   | HTTP-метод upload-запросов (default POST) |
| 9 | `xPaddingObfsMode`     | `x_padding_obfs_mode`    | ✅ | obfs   | Главный переключатель: legacy (Referer) vs configurable obfs |
| 10 | `xPaddingKey`         | `x_padding_key`          | ✅ | obfs   | Имя cookie/query-параметра для padding (obfs-режим) |
| 11 | `xPaddingHeader`      | `x_padding_header`       | ✅ | obfs   | Имя заголовка для padding (obfs-режим) |
| 12 | `xPaddingPlacement`   | `x_padding_placement`    | ✅ | obfs   | Куда класть padding: cookie/header/query/queryInHeader |
| 13 | `xPaddingMethod`      | `x_padding_method`       | ✅ | obfs   | Алгоритм генерации padding: repeat-x / tokenish |
| 14 | `serverMaxHeaderBytes`| `server_max_header_bytes`| ❌ | server-only | Лимит размера заголовков на **сервере** |
| 15 | `noSSEHeader`         | `no_sse_header`          | ❌ | server-only | Сервер не шлёт `Content-Type: text/event-stream` |
| 16 | `scMaxBufferedPosts`  | `sc_max_buffered_posts`  | ❌ | server-only | Глубина буфера переупорядочивания upload на сервере |
| 17 | `scStreamUpServerSecs`| `sc_stream_up_server_secs`| ❌ | server-only | Интервал keepalive-padding в ответе stream-up (сервер) |
| 18 | `scMaxConcurrentPosts`| `sc_max_concurrent_posts`| ❌ | legacy/ignore | Legacy-лимит параллельных upload-POST; **удалён из текущего Xray** |

\* `sessionKey` не входил в исходный список NekoBox+ из 16, но это парный к `sessionPlacement`
ключ (так же как `seqKey` парен к `seqPlacement`); без него placement query/header/cookie для session
неполон. Считаем его частью core-набора.

### Бонус: клиентские upload-tuning поля (вне списка 16, но клиент их читает)

Аудит вскрыл два поля, которые **читает клиент** в packet-up, но которых нет в исходном списке:

| Параметр | JSON | Клиент? | Тир | Что делает |
|----------|------|:-------:|-----|------------|
| `scMaxEachPostBytes`   | `sc_max_each_post_bytes`    | ✅ | tuning | Макс. размер одного upload-POST (порог разбиения) |
| `scMinPostsIntervalMs` | `sc_min_posts_interval_ms`  | ✅ | tuning | Мин. интервал между upload-POST (анти-burst) |

Включаем их в реализацию для полноты packet-up (детали в §6 SPEC).

---

## 1. Текущая база sing-box-lx ↔ дефолт Xray (важно!)

**Наша уже существующая реализация (`transport/v2rayxhttp`) совместима с дефолтным Xray-сервером**
без новых полей, потому что:

| Аспект | Наш текущий код | Эквивалент в терминах этих полей |
|--------|------------------|----------------------------------|
| Padding | `x_padding=<нули>` в query внутри заголовка `Referer` | `xPaddingObfsMode=false` (legacy-ветка Xray) |
| Session id | path-сегмент `<path>/<sessionId>` | `sessionPlacement=path` |
| Seq | path-сегмент `<path>/<sessionId>/<seq>` | `seqPlacement=path` |
| Upload payload | тело POST | `uplinkDataPlacement=body` |
| Upload-метод | `POST` (`GET` для download) | `uplinkHTTPMethod=POST` |

То есть **расширение = добавление альтернативных режимов поверх рабочей base-линии**, а не
переписывание. Дефолты всех новых полей выбраны так, чтобы поведение «из коробки» осталось байт-в-байт
прежним.

---

## 2. Группа: session / seq placement (core)

### `sessionPlacement` + `sessionKey`

- **Xray-поле:** `Config.SessionIDPlacement` (proto `sessionIDPlacement=20`), `Config.SessionIDKey`
  (`sessionIDKey=21`). ⚠️ В Xray поле называется `sessionID*`, а sing-box-extended экспонирует JSON
  как `session_placement`/`session_key` (нормализатор `GetNormalizedSessionPlacement`). **Несовпадение
  имён** — учитываем при реализации.
- **Назначение:** где разместить **session id** на каждом запросе (и GET-download, и POST-upload), чтобы
  сервер мог демультиплексировать логические соединения, разделяющие один HTTP-origin, и сшить
  upload-POST с соответствующим download-GET.
- **Значения:** `path` | `query` | `header` | `cookie`. (В отличие от uplink-data — **нет** `body`/`auto`.)
  Иное значение отвергается: `unsupported session placement: …`.
- **Default:** `path` (пустое → `path`).
- **On-wire:**
  - `path` → первый path-сегмент после base-path: `<path>/<sessionId>` (session **перед** seq).
  - `query` → `?<sessionKey>=<sessionId>`.
  - `header` → `<sessionKey>: <sessionId>`.
  - `cookie` → `Cookie: <sessionKey>=<sessionId>`.
- **Ключ (`sessionKey`):** `GetNormalizedSessionKey` — default `X-Session` для header, `x_session` для
  cookie/query, `""` (не используется) для path. Регистр асимметричен: header — каноничный `X-Session`,
  cookie/query — нижний `x_session`. Клиент и сервер обязаны совпасть.
- **Генерация id:** `GenerateSessionID` — N случайных символов из `SessionIDTable`/`SessionIDLength`,
  иначе UUID-строка. Наш текущий `newSessionID()` уже даёт UUID-формат → совместимо.
- **Сервер:** `ExtractMetaFromRequest`. Пустой sessionId → HTTP 400, **кроме** режимов
  `""`/`auto`/`stream-one`/`stream-up` (там допустима одна неявная сессия).
- **Источник:** Xray `config.go` `GetNormalizedSessionPlacement`/`…Key`/`ApplyMetaToRequest`/
  `ExtractMetaFromRequest`; extended `transport/v2rayxhttp/dialer.go` `ApplyMetaToRequest`,
  `utils.go` `GenerateSessionID`, `option/v2ray_transport.go` валидация + нормализаторы.

### `seqPlacement` + `seqKey`

- **Xray-поле:** `Config.SeqPlacement` (`seqPlacement=22`), `Config.SeqKey` (`seqKey=23`).
- **Назначение:** где разместить **номер пакета** (`seqStr`) на каждом uplink-POST в **packet-up**. seq —
  монотонный `int64` с 0, формат — десятичная ASCII-строка (`strconv.FormatInt(seq,10)`), по одному на
  чанк, чтобы сервер переупорядочил пришедшие не по порядку POST в корректный поток. **Только packet-up**;
  в stream-up/stream-one seqStr = `""` и не отправляется.
- **Значения:** `path` | `query` | `header` | `cookie`. Иначе — `unsupported seq placement: …`.
- **Default:** `path` (пустое → `path`).
- **On-wire:**
  - `path` → **второй** path-сегмент: `<path>/<sessionId>/<seq>` (session первый, seq второй — **порядок
    нагруженный**, сервер разбирает сегменты позиционно).
  - `query` → `?<seqKey>=<seq>`.
  - `header` → `<seqKey>: <seq>`.
  - `cookie` → `Cookie: <seqKey>=<seq>`.
  - Значение — всегда сырая десятичная строка; placement только переносит её, не кодирует.
- **Ключ (`seqKey`):** `GetNormalizedSeqKey` — default `X-Seq` (header) / `x_seq` (cookie/query) / `""` (path).
- **Сервер:** читает обратно симметрично; парсит `strconv.ParseUint(seqStr,10,64)`; ошибка парсинга →
  HTTP **500**. GET с непустым seq = uplink; GET с пустым seq = downlink.
- ⚠️ **Коррекция верификации:** заголовок `Access-Control-Allow-Credentials: true` **НЕ выставляется**
  ни для cookie/query placement, ни где-либо ещё (в исходниках обоих апстримов его нет — была
  галлюцинация в черновике аудита).
- **Источник:** extended `dialer.go` `ApplyMetaToRequest` + `appendToPath` (вставляет `/`-разделитель),
  `client.go` (`var seq int64` / `seqStr := strconv.FormatInt(seq,10)` / `seq += 1`); Xray `config.go`
  одноимённые методы; сервер `hub.go`/`server.go` `strconv.ParseUint`.

---

## 3. Группа: uplink-data (obfs + tuning)

### `uplinkDataPlacement`

- **Xray-поле:** `Config.UplinkDataPlacement` (`uplinkDataPlacement=24`).
- **Назначение:** где нести **payload upload** для одного packet-up POST/GET-запроса. Влияет **только**
  на packet-up (stream-up/stream-one всегда стримят body).
- **Значения:** `body` | `auto` | `header` | `cookie`.
  - `body`/`auto` → payload = сырое тело запроса, `Content-Length` выставлен. (На **клиенте** `auto` ведёт
    себя как `body`; различие `auto` есть только на сервере, который при `auto` конкатенирует header+cookie+body.)
  - `header` → payload `base64.RawURLEncoding`, нарезается на чанки, каждый → заголовок `<uplinkDataKey>-<i>`
    (i с 0, по возрастанию).
  - `cookie` → то же, но cookie `<uplinkDataKey>_<i>` (разделитель `_`, не `-`).
- **Default:** после валидации — `auto` (пустое → `auto`); чистый `GetNormalizedUplinkDataPlacement`
  возвращает `body` для пустого. Чистое клиентское поведение по умолчанию = payload в body.
- **Mode-gate:** `header`/`cookie` **отвергаются**, если `mode != packet-up`
  (`UplinkDataPlacement can be <x> only in packet-up mode`). ⚠️ Этот gate живёт **только в
  sing-box-extended** option-слое, не в Xray-core core.
- **Сервер:** переразбирает, итерируя индексы `i=0..` пока есть keyed header/cookie, `strings.Join` без
  разделителя, затем `base64.RawURLEncoding.DecodeString`. Битый base64 → 400; превышение
  `scMaxEachPostBytes` → 413.
- **Источник:** extended `utils.go` `FillPacketRequest` + `GetRequestHeaderWithPayload`/
  `GetRequestCookiesWithPayload`; сервер `server.go`; Xray `config.go`/`hub.go`.

### `uplinkDataKey`

- **Xray-поле:** `Config.UplinkDataKey` (`uplinkDataKey=25`).
- **Назначение:** базовое **имя** header/cookie для chunked-payload (placement header/cookie). Это
  **имя/обфускационный ключ, не криптографический** — payload base64url-кодируется, не шифруется.
- **On-wire:** header `fmt.Sprintf("%s-%d", key, i)` (`X-Data-0`, `X-Data-1`, …); cookie
  `fmt.Sprintf("%s_%d", key, i)` (`x_data_0`, …).
- **Default:** при placement≠body и пустом ключе — `X-Data` (header/auto) / `x_data` (cookie). Для body —
  пусто (не используется).
- **Источник:** extended `utils.go` тех же функций; default — `checkV2RayXHTTPBaseOptions`.

### `uplinkChunkSize`

- **Xray-поле:** `Config.UplinkChunkSize` (`uplinkChunkSize=26`, тип `RangeConfig`).
- **Назначение:** `Range[int]` (From..To) — размер **в base64-символах** каждого чанка при header/cookie
  payload. Для каждого чанка клиент берёт `min(Range.Rand(), остаток)`. Не влияет на body-placement
  (там размер регулирует `scMaxEachPostBytes`).
- **Default (зависит от placement):** cookie → `[2048, 3072]`; header → `[3000, 4000]`; иначе → значение
  `scMaxEachPostBytes` (default `[1_000_000, 1_000_000]`). Пол: `From < 64` → подтягивается к 64.
- **Сервер:** **не использует** для переразбора — итерирует индексы вслепую и join'ит. Значит чанк-сайз —
  чисто клиентская emission-политика; любой валидный сплит, который сервер сможет собрать, работает.
- **Источник:** extended `option/v2ray_transport.go` `GetNormalizedUplinkChunkSize`; usage в `utils.go`.

### `uplinkHTTPMethod`

- **Xray-поле:** `Config.UplinkHTTPMethod` (`uplinkHTTPMethod=19`).
- **Назначение:** HTTP-метод client→server **upload**-запросов (packet-up POST и stream-up/stream-one
  upstream-запрос с телом). Download (stream-down) — всегда `GET`, не затрагивается. Позволяет замаскировать
  upload под не-POST глагол.
- **On-wire:** метод запроса (request-line / `:method`). `GET` при body==nil (download), иначе настроенный
  метод.
- **Default:** `POST`. ⚠️ Upper-casing значения — **только** в sing-box-extended option-слое; Xray-core
  `GetNormalizedUplinkHTTPMethod` возвращает значение как есть.
- **Mode-gate (lx: soft-fallback):** `GET` осмыслен **только** при `mode=packet-up`, т.к. stream-up/
  stream-one нужен запрос с телом, а GET-с-телом сервер трактует как stream-down. Раньше `GET` вне
  packet-up был **жёсткой ошибкой** (`uplink_http_method can be GET only in packet-up mode`) — но это
  роняло **весь** конфиг из-за одной кривой ноды подписки (наблюдалось: `initialize outbound[N] … can
  be GET only in packet-up mode`). Теперь вместо ошибки — **fallback на `POST`** (безопасный дефолт,
  валиден во всех режимах) + `WARN` в лог; в `packet-up` `GET` сохраняется. Gate — в extended
  (`normalizeMeta`, `meta.go`). См. `SPEC.md` §поведение и тест `TestUplinkGetFallsBackToPostOutsidePacketUp`.
- **Сервер:** маршрутизирует по методу, не по равенству настроенному значению: `GET` + непустой seq =
  uplink; любой не-GET = uplink. Явной проверки настроенного метода нет.
- **Источник:** extended `dialer.go` `OpenStream`/`PostPacket`; `option/v2ray_transport.go`
  `GetNormalizedUplinkHTTPMethod`; Xray `config.go`/`client.go`/`hub.go`.

---

## 4. Группа: X-Padding obfs (obfs)

### `xPaddingObfsMode`

- **Xray-поле:** `Config.XPaddingObfsMode` (bool, `xPaddingObfsMode=14`).
- **Назначение:** **главный переключатель** схемы padding.
  - `false` (default/legacy): padding **всегда** как `queryInHeader` в `Referer` — запрос несёт
    `Referer: <scheme>://<host><path>?x_padding=<padding>`. Ключ жёстко `x_padding`, заголовок жёстко
    `Referer`, метод неявно repeat-x.
  - `true` (obfs): padding по настраиваемым `xPaddingKey`/`xPaddingHeader`/`xPaddingPlacement`/
    `xPaddingMethod` — можно перенести в произвольный cookie/header/query/queryInHeader и выбрать алгоритм.
- **Padding обязателен в обоих режимах** — `x_padding_bytes` нельзя отключить. Меняется только форма и где
  сервер ищет/валидирует.
- **Default:** `false` (JSON `x_padding_obfs_mode`, omitempty).
- **On-wire:**
  - `false` → `Referer: …?x_padding=XXXX…` (наш **текущий** код).
  - `true` → padding по `xPaddingPlacement` в header (default `X-Padding`)/cookie/query/queryInHeader.
  - Ответ сервера зеркалит: obfs → той же placement; non-obfs → `header` с именем `X-Padding`
    (default ответа **отличается** от Referer-дефолта запроса).
- **Сервер:** `ExtractXPaddingFromRequest(request, obfsMode)` → `IsPaddingValid` против
  `GetNormalizedXPaddingBytes`. Невалидно → HTTP **400**. Также гейтит
  `obfsPaddingAccepted := XPaddingObfsMode && paddingValue != ""`, что (вместе с `scStreamUpServerSecs.To>0`
  **или** legacy-Referer-маркером) включает периодический серверный keepalive-padding.
- ⚠️ **Коррекция верификации:** серверный keepalive-тикер гейтится
  `(legacyRefererCompatMarker || obfsPaddingAccepted) && scStreamUpServerSecs.To > 0` — `obfsPaddingAccepted`
  это **одно из двух** условий, не единственное.
- **Out-of-band:** этот bool **не согласуется по проводу** — client и server должны иметь одинаковую
  настройку в конфиге.
- **Источник:** Xray `config.go` `FillStreamRequest`/`FillPacketRequest`; `hub.go` `ServeHTTP`;
  extended `utils.go` тех же + `xpadding.go` `ExtractXPaddingFromRequest`.

### `xPaddingKey`

- **Xray-поле:** `Config.XPaddingKey` (`xPaddingKey=15`).
- **Назначение:** **имя** для несения padding в obfs-режиме — имя cookie (cookie), имя query-параметра
  (query), или имя query-параметра внутри header-URL (queryInHeader). **Не используется** при
  `xPaddingPlacement=header` (там значение = весь заголовок). Это **идентификатор-строка, не крипто-ключ**:
  нигде нет keyed-hash/шифрования padding.
- **On-wire:** cookie `Cookie: <key>=<pad>; Path=/`; query `?<key>=<pad>`; queryInHeader — URL
  `<RawURL>?<key>=<pad>` в заголовке.
- **Default:** в proto пусто; extended при пустом → `x_padding`. В non-obfs режиме литерал `x_padding`
  жёстко зашит независимо от поля.
- **Тонкость:** Xray в queryInHeader присваивает `u.RawQuery = key + "=" + paddingValue` **без**
  URL-escaping. Безопасно, т.к. padding — только `[A-Za-z0-9]` (base62) или `X`.
- **Источник:** Xray `xpadding.go` `ExtractXPaddingFromRequest`/`ApplyXPaddingToHeader`/cookie/query;
  default — extended `checkV2RayXHTTPBaseOptions`.

### `xPaddingHeader`

- **Xray-поле:** `Config.XPaddingHeader` (`xPaddingHeader=16`).
- **Назначение:** **имя заголовка** для padding в obfs+header-based placement.
  - `PlacementHeader`: всё значение заголовка = padding.
  - `PlacementQueryInHeader`: значение = полный URL с `?<key>=<padding>`.
  - Игнорируется при cookie/query placement.
- **Default:** пусто в proto; extended → `X-Padding`. Non-obfs серверный ответ тоже жёстко `X-Padding`.
- **HTTP/2:** имя заголовка в проводе lowercase'ится (HPACK) — нормально, матчинг case-insensitive.
- **Источник:** Xray `xpadding.go` `ApplyXPaddingToHeader`/`ExtractXPaddingFromRequest`; default — extended.

### `xPaddingPlacement`

- **Xray-поле:** `Config.XPaddingPlacement` (`xPaddingPlacement=17`).
- **Назначение:** **где** физически разместить padding в obfs-режиме: `cookie` | `header` | `query` |
  `queryInHeader`. (Константы `path`/`body`/`auto` существуют для других полей, но для padding
  **невалидны** — extended-валидатор отвергает.) Только при `xPaddingObfsMode=true`; в non-obfs принудительно
  queryInHeader(Referer) для запросов, header(X-Padding) для ответа.
- **On-wire:** cookie `Cookie: <key>=<pad>`; header `<XPaddingHeader>: <pad>`; query `?<key>=<pad>`;
  queryInHeader `<XPaddingHeader>: <reqURL>?<key>=<pad>`. `ApplyXPaddingToResponse` обрабатывает только
  header/queryInHeader/cookie (query-on-response нет).
- **Default:** пусто → `queryInHeader` (extended).
- ⚠️ **Коррекция верификации:** никакого `Access-Control-Allow-Credentials` для cookie-placement нет
  (была галлюцинация). CORS-логика в `config.go WriteResponseHeader` касается других заголовков.
- **Источник:** Xray `xpadding.go` `ApplyXPaddingToRequest`/`…ToResponse`/`Extract…`; константы + валидация —
  extended `option/v2ray_transport.go`.

### `xPaddingMethod`

- **Xray-поле:** `Config.XPaddingMethod` (`xPaddingMethod=18`).
- **Назначение:** **алгоритм** генерации байт padding:
  - `repeat-x`: N литеральных `X` (длина == целевому числу байт точно).
  - `tokenish`: случайная base62-строка (`[0-9A-Za-z]`), чья **HPACK/QPACK-Huffman-кодированная** длина
    итеративно подгоняется в пределах ±2 байт от цели, чтобы после HTTP/2-сжатия заголовков размер в
    проводе попадал в диапазон. `tokenish` делает padding похожим на случайный токен, а не на ряд `X`.
- **On-wire:** repeat-x — `strings.Repeat("X", length)`. tokenish — base62-токен через
  `hpack.HuffmanEncodeLength`; `X`/`Z` берутся как filler (у них 8-битные Huffman-коды → не сжимаются),
  чтобы длина была стабильной.
- **Default:** пусто → `repeat-x` (extended).
- **Сервер:** `IsPaddingValid` с тем же методом. repeat-x → `len(value) ∈ [from,to]`; tokenish →
  `hpack.HuffmanEncodeLength(value) ∈ [from-2, to+2]`. **Client и server обязаны использовать один метод** —
  tokenish-значение, проверенное как repeat-x, сравнит сырую длину, не Huffman.
- **Реализация tokenish:** не нужен вендоринг Xray — нужен `golang.org/x/net/http2/hpack` (уже транзитивно
  в go.mod). Порт `GenerateTokenishPaddingBase62` (~40 строк): `crypto/rand` base62 длины ≈ `ceil(target/0.8)`,
  затем тюнинг `HuffmanEncodeLength` в ±2, чередуя filler `X`/`Z`, лимит 150 итераций.
- **HTTP/1.1:** сырая длина = длине в проводе → repeat-x и tokenish по длине эквивалентны.
- **Источник:** Xray `xpadding.go` `GeneratePadding`/`GenerateTokenishPaddingBase62`/`IsPaddingValid`;
  extended `xpadding.go` байт-в-байт зеркало.

---

## 5. Группа: server-only (документируем, НЕ реализуем)

Все 4 поля **не читаются клиентом** ни в Xray-core, ни в sing-box-extended. Ключевой факт: **клиент вообще
не инспектирует `Content-Type` ответа** (`stream-down` → `wrc.Set(resp.Body)`; `stream-up` →
`io.Copy(io.Discard, resp.Body)`), поэтому корректно работает и с SSE-заголовком, и без него, без какого-либо
кода.

| Параметр | Что делает (на сервере) | Default | Почему клиенту не нужно |
|----------|--------------------------|---------|--------------------------|
| `serverMaxHeaderBytes`  | `http.Server{MaxHeaderBytes}` — лимит размера заголовков входящего запроса | `8192` | У client-only транспорта нет `http.Server` |
| `noSSEHeader`           | Сервер не шлёт `Content-Type: text/event-stream` на stream-down GET | `false` (SSE шлётся) | Клиент не читает Content-Type — обрабатывает оба случая без кода |
| `scMaxBufferedPosts`    | Ёмкость серверной очереди переупорядочивания upload-POST (packet-up) | `30` | Клиент не знает о глубине буфера сервера |
| `scStreamUpServerSecs`  | Интервал (сек, Range) периодической записи `X`-padding в ответ stream-up | `{20,80}` | Клиент тихо отбрасывает эти байты (`io.Discard`) |

**Решение для реализации:** не реализуем. В конфиге — `expose-but-ignore` (принимаем поля, чтобы
server-образные конфиги не падали на парсинге), помечены как inbound-only. Альтернатива (просто
document-and-skip без полей в struct) тоже допустима; финальный выбор зафиксирован в SPEC §6.

- **Источник:** Xray `config.go` `GetNormalizedServerMaxHeaderBytes`/`…ScMaxBufferedPosts`/
  `…ScStreamUpServerSecs`; `hub.go` `ListenXH`/`ServeHTTP`/`upsertSession`; extended `server.go` зеркало.
  `noSSEHeader` — без нормализатора, читается как сырой bool.

### `scMaxConcurrentPosts` — legacy, удалено из upstream (accept-but-ignore)

- **Статус:** **поля НЕТ** в текущих Xray-core и sing-box-extended. Подтверждено: `grep` пусто,
  GitHub code search `scMaxConcurrentPosts repo:XTLS/Xray-core` → `total: 0`, нет
  `GetNormalizedScMaxConcurrentPosts`. Это knob **старого** релиза Xray, удалённый при редизайне
  upload-пути. Встречается в реальных `vless://`-ссылках (в `extra={...}`) как legacy-артефакт клиента,
  сгенерировавшего ссылку.
- **Что было:** когда-то ограничивал число параллельных upload-POST в packet-up.
- **Текущий механизм Xray (вместо него):** **не семафор**, а (a) bounded pipe (backpressure) +
  `scMaxBufferedPosts` (server reorder window, default 30) и (b) сериализация на
  `<-wroteRequest.Wait()` для `DefaultDialerClient` — следующий POST не диспетчится, пока тело текущего
  не дописано в провод. Фактически **1 POST-тело в полёте за раз**. Seq инкрементируется per-packet
  синхронно в writer-goroutine; переупорядочивание — server-side по seq (`upload_queue.go`, heap).
- **Наш клиент:** `packetConn.Write` шлёт upload-POST **последовательно** (один `sendPacket` за раз) —
  это уже соответствует текущему поведению Xray (1 тело за раз). Истинная bounded-concurrency (N в
  полёте) была бы **улучшением над upstream**, не совместимостью.
- **Решение:** поле `sc_max_concurrent_posts` принимается в опциях (чтобы legacy-конфиги/ссылки не
  падали на парсинге), но **игнорируется**. Тир — `legacy/ignore`.
- **Источник:** Xray `dialer.go` writer-loop (нет concurrency-cap), `upload_queue.go` (heap по `Seq`);
  отсутствие символа подтверждено code search.

---

## 6. Что из этого реализуем в sing-box-lx (резюме)

- **Реализуем (12 клиентских + 2 tuning-бонуса):** session/seq placement+key, uplink-data
  placement/key/chunk/method, полный X-Padding obfs (placement/key/header/method, включая `tokenish`),
  плюс `sc_max_each_post_bytes` / `sc_min_posts_interval_ms` для packet-up.
- **Не реализуем (4 server-only):** `server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts`,
  `sc_stream_up_server_secs` — accept-but-ignore в опциях.
- **Не реализуем (1 legacy):** `sc_max_concurrent_posts` — удалено из upstream; наш последовательный
  upload = текущий Xray; accept-but-ignore.
- **Зависимости:** только `golang.org/x/net/http2/hpack` для tokenish (уже есть). Без вендоринга Xray.
- **Совместимость:** все дефолты сохраняют текущее (рабочее, лайв-проверенное) поведение байт-в-байт.

Детали маппинга в Go-структуры, точки касания upstream и план реализации — в [SPEC.md](SPEC.md) §5–§7.
