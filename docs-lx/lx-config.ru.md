# sing-box-lx — конфигурация фич форка

> 🌐 English version: **[lx-config.md](lx-config.md)**.

`sing-box-lx` — это upstream [sing-box](https://github.com/SagerNet/sing-box) плюс небольшой набор **клиентских** фич, каждая за своим build-тегом:

| Фича | Build-тег | Где живёт в конфиге | Входит в |
|------|-----------|---------------------|----------|
| **XHTTP** транспорт (совместим с Xray) | `with_xhttp` | `transport.type: "xhttp"` на VLESS / VMess / Trojan outbound | desktop + mobile |
| **AmneziaWG 2.0** (AWG2) | `with_awg` | доп. поля на `wireguard` **endpoint** | desktop + mobile |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic`+`with_gvisor` | `outbounds[].type: "masque"` | desktop + mobile |
| **Idle-suspend** (SPEC 020) | `with_lx_idle_suspend` | `route.lx_idle_suspend` (+ `lx_idle_suspend_reachable`, `lx_idle_teardown`) | **только mobile** (AAR) |
| **Группа DNS-серверов** (SPEC 033/035) | — (всегда в сборке) | `dns.servers[].type: "group"` | desktop + mobile |
| **VLESS `encryption`** (SPEC 032) | — (всегда в сборке) | `encryption` на `vless`-outbound | desktop + mobile |
| **Демон `lxd`** (SPEC 055–057, 063–068) | `with_lxd` | не ключ конфига — подкоманда `sing-box lxd` + `<state-dir>/daemon.json`; см. [lxd-daemon.ru.md](lxd-daemon.ru.md) | desktop / сервер (**не** Win7, **не** AAR) |

Собрать desktop/CLI бинарь: `make -f Makefile.lx lx-build` (выход `sing-box`, версия `…-lx.N`) — включает `with_xhttp` + `with_awg` (+ `with_lx_command`, `with_lxd`), но **не** `with_lx_idle_suspend`.
Без тега фича отсутствует: `xhttp`-транспорт или AWG-поле отклоняется при загрузке с явной ошибкой (без молчаливого отката).

**`with_lx_idle_suspend` — только для mobile** и добавляется лишь в Android/iOS AAR
(`cmd/internal/build_libbox`), не в desktop `LX_TAGS`. Он гасит простаивающие +
недостижимые WireGuard/AmneziaWG-эндпоинты, освобождая их recv-буферы (держатель
GC-нагрева / RAM, ~8 МБ каждый там, где `BatchSize=128` — Android/Linux; замерено на
устройстве: 8 эндпоинтов усыплены → освобождено 134 МБ). На десктопе `BatchSize` мал,
экономить почти нечего; чтобы не было молчаливого расхождения, desktop/CLI-бинарь,
которому дали конфиг с `route.lx_idle_suspend`, **падает при старте** с ошибкой
`route.lx_idle_suspend is set but this build lacks idle-suspend support; rebuild with
-tags with_lx_idle_suspend (mobile-only feature)`. См. [фичу ENERGY](../SPECS/FEATURES/008-ENERGY/FEATURE.md).

Смежные ключи (ревизия 2026-07-15): `route.lx_idle_suspend_reachable` — опциональное
второе, более длинное окно простоя, после которого гасятся и *достижимые* эндпоинты
(члены пула, выбранный узел, final); `route.lx_idle_teardown` — третий уровень:
сколько эндпоинт может *спать* до полного сноса (Close, освобождается и gVisor
netstack; пробуждение = rebuild ~0.5–1 с; дефолт = reachable-окну);
`urltest.passive_check` — пропуск health-проб,
пока свежий успешный TCP-дайл доказывает живость узла. Полная энергомодель,
таймлайны и рекомендованный мобильный конфиг — в **[lx-energy.ru.md](lx-energy.ru.md)**.

> ⚠️ Все ключи/UUID ниже — **заглушки**. Никогда не коммитьте реальные приватные ключи / pre-shared-ключи в репозиторий.

> 🌐 English version: **[lx-config.md](lx-config.md)**.

---

## 0. Все поля разом (исчерпывающий пример)

Один конфиг, несущий все поля **outbound-фич** — XHTTP-транспорт, AmneziaWG 2.0 endpoint,
masquerade-сахар `id`/`ip`/`ib`, VLESS `encryption` и `round_robin`-балансировщик `urltest`
(у MASQUE, DNS-группы и ключей `route.lx_idle_*` — свои примеры в [§4](#4-masque-outbound--cloudflare-warp-spec-021),
[§5](#5-группа-dns-серверов-spec-033035) и [lx-energy.ru.md](lx-energy.ru.md)).
Это **справочник «всё сразу»**, а не рекомендуемый конфиг: многие поля взаимоисключающи
(например, сахар `id`/`ip`/`ib` против написанного вручную `i1`) либо серверные и игнорируются
клиентом — такие помечены прямо в комментариях. Для рабочей настройки скопируйте только нужный
блок и прочитайте его раздел ниже. В каждом комментарии — **значение по умолчанию** и **допустимые значения**.

```jsonc
{
  "outbounds": [
    // ─────────────────────────────────────────────────────────────────────────
    // XHTTP-транспорт (§1) — крепится к VLESS / VMess / Trojan outbound.
    // Требует build-тег with_xhttp.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "vless",
      "tag": "xhttp-out",
      "server": "example.com",
      "server_port": 443,
      "uuid": "00000000-0000-0000-0000-000000000000",
      "encryption": "",                         // по умолчанию: "" (выкл). PQ-слой VLESS (§6):
                                                //   "mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>….<ключ>"
      "tls": {
        "enabled": true,
        "server_name": "example.com",
        "utls": { "enabled": true, "fingerprint": "chrome" },
        "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
      },
      "transport": {
        "type": "xhttp",                        // селектор — должно быть "xhttp"

        // ── базовые (v1) ──
        "mode": "auto",                         // auto | packet-up | stream-up | stream-one.
                                                //   auto → stream-one на Reality, иначе packet-up
        "host": "example.com",                  // по умолчанию: TLS SNI / сервер. Переопределяет заголовок Host
        "path": "/xhttp",                       // по умолчанию: "" (корень). Session id / seq дописываются сегментами пути
        "headers": { "X-Foo": "bar" },          // по умолчанию: нет. Доп. заголовки на каждый запрос
        "x_padding_bytes": "100-1000",          // по умолчанию: "100-1000". "min-max" или одно число — длина паддинга
        "no_grpc_header": false,                // по умолчанию: false. Не слать Content-Type: application/grpc в stream-one/stream-up

        // ── размещение session / seq (v2) ──
        "session_placement": "path",            // по умолчанию: path. path | query | header | cookie
        "session_key": "",                      // по умолчанию: X-Session (header) / x_session (query|cookie); для path не нужен
        "seq_placement": "path",                // по умолчанию: path. path | query | header | cookie (packet-up)
        "seq_key": "",                          // по умолчанию: X-Seq (header) / x_seq (query|cookie); для path не нужен

        // ── размещение uplink-данных (v2, packet-up) ──
        "uplink_data_placement": "auto",        // по умолчанию: auto (== body). body | auto | header | cookie.
                                                //   header/cookie допустимы ТОЛЬКО в packet-up
        "uplink_data_key": "",                  // по умолчанию: X-Data (header) / x_data (cookie); "" для body
        "uplink_chunk_size": "",                // по умолчанию: cookie 2048-3072 / header 3000-4000 / иначе = sc_max_each_post_bytes.
                                                //   "min-max" в base64-символах, min не ниже 64
        "uplink_http_method": "POST",           // по умолчанию: POST. Приводится к верхнему регистру. GET только в packet-up

        // ── обфускация X-Padding (v2) — всё ниже работает только при obfs_mode=true ──
        "x_padding_obfs_mode": false,           // по умолчанию: false. Главный переключатель. false → legacy x_padding в Referer
        "x_padding_placement": "queryInHeader", // по умолчанию: queryInHeader. cookie | header | query | queryInHeader
        "x_padding_key": "x_padding",           // по умолчанию: x_padding. Имя cookie/query-параметра
        "x_padding_header": "X-Padding",         // по умолчанию: X-Padding. Имя заголовка (placement header / queryInHeader)
        "x_padding_method": "repeat-x",         // по умолчанию: repeat-x. repeat-x ('X'*N) | tokenish (base62, длина под HPACK)

        // ── тюнинг packet-up (v2) ──
        "sc_max_each_post_bytes": "1000000-1000000", // по умолчанию: "1000000-1000000". Порог дробления POST ("min-max")
        "sc_min_posts_interval_ms": "30-30",    // по умолчанию: "30-30". Анти-burst задержка между POST, мс ("min-max")

        // ── принимаются, но ИГНОРИРУЮТСЯ клиентом (только для симметрии конфига/ссылки) ──
        "sc_max_concurrent_posts": 0,           // ИГНОР — legacy-ручка Xray; клиент держит один POST в полёте
        "server_max_header_bytes": 0,           // ИГНОР — только сервер (http.Server.MaxHeaderBytes)
        "no_sse_header": false,                 // ИГНОР — только сервер (SSE Content-Type на downlink)
        "sc_max_buffered_posts": 0,             // ИГНОР — только сервер (глубина буфера переупорядочивания upload)
        "sc_stream_up_server_secs": ""          // ИГНОР — только сервер (интервал keepalive на stream-up)
      }
    },

    // ─────────────────────────────────────────────────────────────────────────
    // AmneziaWG 2.0 endpoint (§2) — wireguard endpoint с обфускацией.
    // Требует build-тег with_awg. Все AWG-поля на КОРНЕ endpoint
    // (ни одного на peer), повторяя секцию [Interface] файла awg-quick .conf.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "wireguard",
      "tag": "awg-out",
      "system": false,
      "mtu": 1280,                              // ниже обычного WG (transport junk s4); ядро ставит 1280 по умолчанию, если задан s4
      "address": ["10.0.0.2/32"],
      "private_key": "<client-private-key-base64>",

      // ── junk-пакеты перед handshake ──
      "jc": 10,                                 // по умолчанию: 0 (не задано). Количество junk-пакетов
      "jmin": 50,                               // по умолчанию: 0. Мин. размер каждого junk-пакета
      "jmax": 100,                              // по умолчанию: 0. Макс. размер каждого junk-пакета (держать ниже path MTU)

      // ── junk в handshake-сообщениях (s1/s2) + junk-size AWG 2.0 (s3/s4) ──
      "s1": 20,                                 // по умолчанию: 0. Junk перед handshake-сообщением INIT
      "s2": 20,                                 // по умолчанию: 0. Junk перед handshake-сообщением RESPONSE
      "s3": 60,                                 // по умолчанию: 0. Параметр junk-size AWG 2.0
      "s4": 60,                                 // по умолчанию: 0. Параметр junk-size AWG 2.0

      // ── magic headers: одно uint32 (AWG 1.x) ЛИБО диапазон "min-max" (AWG 2.0) ──
      // диапазоны НЕ должны пересекаться; 0 / "" = не задано (считается WG-дефолтом 1/2/3/4)
      "h1": 1234567890,                         // или "43613244-384550127"   — WG-сообщение тип 1
      "h2": 1234567891,                         // или "826869626-2105069164" — тип 2
      "h3": 1234567892,                         // или "2124774725-2141151992" — тип 3
      "h4": 1234567893,                         // или "2144594503-2146278491" — тип 4

      // ── CPS-приманки i1..i5 (регистрозависимы; шлются по порядку до handshake) ──
      // теги: <b 0xHEX> байты, <c> счётчик, <t> таймстамп, <r N> случайные байты,
      //       <rc N> случайные символы, <rd N> случайные цифры.
      // i1 ВЗАИМОИСКЛЮЧАЕТСЯ с сахаром id/ip/ib ниже — используйте что-то одно.
      "i1": "<b 0x000100002112a442><r 12>",     // по умолчанию: "". CPS-пакет 1
      "i2": "<b 0x010100002112a442><r 12>",     // по умолчанию: "". CPS-пакет 2
      "i3": "<r 24>",                            // по умолчанию: "". CPS-пакет 3
      "i4": "",                                  // по умолчанию: "". CPS-пакет 4
      "i5": "",                                  // по умолчанию: "". CPS-пакет 5

      // ── masquerade-сахар (§2 → id/ip/ib) — генерирует i1 за вас.
      //    ВЗАИМОИСКЛЮЧАЕТСЯ с явным i1 выше. Показан для справки;
      //    в реальном конфиге используйте ЛИБО i1..i5, ЛИБО id/ip/ib, не оба. ──
      "id": "www.google.com",                   // по умолчанию: "". Masquerade-домен (SNI/QNAME/SIP-host). Обязателен для ip=quic
      "ip": "quic",                             // по умолчанию: "". quic | dns | stun | sip
      "ib": "chrome",                           // по умолчанию: "". chrome | firefox | curl. Имеет смысл только с ip=quic

      "peers": [
        {
          "address": "server.example.com",
          "port": 51821,
          "public_key": "<server-public-key-base64>",
          "pre_shared_key": "<preshared-key-base64>",
          "allowed_ips": ["0.0.0.0/0", "::/0"],
          "persistent_keepalive_interval": 25
        }
      ]
    },

    // ─────────────────────────────────────────────────────────────────────────
    // Балансировка urltest round_robin (§3). Поля конфига всегда доступны;
    // клиентский метод GetPool требует with_lx_command.
    // ─────────────────────────────────────────────────────────────────────────
    {
      "type": "urltest",
      "tag": "auto",
      "outbounds": ["xhttp-out", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
      "url": "https://www.gstatic.com/generate_204",
      "interval": "15m",
      "passive_check": false,                   // по умолчанию: false. Свежий успешный TCP-дайл
                                                //   считается доказательством живости (< interval) — пробы молчат
      "mode": "round_robin",                    // по умолчанию: least_test. least_test | round_robin
      "balancer": {                             // допустим только с mode: round_robin
        "pool": 3,                              // по умолчанию: 3. 0/отсутствие → 3; эффективный = min(pool, #outbounds)
        "pool_tolerance": 0,                    // по умолчанию: 0 (мс). 0 = держать живой пул; >0 = топ-N по задержке с гистерезисом
        "sticky_hash": ["process", "domain"]    // по умолчанию: ["process","domain"]. Компоненты:
                                                //   process | domain | source_ip | dest_ip | dest_port.
                                                //   ОТКЛЮЧИТЬ через sentinel ["none"] — никогда не пустой []
      }
    }
  ]
}
```

> **Счёт полей:** 26 XHTTP + 21 AmneziaWG (вкл. `id`/`ip`/`ib`) + 1 VLESS (`encryption`) +
> 6 `urltest` (`mode`, `passive_check` + `balancer{pool,pool_tolerance,sticky_hash}`). Взаимоисключающие / игнорируемые поля помечены
> в комментариях выше; разделы ниже дают семантику каждого поля, подводные камни и статус
> живой проверки.

---

## 1. XHTTP-транспорт

XHTTP (Xray «splithttp»/«xhttp») — это v2ray-транспорт, туннелирующий прокси поверх обычных HTTP/2-запросов. Крепится к VLESS / VMess / Trojan через общий блок `transport` и сочетается с TLS, включая **Reality**. (XHTTP несовместим с XTLS-Vision — это ограничение протокола, не наше.)

### Поля (`transport`)

Дефолтная форма на проводе (всё ниже в значениях по умолчанию) **байт-в-байт совпадает
с лайв-проверенным v1-клиентом**, поэтому существующие конфиги не затрагиваются — все v2-поля опциональны.

**Базовые (v1):**

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `type` | string | — | должно быть `"xhttp"` |
| `mode` | string | `auto` | `auto` \| `packet-up` \| `stream-up` \| `stream-one`. **`auto` → `stream-one` на Reality-TLS, иначе `packet-up`** — то же правило, что у Xray. Оставляйте `auto`, если сервер не требует иного; выбирайте явно, только когда знаете, чего ждёт сервер. |
| `host` | string | TLS SNI / сервер | переопределяет HTTP-заголовок `Host` |
| `path` | string | `""` (корень) | префикс пути запроса; session id (и, для `packet-up`, sequence-номер upload) дописываются сегментами пути, когда их placement = `path` |
| `headers` | object | — | доп. заголовки на каждый XHTTP-запрос |
| `x_padding_bytes` | string | `"100-1000"` | включающий **диапазон** длины значения паддинга (`"min-max"` или одно число). Управляет и длиной legacy `x_padding` в Referer, и длиной паддинга в obfs-режиме |
| `no_grpc_header` | bool | `false` | не отправлять заголовок `Content-Type: application/grpc`, который по умолчанию несут запросы с телом (`stream-one`, `stream-up`) — аналог `NoGRPCHeader` у Xray. Включать только если сервер отвергает gRPC-тип контента |

**Размещение session / seq (v2)** — где несутся session id и (packet-up) sequence-номер upload:

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `session_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie` |
| `session_key` | string | `X-Session` (header) / `x_session` (query\|cookie) | имя, несущее session id при placement ≠ `path`; для `path` не используется |
| `seq_placement` | string | `path` | `path` \| `query` \| `header` \| `cookie`. Для `path` seq — **второй** дописанный сегмент (session id первый — порядок значим) |
| `seq_key` | string | `X-Seq` (header) / `x_seq` (query\|cookie) | имя, несущее seq при placement ≠ `path`; для `path` не используется |

**Размещение uplink-данных (v2, packet-up)** — куда идёт payload upload'а:

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `uplink_data_placement` | string | `auto` | `body` \| `auto` (== body) \| `header` \| `cookie`. `header`/`cookie` допустимы **только в `packet-up`** (иначе ошибка); несут payload как `base64.RawURLEncoding`, нарезая на заголовки `<key>-<i>` / cookie `<key>_<i>` |
| `uplink_data_key` | string | `X-Data` (header) / `x_data` (cookie) | базовое имя для нарезанного header/cookie-payload; `""` для body |
| `uplink_chunk_size` | string | cookie `2048-3072`, header `3000-4000`, иначе `= sc_max_each_post_bytes` | `"min-max"` диапазон (в base64-символах) каждого чанка; min не ниже 64 |
| `uplink_http_method` | string | `POST` | HTTP-метод для запросов **upload** (download всегда GET); приводится к верхнему регистру; `GET` допустим только в `packet-up` |

**Обфускация X-Padding (v2)** — активна только при `x_padding_obfs_mode` = `true`; иначе используется legacy-паддинг в Referer (примечание ниже):

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `x_padding_obfs_mode` | bool | `false` | главный переключатель. `false` → legacy `x_padding` в Referer. `true` → настраиваемое семейство `x_padding_*` ниже |
| `x_padding_placement` | string | `queryInHeader` | `cookie` \| `header` \| `query` \| `queryInHeader` |
| `x_padding_key` | string | `x_padding` | имя cookie/query-параметра (не используется при placement `header`) |
| `x_padding_header` | string | `X-Padding` | имя заголовка (для placement `header` / `queryInHeader`) |
| `x_padding_method` | string | `repeat-x` | `repeat-x` (N литеральных байт `X`) \| `tokenish` (base62-токен, чья HPACK-Huffman длина подогнана под ~N) |

**Тюнинг packet-up (v2):**

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `sc_max_each_post_bytes` | string | `"1000000-1000000"` | `"min-max"` диапазон одного upload-POST (порог дробления) |
| `sc_min_posts_interval_ms` | string | `"30-30"` | `"min-max"` анти-burst задержка между POST, в мс |

**Переиспользование соединений — `xmux` (SPEC 059):**

Без пула каждый XHTTP-поток платит полный хендшейк TCP + TLS (+ REALITY). `xmux` переиспользует
HTTP-соединения — и, не менее важно, именно этого ждут Xray-серверы: секция `xmux`, приходящая
из подписки, раньше молча игнорировалась, и клиент вёл себя не так, как задумал автор сервера.

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `xmux.max_concurrency` | диапазон | `1-1` | сколько потоков делят одно HTTP-соединение. Взаимоисключается с `max_connections` |
| `xmux.max_connections` | диапазон | без лимита | сколько соединений держит пул; пока пул ниже этого числа, новое соединение открывается всегда. Взаимоисключается с `max_concurrency` |
| `xmux.c_max_reuse_times` | диапазон | без лимита | сколько раз соединение может быть выдано под новый поток до вывода из пула |
| `xmux.h_max_request_times` | диапазон | `600-900` | сколько **HTTP-запросов** может пройти через соединение до вывода из пула. Считаются запросы, а не потоки — в `packet-up` один поток шлёт много upload-POST |
| `xmux.h_max_reusable_secs` | диапазон | `1800-3000` | сколько секунд соединение остаётся пригодным к переиспользованию |
| `xmux.h_keep_alive_period` | int (секунды) | `0` = дефолт | период HTTP/2 keep-alive пингов. Отрицательное — выключить. Здесь обычное число, не диапазон — как в референсе |

Диапазоны принимаются в нашей строковой форме `"min-max"` (`"600-900"`), одним числом
(`4` == `4-4`) или двухэлементным массивом (`[600, 900]`) — для конфигов, написанных под
Xray / sing-box-extended. **Каждый диапазон разыгрывается один раз, а не на запрос**:
`max_concurrency` и `max_connections` — при создании менеджера, лимиты переиспользования —
при создании каждого соединения. Дефолты действуют и без секции `xmux` вовсе. Соединение,
упершееся в лимит, выводится из пула, но **не рвётся, пока на нём живут потоки** — закрытие
откладывается до последнего. Только клиент: серверная половина и секция `download` — вне объёма.

**Принимаются, но игнорируются клиентом** (присутствуют, чтобы конфиг в форме inbound или симметричная ссылка не падали — клиент на них не реагирует): `sc_max_concurrent_posts`, `server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts`, `sc_stream_up_server_secs`.

> **Примечание (дефолтный формат на проводе):** при выключенном `x_padding_obfs_mode` (по умолчанию) паддинг несётся как `x_padding=<нули>` внутри заголовка `Referer` (дефолтное размещение Xray) — лайв-проверено против реального Xray (3x-ui). Сервер валидирует длину `x_padding` (по умолчанию 100–1000) и без неё отвечает `400`. Версии Xray клиента и сервера всё же должны совпадать (XHTTP быстро эволюционирует).

### Пример — VLESS + XHTTP + Reality

```jsonc
{
  "type": "vless",
  "tag": "xhttp-out",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
  },
  "transport": {
    "type": "xhttp",
    "mode": "stream-one",
    "host": "example.com",
    "path": "/xhttp",
    "x_padding_bytes": "100-1000"
  }
}
```

---

## 2. AmneziaWG 2.0 (AWG2)

AWG — это WireGuard + обфускация против DPI. Настраивается как обычный sing-box **`wireguard` endpoint** с дополнительными «поднятыми» полями. С `with_awg` они передаются на устройство; конфиг без единого AWG-поля — обычный WireGuard endpoint (поведение байт-в-байт как в upstream).

AWG2 = поля AWG1 **плюс** CPS-пакеты `I1`–`I5`. И клиент, и сервер должны работать на AmneziaWG с **совпадающими** параметрами (I-пакеты — это конфигурация, не согласуются). Более дружелюбный способ задать первую приманку — WireSock-style сахар [`id`/`ip`/`ib`](#masquerade-id--ip--ib-wiresock-стиль-сахар-над-i1) ниже, который генерирует `i1` за вас.

### Поля (на `wireguard` endpoint, рядом с `private_key`/`peers`/…)

| Ключ | Тип | Значение |
|------|-----|----------|
| `jc` | int | по умолчанию `0` (не задано). Количество junk-пакетов перед handshake |
| `jmin` / `jmax` | int | по умолчанию `0`. Мин. / макс. размер этих junk-пакетов |
| `s1` / `s2` | int | по умолчанию `0`. Junk перед handshake-сообщениями **INIT** / **RESPONSE** |
| `s3` / `s4` | int | по умолчанию `0`. Параметры junk-size AWG 2.0 (компаньоны `s1`/`s2`). Именно накладные расходы `s4` (на каждый transport-пакет) диктуют требование [пониженного MTU](#mtu); `s3` дополняет только cookie-reply |
| `h1` / `h2` / `h3` / `h4` | int \| `"min-max"` string | magic-header значения, переопределяющие четыре типа WireGuard-сообщений. Либо одно uint32 (`1234567890`, AWG 1.x), либо включающий диапазон-строка (`"43613244-384550127"`, диапазонные заголовки AWG 2.0) — устройство берёт случайное значение из диапазона на каждое сообщение. `0` **или** `""` = не задано (считается WG-дефолтом `1`/`2`/`3`/`4`) |
| `i1` … `i5` | string | по умолчанию `""`. CPS-приманки AWG 2.0, **регистрозависимые** строки тег-формата, шлются по порядку до handshake. `i1` обычно имитирует реальный протокол (напр. заголовок QUIC/STUN) и **взаимоисключается с сахаром [`id`/`ip`/`ib`](#masquerade-id--ip--ib-wiresock-стиль-сахар-над-i1)**. Теги: `<b 0xHEX>` статичные байты, `<c>` счётчик, `<t>` таймстамп, `<r N>` случайные байты, `<rc N>` случайные символы, `<rd N>` случайные цифры |

> **Диапазонные заголовки (AWG 2.0):** четыре диапазона `h1`–`h4` (незаданный заголовок считается своим WireGuard-дефолтом — `1`/`2`/`3`/`4`) **не должны пересекаться**, иначе устройство отклонит конфиг с `headers must not overlap`. Задавайте все четыре вместе, как делают awg2-экспорты. Обычное число `N` эквивалентно диапазону `"N-N"`; `0` означает «не задано».

### Пример — AmneziaWG 2.0 endpoint

```jsonc
{
  "type": "wireguard",
  "tag": "awg-out",
  "system": false,
  "mtu": 1280,
  "address": ["10.0.0.2/32"],
  "private_key": "<client-private-key-base64>",

  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  // одиночные значения (стиль AWG 1.x) — или диапазонные заголовки AWG 2.0, напр.
  // "h1": "43613244-384550127", "h2": "826869626-2105069164",
  // "h3": "2124774725-2141151992", "h4": "2144594503-2146278491",
  "h1": 1234567890, "h2": 1234567891, "h3": 1234567892, "h4": 1234567893,
  "i1": "<b 0x000100002112a442><r 12>",
  "i2": "<b 0x010100002112a442><r 12>",
  "i3": "<r 24>",

  "peers": [
    {
      "address": "server.example.com",
      "port": 51821,
      "public_key": "<server-public-key-base64>",
      "pre_shared_key": "<preshared-key-base64>",
      "allowed_ips": ["0.0.0.0/0", "::/0"],
      "persistent_keepalive_interval": 25
    }
  ]
}
```

### Masquerade `id` / `ip` / `ib` (WireSock-стиль сахар над `i1`)

Писать CPS-строку `i1` руками — занятие муторное. Как более дружелюбная альтернатива — с тем же
именованием, что использует [WireSock Secure Connect](https://www.wiresock.net/) — можно объявить
masquerade через **домен / протокол / браузер**, и устройство сгенерирует приманку `i1` за вас:

| Ключ | Тип | Значение |
|------|-----|----------|
| `id` | string | masquerade-**домен** (хост, выглядящий нормально для вашего региона, напр. `www.google.com`). Строгий LDH-hostname (буквы/цифры/`-`/`_`, метки ≤63, всего ≤253). Встраивается в приманку для `ip=quic` (как **SNI в ClientHello**), `ip=dns` (как QNAME) и `ip=sip` (как host) — только у `ip=stun` некуда нести hostname, и он его игнорирует. **Обязателен только для `quic`; для `dns`/`sip` при отсутствии генерируется псевдоимя; `stun` игнорирует.** При любой установке проходит LDH-валидацию (невалидные/инъекционные значения **отклоняются**) |
| `ip` | string | masquerade-**протокол**: `quic` \| `dns` \| `stun` \| `sip` |
| `ib` | string | masquerade-**браузер**: `chrome` \| `firefox` \| `curl`. Имеет смысл только с `ip=quic`, и даже тогда эффект **минимален** (см. примечание) |

Приманка шлётся до handshake, ровно как написанный вручную `i1`. Каждый профиль — это
**инициируемый клиентом** пакет в форме соответствующего протокола (формы вдохновлены
open-source референсом WireSock, `amneziawg-proxy/src/transform.rs`, но выпускаются как первый
запрос, который реально шлёт клиент, а не ответ сервера); `quic` — это специально собранный
QUIC Initial по RFC 9001, обходящий line-rate DPI:

- **`quic`** — полный **QUIC Initial (RFC 9001)**, несущий реалистичный браузероподобный
  ClientHello (с вашим `id` в SNI), **разбитый на несколько CRYPTO-фреймов вне порядка**:
  первый фрейм на проводе начинается с середины ClientHello (offset≠0), так что line-rate DPI,
  хватающий первый фрейм и полагающий offset 0, парсит мусор и пропускает (fail open), тогда
  как реальный QUIC-сервер переупорядочивает фреймы штатно. Раскладка рандомизируется на каждый
  вызов (без фиксированной кросс-юзерной сигнатуры). `ip=quic` выдаёт **один** фрагментированный
  Initial — в `i1`; `i2` заполняет только `ip=sip`, которому нужны два сообщения одного диалога.
  Это device-proven обход DPI (обычный QUIC short header был эмпирически заблокирован).
- **`dns`** — клиентский DNS-**запрос** (QR=0, QTYPE HTTPS), QNAME которого — ваш `id`, несущий
  случайные cover-байты как непрозрачную неизвестную EDNS-опцию.
- **`stun`** — WebRTC STUN **Binding Request** (magic cookie + USERNAME + ICE-CONTROLLING +
  PRIORITY + SOFTWARE + MESSAGE-INTEGRITY + FINGERPRINT).
- **`sip`** — SIP **INVITE-запрос без тела** (`i1`: request-line + Via/Max-Forwards/To/From/
  Call-ID/CSeq/Contact + `Content-Type: application/sdp` и `Content-Length: 0`, без SDP-тела)
  в паре с соответствующим провизорным ответом **`100 Trying`** того же диалога (`i2`),
  используя ваш `id` (или сгенерированный псевдо-host) как host и произносимые псевдо-имена
  пользователей.

```jsonc
{
  "type": "wireguard", "tag": "awg-out", "mtu": 1280,
  "address": ["10.0.0.2/32"], "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "www.google.com", "ip": "quic", "ib": "chrome",
  "peers": [ { "address": "engage.cloudflareclient.com", "port": 2408,
    "public_key": "<server-public-key-base64>", "allowed_ips": ["0.0.0.0/0", "::/0"] } ]
}
```

> **Примечания и ограничения.**
> - `id`/`ip`/`ib` **взаимоисключаются** с явным `i1` — задавайте что-то одно, не оба (конфиг с
>   обоими отклоняется).
> - Это **приманка**, отправляемая до handshake, а не полноценная протокольная сессия — `quic`
>   Initial никогда не завершает TLS-handshake (ему лишь нужно сделать первый пакет потока похожим
>   на легитимный старт QUIC). `id` **действительно** кладётся на провод как SNI в ClientHello
>   (DPI, публично расшифровывающий Initial, может его прочитать), так что выбирайте
>   **правдоподобный, разрешённый** домен — никогда не VPN/Cloudflare-маркер.
> - Обход DPI держится прежде всего на **фрагментации CRYPTO-фреймов**, а не на TLS-отпечатке.
>   Но `ib` его всё же выбирает: `chrome` и `firefox` дают подлинный браузерный ClientHello
>   (настоящий JA3/JA4) в сборках с поддержкой имитации TLS, а `curl` и отсутствие `ib` —
>   общий ClientHello. Без такой поддержки браузерные профили деградируют до общего.
> - `id` несётся на проводе для `quic` (SNI), `dns` (QNAME) и `sip` (host); только `ip=stun`
>   даёт приманку без hostname независимо от `id`.
> - Мотивирующий сценарий — облегчение подключений к **Cloudflare WARP**.

**📖 [Подробные примеры →](../SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md)** —
полные конфиги по каждому профилю (вкл. Cloudflare WARP), сгенерированный CPS для каждого,
руководство «какой профиль выбрать» и таблица траблшутинга с точными ошибками валидации.

### MTU

`s4` в AmneziaWG добавляет junk-байты к **каждому transport-сообщению (данные)**, поэтому AWG-endpoint требует **более низкий `mtu`, чем обычный WireGuard**. (`s3` дополняет только cookie-reply сообщения, а не пакеты данных, поэтому на бюджет MTU не влияет.) Если обфусцированный пакет превышает path MTU, ОС его отвергает, и туннель завершает handshake, но **не может слать данные**:

```
peer(…) - received handshake response
peer(…) - failed to send data packets: write udp4 …: sendmsg: message too long
```

Закладывайте накладные расходы под путь в 1500 байт:

```
mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − S4 junk-байт
```

Для `S4 = 60` это `mtu ≤ 1380`. **Используйте `1280`** (рекомендованный AmneziaWG клиентский MTU) ради запаса на меньших path MTU (PPPoE, вложенные туннели). Это не связано с handshake — слишком высокий `mtu` позволяет handshake пройти, но молча ломает передачу данных.

**Что sing-box-lx делает за вас:** если вы опускаете `mtu` на endpoint'е с `s4`, ядро ставит по умолчанию **`1280`** (вместо обычного WireGuard'овского `1408`). Если вы задали `mtu` явно и он слишком велик для junk-расходов, ядро пишет стартовое предупреждение — против консервативного бюджета **1492** байт (PPPoE), `mtu ≤ 1492 − 28 − 32 − S4`, так что оно может пометить значение на несколько байт ниже Ethernet-потолка 1500. Предупреждение рекомендательное; туннель всё равно загружается.

**Внешний сокет больше не форсирует DF (SPEC 028).** По умолчанию sing-box-lx теперь позволяет ОС IP-фрагментировать великоватую внешнюю датаграмму на `wireguard`-endpoint'е (и `masque`-outbound'е), а не дропать её — прежний дефолт ставил `IP_MTU_DISCOVER=IP_PMTUDISC_DO` (Linux/Android) / `IP_DONTFRAG` (macOS), что и порождало `sendmsg: message too long` выше, когда AWG-датаграмма (`mtu + 32 + s4 + 28`) превышала path MTU. Именно это позволяет работать **вложенным туннелям**: `masque`/`wireguard`/AWG в цепочке через `detour` в любых комбинациях, где внешняя датаграмма регулярно великовата и должна фрагментироваться. Чтобы вернуть прежнее поведение на конкретном endpoint'е, поставьте на нём `"udp_fragment": false`. Правильный подбор `mtu` (выше) по-прежнему убирает фрагментацию совсем и предпочтителен — фрагментация это подстраховка, а не цель.

Также держите `jmax` **ниже** реального path MTU: amneziawg-go предупреждает, что если размер junk-пакета достигает системного MTU, он IP-фрагментируется, что те же стеснённые пути затем дропают. Junk/сигнатурные параметры (`jc`, `s1`–`s4`, `i1`–`i5`) — это только клиентская конфигурация.

Маппинг файла `awg.conf` / awg-quick 1:1: `[Interface] PrivateKey/Address/Jc/Jmin/Jmax/S1–S4/H1–H4/I1–I5` → корень endpoint'а; `[Peer] PublicKey/PresharedKey/Endpoint/AllowedIPs/PersistentKeepalive` → `peers[0]` (`Endpoint host:port` → `address`+`port`). Строка `H1 = N` маппится в JSON-число `N`, диапазонная строка `H1 = N-M` (awg2-экспорт) — в JSON-строку `"N-M"` дословно. Если `awg.conf` опускает `MTU` или ставит WireGuard-дефолт `1420`, понизьте его для AWG2 (см. [MTU](#mtu) выше).

Рантайм обеспечивается `Leadaxe/wireguard-go` (sagernet/wireguard-go + обфускация AmneziaWG, подключён через submodule `submodules/wireguard-go`) — см. [фичу AWG2](../SPECS/FEATURES/003-AWG2/FEATURE.md).

---

## 3. Балансировка нагрузки round_robin (SPEC 019)

Upstream `urltest` всегда выбирает единственную ноду с наименьшей задержкой. sing-box-lx добавляет
**режим** `round_robin`, который ротирует трафик по фиксированному **пулу** нод — спроектирован
масштабироваться на большие списки (health-check проходит только пул, а не каждую ноду). Выбор
происходит один раз на соединение; UDP/QUIC-сессия остаётся на своей ноде. С опущенным `mode` (или
`least_test`) outbound ведёт себя ровно как upstream, и `balancer` задавать нельзя.

Метод CommandClient `GetPool` (см. [§7](#7-наблюдаемость-расширения-commandclient)) за тегом
`with_lx_command`; сами поля конфига `mode`/`balancer` доступны всегда.

### Поля (на `urltest` outbound)

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `mode` | string | `least_test` | `least_test` (поведение upstream) \| `round_robin` (ротация по пулу). `least_connection` отклоняется (round_robin статистически равномерен) |
| `passive_check` | bool | `false` | свежий успешный TCP-дайл считается доказательством живости, пока свеж (< `interval`): `least_test` пропускает целые циклы перетеста, пока выбранный узел пассивно подтверждён; `round_robin` (только при `pool_tolerance: 0`) считает подтверждённые слоты живыми без проб. Цена: более лежалые числа задержек в UI. См. [lx-energy.ru.md](lx-energy.ru.md) |
| `balancer` | object | — | параметры round_robin; **допустим только с `mode: round_robin`** (иначе ошибка). Upstream-поле `tolerance` в round_robin игнорируется — используйте `pool_tolerance` (предупреждение при старте подсказывает это, пока `pool_tolerance` не задан) |

#### Поля `balancer`

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `pool` | int | `3` | размер пула ротации. `0`/опущено → `3`; отрицательное — ошибка. Эффективный размер — `min(pool, число outbounds)`. URL-тестируется только пул каждый `interval`, поэтому список из сотен нод **не** означает сотни тестов |
| `pool_tolerance` | int (мс) | `0` | `0` = держать пул полным из **живых** нод (задержка не ранжируется), тестируя не больше нужного — дешёвый режим для больших списков. `> 0` = тестировать **все** ноды и держать самые быстрые `pool`; член заменяется только когда нода вне пула обгоняет его больше чем на `pool_tolerance` мс (гистерезис). Мёртвая нода пула держит свой слот, пока не найдётся живая замена (пул никогда не пустеет); ошибка dial никогда не меняет пул — только периодический health-check |
| `sticky_hash` | string[] | `["process","domain"]` | компоненты ключа липкости потока (см. ниже). Опущено/`[]` → дефолт. **Чтобы отключить** липкость, используйте sentinel **`["none"]`** — никогда не пустой `[]`. Компоненты: `process` \| `domain` \| `source_ip` \| `dest_ip` \| `dest_port` |

> **Подвох с `[]` (badjson).** Не пишите `"sticky_hash": []`, чтобы отключить липкость: декодер
> конфига sing-box ре-маршалит каждый outbound, и пустой JSON-массив не переживает round-trip —
> он схлопывается в «опущено», что означает *дефолт* (`["process","domain"]`, т.е. липкость
> **включена**). Используйте явный sentinel **`["none"]`**; это единственный допустимый элемент,
> когда он присутствует (смешивание `none` с реальным компонентом — ошибка).

### Привязка по слот-хешу

`sticky_hash` привязывает поток к фиксированному **индексу слота** — `slot[hash(key) % pool]`
(FNV-64a по конкатенированным компонентам) — а не к позиции ноды. Слоты никогда не двигаются, и
нода-замена занимает ровно тот слот, что вытеснила, поэтому нода, оставшаяся в своём слоте, держит
все свои ключи, когда другие слоты меняются: ни лишних реконнектов, ни per-key состояния. Дефолт
`["process","domain"]` даёт привязку по процессу и по домену назначения; `domain` читает исходный
снифнутый домен (он переживает резолв роутера домен→IP, поэтому заполнен для нормального
доменного трафика, а не только для назначений-литеральных-IP). Для доменного трафика держите
`domain` в ключе — ключ только из `source_ip`/`dest_ip`/`dest_port` может схлопнуться в `""` для
нерезолвленного назначения, приклеив все потоки одного источника к единственному слоту.

### Пример — urltest с round_robin

```jsonc
{
  "type": "urltest",
  "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "interval": "15m",
  "mode": "round_robin",
  "balancer": {
    "pool": 3,
    "pool_tolerance": 0,
    "sticky_hash": ["process", "domain"]   // ["none"] чтобы отключить липкость
  }
}
```

> **Статус.** Равномерная ротация проверена локально (10/10/10 с выключенной липкостью) и
> **device-verified end-to-end** на реальном многонодовом пуле — фикс `domain` из rc.15 поднимает
> на устройстве per-domain равномерность с ~0.27 до 0.95+. Для большого списка нод
> `pool_tolerance: 0` + малый `pool` + бóльший `interval` — рекомендуемая настройка с минимальными
> накладными расходами.

**📖 [Полный справочник →](../docs/configuration/outbound/urltest.md)** — каждое поле, семантика
липкости по компонентам, правила наполнения/поддержки пула и советы по тюнингу.

---

## 4. MASQUE outbound — Cloudflare WARP (SPEC 021)

Outbound `masque` туннелирует **целые IP-пакеты** поверх HTTP/3 или HTTP/2 через
**CONNECT-IP (RFC 9484)**, в первую очередь для подключения к **Cloudflare WARP**. (Это
CONNECT-IP, а НЕ CONNECT-UDP/RFC 9298; и не путать с AWG-сахаром *masquerade* `id/ip/ib` из §2 —
одно слово, разные фичи.) Ядро поднимает userspace gVisor-стек на туннель и гонит трафик через
него, как через WireGuard-endpoint. За тегами `with_quic` + `with_gvisor` (оба в дефолтном
`LX_TAGS`). Ключевой материал берётся готовым из конфига — регистрацию устройства (ECDSA-ключи,
WARP enroll) делает клиент, не ядро.

> ⚠️ **Версия HTTP задаётся полем `vhttp`, а не `network`** (SPEC 062). В старых конфигах
> `h3`/`h2` жили в `network` — обратно тому, что `network` значит на всех остальных outbound.
> Старая форма ещё принимается и печатает deprecation; таблица миграции ниже.

### Поля (на outbound `masque`)

| Ключ | Тип | По умолчанию | Смысл |
|------|-----|--------------|-------|
| `profile` | string | `cloudflare` | `cloudflare` (квирки WARP: `cf-connect-ip`, терпит отсутствие Extended-CONNECT settings, pinning на ECDSA public key, дефолты SNI/URI WARP) \| `standard` (строгий RFC 9484, для своего CONNECT-IP сервера) |
| `vhttp` | string | `h3` | **версия HTTP, несущая CONNECT-IP**: `h3` (QUIC) \| `h2` (HTTP/2, TCP:443). Список tcp/udp — это `network_list`, как везде |
| `private_key` | string (base64) | — | client EC private key, DER (`x509.ParseECPrivateKey`). Обязателен для `cloudflare` |
| `public_key` | string (base64) | — | endpoint PKIX public key, DER (`x509.ParsePKIXPublicKey`, ECDSA). Обязателен для `cloudflare` |
| `ip` | string (CIDR) | — | локальный IPv4 внутри туннеля; без маски → `/32`. Нужен хотя бы один из `ip`/`ipv6` |
| `ipv6` | string (CIDR) | — | локальный IPv6 внутри туннеля; без маски → `/128` |
| `tls` | object | — | **стандартный** блок TLS outbound'а — `server_name`, `insecure`, `disable_sni`, `fragment`, `record_fragment`, `fragment_fallback_delay`, … Тот же контейнер, что у всех TLS-outbound |
| `uri` | string | по профилю¹ | URI запроса CONNECT-IP |
| `mtu` | int | `1280` | MTU userspace-стека. На `h2` максимум `16000` (один IP-пакет = один HTTP/2 DATA-фрейм) |
| `idle_timeout` | duration | `5m` | suspend туннеля после простоя (освобождает gVisor-стек, насосы и QUIC keepalive); следующий dial поднимает заново. Отрицательное — выключить |
| `keep_alive_period` | duration | `30s` | QUIC keepalive (h3). Отрицательное — выключить |
| `network_list` | list | tcp+udp | L4-протоколы через туннель |

¹ Дефолты `cloudflare`: `tls.server_name` = `www.cloudflare.com`, `uri` = `https://cloudflareaccess.com`. У `standard` дефолтов нет (оба обязательны).

