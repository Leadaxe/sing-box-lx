# SPEC: 046 — DNS_HIJACK_PACKET_LOOP_STALL

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — хрупкость апстрима (sing-box DNS async path + sing-tun flow dispatch), чиним у себя |
| Статус | C (complete) — фикс в дереве (`1ea1eec13`), emulator-верификация пройдена, срезан в `v1.14.0-lx.20-rc.2`; device-подтверждение на конфиге инцидента ждём с поля |
| Ветка | `lx` |
| Base | `84ce10f09` (v1.14.0-lx.19-rc.3 + доки lx.20-rc.1) |
| Связанные | Инцидент LxBox 2026-08-02 (CPH2411, подписка Liberty): «первая страница грузится, дальше всё висит / сменилась сеть / 0 соединений в профайлере» |

**Touches:** реестр HOTFIXES (новая строка). Промисы других фич не затронуты.

## Why

**Один DNS-сервер с `detour` на неотвечающий outbound останавливает весь
форвардинг туннеля** — не только DNS этого сервера: гаснут чужие DNS-запросы,
ICMP, новые соединения всех протоколов. Конфиг при этом полностью легальный:

```json
{ "type": "udp", "tag": "yandex_udp", "server": "77.88.8.8", "detour": "vpn-2" }
```

Если выбранная в `vpn-2` нода молча дропается сетью (DPI silent-drop, чёрная
дыра — TCP SYN без ответа), dial DNS-транспорта висит до таймаута, и каждый
уникальный DNS-запрос, зароученный правилами в этот сервер, **блокирует
пакетный цикл tun-стека на полный DNS-таймаут (10 с)**. Постоянного фона
ru-запросов от Android-приложений (каждые ~5 с) достаточно, чтобы цикл стоял
почти непрерывно.

### Фактура

Устройство (OnePlus CPH2411, Android 15, LxBox 2.19.2, ядро v1.14.0-lx.19-rc.3,
stack=system, LTE Tele2, 2026-08-02):

- конфиг юзера: `yandex_udp` (77.88.8.8, detour vpn-2) + dns-правила
  `ru-domains/ru-services/RU apps → yandex_udp`; в `vpn-2` выбрана нода,
  которую сеть молча дропает (urltest delay −1);
- после каждого старта/reload туннеля форвардинг живёт 12–60 с, затем встаёт
  целиком: системный резолвер «unknown host» на любые свежие домены, ICMP к
  1.1.1.1 не проходит, профайлер показывает 0 соединений;
- ядро при этом живо и **не пишет ни одной ошибки**: в логах входящие
  `inbound DNS packet from …` продолжаются, ответных `dns: exchanged` нет;
  зафиксирован и промежуточный режим — `dns: exchanged A www.bbc.com … 44ms`
  есть, но ответ до клиента не доходит (записан после разблокировки цикла,
  когда netd уже ушёл в backoff);
- через ~4–5 минут форвардинг «сам» оживает (dial-таймауты отпускают цикл)
  и снова умирает — периодические волны;
- смена ноды vpn-2 на живую мгновенно и полностью снимает симптом
  (подтверждено юзером на устройстве);
- вторичный эффект: netd после серии таймаутов помечает DNS-сервер VPN
  unresponsive и отвечает «unknown host» мгновенно ещё минуты после оживания
  ядра — усиливает видимую длительность отказа.

Эмулятор (AVD hy2test, arm64, LxBox 2.19.2-dev.17, то же ядро, stack=gvisor,
2026-08-02): репро воспроизведён синтетически — `yandex_udp` с detour на
selector, где выбрана vless-нода с server `192.0.2.1:443` (TEST-NET, SYN в
никуда) + правило `domain_suffix: ["ru"] → yandex_udp` + поток ping разных
ru-доменов. Деградация форвардинга наблюдается на обоих стеках — баг **не
стеко-специфичен**.

## Механика

Обе ветки tun-стека вызывают DNS-hijack **синхронно из пакетного цикла**:

- system: `sing-tun/flow_dispatch.go` `ActionHijackDNS → d.hijackDNSPacket(packet)`
  → `flow_dns.go:14` → `handler.NewDNSPacket(...)` — в горутине диспатча
  пакетов (`ForwardDispatcher`);
- gvisor: `sing-tun/stack_gvisor_udp.go` `PreparePacketConnection` →
  `ActionHijackDNS → f.handler.NewDNSPacket(...)` — в контексте gvisor UDP
  forwarder (NIC dispatch).

