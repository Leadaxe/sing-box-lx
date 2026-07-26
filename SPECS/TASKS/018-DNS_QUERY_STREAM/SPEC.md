# SPEC 018 — DNS query stream (structured, live, via command-мультиплекс)

**Фича:** [OBSERVABILITY](../../FEATURES/OBSERVABILITY/FEATURE.md)

**Тип:** lx-фича (наблюдаемость DNS через command-мультиплекс)
**Статус:** C (complete) — v2 (DNS-стрим в command-мультиплексе) реализован и отгружен
**Приоритет:** Medium (профайлер LxBox: атрибуция DNS-запросов к приложению в реальном времени)
**Файлы ядра:** `common/dnstrack/*`, `dns/client.go`, `dns/client_log.go`,
`daemon/started_service.proto`, `daemon/started_service_command_lx.go`, `experimental/libbox/*`
**Связано:** SPEC 014 (Clash→CommandClient), SPEC 015 (RPC extensions), SPEC 017 (Connection.Detour), SPEC 016 (observable слой)
**История смены архитектуры:** [HISTORY.md](HISTORY.md) (v1 standalone-подписка → v2 мультиплекс; баг, отвергнутые пути)

---

## Что это

Структурный live-поток DNS-резолвов с атрибуцией к процессу, симметричный
connections-стриму. Каждый резолв (успех и провал) даёт событие с доменом, qtype, rcode,
ttl, источником (cache/сеть), процессом-владельцем, DNS-сервером и, опционально,
CNAME-цепочкой. Заменяет текстовый парсинг core-лога, где атрибуции к приложению нет.

**Транспорт: DNS — обычный член command-мультиплекса, устроен идентично
`CommandConnections`.** Клиент подписывается через `addCommand(CommandDNS)`; стрим живёт на
общем `c.ctx` `CommandClient` и авто-восстанавливается вместе со всеми стримами профайлера
при реконнекте (`Connect()` перезапускает `dispatchCommands` по `options.commands`).
Поднимается и опускается вместе с профайлер-клиентом — не отдельная сущность, не вечная
горутина, независимого `Close()` нет (on-demand управляется на стороне клиента через
refcount профайлера).

## Архитектура

Два независимых слоя: **источник событий в ядре** (эмиссия) и **транспорт наружу**
(мультиплекс). Границу держать чётко — источник не знает про транспорт.

### Слой 1 — источник событий (`common/dnstrack`)

```
common/dnstrack/manager.go:
  Manager{ eventSubscriber *observable.Subscriber[QueryEvent] }   // зеркало trafficcontrol
  NewManager() → Subscriber(256) + Observer(64)
  SubscribeEvents() / UnSubscribeEvents() / Emit(QueryEvent)
  HasSubscribers() bool                                            // atomic счётчик подписок

box.go:  service.MustRegisterPtr(ctx, dnstrack.NewManager())       // читается PtrFromContext

dns/client_log.go:  emitQueryEvent / emitFailedQuery
  manager := service.PtrFromContext[dnstrack.Manager](ctx)
  if manager == nil || !manager.HasSubscribers() { return }        // гейт ПЕРЕД построением
  manager.Emit(QueryEvent{ domain, qtype, rcode, ttl, source, processInfo, answers, … })
```

**`HasSubscribers`-гейт обязателен.** Без открытого профайлера DNS-горячий путь не строит
ни событие, ни `answers`, ни теги — нулевая стоимость в обычном режиме. Гейт стоит в обеих
эмит-функциях (успех и провал) — они за ОДНИМ условием, поэтому «нет подписчика» глушит и
успех, и провал согласованно.

**Эмит-точки** (`dns/client.go` `Exchange`): успех — из `log*Response` (`client_log.go`),
провал — из `emitFailedQuery` перед `return nil, err` (timeout/loopback/rejected-cached/
SERVFAIL). ProcessInfo берётся из `adapter.ContextFrom(ctx).ProcessInfo`.

