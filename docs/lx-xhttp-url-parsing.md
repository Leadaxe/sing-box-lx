# Разбор `vless://…type=xhttp` → sing-box transport (для парсера ссылок)

> Справочник для команд **Android-клиента** и **лаунчера**: как превратить VLESS-ссылку
> с XHTTP-транспортом в `outbound.transport` конфига sing-box-lx (тег сборки `with_xhttp`).
> Источник полей — [SPECS/002-XHTTP_CLIENT_TRANSPORT/PARAM_MAP.md](../SPECS/002-XHTTP_CLIENT_TRANSPORT/PARAM_MAP.md).
> Версия транспорта: SPEC 002 v2 (полная клиентская поддержка).

---

## 0. TL;DR

Ссылка вида:

```
vless://<uuid>@<host>:<port>?type=xhttp&<params...>[&extra=<urlencoded-json>]#<remark>
```

даёт `outbound`:

```jsonc
{
  "type": "vless",
  "server": "<host>",
  "server_port": <port>,
  "uuid": "<uuid>",
  "flow": "",                       // XHTTP несовместим с xtls-rprx-vision → flow всегда пустой
  "tls": { ... },                   // из security/sni/fp/pbk/sid/alpn (см. §3)
  "transport": {
    "type": "xhttp",
    ...                             // из xhttp-параметров и extra (см. §2)
  }
}
```

**Два источника XHTTP-полей в URL:**
1. Плоские query-параметры (`path`, `mode`, `host`, …).
2. Параметр **`extra`** — это **URL-encoded JSON** с дополнительными полями (`scMaxEachPostBytes`,
   `xPaddingBytes`, `noGRPCHeader`, …). Его надо: `urldecode` → `JSON.parse` → влить в transport.

---

## 1. Алгоритм парсера (по шагам)

1. Срезать схему `vless://`, отделить `#remark` (фрагмент) — это только подпись ноды.
2. `userinfo@host:port` → `uuid` / `server` / `server_port`.
3. Разобрать query-string в map. **Все значения percent-decoded.**
4. Если `type` (он же может прийти как `transport`/`net` в других форматах) != `xhttp` — это не наш транспорт, парсить по другой ветке.
5. Если есть `extra` → `JSON.parse(urldecode(extra))` и слить ключи в ту же map (extra имеет приоритет для своих ключей).
6. Собрать `tls` (§3) и `transport` (§2) по таблицам ниже.
7. Поля, которых нет в URL, **не выставлять** — у транспорта корректные дефолты (см. колонку «дефолт»).

---

## 2. Маппинг XHTTP-параметров → `transport`

JSON-ключи sing-box — **snake_case**. Источник в URL — camelCase (как в Xray/extended).

### 2.1 Базовые (приходят как плоские query ИЛИ в `extra`)

| URL-параметр | → transport JSON | Тип | Дефолт | Примечание |
|--------------|------------------|-----|--------|------------|
| `host`       | `host`           | str | SNI/server | HTTP Host header |
| `path`       | `path`           | str | `/`    | префикс пути; **обрезать `?…` хвост** (см. §4) |
| `mode`       | `mode`           | str | `auto` | `auto`\|`packet-up`\|`stream-up`\|`stream-one` |
| `xPaddingBytes` | `x_padding_bytes` | str | `100-1000` | формат `"min-max"` или одиночное число |
| `noGRPCHeader` | `no_grpc_header` | bool | `false` | |
| (headers)    | `headers`        | obj | —      | произвольные доп. заголовки (если клиент их хранит) |

### 2.2 Placement / keys (расширенные, v2)

| URL-параметр | → transport JSON | Тип | Дефолт | Допустимые |
|--------------|------------------|-----|--------|------------|
| `sessionPlacement`   | `session_placement`   | str | `path` | path\|query\|header\|cookie |
| `sessionKey`         | `session_key`         | str | `X-Session`/`x_session` | |
| `seqPlacement`       | `seq_placement`       | str | `path` | path\|query\|header\|cookie |
| `seqKey`             | `seq_key`             | str | `X-Seq`/`x_seq` | |
| `uplinkDataPlacement`| `uplink_data_placement` | str | `auto` | body\|auto\|header\|cookie |
| `uplinkDataKey`      | `uplink_data_key`     | str | `X-Data`/`x_data` | |
| `uplinkChunkSize`    | `uplink_chunk_size`   | str | (зависит от placement) | `"min-max"` |
| `uplinkHTTPMethod`   | `uplink_http_method`  | str | `POST` | upper-case; `GET` только в packet-up |

### 2.3 X-Padding obfs (расширенные, v2)

