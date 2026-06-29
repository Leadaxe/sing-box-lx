# IMPLEMENTATION_REPORT — 002 XHTTP_CLIENT_TRANSPORT

---

## v2 — полная клиентская поддержка параметров (2026-06-29)

**Статус:** реализация code-complete + все проверки зелёные; **лайв на не-дефолтном режиме — открытый TODO** (как и v1, нужен Xray-сервер с obfs/placement-настройкой).
**База:** upstream v1.14.0-alpha.* (`lx-1.14`) · **Ветка реализации:** `lx-1.14-xhttp-full` · **Спека:** [SPEC.md](SPEC.md) + [PARAM_MAP.md](PARAM_MAP.md).

### Что сделано

Расширил клиент до **полной клиентской поддержки** расширенных XHTTP-параметров Xray / sing-box-extended,
оставаясь client-only и без вендоринга Xray. Основание — глубокий аудит исходников XTLS/Xray-core
(`transport/internet/splithttp`) и shtorm-7/sing-box-extended (`transport/v2rayxhttp` + `option`), с
adversarial-верификацией каждого факта. Карта всех 16 полей — в [PARAM_MAP.md](PARAM_MAP.md).

**Реализованы (12 клиентских + 2 tuning-бонуса):**
- session/seq **placement** (path/query/header/cookie) + ключи (`session_key`/`seq_key`);
- uplink-data **placement** (body/auto/header/cookie, chunked base64) + `uplink_data_key` + `uplink_chunk_size`;
- `uplink_http_method` (upper-case, GET только в packet-up);
- **X-Padding obfs**: `x_padding_obfs_mode` + placement (cookie/header/query/queryInHeader) +
  `x_padding_key`/`x_padding_header` + method (`repeat-x` и `tokenish` с HPACK-Huffman-тюнингом);
- packet-up tuning: `sc_max_each_post_bytes` (разбиение), `sc_min_posts_interval_ms` (троттлинг).

**Server-only (accept-but-ignore, в struct, не используются клиентом):** `server_max_header_bytes`,
`no_sse_header`, `sc_max_buffered_posts`, `sc_stream_up_server_secs`. Клиент не инспектирует Content-Type
ответа, поэтому корректен и с SSE-заголовком, и без него.

