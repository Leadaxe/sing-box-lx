# SPEC 018 — HISTORY

Хронология и обоснование смены архитектуры DNS-стрима. Актуальное устройство — в
[SPEC.md](SPEC.md); здесь только «как было раньше и почему переделали».

---

## v1 (rc.7–rc.16) — standalone-подписка. Признана ошибкой класса.

### Как было

DNS-стрим был реализован как **отдельный императивный gRPC-метод** на `CommandClient`,
вне command-мультиплекса:

```
experimental/libbox/command_client_command_lx.go:
  func (c *CommandClient) SubscribeDNSQueries(includeAnswers bool, handler DnsQueryHandler)
      (*DnsQuerySubscription, error)
    → собственный context.WithCancel(parentCtx)   // НЕ c.ctx
    → go-рутина: for { stream.Recv() → handler.OnQuery() | handler.OnError()+return }

  type DnsQuerySubscription struct { streamSession }   // свой Close()
  type DnsQueryHandler interface { OnQuery(*DnsQuery); OnError(string) }
```

Клиент (LxBox `BoxCommandClient.kt`) вызывал `client.subscribeDNSQueries(true, DnsHandler())`
ПОВЕРХ уже подключённого `profilerClient`, держал `DnsQuerySubscription` в
`AtomicReference`, закрывал вручную в `disconnectProfiler()`/`shutdownAll()`. Отдельный
`DnsHandler` inner-class реализовывал `DnsQueryHandler` (не тот же класс, что
`CommandClientHandler`).

### Почему это была ошибка класса

`SubscribeDNSQueries` — **streaming-подписка**, но оформлена в **классе unary Get-методов**
(рядом с `URLTestOutbound`/`GetRules`/`GetGroups`/`GetPool` в `command_client_command_lx.go`),
со своим `context.WithCancel` и `OnQuery/OnError`-хендлером. Правильный класс для неё —
**streaming-мультиплекс** (`handleConnectionsStream`/`handleStatusStream` на общем `c.ctx`,
диспетчеризуемые `dispatchCommands` по `options.commands`).

Правильная классификация (установлена по коду):

| Класс | Механизм | Reconnect | Примеры |
|---|---|---|---|
| Unary | запрос-ответ | не нужен | `GetRules`, `GetGroups`, `GetPool`, `URLTestOutbound` |
| Streaming в мультиплексе | `addCommand` → `dispatchCommands` | ✅ авто (при `Connect()`) | `SubscribeConnections/Status/Groups/Outbounds` |
| Streaming ВНЕ мультиплекса ← **ОШИБКА** | ручной метод + `Subscription` | ❌ нет | **`SubscribeDNSQueries`** |

`SubscribeDNSQueries` — единственная streaming-подписка среди lx-фич, и единственная,
попавшая не в свой класс.

### Баг, который это породило (наблюдался на устройстве)

**Симптом (CPH2411/ColorOS):** §180 DNS-поток эмитил события ~47с после старта recording,
потом ЗАМОЛКАЛ навсегда (0 событий за 27+ мин активного теста), хотя TCP/UDP в том же
профайлере шли, а core-лог показывал живые `dns: exchanged` и `dns: lookup failed …
deadline exceeded` ПРЯМО В ЭТОТ МОМЕНТ.

**Корень.** DNS-стрим не переживал уход в фон / Doze / смену сети:
- `CommandConnections` (TCP/UDP) авто-ре-стримится: он в `dispatchCommands`, а `Connect()`
  безусловно перезапускает весь `options.commands` при реконнекте.
- standalone-`SubscribeDNSQueries` — НЕ в `dispatchCommands`; его go-рутина на `recvErr`
  звала `handler.OnError()` и умирала (`command_client_command_lx.go`), и её никто не
  переподнимал.
- На ядре при закрытии стрима `defer manager.UnSubscribeEvents` снимал подписчика →
  `HasSubscribers()` → false → эмиссия by-design глохла (и успех, и провал ОДНОВРЕМЕННО —
  поэтому «молчат оба», а не «теряется error-путь»).

Триггер — фон: LxBox `home_controller.dart` серией писал «Resumed from background —
streams were paused», первый ровно за 3с до последнего DNS-события. Resume звал
`resumeClients()`, но тот восстанавливал только status+screen, не DNS-подписку.

Диагностические ловушки этого расследования (для будущих):
- `/logs/core` на ColorOS обрезан (Go-stderr→/dev/null) — судить о DNS по §180-потоку, не по логу.
- attributed `/profiler/live` НИКОГДА не показывает DNS (ProcessInfo==nil на fast-path,
  hijacked DNS) — только `/profiler/live/unattributed`.
- Android `ping`/`nslookup` резолвят через netd, мимо TUN — ядро их не видит; тест только
  через реальное приложение (`am start` URL в браузер).
- 10-сек лаг `dnsFail` реален (`C.DNSTimeout`), но НЕ причина «0 событий за 20 мин».

---

## Промежуточная заплатка (rc-линия, НЕ финал) — Kotlin reconnect-хук

