# IMPLEMENTATION_REPORT — 002 XHTTP_CLIENT_TRANSPORT

**Дата:** 2026-06-09 · **Статус:** O (в работе — собрано/check, wire не сверен с Xray, лайв-тест отложен) · **База:** `v1.13.13`

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

## Остаточные пробелы (для доведения)

1. **Wire-протокол — частично сверен с Xray-исходниками** (`XTLS/Xray-core/transport/internet/splithttp`, без живого сервера):
   - ✅ **`sessionId`** исправлен на UUID-формат с дефисами (Xray: `uuid.New().String()`) — было 32-hex.
   - ✅ path-layout `<path>/<sessionId>[/<seq>]` — совпадает.
   - ⚠️ **padding placement версионно-зависим:** текущий Xray (`config.go`) кладёт `x_padding=<нули>` query-параметром в **Referer** (Key `x_padding`, Header `Referer`); старый Xray — отдельным `X-Padding`. У нас сейчас отдельный `X-Padding` + plain `Referer` (помечено комментом в `client.go`). **Сверить с версией Xray целевого сервера на лайв-тесте.**
2. **`mode=auto`** сейчас алиас `stream-one` — реальная негоциация (try stream-one → fallback packet-up) не реализована.
3. **packet-up** без xmux/переиспользования соединений; **stream-up** допускает, что сервер начинает download-стрим до завершения upload.
4. **Лайв end-to-end** не выполнялся (нет XHTTP-сервера) — по SPEC §7 это потолок приёмки.
5. `x_padding_bytes` смоделирован строкой «min-max» (нет Range-типа в badoption).

## Дальше

- Сверка wire с Xray-исходниками (не требует сервера).
- Лайв-тест против Xray XHTTP-сервера (когда появится) → перевод в `C`.
- Лаунчер: маппинг `type=xhttp` в реальный транспорт (follow-up к его задаче 023).
