# SPEC: 011 — XHTTP_STREAM_ONE_DOWNLINK

Починить XHTTP-режим **`stream-one`** (сломан с момента 002) и привести **`mode=auto`** к поведению Xray, чтобы конфиги `vless + reality + xhttp + mode:auto` поднимались на нашем ядре «как есть».

Тип: **B** (баг в фиче 002). Build-tag: `with_xhttp`. Scope: **client-only**.

---

## 1. Проблема / контекст

Жалоба (2026-06-21): простейший `vless + reality + xhttp`, `mode:auto`, `path:/` работает на стороннем ядре (Xray-логика), **не работает на нашем**. Это типовой конфиг из панелей/подписок.

Две связанные первопричины (обе сверены построчно с исходниками Xray-core `transport/internet/splithttp` ветки `main` и подтверждены [issue #5635](https://github.com/XTLS/Xray-core/issues/5635), а также референс-портом hiddify):

### 1.1 `stream-one` шлёт `sessionId` в пути — главный баг

В Xray `stream-one` — это **один POST на голый путь без sessionId**; сервер (`hub.go`) роутит режим по наличию sessionId:
- `sessionId == ""` → bidirectional **stream-one** ветка (один `httpServerConn`: `request.Body` = uplink, `ResponseWriter` = downlink);
- `sessionId != ""` → stream-up / packet-up.

Наш `dialStreamOne` ([transport/v2rayxhttp/conn.go:21](../../transport/v2rayxhttp/conn.go)) строит URL как `c.requestURL(sessionID)` → `<path>/<sessionId>`. Сервер парсит **непустой** sessionId, уходит в stream-down ветку (ждёт парный stream-up POST, которого нет), и в response.Body летят **не VLESS-байты**. VLESS-парсер читает первый байт как версию → `unknown version` (часто `0x58='X'` из HTTP-обвязки). Зафиксировано как «known bug» ещё в [002 IMPLEMENTATION_REPORT](../002-F-C-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md).

### 1.2 `mode=auto` всегда → packet-up

Xray (`dialer.go`): `auto` → packet-up по умолчанию, **но если REALITY → stream-one** (если ещё и `downloadSettings` → stream-up). Решает только наличие REALITY/downloadSettings, не h2/h3.

Наш `DialContext` ([client.go:155](../../transport/v2rayxhttp/client.go)) намеренно лепит `auto` к packet-up (т.к. stream-one был сломан). После фикса 1.1 `auto` должен резолвиться как Xray: **REALITY → stream-one**, иначе packet-up. Тогда конфиг из жалобы работает без правок пользователя.

---

## 2. Цель

`vless/vmess/trojan` outbound с `transport.type=xhttp`, `mode:stream-one` — поднимает рабочее соединение к Xray XHTTP-серверу (handshake + DNS + HTTPS + загрузка), в т.ч. поверх Reality. `mode:auto` при включённом Reality резолвится в `stream-one` (как Xray). `mode:packet-up`/`stream-up` — без регрессий.

---

## 3. Требования

### 3.1 Фикс `stream-one` (главное)
- `stream-one` шлёт запрос на **голый нормализованный путь** (`<path>`), **без** `sessionId` в URL (и нигде — ни query, ни header). Метод — `POST` (как сейчас), тело — uplink-pipe, downlink — response.Body того же запроса (late-binding уже корректен — `streamConn.created`).
- `requestURL()` при **пустом** наборе элементов обязан вернуть голый `<path>` **без** trailing-slash. Сейчас `requestURL()` с пустым elem даёт `<path>/` (ловушка `strings.Join([], "/")==""` → `c.path + "/"`), что отличается от Xray/hiddify (`<path>`).
- `stream-up` и `packet-up` URL **не трогаем** — они законно используют sessionId (`conn.go:48,52,86,236`).

### 3.2 `mode=auto` как Xray
- `auto` + **Reality включён** → `stream-one`.
- `auto` + Reality выключен → `packet-up` (текущая совместимая ветка; download-settings у нас нет — stream-up в auto не выбираем).
- REALITY-признак определяется **в конструкторе** (`NewClient`), один раз, и сохраняется на `Client` (в `DialContext` tlsConfig вне области видимости).

### 3.3 Изоляция REALITY-детекта (граница build-tag)
- `*tls.RealityClientConfig`/`*tls.KTLSClientConfig` объявлены под `//go:build with_utls`, а пакет `v2rayxhttp` — под `with_xhttp`. **Запрещён прямой type-assert** на with_utls-типы из v2rayxhttp: это введёт жёсткую зависимость `with_xhttp → with_utls` и сломает сборку с `with_xhttp` без `with_utls` (нарушение §3.2 CONSTITUTION — фича за своим тегом).
- Детект REALITY делать **без прямой ссылки на with_utls-тип**: по имени конкретного типа (`reflect`/`fmt %T`, сопоставление с суффиксом `RealityClientConfig`) либо иным способом, не вводящим импорт-связь между тегами. Способ фиксируется в PLAN.

### 3.4 Что НЕ трогаем (доказано в разведке)
- **ALPN.** Наш форсинг `["h2"]` → uTLS добавляет `http/1.1` → `["h2","http/1.1"]`. Xray `decideHTTPVersion` для списка длиной ≠1 → h2; reality всегда h2. Форсинг benign, а h2 для bidirectional stream-one **обязателен**. Оставляем как есть.
- **Padding.** `x_padding` в query внутри `Referer` совпадает с Xray (client request direction). Не трогаем.
- **Submodule, server/inbound** — вне scope.

---

## 4. Критерии приёмки

- `sing-box check -c` принимает `vless + reality + xhttp + mode:stream-one` и `mode:auto` (есть/будет тест-конфиг в `lx-test/config`).
- **Лайв:** реальный Xray XHTTP-сервер (reality-нода) — `mode:stream-one` поднимает соединение (handshake + DNS + HTTPS-страница + загрузка); `mode:auto` на той же ноде даёт идентичный результат (резолвится в stream-one). packet-up/stream-up — без регрессий.
- Сборка **с** `with_xhttp` **без** `with_utls` — компилируется (изоляция тега не нарушена).
- Сборка без `with_xhttp` = поведение upstream (xhttp отвергается).
- `go vet` (lx-теги) и `gofmt -l` по затронутым файлам — чисто. `go build ./...` без тегов — ок.
- Ребейз-зона не расширяется: правки только в новых файлах пакета `v2rayxhttp` и (возможно) комментарий в `option/v2ray_xhttp.go`. Upstream-файлы — не трогаем.

---

## 5. Вне скоупа

- XHTTP **server/inbound**, xmux/мультиплекс, `downloadSettings`/asymmetric transport.
- Переключение HTTP-версии (h1/h3) и отказ от h2-only — our транспорт остаётся h2-only (для stream-one это и нужно).
- Маппинг `type=xhttp` в лаунчере (`singbox-launcher`) — отдельный репозиторий.
- ALPN-рефактор (benign, см. §3.4).

---

## 6. Ссылки

- [Xray-core splithttp dialer.go / client.go / hub.go](https://github.com/XTLS/Xray-core/tree/main/transport/internet/splithttp) — проводной контракт.
- [Xray issue #5635](https://github.com/XTLS/Xray-core/issues/5635) — «auto+reality → unexpected response version; stream-one работает».
- [XHTTP: Beyond REALITY (#4113)](https://github.com/XTLS/Xray-core/discussions/4113) — семантика auto.
- [002 SPEC / IMPLEMENTATION_REPORT](../002-F-C-XHTTP_CLIENT_TRANSPORT/) — исходная фича и зафиксированный stream-one bug.
