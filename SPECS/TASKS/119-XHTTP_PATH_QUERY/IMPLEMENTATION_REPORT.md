# IMPLEMENTATION REPORT: 119 — XHTTP_PATH_QUERY

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `transport/v2rayxhttp/client.go` | `path` делится по первому `?`; путь нормализуется как раньше, query хранится в `Client.query` и ставится в `RawQuery` базового URL |
| `transport/v2rayxhttp/url_query_test.go` | `TestPathQueryPreserved` (критерии 2, 3, 5), `TestPathQueryWireForm` (критерий 1), `TestPathQueryKeptByQueryPadding` (критерий 4) |

Апстримных файлов ноль; `go.mod` не менялся.

## Наблюдения

- `setQuery` уже сохранял существующие параметры: правка размещений не понадобилась.
- До правки `stream-one` с `path: "/?proxyip=…"` слал `/%3Fproxyip=149.56.109.62/` — к закодированному `?` добавлялся ещё и завершающий слэш из [043](../043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md).
- Отдельной сборки URL в пакете нет: HTTP/1.1, HTTP/2 и HTTP/3 получают запрос из `newRequest`.

## Проверка

- `go vet` и `go test -tags with_xhttp,with_utls,with_quic -ldflags=-checklinkname=0 ./transport/v2rayxhttp/...` (go1.26.8) — ok. С откаченной правкой новые тесты красные.
- `make -f Makefile.lx lx-build` — ok; `sing-box check` конфига VLESS + TLS + xhttp `stream-one` с `path: "/?proxyip=149.56.109.62"` — ok.

## Открыто

- ~~Проверка репортёром #36 через Cloudflare-релей~~ — подтверждена 2026-10-09 обходом `path: "/proxyip=IP"` на lx.12 (`ip=149.56.109.62, colo=YYZ`); тем самым показано, что отдельный DATA-фрейм с голым VLESS-заголовком (без `sniff`) этот релей переживает — выравнивание с ожиданием первого payload у Xray для #36 не требуется.
- Парсеры ссылок лаунчера и LxBox срезают `?…` у xhttp `path` — задачи заведены в их репозиториях 2026-10-09; без них подписки с такими нодами ломаются до ядра.
