# PLAN: 119 — XHTTP_PATH_QUERY

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `transport/v2rayxhttp/client.go` | Поле `Client.query`. В `NewClient` `options.Path` делится `strings.Cut(…, "?")`: путь нормализуется как раньше (ведущий `/`), остаток — в `query`. `baseURL()` ставит `u.RawQuery = c.query` после `URLSetPath` |
| `transport/v2rayxhttp/url_query_test.go` | Критерии §5 SPEC; клиент строится через `NewClient`, паддинг выключен (`x_padding_bytes: "0"`), кроме теста паддинга в query |
| `SPECS/FEATURES/002-XHTTP/FEATURE.md` | Строка `path` в таблице параметров, правило в «Правилах и гарантиях», строка 119 в «Задачах фичи» |
| `SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/URL_PARSING.md` | Совет парсерам «срезать `?…`» заменён на «передавать `path` целиком» |
| `SPECS/README.md` | Строка 119 в Roadmap |

## Что не меняется

- `applyMeta` (`meta.go`) и `applyXPadding` (`xpadding.go`): размещения в query идут через `setQuery` (`URL.Query()` → `Set` → `Encode()`), который уже сохраняет существующие параметры. Паддинг в заголовке строит URL из пути — как у Xray.
- Нормализация пути `barePathForStreamOne` / `appendPathSegment` работает с путём без query и не трогается.
- Все запросы (`stream-one`, `stream-up`, `packet-up` upload/download) и все версии HTTP (`http1.go`, `http3.go`, HTTP/2) берут URL из `newRequest` → `baseURL`; отдельной сборки URL нет.

## Зона касания upstream

Ноль апстримных файлов, `go.mod` не меняется.

## Проверка

- `gofmt -l` на тронутых файлах.
- `go vet` и `go test` пакета `./transport/v2rayxhttp/...` с тегами `with_xhttp,with_utls,with_quic`, `-ldflags=-checklinkname=0`, тулчейн из `go.version`.
- `make -f Makefile.lx lx-build` и `sing-box check` конфига с `path: "/?proxyip=…"`.
