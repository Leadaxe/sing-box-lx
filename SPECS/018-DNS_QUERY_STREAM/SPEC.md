# SPEC 018 — DNS query stream: `SubscribeDNSQueries` (structured, live)

**Тип:** lx-фича (новый command-RPC, наблюдаемость DNS)
**Статус:** Open → реализуется на ветке `lx-1.14`
**Приоритет:** Medium (профайлер LxBox: атрибуция DNS-запросов к приложению в реальном
времени)
**Файлы ядра:** `common/dnstrack/*` (новый), `dns/client.go`, `dns/client_log.go`,
`daemon/started_service.proto`, `daemon/started_service_command_lx.go`,
`experimental/libbox/*`
**Связано:** SPEC 014 (Clash→CommandClient), SPEC 015 (RPC extensions), SPEC 017
(Connection.Detour), SPEC 016 (connections map race — тот же `observable` слой)

## Задача

LxBox показывает TCP/UDP из connections-стрима (структурные `CcConnection` с
`getProcessInfo()`, `rule`, `chains` — атрибуция к приложению приходит из ядра готовой).
DNS-запросы же приходится брать из **текстового лога** (`_handleDnsLine` парсит строки
`dns: exchanged …`), а package'а там нет — приходится сшивать по conn_id с TCP-стримом.
Лог — «обглоданный канал». Нужен **структурный live-поток DNS-запросов** с атрибуцией к
процессу, симметричный connections-стриму.

## Корень: hijack-нутый DNS минует tracker

DNS-запросы приложений на Android-VPN перехватываются правилом `hijack-dns`. В
`route/route.go:88-157` hijack уходит в `hijackDNSStream`/`hijackDNSPacket`
(`route/dns.go:23,38`) **до** строки 157, где обычное соединение становится
`tracker.RoutedConnection(...)`. Значит:

- В connections-стрим (trafficcontrol) hijack-нутый DNS **не попадает** — его там нет.
- Фильтр `metadata.OutboundType != C.TypeDNS` (`experimental/clashapi/connections.go:35`)
  режет не это, а редкий DNS-as-outbound.
- v2ray Stats DNS не считает; Clash API даёт только `/dns/flush`.
- **Единственный существующий выход DNS-запросов наружу — текстовый лог**
  (`dns/client_log.go`), потому LxBox его и парсит. Это не лень, а отсутствие
  структурной альтернативы.

## Данные в точке лога уже на руках

Все пять лог-функций (`logExchanged/Cached/Optimistic/Refreshed/Rejected Response`,
`dns/client_log.go`) вызываются из `dns/client.go` (`:199,203,257,389,520`) с `ctx` и
`response *dns.Msg`. В этой точке доступно без новых параметров:

| Поле | Источник | Стоимость |
|---|---|---|
| `domain` | `response.Question[0]` | уже извлекается |
| `qtype` | `response.Question[0].Qtype` | в руке |
| `rcode` | `response.Rcode` | уже логируется |
| `ttl` | параметр функции | уже логируется |
| `source` | какая из 5 функций (cached/exchanged/optimistic/refreshed/rejected) | имя функции |
| `processInfo` | `adapter.ContextFrom(ctx).ProcessInfo` | один вызов |
| `answers[]` | `response.Answer` | уже итерируется; опционально |

`ctx` — тот же, что течёт от `dns/router.go` (`adapter.ContextFrom`/`ExtendContext` на
`:289,427,548,661,791`), где `metadata.ProcessInfo` уже заполнен (это подтверждается тем,
что DNS-правила матчат `package_name_regex`, `route/rule/rule_dns.go:257` — package
известен к моменту резолва). Значит атрибуция к приложению **доступна**, просто не
выводится структурно.

`source` — недооценённое поле: бесплатно (имя функции) и для профайлера ценно (кэш vs
сеть — прямой сигнал эффективности DNS).

## Решение: пакет `dnstrack` поверх существующего `observable`

`SubscribeConnections` доставляет live-поток через `common/observable.Subscriber[T]`
(generic pub/sub, `common/trafficcontrol/manager.go:47-91`). Тот же слой переиспользуется
для DNS — не нужен новый транспорт.

```
common/dnstrack (новый, зеркало trafficcontrol/manager.go):
  Manager{ eventSubscriber *observable.Subscriber[DnsQueryEvent] }
  NewManager() → Subscriber(256) + Observer(64)
  SubscribeEvents() / UnSubscribeEvents()
  Emit(DnsQueryEvent{...})

dns/client_log.go (точка эмита, рядом с каждым log*Response):
  manager.Emit(DnsQueryEvent{domain, qtype, rcode, ttl, source, processInfo})
    ← processInfo := adapter.ContextFrom(ctx).ProcessInfo

command server (daemon/started_service_command_lx.go):
  SubscribeDNSQueries(stream)  ← копия SubscribeConnections-доставки

proto:
  rpc SubscribeDNSQueries(Empty) returns (stream DnsQueryEvent)
  message DnsQueryEvent { domain, qtype, rcode, ttl, source, ProcessInfo, [answers] }

libbox:
  handleDNSQueriesStream + DnsQuery struct  ← копия handleConnectionsStream

LxBox:
  подписка на DNS-стрим → выкинуть _handleDnsLine текстовый парсинг
```

