# SPEC: 119 — XHTTP_PATH_QUERY

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — расхождение клиентского XHTTP с контрактом Xray по `path` |
| Статус | I (implemented) — код и тесты пакета в дереве; проверка репортёром через Cloudflare-релей не проводилась ([IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md)) |
| Ветка | `lx` |
| Build-tag | `with_xhttp` |
| Заявка | [Leadaxe/sing-box-lx#36](https://github.com/Leadaxe/sing-box-lx/issues/36) |
| Связано | [043](../043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md) (завершающий слэш пути `stream-one`), [002](../002-XHTTP_CLIENT_TRANSPORT/SPEC.md) (`URL_PARSING.md` — правило для парсеров ссылок) |

`path` транспорта XHTTP может нести query: всё после первого `?` уходит на провод как query запроса, а не как часть пути. Так поступает Xray, и так конфиг понимают релеи, которые читают свои параметры из query.

Scope: **client-only**.

---

## 1. Как устроено

Настроенный `path` делится по первому `?`:

| Часть | Что с ней происходит |
|-------|----------------------|
| до `?` | Путь запроса. Нормализуется как раньше: ведущий `/` гарантирован; при размещении сессии или seq в пути `stream-one` получает завершающий `/` ([043](../043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md)), остальные режимы дописывают `/<sessionId>` и `/<seq>` |
| после `?` | Query запроса, байт в байт как в конфиге |

Размещения `session_placement: query`, `seq_placement: query` и паддинг `x_padding_placement: query` (obfs-режим) **добавляют** свои параметры к настроенному query, а не заменяют его. Когда такое размещение срабатывает, query пересобирается стандартным кодированием — параметры сортируются по ключу, значения перекодируются. Так же поступает Xray (`q.Set` + `q.Encode()`).

Паддинг в заголовке (`Referer` по умолчанию, `queryInHeader` в obfs-режиме) несёт только путь и `x_padding`, без настроенного query — как у Xray.

Правило действует во всех режимах (`stream-one`, `stream-up`, `packet-up`) и на всех версиях HTTP: все запросы транспорта строятся из одного базового URL.

### 1.1 Пример

`path: "/?proxyip=149.56.109.62"`, `mode: stream-one` → на проводе `POST /?proxyip=149.56.109.62`.

`path: "/base?x=1"`, `mode: packet-up`, размещения по умолчанию → upload `POST /base/<sessionId>/<seq>?x=1`, download `GET /base/<sessionId>?x=1`.

## 2. Проблема (issue #36)

Узел за Cloudflare-Worker-релеем (`cmliu/edgetunnel`) с `path: "/?proxyip=…"`: сайты, хостящиеся у Cloudflare, через него отвечали 502 через 20–60 с; тот же узел в Shadowrocket и sing-box-extended работал.

Клиент клал весь `path` в путь запроса, и `?` кодировался: на провод уходило `/%3Fproxyip=149.56.109.62/`. Релей читает `proxyip` из query, получал пустое значение и не мог выйти к адресатам на Cloudflare (Worker не открывает сокеты к IP самого Cloudflare без прокси-IP).

## 3. Контракт Xray

`transport/internet/splithttp/config.go` и `dialer.go`:

- `GetNormalizedPath()` — часть до первого `?`; ведущий `/` гарантирован; завершающий `/` дописывается, если сессия или seq размещены в пути.
- `GetNormalizedQuery()` — часть после первого `?`; в dialer ставится в `requestURL.RawQuery`.
- Размещения в query (`ApplyMetaToRequest`, `ApplyPaddingToQuery`) — `q := URL.Query(); q.Set(…); URL.RawQuery = q.Encode()`.

sing-box-extended делает то же самое (`requestURL.Path = GetNormalizedPath(); requestURL.RawQuery = GetNormalizedQuery()`).

## 4. Требования

1. Часть `path` после первого `?` отправляется как query запроса, не попадая в путь.
2. Нормализация пути (ведущий `/`, завершающий `/` в `stream-one`, сегменты сессии и seq) применяется к части до `?` и не меняется.
3. Размещения сессии, seq и паддинга в query сохраняют настроенный query.
4. `path` без `?` даёт на проводе тот же запрос, что и раньше.

## 5. Критерии приёмки

Юнит-тесты пакета `transport/v2rayxhttp` (клиент строится через `NewClient`):

1. `path: "/?proxyip=149.56.109.62"`, `stream-one` → `RequestURI() == "/?proxyip=149.56.109.62"`.
2. `path: "/base?x=1"`, `packet-up`, размещения в пути → путь `/base/<sid>/<seq>`, query `x=1`.
3. `path: "/base?x=1"`, сессия и seq в query → путь `/base`, query `x=1`, `x_session`, `x_seq`.
4. Паддинг в query (obfs) рядом с настроенным query.
5. `path: "/xhttp"` без `?` → путь `/xhttp/<sid>/<seq>`, query пуст.

Полевая: репортёр #36 подтверждает, что Cloudflare-сайты через релей открываются.

## 6. Зона касания upstream

Апстримных файлов ноль: правка в пакете `transport/v2rayxhttp` (наш пакет). `go.mod` не меняется.

## 7. Границы

- Только клиент.
- Парсеры ссылок (лаунчер, LxBox) должны передавать `path` целиком, с query; правило для них — в `URL_PARSING.md` задачи 002.