> **ProcessInfo на fast-path.** Hijack-нутый DNS (большинство UDP DNS на VPN) уходит в
> `hijackDNSStream`/`hijackDNSPacket` (`route/dns.go`) ДО `matchRule`, где заполняется
> `metadata.ProcessInfo`. Поэтому `r.searchProcessInfo(ctx, &metadata)` ОБЯЗАН вызываться
> ПЕРЕД обоими fast-path hijack-вызовами (идемпотентен, кэширован по `{network,source,
> destination}`), иначе событие доходит до эмита с `ProcessInfo==nil`.

### Слой 2 — транспорт (command-мультиплекс)

Серверный gRPC-стрим и proto — те же, что у любого `Subscribe*`. Клиент вызывает их из
мультиплекса, а не отдельным методом.

```
proto (daemon/started_service.proto):
  rpc SubscribeDNSQueries(SubscribeDNSQueriesRequest) returns (stream DnsQueryEvent) {}

server (daemon/started_service_command_lx.go, за with_lx_command):
  SubscribeDNSQueries: manager := PtrFromContext[dnstrack.Manager]; SubscribeEvents;
    defer UnSubscribeEvents; select-loop { event → server.Send }

libbox client (experimental/libbox/):
  command.go:            CommandDNS  (следующая в iota, как CommandConnections)
  command_client.go:     case CommandDNS: go c.handleDNSStream()   // в общем ряду dispatchCommands
                         CommandClientOptions.DNSIncludeAnswers     // поле, как StatusInterval
                         CommandClientHandler.WriteDNSQuery(*DnsQuery)   // рядом с WriteConnectionEvents
  handleDNSStream():     калька handleConnectionsStream — client.SubscribeDNSQueries(c.ctx, req),
                         for { Recv → WriteDNSQuery | err → Disconnected }

LxBox (BoxCommandClient.kt):
  connectProfilerClient(): options.addCommand(CommandDNS); options.setDNSIncludeAnswers(true)
  ProfilerHandler.writeDNSQuery(query): маппинг → dnsQueriesEmitter (как writeConnectionEvents)
```

**Ключевое отличие от connections — ноль.** DNS-стрим устроен на всех уровнях так же:
константа в общем iota, `case` в общем switch, метод в общем handler-интерфейсе, поле опций
как `StatusInterval`. Реконнект бесплатный (общий `c.ctx` + `dispatchCommands`); отдельного
`OnError`-пути нет — обрыв идёт через `Disconnected()`.

## Контракт события

**`dnstrack.QueryEvent` (Go):**
```go
type QueryEvent struct {
    Domain        string
    QueryType     uint16
    Rcode         int32                     // dns.Rcode; -1 когда ответа нет (timeout)
    TTL           uint32
    Source        Source                    // exchanged/cached/optimistic/refreshed/rejected/failed
    Failed        bool                      // true на timeout/loopback/rejected-cached/SERVFAIL-reject
    Error         string                    // "timeout"/"loopback"/"rejected"/…; "" на успехе
    ProcessInfo   *adapter.ConnectionOwner
    Answers       []Answer                  // ВЕСЬ response.Answer; nil если includeAnswers=false
    DNSServer     string                    // transport.Tag()
    DNSServerType string                    // transport.Type(): udp/tls/https/quic
    Outbound      []string                  // detour-тег DNS-сервера; пусто на cached/optimistic
}
type Answer struct { Name string; Type uint16; RData string; TTL uint32 }
```

**proto (additive):**
```proto
rpc SubscribeDNSQueries(SubscribeDNSQueriesRequest) returns (stream DnsQueryEvent) {}
message SubscribeDNSQueriesRequest { bool includeAnswers = 1; }
message DnsQueryEvent {
  string domain = 1; uint32 queryType = 2; int32 rcode = 3; uint32 ttl = 4;
  string source = 5; ProcessInfo processInfo = 6;
  bool failed = 7; string error = 8;
  repeated DnsAnswer answers = 9;               // только при includeAnswers
  string dnsServer = 10; string dnsServerType = 11; repeated string outbound = 12;
}
message DnsAnswer { string name = 1; uint32 type = 2; string rdata = 3; uint32 ttl = 4; }
```