Профайлер потребляет DNS в реальном времени → именно **stream** (`SubscribeDNSQueries`),
а не pull-снапшот. Снапшот `GetDNSQueries` можно добавить позже как re-read при потере
стрима (как `GetGroups` рядом с `SubscribeGroups` в SPEC 015) — НЕ в этом SPEC.

### Точки правки

1. **`common/dnstrack/manager.go`** (новый) — `Manager` с `observable.Subscriber[DnsQueryEvent]`,
   `SubscribeEvents/UnSubscribeEvents/Emit`, `Start/Close` (lifecycle), зеркало
   trafficcontrol. `DnsQueryEvent` struct.
2. **`box.go`** — создать и зарегистрировать `dnstrack.Manager` (как trafficManager),
   гейт `needObservable` (DNS-наблюдаемость нужна тому же платформенному клиенту).
3. **`dns/client_log.go`** / **`dns/client.go`** — в каждой `log*Response` (или рядом)
   достать `adapter.ContextFrom(ctx).ProcessInfo` и `manager.Emit(...)`. Manager берётся
   из ctx (`service.FromContext`), как logger.
4. **`daemon/started_service.proto`** — `rpc SubscribeDNSQueries` + `message DnsQueryEvent`
   (additive); реген pb.go ручным минимальным дифом (как SPEC 017 field 23).
5. **`daemon/started_service_command_lx.go`** — серверный стрим, зеркало
   `SubscribeConnections` (тикер не нужен — DNS-события событийные, эмитятся по факту).
6. **`experimental/libbox/`** — `handleDNSQueriesStream` + `DnsQuery` struct +
   subscription handle (зеркало connections).
Всё за тегом `with_lx_command` там, где пересекает command-поверхность (как SPEC 014/015).

### Объём поля

`domain`, `qtype`, `rcode`, `ttl`, `source`, `processInfo` (цель), `failed` + `error`
(провалы, пункт 1). `answers[]` присутствует в proto с v1, по умолчанию off за флагом
подписки (переменный размер; нужен для CNAME-сшивки пункт 2 и будущей DNS↔TCP
IP-атрибуции — без proto-bump позже).

**`answers[]` = ВЕСЬ `response.Answer` в исходном порядке — CNAME-hops И финальные
A/AAAA, НЕ отфильтрованные до конечных IP.** Каждый элемент `{name, type, rdata, ttl}`.
Профайлеру для `cnameChain` нужны промежуточные CNAME-записи (`api.x.ru → cdn.y.ru →
IP`), а не только результат — реализатор НЕ должен «помогать», оставляя только A/AAAA,
иначе цепочку не собрать и флаг бесполезен. Проверено: `response.Answer` несёт смешанные
RR (`dns.CNAME`/`dns.A`/`dns.AAAA`, `dns/client.go:608-647`), ядро их не фильтрует —
отдавать как есть.

### Канал

DNS-события событийные (эмит на каждый резолв), не тиковые. На активном устройстве DNS
реже, чем traffic-апдейты connections, и каждое событие — короткая структура. Слой
`observable` с буфером 256 (как connections) гасит всплески; переполнение дропает
старейшие (профайлер — наблюдатель, не аудит). Отдельного канала/тикера не требует.

### Связь с SPEC 016

`observable.Subscriber` — тот же слой, где SPEC 016 нашёл map-гонку. Серверная сторона
(`Subscriber.Emit`) безопасна; клиентская аккумуляция событий — забота клиента, вне этого
SPEC.

## Согласованная форма (контракт, финал)

Решено по фидбэку — реализация следует этому буквально.

**`dnstrack.QueryEvent` (Go):**
```go
type QueryEvent struct {
    Domain      string
    QueryType   uint16
    Rcode       int32                     // dns.Rcode; -1 когда ответа нет (timeout)
    TTL         uint32
    Source      Source                    // exchanged/cached/optimistic/refreshed/rejected/failed
    Failed      bool                      // true на timeout/loopback/rejected-cached/SERVFAIL-reject
    Error       string                    // причина: "timeout"/"loopback"/"rejected"/…; "" на успехе
    ProcessInfo *adapter.ConnectionOwner
    Answers     []Answer                  // ВЕСЬ response.Answer; nil если includeAnswers=false
}
type Answer struct { Name string; Type uint16; RData string; TTL uint32 }
```

**`Source`:** добавляется `SourceFailed = "failed"` (провал отличим по `source`, не только
по флагу).

**Решения по развилкам:**
- **Q1 — `Rcode = -1` при `response == nil`** (timeout): sentinel «нет ответа», явно
  отличается от `0`=NOERROR. При `response != nil` — реальный `response.Rcode`.