> **SNI по умолчанию — `www.cloudflare.com`, а не имя эндпоинта.** Имя MASQUE-эндпоинта
> в ClientHello — ровно то, по чему его режет DPI; нейтральный популярный хост — нет.
> Эндпоинт аутентифицируется пиннингом `public_key`, поэтому SNI волен отличаться.
> `tls.disable_sni: true` не шлёт SNI вовсе — часть эндпоинтов отдаёт настоящий сертификат
> только на ClientHello без него.

### Миграция со старой формы (до SPEC 062)

Обе формы принимаются до **v1.14.0-lx.30**; использование legacy-поля печатает одну строку
deprecation на outbound. Если legacy-поле **противоречит** своей замене — это жёсткая ошибка,
а не молчаливый выбор одного из двух.

| Legacy (deprecated) | Актуальное |
|---|---|
| `network: "h3"` / `"h2"` | `vhttp: "h3"` / `"h2"` |
| `sni` | `tls.server_name` |
| `skip_cert_verify: true` | `tls.insecure: true` |
| `fragment: true` | `tls.fragment: true` |
| `fragment_fallback_delay` | `tls.fragment_fallback_delay` |
| `record_fragment: true` | `tls.record_fragment: true` |

> Legacy-булевы неотличимы «не задано» от явного `false`, поэтому переносится только
> legacy-`true` — чтобы что-то выключить, пиши форму `tls`.

