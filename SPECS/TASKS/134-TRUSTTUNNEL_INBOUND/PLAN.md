# PLAN: 134 — TRUSTTUNNEL_INBOUND

**Фича:** [018-TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) · контракт — [SPEC.md](SPEC.md) · клиентская половина и общий план фичи — [133](../133-TRUSTTUNNEL_OUTBOUND/PLAN.md)

> Тег `with_trusttunnel` в `LX_TAGS` (`Makefile.lx`) с 2026-10-16, общим шагом с задачей 133
> (решение владельца): inbound есть в desktop- и роутерных бинарях; HTTP/3-половина дополнительно требует `with_quic` (он в
> наборе). Решения владельца по §7 этим не закрыты.

## Этапы

Те же, что в [PLAN 133](../133-TRUSTTUNNEL_OUTBOUND/PLAN.md): (1) ядро `transport/trusttunnel/`
— здесь его серверная половина, (2) адаптер sing-box, (3) стенд и документация.

## Файлы

| Файл | Этап | Что меняется |
|------|------|--------------|
| `transport/trusttunnel/service.go` | 1 | `Service`, `ServiceOptions`, `Handler`, `Metadata`, `UpdateUsers` (таблица в `atomic.Pointer`). `ServeTCP(listener, tlsConfig)`: своя accept-петля → TLS-рукопожатие (15 с) → ALPN обязан быть `h2`, иначе соединение закрывается → `http2.Server.ServeConn` (`MaxConcurrentStreams` 1000, `IdleTimeout` 600 с); HTTP/1.1 не обслуживается. `ServeHTTP`: не-CONNECT → 404 с пустым телом и без `proxy-authenticate`; неверный `proxy-authorization` → 407 + `proxy-authenticate: Basic realm=Authorization Required`; `_check` → 200; `_icmp` → 404; authority без порта или с портом 0 → 400; `host:port` → `ServerConn` и `Handler.NewConnection`, стрим ждёт закрытия conn. `ServerConn`: 200 откладывается до первого `Read`/`Write`, `Fail(err)` до ответа → 502 + `X-Warning` (`302 - Connection timed out` на дедлайн, `301 - Remote host is unreachable` на `EHOSTUNREACH`/`ENETUNREACH`, иначе `300 - Connection failed for some reason`), `Close` до ответа → 502/300; запись после выхода хендлера исключена мьютексом и флагом. `Close` закрывает слушатели и все сессии |
| `transport/trusttunnel/service_udp.go` | 1 | `_udp2` → 200 сразу, цикл кадров; потоки по 4-кортежу кадра, новый кортеж → `udpFlow` (`N.PacketConn`) и `Handler.NewPacketConnection` в своей горутине; нулевой источник → `Metadata.Source` = адрес сессии; ответ кадрируется с отправителем как источником и исходным адресом клиента как назначением, под мьютексом записи стрима + flush; очередь 64 датаграммы на поток, сверх — дроп; `app_name` проверяется по длине и отбрасывается; конец стрима закрывает все потоки; битый кадр закрывает стрим с записью в лог |
| `transport/trusttunnel/service_quic.go`, `service_quic_stub.go` | 1 | Под `with_quic`: `qtls.ConfigureHTTP3`, ALPN `h3`, `qtls.ListenEarly` (`MaxIncomingStreams` 4096, `Allow0RTT: false`, `MaxIdleTimeout` 600 с, `DisablePathManager`), `http3.Server` (`IdleTimeout` 600 с, congestion control в `ConnContext`), тот же `ServeHTTP`. Заглушка: `ErrQUICNotIncluded` |
| `transport/trusttunnel/service_test.go`, `service_quic_test.go` | 1 | Общий end-to-end с задачей 133 (список — в [TASKS 133](../133-TRUSTTUNNEL_OUTBOUND/TASKS.md) п. 6) плюс серверное: 404 без `proxy-authenticate` на GET, клиент без `h2` в ALPN не получает HTTP/1.1, 1000 одновременных стримов на H2 и 4096 на H3 |
| `option/trusttunnel.go` | 2 | `TrustTunnelInboundOptions`: `ListenOptions`, `users`, `network`, `quic_congestion_control`, `InboundTLSOptionsContainer` (общий файл с 133) |
| `protocol/trusttunnel/inbound.go` | 2 | Валидация §3 (пустой `users` или пустое поле в записи, `icmp` в `network`, `tls.enabled: false` → `C.ErrTLSRequired`, неизвестный congestion control); `Start(StartStateStart, scope)`: TLS-конфиг, `tcp` → `ServeTCP` на TCP-слушателе, `udp` → `ServeQUIC` на UDP-слушателе (`udp` без `with_quic`: ошибка старта, если он один, предупреждение, если оба); `Handler` → `RouteConnectionEx` с `onClose(err)` → `ServerConn.Fail(err)`, `RoutePacketConnectionEx`; `metadata`: `Inbound`, `InboundType`, `User`, `Source`, `Destination`, `OriginDestination` = адрес слушателя; `Close` через scope |
| `protocol/trusttunnel/inbound_quic.go` | 2 | По SPEC R1 — хук под `with_quic`. Ядро уже гейтит HTTP/3 своим `with_quic` и отдаёт `ErrQUICNotIncluded`, поэтому файл может не понадобиться; решается на этапе 2 по тому, как адаптеру удобнее узнать о H3 до открытия UDP-слушателя |
| `include/*`, `constant/proxy.go` | 2 | Общие с 133, уже посчитаны в SPEC 133 §5 |
| `docs-lx/protocols-transports.md`, `.ru.md` | 3 | Раздел inbound рядом с outbound: конфиг §3, три предела для оператора (нет `client_random`-правил, нет подписки, не-туннельные пути → 404), route-правило `ip_is_private` с `"action": "reject"` как замена `allow_private_network_connections` |
| `lx-test/trusttunnel/` | 3 | Официальный клиент (`stand.sh client-up`) через наш inbound: H2, H3, `upstream_protocol: auto` против H2-only, ICMP у клиента включён; R4 при `sniff`; `curl https://host/` → 404 |