- **Q2 — `SourceFailed`** на всех провальных путях (плюс `Failed=true`).
- **Q3 — флаг `includeAnswers`** в запросе подписки: `answers[]` едут ТОЛЬКО когда клиент
  запросил (иначе пустой трафик). Меняет RPC-вход с `Empty` на `SubscribeDNSQueriesRequest`.

**proto:**
```proto
rpc SubscribeDNSQueries(SubscribeDNSQueriesRequest) returns (stream DnsQueryEvent) {}
message SubscribeDNSQueriesRequest { bool includeAnswers = 1; }
message DnsQueryEvent {
  string domain = 1; uint32 queryType = 2; int32 rcode = 3; uint32 ttl = 4;
  string source = 5; ProcessInfo processInfo = 6;
  bool failed = 7; string error = 8;            // пункт 1
  repeated DnsAnswer answers = 9;               // пункт 2, только при includeAnswers
}
message DnsAnswer { string name = 1; uint32 type = 2; string rdata = 3; uint32 ttl = 4; }
```

**Эмит-точки в `dns/client.go` Exchange:**
- `:213` loopback → `Failed`, `Error="loopback"`, `Rcode=-1`, поля из `question`.
- `:219` rejected-cached → `Failed`, `Error="rejected (cached)"`, `Rcode=-1`, из `question`.
- `:224` transport error → `Failed`, `Error=err.Error()` (timeout/сеть), `Rcode=-1`, из `question`.
- `:239` SERVFAIL/checker reject → `Failed`, `Error="rejected"`, `Rcode=response.Rcode`, из `response`.
- успех (`client_log.go` logExchanged/cached/optimistic/refreshed) — `Failed=false`,
  `Source` по вербу, `Rcode=response.Rcode`.

## Фидбэк по корректности канала (по коду)

Скоуп — только свойства самого канала ядра. Что делает с ним LxBox (держит ли текстовый
парсинг, как дедуплицирует) — вне этого SPEC.

### Пункт 1 — провалы DNS ОБЯЗАНЫ эмитить событие (подтверждён по коду)

`dns/client.go`: на ошибке транспорта (timeout/сеть) путь —
`c.exchangeToTransport(...)` → `return nil, err` (`:222-224`), `response == nil`, **ни
одна `log*Response` не вызывается**. SERVFAIL-через-rejected (`:239`), loopback (`:213`),
rejected-cached (`:219`) — тоже до успешного лога. Эмит, висящий на лог-функциях,
**пропускает все сбои** → канал неполон. Провалы — главный диагностический сигнал.

**Решение:** `DnsQueryEvent` получает `bool failed` + `string error`; эмит добавляется на
путях провала в `Exchange` (перед `return nil, err` на `:213/:219/:224/:239`). При
`response != nil` — `rcode` несёт код; при `response == nil` — `failed=true` + `error`,
поля запроса из `message.Question[0]`, `processInfo` из ctx.

### Пункт 2 — CNAME-цепочка через `answers[]` (без query-id)

Стабильного per-query id в `InboundContext` нет (`message.Id` — переиспользуемый 16-бит
transaction ID, негоден). Но и не нужен: CNAME-цепочка одного ответа целиком в
`response.Answer` одного `*dns.Msg`, который на руках в точке эмита. `answers[]` отдаёт её
в одном событии — сшивка по conn_id не требуется. **Отдавать ВЕСЬ `response.Answer`
(CNAME-hops + финальные A/AAAA), не только конечные IP** — иначе `cnameChain` не собрать
(см. «Объём поля»). (Связка split A/AAAA как РАЗНЫХ событий — возможное будущее additive
`query_id`, не сейчас.)

### Пункт 3 — ProcessInfo на cached/optimistic (проверено — регрессии нет)

`logCachedResponse` (`:203`), `logOptimisticResponse` (`:199`), `logExchangedResponse`
(`:257`) получают ОДИН ctx — параметр `Exchange`. Cache-hit идёт коротким путём, но из
того же ctx. `adapter.ContextFrom(ctx).ProcessInfo` непуст одинаково на всех путях.
`refreshed` (`:520`) — background-горутина с тем же ctx из замыкания. Атрибуция cached-DNS
корректна.

## Критерии готовности

- Каждый успешный резолв (exchanged/cached/optimistic/refreshed) эмитит `DnsQueryEvent`
  с `domain` + `processInfo`.
- **Провалы (timeout/SERVFAIL/rejected/loopback) тоже эмитят** с `failed=true` и/или
  ненулевым `rcode` + `error` (пункт 1 — полнота канала).
- `qtype/rcode/ttl/source` заполнены; `answers[]` в proto за флагом (пункт 2).
- ProcessInfo непуст на cached/optimistic путях, не только exchanged — протестировать
  cache-hit отдельно (пункт 3).
- Старый core без `with_lx_command` → `codes.Unimplemented`; proto additive;
  `go build ./...` зелёная; gofmt чист.
