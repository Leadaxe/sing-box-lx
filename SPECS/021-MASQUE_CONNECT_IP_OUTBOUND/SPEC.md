# SPEC 021 — MASQUE outbound (CONNECT-IP over HTTP/3 + HTTP/2), профиль Cloudflare WARP

**Статус:** h3 И h2 РАБОТАЮТ на живом Cloudflare WARP (device-verified 2026-07-02, warp=on
на обоих). Пройден аудит-проход (24 находки): исправлены баги корректности (reconnect,
blackhole на битом пакете, ICMP-снимок, h2-потолки), добавлен idle-suspend + самовосстановление
туннеля (stateless idle), убраны per-packet аллокации в горячем пути. Все три lifecycle-режима
(dial / idle-suspend / reconnect) device-verified на h3 и h2. См. TEST_PLAN.md §RESULTS.
**Ветка:** `lx-spec021-masque`
**Тег типа:** `masque` (`C.TypeMASQUE`)
**Build-tag:** `with_quic` + `with_gvisor` (userspace-стек)

---

## Зачем

Нужен outbound, умеющий подключаться к **Cloudflare WARP по MASQUE**. MASQUE у Cloudflare — это
**CONNECT-IP (RFC 9484)**, а НЕ CONNECT-UDP (RFC 9298): туннелируются целые IP-пакеты, не UDP-датаграммы.
Сервер отдаёт нам поток IP-пакетов, поверх которого мы поднимаем userspace-стек (как WireGuard-endpoint)
и раздаём приложениям через `DialContext`/`ListenPacket`.

Референс — реализация mihomo (`transport/masque`, `adapter/outbound/masque.go`, ветка `Alpha`), которая
в свою очередь порт от `Diniboy1123/usque` + `connect-ip-go`. **Портируем логику**, адаптируя импорты на
наш форк (`sagernet/quic-go/http3`, наш `transport/wireguard` gVisor-стек), НЕ тянем чужой WARP-заточенный
`connect-ip-go` в go.mod — вкапываем его в `transport/masque/` как lx-owned код.

