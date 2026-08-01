# SPEC: 002 — XHTTP_CLIENT_TRANSPORT (full)

**Фича:** [XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) — полная клиентская поддержка реализована и отгружена; дальнейшие правки транспорта идут отдельными задачами (011, 042, 043) |
| База | upstream v1.14.0-alpha.* (ветка `lx-1.14`) |
| Реализация | ветка `lx-1.14-xhttp-full` |
| История | v1 (минимальный lean-native клиент, Complete) → см. [SPEC_v1.md](SPEC_v1.md) |

Расширить **клиентский XHTTP-транспорт** (`transport/v2rayxhttp`, build-тег `with_xhttp`) до
**полной клиентской поддержки** расширенных параметров Xray XHTTP / sing-box-extended: настраиваемые
placement'ы session/seq/uplink-data, ключи, метод upload и полноценный **X-Padding obfs-режим**
(включая `tokenish`/HPACK). Сохранить байт-в-байт текущее (лайв-проверенное) поведение по умолчанию.

> **Карта параметров** (что каждое поле делает в Xray, клиент/сервер, дефолты, on-wire) вынесена в
> отдельный документ: **[PARAM_MAP.md](PARAM_MAP.md)** — читать его первым.

---

## 1. Проблема / контекст

- v1 (см. [SPEC_v1.md](SPEC_v1.md)) дал рабочий lean-native клиент с 6 полями (`host`, `path`, `mode`,
  `headers`, `x_padding_bytes`, `no_grpc_header`) и проверенным коннектом к Xray/3x-ui (packet-up/auto).
- Xray и форки (sing-box-extended, NekoBox+) ушли далеко вперёд: добавлены настраиваемые **placement'ы**
  (path/query/header/cookie/body) для session id, seq, payload; **obfs-режим X-Padding** с произвольными
  ключами/заголовками и алгоритмами (`repeat-x`/`tokenish`); метод upload; tuning packet-up. Сервера,
  настроенные на эти режимы, нашим v1-клиентом **не обслуживаются**.
- Цель — закрыть **весь клиентский** surface, оставаясь client-only и не вендоря Xray-внутренности.

## 2. Цель

VLESS/VMess/Trojan outbound с `transport.type=xhttp` поднимает рабочее соединение к XHTTP-серверу Xray
в **любой** из поддерживаемых сервером клиентских конфигураций placement/obfs, поверх TLS/Reality.
Дефолты идентичны v1 (нулевая регрессия). Без `with_xhttp` тип `xhttp` отвергается как прежде.

## 3. Аудит (основание спеки)

Глубокий аудит исходников **XTLS/Xray-core** (`transport/internet/splithttp/*`, `main`) и
**shtorm-7/sing-box-extended** (`transport/v2rayxhttp/*`, `option/v2ray_transport.go`, `extended`),
с adversarial-верификацией каждого факта против исходника. Полные карточки — в [PARAM_MAP.md](PARAM_MAP.md).

**Итог по 16 полям из списка NekoBox+:**

- **12 клиентских** (реализуем): `sessionPlacement`(+`sessionKey`), `seqPlacement`(+`seqKey`),
  `uplinkDataPlacement`, `uplinkDataKey`, `uplinkChunkSize`, `uplinkHTTPMethod`, `xPaddingObfsMode`,
  `xPaddingKey`, `xPaddingHeader`, `xPaddingPlacement`, `xPaddingMethod`.
- **4 server-only** (НЕ реализуем, accept-but-ignore): `serverMaxHeaderBytes`, `noSSEHeader`,
  `scMaxBufferedPosts`, `scStreamUpServerSecs`. Ни одно не читается клиентом; клиент даже не инспектирует
  `Content-Type` ответа.
- **+2 клиентских tuning-поля** вне списка, которые клиент реально читает в packet-up:
  `scMaxEachPostBytes`, `scMinPostsIntervalMs` — добавляем для полноты.

**Ключевой факт совместимости:** текущий v1-код = дефолтный Xray (obfs off → x_padding в Referer;
session/seq в path; payload в body; метод POST). Расширение — это **добавление альтернативных режимов**,
а не переписывание. Все новые поля имеют дефолты, сохраняющие текущее поведение.

