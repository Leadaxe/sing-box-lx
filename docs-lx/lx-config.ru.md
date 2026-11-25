# sing-box-lx — конфигурация фич форка

> 🌐 English version: **[lx-config.md](lx-config.md)**.

> 🧭 **Где что искать.** Документация форка — три уровня, по вопросу читателя:
> [lx-config](lx-config.ru.md) — что есть в форке и как включить;
> [protocols-transports](protocols-transports.ru.md) — каждое поле, тип, дефолт, текст ошибки;
> [xray-protocols-explained](xray-protocols-explained.ru.md), [amneziawg-explained](amneziawg-explained.ru.md) и [masque-explained](masque-explained.ru.md) —
> как устроено, почему, как сделано у нас и чем отличается от ванили.

## Оглавление

- [Фичи и build-теги](#фичи-и-build-теги)
- [0. Все поля разом (исчерпывающий пример)](#0-все-поля-разом-исчерпывающий-пример)
- [1. XHTTP-транспорт](#1-xhttp-транспорт)
  - [Пример — VLESS + XHTTP + Reality](#пример--vless--xhttp--reality)
- [2. AmneziaWG 2.0/3.x (AWG2, AWG3)](#2-amneziawg-203x-awg2-awg3)
  - [Пример — AmneziaWG 3.1 endpoint (экспорт Amnezia `amnezia-awg2`)](#пример--amneziawg-31-endpoint-экспорт-amnezia-amnezia-awg2)
  - [Пример — AmneziaWG 2.0 endpoint](#пример--amneziawg-20-endpoint)
- [3. Режимы выбора узла в urltest](#3-режимы-выбора-узла-в-urltest)
- [4. MASQUE outbound — Cloudflare WARP](#4-masque-outbound--cloudflare-warp)
- [5. Группа DNS-серверов](#5-группа-dns-серверов)
- [6. VLESS `encryption` — пост-квантовый слой](#6-vless-encryption--пост-квантовый-слой)
  - [Поле (на `vless`-outbound)](#поле-на-vless-outbound)
  - [Пример](#пример)
- [7. REALITY `key_share` — гибридный или классический ClientHello](#7-reality-key_share--гибридный-или-классический-clienthello)
- [8. Наблюдаемость (расширения CommandClient)](#8-наблюдаемость-расширения-commandclient)
- [9. ClientHello под `detour` — без дефолта](#9-clienthello-под-detour--без-дефолта)
- [10. Outbound `chain` — виртуальная цепочка хопов из групп и узлов](#10-outbound-chain--виртуальная-цепочка-хопов-из-групп-и-узлов)
- [11. Снифферы протоколов для трафика из LAN](#11-снифферы-протоколов-для-трафика-из-lan)
- [12. Проверка и сборка](#12-проверка-и-сборка)
- [13. Корневой блок `lx`](lx-block.ru.md)
- [14. Tailscale: выключить Taildrop](#14-tailscale-выключить-taildrop)

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
| **Корневой блок `lx`** | — (разбирается всегда) | `lx.wg`, `lx.masque`, `lx.trusttunnel`, `lx.mtu_align`, `lx.sniff_guard` — глобальные ручки форка, [lx-block.ru.md](lx-block.ru.md) | desktop + mobile |
| **Группа DNS-серверов** | — (всегда в сборке) | `dns.servers[].type: "group"` | desktop + mobile |
| **VLESS `encryption`** | — (всегда в сборке) | `encryption` на `vless`-outbound | desktop + mobile |
| **Демон `lxd`** | `with_lxd` | не ключ конфига — подкоманда `sing-box lxd` + `<state-dir>/daemon.json`; см. [lxd-daemon.ru.md](lxd-daemon.ru.md) | desktop / сервер (не Win7, не AAR) |
| **Outbound `chain`** | `with_lx_chain` | `outbounds[].type: "chain"` — многохоповый путь из групп и узлов | desktop + mobile |

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

XHTTP (в Xray — «splithttp»/«xhttp») — v2ray-транспорт: он туннелирует прокси поверх обычных
HTTP/2-запросов. XHTTP крепится к VLESS / VMess / Trojan через общий блок `transport` и сочетается
с TLS, включая Reality. С XTLS-Vision XHTTP несовместим; это ограничение протокола, а не
форка. Дефолтная форма на проводе байт-в-байт совпадает с v1-клиентом, проверенным вживую.
Каждое v2-поле включается явно (opt-in): размещение session/seq, обфускация uplink, семейство
`x_padding_*`, переиспользование соединений `xmux`.

Минимальный блок `transport` — просто `"type": "xhttp"` (режим `auto`). [Пример
ниже](#пример--vless--xhttp--reality) добавляет Reality-узел с `stream-one`.

> 📖 Полный справочник полей — в
> [protocols-transports.ru.md §1](protocols-transports.ru.md#1-xhttp-транспорт)
> ([EN](protocols-transports.md#1-xhttp-transport)): все 26 ключей XHTTP с дефолтами,
> семантика пула `xmux`, формы записи диапазонов и таблица диагностики.

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

## 2. AmneziaWG 2.0/3.x (AWG2, AWG3)

AWG — это WireGuard + обфускация против DPI. Настраивается как обычный sing-box `wireguard` endpoint с дополнительными полями. С `with_awg` они передаются на устройство. Конфиг без единого AWG-поля — обычный WireGuard endpoint, поведение байт-в-байт как в upstream.

AWG2 = поля AWG1 плюс CPS-пакеты `I1`–`I5`. И клиент, и сервер должны работать на AmneziaWG с совпадающими параметрами: I-пакеты — это конфигурация, они не согласуются. Первую приманку проще задать WireSock-style сахаром `id`/`ip`/`ib`: он генерирует `i1` сам. Сахар описан в [полном справочнике](protocols-transports.ru.md#25-сахар-маскировки-id--ip--ib).

AWG3 — это amneziawg-go v3.0/v3.1, контейнер Amnezia `amnezia-awg2` с `protocol_version` 3.x. Он добавляет защиту заголовка, паддинг содержимого, случайные хвосты, отключённые cookie, диапазонные тайминги и диапазонный `persistent_keepalive_interval`. Ключ защиты заголовка `header_protection_key` серверный и обязан совпасть с сервером. Все поля AWG3, как и AWG2, лежат на корне endpoint — [справочник §2.10](protocols-transports.ru.md#210-awg-3x-защита-заголовка-паддинг-хвосты-тайминги).

AWG-поля лежат на корне endpoint, ни одно не на peer, как в секции `[Interface]`
файла `awg-quick` `.conf`: junk (`jc`/`jmin`/`jmax`), паддинг handshake (`s1`–`s4`),
magic-заголовки (`h1`–`h4`, одно значение или диапазон `"min-max"`) и CPS-приманки
(`i1`–`i5`). AWG-endpoint нужен `mtu` ниже, чем у обычного WireGuard, потому что `s4`
паддит каждый data-пакет. Если `s4` задан, а `mtu` нет, ядро ставит `1280`.

> 📖 Полный справочник полей — в
> [protocols-transports.ru.md §2](protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3)
> ([EN](protocols-transports.md#2-amneziawg-203x-awg2-awg3)): каждое junk/signature/magic/CPS-поле
> с типом и дефолтом, формат CPS-тегов, сахар маскировки `id`/`ip`/`ib` (четыре профиля, какой
> выбрать, что попадает на провод), математика бюджета MTU, маппинг `awg.conf` 1:1 и дословные
> ошибки валидации.

### Пример — AmneziaWG 3.1 endpoint (экспорт Amnezia `amnezia-awg2`)

```jsonc
{
  "type": "wireguard", "tag": "awg3-out", "system": false, "mtu": 1376,
  "address": ["10.8.1.7/32"],
  "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 10, "jmax": 50,
  "s1": 55, "s2": 42, "s3": 40, "s4": 12,      // все >= 12 для защиты заголовка
  "h1": 1, "h2": 2, "h3": 3, "h4": 4,
  "header_protection_key": "<HeaderProtectionKey-base64>",
  "content_padding_addition": "10-100",
  "rekey_after_time": "100-120", "rekey_timeout": "3-7", "reject_after_time": "150-180",
  "keepalive_timeout": "5-15", "max_handshake_attempts": "15-20",
  "random_trailers": true, "disable_cookies": true,
  "peers": [ { "address": "77.239.123.44", "port": 30565,
    "public_key": "<server-public-key-base64>", "pre_shared_key": "<preshared-key-base64>",
    "allowed_ips": ["0.0.0.0/0", "::/0"], "persistent_keepalive_interval": "25-35" } ]
}
```

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

Подробно в [полном справочнике](protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3) разобраны три темы:

- сахар маскировки `id`/`ip`/`ib` — четыре профиля приманки: `quic`/`dns`/`stun`/`sip`;
- бюджет MTU — почему `s4` требует пониженный MTU, симптом `sendmsg: message too long`,
  авто-дефолт `1280`, `udp_fragment` для вложенных туннелей;
- маппинг `awg.conf` 1:1.

Рантайм — `Leadaxe/wireguard-go`: sagernet/wireguard-go с обфускацией AmneziaWG, подключён сабмодулем `submodules/wireguard-go`. Как устроена фича — в [описании фичи AWG](../SPECS/FEATURES/003-AWG/FEATURE.md).

---

## 3. Режимы выбора узла в urltest

Upstream `urltest` всегда выбирает узел с наименьшей задержкой. sing-box-lx добавляет ещё два
режима:

| `mode` | Выбор | Переключение | Что проверяется каждый `interval` |
|---|---|---|---|
| `least_test` (по умолчанию) | самый быстрый | когда другой быстрее на `tolerance` или подключение через текущий не удалось | все узлы |
| `round_robin` | пул живых узлов | соединения раскладываются по пулу | только пул |
| `failover` | самый быстрый на момент выбора | только когда текущий отказал или стал вдвое медленнее остальных; дальше снова самый быстрый | только текущий узел |

Поля `url`, `interval`, `idle_timeout` и `interrupt_exist_connections` работают как в апстриме.
Поля `mode` и `balancer` работают в любой сборке; состояние пула для интерфейса (`GetPool`,
[§8](#8-наблюдаемость-расширения-commandclient)) — в сборке с `with_lx_command`.

Поля, как работает каждый режим, самовосстановление пула `round_robin` и примеры — в
[lx-urltest.ru.md](lx-urltest.ru.md).

---

## 4. MASQUE outbound — Cloudflare WARP

Outbound `masque` подключается к Cloudflare WARP и другим серверам CONNECT-IP (RFC 9484): целые
IP-пакеты идут поверх HTTP/3 или HTTP/2, а ядро поднимает на туннель свой userspace-стек, как у
WireGuard-endpoint'а. С AWG-сахаром *masquerade* `id/ip/ib` из §2 у него общее только слово.
Нужны теги `with_quic` и `with_gvisor`, оба есть в `LX_TAGS`. Ключи берутся готовыми из конфига:
устройство в WARP регистрирует клиент, а не ядро.

Обязательные поля — `server`/`server_port`, пара ключей `private_key`/`public_key` и хотя бы один
из `ip`/`ipv6`: это адрес клиента внутри туннеля, а не выходной IP. Нужен блок `dns` верхнего
уровня: домены outbound разрешает через DNS-роутер. У остальных полей есть значения по умолчанию:

| Поле | По умолчанию |
|------|--------------|
| `profile` | `cloudflare` |
| `vhttp` | `auto` |
| `tls.server_name` | `www.cloudflare.com` |
| `mtu` | `1280` |
| `idle_timeout` | выключен или глобальный `lx.masque.idle_timeout` ([lx-block.ru.md](lx-block.ru.md)); ключ узла сильнее глобального, `"0"` тоже |
| `keep_alive_period` | `30s` |
| `network_list` | `tcp+udp` |

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "private_key": "<base64 DER EC private key>",
  "public_key":  "<base64 DER PKIX public key>",
  "ip":   "172.16.0.2/32",
  "ipv6": "2606:4700:110:...::/128"
}
```

- SNI по умолчанию — `www.cloudflare.com`, а не имя эндпоинта: по имени MASQUE-эндпоинта в
  ClientHello его режет DPI, а сервер проверяется по `public_key`.
- `vhttp: auto` сначала пробует h3 и переходит на h2 (TCP:443), если QUIC не поднялся за 3 с, и
  запоминает, что сработало. Это для сетей и хопов в `detour`, которые молча глотают QUIC.
  Профиль `standard` работает только по h3 и требует `uri`.
- Узлы над masque (через `detour`, группу или в `chain`) получают размер пакета под `mtu` сами
  ([`lx.mtu_align`](lx-block.ru.md#lxmtu_align--выравнивание-mtu-и-размера-пакета-по-пути)): WARP теряет
  внутренние IPv6-фрагменты. Внешний QUIC к серверу стартует с пакета `mtu + 51` байт.
- Версию HTTP задаёт `vhttp`; поле `network` в `masque`-узле — ошибка загрузки.

Проверено на устройстве в Wi-Fi и LTE, по `h3` и по `h2`, вместе со сном и восстановлением после
обрыва.

📖 Как устроено и почему — [masque-explained.ru.md](masque-explained.ru.md). Все поля, профили, примеры и частые грабли — в
[protocols-transports.ru.md §3](protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)
([EN](protocols-transports.md#3-masque-outbound-connect-ip--warp)).

---

## 5. Группа DNS-серверов

Тип `group` в `dns.servers[]` объединяет несколько DNS-серверов под одним тегом: если один сервер
не отвечает, запрос уходит к другим. Режимы: `stable` держится за текущий сервер, `fastest` берёт
тот, что чаще отвечал первым, `parallel` спрашивает всех сразу. Ключ `hosts` отвечает на
локальные имена до обращения к сети. Build-тег не нужен.

```jsonc
{ "type": "group", "tag": "public", "servers": ["google", "cloudflare"], "mode": "stable" }
```

Поля, как группа выбирает сервер, `hosts` и примеры — в [lx-dns-group.ru.md](lx-dns-group.ru.md).

## 6. VLESS `encryption` — пост-квантовый слой

Плоское поле `encryption` на `vless`-outbound включает рукопожатие `mlkem768x25519plus` внутри
VLESS. Этот слой лежит над транспортом и TLS, под VLESS-клиентом. От key exchange REALITY он не
зависит: это другой слой. Серверы, которые требуют его (в Xray настроен `decryption`), молча
рвут обычный VLESS. Транспорт поднимается: WS отвечает `101`, gRPC — на SETTINGS. После этого
пир закрывает соединение без единой строки в логе ядра; этот симптом поле и лечит. В форке
только клиентская половина: форк клиентский, серверная половина не портирована. Поле всегда в
сборке, build-тега нет.

### Поле (на `vless`-outbound)

| Ключ | Тип | По умолчанию | Значение |
|------|-----|--------------|----------|
| `encryption` | string | `""` | `""`/`"none"` = слой выключен (поведение upstream байт-в-байт). Иначе — spec-строка; `check` и старт проверяют её, ошибка называет конкретный сегмент |

Грамматика spec-строки (сегменты через точку):

```
mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<padding>…].<ключ>[.<ключ>…]
```

- **appearance** — как слой выглядит на проводе: `native` (AEAD-заголовки в форме
  TLSv1.3), `xorpub` (XOR по публичному ключу), `random`.
- **rtt** — `0rtt` или `1rtt`.
- **padding** — опциональные короткие блоки вида `100-111-1111`. Сегмент короче
  20 символов до первого ключа читается как padding.
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

Поле вышло в `v1.14.0-lx.18` и проверено на устройстве на подписке, из-за которой появилось:
заработали 10 узлов, мёртвых без него (6 из 8 WS, 4 из 4 gRPC), в остальных группах транспортов
ничего не изменилось. В поле встречается форма `native.0rtt`. `1rtt` с padding и
`xorpub`/`random` парсятся и собираются, но живого сервера с ними ещё не встречали.

> ⚠️ В подписке значение приходит как `settings.vnext[0].users[0].encryption`. На
> sing-box-outbound это плоское поле `encryption` рядом с `uuid`. Если билдер конфига его
> теряет, ядру нечего включать.

---

## 7. REALITY `key_share` — гибридный или классический ClientHello

Поле `key_share` — строка в `tls.reality`, задаётся на каждом узле:

```json
"reality": { "enabled": true, "public_key": "…", "short_id": "0123abcd", "key_share": "classical" }
```

| Значение | ClientHello | Работает против |
|---|---|---|
| `""` (не задано) | Как несёт отпечаток: `chrome` / `firefox` / `safari` шлют гибридный шар `X25519MLKEM768`, `edge` / `ios` / `android` / `360` / `qq` — только X25519. Ядро ничего не меняет. | любой Xray, если отпечаток несёт гибрид; иначе только Xray < v26.9.8 |
| `"classical"` | `X25519MLKEM768` вырезан из `key_share` и `supported_groups`. Приветствие как у апстрима, на ~1,2 КБ короче (`chrome`: 594 байта вместо 1720, один TCP-сегмент вместо двух). | Только Xray < v26.9.8. Новые серверы молча отвергают приветствие без гибрида (`reality verification failed`). |
| `"hybrid"` | Гибрид обязателен. На отпечатке без него рукопожатие сразу падает с `reality key_share "hybrid": fingerprint Edge 85 carries no X25519MLKEM768 key share`. Без этого значения сервер отказал бы молча, и отказ не отличить от чужого ключа. На `chrome` / `firefox` / `safari` ничего не меняет. | как `""` |

Любое другое значение — ошибка конфига при загрузке: опечатка не должна молча становиться
дефолтом.

В некоторых сетях двухсегментное гибридное приветствие теряется, а односегментное классическое
проходит (LxBox #142). Клиент самого Xray упирается в ту же проблему (XTLS#6256). Новые
Xray-серверы гибрид требуют. Ядро не может выбрать за пользователя между «сервер отвергнет» и
«сеть потеряет», поэтому выбор задаётся на узле. `classical` снижает постквантовую защиту и
годится только для старых серверов. Для нового сервера в такой сети остаются `record_fragment`
([§9](#9-clienthello-под-detour--без-дефолта)) и `detour`.

---

## 8. Наблюдаемость (расширения CommandClient)

Это дополнения клиентского API, а не конфига: методы на `CommandClient` libbox (нативный
gRPC-канал управления). Все они за тегом `with_lx_command`, их вызывает LxBox. В конфиг-файл
sing-box они ничего не добавляют: методы появляются в сборке с тегом, а вызывает их клиент. Без
тега методов нет, и демон обслуживает только апстримный набор команд. Разбор — в [фиче
OBSERVABILITY](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md).

Методы, которые форк добавляет в `CommandClient`:

- **`URLTestOutbound(tag, link, timeout)`** — синхронно измеряет задержку одного outbound
  или endpoint по требованию, вне периодического теста группы.
- **`GetRules()`** — отдаёт снимок таблицы маршрутных правил: route-правила и DNS-правила.
- **`GetGroups()`** — отдаёт снимок outbound-групп; те же данные пушит поток групп.
- **`GetOutbounds()`** — отдаёт плоский список outbound и endpoint. Он нужен рядом с
  `GetGroups`, потому что отдельно стоящие outbound не входят ни в одну группу. Элемент
  WG/AWG-endpoint'а ещё несёт `EndpointState` (`never_built` / `building` / `up` / `asleep`
  / `torn_down` / `down` / `disabled`) и `IdleSinceSeconds` — время с последнего дайла. У
  прочих outbound оба поля пусты или 0
  ([lx-energy.ru.md §11](lx-energy.ru.md#11-ленивая-сборка-и-бюджет-не-дают-собирать-устройства-впустую)).
  Состояние `disabled` выставляет `SetEndpointEnabled` (ниже;
  [lx-energy.ru.md §12](lx-energy.ru.md#12-ручной-переключатель-выключает-endpoint-без-правки-конфига)).
- **`GetPool(groupTag)`** — отдаёт текущий пул ротации round_robin группы `urltest`, слот
  за слотом ([§3](#3-режимы-выбора-узла-в-urltest)). По каждому слоту ещё отдаёт
  `Quarantined`, `Connections` и `ConfirmedFailures`.
- **`GetPoolState(groupTag)`** — отдаёт те же слоты и состояние группы: `ShiftedKeys`,
  `MarkedKeys`, `Frozen` и `FrozenSeconds`
  ([самовосстановление](lx-urltest.ru.md#самовосстановление-пула)).
- **`GetDNSGroups()`** — отдаёт живое состояние каждого DNS-сервера `group`
  ([§5](#5-группа-dns-серверов)): по каждому участнику `clean` / `liveErrors` /
  `lastErrorAgeMs` / `liveWins` / `current`.
- **`GetRunningConfig()`** — отдаёт канонический JSON options, из которых построен
  работающий box, после override. Результат — объект с аксессором `Content()`: голый
  `string`-возврат ронял бы gomobile на android/arm64.
- **`SubscribeDNSQueries(includeAnswers, handler)`** — подписывает на структурный живой
  поток DNS-запросов. По каждому запросу приходят `domain`, `qtype`, `rcode`, CNAME-цепочка
  и ответы (при `includeAnswers`), привязка к процессу, `dnsServer` / `dnsServerType` /
  `outbound`. `rcode` `-1` означает ошибку резолва — это полноправное состояние. Пустой
  `outbound` означает direct/system — тоже валидное состояние, не баг.
- **`GetChains()`** — отдаёт состояние каждого outbound `chain`
  ([§10](#10-outbound-chain--виртуальная-цепочка-хопов-из-групп-и-узлов)): по позициям
  разрешённый узел, а для позиций ≥ 1 — звено (`starting|active|idle`, живые соединения,
  эффективный MTU и причина, что снял `strip`, применён ли `rewrite`, последняя ошибка).
  Метод отдаёт и счётчики дозвонов, ошибок и звеньев.
- **`SetEndpointEnabled(tag, enabled)`** — выключает или включает WG/AWG-endpoint на ходу
  ([lx-energy.ru.md §12](lx-energy.ru.md#12-ручной-переключатель-выключает-endpoint-без-правки-конфига)). Возвращает
  объект `EndpointToggleResult`; его поле `State` — состояние endpoint'а после вызова.

Поток соединений тоже расширен: отслеживаемое `Connection` несёт отдельное поле **`detourList`**
— хвост transport-detour'а финального outbound. Он отдаётся отдельно от прокси-`chain`, потому
что `chain` не включает detour.

Методы появляются в сборке с тегом:

```sh
make -f Makefile.lx lx-build   # включает with_lx_command (и with_xhttp/with_awg)
```

---

## 9. ClientHello под `detour` — без дефолта

> ⚠️ Без блока `lx.sniff_guard` ядро режет ClientHello как апстрим: только там, где у узла
> задан `fragment` или `record_fragment`, через `detour` тоже. В прежних релизах
> TLS-over-TCP outbound (VLESS, trojan, vmess, anytls, shadowtls, http, masque `h2`, …),
> который диалит через `detour`, по умолчанию резал ClientHello на TLS-записи
> (`record_fragment`). Конфиг без блока загружается без ошибки, а TLS-узлы через релеи,
> которые перенаправляют соединение по SNI, перестают подниматься.

Разрез защищает от релея, который читает SNI из ClientHello и отправляет соединение на это имя
вместо назначения узла. Так работает Xray `sniffing` с `destOverride` без `routeOnly`. Узел, у
которого SNI — маскировка (WARP с чужим именем, REALITY), получает соединение не туда: хендшейк
висит и падает с `tls handshake: EOF` через 12–17 с, либо приходит чужой ключ. ClientHello,
разрезанный на TLS-записи, TLS-сниффер Xray не разбирает. Замер через сниффящее плечо: без
разреза ❌ отказ за 12 с; `fragment` ✅ 0.6 с; `record_fragment` ✅ 0.1 с.

Защиту включает блок [`lx.sniff_guard`](lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni) (§13).
Минимальный блок:

```json
"lx": { "sniff_guard": { "tls": { "method": "record_fragment" } } }
```

С ним путь через любой релей, кроме `ssh` и `socks`, стартует с разрезом, прямой путь — без.
Путь через `ssh` и `socks` стартует без разреза. Разрез на нём включается, если узел на этом
пути упал без разреза и прошёл с ним. Чтобы с разрезом стартовали все релеи, как при прежнем
дефолте, добавьте `"detour_type": {"ssh": "suspect", "socks": "suspect"}`. Карта накладывается
поверх дефолтов, поэтому перечислять нужно только то, что меняется. Узлы, у которых SNI
совпадает с `server`, блок по умолчанию не защищает (`nodes.sni_mismatch`). Прежний дефолт резал
и их; чтобы резать их тоже, добавьте `"nodes": {"sni_mismatch": false}`. В отличие от прежнего
дефолта, блок учится по исходу подключений и может снять разрез с пути, где он мешает.

Узловые ключи апстрима:

- **`fragment` / `record_fragment`** у узла действуют как в апстриме, с блоком и без него.
  Блок перекрывает их только при `"override": true`.
- **`"record_fragment": false`** — узел просит ClientHello одной записью. Под блоком без
  `override` такой узел идёт без разреза на любом пути.
- **REALITY-узлы** — `fragment` / `record_fragment` действуют и на них. В апстриме
  REALITY-клиент строит uTLS-соединение на голом сокете, и эти ключи там молча не
  действуют. Гибридный ClientHello занимает 1,5–1,9 КБ, это два TCP-сегмента. Поможет ли
  разрез в сети, которая теряет такой первый пакет, — свойство той сети. Второй способ —
  `key_share` ([§7](#7-reality-key_share--гибридный-или-классический-clienthello)).
- Разрезается только хендшейк, трафик после него не трогается.

---

## 10. Outbound `chain` — виртуальная цепочка хопов из групп и узлов

Build-тег `with_lx_chain` входит в десктопные `LX_TAGS` и в AAR. Без него `"type": "chain"`
отвергается при чтении конфига. Разбор — в [фиче CHAIN](../SPECS/FEATURES/015-CHAIN/FEATURE.md).

```json
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "idle_timeout": "5m",
  "strip_evasion": true,
  "strip": { "multiplex.padding": false, "tls.utls": true },
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

Позиции в `outbounds` идут в порядке пакета. `outbounds[0]` — первый хоп от клиента: он касается
реальной сети и используется как есть, с его dial-полями. Последняя позиция — узел, чей адрес
видит цель. Запись через `detour` читается наоборот: там узел с `detour` — выход. Любая позиция
— узел, endpoint или группа любой вложенности. Все три формы работают одинаково, длина любая (≥
2):

```
["selector-in", "selector-mid", "selector-exit"]   // все группы
["node-in",     "node-mid",     "node-exit"]       // все узлы
["node-in",     "selector-mid", "node-exit"]       // смешанно
```

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `outbounds` | список тегов, ≥ 2, все различны | обязательное | позиции в порядке пакета; повтор тега — ошибка старта, см. ниже |
| `idle_timeout` | длительность | `5m` | простаивающее звено (см. ниже) без живых соединений удаляется через это время; `0` — жить до остановки |
| `strip_evasion` | bool | `true` | снимать односторонние DPI-приёмы у звеньев на позициях ≥ 1 (каталог ниже) |
| `strip` | карта ключ → bool | `{}` | патч к каталогу: `false` — оставить, `true` — снимать дополнительно; неизвестный ключ — ошибка старта |
| `rewrite` | карта тип → JSON-объект | `{}` | merge-patch (RFC 7396) к конфигу каждого звена этого типа на позициях ≥ 1 |

Теги в `outbounds` должны различаться: повтор валит старт с `duplicate outbound in chain`,
потому что в списке, который читается как путь, это почти всегда описка. Чтобы поставить один
узел на несколько позиций намеренно, объявите его отдельным тегом на каждую позицию; учётные
данные в объявлениях могут совпадать. Так строится «сэндвич» `WARP → чужой узел → WARP`: внешние
слои прячут середину от вашего адреса, а цель — от середины. Каждая позиция получает своё звено
со своим detour (звено ключуется парой «позиция, узел»), и экземпляры независимы.

Группы не копируются: цепочка зовёт оригинальную группу, и та выбирает со всей своей логикой
(ручной выбор, health-check, sticky, штрафы, `interrupt_exist_connections`). Узел, выбранный на
позиции ≥ 1, работает через своё звено — рантайм-экземпляр этого узла, который дозванивается до
своего сервера через предыдущую позицию. Звенья создаются при первом использовании. На старте
прогреваются позиции с известным выбором — узлы и селекторы; позиции с urltest остаются
ленивыми. Звено несёт тег узла, поэтому история и штрафы группы продолжают работать. Звено
удаляется, только когда у него ноль живых соединений и его не выбирали дольше `idle_timeout`.
Переключение группы не убивает старое звено, пока через него идёт поток. WireGuard-звенья
засыпают по правилам idle-suspend, как любой endpoint.

- **`direct` на позиции ≥ 1 прозрачен**: хопа нет. Положите `direct` в селектор, чтобы
  выключать позицию на лету. `block` отвергает. Если все позиции ≥ 1 — `direct`, цепочка
  равна позиции 0.
- **MTU и размер пакета звеньев выравниваются автоматически** общим механизмом
  `lx.mtu_align` ([lx-block.ru.md](lx-block.ru.md#lxmtu_align--выравнивание-mtu-и-размера-пакета-по-пути)), тем же,
  что под обычным `detour`. `mtu` в конфиге узла означает «как у самостоятельного узла».
  Цепочка вычитает точные накладные IP-туннелей *под* звеном и берёт худший случай по
  участникам группы: WG внутри IP-туннеля −60/−80 по семейству адреса сервера, MASQUE
  −79/−99. QUIC-звенья (hysteria2, tuic, hysteria) на позициях ≥ 1 получают
  `initial_packet_size` под ёмкость хопа ниже и `disable_path_mtu_discovery: true`. Над
  любым прокси (vless/trojan/ss/hysteria2/…) ничего не меняется. Режим (`clamp` по
  умолчанию, `fill`, `off`) и `except` действуют на звенья так же.
- **Каталог `strip`** — односторонние приёмы, сервер их не видит:
  - `tls.fragment` — пакетная фрагментация ClientHello и `fragment_fallback_delay`.
    `record_fragment` каталог не трогает: защиту пути от перенаправления по SNI задаёт
    `lx.sniff_guard` (§9, §13);
  - `multiplex.padding`;
  - `xhttp.padding` — `x_padding_bytes` фиксируется на нижней границе диапазона узла, без
    явного диапазона — на `100`; размещение и obfs-режим не меняются;
  - `tls.utls` — только явным `"tls.utls": true`; на узле с `reality` это ошибка старта.
- Контракты с сервером — `flow`, `obfs`, `shadowtls`, `plugin`, `udp_over_tcp`, `ech`,
  пути транспортов — не снимаются никогда.
- Порядок преобразований звена: `strip` → `rewrite` → MTU. Все патчи прогоняются всухую на
  старте по каждому узлу, достижимому на позициях ≥ 1: `rewrite` с неизвестным полем валит
  старт, а не первый дозвон.

> ⚠️ По умолчанию цепочка снимает фрагментацию ClientHello на позициях ≥ 1: обычный DPI
> стоит между вами и первым хопом. Если позиция 0 — домашний relay, а DPI стоит на границе
> (`["relay", "foreign-node"]`), фрагментация нужна на позиции 1. Для этого поставьте
> `"strip": {"tls.fragment": false}`.

`detourList` соединения ([§8](#8-наблюдаемость-расширения-commandclient)) показывает разрешённый
путь. `GetChains` (CommandClient) и Clash API `/proxies/<tag>` → `chain` отдают состояние по
позициям и счётчики. Состояние позиции — выбранный узел, состояние звена `starting|active|idle`,
живые соединения, эффективный MTU и причина, что снято или переписано, последняя ошибка. Ошибки
дозвона называют позицию и хоп под ней: `chain[virtualisation] #2 (warp-exit) via #1 (node-m):
…`. Задержку по слоям меряет URLTest по внутренним тегам `<tag>#0`, `<tag>#1`, …; каждый тег
меряет путь до своей позиции.

Известные границы: группа на позиции ≥ 1 ранжирует узлы по *прямым* замерам; туннель над хопом
без UDP падает при дозвоне, и ошибка называет обе позиции; вложенный `chain` допустим только на
позиции 0.

---

## 11. Снифферы протоколов для трафика из LAN

Форк добавляет имена протоколов для списка `sniffer` действия `sniff` и матчера `protocol` в
правилах: `wireguard`, `openvpn`, `ike`, `tailscale`, `sip`. `sip` ещё ставит `domain` из
Request-URI. Снифферы узнают VPN-туннели и звонки других устройств за роутером по форме первого
пакета. Они стоят перед апстримным uTP-сниффером, потому что тот помечает plain WireGuard как
`bittorrent`. Считается только первый пакет потока, поэтому junk и decoy насквозь не видны.
Порядок снифферов, ограничения и пример для роутера — в [lx-sniff.ru.md](lx-sniff.ru.md).

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

Глобальные ручки форка живут в одном корневом блоке `lx`, сгруппированные по подсистеме. Всё, что
описывает конкретный узел, остаётся в его объекте, как в апстриме: поля AWG — в endpoint'е, `xhttp` —
в `transport`, собственный `idle_timeout` masque-узла — в узле. Все подблоки и ключи опциональны;
пустой `lx` и отсутствующий равнозначны. Опечатка в имени ключа — ошибка загрузки, не тихий дефолт.
Running-config (`GetRunningConfig`) отдаёт блок `lx` в канонической форме: пустые подблоки отброшены.

Подблоки и где они описаны:

- [`lx.wg` — endpoint'ы WireGuard / AmneziaWG](lx-block.ru.md#lxwg--endpointы-wireguard--amneziawg)
- [`lx.masque` — MASQUE-outbound'ы](lx-block.ru.md#lxmasque--masque-outboundы)
- [`lx.trusttunnel` — TrustTunnel-outbound'ы](lx-block.ru.md#lxtrusttunnel--trusttunnel-outboundы)
- [`lx.mtu_align` — выравнивание MTU и размера пакета по пути](lx-block.ru.md#lxmtu_align--выравнивание-mtu-и-размера-пакета-по-пути)
- [`lx.sniff_guard` — защита от перенаправления по SNI](lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni)
- [Удалённые ключи](lx-block.ru.md#удалённые-ключи)
- [Зарезервировано: `lx.naive`](lx-block.ru.md#зарезервировано-lxnaive)

Ключи каждого подблока, ошибки загрузки и примеры — в [lx-block.ru.md](lx-block.ru.md).

---

## 14. Tailscale: выключить Taildrop

Ключ `disable_taildrop` на endpoint'е `tailscale`. Без ключа Taildrop работает как в апстриме.

```json
{ "type": "tailscale", "tag": "ts", "disable_taildrop": true }
```

С `true` узел не принимает файлы: отправитель получает ошибку 403 `Taildrop is disabled on this node`. Отправка с узла тоже отвергается, в статусе `canShareFiles = false`. Файлы, полученные раньше, остаются в `taildrop_directory`, их можно открыть и удалить. Другие устройства tailnet всё равно видят узел получателем: это задаёт админка Tailscale, а не узел.