Это отвечает на давний запрос апстрима — [SagerNet/sing-box#4000](https://github.com/SagerNet/sing-box/issues/4000)
(«MASQUE по образцу WireGuard endpoint»).

## Не путать

- НЕ RFC 9298 (CONNECT-UDP). Здесь **CONNECT-IP** (RFC 9484), IP-пакеты.
- `transport/wireguard/masque_awg.go` — ЭТО ДРУГОЕ (SPEC 009, AWG-обфускация I1 «masquerade»).
  Случайное совпадение имени. К этому спеку отношения не имеет.

---

## Дизайн-решения (закрыты с заказчиком)

1. **Один outbound `type: masque`**, режим переключается полем `profile`, а не отдельными протоколами
   (паттерн `tuic.udp_relay_mode`). Значения: `cloudflare` (дефолт, цель) | `standard` (чистый RFC 9484, задел).
2. **Транспорты в v1: `h3` И `h2` сразу.** `h3` = CONNECT-IP over QUIC (основной путь WARP);
   `h2` = CONNECT-IP over HTTP/2 (для сетей, где режут QUIC/UDP). `h3-l4proxy` — НЕ в scope v1 (см. §Не в scope).
3. **`connect-ip-go` вкопировать** в `transport/masque/` (lx-owned, ~500 строк), не внешней зависимостью.
4. **Регистрация в WARP — вне ядра.** Клиент ПРИНИМАЕТ готовый ключевой материал из конфига
   (`private_key`/`public_key`/`ip`/`ipv6`). Регистрацию устройства (генерация ECDSA, WARP API) делает
   вызывающая сторона (Dart/LxBox). См. §Ключевой материал.
5. **userspace-стек — переиспользуем наш `transport/wireguard.stackDevice`** (gVisor), НЕ тянем
   `sing-wireguard` как mihomo. `stackDevice` уже даёт ровно нужный контракт (см. §Стек).

---

## Конфиг

```jsonc
{
  "type": "masque",
  "tag": "warp",

  "server": "162.159.198.1",       // WARP endpoint IP (или host)
  "server_port": 443,

  "profile": "cloudflare",         // "cloudflare" (дефолт) | "standard"
  "network": "h3",                 // "h3" (дефолт) | "h2"

  // --- ключевой материал (обязателен для cloudflare) ---
  "private_key": "<base64>",       // x509 EC private key (DER), ParseECPrivateKey
  "public_key":  "<base64>",       // PKIX public key (DER) endpoint'а, ParsePKIXPublicKey → *ecdsa.PublicKey
  "ip":   "172.16.0.2/32",         // локальный IPv4 внутри туннеля (/32 если без маски)
  "ipv6": "2606:4700:110:...::/128",

  // --- опционально ---
  "uri": "https://cloudflareaccess.com",   // CONNECT-IP URI-шаблон; дефолт зависит от profile
  "sni": "",                        // дефолт: consumer-masque.cloudflareclient.com (cloudflare)
  "mtu": 1280,                      // дефолт 1280 (на h2 макс. 16000)
  "skip_cert_verify": false,        // отключить pinning на public_key (debug)
  "idle_timeout": "5m",             // suspend туннеля после простоя; "" = 5m, отрицательное = выкл
  "keep_alive_period": "30s",       // QUIC keepalive (h3); "" = 30s, отрицательное = выкл

  // стандартные:
  "network_list": ["tcp","udp"],    // L4-протоколы (НЕ транспорт — это `network`)
  ...DialerOptions
}
```

> ⚠️ **`network` ≠ tcp/udp.** У masque-outbound `network` выбирает **ТРАНСПОРТ** (`h3`/`h2`),
> а список L4-протоколов — это `network_list`. Это обратно всем остальным outbound'ам,
> где `network` = `["tcp","udp"]`. Ошибочный `"network": "tcp"` → fail-fast «invalid network».

### Поля

| Поле | Тип | Дефолт | Профиль | Смысл |
|---|---|---|---|---|
| `profile` | string | `cloudflare` | — | набор поведения (см. §Профили) |
| `network` | string | `h3` | оба | **транспорт**: `h3` = QUIC, `h2` = HTTP/2 |
| `private_key` | string(b64) | — | cloudflare (обяз.) | EC private key, DER → `x509.ParseECPrivateKey` |
| `public_key` | string(b64) | — | cloudflare (обяз.) | endpoint PKIX pubkey, `x509.ParsePKIXPublicKey` |
| `ip` | string(CIDR) | — | обяз. (хотя бы один из ip/ipv6) | локальный IPv4 туннеля |
| `ipv6` | string(CIDR) | — | обяз. (хотя бы один) | локальный IPv6 туннеля |
| `uri` | string | по профилю | оба | CONNECT-IP URI-шаблон |
| `sni` | string | по профилю | оба | TLS SNI (для WARP != endpoint host) |
| `mtu` | int | 1280 | оба | MTU userspace-стека (h2: ≤ 16000) |
| `skip_cert_verify` | bool | false | cloudflare | отключить pubkey-pinning |
| `idle_timeout` | duration | `5m` | оба | suspend туннеля после простоя; отриц. = выкл |
| `keep_alive_period` | duration | `30s` | h3 | QUIC keepalive; отриц. = выкл |
| `network_list` | list | оба tcp+udp | оба | L4-протоколы через туннель |

**Валидация fail-fast при старте:**
- `profile == cloudflare`: `private_key`, `public_key` обязательны и парсятся (иначе ошибка конфига).
- Хотя бы одно из `ip`/`ipv6` задано и парсится в CIDR.
- `network ∈ {h3, h2}`.
- `network == h2` → `mtu ≤ 16000` (один IP-пакет = один HTTP/2 DATA-фрейм).
- `network == h2` + `profile == standard` → пока ошибка «not implemented» (h2 в v1 только для cloudflare,
  т.к. capsule-over-h2 завязан на cf-поведение; RFC-h2 — задел).

---

## Профили (что переключает `profile`)

Профиль влияет ТОЛЬКО на 4 точки; всё остальное (QUIC/http3, capsule/datagram, стек, насосы) — общее.

| Точка | `cloudflare` | `standard` (RFC 9484) |
|---|---|---|
| `:protocol` (Extended CONNECT) | `cf-connect-ip` | `connect-ip` |
| Extended CONNECT settings | **игнорировать** отсутствие (`ignoreExtendedConnect=true`) — WARP их не шлёт (нарушает RFC) | требовать `EnableExtendedConnect` |
| Доп. заголовки | h3: `User-Agent: ""`; h2: `cf-connect-proto: cf-connect-ip`, `pq-enabled: false` | нет |
| TLS-проверка | pinning на `public_key` (ECDSA equal), `InsecureSkipVerify` + `VerifyConnection` | обычная цепочка по `sni` |
| дефолт `uri` | `https://cloudflareaccess.com` | нет дефолта (обязателен) |
| дефолт `sni` | `consumer-masque.cloudflareclient.com` | = server host |

В коде — одна структура `profile` с полями/функциями, выбирается в `NewOutbound` по `options.Profile`.

---

## Файлы и точки интеграции (как реализовано)

```
constant/proxy.go        + TypeMASQUE = "masque" (рядом с TypeTUIC) + в displayName switch
option/masque.go         type MASQUEOutboundOptions (DialerOptions + ServerOptions + поля §Конфиг)
include/quic.go          masque.RegisterOutbound в registerQUICOutbounds (with_quic) + импорт
include/quic_stub.go     заглушка ErrQUICNotIncluded (без with_quic)

protocol/masque/
  outbound.go            adapter.Outbound: NewOutbound, ensureSession/teardownSession/idleWatcher,
                         pumpToTunnel/pumpFromTunnel, DialContext/ListenPacket/Close, resolve, RegisterOutbound
  outbound_test.go       parsePrefixes, EC-ключи
  lifecycle_test.go      generation-guard, идемпотентность teardown, close-guard

transport/masque/
  masque.go              ConnectTunnelH3 + IpConn интерфейс + dialCONNECTIP
  request.go             buildConnectIPRequest + advertiseDefaultRoute
  client_h2.go           ручной h2-фреймер: h2RawConn, h2IpConn, sendConnect, readLoop, receiveDatagram
  profile.go             cloudflare/standard, PrepareTLSConfig (pubkey-pinning), generateClientCert
  profile_test.go        профиль-матрица, TLS-pinning
  client_h2_test.go      capsule-reassembly через границы DATA-фреймов
  connectip/             вкопанный connect-ip-go (client subset) на sagernet/quic-go:
    connectip.go         Conn (ReadPacket/WritePacket/AdvertiseRoute), горутины readFromStream/writeToStream
    capsule.go           address/route capsules; iprange.go; checksum.go; icmp.go; *_test.go
```

Отличия от первоначального плана: НЕТ `client_h3.go` (h3 в `masque.go`), НЕТ `device.go`
(переиспользуем `wireguard.NewDevice(System:false)` напрямую), НЕТ `const.go` (константы в profile.go),
connectip — подпакет. h2 — ручной Framer (не http.Transport/h2c).

**Фабрика/registry не трогаем** — регистрация через `outbound.Register[MASQUEOutboundOptions]`.

### adapter.Outbound (что реализуем)

`adapter/outbound.go:16` — интерфейс минимален: `Type/Tag/Network/Dependencies` + `N.Dialer`.
Реально пишем:
- `DialContext(ctx, network, dest) (net.Conn, error)` — TCP через userspace-стек
- `ListenPacket(ctx, dest) (net.PacketConn, error)` — UDP через userspace-стек
- `Network() []string` — из `network_list` (tcp/udp)
- `Close()` — рвёт туннель, гасит стек
- lazy `run(ctx)` — поднимает туннель+стек+насосы при первом дайле (как mihomo `run()` с sync.Once-паттерном)

---

## Стек (userspace gVisor) — переиспользование

Наш `transport/wireguard/device_stack.go` `stackDevice` (build `with_gvisor`) уже даёт ровно нужный контракт:
- `newStackDevice(DeviceOptions{Address: prefixes, MTU})` — gVisor-стек с локальными IP
- `Read(bufs, sizes, offset)` — вынимает исходящий IP-пакет (приложение → сеть) ⇒ пихаем в туннель
- `Write(bufs, offset)` — вкидывает входящий IP-пакет (сеть → приложение)
- `DialContext` / `ListenPacket` — наружу для adapter.Outbound
- `Inet4Address()` / `Inet6Address()` — локальные IP

WG-специфика (`SetDevice`, `Events`, wg `bind`) для MASQUE НЕ нужна — просто не вызываем.
**Решение по переиспользованию (выбрать при имплементации):**
- (A) вызвать `newStackDevice` напрямую из `protocol/masque` — но это внутренний символ пакета `wireguard`.
  Тогда экспортировать конструктор (`NewStackDevice`) или вынести stackDevice в общий пакет.
- (B) не трогать wireguard, сделать свой тонкий gVisor-стек в `protocol/masque/device.go` (копия ~200 строк).

Рекомендация: **(A) экспортировать `NewStackDevice`** из `transport/wireguard` (минимальный diff, один
источник gVisor-стека, меньше дублирования). Проверить, что экспорт не тянет лишних WG-зависимостей в сборку
без WG. Fallback — (B).

---

## Жизненный цикл + поток данных (общее)

Туннель — это `*session` (device + ipConn + closer + ctx/cancel + счётчик активности).
Живёт лениво и самовосстанавливается:

```
NewOutbound: разобрать ключи/prefixes/profile; quicConfig{EnableDatagrams,InitialPacketSize:1242,KeepAlive}
             idleTimeout (деф. 5m); НИЧЕГО не поднимаем.
ensureSession(ctx) (под runMu, lazy): если sess==nil →
  1. device = wireguard.NewDevice(System:false → gVisor stackDevice, prefixes, mtu); device.Start()
  2. connectH3/connectH2 → (closer, ipConn)
  3. s = &session{...}; s.markActivity(now); o.sess = s
  4. go pumpToTunnel(s), go pumpFromTunnel(s), go idleWatcher(s)  (если idleTimeout>0)
насос TX: for { device.Read → markActivity → ipConn.WritePacket [→ icmp? → device.Write] }
насос RX: for { ipConn.ReadPacket → markActivity → device.Write }
DialContext/ListenPacket: s = ensureSession(); резолв домена → s.device.DialContext/ListenPacket
idleWatcher: тик; если простой ≥ idleTimeout → teardownSession(s)
teardownSession(s) (sync.Once + generation-guard): cancel; ipConn.Close (разблокирует парный насос);
  closer.Close; device.Close; если o.sess==s → o.sess=nil (следующий dial пере-поднимет)
Close: o.closed=true; teardown текущей сессии
```

**Самовосстановление (C1):** любой выход насоса (обрыв WARP, GOAWAY, битый пакет) → `teardownSession`
→ `o.sess=nil` → следующий `DialContext` пере-собирает туннель. **Idle-suspend (B1):** после простоя
туннель сносится целиком (netstack + горутины + keepalive), поднимается заново по требованию.
**Generation-guard (C1/C2):** teardown устаревшей сессии не трогает новую; `ipConn.Close` разблокирует
парный насос, залипший в блокирующем read.

`ConnectTunnelH3`: `tr{EnableDatagrams, AdditionalSettings{0x276:1}, DisableCompression}` →
`NewClientConn(quicConn)` → `OpenRequestStream` → `SendRequestHeader`
(`:method CONNECT`, `:protocol cf-connect-ip`, `Capsule-Protocol: ?1`, `User-Agent:""`) →
`ReadResponse` (2xx) → `connectip.NewProxiedConn` → `AdvertiseRoute(0.0.0.0/0, ::/0)`.
При `ignoreExtendedConnect` НЕ падать, если `settings.EnableExtendedConnect==false`.
`0x276` = legacy SETTINGS_H3_DATAGRAM_00, которого WARP требует (см. §Риски.4).

`ConnectTunnelH2`: сами дайлим TCP+TLS(ALPN h2), затем **ручной фреймер** на `x/net/http2.Framer`+`hpack`
(НЕ высокоуровневый Client — он блокирует extended CONNECT из-за отсутствия peer-SETTINGS): preface +
свои SETTINGS + WINDOW_UPDATE → одна HEADERS-рамка **plain CONNECT** (`:method`+`:authority`+
`cf-connect-proto:cf-connect-ip`+`capsule-protocol:?1`, БЕЗ `:protocol`) → DATA-фреймы несут capsule
DATAGRAM (type 0 + varint len). `payloadLen` ограничен `maxCapsulePayload`. TTL/checksum-декремент —
клиент. Тот же `IpConn` (ReadPacket/WritePacket).

---

## Ключевой материал (контракт с Dart/LxBox)

Регистрацию WARP делает вызывающая сторона (воспроизводимо на Dart, ядро не нужно):
1. Сгенерировать **ECDSA P-256** keypair.
2. WARP registration API (`api.cloudflareclient.com`) → назначенные `ip`/`ipv6`, endpoint `public_key`.
3. Сериализовать: `private_key` = base64(DER `x509.MarshalECPrivateKey`),
   `public_key` = base64(DER `x509.MarshalPKIXPublicKey` от `*ecdsa.PublicKey`).

Ядро только парсит (`ParseECPrivateKey` / `ParsePKIXPublicKey`) и пинит. **Формат байт матчить точно** —
несовпадение DER-кодировки = ошибка парсинга при старте. Тест-вектор согласовать с Dart-стороной (§Тест-план).

---

## Совместимость / сборка

- Только под `with_quic` + `with_gvisor`. В минимальных сборках отсутствует (как tuic/hysteria).
- **LX_TAGS**: `masque` попадает в сборки, где есть `with_quic`+`with_gvisor` (desktop/CLI — да; AAR — проверить,
  gVisor обычно включён т.к. WG-endpoint его требует). Свериться с [[desktop-keeps-clash-api-aar-drops]] логикой тегов.
- go.mod: **новых внешних зависимостей нет** (connect-ip-go вкопан). Если h2 потребует http-форк — это
  единственная возможная новая зависимость, решить отдельно (см. §Риски).

---

## Тест-план

Юнит (без сети):
- `option/masque_test.go` — парсинг конфига, дефолты по профилю, fail-fast валидация (нет ключей → ошибка;
  плохой CIDR → ошибка; `network` вне множества → ошибка).
- `transport/masque` — парсинг ключей (тест-вектор DER, согласованный с Dart); capsule DATAGRAM
  encode/decode (h2 path); TTL-декремент + IPv4-checksum пересчёт (известные векторы).
- профиль-матрица: cloudflare vs standard дают правильный `:protocol` и набор заголовков.

Интеграция (live, стенд):
- реальный WARP endpoint + реальный ключевой материал (из Dart-регистрации):
  - `network: h3` — TCP (curl ifconfig.me через outbound → видим WARP-IP) + UDP (DNS/QUIC через outbound).
  - `network: h2` — то же, там где h3 режется.
- reachability-матрица как в SPEC 020: dial успех, данные ходят в обе стороны, Close чистый (нет goroutine-leak).
- негатив: неверный `public_key` → login failed (ожидаемая ошибка `tls: access denied`); неверный `ip` → нет данных.

Записать РЕЗУЛЬТАТЫ в `SPECS/021-.../TEST_PLAN.md` §RESULTS с указанием endpoint/даты/сборки
(конвенция форка — стендовые замеры в спеке).

---

## Фазы

- **Ф1 (h3, cloudflare):** constant + option + профили + client_h3 + connectip + stackDevice-reuse + outbound +
  регистрация. Цель: живое TCP+UDP через WARP по QUIC. ← основная ценность.
- **Ф2 (h2):** client_h2 + capsule-over-h2 (+ решение по http-форку). Цель: WARP там, где режут QUIC.
- **Ф3 (standard/RFC, задел):** `connect-ip`/строгий Extended CONNECT/обычный TLS. НЕ вылизываем в v1.

---

## Что НЕ в scope

- **CONNECT-UDP (RFC 9298)** — здесь только CONNECT-IP. Отдельный спек, если понадобится generic UDP-прокси.
- **h3-l4proxy** (mihomo `network: h3-l4proxy`, L4 CONNECT/TCP-стрим поверх, только TCP) — фаза позже.
- **inbound / MASQUE-сервер** — только клиент-outbound.
- **регистрация устройства в WARP** — вне ядра (Dart/LxBox).
- **PQC** (`pq-enabled: true`) — cf-поле фиксируем в false.
- **IP flow forwarding** (URI-шаблон с переменными host/port) — mihomo его тоже не поддерживает (`Varnames > 0` → ошибка).

---

## Риски / открытые вопросы (итог)

1. **h2 extended-CONNECT gate — ✅ СНЯТ.** Оказалось не про h2c, а про то, что WARP не шлёт
   SETTINGS `ENABLE_CONNECT_PROTOCOL`, и высокоуровневые h2-клиенты (stdlib и `x/net/http2`)
   блокируют запрос. Решение — ручной фреймер на публичном `x/net/http2.Framer` + `hpack`
   (обе уже в go.mod). БЕЗ http-форка. Плюс WARP h2 = **plain CONNECT** + `cf-connect-proto`,
   а НЕ extended CONNECT с `:protocol`. См. TEST_PLAN §RESULTS.
2. **Экспорт `NewStackDevice` — ✅ НЕ понадобился.** `transport/wireguard.NewDevice(System:false)`
   уже экспортирован и даёт gVisor `stackDevice`. Ничего не экспортировали, WG-тесты не тронуты.
3. **DER-формат ключей — ✅ СНЯТ.** Реальные WARP-ключи из ClashWARP-конфига (base64 DER
   `ParseECPrivateKey`/`ParsePKIXPublicKey`) парсятся без изменений; формат Dart = наш формат.
4. **Cloudflare non-RFC quirks могут дрейфовать** — открыто. Привязка к поведению mihomo Alpha
   (референс). Зафиксированные quirks: `cf-connect-ip`, игнор Extended-CONNECT settings, h3-legacy
   SETTINGS `0x276`, h2 plain-CONNECT + `cf-connect-proto`, pubkey-pinning вместо цепочки.

## Результаты реализации (что реально легло)

Файлы:
- `transport/masque/connectip/` — вкопанный connect-ip-go (connectip.go, capsule.go, iprange.go,
  checksum.go, icmp.go) на `sagernet/quic-go`. Порт = смена импортов + `min` builtin (выкинут minmax.go),
  без httpsfv/uritemplate (URI парсим `net/url`, capsule-header хардкод `?1`). Отброшены proxy/request/
  client (серверная сторона + RFC-strict Dial). Компилируется без build-тегов.
- `transport/masque/` — `profile.go` (cloudflare/standard + PrepareTLSConfig pinning + генерация client
  cert), `masque.go` (ConnectTunnelH3 + IpConn), `request.go` (Extended CONNECT + advertiseDefaultRoute),
  `client_h2.go` (capsule DATAGRAM поверх stdlib net/http CONNECT-pipe).
- `protocol/masque/outbound.go` — adapter.Outbound: lazy run(), два насоса, DialContext/ListenPacket/Close.
- `option/masque.go`, `constant/proxy.go` (TypeMASQUE + displayName), `include/quic.go` (+quic_stub.go).

Отклонения от плана спека:
- **Экспорт `NewStackDevice` НЕ понадобился.** `transport/wireguard.NewDevice(DeviceOptions{Address,MTU})`
  уже экспортирован и при `System:false` даёт gVisor `stackDevice` с Read/Write (raw IP) + DialContext/
  ListenPacket + Start/Close. `SetDevice` — no-op для stackDevice, WG-device не нужен. Риск №2 снят.
- **h2 БЕЗ http-форка.** Риск №1 снят: stdlib Go 1.25 `net/http` (Protocols.SetHTTP2 + DialTLSContext)
  тянет обычный CONNECT со стриминговым телом-pipe; capsule DATAGRAM (type 0) framing ручной поверх
  `quicvarint`. Не используем `NewClientConn`/`Reserve` (это была причина форка у mihomo). НОВЫХ внешних
  зависимостей нет; `golang.org/x/net` (icmp/ipv4/ipv6) и `x/exp` уже в go.mod.
- Без gvisor `NewDevice(System:false)` вернёт `ErrGVisorNotIncluded` при первом дайле (graceful, не паника).

Проверки: `go build` (with_quic+with_gvisor+with_wireguard и stub-путь без quic), `go vet`, `gofmt -l`
чисто. Юнит-тесты зелёные: profile-матрица, TLS-pinning, EC-ключи round-trip, capsule round-trip
(route/address), IPv4-checksum (вектор 0xb861), parsePrefixes, decode `type:masque` через include-registry.
**НЕ проверено на живом WARP** — нужен реальный ключевой материал из Dart-регистрации (риск №3, §Тест-план).

## Референс

- mihomo `Alpha`: `transport/masque/{masque.go,client_h2.go,l4proxy.go}`, `adapter/outbound/masque.go`
  (порт от `Diniboy1123/usque` + `connect-ip-go`; изучены в разведке).
- RFC 9484 (CONNECT-IP), RFC 9298 (CONNECT-UDP, для контраста), RFC 9297 (HTTP Datagrams).
- Наш форк: `transport/wireguard/device_stack.go` (stackDevice), `sagernet/quic-go/http3`
  (OpenRequestStream/SendDatagram/capsule), `include/quic.go`.
- Апстрим-запрос: SagerNet/sing-box#4000.