## Решения реализации

- **Своя accept-петля вместо `http.Server` + `http2.ConfigureServer`.** `aTLS.NewListener` отдаёт
  `*aTLS.LazyConn`, не `*tls.Conn`, поэтому `TLSNextProto` у `http.Server` не срабатывает никогда
  (`naive` обходит это через `h2c`). Петля проверяет ALPN после рукопожатия и передаёт соединение
  `http2.Server.ServeConn` — HTTP/1.1 не обслуживается по построению.
- **API сервиса — `ServeTCP` / `ServeQUIC` / `Close`**, а не `Start(tcpListener, packetConn,
  tlsConfig)` из SPEC R9: каждый транспорт блокируется в своём `Serve*`, как `http.Server.Serve`, и
  адаптер запускает только нужные по `network`. Слушатели открывает и закрывает адаптер.
- **Отложенный 200 и `Close`.** Если роутер закрыл conn, не прочитав ничего, клиент получает
  502/300, а не 200 с пустым телом (умолчание HTTP-сервера при выходе хендлера без ответа).
- **`Metadata.Source`** берётся из `RemoteAddr` запроса; `X-Forwarded-For` не учитывается — его
  подставляет клиент.

## Что не меняется

- `include/quic.go`, `include/quic_stub.go`, `protocol/naive/*` — `naive` остаётся образцом.
- `go.mod`, `go.sum`. `Makefile.lx` (`LX_TAGS`) — одна правка, общая с 133 (2026-10-16).

## Проверка

- Этап 1 — общая с 133 (см. [PLAN 133](../133-TRUSTTUNNEL_OUTBOUND/PLAN.md) «Проверка»).
- Этап 2: сборки без тега, с `with_trusttunnel` без `with_quic` (только H2), с обоими; AAR;
  `go vet` под всеми наборами; `sing-box check` на конфиге §3 и на ошибках валидации.
- Этап 3: §6 SPEC — официальный клиент и наш outbound через inbound, R4 на закрытом порту
  (`X-Warning 300`) и при `sniff`, R7, idle 600 с, route rules на `inbound: tt-in`.