**Семантика полей:**

- **`Rcode = -1` при `response == nil`** (timeout): sentinel «нет ответа», отличается от
  `0`=NOERROR. proto `int32` (signed varint) — `-1` на проводе физически ≠ `65535`. Клиент
  маппит `rcode == -1` → «нет ответа» ДО `.toUInt()`, иначе `-1` станет `4294967295`.
- **`Source` + `Failed`.** `SourceFailed="failed"` на всех провальных путях (плюс
  `Failed=true`). Успех — `exchanged`/`cached`/`optimistic`/`refreshed` (кэш vs сеть —
  сигнал эффективности DNS).
- **`Answers[]` — ВЕСЬ `response.Answer` в исходном порядке** (CNAME-hops И финальные
  A/AAAA, НЕ отфильтрованные до IP). Профайлеру для `cnameChain` нужны промежуточные CNAME.
  Едет только при `includeAnswers=true` (переменный размер — иначе пустой трафик).
- **`DNSServer`/`DNSServerType`** = `transport.Tag()`/`Type()`, доступны на всех путях
  (успех+провал). **`Outbound`** = detour-тег транспорта (`OutboundTag()` из
  `DialerOptions.Detour`); ядро кладёт статический тег, СЕРВЕР разворачивает селектор в
  активный узел через `Now()` при отдаче (как `Connection.Detour` SPEC 017). Пусто на
  `cached`/`optimistic` (запрос не уходил).

## Изоляция и merge-зона

- **Ядро / сервер / proto — за `with_lx_command`**, как SPEC 014/015. `common/dnstrack` —
  новый пакет. `.pb.go` — регенерируемый артефакт (см. §3.3 CONSTITUTION).
- **Апстрим-контакт (3 точки, осознанная цена за однообразность)** — все в
  `experimental/libbox/`, помечены `// lx:begin dns` / `// lx:end dns`:
  1. `command.go` — `CommandDNS` следующей в iota-блоке;
  2. `command_client.go` `dispatchCommands` — `case CommandDNS: go c.handleDNSStream()`;
  3. `command_client.go` `CommandClientHandler` — метод `WriteDNSQuery(*DnsQuery)`;
  плюс поле `DNSIncludeAnswers` в `CommandClientOptions` (холодная зона).
- `handleDNSStream`, `DnsQuery`/`DnsAnswer`, `dnsQueryFromGRPC` — в lx-owned
  `command_client_command_lx.go` (0 merge-риска).
- Merge-конфликт при ре-графе — только когда апстрим сам добавляет команду (редко);
  резолвится тривиально (наши строки съезжают ниже апстримовских, разные hunks).

## Критерии готовности

- Каждый резолв (успех: exchanged/cached/optimistic/refreshed; провал: timeout/SERVFAIL/
  rejected/loopback) эмитит событие с `domain` + `processInfo`; провалы — `failed=true`
  и/или ненулевой rcode + error.
- `qtype/rcode/ttl/source/dnsServer` заполнены; `answers[]` за флагом `includeAnswers`.
- ProcessInfo непуст на cached/optimistic путях, не только exchanged (тест cache-hit
  отдельно — покрывает fast-path searchProcessInfo-фикс).
- DNS-стрим — член мультиплекса: `addCommand(CommandDNS)` поднимает его вместе с
  connections; при обрыве/фоне/Doze авто-восстанавливается через `Connect()` БЕЗ отдельного
  reconnect-кода на клиенте; гаснет вместе с профайлер-клиентом.
- Старое ядро без `with_lx_command` → `codes.Unimplemented`; proto additive; `go build ./...`
  зелёная; `gofmt -l` чист на lx-файлах.
- **Device-verify:** §180-поток оживает после ухода в фон и возврата, БЕЗ Kotlin
  reconnect-хука (доказывает, что мультиплекс-реконнект переподнял DNS); события идут с
  атрибуцией (`processInfo`, `answers[]` CNAME, `outbound`, `rcode`).
