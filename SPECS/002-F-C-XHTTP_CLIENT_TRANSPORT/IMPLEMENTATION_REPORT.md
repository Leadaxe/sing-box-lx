# IMPLEMENTATION_REPORT — 002 XHTTP_CLIENT_TRANSPORT

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