Дальше цепочка целиком синхронна:

```
protocol/tun/inbound.go  NewDNSPacket
  → route/dns.go:44      Router.HijackDNSPacket   (Unpack + ExchangeAsync)
  → dns/router.go:1187   Router.ExchangeAsync
  → dns/router.go:922    exchangeWithRulesAsync    (simple route-правило → ветка client.ExchangeAsync)
  → dns/client.go:344    Client.ExchangeAsync
  → dns/client.go:677    exchangeToTransportAsync  (ctx = WithTimeout, дефолт 10 с)
  → dns/transport/udp.go UDPTransport.ExchangeAsync → queryMultiplexer.dispatch
  → dns/transport/multiplexer.go:297 exchangeAsync
  → dns/transport/conn_pool.go:298   ConnPool.acquireShared
```

`acquireShared` для `ConnPoolSingle` при отсутствии готового соединения ждёт
завершения dial:

```go
select {
case <-state.done:     // dial завершился (у мёртвого detour — никогда вовремя)
case <-ctx.Done():     // DNS-таймаут (10 с)
case <-current.ctx.Done():
}
```

`Client.ExchangeAsync`, вопреки имени, блокирует вызывающего вплоть до этого
select. Итог: **каждый уникальный** (question+transport) запрос в сервер с
висящим dial держит пакетный цикл 10 с. Дедуплицируются только точные дубли
(singleflight по cacheKey в `beginExchange`; ветка `exchangeWait` уходит в
горутину) — разные домены/типы выстраивают очередь блокировок цикла, и
форвардинг стоит, пока поток таких запросов не иссякнет.

Диагностическая сигнатура в goroutine-дампе: горутина пакетного цикла
(`ForwardDispatcher`/gvisor dispatch) в
`conn_pool.go acquireShared → select`, рядом — `queryMultiplexer.recvLoop`/
`connectSingle` с зависшим dial через detour.

Смертельный стек снят живьём (эмулятор, мёртвая фаза, 2026-08-02) — блокирован
**сам цикл чтения пакетов из tun fd**:

```
gvisor fdbased.(*endpoint).dispatchLoop           ← цикл чтения tun fd
  → GRO.Enqueue → networkDispatcherFilter.DeliverNetworkPacket
  → ForwardDispatcher.Dispatch → judgeAndInstall (flow_dispatch.go:323)
  → hijackDNSPacket (flow_dns.go:19)
  → tun.(*Inbound).NewDNSPacket (inbound.go:545)
  → route.(*Router).HijackDNSPacket (route/dns.go:53)
  → dns.(*Router).ExchangeAsync → exchangeWithRulesAsync → Client.ExchangeAsync
  → exchangeToTransportAsync → UDPTransport.ExchangeAsync → multiplexer
  → ConnPool.acquireShared select (conn_pool.go:333)   [BLOCKED]
```

## Требования

1. DNS-hijack не должен блокировать пакетный цикл ни одного из стеков:
   зависший exchange (dial detour, медленный сервер) задерживает только свой
   запрос.
2. Шторм hijack-запросов не должен порождать неограниченный рост горутин:
   параллелизм ограничен; сверх лимита пакеты дропаются (UDP-клиент
   ретрайнет) с debug-логом, без блокировки цикла.
3. Фикс общий для всех вызывателей `Router.HijackDNSPacket` (tun system,
   tun gvisor, wireguard/openvpn/openconnect/tailscale endpoints).
4. Минимальный дифф к upstream-файлам, `// lx:`-маркировка по CONSTITUTION.

## Критерии приёмки

1. Синтетика (эмулятор или устройство): конфиг с DNS-сервером `detour` на
   selector с нодой `192.0.2.1:443` + правило, роутящее в него поток
   уникальных доменов (≥1 запрос/2 с) → в течение ≥5 минут: ICMP к 1.1.1.1
   ходит стабильно, DNS через живые серверы отвечает, новые TCP-соединения
   устанавливаются. Запросы в мёртвый сервер фейлятся по своему таймауту —
   это ожидаемо и допустимо.
2. `go test ./...` зелёный; behaviour-тест на неблокирующий hijack.
3. Device-верификация на CPH2411 с конфигом инцидента (мёртвая нода vpn-2):
   симптом «всё висит» не воспроизводится.