**Файлы:**
- `option/v2ray_xhttp.go` — +20 полей в `V2RayXHTTPOptions` (всё новый код, нулевое касание upstream).
- `transport/v2rayxhttp/meta.go` (новый) — placement-движок (`applyMeta`), нормализация/валидация
  (`normalizeMeta`, mode-gate'ы, дефолты по placement), uplink-data сборка (`applyUplinkData`/`chunkEncoded`).
- `transport/v2rayxhttp/xpadding.go` (новый) — obfs-движок (`applyXPadding`), генераторы
  `repeat-x`/`tokenish` (`generateTokenishPaddingBase62` на `crypto/rand` + `golang.org/x/net/http2/hpack`).
- `transport/v2rayxhttp/client.go` — `Client.meta`/`paddingRange`; `NewClient` зовёт `normalizeMeta`;
  `requestURL`/`padding` заменены на `baseURL` + `newRequest(ctx, method, sessionID, seqStr, body)`.
- `transport/v2rayxhttp/conn.go` — dial-функции под новую сигнатуру; `packetConn.Write` разбивает по
  `sc_max_each_post_bytes`, троттлит по `sc_min_posts_interval_ms`, раскладывает payload по placement.
- `transport/v2rayxhttp/xhttp_test.go` (новый) + обновлённый `url_test.go` — 16 тест-функций.
- `lx-test/config/xhttp_obfs_full.json` — VLESS+Reality+xhttp со всеми 20 новыми полями.

### Архитектурное решение: range-поля строкой

`uplink_chunk_size`, `sc_*` (range) представлены строкой `"min-max"` (как существующий `x_padding_bytes`),
а **не** `*badoption.Range[int]`: в нашем `sing` badoption нет типа `Range`, а строковая форма уже есть и
парсится одним хелпером (`parseRange`). Это отступление от proto-типа Xray зафиксировано в SPEC §4.1.

### Проверки (все зелёные)

- `make -f Makefile.lx lx-build` → ок.
- `./sing-box check -c` на `xhttp_reality.json`, `xhttp_auto_reality.json`, `xhttp_obfs_full.json` → **все PASS**
  (полный obfs-конфиг проходит реальный `box.New`-путь — проверка против badjson-схлопывания слайсов).
- `go test -tags with_xhttp ./transport/v2rayxhttp/` → **16/16 PASS** (включая нулевую регрессию дефолта,
  все placement'ы, uplink-data сборку/разборку, 4×2 obfs-комбинации, tokenish HPACK-длину, валидацию).
- `go vet -tags with_xhttp ./...` → чисто (2 предсуществующих unsafe.Pointer в daemon/libbox — не наши).
- `gofmt -l` → чисто. `go build ./...` (tagged/untagged) → ок. `go test` (untagged) → ок.
- Негатив: бинарь **без** `with_xhttp` отвергает obfs-конфиг (`unknown transport type: xhttp`).

### Нулевая регрессия дефолта

Все новые поля имеют дефолты, сохраняющие v1-поведение байт-в-байт: obfs off → `x_padding` в Referer;
session/seq в path; payload в body; метод POST. Тест `TestDefaultLegacyPadding` это фиксирует.

### Остаточные пробелы

1. **Лайв не-дефолтного режима** — нужен Xray-сервер с включённым obfs/placement, чтобы подтвердить
   совместимость по проводу (синтетика и unit-тесты этого не закрывают). TODO, как и в v1.
2. Зависимость `golang.org/x/net/http2/hpack` для tokenish — уже транзитивно в go.mod (HTTP/2 transport).
3. xmux/переиспользование соединений — вне скоупа (см. SPEC §8).

---

## v1 (история)

**Дата:** 2026-06-09 · **Статус:** Complete — lean-native клиент, **проверен живым Xray/3x-ui сервером** (packet-up/auto) · **База:** `v1.13.13`

## Что сделано

Клиентский XHTTP-транспорт, подход **lean-native** (на примитивах sing-box, минимум зависимостей) — реализован многоагентным workflow в изолированном worktree, влит в `lx` (коммиты `2d97ff56` registry/const + `d1b434fc` транспорт).

**Файлы (новые, если не указано иное):**
- `transport/v2ray/registry.go`, `// lx` в `transport/v2ray/transport.go`, константа в `constant/v2ray.go` — registry-рефактор (ранее).
- `option/v2ray_xhttp.go` — тип `V2RayXHTTPOptions` (Host, Path, Mode, Headers, padding).
- `option/v2ray_transport.go` — **единственная upstream-правка** (// lx): поле `XHTTPOptions` + xhttp-case в Marshal/Unmarshal.
- `transport/v2rayxhttp/{client,conn,register}.go` — клиент; `register.go` под `//go:build with_xhttp`.
- `include/v2rayxhttp.go` (`//go:build with_xhttp`) — blank-import для запуска `init()`.
- `lx-test/config/xhttp_reality.json` — VLESS+xhttp+reality для `check`.

## Проверки (DoD = compiles + check)

- `make -f Makefile.lx lx-build` → ок; `./sing-box check -c lx-test/config/xhttp_reality.json` → **pass**.
- `go vet` (lx-теги) по `transport/v2rayxhttp`, `option`, `transport/v2ray` → чисто; `go build ./...` без тегов → ок; `gofmt` чисто.
- Негатив: бинарь **без** `with_xhttp` отвергает xhttp-конфиг (`unknown transport type: xhttp`). Невалидный mode → `v2ray-xhttp: unknown mode`. Все 4 mode конструируются.

## Зона касания upstream (ребейз)

Ровно **1 файл**: `option/v2ray_transport.go` (3 правки в // lx-маркерах). Реестр и весь пакет `v2rayxhttp` — новые файлы, конфликтов не дают.

## Лайв-тест (реальный Xray/3x-ui XHTTP-сервер)

Проверено против VLESS + Reality + `type=xhttp` ноды (панель 3x-ui):
- ✅ **packet-up** (и `auto` → packet-up): handshake + DNS + HTTPS (example.com 200) + скачивание 2 МБ @ ~2.1 МБ/с — трафик выходит через IP сервера.
- ❌ **stream-one**: `unknown version` — баг при чтении downlink-ответа (выбирается только явно). → **Исправлено в задаче 011** (корень: stream-one должен слать голый путь без sessionId; auto+reality → stream-one). Принято на синтетике, лайв отложен.

**Ключевой фикс (по исходникам Xray hub.go/config.go + лайв):** padding кладётся как `x_padding=<нули>` в **query внутри заголовка `Referer`** (Xray default `PlacementQueryInHeader`, key `x_padding`), а **не** отдельным `X-Padding`. Сервер валидирует длину `x_padding` (дефолт 100–1000) и без неё отвечает **400 Bad Request**. Плюс `mode=auto` переключён на **packet-up**. Коммит `5a398a5e`. Также ранее: `sessionId` → UUID-формат, path-layout `<path>/<sessionId>[/<seq>]` сверены.

## Остаточные пробелы

1. ~~**stream-one** — баг framing downlink (`unknown version`)~~ → **исправлено в 011** (голый путь без sessionId; `auto`+reality → stream-one). Лайв-подтверждение — открытый TODO в 011.
2. **packet-up** без xmux/переиспользования соединений; **stream-up** не лайв-тестился.
3. `x_padding_bytes` — строка «min-max» (нет Range-типа в badoption); дефолт 100–1000.

## Дальше

- Лаунчер: маппинг `type=xhttp` (его задача 023 сейчас маппит в `httpupgrade`) → реальный xhttp-транспорт.
- Опционально: починить stream-one, добавить xmux.