**Коррекции верификации, влияющие на реализацию:**
1. `Access-Control-Allow-Credentials` **не выставляется** нигде (cookie/query placement его не ставят).
2. Mode-gate'ы (`header`/`cookie` uplink только в packet-up; `GET` метод только в packet-up) живут **только**
   в extended option-слое — переносим как нашу валидацию. **Исключение (lx):** `GET` вне packet-up — НЕ
   ошибка, а **soft-fallback на `POST` + `WARN`** (чтобы одна кривая нода подписки не роняла весь конфиг);
   `header`/`cookie` uplink вне packet-up остаётся жёсткой ошибкой (нет безопасного дефолта). См. журнал.
3. Upper-casing `uplink_http_method` — только в extended option-слое.
4. Серверный keepalive-padding гейтится `(legacyMarker || obfsAccepted) && scStreamUpServerSecs.To>0` —
   нас (клиент) не касается, но учтено в карте.

## 4. Требования

### 4.1 Опции (`option/v2ray_xhttp.go` — новый код, нулевое касание upstream)

Расширить `V2RayXHTTPOptions`, **сохранив** 6 существующих полей. Добавить (JSON snake_case, как в
sing-box-extended; все `omitempty`):

**Placement / keys (core):**
- `session_placement` (string: `path`|`query`|`header`|`cookie`; default `path`)
- `session_key` (string; default `X-Session`/`x_session` по placement)
- `seq_placement` (string: `path`|`query`|`header`|`cookie`; default `path`)
- `seq_key` (string; default `X-Seq`/`x_seq` по placement)

**Uplink data (obfs/tuning):**
- `uplink_data_placement` (string: `body`|`auto`|`header`|`cookie`; default `auto`≈body)
- `uplink_data_key` (string; default `X-Data`/`x_data` по placement)
- `uplink_chunk_size` (`*badoption.Range[int]`; default зависит от placement)
- `uplink_http_method` (string; default `POST`; upper-case)

**X-Padding obfs:**
- `x_padding_obfs_mode` (bool; default false)
- `x_padding_key` (string; default `x_padding`)
- `x_padding_header` (string; default `X-Padding`)
- `x_padding_placement` (string: `cookie`|`header`|`query`|`queryInHeader`; default `queryInHeader`)
- `x_padding_method` (string: `repeat-x`|`tokenish`; default `repeat-x`)

**Packet-up tuning (бонус):**
- `sc_max_each_post_bytes` (`*badoption.Range[int]`; default `[1000000,1000000]`)
- `sc_min_posts_interval_ms` (`*badoption.Range[int]`; default `[30,30]`)

**Server-only (accept-but-ignore — присутствуют в struct, помечены `// server-only, ignored by client`):**
- `server_max_header_bytes` (int), `no_sse_header` (bool), `sc_max_buffered_posts` (int64),
  `sc_stream_up_server_secs` (`*badoption.Range[int]`)

Нормализация и валидация (mode-gate'ы, дефолты по placement, upper-case метода, отказ на неизвестных
значениях с понятными ошибками) — реплицируем семантику extended `checkV2RayXHTTPBaseOptions` +
`GetNormalized*`.

### 4.2 Транспорт (`transport/v2rayxhttp/*` — новый код)

- **placement-движок:** единая функция `applyMeta(req, sessionID, seqStr)` раскладывающая session id и seq
  по настроенным placement/key (path/query/header/cookie). Path сохраняет порядок «session первый, seq
  второй». Заменяет нынешнюю жёсткую path-логику в `requestURL`.
- **нормализация path (trailing slash):** `NewClient` хранит `path` **как в конфиге**, гарантируя лишь
  ведущий `/`; хвостовой слэш **значим и сохраняется** на проводе для всех режимов, кроме stream-one.
  Единственная точка, где слэш срезается, — bare-path ветка `applyMeta` (пустой `sessionID`, stream-one):
  Xray-сервер роутит двунаправленную stream-one ветку по **точному голому пути**, поэтому там путь
  тримится (`trimBarePathSlash`, корень `/` не схлопывается в пустой). См. §9 (первопричина 301).
- **uplink-data:** для packet-up — `body`/`auto` (тело, как сейчас) или header/cookie chunked
  (`base64.RawURLEncoding`, чанки `<key>-<i>`/`<key>_<i>`, размер по `uplink_chunk_size`).
