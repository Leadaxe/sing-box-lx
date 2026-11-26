# sing-box-lx — конфигурация фич форка

> 🌐 English version: **[lx-config.md](lx-config.md)**.

> 🧭 **Где что искать.** Документация форка — три уровня, по вопросу читателя:
> [lx-config](lx-config.ru.md) — что есть в форке, все ключи по строке и как включить; у фич со
> своей логикой — отдельный документ с примерами: [lx-urltest](lx-urltest.ru.md),
> [lx-dns-group](lx-dns-group.ru.md), [lx-chain](lx-chain.ru.md), [lx-block](lx-block.ru.md),
> [lx-sniff](lx-sniff.ru.md), [lx-energy](lx-energy.ru.md), [lx-observability](lx-observability.ru.md), [lx-impair](lx-impair.ru.md);
> [protocols-transports](protocols-transports.ru.md) — каждое поле, тип, дефолт, текст ошибки;
> [xray-protocols-explained](xray-protocols-explained.ru.md), [amneziawg-explained](amneziawg-explained.ru.md) и [masque-explained](masque-explained.ru.md) —
> как устроено, почему, как сделано у нас и чем отличается от ванили.

## Оглавление

- [Фичи и build-теги](#фичи-и-build-теги)
- [0. Все поля разом (исчерпывающий пример)](#0-все-поля-разом-исчерпывающий-пример)
- [1. XHTTP-транспорт](#1-xhttp-транспорт)
- [2. AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
- [3. Режимы выбора узла в urltest](#3-режимы-выбора-узла-в-urltest)
- [4. MASQUE outbound — Cloudflare WARP](#4-masque-outbound--cloudflare-warp)
- [5. Группа DNS-серверов](#5-группа-dns-серверов)
- [6. VLESS `encryption` — пост-квантовый слой](#6-vless-encryption--пост-квантовый-слой)
- [7. REALITY `key_share` — гибридный или классический ClientHello](#7-reality-key_share--гибридный-или-классический-clienthello)
- [8. Наблюдаемость (расширения CommandClient)](#8-наблюдаемость-расширения-commandclient)
- [9. ClientHello под `detour` — без дефолта](#9-clienthello-под-detour--без-дефолта)
- [10. Outbound `chain` — виртуальная цепочка хопов из групп и узлов](#10-outbound-chain--виртуальная-цепочка-хопов-из-групп-и-узлов)
- [11. Снифферы протоколов для трафика из LAN](#11-снифферы-протоколов-для-трафика-из-lan)
- [12. Проверка и сборка](#12-проверка-и-сборка)
- [13. Корневой блок `lx`](#13-корневой-блок-lx)
- [14. Tailscale: выключить Taildrop](#14-tailscale-выключить-taildrop)
- [15. Outbound `impair` — ухудшение линии для испытаний](#15-outbound-impair--ухудшение-линии-для-испытаний)

---

## Фичи и build-теги

`sing-box-lx` — это upstream [sing-box](https://github.com/SagerNet/sing-box) плюс небольшой набор клиентских фич, каждая за своим build-тегом:

| Фича | Build-тег | Где живёт в конфиге | Входит в |
|------|-----------|---------------------|----------|
| **XHTTP** транспорт (совместим с Xray) | `with_xhttp` | `transport.type: "xhttp"` на VLESS / VMess / Trojan outbound | desktop + mobile |
| **AmneziaWG 2.0/3.x** (AWG2, AWG3) | `with_awg` | доп. поля на `wireguard` endpoint | desktop + mobile |
| **MASQUE** outbound (CONNECT-IP / WARP) | `with_quic`+`with_gvisor` | `outbounds[].type: "masque"` | desktop + mobile |
| **TrustTunnel** outbound и inbound | `with_trusttunnel` (+ `with_quic` для HTTP/3) | `outbounds[].type` / `inbounds[].type: "trusttunnel"`; поля — [protocols-transports.ru.md §6](protocols-transports.ru.md#6-trusttunnel-outbound-и-inbound), глобальный `lx.trusttunnel.idle_timeout` — [lx-block.ru.md](lx-block.ru.md#lxtrusttunnel--trusttunnel-outboundы) | desktop + mobile |
| **Idle-suspend** | `with_lx_idle_suspend` | `lx.wg.idle.suspend` (+ `suspend_reachable`, `teardown`) | только mobile (AAR) |
| **Корневой блок `lx`** | — (разбирается всегда) | `lx.wg`, `lx.masque`, `lx.trusttunnel`, `lx.detour`, `lx.sniff_guard` — глобальные ручки форка, [lx-block.ru.md](lx-block.ru.md) | desktop + mobile |
| **Группа DNS-серверов** | — (всегда в сборке) | `dns.servers[].type: "group"` | desktop + mobile |
| **VLESS `encryption`** | — (всегда в сборке) | `encryption` на `vless`-outbound | desktop + mobile |
| **Демон `lxd`** | `with_lxd` | не ключ конфига — подкоманда `sing-box lxd` + `<state-dir>/daemon.json`; см. [lxd-daemon.ru.md](lxd-daemon.ru.md) | desktop / сервер (не Win7, не AAR) |
| **Outbound `chain`** | `with_lx_chain` | `outbounds[].type: "chain"` — многохоповый путь из групп и узлов | desktop + mobile |
| **Outbound `impair`** | `with_lx_impair` | `outbounds[].type: "impair"` — намеренно ухудшенная линия для испытаний, [§15](#15-outbound-impair--ухудшение-линии-для-испытаний) | desktop + mobile |

Desktop/CLI-бинарь собирает `make -f Makefile.lx lx-build`: на выходе `sing-box` с версией `…-lx.N`.
В сборку входят `with_xhttp` и `with_awg` (плюс `with_lx_command` и `with_lxd`), но не `with_lx_idle_suspend`.
Без тега фичи нет: `xhttp`-транспорт или AWG-поле отклоняются при загрузке с явной ошибкой, молчаливого отката нет.

Тег `with_lx_idle_suspend` входит только в мобильный AAR (`cmd/internal/build_libbox`), не в
desktop `LX_TAGS`. Он усыпляет простаивающие WireGuard/AmneziaWG-эндпоинты и освобождает их
приёмные буферы: на устройстве 8 усыплённых эндпоинтов освободили 134 МБ. Ключи `lx.wg.idle` и
отказ десктоп-бинаря на них — в [lx-block.ru.md](lx-block.ru.md#lxwg--endpointы-wireguard--amneziawg), энергомодель и
рекомендованный мобильный конфиг — в [lx-energy.ru.md](lx-energy.ru.md).

> ⚠️ Все ключи/UUID ниже — заглушки. Никогда не коммитьте реальные приватные ключи / pre-shared-ключи в репозиторий.

---

## 0. Все поля разом (исчерпывающий пример)

Один конфиг, в котором есть все поля фич форка с комментариями: значение по умолчанию и
допустимые значения у каждого. Это справочник для тех, кто пишет генератор конфига, а не
рабочая настройка: часть полей взаимоисключающие или серверные. Пример целиком —
[lx-config-example.ru.md](lx-config-example.ru.md).

---

## 1. XHTTP-транспорт

XHTTP — транспорт Xray (`splithttp`/`xhttp`): прокси идёт внутри обычных HTTP-запросов, поэтому
проходит через CDN. Он крепится к VLESS, VMess и Trojan блоком `transport` и
сочетается с TLS и REALITY; с XTLS-Vision несовместим. Версию HTTP задаёт `tls.alpn`: по
умолчанию HTTP/2, `["h3"]` — HTTP/3, `["http/1.1"]` — HTTP/1.1. Поля второй версии включаются
явно; без них провод такой же, как у клиента первой версии. Нужен build-тег `with_xhttp`.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `mode` | `auto` | `auto` (`stream-one` на REALITY, иначе `packet-up`), `packet-up`, `stream-up`, `stream-one` |
| `host` | SNI или адрес сервера | заголовок `Host` |
| `path` | `""` | префикс пути; id сессии и номер пакета дописываются сегментами |
| `headers` | — | дополнительные заголовки на каждом запросе |
| `x_padding_bytes` | `"100-1000"` | длина паддинга в байтах, `"min-max"` или число |
| `no_grpc_header` | `false` | не слать `Content-Type: application/grpc` в `stream-one` и `stream-up` |
| `session_placement` | `path` | где id сессии: `path`, `query`, `header`, `cookie` |
| `session_key` | `X-Session` / `x_session` | имя поля с id сессии, если он не в пути |
| `seq_placement` | `path` | где номер пакета `packet-up`: `path`, `query`, `header`, `cookie` |
| `seq_key` | `X-Seq` / `x_seq` | имя поля с номером пакета, если он не в пути |
| `session_table` | — | алфавит случайного id сессии; без него id — UUID |
| `session_length` | — | длина id сессии, `"min-max"` или `"n"`; только вместе с `session_table` |
| `uplink_data_placement` | `auto` (= `body`) | где данные отдачи: `body`, `header`, `cookie`; последние два — только `packet-up` |
| `uplink_data_key` | `X-Data` / `x_data` | имя заголовка или cookie с данными отдачи |
| `uplink_chunk_size` | по размещению | размер куска данных отдачи в base64-символах, `"min-max"` |
| `uplink_http_method` | `POST` | метод запросов отдачи; `GET` — только `packet-up` |
| `x_padding_obfs_mode` | `false` | настраиваемый паддинг `x_padding_*` вместо паддинга в `Referer` |
| `x_padding_placement` | `queryInHeader` | где паддинг: `cookie`, `header`, `query`, `queryInHeader` |
| `x_padding_key` | `x_padding` | имя cookie или параметра с паддингом |
| `x_padding_header` | `X-Padding` | имя заголовка с паддингом |
| `x_padding_method` | `repeat-x` | `repeat-x` (символы `X`) или `tokenish` (base62) |
| `sc_max_each_post_bytes` | `"1000000-1000000"` | порог, после которого POST отдачи делится |
| `sc_min_posts_interval_ms` | `"30-30"` | пауза между POST отдачи, мс |
| `xmux` | — | пул HTTP-соединений: `max_connections` (`3-3`), `max_concurrency`, `c_max_reuse_times`, `h_max_request_times` (`600-900`), `h_max_reusable_secs` (`1800-3000`), `h_keep_alive_period` |

`sc_max_concurrent_posts`, `server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts` и
`sc_stream_up_server_secs` принимаются для совместимости со ссылками Xray, клиент их не использует.

```jsonc
{
  "type": "vless", "tag": "xhttp-out", "server": "example.com", "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "tls": {
    "enabled": true, "server_name": "example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" },
    "reality": { "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd" }
  },
  "transport": { "type": "xhttp", "mode": "stream-one", "path": "/xhttp" }
}
```

📖 Каждый ключ, `xmux`, версии HTTP, примеры за CDN и диагностика — в
[protocols-transports.ru.md §1](protocols-transports.ru.md#1-xhttp-транспорт); как устроен XHTTP —
в [xray-protocols-explained.ru.md](xray-protocols-explained.ru.md).

---

## 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

AmneziaWG — WireGuard, который прячет форму своих пакетов от DPI: мусорные пакеты перед
рукопожатием, паддинг, свои заголовки и приманки под другой протокол. Это обычный endpoint
`wireguard` с дополнительными полями на корне endpoint, как в секции `[Interface]` файла
`awg-quick`; без них endpoint работает как обычный WireGuard. Параметры не согласуются: у клиента и
сервера они должны совпадать. AWG 3.x — контейнер Amnezia `amnezia-awg2` с `protocol_version` 3.x.
Нужен build-тег `with_awg`.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `jc`, `jmin`, `jmax` | `0` | число мусорных пакетов перед рукопожатием и их размер |
| `s1`, `s2` | `0` | мусор перед сообщениями рукопожатия INIT и RESPONSE |
| `s3`, `s4` | `0` | AWG 2.0: мусор перед cookie reply и перед каждым data-пакетом; с `s4` ядро ставит `mtu` 1280 |
| `h1`–`h4` | `1`–`4`, как в WireGuard | magic-заголовки четырёх типов сообщений: число или диапазон `"min-max"` без пересечений |
| `i1`–`i5` | `""` | приманки перед рукопожатием в формате тегов: `<b 0x…>`, `<r N>`, `<c>`, `<t>`, `<rc N>`, `<rd N>` |
| `id`, `ip`, `ib` | `""` | сахар вместо ручного `i1`: приманка `quic`, `dns`, `stun` или `sip` с доменом `id` и браузером `ib` |
| `header_protection_key` | — | AWG 3.x: ключ шифрования заголовков, задаёт сервер; при нём `s1`–`s4` не меньше 12 |
| `content_padding_addition` | — | AWG 3.x: добавочный паддинг внутри data-пакетов, число или `"min-max"` |
| `rekey_after_time`, `rekey_timeout`, `reject_after_time` | как в WireGuard | AWG 3.x: тайминги смены ключей, число или `"min-max"` секунд |
| `keepalive_timeout`, `max_handshake_attempts` | как в WireGuard | AWG 3.x: таймаут keepalive и число попыток рукопожатия, число или `"min-max"` |
| `random_trailers` | `false` | AWG 3.x: хвост случайной длины у сообщений |
| `disable_cookies` | `false` | AWG 3.x: не слать и не требовать cookie reply |
| `peers[].persistent_keepalive_interval` | — | keepalive пира: число секунд или, в AWG 3.x, `"min-max"` |

```jsonc
{
  "type": "wireguard", "tag": "awg-out", "mtu": 1280,
  "address": ["10.0.0.2/32"], "private_key": "<client-private-key-base64>",
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": "43613244-384550127", "h2": "826869626-2105069164",
  "h3": "2124774725-2141151992", "h4": "2144594503-2146278491",
  "id": "www.google.com", "ip": "quic", "ib": "chrome",
  "peers": [{ "address": "server.example.com", "port": 51821,
    "public_key": "<server-public-key-base64>", "allowed_ips": ["0.0.0.0/0", "::/0"] }]
}
```

📖 Каждый ключ, формат приманок, профили сахара, бюджет MTU, перевод `awg.conf` и пример AWG 3.1 —
в [protocols-transports.ru.md §2](protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3); как
устроен AmneziaWG — в [amneziawg-explained.ru.md](amneziawg-explained.ru.md).

---

## 3. Режимы выбора узла в urltest

Upstream `urltest` всегда выбирает узел с наименьшей задержкой. Форк добавляет ещё два режима:

| `mode` | Выбор | Переключение | Что проверяется каждый `interval` |
|---|---|---|---|
| `least_test` (по умолчанию) | самый быстрый | когда другой быстрее на `tolerance` или подключение через текущий не удалось | все узлы |
| `round_robin` | пул живых узлов | соединения раскладываются по пулу | только пул |
| `failover` | самый быстрый на момент выбора | только когда текущий отказал или стал вдвое медленнее остальных | только текущий узел |

`round_robin` держит сайт на своём узле (sticky-привязка) и восстанавливается от самих соединений:
соединение, не открывшееся через мёртвый узел, открывается через соседний, а узел сразу меняется на
живой. Поля `url`, `interval`, `idle_timeout` и `interrupt_exist_connections` работают как в
апстриме; build-тег не нужен.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `mode` | `least_test` | `least_test`, `round_robin` или `failover` |
| `balancer.pool` | `3` | сколько узлов в пуле `round_robin` |
| `balancer.pool_tolerance` | `0` | `0` — пул из живых узлов без сравнения задержек; `> 0` — самые быстрые, замена при выигрыше больше чем на столько мс |
| `balancer.sticky_hash` | `["process","domain"]` | из чего ключ привязки потока к узлу: `process`, `domain`, `source_ip`, `dest_ip`, `dest_port`; `["none"]` отключает |

```jsonc
{
  "type": "urltest", "tag": "auto",
  "outbounds": ["proxy-a", "proxy-b", "proxy-c", "proxy-d", "proxy-e"],
  "interval": "15m",
  "mode": "round_robin",
  "balancer": { "pool": 3, "pool_tolerance": 0 }
}
```

📖 Как работает каждый режим, самовосстановление пула, ограничения и примеры — в
[lx-urltest.ru.md](lx-urltest.ru.md).

---

## 4. MASQUE outbound — Cloudflare WARP

Outbound `masque` подключается к Cloudflare WARP и другим серверам CONNECT-IP ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)): целые
IP-пакеты идут поверх HTTP/3 или HTTP/2, а ядро поднимает на туннель свой userspace-стек, как у
WireGuard-endpoint'а. Ключи берутся готовыми из конфига: устройство в WARP регистрирует клиент.
SNI по умолчанию — `www.cloudflare.com`, сервер проверяется по закреплённому `public_key`.
`vhttp: auto` пробует HTTP/3 и через 3 с без ответа переходит на HTTP/2. Нужен блок `dns` верхнего
уровня и теги `with_quic` и `with_gvisor` (оба есть в `LX_TAGS`).

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `server`, `server_port` | обязательные | адрес эндпоинта и порт, обычно `443` |
| `private_key`, `public_key` | обязательные для `cloudflare` | ключи из регистрации WARP, base64 от DER |
| `ip`, `ipv6` | хотя бы один | адреса клиента внутри туннеля |
| `profile` | `cloudflare` | `cloudflare` — WARP, `standard` — свой сервер по [RFC 9484](https://www.rfc-editor.org/rfc/rfc9484) |
| `vhttp` | `auto` | `auto`, `h3` или `h2` |
| `uri` | `https://cloudflareaccess.com` у `cloudflare` | адрес запроса CONNECT-IP; для `standard` обязателен |
| `tls` | `server_name`: `www.cloudflare.com` | обычный TLS-блок: `server_name`, `disable_sni`, `insecure`, `fragment`, `record_fragment` |
| `mtu` | `1280` | размер внутреннего пакета; на `h2` до `16000` |
| `idle_timeout` | выключен или `lx.masque.idle_timeout` | простой, после которого туннель засыпает |
| `keep_alive_period` | `30s` | QUIC keepalive, только `h3`; отрицательное значение выключает |
| `network_list` | `tcp`, `udp` | какие протоколы пускать в туннель |
| `udp_fragment` | фрагментация разрешена | `false` запрещает фрагментацию внешних датаграмм |

```jsonc
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

📖 Как устроено и почему, примеры за релеем и типовые сбои — в
[masque-explained.ru.md](masque-explained.ru.md); ошибки загрузки и переход со старой схемы — в
[protocols-transports.ru.md §3](protocols-transports.ru.md#3-masque-outbound-connect-ip--warp).

---

## 5. Группа DNS-серверов

Тип `group` в `dns.servers[]` объединяет несколько DNS-серверов под одним тегом: если один сервер
не отвечает, запрос уходит к другим. Группа помнит ошибки и победы серверов и выбирает среди
чистых: `stable` держится за текущий, `fastest` берёт тот, что чаще отвечал первым, `parallel`
спрашивает всех сразу. Ключ `hosts` отвечает на локальные имена до обращения к сети. Build-тег не
нужен.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `servers` | обязательное | теги серверов группы, хотя бы один |
| `mode` | `stable` | `stable`, `fastest` или `parallel` |
| `error_ttl` | `2m` | сколько помнится ошибка сервера |
| `win_ttl` | `5m` | сколько помнится победа сервера; только `fastest` |
| `hosts` | — | теги серверов типа `hosts`, которые спрашиваются первыми |

```jsonc
{ "type": "group", "tag": "public", "servers": ["google", "cloudflare"], "mode": "stable" }
```

📖 Как группа выбирает сервер, `hosts` для адресов самих серверов и примеры — в
[lx-dns-group.ru.md](lx-dns-group.ru.md).

## 6. VLESS `encryption` — пост-квантовый слой

Поле `encryption` на `vless`-outbound включает внутри VLESS рукопожатие `mlkem768x25519plus`. Слой
лежит под VLESS и над транспортом, от REALITY не зависит. Сервер с настроенным `decryption` без
этого слоя молча рвёт соединение сразу после подъёма транспорта; поле это лечит. В форке только
клиентская половина, build-тег не нужен.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `encryption` | `""` | `""` или `"none"` — слой выключен; иначе строка `mlkem768x25519plus.<native\|xorpub\|random>.<0rtt\|1rtt>[.<padding>…].<ключ>` |

```jsonc
{
  "type": "vless", "tag": "pq-node", "server": "example.com", "server_port": 443,
  "uuid": "00000000-0000-0000-0000-000000000000",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64url ключ ML-KEM-768>",
  "transport": { "type": "ws", "path": "/ws" }
}
```

> ⚠️ В подписке значение приходит как `settings.vnext[0].users[0].encryption`. Генератор конфига
> должен переносить его в плоское поле `encryption` рядом с `uuid`.

📖 Сегменты строки, ошибки загрузки и проверка на подписке — в
[protocols-transports.ru.md §5.5](protocols-transports.ru.md#55-поле-encryption-у-vless); как
устроен слой — в [xray-protocols-explained.ru.md §5](xray-protocols-explained.ru.md#5-vless-encryption-постквантовый-слой).

---

## 7. REALITY `key_share` — гибридный или классический ClientHello

Браузерные отпечатки `chrome`, `firefox` и `safari` шлют гибридный постквантовый key share
`X25519MLKEM768`, и ClientHello становится втрое длиннее: два TCP-сегмента вместо одного. Новые
серверы Xray (от v26.9.8) гибрид требуют, а в некоторых сетях такое приветствие теряется. Ядро не
выбирает между «сервер отвергнет» и «сеть потеряет», поэтому выбор задаётся на узле.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `tls.reality.key_share` | `""` | `""` — как несёт отпечаток; `"classical"` — вырезать гибрид (только Xray < v26.9.8); `"hybrid"` — требовать гибрид, на отпечатке без него сразу ошибка |

```json
"reality": { "enabled": true, "public_key": "…", "short_id": "0123abcd", "key_share": "classical" }
```

Для нового сервера в такой сети остаются разрез ClientHello (`record_fragment`) и `detour`.

📖 Какой отпечаток что шлёт, размеры приветствий и ошибки — в
[protocols-transports.ru.md §5.2](protocols-transports.ru.md#52-отпечатки-и-гибридный-key-share); как
устроен REALITY — в [xray-protocols-explained.ru.md §2](xray-protocols-explained.ru.md#2-reality).

---

## 8. Наблюдаемость (расширения CommandClient)

Форк добавляет в `CommandClient` libbox методы, через которые клиент видит группы, пул,
цепочки, правила, работающий конфиг и DNS-запросы, не разбирая логи. Это API, а не ключи конфига:
методы есть в сборке с `with_lx_command` (входит в `make -f Makefile.lx lx-build`), без тега их нет.

| Метод | Что отдаёт или делает |
|-------|-----------------------|
| `URLTestOutbound(tag, link, timeout)` | замер задержки одного узла по требованию |
| `GetRules()` | таблица route- и DNS-правил |
| `GetGroups()` | группы outbound'ов и их выбор |
| `GetOutbounds()` | плоский список outbound'ов и endpoint'ов; у WG/AWG — состояние и время простоя |
| `GetPool(groupTag)` | слоты пула `round_robin` с состоянием и счётчиками |
| `GetPoolState(groupTag)` | то же плюс сдвинутые ключи, проблемы в адресе и заморозка |
| `GetDNSGroups()` | живое состояние каждого DNS-сервера `group` |
| `GetRunningConfig()` | канонический JSON работающего конфига |
| `GetEffectiveConfig()` | тот же конфиг после lx-предобработки: хопы цепочек добавлены, `detour` перехваченных узлов переписан, MTU выровнен |
| `GetOutboundConfig(tag)` | один узел как написан, как собран и список изменений между ними |
| `SubscribeDNSQueries(includeAnswers, handler)` | живой поток DNS-запросов |
| `GetChainStatus(tag)` | состояние одного outbound `chain` по позициям |
| `SetEndpointEnabled(tag, enabled)` | выключает и включает WG/AWG-endpoint на ходу |

У соединений в потоке есть поле `detourList` — путь через `detour` до финального outbound.

```sh
sing-box api group pool auto     # пул round_robin группы auto (сборка с with_lx_command)
```

Поля каждого метода и их значения — в [lx-observability.ru.md](lx-observability.ru.md).

---

## 9. ClientHello под `detour` — без дефолта

Релей, который читает SNI из ClientHello и отправляет соединение на это имя (Xray `sniffing` с
`destOverride` без `routeOnly`), уводит узел с маскировочным SNI (REALITY, WARP) на чужой хост.
Разрезанный ClientHello такой релей не разбирает. Без блока `lx.sniff_guard` ядро режет ClientHello
как апстрим — только у узлов с `fragment` или `record_fragment`; под `detour` без сокета ядра снизу
`fragment` узла по умолчанию снимает `lx.detour.strip`, `record_fragment` остаётся. Блок включает защиту для путей
через релеи и учится по исходам подключений; его ключи — в [§13](#13-корневой-блок-lx).

```json
"lx": { "sniff_guard": { "tls": { "method": "record_fragment" } } }
```

📖 Замер, прежний дефолт, узловые ключи и все режимы блока — в
[lx-block.ru.md](lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni).

---

## 10. Outbound `chain` — виртуальная цепочка хопов из групп и узлов

Outbound `chain` строит многохоповый путь из существующих узлов и групп. Каждую позицию
выбирает своя группа со всей своей логикой, цепочка идёт за выбором. При загрузке конфига каждый
узел, до которого доходит позиция ≥ 1, включая членов групп, получает `detour` на хоп позиции ниже,
`<tag>#<i−1>`, — внутренний исходящий типа `chain-hop`; собственный `detour` узла перекрывается. У
узла один экземпляр и один путь, и группа на позиции проверяет те экземпляры, что несут её трафик.
Выравнивание MTU и фильтры strip из `lx.detour` действуют на эти узлы как на любой узел под
`detour`. Нужен build-тег `with_lx_chain` (есть в десктопных `LX_TAGS` и в AAR).

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `outbounds` | обязательное | теги позиций в порядке пакета, от клиента к выходу; не меньше двух, все разные |
| `rewrite` | `{}` | merge-patch к конфигу перехваченных узлов по типу узла на позициях ≥ 1; `detour` менять нельзя |
| `interrupt_exist_connections` | `false` | при переключении позиции рвать и пользовательские соединения через цепочку |

`idle_timeout`, `strip_evasion` и `strip` в цепочке валят загрузку и называют замену.

```jsonc
{
  "type": "chain",
  "tag": "via-three",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

Как работают хопы, ошибки загрузки, выключение позиций, наблюдаемость и границы — в [lx-chain.ru.md](lx-chain.ru.md).

---

## 11. Снифферы протоколов для трафика из LAN

Форк добавляет пять имён протоколов для списка `sniffer` действия `sniff` и для матчера `protocol`
в правилах. Роутер узнаёт по первому пакету VPN-туннели и звонки устройств в LAN и маршрутизирует их
правилами. Снифферы форка стоят перед апстримным сниффером uTP, иначе тот помечал бы WireGuard как
`bittorrent`. Смотрится только первый пакет, поэтому обфусцированные туннели и RTP не распознаются.

| Имя | Сеть | Что узнаёт |
|-----|------|------------|
| `wireguard` | UDP | рукопожатие и data-пакеты WireGuard по размеру |
| `openvpn` | UDP, TCP | первый пакет OpenVPN: plain, tls-auth, tls-crypt |
| `ike` | UDP | IKE (IPsec): `IKE_SA_INIT` |
| `tailscale` | UDP | disco-пинг Tailscale; сам туннель виден как `wireguard` |
| `sip` | UDP, TCP | запрос SIP; ставит `domain` из Request-URI |

```jsonc
{ "protocol": ["wireguard", "openvpn", "ike", "tailscale"], "outbound": "warp" }
```

📖 Признаки каждого протокола, порядок снифферов, пример для роутера и что не распознаётся — в
[lx-sniff.ru.md](lx-sniff.ru.md).

## 12. Проверка и сборка

```sh
git clone --recurse-submodules <repo>           # with_awg требует submodule
make -f Makefile.lx lx-build                     # собирает ./sing-box с обеими фичами
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
./sing-box check -c lx-test/config/awg3_full.json     # набор полей AmneziaWG 3.1
./sing-box check -c lx-test/config/lx_block.json      # корневой блок lx, все ключи
make -f Makefile.lx lx-check                     # сборка + check каждого lx-test/config/*.json

# Android (опционально): libbox.aar с зашитыми with_xhttp+with_awg (нужны NDK r28 + OpenJDK 17)
make lib_install && make lib_android             # → libbox.aar (SDK23) + libbox-legacy.aar (SDK21)
```

CI (`.github/workflows/lx-ci.yml`) собирает матрицу фич (`baseline` / `xhttp` / `awg` / `full`),
кросс-платформенную матрицу и Android `libbox.aar` (gomobile) и прогоняет `check` на
соответствующих примерах конфигов. Push тега `v*-lx.*` запускает `lx-release.yml`. Он публикует
ассетами GitHub Release десктоп-бинари, `libbox-<ver>.aar` и `libbox-legacy-<ver>.aar`, а также
legacy-бинарь для Windows 7 (32-бит): `sing-box-<ver>-windows-386-legacy-windows-7.zip`. Бинарь
для Windows 7 собран Win7-патченным Go и без `with_naive_outbound`: у `cronet-go` нет сборки под
windows/386. Остальные фичи в нём без изменений.

## 13. Корневой блок `lx`

Глобальные ручки форка живут в одном корневом блоке `lx`, по подсистемам. Всё, что описывает
конкретный узел, остаётся в его объекте, как в апстриме. Все подблоки и ключи необязательны; пустой
`lx` и отсутствующий равнозначны, опечатка в имени ключа — ошибка загрузки. `GetRunningConfig`
отдаёт блок в канонической форме: пустые подблоки отброшены.

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `wg.idle.suspend` | выкл | простой, после которого WG/AWG-endpoint вне активного маршрута засыпает; только мобильная сборка |
| `wg.idle.suspend_reachable` | выкл | то же для endpoint'ов в активном маршруте; не меньше `suspend` |
| `wg.idle.teardown` | = `suspend_reachable` | сколько endpoint спит до полной разборки; `"0"` — не разбирать |
| `wg.idle.lazy_build` | `false` | собирать устройство при первом подключении, а не на старте |
| `wg.idle.build_max` | `0` | не больше N собранных устройств одновременно; `0` — без потолка |
| `wg.idle.build_overflow` | `wait` | при потолке: `wait` — ждать, `build` — собрать сверх потолка |
| `wg.buffers.read_datagram_count` | `128` | датаграмм на одно чтение из сокета, 1–128; меньше — меньше памяти |
| `wg.buffers.send_by_mtu` | `true` | буферы чтения по MTU туннеля; `false` — по 64 КБ, для диагностики |
| `wg.connected_socket` | `false` | подключать сокет туннеля к адресу сервера; Linux и Android |
| `masque.idle_timeout` | выкл | общее окно сна для узлов `masque` без своего `idle_timeout` |
| `trusttunnel.idle_timeout` | `5m` | общее окно простоя для узлов `trusttunnel` без своего `idle_timeout` |
| `detour` | `true` | что ядро делает с узлом под `detour`: `true` — выравнивание MTU `clamp` и все фильтры strip, `false` — поведение апстрима, или объект |
| `detour.mtu_align` | `clamp` | подгонка MTU и размера пакета узлов над туннелями: `off`, `fill`, `clamp` |
| `detour.strip` | все фильтры | фильтры для узла без сокета ядра снизу: `true`, `false` или `{filters: {tls: {fragment, spoof, kernel, engine}}}`; объект фильтров заменяет каталог |
| `detour.except_outbounds` | нет | теги, которые не выравниваются и не фильтруются |
| `sniff_guard.nodes.sni_mismatch` | `true` | защищать только узлы, у которых SNI не совпадает с `server` |
| `sniff_guard.nodes.outbound_type` | все типы | защищать только узлы этих типов |
| `sniff_guard.detour_type` | `direct`, `ssh`, `socks` — `clean`, остальные — `suspect` | стартовая оценка пути по типу релея: `never`, `clean`, `suspect` |
| `sniff_guard.override` | `false` | блок сильнее узловых `fragment` и `record_fragment` |
| `sniff_guard.tls.method` | `off` | защита TLS: `record_fragment`, `fragment`, `delay`, `off` |
| `sniff_guard.quic.method` | `off` | защита QUIC: `decoy_first_byte`, `delay`, `off` |
| `sniff_guard.tls/quic.delay` | `300ms` | пауза метода `delay` |
| `sniff_guard.tls/quic.persist` | `false` | хранить оценки релеев в `experimental.cache_file` |
| `sniff_guard.tls/quic.detour_type` | общая карта | своя карта `detour_type` для TLS или QUIC |

```jsonc
"lx": {
  "wg": { "idle": { "suspend": "30s", "suspend_reachable": "5m" } },
  "masque": { "idle_timeout": "5m" },
  "detour": { "mtu_align": "clamp", "strip": true },
  "sniff_guard": {
    "tls":  { "method": "record_fragment" },
    "quic": { "method": "decoy_first_byte" }
  }
}
```

Удалённые ключи (`lx.quic_sniff_decoy`, `lx.awg_masquerade`, плоские ключи сна `lx.wg`,
`route.lx_idle_*`) — ошибка загрузки; что ставить вместо них — в
[lx-block.ru.md](lx-block.ru.md#удалённые-ключи).

📖 Каждый подблок с логикой, ошибками загрузки и примерами — в [lx-block.ru.md](lx-block.ru.md);
энергомодель сна — в [lx-energy.ru.md](lx-energy.ru.md).

---

## 14. Tailscale: выключить Taildrop

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `disable_taildrop` | `false` | на endpoint'е `tailscale`: не принимать и не отправлять файлы Taildrop |

```json
{ "type": "tailscale", "tag": "ts", "disable_taildrop": true }
```

С `true` узел не принимает файлы: отправитель получает ошибку 403 `Taildrop is disabled on this node`. Отправка с узла тоже отвергается, в статусе `canShareFiles = false`. Файлы, полученные раньше, остаются в `taildrop_directory`, их можно открыть и удалить. Другие устройства tailnet всё равно видят узел получателем: это задаёт админка Tailscale, а не узел.

---

## 15. Outbound `impair` — ухудшение линии для испытаний

Outbound `impair` намеренно ухудшает соединения, которые через него идут: узкое место с очередью,
задержка и её разброс, потери сериями, скачки скорости, остановки и обрывы по расписанию — для
всех соединений или только для части.
Соединяется он как `direct` или через свой `detour`, в обе стороны с одного конца, без root и netem.
Нужен, чтобы проверять балансировку, failover и цепочки на настоящих серверах под плохой линией.
Build-тег `with_lx_impair` есть в десктопных `LX_TAGS` и в AAR; пока в конфиге нет
`"type": "impair"`, он ничего не делает. При старте `impair` пишет в лог предупреждение, что линия
ухудшается намеренно.

Ключи outbound'а кроме ключей соединения `direct`:

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `link_params` | без ухудшения | линия outbound'а: все ключи линии лежат здесь |
| `sublinks` | нет | вторая ступень для подошедших соединений: один сайт, порт, доля потоков |
| `schedule`, `schedule_repeat` | нет | изменения линии и подлиний по времени; длина повторяющегося круга |
| `start` | `process` | от чего отсчитывается расписание: старт процесса или первый трафик (`first_use`) |
| `seed` | случайное | зерно: одинаковое даёт те же потери и скачки |

`link_params` — один и тот же объект у outbound'а, в подлинии, в записи расписания, в `impair set` и
в профиле:

| Ключ | По умолчанию | Что задаёт |
|------|--------------|------------|
| `profile` | нет | шаблон: `dsl`, `lte-walk`, `wifi-lossy` или имя из `lx.impair_profiles`; остальные ключи его подстраивают |
| `rate`, `queue` | без ограничения, `"50ms"` | скорость узкого места (`"10mbit"`) и очередь перед ним (время или размер) |
| `delay`, `jitter` | `0` | добавочная задержка в одну сторону и её разброс |
| `loss`, `loss_burst` | `0`, `1` | доля потерь (`"0.5%"`) и средняя длина серии потерь |
| `vary`, `stall` | нет | скачки скорости и случайные остановки линии; `false` выключает их |
| `outage`, `reset` | `false` | линия молча пропадает; `reset` рвёт соединения (только расписание, `impair set`, подлиния) |
| `up`, `down` | — | те же ключи для одного направления |

Основное положение — под прокси-узлом: прокси ссылается на `impair` своим `detour`, и ухудшается
дорога до сервера прокси.

```jsonc
{"type": "vless", "tag": "via-se", "server": "se.example.net", "server_port": 443, "detour": "slow-se" /* … */},
{"type": "impair", "tag": "slow-se", "seed": 1, "link_params": {"rate": "10mbit", "delay": "20ms", "loss": "0.5%"}}
```

Линия меняется на лету командами `sing-box api impair set|schedule|status` и соответствующими
командами gRPC.

Все ключи с диапазонами, подлинии, расписание, профили, управление на лету, как ухудшаются
датаграммы и потоки, ошибки и рецепты для испытаний `round_robin` и `failover` — в
[lx-impair.ru.md](lx-impair.ru.md).