### Пример — WARP по h3 (QUIC)

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "profile": "cloudflare",
  "vhttp": "h3",
  "tls": {
    "server_name": "www.microsoft.com"   // любой нейтральный популярный хост (domain-fronting)
  },
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2/32",
  "ipv6": "2606:4700:110:...::/128",
  "mtu":  1280
}
```

Для `h2` (CONNECT-IP over TCP:443) меняется одно поле: `"vhttp": "h2"`. Путь `h2` гонит свой
TLS через общий слой `common/tls`, поэтому получает фрагментацию ClientHello наравне с любым
другим TLS-outbound — включая автоматическую под `detour`
([§8](#8-автоматическая-фрагментация-clienthello-под-detour-spec-060)). `h3` этим не затронут:
QUIC не несёт TLS поверх TCP вовсе.

> Нужен блок `dns` верхнего уровня — userspace-стек работает на L3 и сам домены не резолвит;
> outbound резолвит их через DNS-роутер перед dial.

> **h3 vs h2 — что выбрать.** `h3` (QUIC) — по умолчанию и быстрее. Но на сетях, где режется
> входящий UDP:443, QUIC-handshake виснет и `h3` не поднимается — переключите такой узел на
> `h2` (TCP:443), он device-verified работает там. Учтите также: первый dial `h3` медленный
> (холодный CONNECT-IP: QUIC-handshake + Extended CONNECT + анонс маршрута + стек), поэтому
> короткий urltest-таймаут может пометить свежий h3-узел `-1` на первой пробе, хотя дальше он
> работает.

> **Статус.** Device-verified end-to-end на реальных Wi-Fi и LTE — `warp=on`, реальный трафик на
> обоих `h3` и `h2`, idle-suspend + самовосстановление подтверждены на устройстве.

**📖 [Полный справочник →](../SPECS/TASKS/021-MASQUE_CONNECT_IP_OUTBOUND/CONFIG.md)** — полная таблица
параметров, матрица профилей, формат ключевого материала, валидация при старте и частые грабли.

---

## 5. Группа DNS-серверов (SPEC 033/035)

Несколько DNS-серверов под одним тегом со стратегией выбора. Решает проблему
«один мёртвый DNS-сервер роняет резолв целиком»: upstream'овский `dns.final` —
сервер *по умолчанию*, а не резерв, и запрос, направленный правилом в сервер,
при любой сетевой ошибке, таймауте или SERVFAIL падает без повтора. Build-тега
нет — тип доступен всегда; конфиг без `group`-сервера ведёт себя как upstream.

У серверов **нет состояний** (down/backoff не существуют). Вместо них две
таблицы записей с TTL: **ошибка** (любой сбойный обмен; стирает живые победы
сервера) и **победа** (только первый успех веера; любой успех стирает живые
ошибки сервера). **Чистый** = ноль живых ошибок. Смена сети — амнистия обеих
таблиц.

### Поля (запись в `dns.servers[]`)

```jsonc
{
  "type": "group",                  // селектор — строго "group"
  "tag": "public",
  "servers": ["google", "cloudflare", "quad9"], // ОБЯЗАТЕЛЬНОЕ, ≥1 тегов.
                                    //   Порядок НЕ значим ни в одном режиме
  "mode": "stable",                 // stable (дефолт) | fastest | parallel
  "error_ttl": "2m",                // дефолт 2m: сколько живёт запись об ошибке
  "win_ttl": "5m"                   // дефолт 5m: сколько живёт победа.
                                    //   Только fastest; вне него — предупреждение
}
```

**Сбой** = транспортная ошибка, таймаут, `SERVFAIL`. `NXDOMAIN` и пустой
ответ — **валидные ответы** (и соревновательные победы, если пришли первыми).

**Режимы** (цель выбирается среди чистых; **чистых нет** — любой режим
делает ровно ОДНУ попытку к наименее грязному и никогда не фанится —
анти-шторм, «режим выживания»):

- `stable` — липкость прежде случайности: остаёмся на текущем, пока он
  чист; случайные перевыборы из чистых — только когда его нет. Возврата
  на выздоровевший экс-текущий нет: он просто возвращается в пул.
- `fastest` — чистый сервер с максимумом живых побед; когда живых побед нет
  ни у кого, запрос становится **веером-выборами** по всем чистым
  (single-flight: одни выборы за раз, конкуренты идут к случайному чистому).
  Ритм переизбрания — истечение `win_ttl`.
- `parallel` — каждый запрос веером по всем чистым; побед не пишет;
  N× трафика по определению.

**Единый поток:** одиночная цель получает под-дедлайн — ПОЛОВИНУ остатка
бюджета запроса; вееру-спасению гарантирован остаток. При сбое цели запрос
уходит веером по оставшимся чистым; первый успех отвечает (и становится
текущим), опоздавшие отбрасываются (в кеш не идут, но успех лечит записи
ошибок своего сервера). Сбой участника веера при уже завершённом контексте
запроса — артефакт, никуда не записывается.

**Наблюдаемость:** поток DNS-запросов несёт фактически ответившего
участника (кеш-попадания и полный сбой — тег группы), трассу проб (путь
групп изнутри наружу, исходы `answered`/`timeout`/`network_error`/`servfail`
и rtt) и флаги `fanned` / `survival`. `GetDNSGroups` (§7, `with_lx_command`)
отдаёт живые записи: по участнику — чистота, живые ошибки (счёт + возраст
последней), живые победы, последний rtt, флаг текущего.

> **Предупреждение об утечке имён.** Любой режим при сбое шлёт имя запроса
> всем чистым участникам; `parallel` — каждым запросом. Не смешивайте
> внутренние и публичные резолверы в одной группе.

### Пример — отказоустойчивый публичный DNS по умолчанию

```jsonc
{
  "dns": {
    "servers": [
      { "type": "udp", "tag": "google",     "server": "8.8.8.8" },
      { "type": "udp", "tag": "cloudflare", "server": "1.1.1.1" },
      { "type": "group", "tag": "public",
        "servers": ["google", "cloudflare"],
        "mode": "fastest", "error_ttl": "2m", "win_ttl": "5m" }
    ],
    "final": "public"
  }
}
```

> ⚠️ Контракт v1 (`mode: failover|race`, `interval`, `down_time`,
> отгружался в `v1.14.0-lx.16-rc.1`) УДАЛЁН: такие конфиги не загружаются.

## 6. VLESS `encryption` — пост-квантовый слой (SPEC 032)

Плоское поле `encryption` на `vless`-outbound включает `mlkem768x25519plus`-рукопожатие
**внутри** VLESS — над транспортом/TLS, под VLESS-клиентом и независимо от key exchange
REALITY (другой слой, не путать). Серверы, которые его требуют (в Xray настроен
`decryption`), молча рвут обычный VLESS: транспорт поднимается (WS `101`, gRPC отвечает
на SETTINGS), после чего пир закрывает соединение без единой строки в логе ядра — именно
этот симптом поле и лечит. Только клиентская половина; серверная намеренно не
портирована (клиентский форк). Всегда в сборке, build-тега нет.

### Поле (на `vless`-outbound)

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `encryption` | string | `""` | `""`/`"none"` = слой выключен (поведение upstream байт-в-байт). Иначе — spec-строка, валидируется на `check`/старте с ошибками, называющими конкретный сегмент |

Грамматика spec-строки (сегменты через точку):

```
mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<ключ>[.<ключ>…]
```

- **appearance** — как слой выглядит на проводе: `native` (AEAD-заголовки в форме
  TLSv1.3), `xorpub` (XOR по публичному ключу), `random`.
- **rtt** — `0rtt` или `1rtt`.
- **padding** — опциональные короткие блоки вида `100-111-1111` (сегмент короче
  20 символов до первого ключа читается как padding).
- **ключ** — base64url публичного ключа X25519 (32 байта) или ML-KEM-768
  (1184 байта); ключей может быть несколько. Рабочий ключ ML-KEM-768 — ~1579
  символов; заметно более короткий — обрезанный ключ, а не другой формат.

### Пример

```jsonc
{
  "type": "vless",
  "tag": "pq-node",
  "server": "example.com",
  "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ключ ML-KEM-768>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

> **Статус.** Отгружено в `v1.14.0-lx.18`, **девайс-верифицировано** на подписке,
> из-за которой фича появилась: +10 прежде мёртвых нод (6/8 WS, 4/4 gRPC), остальные
> группы транспортов не сдвинулись. Полевая форма — `native.0rtt`; `1rtt`+padding и
> `xorpub`/`random` парсятся и собираются, но живого сервера ещё не встречали.
> В подписке значение приходит как `settings.vnext[0].users[0].encryption` — на
> sing-box-outbound это плоское поле `encryption` рядом с `uuid`; билдер конфига,
> который его теряет, оставляет ядро ни с чем.

## 7. Наблюдаемость (расширения CommandClient)

Это **дополнения клиентского API, а не конфиг** — дополнительные методы на `CommandClient` libbox
(нативный gRPC-канал управления), все за тегом `with_lx_command`, потребляются LxBox. Они ничего
не добавляют в конфиг-файл sing-box; вы включаете их сборкой с тегом и вызовом из клиента. Без тега
методы отсутствуют, и демон обслуживает только upstream-набор команд.

Добавленные методы `CommandClient`:

- **`URLTestOutbound(tag, link, timeout)`** — синхронно измерить задержку одного outbound **или
  endpoint** по требованию (не только периодический тест группы).
- **`GetRules()`** — снимок таблицы маршрутных правил (route-правила + DNS-правила).
- **`GetGroups()`** — снимок outbound-групп (те же данные, что пушит поток групп).
- **`GetOutbounds()`** — плоский список outbound/endpoint (нужен рядом с `GetGroups`, потому что
  отдельно стоящие outbounds не входят ни в одну группу).
- **`GetPool(groupTag)`** — прочитать текущий пул ротации round_robin группы `urltest`, слот за
  слотом (SPEC 019; см. [§3](#3-балансировка-нагрузки-round_robin-spec-019)).
- **`GetDNSGroups()`** — live-состояние каждого DNS-сервера `group` (SPEC 035; см.
  [§5](#5-группа-dns-серверов-spec-033035)): по каждому члену `clean` / `liveErrors` /
  `lastErrorAgeMs` / `liveWins` / `current`.
- **`GetRunningConfig()`** — канонический JSON options, из которых реально построен
  работающий box, post-override (SPEC 037). Возвращается объектом с аксессором `Content()` —
  голый `string`-возврат ронял бы gomobile на android/arm64 (SPEC 038).
- **`SubscribeDNSQueries(includeAnswers, handler)`** — структурный live-поток DNS-запросов
  (SPEC 018): по каждому запросу `domain`, `qtype`, `rcode` (**`-1` = ошибка резолва**,
  полноправное состояние), CNAME-цепочка / ответы (при `includeAnswers`), привязка к процессу и
  `dnsServer` / `dnsServerType` / `outbound` (пустой `outbound` означает direct/system — валидное
  состояние, не баг).

SPEC 017 также обогащает существующий поток соединений: отслеживаемое `Connection` теперь несёт
отдельное поле **`detourList`** — хвост transport-detour'а финального outbound, выставленный
отдельно от прокси-`chain` (chain опускает detour по дизайну).

Соберите с тегом, чтобы их получить:

```sh
make -f Makefile.lx lx-build   # включает with_lx_command (и with_xhttp/with_awg)
```

---

## 8. Автоматическая фрагментация ClientHello под `detour` (SPEC 060)

**Это не ключ конфига, а изменённый дефолт.** Когда TLS-over-TCP outbound (VLESS, trojan,
vmess, anytls, shadowtls, http, masque `h2`, …) диалит **через `detour`**, `record_fragment`
теперь по умолчанию **включён**.

Почему: нижнее плечо пересылает наш ClientHello от своего имени, а PMTU за тем сервером может
быть ниже размера ClientHello. ICMP *Fragmentation Needed* до нас не доходит — пакет просто
исчезает, и снаружи это выглядит как `tls handshake: EOF` через 12–17 с. Воспроизводится голым
`curl`, то есть причина в пути, а не в sing-box, — но обойти её можно только фрагментацией
первой TLS-записи. Замер через сломанное плечо: без фрагментации ❌ отказ за 12 с;
`fragment` ✅ 0.6 с; `record_fragment` ✅ **0.1 с**.

Правила:

- **Явное значение в конфиге всегда сильнее.** `fragment: true` не апгрейдится до
  record-split — если выбран packet-split, это остаётся твоим выбором.
- **Фрагментируется только хендшейк**, никогда не трафик после него. Постоянного налога нет.
- **`h3`/QUIC не затронут** — там нет TLS поверх TCP, а quic-go и так держит Initial ниже
  порога (masque `h3` через detour: 4/4 ОК).
- Вложенные цепочки покрыты автоматически: у каждого звена свой `detour`.

> ⚠️ **Известное ограничение:** явный `"record_fragment": false` неотличим от «не задано»,
> поэтому под `detour` авто всё равно включится. Чтобы диалить через detour другим режимом,
> ставь `"fragment": true`; способа «под detour вообще без фрагментации» сейчас нет.

---

## 9. Проверка и сборка

```sh
git clone --recurse-submodules <repo>           # with_awg требует submodule
make -f Makefile.lx lx-build                     # собирает ./sing-box с обеими фичами
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json

# Android (опционально): libbox.aar с зашитыми with_xhttp+with_awg (нужны NDK r28 + OpenJDK 17)
make lib_install && make lib_android             # → libbox.aar (SDK23) + libbox-legacy.aar (SDK21)
```

CI (`.github/workflows/lx-ci.yml`) собирает матрицу фич (`baseline` / `xhttp` / `awg` / `full`), кросс-платформенную матрицу **и Android `libbox.aar`** (gomobile), прогоняя `check` на соответствующих примерах конфигов. Push тега `v*-lx.*` запускает `lx-release.yml`, который публикует десктоп-бинари **и** `libbox-<ver>.aar` / `libbox-legacy-<ver>.aar` как ассеты GitHub Release. Также публикуется legacy-бинарь **Windows 7 (32-бит)** (`sing-box-<ver>-windows-386-legacy-windows-7.zip`) — собранный Win7-патченным Go и **без `with_naive_outbound`** (у `cronet-go` нет сборки под windows/386; все остальные фичи без изменений).