- **uplink-метод:** upload-запросы используют `uplink_http_method` (download — всегда GET).
- **X-Padding движок (`xpadding.go`, новый файл):**
  - non-obfs (default): как сейчас — `x_padding=<pad>` в query внутри `Referer`.
  - obfs: `applyXPadding(req)` по `x_padding_placement` (cookie/header/query/queryInHeader) с именами
    `x_padding_key`/`x_padding_header`.
  - генератор: `repeat-x` (`strings.Repeat`) и `tokenish` (порт `GenerateTokenishPaddingBase62` на
    `crypto/rand` + `golang.org/x/net/http2/hpack.HuffmanEncodeLength`, ±2 тюнинг, filler `X`/`Z`, ≤150 итер).
- **packet-up tuning:** разбиение upload по `sc_max_each_post_bytes`, троттлинг по `sc_min_posts_interval_ms`.
- Конструктор `NewClient` принимает расширенные опции, нормализует, валидирует, кэширует разобранные
  placement/method.

### 4.3 Зона касания upstream

Без изменений против v1: ровно `option/v2ray_transport.go` (уже прокидывает весь `XHTTPOptions`),
`constant/v2ray.go`, `transport/v2ray/transport.go` (registry). Все новые поля и логика — в новых файлах
(`option/v2ray_xhttp.go`, `transport/v2rayxhttp/*`).

### 4.4 TLS/Reality

Без изменений — `tlsConfig` прокидывается как прежде; XHTTP+Reality работает. (XHTTP+XTLS-Vision
несовместимы — ограничение протокола.)

## 5. Маппинг в Go (точки реализации)

| Слой | Файл | Изменение |
|------|------|-----------|
| Опции | `option/v2ray_xhttp.go` | +20 полей в `V2RayXHTTPOptions`; новый файл нормализации/валидации (можно `option/v2ray_xhttp_normalize.go`) |
| Placement | `transport/v2rayxhttp/meta.go` (новый) | `applyMeta` + резолверы ключей/дефолтов |
| Padding | `transport/v2rayxhttp/xpadding.go` (новый) | obfs-движок + `repeat-x`/`tokenish` |
| Клиент | `transport/v2rayxhttp/client.go` | приём опций, ветвление newRequest на obfs/non-obfs |
| Conn | `transport/v2rayxhttp/conn.go` | uplink-data placement, packet-up tuning |
| Тесты | `transport/v2rayxhttp/*_test.go` | юнит-тесты на каждый placement/метод/генератор |

## 6. Критерии приёмки

- `sing-box check -c` принимает VLESS + `transport.type=xhttp` со всеми новыми полями (валидные конфиги
  под каждый placement/obfs-режим в `lx-test/config/`).
- **Нулевая регрессия дефолта:** конфиг без новых полей даёт байт-в-байт тот же on-wire запрос, что v1
  (тест сравнения с золотым образцом Referer/path/body).
- Юнит-тесты зелёные на каждый:
  - placement session/seq (path/query/header/cookie) — корректные URL/заголовки/cookie;
  - uplink-data (body/header/cookie) — корректная сборка base64-чанков;
  - X-Padding obfs (4 placement × 2 метода) — корректное размещение и длина;
  - `tokenish` — HPACK-Huffman-длина в [from-2, to+2];
  - валидация — отказ на невалидных значениях и mode-gate'ах с правильными ошибками.
- Сборка `-tags with_xhttp` — ок; сборка без тега — `xhttp` отвергается.
- `go test ./transport/v2rayxhttp/`, `go vet` (lx-теги), `gofmt -l` — зелёные.
- Ребейз-проверка: зона касания upstream не расширилась против v1.
- **Лайв (если есть сервер):** коннект к Xray-серверу хотя бы в одном не-дефолтном режиме (например
  `x_padding_obfs_mode=true` + `x_padding_placement=header`) — иначе помечается как открытый TODO, как в v1.

## 7. План реализации (ветка `lx-1.14-xhttp-full`)

1. Расширить `option/v2ray_xhttp.go` (+ нормализация/валидация). `gofmt`, компиляция.
2. `transport/v2rayxhttp/meta.go` — placement-движок + резолверы. Юнит-тесты.
3. `transport/v2rayxhttp/xpadding.go` — obfs + repeat-x/tokenish. Юнит-тесты (включая HPACK-длину).
4. Прошить в `client.go`/`conn.go`: ветвление obfs/non-obfs, uplink-data placement, метод, tuning.
5. Конфиги `lx-test/config/xhttp_*.json` под новые режимы; `sing-box check`.
6. Тест нулевой регрессии дефолта.
7. `go test` + `go vet` + `gofmt` + сборка с тегом/без. `make -f Makefile.lx lx-build`.
8. IMPLEMENTATION_REPORT, TASKS, статус папки.

