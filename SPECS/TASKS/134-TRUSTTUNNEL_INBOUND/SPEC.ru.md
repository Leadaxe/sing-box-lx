# SPEC 134 — Inbound `trusttunnel`: серверная половина протокола

> 🌐 English version: **[SPEC.md](SPEC.md)**.

**Фича:** [TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.ru.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — серверный inbound |
| Статус | N (new) — черновик на обсуждение; форма (inbound, не endpoint), двухслойная раскладка и форма файлов унаследованы из SPEC 133 и здесь не пересматриваются; §7 открыт |
| Ветка | `lx` |
| Build-tag | `with_trusttunnel` (половина HTTP/3 дополнительно требует `with_quic`) |
| Обсуждение | [Leadaxe/sing-box-lx#46](https://github.com/Leadaxe/sing-box-lx/issues/46) |
| Источник | Владелец 2026-10-15: серверная сторона пишется сразу, а не по запросу (SPEC 133 §7 В6) |
| Зависит от | [133](../133-TRUSTTUNNEL_OUTBOUND/SPEC.ru.md) — пакет-ядро протокола `transport/trusttunnel/`, файл опций, регистрация, константа; эта задача добавляет в тот же пакет серверную половину и адаптер `inbound.go` |
| Связанные | [CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) · [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md) (условие снятия) · апстримный `protocol/naive` (образец в дереве) |

## 0. TL;DR

Inbound `trusttunnel`: экземпляр sing-box принимает сессии от официальных клиентов
TrustTunnel и от outbound'а SPEC 133, проверяет их Basic auth по `users[]` и отдаёт роутеру
каждый TCP-`CONNECT` и каждый UDP-поток стрима `CONNECT _udp2`, как любой другой inbound.
HTTP/2 поверх TLS листенера всегда, HTTP/3 поверх QUIC — когда сборка с `with_quic`. Это
**inbound, а не endpoint**: протокол четвёртого уровня (`CONNECT` на каждое TCP-соединение,
UDP-кадры с адресами внутри), сервер никогда не дозванивается к своим клиентам и не
выдаёт им адресов, так что он стоит рядом с `naive` и `anytls`, а не с WireGuard или
апстримным endpoint'ом `masque-server`, которые терминируют IP-пакеты. Сервер — терминатор
туннеля, а не продукт AdGuard: правила по `client_random`, подписка, ping- и
speedtest-хосты, reverse proxy, ICMP и SOCKS5-форвардинг остаются у официального
endpoint'а.

## 1. Проблема и критерии §3.1

- **(а1) Нужна.** Решение владельца 2026-10-15. Два применения: самостоятельно поднятый
  узел, к которому официальные клиенты AdGuard (все платформы) подключаются при одном
  sing-box на сервере, и второй конец для сквозной проверки outbound'а SPEC 133 на
  собственном стенде без Rust-endpoint'а.
- **(а2) Апстрим не даёт.** Inbound `trusttunnel` нет ни в `upstream/stable`, ни в
  `testing`, ни в `unstable` (проверено 2026-10-15).
- **(а3) Дешевле, чем альтернатива.** Альтернатива — официальный `trusttunnel_endpoint`
  рядом с sing-box: второй демон со своим TLS, своими учётными данными и без маршрутизации
  sing-box. Цена у нас — ноль дополнительных строк в апстримных файлах сверх уже
  посчитанных в SPEC 133 §5: блок регистрации для реестра inbound'ов и общая константа
  входят в тот счёт.
- **(б) Изоляция** — §5.
- **(в) Spec Kit** — сначала фича [018](../../FEATURES/018-TRUSTTUNNEL/FEATURE.ru.md), затем
  133 для клиента и эта задача для сервера.

## 2. Факты протокола, которые сервер обязан соблюдать (по `PROTOCOL.md` v1.0, коду endpoint'а и официальному клиенту)

SPEC 133 §2 — справочник по проводу; здесь только то, что добавляет серверная сторона.

### 2.1 Что шлёт официальный клиент

- TLS с SNI; ALPN `h2` на HTTP/2, `h3` на HTTP/3. Клиент рвёт сессию, если согласовано
  что-то кроме ожидаемого ALPN.
- `CONNECT host:port` с `user-agent: <платформа> <приложение>` и
  `proxy-authorization: Basic …` на каждом запросе; `CONNECT _udp2`, `CONNECT _check`,
  `CONNECT _icmp` с теми же заголовками (user agent `<платформа> _udp2` / `<платформа>` /
  `<платформа> _icmp`).
- HTTP/2 SETTINGS: начальное окно потока 131072; клиент ожидает, что сервер примет до
  `max_concurrent_streams` потоков (официальный дефолт 1000).
- При `upstream_protocol: auto` клиент гоняет HTTP/3 против HTTP/2 с задержкой 1 с и
  оставляет ту сессию, что открылась первой; сервер, дающий только один из них, — норма.
- HTTP/3-сессию клиент держит живой ACK-eliciting-пакетами каждые 30 с; на HTTP/2 он шлёт
  `CONNECT _check` по событиям туннеля с троттлингом раз в 30 с.

### 2.2 Что отвечает официальный endpoint

| Запрос | Ответ |
|--------|-------|
| `CONNECT host:port`, auth ok, соединение наружу установлено | 200, дальше поток — сырой канал; **200 уходит после успешного соединения наружу** |
| `CONNECT host:port`, соединение наружу не удалось | 502 + `X-Warning: 300/301/302/310/311 - …`, плюс `X-Adguard-Vpn-Error: <host>` для 310/311 |
| `CONNECT` без порта в authority | ошибка запроса |
| auth не прошёл (любой `CONNECT`) | 407 + `proxy-authenticate: Basic realm=Authorization Required`; `auth_failure_status_code` может сделать из него 403/404/405 |
| `CONNECT _udp2`, auth ok | 200 сразу, дальше кадры в обе стороны; потоки ведутся внутри стрима по 4-кортежу, idle 300 с по дефолту конфига (120 с в спецификации) |
| `CONNECT _check`, auth ok | 200 сразу, поток закрыт |
| не-`CONNECT` запрос на основном хосте | канал туннеля, если не совпал путь ping/speedtest/подписки/reverse proxy; отказ кодом `non_connect_auth_failure_status_code` |
| сессия молчит `client_listener_timeout_secs` (600) | сессия закрывается (HTTP/2: закрытие соединения; HTTP/3: idle-таймаут QUIC) |

Значение `proxy-authorization` endpoint сравнивает как строку со стандартным base64 от
`user:pass`; любая другая кодировка тех же данных — отказ.

### 2.3 Что роутер sing-box даёт inbound'у

`RouteConnectionEx(ctx, conn, metadata, onClose)` дозванивается до outbound'а и качает
данные; обратного вызова «дайл удался» inbound не получает. Зато он получает два сигнала,
пригодных как один: `onClose(err)` вызывается, когда роутер сдаётся (ошибка дайла в том
числе), а первый `Read` роутера из переданного `conn` происходит только после успешного
дайла, когда запускается цикл копирования (или раньше — когда на этом inbound включён
sniff и роутер читает, чтобы снифить). Апстримный inbound `naive` это игнорирует и отвечает
200 до маршрутизации.

## 3. Контракт конфига

```json
{
  "type": "trusttunnel",
  "tag": "tt-in",
  "listen": "::",
  "listen_port": 443,
  "users": [
    { "username": "user", "password": "secret" }
  ],
  "network": "",
  "quic_congestion_control": "bbr",
  "tls": {
    "enabled": true,
    "server_name": "vpn.example.com",
    "certificate_path": "/etc/sing-box/fullchain.pem",
    "key_path": "/etc/sing-box/key.pem"
  }
}
```

| Ключ | Тип | Дефолт | Валидация |
|------|-----|--------|-----------|
| Listen Fields | `ListenOptions` | — | как у всех (`listen`, `listen_port`, `detour`, `sniff`, …) |
| `users` | `[]auth.User` (`username`, `password`) | обязателен | пустой список — ошибка старта; пустой `username` или `password` в записи — ошибка старта |
| `network` | `NetworkList` (`tcp`/`udp`) | оба | **какие листенеры открывать**, как у апстримного inbound `naive`: `tcp` — HTTP/2 поверх TLS на TCP-листенере, `udp` — HTTP/3 поверх QUIC на UDP-листенере. `udp` без `with_quic` — ошибка старта, если он единственный, и предупреждение, если перечислены оба. Туннелируемые сети — всегда обе; что клиенту доступно, решают route-правила (В1) |
| `quic_congestion_control` | `string` enum `bbr,cubic,reno` | `bbr` | неизвестное — ошибка старта; без `udp` игнорируется |
| `tls` | `InboundTLSOptions` | обязателен, `enabled: true` | `enabled: false` — `C.ErrTLSRequired` (В3 про h2c); ALPN задаёт inbound: `h2` на TCP, `h3` на UDP, пользовательские значения добавляются впереди, но не заменяют их |

Схема совпадает с husi `TrustTunnelInboundOptions` (`ListenOptions`, `Users`,
`QUICCongestionControl`, `Network`, контейнер TLS), кроме `icmp` в `network` — здесь это
ошибка старта. `TrustTunnelInboundOptions` живёт в том же `option/trusttunnel.go`, что и
опции outbound'а (SPEC 133 §5).

## 4. Требования

- **R1. Листенеры.** `Start(StartStateStart)`: TLS-конфиг стартует и попадает в scope; при
  `tcp` — `http.Server` поверх TLS-листенера с включённым HTTP/2 и выключенным HTTP/1.1,
  ALPN `h2`; при `udp` — HTTP/3-сервер поверх UDP-листенера через хук, который выставляет
  файл пакета с `//go:build with_quic` (как `naive` делает с `ConfigureHTTP3ListenerFunc`, но
  внутри `protocol/trusttunnel/` — `include/quic.go` не трогается). Лимиты сервера повторяют
  официальный endpoint: `MaxConcurrentStreams` 1000 на HTTP/2 (дефолт Go — 250, и R11
  outbound'а ждал бы на четверти ожидаемой глубины), idle-таймаут сессии 600 с на обоих
  транспортах, QUIC `MaxIncomingStreams` 4096, 0-RTT выключен.
- **R2. Аутентификация.** Каждый запрос обязан нести `proxy-authorization: Basic`; учётные
  данные разбираются и сверяются с `users[]` (сравнение разобранных значений, так что
  неканоничный base64 верных данных принимается там, где endpoint отказал бы; это мягкая
  сторона и вреда не несёт). Отказ: 407 с `proxy-authenticate: Basic realm=Authorization
  Required` и закрытие потока; в лог — адрес источника, без учётных данных. Имя
  пользователя — в `metadata.User`.
- **R3. TCP.** `CONNECT` с `:authority` `host:port` (IPv6 `[addr]:port`, домен передаётся
  роутеру доменом, без резолва) становится соединением для `RouteConnectionEx` с
  `Source` = удалённый адрес сессии, `Destination` = authority, `OriginDestination` = адрес
  листенера. Authority без порта — 400 и закрытие потока. H2-поток оборачивается так же,
  как `naive` оборачивает свой `naiveH2Conn` (тело как reader, response writer с flush как
  writer, close ждёт `onClose` роутера).
- **R4. 200 откладывается до результата дайла (предложение, В2).** Обработчик отвечает
  **200 на первом `Read` роутера** из переданного соединения и **502 + `X-Warning`**, если
  раньше сработал `onClose(err)`: `300 - Connection failed for some reason` как общий код и
  `302` для ошибки дедлайна, чтобы официальный клиент и наш outbound (SPEC 133 R1) видели
  проваленный дайл так же, как видят его от официального endpoint'а. При `sniff` на этом
  inbound роутер читает до дайла: 200 уходит рано, а поздний провал дайла — просто закрытие
  потока, как на `naive`. Альтернатива — 200 сразу, как у `naive`; тогда мёртвый адресат
  невидим клиенту до закрытия потока.
- **R5. UDP.** На `CONNECT _udp2` — 200 сразу. Поток читается кадр за кадром (§2.4 SPEC 133;
  `len` ограничен, обрезанный или слишком большой кадр закрывает поток с ошибкой в логе).
  Потоки ведутся внутри стрима по 4-кортежу из кадра; новый кортеж создаёт виртуальный
  `N.PacketConn`, передаваемый в `RoutePacketConnectionEx` с `Source` = источник из кадра,
  если он ненулевой, иначе удалённый адрес сессии, `Destination` = адресат из кадра. Запись
  в этот conn кадрируется обратно с отправителем как источником и исходным источником потока
  как адресатом, пишется в стрим и флашится. Поток завершается, когда его закрывает роутер
  (действует UDP-таймаут sing-box) или когда закрывается стрим; закрытие стрима закрывает все
  его потоки. `app_name` читается, проверяется по длине и игнорируется (не логируется).
- **R6. `_check`.** При верном auth: 200 сразу и закрытие потока. Без маршрутизации, без
  строки в логе выше debug.
- **R7. Не обслуживается.** `CONNECT _icmp`: 404 и закрытие потока (официальный клиент с
  включённым ICMP обязан продолжать работать по TCP/UDP — проверяется на стенде, §6). Любой
  не-`CONNECT` запрос: 404 с пустым телом, без заголовка `proxy-authenticate`, чтобы GET из
  браузера или сканера видел веб-сервер, на котором ничего нет, а не прокси (В4 — идти ли
  дальше).
- **R8. Жизненный цикл.** Конструктор только проверяет; листенеры открываются в `Start`;
  `Close` останавливает оба сервера с ограниченным grace-периодом и закрывает все потоки
  и UDP-потоки. Без таймеров, пока нет ни одной сессии.
- **R9. API пакета-ядра.** Серверная половина живёт в `transport/trusttunnel/` рядом с
  клиентом (SPEC 133 §5): `Service` с обработчиками TCP-соединений и UDP-потоков (каждый
  несёт имя пользователя), таблица пользователей, заменяемая на лету,
  `Start(tcpListener, packetConn, tlsServerConfig)` / `Close()`, H3-часть под `with_quic`.
  Без импортов sing-box: логирование через `sing/common/logger`, TLS через интерфейсы
  `sing/common/tls`, пользователи через `sing/common/auth`.
  `protocol/trusttunnel/inbound.go` отображает на него опции, листенер, метаданные и
  роутер.
- **R10. Без тега.** Заглушка из SPEC 133 R8 покрывает и тип inbound.
- **R11. Документация.** Раздел про inbound рядом с outbound в
  `docs-lx/protocols-transports.md` (SPEC 133 R12), с конфигом выше и тремя ограничениями,
  которые должен знать оператор сервера: нет правил `client_random`, нет подписки,
  нетуннельные пути отвечают 404.

## 5. Изоляция и цена мержа

Всё внутри раскладки, решённой в SPEC 133 §5; эта задача добавляет файлы, а не апстримные
строки:

| Что | Где | Апстримный файл? |
|-----|-----|------------------|
| Серверная половина ядра — `Service`, обработка запросов, таблица потоков `_udp2`, ответы 407/502 | `transport/trusttunnel/service.go`, `service_udp.go`; `service_quic.go` под `with_quic` | нет |
| Адаптер sing-box | `protocol/trusttunnel/inbound.go`; `inbound_quic.go` (`//go:build with_quic`) выставляет H3-хук | нет |
| Опции | `TrustTunnelInboundOptions` в `option/trusttunnel.go` (общий со 133) | нет |
| Регистрация / заглушка / константа | `include/trusttunnel.go`, `include/trusttunnel_stub.go`, маркированные блоки в `include/registry.go` и `constant/proxy.go` — **уже посчитаны в SPEC 133 §5** | — |
| Тесты | `*_test.go` рядом с кодом; стенд `lx-test/trusttunnel/` общий со 133 | нет |

`include/quic.go`, `include/quic_stub.go` и `protocol/naive/*` не трогаются; код `naive` —
образец, а не зависимость.

## 6. Критерии приёмки

1. Сборка без `with_trusttunnel`, с ним и без `with_quic` (только H2), с обоими; AAR без
   тега; `go vet` под всеми наборами.
2. Юниты пакета-ядра, без sing-box: клиент SPEC 133 против этого `Service` in-process поверх
   loopback-TLS-листенера — TCP туда-обратно, UDP-кадры в обе стороны (потоки IPv4 и IPv6 на
   одном стриме, два потока к одному адресату), неверный пароль → 407, который клиент
   видит как ошибку дайла, `_check` → 200, обрезанный кадр закрывает стрим, 1000
   одновременных потоков принимаются.
3. Стенд `lx-test/trusttunnel/`: этот inbound на самоподписанном сертификате; через него —
   **официальный клиент** (CLI-сборка `TrustTunnelClient`, `tt://`-конфиг на стенд,
   `certificate` запинен) и **наш outbound** (SPEC 133): HTTP(S) по TCP, DNS по UDP, большой
   UDP-кадр; HTTP/2 и HTTP/3; официальный клиент с `upstream_protocol: auto` против
   inbound'а только с H2.
4. R4 на стенде: `CONNECT` на закрытый порт через наш outbound — ошибка из `DialContext` с
   `X-Warning 300`, а не мёртвый поток; то же с включённым `sniff` деградирует до закрытия
   потока, и ничего не виснет.
5. R7 на стенде: `curl https://host/` получает 404 без `proxy-authenticate`; официальный
   клиент с включённым ICMP продолжает работать по TCP/UDP после отказа на `_icmp`.
6. Простой: сессию без запросов 600 с inbound закрывает; наш outbound прозрачно
   пересобирает её на следующем dial (SPEC 133 §6 п. 6, теперь против нашего же сервера).
7. Route-правила на inbound (`inbound: tt-in` + `ip_is_private` → `block`) не пускают клиента
   в приватную сеть сервера — это sing-box-путь к `allow_private_network_connections = false`
   endpoint'а.

## 7. Открытые вопросы (на обсуждение)

- **В1. Семантика `network`.** Предложение — как у апстримного inbound `naive`: какие
  листенеры открывать (`tcp` = HTTP/2, `udp` = HTTP/3), туннелируемые сети всегда обе и
  ограничиваются route-правилами. Это противоположно смыслу `network` на outbound'е SPEC 133
  (туннелируемые сети), но такова сложившаяся конвенция ближайшего апстримного inbound'а, и
  опция husi несёт то же поле. Альтернатива — отдельный список `transport`, а `network` — для
  туннелируемых сетей.
- **В2. Отложенный 200 (R4).** Предложение — откладывать до первого чтения роутера /
  `onClose`, с документированной деградацией при `sniff`. Альтернатива — 200 сразу, как у
  `naive`. Отложенная форма — то, что делает проваленный дайл видимым клиенту и сохраняет
  отчёт об ошибках официального клиента; цена — одна обёртка над H2/H3-потоком.
- **В3. Чистый HTTP/2 (h2c) за TLS-терминирующим прокси.** Протокол требует TLS, но
  оператор с nginx/caddy впереди может хотеть `tls.enabled: false` на TCP-листенере с h2c,
  который обработчик `naive` уже умеет. Предложение — не в этой задаче; оставить
  `C.ErrTLSRequired` и открыть по запросу.
- **В4. Поза против активного зондирования.** Официальный endpoint даёт оператору отвечать
  403/404/405 вместо 407 и отдавать настоящее содержимое на нетуннельных путях (reverse
  proxy, ping). Предложение — 407 на плохой auth в `CONNECT`, 404 на всё остальное (R2, R7),
  без ключа конфига в этой задаче; детур в стиле `fallback` на настоящий веб-сервер (как у
  inbound'ов `trojan`/`vless`) — отдельный вопрос, когда кто-то попросит.
- **В5. Нулевой источник в UDP-кадрах.** Наш outbound и mihomo шлют нулевой источник,
  официальный клиент — настоящий. Предложение — принимать оба, вести потоки по полному
  кортежу и подставлять удалённый адрес сессии в `metadata.Source`, когда источник в кадре
  нулевой (R5), чтобы route-правила по `source_ip` продолжали означать клиента.

## 8. Условие снятия

Апстрим sing-box добавляет inbound `trusttunnel` в той поверхности, которую мы собираем, —
снимается вместе с outbound'ом (SPEC 133 §8).

## 9. Источники

- `TrustTunnel/TrustTunnel` (коммит `48f424c`): `PROTOCOL.md` §5–§9, `CONFIGURATION.md`
  (core settings, `auth_failure_status_code`, `client_listener_timeout_secs`,
  `max_concurrent_streams`, `allow_private_network_connections`), endpoint
  `lib/src/http_downstream.rs` (маршрутизация запросов, `fail_request`, отображение
  `X-Warning`, `proxy-authenticate`), `lib/src/http_demultiplexer.rs` (каналы по путям),
  `lib/src/udp_forwarder.rs` (таблица потоков), `lib/src/authentication/registry_based.rs`.
- `TrustTunnel/TrustTunnelClient` (коммит `64e2e5b`): `core/src/utils.cpp` (заголовки
  запросов), `core/src/fallbackable_upstream_connector.cpp` (гонка H3/H2),
  `core/src/http3_upstream.cpp` (keepalive в простое).
- Образец в дереве: `protocol/naive/inbound.go`, `protocol/naive/quic/inbound_init.go`
  (H3-хук, выбор congestion control); `protocol/hysteria2/inbound.go` и
  `protocol/tuic/inbound.go` (`RoutePacketConnectionEx` с packet conn на поток).
- Сторонние: `xchacha20-poly1305/sing-trusttunnel` v0.3.2 `service.go` (отвечает 403 на
  плохой auth, 405 на не-`CONNECT`; ориентир по форме серверного API, не по проводу).
