# sing-box-lx — все поля фич форка в одном конфиге

> EN: [lx-config-example.md](lx-config-example.md)

Один конфиг несёт все поля outbound-фич: XHTTP-транспорт, AmneziaWG 2.0 endpoint,
masquerade-сахар `id`/`ip`/`ib`, VLESS `encryption`, `round_robin`-балансировщик `urltest`
и корневой блок `lx` со всеми ключами. У MASQUE и DNS-группы свои примеры — в [§4](lx-config.ru.md#4-masque-outbound--cloudflare-warp)
и [§5](lx-config.ru.md#5-группа-dns-серверов). Блок `lx` описан в [lx-block.ru.md](lx-block.ru.md), его энергоключи — в [lx-energy.ru.md](lx-energy.ru.md).

Это справочник «всё сразу», а не рекомендуемый конфиг. Многие поля взаимоисключающие
(сахар `id`/`ip`/`ib` и написанный вручную `i1`) либо серверные и клиентом игнорируются;
такие поля помечены в комментариях. Для рабочей настройки скопируйте только нужный
блок и прочитайте его документ. В каждом комментарии — значение по умолчанию и допустимые значения.

```jsonc
{
  // ─────────────────────────────────────────────────────────────────────────
  // Корневой блок `lx` (§13) — глобальные ручки форка по подсистемам.
  // lx.wg.idle действует только с build-тегом with_lx_idle_suspend (мобильный AAR);
  // десктоп-бинарь отвергает его на старте. Остальное в lx.wg работает в любой сборке.
  // ─────────────────────────────────────────────────────────────────────────
  "lx": {
    "wg": {
      "idle": {                                 // только сборки с with_lx_idle_suspend
        "suspend": "30s",                       // дефолт: выкл. Endpoint вне дерева маршрутизации → Down после простоя
        "suspend_reachable": "5m",              // дефолт: выкл. То же для endpoint'ов в дереве; >= suspend, требует его
        "teardown": "10m",                      // дефолт: = suspend_reachable. Сон до полной разборки; "0" = никогда
        "lazy_build": true,                     // дефолт: false. Сборка устройства при первом дайле (SPEC 097)
        "build_max": 4,                         // дефолт: 0 (без потолка). Не больше N собранных устройств (SPEC 097)
        "build_overflow": "wait"                // дефолт: wait. wait | build (SPEC 097)
      },
      "buffers": {                              // любая сборка (SPEC 158)
        "read_datagram_count": 8,               // дефолт: 128. 1–128 датаграмм на одно чтение из сокета
        "send_by_mtu": true                     // дефолт: true. false = буферы чтения из стека по 64 КБ (диагностика)
      },
      "connected_socket": true                  // дефолт: false. Подключённый сокет туннеля в режиме одного пира (SPEC 160)
    },
    "masque": {
      "idle_timeout": "5m"                      // дефолт: выкл. Глобальное окно простоя masque-узлов без своего ключа
    },
    "trusttunnel": {
      "idle_timeout": "5m"                      // дефолт: 5m. Глобальное окно простоя trusttunnel-узлов без своего ключа; выключается только "0" в узле (SPEC 133)
    },
    "detour": {                                 // дефолт: true (нет ключа = true) = clamp + все фильтры strip; false = поведение апстрима (§13, SPEC 165)
      "mtu_align": "clamp",                     // дефолт: clamp. off | fill | clamp
      "strip": {                                // дефолт: каталог. true | false | {"filters": …}
        "filters": {                            // объект заменяет каталог: не названный фильтр выключен
          "tls": { "fragment": true, "spoof": true, "kernel": true, "engine": true }
        }
      },
      "except_outbounds": []                    // дефолт: нет. Теги, которые не выравниваются и не фильтруются; неизвестный тег — ошибка загрузки
    },
    "sniff_guard": {                            // дефолт: нет блока = поведение апстрима. Защита рукопожатия от перенаправления по SNI на релее или роутере (§13, SPEC 060)
      "nodes": {
        "sni_mismatch": true,                   // дефолт: true — только узлы, у которых SNI не совпадает с server. false — фильтра нет
        "outbound_type": ["vless", "masque"]    // дефолт: фильтра нет. Список типов или false; true, [] и неизвестный тип — ошибка загрузки
      },
      "detour_type": {                          // дефолт: {"direct": "clean", "ssh": "clean", "socks": "clean", "*": "suspect"}
        "direct": "clean",                      //   тип носителя → never | clean | suspect; "*" — остальные типы; группы ключами нельзя
        "*": "suspect"
      },
      "override": false,                        // дефолт: false. true — блок сильнее узловых fragment / record_fragment
      "tls": {
        "method": "record_fragment",            // дефолт: off. record_fragment | fragment | delay | off
        "delay": "300ms",                       // дефолт: 300ms. Пауза метода delay; > 0
        "persist": true,                        // дефолт: false. Лейблы релеев в experimental.cache_file
        "detour_type": { "ssh": "suspect" }     // дефолт: общая карта. Перекрытие для TLS
      },
      "quic": {
        "method": "decoy_first_byte",           // дефолт: off. decoy_first_byte | delay | off
        "delay": "300ms",                       // дефолт: 300ms
        "persist": true,                        // дефолт: false
        "detour_type": { "socks": "never" }     // дефолт: общая карта. Перекрытие для QUIC
      }
    }
  },

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
        "reality": {
          "enabled": true, "public_key": "<reality-public-key-base64>", "short_id": "0123abcd",
          "key_share": ""                       // дефолт: "" (как несёт отпечаток). §7:
                                                //   "classical" = вырезать X25519MLKEM768 (только Xray < v26.9.8)
                                                //   "hybrid"    = требовать его (ошибка на edge/ios/…)
        }
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
        "session_table": "",                    // по умолчанию: не задан (id = дашед-UUID). Имя алфавита или свой ASCII-набор
        "session_length": "",                   // по умолчанию: не задан. "min-max" или "n"; задаётся вместе с session_table

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

      // ── AmneziaWG 3.x (контейнер amnezia-awg2, protocol_version 3.x; SPEC 080) ──
      // header_protection_key — СЕРВЕРНЫЙ (обязан совпасть с сервером); при нём
      // каждый s1..s4 должен быть >= 12 (в паддинге лежит nonce шифра заголовка).
      "header_protection_key": "<base64-32-bytes>", // по умолчанию: "" (выкл). Вывод `awg genkey`; маскирует тип/receiver/счётчик каждого пакета
      "content_padding_addition": "10-100",     // по умолчанию: "" (выкл). int | "min-max" доп. паддинг внутри каждого data-пакета
      "rekey_after_time": "100-120",            // по умолчанию: "" (WireGuard 120 с). int | "min-max" секунд
      "rekey_timeout": "3-7",                   // по умолчанию: "" (WireGuard 5 с)
      "reject_after_time": "150-180",           // по умолчанию: "" (WireGuard 180 с)
      "keepalive_timeout": "5-15",              // по умолчанию: "" (WireGuard 10 с)
      "max_handshake_attempts": "15-20",        // по умолчанию: "" (WireGuard 18)
      "random_trailers": true,                  // по умолчанию: false. Хвост случайной длины у каждого handshake-сообщения / внутри data-пакетов
      "disable_cookies": true,                  // по умолчанию: false. Никогда не слать и не требовать cookie reply

      "peers": [
        {
          "address": "server.example.com",
          "port": 51821,
          "public_key": "<server-public-key-base64>",
          "pre_shared_key": "<preshared-key-base64>",
          "allowed_ips": ["0.0.0.0/0", "::/0"],
          "persistent_keepalive_interval": "25-35" // число (WireGuard) или "min-max" секунд (AWG 3.x)
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
      "mode": "round_robin",                    // по умолчанию: least_test. least_test | round_robin | failover
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

Счёт полей: 26 XHTTP + 30 AmneziaWG (вкл. `id`/`ip`/`ib` и 9 ключей AWG 3.x) + 1 VLESS (`encryption`) +
5 `urltest` (`mode` + `balancer{pool,pool_tolerance,sticky_hash}`) + 26 `lx` (9 `lx.wg` + `lx.masque.idle_timeout` + `lx.trusttunnel.idle_timeout` + 3 `lx.detour{mtu_align,strip,except_outbounds}` + 12 `lx.sniff_guard{nodes{sni_mismatch,outbound_type},detour_type,override,tls{method,delay,persist,detour_type},quic{method,delay,persist,detour_type}}`).
Взаимоисключающие и игнорируемые поля помечены в комментариях выше. Семантику каждого поля,
подводные камни и статус живой проверки дают документы фич: [lx-config.ru.md](lx-config.ru.md) и ссылки из него.
