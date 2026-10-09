# TASKS: 119 — XHTTP_PATH_QUERY

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md) · план — [PLAN.md](PLAN.md)

## 1. Спека и доки

- [x] 1.1 SPEC.md, PLAN.md, TASKS.md; сверка с Xray `splithttp` (`GetNormalizedPath`/`GetNormalizedQuery`, `ApplyPaddingToQuery`)
- [x] 1.2 FEATURE 002: `path` с `?query`, правило, строка 119 в «Задачах фичи»
- [x] 1.3 `URL_PARSING.md` задачи 002: `path` передаётся целиком
- [x] 1.4 Строка 119 в Roadmap `SPECS/README.md`

## 2. Реализация

- [x] 2.1 `client.go`: деление `path` по первому `?`, `Client.query`, `u.RawQuery` в `baseURL()`
- [x] 2.2 Проверено: `setQuery` (сессия/seq/паддинг в query) добавляет к query, не заменяет; все запросы идут через `newRequest` → `baseURL`

## 3. Тесты и сборка

- [x] 3.1 `url_query_test.go`: критерии §5 SPEC; без правки 2.1 тесты красные (`RequestURI() = "/%3Fproxyip=149.56.109.62/"`)
- [x] 3.2 `go vet` + `go test` пакета с тегами `with_xhttp,with_utls,with_quic`
- [x] 3.3 `make -f Makefile.lx lx-build`, `sing-box check` конфига с `path: "/?proxyip=…"`
- [x] 3.4 IMPLEMENTATION_REPORT.md; статус I
- [ ] 3.5 Проверка репортёром #36 через Cloudflare-релей