Первой реакцией был Kotlin-фикс: reconnect-хук в `ProfilerHandler.disconnected()` +
флаг `profilerWanted` (гейт против зомби при Dart refcount→0). Незакоммиченный.
Отвергнут как решение (оставлен как временный unblock, если бы понадобился):
- чинил только обрыв всего коннекта, НЕ изолированную смерть DNS-стрима (та шла в
  `DnsHandler.onError`, который только логировал);
- НЕ снимал корневое неудобство — клиент по-прежнему держал ручную подписку + reconnect-логику;
- `profilerWanted` — костыль, порождённый тем, что reconnect тащили не на тот слой.

---

## Выбор архитектуры замены — рассмотренные пути

Ключевые факты, установленные по коду перед выбором:
1. connections и DNS — ОДИН gRPC-транспорт (`daemon.StartedServiceClient`), не два мира.
   «Legacy libbox command-протокол» — миф; `CommandClient` внутри давно на gRPC, а
   `Command*`-константы — клиентские селекторы `dispatchCommands`, не wire-типы.
2. `includeAnswers` ложится в `CommandClientOptions` как `StatusInterval` (образец
   `handleStatusStream`).
3. Все `handleXStream` при ошибке зовут общий `Disconnected()` → авто-reconnect; отдельного
   `OnError`-пути в мультиплексе нет.
4. Серверный gRPC-метод `SubscribeDNSQueries` и proto НЕ меняются при переносе — клиент
   лишь вызывает тот же метод из `dispatchCommands` вместо отдельной обёртки.
5. DNS всегда живёт вместе с профайлером: `_ccDnsSub`/`_ccConnSub` в `traffic_profiler.dart`
   подписываются/гасятся в паре; on-demand управляется Dart-refcount (§259), нативный
   `DnsQuerySubscription.Close()` как самостоятельная единица НЕ используется.

| Путь | Суть | Отвергнут потому что |
|---|---|---|
| **P2** Kotlin-заплатка | reconnect-хук + `profilerWanted` | добавляет груз вместо снятия; не закрывает изолированную смерть стрима |
| **P3** self-heal в libbox | go-рутина не умирает, сама переоткрывает stream | **вечная горутина** конфликтует с on-demand-моделью («профайлер поднимается/опускается») — решение владельца |
| **P1a** мультиплекс + опциональный интерфейс | `DNSQueryWriter` через type-assertion, lx-seam | **непроверенная gomobile-гипотеза** (двойной интерфейс через `.(DNSQueryWriter)`); «неоднообразно» (магическое `CommandDNS=100`, `default`-делегат) |
| **P1** мультиплекс однообразный ✅ | `CommandDNS` буквально как `CommandConnections` | **ВЫБРАН** |

### Почему P1 (однообразный)

DNS должен подниматься/опускаться ВМЕСТЕ с профайлером (требование владельца) — а это ровно
семантика мультиплекса: `connectProfilerClient()` → `Connect()` → `dispatchCommands`
поднимает все стримы; `disconnectProfiler()` → `Disconnect()` → `c.cancel()` рвёт `c.ctx` →
все стримы умирают вместе. DNS-горутина живёт на `c.ctx` — не вечная, не умирает раньше
времени, ровно как connections.

Однообразный вариант (не lx-seam-изолированный) выбран сознательно: DNS оформляется
идентично `CommandConnections` на всех уровнях (`CommandDNS` следующим в апстрим-`command.go`
iota, `case CommandDNS` в общем ряду `dispatchCommands`, `WriteDNSQuery` в апстрим-
`CommandClientHandler` рядом с `WriteConnectionEvents` — проверенный gomobile-механизм). Цена
— 3 апстрим-точки (merge-риск при ре-графе), принята как мелкая/редкая/тривиально-резолвимая
(на момент решения `HEAD..upstream/testing`=14 коммитов эти файлы не трогают; апстрим правит
`command.go` редко). Отвергнута изоляция в lx-seam (P1a): она давала DNS вид инородного
особого случая ради микро-выигрыша в merge-риске.

---

## Что осталось неизменным между v1 и v2

Ядро эмиссии не менялось (см. SPEC.md): пакет `common/dnstrack`, `HasSubscribers`-гейт,
форма `QueryEvent` (включая `rcode=-1` sentinel, `answers[]`, `SourceFailed`, DNSServer/
Outbound из rc.10), эмит-точки в `dns/client_log.go`, серверный gRPC-хендлер и proto.
Менялся только клиентский транспорт: как libbox-клиент подписывается на уже существующий
серверный стрим (standalone-метод → член мультиплекса) и как это потребляет Kotlin
(`DnsQueryHandler.onQuery` → `CommandClientHandler.writeDNSQuery`).

Хронология полей ядра (для справки): `failed`/`error` (полнота канала, провалы),
`answers[]` за флагом `includeAnswers` (CNAME-цепочка), ProcessInfo-фикс (searchProcessInfo
перед fast-path hijack — иначе DNS доходил до эмита с `ProcessInfo==nil`), DNSServer/
DNSServerType/Outbound (какой сервер резолвил + его канал).
