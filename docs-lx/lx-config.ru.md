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

XHTTP (Xray «splithttp»/«xhttp») крепится к VLESS / VMess / Trojan через общий блок
`transport` и сочетается с TLS, включая **Reality**. Дефолтная форма на проводе
**байт-в-байт совпадает с лайв-проверенным v1-клиентом** — каждое v2-поле (размещение
session/seq, обфускация uplink, семейство `x_padding_*`, переиспользование соединений
`xmux`) включается явно (opt-in).

Минимальный блок `transport` — это просто `"type": "xhttp"` (режим `auto`); [пример
ниже](#пример--vless--xhttp--reality) добавляет Reality-узел с `stream-one`.

> **📖 Полный справочник полей — все 26 ключей XHTTP, их дефолты, семантика пула `xmux`,
> формы записи диапазонов и таблица диагностики — в
> [lx-protocols-transports.ru.md §1](lx-protocols-transports.ru.md#1-xhttp-транспорт)**
> ([EN](lx-protocols-transports.md#1-xhttp-transport)).

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

AWG2 = поля AWG1 **плюс** CPS-пакеты `I1`–`I5`. И клиент, и сервер должны работать на AmneziaWG с **совпадающими** параметрами (I-пакеты — это конфигурация, не согласуются). Более дружелюбный способ задать первую приманку — WireSock-style сахар `id`/`ip`/`ib`, который генерирует `i1` за вас — см. [полный справочник](lx-protocols-transports.ru.md#25-сахар-маскировки-id--ip--ib).

AWG-поля сидят в **корне** endpoint (ни одно не на peer), зеркаля секцию `[Interface]`
из `awg-quick` `.conf`: junk (`jc`/`jmin`/`jmax`), паддинг handshake (`s1`–`s4`),
magic-заголовки (`h1`–`h4`, одно значение или диапазон `"min-max"`) и CPS-приманки
(`i1`–`i5`). AWG-endpoint нуждается в **пониженном `mtu`** относительно обычного
WireGuard, потому что `s4` паддит каждый data-пакет — ядро дефолтит на `1280`, когда вы
задали `s4` и опустили `mtu`.

> **📖 Полный справочник полей — каждое junk/signature/magic/CPS-поле с типом и дефолтом,
> формат CPS-тегов, сахар маскировки `id`/`ip`/`ib` (четыре профиля, какой выбрать, что
> попадает на провод), математика бюджета MTU, маппинг `awg.conf` 1:1 и дословные ошибки
> валидации — в
> [lx-protocols-transports.ru.md §2](lx-protocols-transports.ru.md#2-amneziawg-20-awg2)**
> ([EN](lx-protocols-transports.md#2-amneziawg-20-awg2)).

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

**Сахар маскировки `id`/`ip`/`ib`** (четыре профиля приманки: `quic`/`dns`/`stun`/`sip`),
**бюджет MTU** (почему `s4` форсит пониженный MTU, симптом `sendmsg: message too long`,
авто-дефолт `1280`, `udp_fragment` для вложенных туннелей) и **маппинг `awg.conf` 1:1** —
всё в полном справочнике; см. 📖-ссылку выше.

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

Обязательные поля — `server`/`server_port`, пара ключей (`private_key`/`public_key`, для
дефолтного профиля `cloudflare`) и хотя бы один из `ip`/`ipv6` (твой локальный адрес
*внутри* туннеля, не выходной IP). У всего остального есть дефолт: `profile: cloudflare`,
`vhttp: h3`, `tls.server_name: www.cloudflare.com`, `mtu: 1280`, `idle_timeout: 5m`,
`keep_alive_period: 30s`, `network_list: tcp+udp`. TLS — в стандартном блоке `tls`
outbound'а.

> **SNI по умолчанию — `www.cloudflare.com`, а не имя эндпоинта** — назвать MASQUE-эндпоинт
> в ClientHello это ровно то, по чему его режет DPI. Эндпоинт аутентифицируется пиннингом
> `public_key`, поэтому SNI волен отличаться.

> **📖 Полный справочник полей — каждое поле с типом и дефолтом, матрица профилей
> (`cloudflare` vs `standard`), формат ключевого материала, гайд `vhttp` h3-vs-h2,
> поведение idle-suspend/keepalive, валидация при старте, таблица миграции с до-SPEC-062
> и частые грабли — в
> [lx-protocols-transports.ru.md §3](lx-protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)**
> ([EN](lx-protocols-transports.md#3-masque-outbound-connect-ip--warp)).

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

**📖 [Полный справочник →](lx-protocols-transports.ru.md#3-masque-outbound-connect-ip--warp)**
([EN](lx-protocols-transports.md#3-masque-outbound-connect-ip--warp)) — полная таблица
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
