# FEATURE 018 — TRUSTTUNNEL — клиент протокола TrustTunnel (AdGuard)

| Поле | Значение |
|------|----------|
| Тип | Продуктовая фича |
| Build-tag | `with_trusttunnel` (предложение) |
| Состояние | N — черновик на обсуждение, задача [133](../../TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md); решение владельца не принято |

## Назначение

TrustTunnel — VPN-протокол AdGuard VPN, открытый в ноябре 2025 (Apache-2.0,
спецификация `PROTOCOL.md` v1.0). Снаружи это обычный HTTPS к реальному домену:
HTTP/2 поверх TLS или HTTP/3 поверх QUIC, внутри — прокси-`CONNECT` с Basic-auth.
У протокола свой сервер-endpoint, официальные клиенты на всех платформах и
растущая база самостоятельно поднятых серверов; в mihomo, husi, Shadowrocket и
Surge он уже есть, в апстриме sing-box — нет (запрос
[SagerNet/sing-box#3723](https://github.com/SagerNet/sing-box/issues/3723) открыт
с января 2026 без ответа мейнтейнеров).

Фича даёт outbound `trusttunnel`: узел TrustTunnel в конфиге ядра наравне с
остальными, TCP и UDP через него, участие в группах, `detour`, цепочках.

## Контролируемые параметры

```json
{
  "type": "trusttunnel",
  "tag": "tt-node",

  "server": "vpn.example.com",
  "server_port": 443,
  "username": "user",
  "password": "secret",
  "network": "",
  "quic": false,
  "quic_congestion_control": "bbr",
  "health_check": false,
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "utls": { "enabled": true, "fingerprint": "chrome" }
  },

  ... // Dial Fields
}
```

| Ключ | Тип | Дефолт | Смысл |
|------|-----|--------|-------|
| `server`, `server_port` | строка, число | обязательны | Адрес endpoint'а |
| `username`, `password` | строка | обязательны | Учётные данные; идут в `proxy-authorization: Basic` каждого `CONNECT` |
| `network` | `tcp` \| `udp` \| пусто | пусто = оба | Какие сети узел обслуживает |
| `quic` | bool | `false` | `false` — HTTP/2 поверх TLS/TCP, `true` — HTTP/3 поверх QUIC |
| `quic_congestion_control` | `bbr` \| `cubic` \| `reno` | `bbr` | Контроль перегрузки для QUIC; без `quic` игнорируется |
| `health_check` | bool | `false` | Периодический `CONNECT _check`, держит сессию живой |
| `tls` | блок `tls` sing-box | обязателен, `enabled: true` | Общий слой TLS: `server_name`, `certificate`, `insecure`, `utls`, `fragment`, `ech` |
| Dial Fields | — | — | Как у любого outbound: `detour`, `bind_interface`, `domain_resolver`, … |

Имена ключей совпадают с уже сложившейся sing-box-схемой TrustTunnel в husi
и в клиентах на его основе: конфиг, выгруженный из них, читается без
переименований.

Соответствие экспорта endpoint'а TrustTunnel (`trusttunnel_endpoint … -c`,
подписка, ссылка `tt://`) ключам выше:

| TrustTunnel | sing-box |
|-------------|----------|
| `hostname`, `custom_sni` | `tls.server_name` (`custom_sni`, если задан) |
| `addresses[]` | `server` / `server_port` (по узлу на адрес) |
| `username`, `password` | `username`, `password` |
| `certificate` | `tls.certificate` |
| `skip_verification` | `tls.insecure` |
| `upstream_protocol` `http2` / `http3` | `quic: false` / `true` |
| `anti_dpi` | `tls.fragment` (см. «Границы») |
| `tls_profile` клиента | `tls.utls.fingerprint` |
| `client_random_prefix`, `client_random_auth_key` | нет (см. «Границы») |
| `dns_upstreams`, `has_ipv6`, `name` | вне ядра — забота клиента-приложения |

Build: тег `with_trusttunnel`. Без него `type: trusttunnel` отвергается при
чтении конфига понятной ошибкой, остальное ядро ведёт себя как апстрим.

## Входы

- Конфиг узла.
- Соединения от роутера: TCP-потоки и UDP-сессии с адресом назначения
  (доменом или IP).
- События смены сети от монитора интерфейсов.

## Выходы

- На проводе — одно HTTP/2- или HTTP/3-соединение к endpoint'у, внутри которого
  каждое TCP-соединение — отдельный `CONNECT host:port`, каждая UDP-сессия —
  отдельный `CONNECT _udp2` с кадрированными датаграммами.
