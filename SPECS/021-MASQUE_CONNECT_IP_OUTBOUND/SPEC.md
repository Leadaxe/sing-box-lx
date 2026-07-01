# SPEC 021 — MASQUE outbound (CONNECT-IP over HTTP/3 + HTTP/2), профиль Cloudflare WARP

**Статус:** РЕАЛИЗОВАН (h3+h2, cloudflare+standard), НЕ device-verified на живом WARP
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
  "mtu": 1280,                      // дефолт 1280
  "skip_cert_verify": false,        // отключить pinning на public_key (debug)

  // --- congestion (как tuic/hysteria) ---
  "congestion_control": "",         // "", "cubic", "bbr", ...
  "cwnd": 0,

  // стандартные:
  "network_list": ["tcp","udp"],    // NetworkList
  ...DialerOptions
}
```

### Поля

| Поле | Тип | Дефолт | Профиль | Смысл |
|---|---|---|---|---|
| `profile` | string | `cloudflare` | — | набор поведения (см. §Профили) |
| `network` | string | `h3` | оба | `h3` = QUIC, `h2` = HTTP/2 |
| `private_key` | string(b64) | — | cloudflare (обяз.) | EC private key, DER → `x509.ParseECPrivateKey` |
| `public_key` | string(b64) | — | cloudflare (обяз.) | endpoint PKIX pubkey, `x509.ParsePKIXPublicKey` |
| `ip` | string(CIDR) | — | обяз. (хотя бы один из ip/ipv6) | локальный IPv4 туннеля |
| `ipv6` | string(CIDR) | — | обяз. (хотя бы один) | локальный IPv6 туннеля |
| `uri` | string | по профилю | оба | CONNECT-IP URI-шаблон |
| `sni` | string | по профилю | оба | TLS SNI (для WARP != endpoint host) |
| `mtu` | int | 1280 | оба | MTU userspace-стека |
| `skip_cert_verify` | bool | false | cloudflare | отключить pubkey-pinning |
| `congestion_control`, `cwnd` | | | h3 | как tuic |

**Валидация fail-fast при старте:**
- `profile == cloudflare`: `private_key`, `public_key` обязательны и парсятся (иначе ошибка конфига).
- Хотя бы одно из `ip`/`ipv6` задано и парсится в CIDR.
- `network ∈ {h3, h2}`.
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

## Файлы и точки интеграции

```
constant/proxy.go
  + TypeMASQUE = "masque"                    // рядом с TypeTUIC (стр. ~25) + в displayName switch

option/masque.go                             (НОВЫЙ)
  + type MASQUEOutboundOptions struct { ... } // DialerOptions + ServerOptions + поля выше

protocol/masque/                             (НОВЫЙ пакет)
  outbound.go   — adapter.Outbound: NewOutbound, DialContext, ListenPacket, Close,
                  run()/насосы, resolve, RegisterOutbound
  device.go     — тонкая обёртка над transport/wireguard.stackDevice (или прямое исп.)

transport/masque/                            (НОВЫЙ пакет — вкопанный connect-ip-go + клиент)
  client_h3.go  — ConnectTunnel: http3.Transport{EnableDatagrams} → NewClientConn →
                  OpenRequestStream → SendRequestHeader(Extended CONNECT) → ReadResponse
  client_h2.go  — ConnectTunnelH2: http.Transport (h2c) → CONNECT → capsule DATAGRAM в теле
  connectip.go  — порт connect-ip-go: IpConn (ReadPacket/WritePacket), capsule-парсинг,
                  TTL/checksum, AdvertiseRoute (h3)
  profile.go    — cloudflareProfile / standardProfile, PrepareTLSConfig (pinning), GenerateCert
  const.go      — ConnectSNI, ConnectURI, capsule type constants

include/quic.go
  + masque.RegisterOutbound(registry) в registerQUICOutbounds()   // под with_quic
  + import _ protocol/masque
```

**Фабрика/registry не трогаем** — регистрация идёт через `outbound.Register[MASQUEOutboundOptions]`.

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

## Поток данных (h3)

```
NewOutbound: разобрать ключи, prefixes, profile, quicConfig{EnableDatagrams,InitialPacketSize:1242,KeepAlive:30s}
run() (lazy, sync.Once-паттерн):
  1. stackDevice = NewStackDevice(prefixes, mtu); stackDevice.Start()
  2. pc, quicConn = DialQuic(server, tlsConfig(profile), quicConfig)   // через наш dialer
  3. tr, ipConn = ConnectTunnel(quicConn, uri, profile)                // Extended CONNECT + AdvertiseRoute
  4. насос TX: for { stackDevice.Read(bufs,sizes) → ipConn.WritePacket(pkt) [→ icmp? → stackDevice.Write] }
  5. насос RX: for { ipConn.ReadPacket() → stackDevice.Write(pkt) }
DialContext/ListenPacket: run(); затем stackDevice.DialContext/ListenPacket(dest)
Close: cancel → ipConn.Close, tr.Close, pc.Close, stackDevice.Close
```

`ConnectTunnel` (h3): `tr.NewClientConn(quicConn)` → `OpenRequestStream` → `SendRequestHeader`
(`:method CONNECT`, `:protocol cf-connect-ip`, `Capsule-Protocol: ?1`, cf-заголовки) → `ReadResponse`
(status 2xx) → `connectip.NewProxiedConn(rstr)`; затем `AdvertiseRoute(0.0.0.0/0, ::/0)`.
При `ignoreExtendedConnect` НЕ падать, если `settings.EnableExtendedConnect == false`.

## Поток данных (h2)

`ConnectTunnelH2`: наш http-форк `Transport` c `DialTLSContext` (TCP dial → tls.Client с tlsConfig, ALPN `h2`,
h2c для запрета fallback на HTTP/1.1) → `MethodConnect` с телом-pipe → capsule DATAGRAM (`type 0` + varint len)
пишутся в тело запроса, читаются из тела ответа. `IpConn` тот же интерфейс (ReadPacket/WritePacket),
внутри — `h2DatagramStream` (см. mihomo `client_h2.go`). TTL/checksum-декремент делает клиент (composeDatagram).

**Зависимость h2:** нужен http-форк, поддерживающий `Protocols.SetUnencryptedHTTP2` / `HTTP2Config`
(mihomo использует `metacubex/http`). ⚠️ **Проверить при имплементации**, умеет ли наш стек (stdlib `net/http`
или форк) h2c-режим с masked-tls-conn (см. golang/go#79293). Если нет — h2 требует вкапывания http-форка
ИЛИ ручного h2-framing. Это риск объёма для h2 (см. §Риски).

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

## Риски / открытые вопросы

1. **h2 требует h2c-режима** с masked tls.Conn — стандартный `net/http` это не умеет чисто
   (golang/go#79293). Возможные пути: (а) вкопать нужную часть http-форка, (б) ручной h2-framing поверх
   tls.Conn, (в) отложить h2 в Ф2 и решить по факту. Влияет на объём — уточнить в начале Ф2.
2. **Экспорт `NewStackDevice`** из `transport/wireguard` — убедиться, что не тянет WG-код в masque-only сборку
   и не ломает существующие тесты WG-endpoint.
3. **DER-формат ключей** — согласовать байт-в-байт тест-вектор с Dart до Ф1-интеграции, иначе живой тест
   упадёт на парсинге.
4. **Cloudflare non-RFC quirks** могут дрейфовать (WARP меняет протокол) — привязываемся к текущему поведению
   mihomo Alpha; зафиксировать версию/коммит mihomo как референс в TEST_PLAN.

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