| URL-параметр | → transport JSON | Тип | Дефолт | Допустимые |
|--------------|------------------|-----|--------|------------|
| `xPaddingObfsMode`   | `x_padding_obfs_mode` | bool | `false` | |
| `xPaddingKey`        | `x_padding_key`       | str  | `x_padding` | |
| `xPaddingHeader`     | `x_padding_header`    | str  | `X-Padding` | |
| `xPaddingPlacement`  | `x_padding_placement` | str  | `queryInHeader` | cookie\|header\|query\|queryInHeader |
| `xPaddingMethod`     | `x_padding_method`    | str  | `repeat-x` | repeat-x\|tokenish |

### 2.4 Packet-up tuning (обычно приходят в `extra`)

| URL-параметр (extra) | → transport JSON | Тип | Дефолт |
|----------------------|------------------|-----|--------|
| `scMaxEachPostBytes`   | `sc_max_each_post_bytes`   | str (`"min-max"`) | `1000000-1000000` |
| `scMinPostsIntervalMs` | `sc_min_posts_interval_ms` | str (`"min-max"`) | `30-30` |

> ⚠️ В `extra` эти значения часто приходят **числом** (`"scMaxEachPostBytes":"1000000"`,
> `"scMinPostsIntervalMs":30.0`). Транспорт sing-box-lx ждёт **строку `"min-max"`** — превратить
> одиночное число `N` в строку `"N-N"` (или просто `"N"` — парсер примет и то, и то). Дробную часть
> у `30.0` отбросить → `"30"`.

### 2.5 Игнорируемые / серверные

| URL-параметр | Действие |
|--------------|----------|
| `scMaxConcurrentPosts` | **Не маппить.** Клиент sing-box-lx шлёт upload-POST последовательно; поле не поддержано (см. §6). Игнорировать молча. |
| `serverMaxHeaderBytes`, `noSSEHeader`, `scMaxBufferedPosts`, `scStreamUpServerSecs` | server-only. Можно влить как `server_max_header_bytes`/`no_sse_header`/`sc_max_buffered_posts`/`sc_stream_up_server_secs` (клиент их принимает, но игнорирует) — или просто опустить. |
| `fragment`, `fm`, `fragment=...` | TLS-фрагментация (Xray-специфика). **Не часть XHTTP.** Маппить в свою TLS-fragment-фичу, если есть; иначе опустить. |
| `flow` | Для XHTTP всегда пустой (vision несовместим). |

---

## 3. TLS / Reality (из общих VLESS-параметров)

| URL-параметр | → JSON | Примечание |
|--------------|--------|------------|
| `security=tls`     | `tls.enabled=true` | |
| `security=reality` | `tls.enabled=true` + `tls.reality.enabled=true` | |
| `security=none` / отсутствует | без `tls` (plaintext h2c) | редкие plain-XHTTP ноды |
| `sni`              | `tls.server_name` | |
| `fp`               | `tls.utls.fingerprint` (+ `tls.utls.enabled=true`) | `chrome`/`firefox`/… |
| `alpn`             | `tls.alpn` (split по `,`) | напр. `h2,http/1.1` → `["h2","http/1.1"]` |
| `pbk`              | `tls.reality.public_key` | только при reality |
| `sid`              | `tls.reality.short_id` | только при reality |
| `spx`              | (Xray spiderX) — у sing-box нет аналога, опустить | |
| `allowInsecure` / `insecure=1` | `tls.insecure=true` | |

---

## 4. Подводные камни (обязательно учесть)

1. **`path` с query-хвостом.** Реальные ноды дают `path=/GaMeOpTiMiZeR?ed=2048`. Часть после `?` — это
   НЕ путь; либо отрезать (`path` = `/GaMeOpTiMiZeR`), либо сохранить как есть, если ваш клиент это умеет.
   sing-box-lx сам нормализует путь, но `?` внутри `path` лучше срезать на стороне парсера.
2. **`extra` — это JSON, не query.** Сначала `urldecode`, потом `JSON.parse`. Не пытаться парсить как `&k=v`.
3. **Числа vs строки в `extra`.** `scMaxEachPostBytes`/`scMinPostsIntervalMs` приходят числами →
   привести к строке `"min-max"` (см. §2.4).
4. **`mode=auto` сам резолвится в транспорте** (reality→stream-one, иначе→packet-up). Парсеру **не нужно**
   подменять `auto` на конкретный режим — передавать `auto` как есть.
5. **`flow` всегда пустой** для XHTTP. Если в ссылке `flow=xtls-rprx-vision` — это ошибка ноды для XHTTP;
   ставить `flow=""`.
6. **camelCase → snake_case** — не передавать camelCase-ключи в JSON sing-box, он их не поймёт.

---

## 5. Готовые примеры (из реальных подписок)

### Пример A — Reality + auto (минимальный целевой кейс)