## 8. Вне скоупа

- **XHTTP server/inbound** (отдельная задача) — server-only поля только accept-but-ignore.
- **xmux** (мультиплексирование соединений) — отдельная оптимизация, не входит в параметры.
- Маппинг `vless://…type=xhttp` в лаунчере (его репозиторий).

## 9. Bugfix: хвостовой слэш пути ломал packet-up через reverse-proxy (301)

**Симптом (жалоба, 2026-07):** VLESS + XHTTP через CDN/nginx, `mode: packet-up`,
`session_placement: header`, `path: "/upload/"` — коннект падал сразу с
`v2ray-xhttp: unexpected download status: 301 Moved Permanently`. Тот же конфиг в
v2rayNG (ядро Xray) работал. На сервере nginx `location /upload/ { proxy_pass … 3x-ui }`.

**Первопричина.** `NewClient` **безусловно** срезал хвостовой слэш пути
(`path = strings.TrimRight(path, "/")` в `client.go`) для **всех** режимов сразу.
Это решало узкую задачу stream-one (голый путь без слэша — контракт Xray), но
уродовало значимый слэш там, где он нужен для роутинга на прокси. Проявлялось
только в связке двух условий:

1. `session_placement` ≠ `path` (header/query/cookie) — session id **не** дописывается
   сегментом в путь, поэтому базовый путь уходит на провод как есть.
2. path задан со слэшем (`/upload/`), но `TrimRight` превратил его в `/upload`.

Итог: download-GET уходил на `GET /upload` (без слэша). nginx с `location /upload/`
отвечает `301 → /upload/`. Download-канал ходит через голый `http2.Transport.RoundTrip`,
который **не следует редиректам** (redirect-following живёт в `net/http.Client`, его
здесь нет) → 301 долетает в `conn.go` как `StatusCode != 200` → ошибка dial.
При `session_placement: path` баг незаметен: путь становится `/upload/<sessionId>`
(под-путь, `appendPathSegment` сам нормализует слэши) и под `location /upload/` не
редиректится — поэтому дефолтные конфиги Xray/3x-ui не задеты.

**Фикс.** Убрать глобальный `TrimRight` из `NewClient` — путь хранится как в конфиге
(гарантируется лишь ведущий `/`). Обрезка слэша перенесена **локально** в bare-path
ветку `applyMeta` (`sessionID == ""`, только stream-one) через `trimBarePathSlash`,
который не схлопывает корень `/` в пустую строку. Результат по режимам:

- packet-up / stream-up + `session_placement: header|query|cookie` → путь `/upload/`
  сохраняется → nginx проксирует без 301 (кейс жалобы **чинится без воркэраунда**);
- любой режим + `session_placement: path` → `appendPathSegment("/upload/", sid)` =
  `/upload/sid` (как раньше, `appendPathSegment` терпим к обоим вариантам слэша);
- stream-one (пустой sessionId) → путь тримится до `/upload` → контракт Xray
  bare-path соблюдён, регрессии в [011](../011-XHTTP_STREAM_ONE_DOWNLINK/SPEC.md) нет.

**Тесты.** `TestTrailingSlashPreservedOffPath` (`url_test.go`) фиксирует: session-в-header
+ `/upload/` сохраняет слэш на download и upload, а stream-one тримит до голого пути;
старый `TestRequestURLPaths` (stream-one bare path) остаётся зелёным. `sing-box check`
на конфиге из жалобы принимается.

> **Синтетика.** Правка проверена юнит-тестами (`req.URL.Path`, что уходит в `RoundTrip`)
> и `check`. Лайв на реальной nginx-прокси-ноде перед 3x-ui **не прогонялся** (нет
> такого стенда) — открытый TODO, как и в v1/011; при доступе к ноде прогнать.

## 10. Ссылки

- [PARAM_MAP.md](PARAM_MAP.md) — детальная карта всех параметров (основной справочник).
- [SPEC_v1.md](SPEC_v1.md) — исходная минимальная спека (история).
- Xray-core splithttp: https://github.com/XTLS/Xray-core/tree/main/transport/internet/splithttp
- sing-box-extended (ветка `extended`): https://github.com/shtorm-7/sing-box-extended
- [V2Ray Transport — sing-box](https://sing-box.sagernet.org/configuration/shared/v2ray-transport/)