- Ошибки дайла несут ответ сервера: код статуса и предупреждение `X-Warning`
  (connect failed, host unreachable, timeout, DNS non-routable) — в тексте ошибки,
  а не голое «EOF».
- Наблюдаемость — штатная для outbound: `detourList`, URLTest, Clash API.

## Data flow

```
TCP-соединение ─► outbound trusttunnel
   │  CONNECT host:port, proxy-authorization: Basic, user-agent
   ▼
общая сессия к endpoint'у (одна на узел; поднимается по первому запросу)
   │  HTTP/2: TLS через общий слой (STD | uTLS), ALPN h2
   │  HTTP/3: QUIC v1, ALPN h3
   ▼
200 ─► поток = сырой двунаправленный байтовый канал
≠200 ─► ошибка дайла с кодом и X-Warning

UDP-сессия ─► резолв домена назначения (dial-поля узла / domain_resolver)
   ▼
CONNECT _udp2 ─► кадры: длина | src | dst | имя приложения | payload
   ▼  ответные кадры: длина | src | dst | payload

смена сети ─► сессия сбрасывается, следующие запросы поднимают новую
```

## Правила и гарантии

- **TLS обязателен.** `tls.enabled: false` — ошибка старта.
- **Учётные данные обязательны.** Пустой `username` или `password` — ошибка старта.
- **Сеть открывается на старте, а не в конструкторе.** Конструктор только
  проверяет конфиг; первое соединение к серверу — по первому запросу.
- **Ответ сервера ждётся до возврата дайла.** Неудачный `CONNECT` даёт ошибку
  из `DialContext`, а не отложенную ошибку первого чтения — иначе группы
  (`urltest`, `failover`) не видят отказ узла.
- **UDP идёт только на IP.** Протокол переносит в кадре 16-байтный адрес;
  домен назначения резолвится до записи через dial-поля узла.
- **HTTP/3 — только со стандартным TLS.** uTLS-отпечаток с `quic: true` —
  ошибка старта: QUIC-рукопожатие строится на STD-конфиге.
- **Простой не будит радио.** `health_check` по умолчанию выключен; включённый
  проверяет сессию, только пока через узел идут соединения
  ([ENERGY](../008-ENERGY/FEATURE.md)).
- **Смена сети сбрасывает сессию**, иначе она висит до idle-таймаута QUIC
  или TCP.

## Границы

- **Только клиент.** Inbound/сервер — вне фичи: у протокола есть свой
  официальный сервер, серверная сторона по CONSTITUTION §3.1 — второй приоритет.
- **ICMP не туннелируется.** Протокол умеет `CONNECT _icmp` (только echo, без
  полезной нагрузки), но в ядре это отдельный путь маршрутизации ICMP; отложено
  до запроса.
- **Нет `upstream_protocol: auto`.** Официальный клиент гоняет H2 и H3
  наперегонки; у нас выбор один на узел (`quic`), «авто» собирается группой
  `urltest` из двух узлов.
- **Нет правил `client_random`.** Сервер может пускать только ClientHello с
  заданным префиксом или ключом в `client_random`. Узлы с такими правилами
  этой фичей не обслуживаются; поддержка требует управления `client_random`
  через uTLS и будет отдельной задачей при живом запросе.
- **`anti_dpi` не отдельный ключ.** В официальном клиенте это задержка между
  первым байтом ClientHello и остатком; ближайший штатный аналог —
  `tls.fragment`.
- **Ссылки `tt://` и подписка TrustTunnel не разбираются ядром.** Импорт —
  забота лаунчера и LxBox, ядро получает готовый JSON.
- **Нет мультиплексирования сверх одной сессии.** Все потоки узла идут в одно
  HTTP/2- или HTTP/3-соединение; пула и ротации нет.

## Задачи фичи

| Задача | Что | Статус |
|--------|-----|--------|
| [133](../../TASKS/133-TRUSTTUNNEL_OUTBOUND/SPEC.md) | Outbound `trusttunnel`: TCP + UDP поверх HTTP/2 и HTTP/3 | N |

## Особенности сопровождения

- Протокол — AdGuard, спецификация в `TrustTunnel/TrustTunnel/PROTOCOL.md`;
  сверка провода — по ней и по официальному клиенту `TrustTunnel/TrustTunnelClient`,
  а не по сторонним реализациям.
- Авторизация сравнивается сервером побайтно со стандартным base64 **с паддингом**
  от `user:pass`; любое отклонение кодировки — 407 при верных данных.
- Магические `:authority` (`_udp2`, `_check`, `_icmp`) сервер сравнивает
  литерально, без порта.