URL:
```
vless://4b5cdcab-289e-4d9a-8ebd-f70a4f49db6a@sup.le3service.ir:443?mode=auto&path=/&security=reality&encryption=none&pbk=cmPAZWGaEWFOPF92El1peuQFoScxyS6XsGADu8nhjVc&host=sup.le3service.ir&fp=chrome&spx=/w22l0muhE4dqe8u&type=xhttp&sni=varzesh3.com&sid=5f14f1185c#France
```

→ sing-box:
```jsonc
{
  "type": "vless",
  "server": "sup.le3service.ir",
  "server_port": 443,
  "uuid": "4b5cdcab-289e-4d9a-8ebd-f70a4f49db6a",
  "flow": "",
  "tls": {
    "enabled": true,
    "server_name": "varzesh3.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": {
      "enabled": true,
      "public_key": "cmPAZWGaEWFOPF92El1peuQFoScxyS6XsGADu8nhjVc",
      "short_id": "5f14f1185c"
    }
  },
  "transport": {
    "type": "xhttp",
    "host": "sup.le3service.ir",
    "path": "/",
    "mode": "auto"
  }
}
```

### Пример B — TLS + packet-up с `extra` (tuning-поля)

URL (фрагмент с `extra`):
```
vless://c59eb5ed-…@199.232.244.214:443?type=xhttp&mode=packet-up&security=tls&sni=manage.fastly.com&host=oh6.global.ssl.fastly.net&path=%2F&alpn=h3&fp=chrome&encryption=none&extra=%7B%22scMaxEachPostBytes%22%3A%221000000%22%2C%22scMaxConcurrentPosts%22%3A100.0%2C%22scMinPostsIntervalMs%22%3A30.0%2C%22xPaddingBytes%22%3A%22100-1000%22%2C%22noGRPCHeader%22%3Afalse%7D#France
```

`extra` после `urldecode` + `JSON.parse`:
```json
{
  "scMaxEachPostBytes": "1000000",
  "scMaxConcurrentPosts": 100.0,
  "scMinPostsIntervalMs": 30.0,
  "xPaddingBytes": "100-1000",
  "noGRPCHeader": false
}
```

→ sing-box transport (числа из extra приведены к `"min-max"`-строкам; `scMaxConcurrentPosts` отброшен):
```jsonc
{
  "type": "xhttp",
  "host": "oh6.global.ssl.fastly.net",
  "path": "/",
  "mode": "packet-up",
  "x_padding_bytes": "100-1000",
  "sc_max_each_post_bytes": "1000000-1000000",
  "sc_min_posts_interval_ms": "30-30",
  "no_grpc_header": false
}
```
(плюс `tls.enabled=true`, `tls.server_name="manage.fastly.com"`, `tls.utls.fingerprint="chrome"`, `tls.alpn=["h3"]`)

### Пример C — obfs-режим (расширенный, как настраивают анти-DPI ноды)

Если нода-сервер настроена на obfs (поля в URL приходят плоскими или в `extra`):
```
...&type=xhttp&mode=packet-up&xPaddingObfsMode=true&xPaddingPlacement=header&xPaddingMethod=tokenish&sessionPlacement=header&seqPlacement=query&uplinkDataPlacement=header...
```
→
```jsonc
{
  "type": "xhttp",
  "mode": "packet-up",
  "x_padding_obfs_mode": true,
  "x_padding_placement": "header",
  "x_padding_method": "tokenish",
  "session_placement": "header",
  "seq_placement": "query",
  "uplink_data_placement": "header"
}
```

---

## 6. Известные ограничения клиента (что НЕ маппить)

- `scMaxConcurrentPosts` — параллельные upload-POST не реализованы (последовательная отправка). Игнорировать.
- `downloadSettings` (асимметричный download-транспорт) — не поддержан; `mode=auto`+reality+downloadSettings
  у нас всё равно даст stream-one, не stream-up.
- `spx` (spiderX), Xray browser-dialer — нет аналога.

---

## 7. Чек-лист для интегратора

- [ ] `type=xhttp` распознаётся как XHTTP-транспорт.
- [ ] `extra` декодируется как URL-encoded JSON и вливается в transport.
- [ ] Числовые `sc*`-поля из `extra` → строка `"min-max"`.
- [ ] camelCase → snake_case по таблицам §2.
- [ ] `path` с `?`-хвостом обрезается/обрабатывается.
- [ ] `security=reality` → `tls.reality.{public_key,short_id}` из `pbk`/`sid`.
- [ ] `mode=auto` передаётся как есть (резолвится в ядре).
- [ ] `flow=""` для XHTTP.
- [ ] `scMaxConcurrentPosts` и server-only поля игнорируются (или приняты-но-неактивны).
- [ ] Результат проходит `sing-box check -c`.
