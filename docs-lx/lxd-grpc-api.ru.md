# gRPC API демона lxd — наблюдаемость для клиентских инструментов

> 🌐 English version: **[lxd-grpc-api.md](lxd-grpc-api.md)** · Фича: [OBSERVABILITY](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)

Как клиент (лаунчер, профайлер, любой диагностический инструмент) читает живое
состояние демона `sing-box lxd` по gRPC.

Контракт — это прото, документ его объясняет. При расхождении прав
[`daemon/started_service.proto`](../daemon/started_service.proto). Здесь описано
то, чего в прото нет: семантика дельт, какие поля заполнены в каком событии и
ловушки, которые уже приводили к инцидентам в продакшене.

Как подключиться к демону (mTLS, клиентские сертификаты, admin REST) —
[lxd-daemon.ru.md](lxd-daemon.ru.md).

> ⚠️ Файл прото лежит в `daemon/`, а не в `lxd/`. Демон отдаёт тот же
> `StartedService`, через который работает Android-линия. Поэтому всё, что
> дорабатывается для мобильной наблюдаемости, сразу достаётся серверу, и
> наоборот.

## Оглавление

- [Один контракт доступен через gRPC и libbox](#один-контракт-доступен-через-grpc-и-libbox)
- [Документ описывает только наблюдаемость](#документ-описывает-только-наблюдаемость)
- [RPC форка есть только в сборке с `with_lx_command`](#rpc-форка-есть-только-в-сборке-с-with_lx_command)
- [Правила, общие для всех стримов](#правила-общие-для-всех-стримов)
- [Плоскость соединений](#плоскость-соединений)
  - [`SubscribeConnections(SubscribeConnectionsRequest) → stream ConnectionEvents`](#subscribeconnectionssubscribeconnectionsrequest--stream-connectionevents)
  - [Набор полей зависит от типа события](#набор-полей-зависит-от-типа-события)
  - [`Connection`](#connection)
  - [Закрытие соединений](#закрытие-соединений)
- [DNS-плоскость](#dns-плоскость)
  - [`SubscribeDNSQueries(SubscribeDNSQueriesRequest) → stream DnsQueryEvent`](#subscribednsqueriessubscribednsqueriesrequest--stream-dnsqueryevent)
- [Вспомогательные RPC](#вспомогательные-rpc)
  - [Справочник полей сообщений](#справочник-полей-сообщений)
- [observability-api-lx](#observability-api-lx)
  - [Добавлено](#добавлено)
  - [Расширено](#расширено)
  - [Изменено](#изменено)
- [Удалённую машину профилируют только через gRPC](#удалённую-машину-профилируют-только-через-grpc)
- [Источники](#источники)

---

## Один контракт доступен через gRPC и libbox

Эта плоскость наблюдаемости не привязана к lxd. Один и тот же набор RPC и
сообщений выставлен через два носителя:

| Носитель | Где | Точка входа клиента |
|---|---|---|
| **gRPC** (`StartedService`) | демон `sing-box lxd` — десктоп / сервер | `daemon/started_service.proto` |
| **libbox `CommandClient`** | Android / iOS AAR (gRPC через локальный сокет) | `experimental/libbox/command_client_command_lx.go` |

lx-расширения (`SubscribeDNSQueries`, `GetRules`, `GetGroups`, `GetOutbounds`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetEffectiveConfig`, `GetOutboundConfig`,
`URLTestOutbound`, `GetURLViaOutbound`, `GetChainStatus`, `GetWireGuardStatus`,
`GetTailscaleStatus`)
реализованы один раз в ядре и доступны через оба носителя с одной и той же семантикой полей. Поэтому таблицы полей ниже описывают
Android-AAR так же, как удалённую lxd-машину. Различаются только обрамление на
проводе и синтаксис вызова метода. Правила, зависящие от транспорта, отмечены
отдельно. Так, без тега libbox получает тот же `codes.Unimplemented`, но
приложение видит его как обычную ошибку с текстом вида `get rules: rpc error:
code = Unimplemented desc = …`: отдельного кода у неё нет, распознать отказ можно
только по тексту.

## Документ описывает только наблюдаемость

`StartedService` — плоскость данных демона: всё о работающем ядре (статус,
соединения, DNS, группы, правила). `ManagedService` — плоскость управления
(остановка, reload, системный прокси). Она небольшая, её проще читать прямо в
[`daemon/managed_service.proto`](../daemon/managed_service.proto).

Этот документ описывает часть `StartedService`, отвечающую за наблюдаемость.
В `StartedService` есть ещё десятки RPC для Tailscale, OpenVPN, OpenConnect
и USB-IP. К наблюдению за трафиком они не относятся, здесь их нет — смотри прото.

REST-ручки для профилирования самого процесса демона (память, pprof, его
собственный лог) — отдельная плоскость на admin-порту: `/admin/memory`,
`/admin/stats`, `/admin/logs`, `/admin/pprof/*` и телеметрия хоста
в `/admin/host`. Они описаны в [lxd-daemon.ru.md](lxd-daemon.ru.md).

## RPC форка есть только в сборке с `with_lx_command`

Четырнадцать RPC живут внутри блока `lx:begin lx_command` … `lx:end lx_command`
и существуют только в сборке с `with_lx_command`:

`URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `SubscribeDNSQueries`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetEffectiveConfig`, `GetOutboundConfig`,
`GetURLViaOutbound`, `GetChainStatus`, `GetWireGuardStatus`, `GetTailscaleStatus`.

В том же блоке лежат управляющие RPC, которые этот документ не описывает:

- `SetChainPositionEnabled` — включение и выключение позиций цепочки `chain`
  на лету ([фича CHAIN](../SPECS/FEATURES/015-CHAIN/FEATURE.md));
- `SetEndpointEnabled` — ручное включение и выключение WG/AWG- и
  Tailscale-endpoint'а, см. [lx-energy.ru.md §12](lx-energy.ru.md#12-ручной-переключатель-выключает-endpoint-без-правки-конфига);
- `TailscaleLogin` — вход Tailscale-endpoint'а с новым auth key без перезапуска box;
- `SetImpair`, `ScheduleImpair`, `GetImpairStatus` — управление тестовым outbound'ом `impair` на
  лету, см. [lx-impair.ru.md](lx-impair.ru.md#управление-на-лету).

Против сборки без тега они возвращают `codes.Unimplemented`. Сгенерированные
клиентские заглушки методы всё равно содержат: гейт стоит на сервере, поэтому
ошибка возникает во время выполнения, а не при компиляции. Клиенту, зависящему от
DNS-плоскости, стоит один раз проверить это на старте (дешевле всего — `GetRules`)
и явно деградировать, а не получать `Unimplemented` на каждом вызове.

Всё остальное в этом документе — `SubscribeConnections`, `SubscribeStatus`,
`CloseConnection`, `GetStartedAt` — без гейта и есть в любой сборке.

## Правила, общие для всех стримов

**`interval` — в наносекундах.** Любой `Subscribe*Request.interval` — это счёт
`time.Duration`, по конвенции sing-box/libbox. Значения ниже пола в 200 мс
прижимаются и логируются предупреждением (`minSubscribeInterval` /
`clampSubscribeInterval` в [daemon/started_service.go](../daemon/started_service.go)).

> ⚠️ Клиент, который шлёт `1000` в расчёте на миллисекунды, получает 200 мс, а не
> секунду. Без клампа сервер читал такое значение как 1 мкс, и тикер занимал целое
> ядро CPU: так было с лаунчером, кламп добавлен в `fca1d367e`. Слать
> `int64(time.Second)`, никогда не голое число.

**Стримы ждут ядро.** Обработчики зовут `waitForStarted` до всего остального:
подписка во время старта ядра блокируется, а не падает. Клиент может открывать
свои стримы сразу после подключения.

**Нет ядра — нет учёта соединений.** Если у инстанса нет `trafficManager`,
`SubscribeConnections` вернёт `Unimplemented`. От случая с build-тегом это
отличается только контекстом. Поэтому не трактуй `Unimplemented` как «не та
сборка», пока не проверил, запущено ли ядро вообще.

**Обрыв стрима означает, что твоё состояние протухло.** При ошибке `Recv`
ни одно запомненное соединение больше не подтверждено, и таблицу нужно сбросить.
Если её удержать, давно закрытые соединения будут показаны живыми.

## Плоскость соединений

### `SubscribeConnections(SubscribeConnectionsRequest) → stream ConnectionEvents`

**Это протокол дельт, а не повторяющийся снимок.** Каждый `ConnectionEvents`
несёт то, что изменилось, а не текущий список соединений. Принять его за список —
самая частая ошибка.

```
ConnectionEvents { repeated ConnectionEvent events; bool reset; }
ConnectionEvent  { ConnectionEventType type; string id; Connection connection;
                   int64 uplinkDelta; int64 downlinkDelta; int64 closedAt; }
```

| Поле `ConnectionEvent` | Тип | Смысл |
|---|---|---|
| `type` | enum | `CONNECTION_EVENT_NEW` (0) \| `CONNECTION_EVENT_UPDATE` (1) \| `CONNECTION_EVENT_CLOSED` (2) |
| `id` | string | UUID соединения — ключ индексации, есть в каждом событии |
| `connection` | `Connection` | полный объект на `NEW`, всегда `nil` на `UPDATE`, может быть `nil` на `CLOSED` ([когда](#набор-полей-зависит-от-типа-события)) |
| `uplinkDelta` / `downlinkDelta` | int64 | байты с прошлого тика (не скорость), только на `UPDATE` |
| `closedAt` | int64 | Unix-миллисекунды, только на `CLOSED` |
| `ConnectionEvents.reset` | bool | этот кадр заменяет всю твою таблицу |

**Первый кадр несёт `reset = true`** и содержит всё текущее состояние пачкой
`NEW`-событий: каждое активное соединение и недавно закрытые (уже с `closedAt`).
`reset` приходит и посреди стрима, когда ядро перезапустилось под клиентом:
`apply`, Start/Stop. По `reset` очищай таблицу до применения событий из того же
кадра.

Дальнейшие кадры приходят по двум расписаниям. Когда ядро открывает или
закрывает соединение, кадр уходит немедленно; такие события сливаются пачкой,
поэтому в одном кадре их может быть много. Дельты трафика приходят по тику
интервала.

### Набор полей зависит от типа события

| Тип события | `connection` | `uplinkDelta` / `downlinkDelta` | `closedAt` |
|---|---|---|---|
| `NEW` | полный объект | — | — |
| `UPDATE` | всегда `nil` | дельта с прошлого тика | — |
| `CLOSED` | обычно полный, может быть `nil` | — | заполнен |

**`UPDATE` никогда не несёт объект соединения.** В нём только `id` и две дельты.
Клиент, который читает `ev.GetConnection()` и пропускает событие при `nil`,
покажет соединения, у которых трафик никогда не набегает. Счётчики трафика
клиент обязан вести сам: прибавлять дельты к итогам из события `NEW`.

**У `CLOSED` соединение может быть `nil`**, когда метаданные уже недоступны.
`id` и `closedAt` есть всегда, поэтому удаляй по `id`, а не по вложенному
объекту.

**Соединение может закрыться так, что событие до тебя не дойдёт.** Бывает, что
соединение исчезло из активного набора между тиками и не породило события
подписки. Тогда сервер синтезирует `CLOSED` на следующем тике. `closedAt` в нём
равен текущему моменту, а не реальному времени закрытия. `connection` заполнен,
только если соединение ещё было в кольце закрытых. Считай `closedAt`
приблизительным.

**`UPDATE` с нулевой дельтой осмыслен.** Он помечает соединение, которое
передавало данные и перестало. По этому фронту клиент опускает скорость строки в
ноль, а не оставляет навсегда последнее ненулевое значение. Простаивающие
соединения, которые вообще не двигались, событий не порождают.

**Счётчики могут идти назад.** Идентификаторы соединений переиспользуются.
Увидев отрицательную дельту, сервер переустанавливает базу и шлёт нулевой
`UPDATE` вместо отрицательного. Отрицательной дельты клиент не получит никогда,
но считать итоги монотонными для уже виденного `id` не должен.

### `Connection`

| Поле | Тип | Смысл |
|---|---|---|
| `id` | string | UUID; ключ для `CloseConnection` и для твоей собственной таблицы |
| `inbound`, `inboundType` | string | какой inbound принял соединение |
| `network` | string | `tcp` / `udp` |
| `ipVersion` | int32 | 4 или 6 |
| `source`, `destination` | string | `host:port`; назначением может быть и IP, и имя хоста |
| `domain` | string | сниффнутый домен; пусто, если сниффинг не сработал |
| `protocol` | string | сниффнутый прикладной протокол (`http`, `tls`, `quic`, …) |
| `user` | string | пользователь inbound-авторизации, если она есть |
| `createdAt`, `closedAt` | int64 | Unix-миллисекунды; `closedAt` = 0, пока открыто |
| `uplink`, `downlink` | int64 | текущая скорость (байт/с) |
| `uplinkTotal`, `downlinkTotal` | int64 | байты, накопленные этим соединением |
| `rule` | string | сработавшее правило, отрисованное строкой; структура — в `GetRules` |
| `outbound`, `outboundType` | string | итоговый outbound |
| `chainList` | repeated string | цепочка outbound'ов, изнутри наружу |
| `detourList` | repeated string | **lx**: хвост транспортного detour'а итогового outbound'а |
| `processInfo` | `ProcessInfo` | `processId` (uint32), `userId` (int32), `userName`, `processPath`, `packageNames` (repeated) |
| `fromOutbound` | string | выставлен, когда соединение породил сам outbound |

**`chainList` не включает detour по замыслу**, поэтому форк добавляет
`detourList`. Порядок: итоговый outbound → наружу. Для outbound'ов без detour
поле пустое. Профайлеру, который показывает весь путь, нужны оба списка, склеенные.

**`domain` против `destination`.** Клиенту, которому нужно знать хост, следует
брать `domain`, а при пустом — хостовую часть `destination`. Так делает лаунчер
в `ProtoConnToClash` (`singbox-launcher/internal/traffic/grpc_tracker.go`).

### Закрытие соединений

- `CloseConnection(CloseConnectionRequest{id})` — одно соединение, по UUID.
- `CloseAllConnections(Empty)` — все.

## DNS-плоскость

### `SubscribeDNSQueries(SubscribeDNSQueriesRequest) → stream DnsQueryEvent`

**Удалённому клиенту не нужен текстовый лог ядра ради DNS.** Этот стрим —
структурный эквивалент лога, и он несёт привязку, которой в логе нет. На удалённой
машине файл лога лежит на чужой файловой системе, а Clash API `/connections`
наружу не смотрит.

У запроса одно поле: `includeAnswers` (bool). Ставь `true`, чтобы получать записи
ответа в `answers`.

| Поле | Тип | Смысл |
|---|---|---|
| `domain` | string | запрошенное имя |
| `queryType` | uint32 | код типа DNS-вопроса (1 = A, 28 = AAAA, 65 = HTTPS, …) |
| `rcode` | int32 | код ответа; `-1`, когда ответа не было вовсе |
| `ttl` | uint32 | TTL ответа, секунды |
| `source` | string | глагол резолвера: `exchanged` / `cached` / `optimistic` / `refreshed` / `failed` |
| `failed` | bool | true на таймаут / SERVFAIL / отказ — ошибки полноправны |
| `error` | string | детали ошибки, когда `failed` |
| `answers` | repeated `DnsAnswer` | `{name, type (uint32), rdata, ttl (uint32)}` в проводном порядке; только при `includeAnswers` |
| `dnsServer` | string | какой транспорт (тег DNS-сервера) разрешил запрос |
| `dnsServerType` | string | тип этого сервера |
| `outbound` | repeated string | канал(ы), к которому привязан сервер; пусто на cached/optimistic |
| `processInfo` | `ProcessInfo` | привязка к приложению — пакет / uid |
| `dnsGroupPath` | repeated string | вложенность групп, изнутри наружу; пусто = группа не участвовала |
| `attempts` | repeated `DnsGroupAttempt` | хронология проб на момент ответа: `{server, serverType, outcome, rttMs (uint32)}` |
| `fanned` | bool | был задействован веер (rescue / election / parallel) |
| `survival` | bool | ответ пришёл от наименее грязного сервера, когда чистых не было |

Словарь `DnsGroupAttempt.outcome`: `answered` \| `timeout` \| `network_error` \|
`servfail`. `answered` включает NXDOMAIN и пустые ответы: это валидные ответы,
а не ошибки.

**CNAME-цепочки берутся из `answers`.** Записи ответа идут в проводном порядке:
сначала CNAME-переходы, затем A/AAAA. Пройди их по порядку, и цепочка
восстановится. Отдельного поля CNAME нет, и оно не нужно. Лаунчер делает это в
`core/services/lxd_remote_transport.go` (см. `dnsTypeCNAME`).

**Пустой `outbound` — это состояние, а не баг.** Он означает, что запрос не
покидал устройство: ответ из кеша либо оптимистичный. Пустой `dnsGroupPath` так
же означает, что запрос не шёл через DNS-группу. Прежде чем подозревать ядро,
сверься с семантикой SPEC.

**`attempts` — снимок, а не вся правда.** В нём намеренно нет участников веера,
которые ответили уже после возврата ответа. Полная картина — в `GetDNSGroups`.

## Вспомогательные RPC

| RPC | Зачем |
|---|---|
| `SubscribeStatus(interval) → stream Status` | память, горутины, `connectionsIn`/`connectionsOut`, скорость и итоги — шапка профайлера |
| `GetStartedAt → StartedAt` | время старта ядра, для uptime |
| `GetRules → RuleList` | **lx**: структурные правила — раскрывает `Connection.rule` |
| `GetGroups → Groups` / `SubscribeGroups` | **lx** (`GetGroups`) / апстрим (стрим) — состояние групп |
| `GetOutbounds → OutboundList` | **lx**: теги outbound'ов, чтобы раскрывать элементы цепочки; состояние WG/AWG (`endpointState`); у Tailscale `up`/`disabled`/`down` |
| `GetWireGuardStatus(tag) → WireGuardEndpointStatus` | **lx**: один WG/AWG-endpoint — состояние устройства и по каждому пиру последний хендшейк, адрес, трафик |
| `GetTailscaleStatus(endpointTag) → TailscaleEndpointStatus` | **lx**: один Tailscale-endpoint, свежий снимок: путь к каждому пиру (direct / peer relay / DERP), хендшейк, предупреждения бэкенда |
| `SubscribeTaildropInbox(endpointTag) → stream TaildropInbox` | апстрим; **lx**: у полученного файла `senderID` — stable node ID отправителя, пустой для файлов, лежавших до перезапуска ядра |
| `GetPool(GetPoolRequest) → PoolList` | **lx**: состояние ротации балансируемой urltest-группы |
| `GetDNSGroups → DnsGroupList` | **lx**: полное состояние DNS-групп |
| `GetRunningConfig → RunningConfig` | **lx**: конфиг, на котором работает ядро |
| `GetEffectiveConfig → RunningConfig` | **lx**: тот же конфиг после lx-предобработки — хопы цепочек, переписанный `detour`, выровненный MTU, развёрнутый `lx.detour` |
| `GetOutboundConfig(tag) → OutboundConfig` | **lx**: один узел JSON-объектом `{tag, original, effective, changes}`; неизвестный тег — `NotFound` |
| `URLTestOutbound → delay` | **lx**: проба одного узла |
| `GetURLViaOutbound → body` | **lx**: проба с возвратом тела ответа — какой exit-IP даёт этот узел |
| `GetChainStatus(tag) → ChainState` | **lx**: состояние одного outbound'а `chain` — по позициям разрешённый узел, `transparent`, `disabled`, ошибки; счётчики дозвонов и ошибок |
| `SubscribeLog → stream Log` | лог ядра, не демона; лог демона отдаёт admin REST ([lxd-daemon.ru.md](lxd-daemon.ru.md)) |

`Status.trafficAvailable` отличает «нулевой трафик» от «учёт трафика
недоступен». Когда там `false`, нули рисовать не нужно.

### Справочник полей сообщений

Формы ответов вспомогательных RPC.

**`Status`** (`SubscribeStatus`) — шапка профайлера:

| Поле | Тип | Смысл |
|---|---|---|
| `memory` | uint64 | резидентная память процесса ядра, байты |
| `goroutines` | int32 | число живых горутин |
| `connectionsIn` / `connectionsOut` | int32 | активные in / out соединения |
| `trafficAvailable` | bool | false = учёт выключен; байтовые поля нулями не рисовать |
| `uplink` / `downlink` | int64 | текущая скорость, байт/с |
| `uplinkTotal` / `downlinkTotal` | int64 | байты с момента старта ядра |

**`ServiceStatus`** (`SubscribeServiceStatus`): enum `status`
`IDLE|STARTING|STARTED|STOPPING|FATAL` плюс `errorMessage` (заполнен на `FATAL`).
**`Version`** (`GetVersion`): `version` (string), `apiVersion` (int32).
**`StartedAt`** (`GetStartedAt`): `startedAt` (int64, Unix мс).

**`Group`** / **`GroupItem`** (`GetGroups` / `SubscribeGroups`):

| Поле | Тип | Смысл |
|---|---|---|
| `Group.tag`, `Group.type` | string | тег группы и её вид (`selector` / `urltest`) |
| `Group.selectable` | bool | применим ли `SelectOutbound` |
| `Group.selected` | string | текущий узел; для `round_robin` — только подсказка, ротацию читай в `GetPool` |
| `Group.isExpand` | bool | состояние разворота в UI |
| `Group.mode` | string | **lx**: `least_test` / `round_robin`; пусто для не-urltest групп |
| `Group.items` | repeated `GroupItem` | узлы-участники |
| `GroupItem.tag`, `GroupItem.type` | string | тег и тип узла |
| `GroupItem.urlTestTime` | int64 | Unix мс последней пробы |
| `GroupItem.urlTestDelay` | int32 | задержка последней пробы в мс; `0` = мёртв / не измерен |

**`PoolSlot`** (`GetPool` → `PoolList.slots`) — **lx**:

| Поле | Тип | Смысл |
|---|---|---|
| `slot` | uint32 | фиксированный индекс слота в ротации |
| `tag` | string | узел, сейчас стоящий в этом слоте |
| `delay` | uint32 | результат последнего теста в мс; `0` = мёртв / не измерен (живой узел сервер прижимает к ≥ 1) |

Не-`round_robin` группа возвращает пустой список `slots`: «пула тут нет», а не
ошибка.

**`DnsGroupState`** / **`DnsGroupMember`** (`GetDNSGroups`) — **lx**:

| Поле | Тип | Смысл |
|---|---|---|
| `DnsGroupState.tag`, `.mode`, `.current` | string | тег группы, режим, sticky-цель |
| `DnsGroupMember.tag`, `.serverType` | string | тег участника и тип сервера |
| `DnsGroupMember.clean` | bool | ноль живых ошибок |
| `DnsGroupMember.liveErrors` | uint32 | текущее число живых ошибок |
| `DnsGroupMember.lastErrorAgeMs` | int64 | возраст свежайшей живой ошибки; `-1` = нет |
| `DnsGroupMember.liveWins` | uint32 | записи живых побед (режим fastest) |
| `DnsGroupMember.current` | bool | это ли sticky-цель группы |
| `DnsGroupMember.lastRttMs` | uint32 | последняя успешная проба; `0` = не измерялась |

**`URLTestOutbound`** (**lx**) — запрос `{outboundTag, link, timeout}`, ответ
`{delay (uint32, мс), error}`. `timeout` — в миллисекундах; `0` = без доп.
дедлайна (ограничено только вызовом). При неудаче `delay` не задан, `error` заполнен.

**`GetURLViaOutbound`** (**lx**) — диагностическая HTTP-проба через один узел.
Она возвращает тело ответа и отвечает на вопрос, какой exit-IP, гео или
warp-статус даёт этот узел:

| Поле запроса | Тип | Смысл |
|---|---|---|
| `outboundTag` | string | узел, через который пробить |
| `link` | string | URL для запроса |
| `timeout` | uint32 | мс; `0` = без доп. дедлайна |
| `maxBytes` | uint32 | лимит тела; `0` → дефолт 256 KiB, жёсткий потолок 1 MiB |
| `headers` | repeated `{key, value}` | доп. заголовки запроса |

| Поле ответа | Тип | Смысл |
|---|---|---|
| `httpStatus` | uint32 | HTTP-код |
| `body` | bytes | тело ответа; bytes, а не string, потому что произвольный endpoint не гарантирует UTF-8 |
| `truncated` | bool | тело упёрлось в `maxBytes` и обрезано |
| `contentType` | string | `Content-Type` ответа |
| `remoteAddr` | string | exit-адрес, с которого ушёл запрос |
| `elapsedMs` | uint32 | round-trip время |
| `error` | string | заполнен, когда запрос не завершился (неизвестный тег, dial/TLS-сбой, таймаут) |

**`RunningConfig`** (`GetRunningConfig`, **lx**): единственное поле `content`
(string) — каноническая сериализация опций, из которых ядро собралось
(post-override). Она не совпадает байт-в-байт с текстом профиля, который прислал
клиент: отличаются порядок полей, `omitempty`, `[]`→`null`. Сравнивай
семантически, а не текстово. `GetEffectiveConfig` отдаёт то же сообщение с
конфигом после lx-предобработки, в той же каноничной форме.

**`OutboundConfig`** (`GetOutboundConfig`, **lx**): единственное поле `content`
(string) — JSON-объект `{tag, original, effective, changes}`. `original` — узел
как в `GetRunningConfig`, `effective` — как в `GetEffectiveConfig`; у хопа цепочки
есть только `effective`. `changes` — список `{field, from, to, by}` в порядке
применения, применённый к `original`, он даёт `effective`; поля — в
[lx-observability.ru.md](lx-observability.ru.md).

## observability-api-lx

Что форк делает с этой плоскостью относительно апстримного sing-box.

### Добавлено

Четырнадцать RPC за `with_lx_command`, все отсутствуют в апстриме:

| RPC | Зачем |
|---|---|
| `SubscribeDNSQueries` | структурный DNS-стрим; ошибки полноправны; заменяет разбор лога |
| `GetRules` | таблица правил, чтобы раскрывать `Connection.rule` |
| `GetGroups`, `GetOutbounds` | разовое чтение там, где апстрим даёт только стримы |
| `GetWireGuardStatus`, `GetTailscaleStatus` | статус одного endpoint'а по запросу; `GetOutbounds` остаётся списком узлов |
| `GetPool` | состояние ротации балансируемой urltest-группы |
| `GetDNSGroups` | состояние DNS-групп |
| `GetRunningConfig` | действующий конфиг |
| `GetEffectiveConfig`, `GetOutboundConfig` | конфиг как собран после lx-предобработки и что изменилось в каждом узле |
| `URLTestOutbound` | проба одного узла с возвратом задержки |
| `GetURLViaOutbound` | проба одного узла с возвратом тела ответа |
| `GetChainStatus` | состояние одного outbound'а `chain` по тегу |

### Расширено

Апстримные сообщения, в которые форк добавляет поля. Оба поля — обычные
proto-дополнения: апстримные клиенты их игнорируют, форковые получают больше.

| Сообщение | Поле | Зачем |
|---|---|---|
| `Connection` | `detourList` | хвост транспортного detour'а, который `chainList` не несёт по замыслу |
| `Group` | `mode` | `least_test` / `round_robin`; для не-urltest групп пусто, поэтому поле заодно показывает, балансируется ли группа, без вызова гейтованного `GetPool` |

### Изменено

Тот же проводной контракт, другое поведение.

| Что | Изменение |
|---|---|
| `Subscribe*.interval` | прижимается к полу в 200 мс с предупреждением, в котором названа единица. Апстрим брал что дадут, из-за чего клиент мог раскрутить CPU (`fca1d367e`) |
| `Group.selected` | при `mode: round_robin` единственного текущего узла нет. Поле несёт последний узел, который случайно выбрал балансировщик; считай это подсказкой, а состояние ротации читай в `GetPool` |

## Удалённую машину профилируют только через gRPC

Удалённая lxd-машина отдаёт наружу только эту gRPC-плоскость. Её Clash API
недостижим, а `sing-box.log` лежит на файловой системе, к которой у тебя нет
доступа. Вызовы ниже заменяют и то, и другое.

```
один раз на старте:
  GetRules            → таблица правил, чтобы раскрывать Connection.rule
  GetOutbounds        → таблица тегов, чтобы раскрывать цепочки
  GetStartedAt        → база отсчёта uptime

стримы, держатся открытыми:
  SubscribeConnections{interval: int64(time.Second)}   → таблица соединений
  SubscribeDNSQueries{includeAnswers: true}            → DNS + CNAME-цепочки
  SubscribeStatus{interval: int64(time.Second)}        → итоги, горутины, память

по требованию:
  CloseConnection{id}                                  → убить одно соединение
  GetURLViaOutbound{outboundTag, link}                 → что видит этот узел
```

Четыре инварианта клиента; нарушение каждого уже приводило к ошибкам:

1. Держи таблицу по ключу `id`. `NEW` вставляет, `UPDATE` прибавляет дельты
   к накопленным итогам (объекта соединения в событии нет), `CLOSED` удаляет.
2. По `reset` и по любой ошибке стрима таблицу очищать.
3. Отличай «данных ещё нет» от «соединений нет». Пустая таблица до первого
   кадра не означает пустую машину. Если рисовать её нулём, график падает в ноль
   на каждом переподключении. Лаунчер отслеживает это флагом `live` в `ConnTracker`.
4. Интервалы слать как `int64(time.Second)`, а не `1000`.

Рабочий клиент такой формы есть в лаунчере:
`internal/traffic/grpc_tracker.go` (таблица),
`core/services/lxd_remote_transport.go` (удалённый стрим и разбор DNS).

## Источники

- [`daemon/started_service.proto`](../daemon/started_service.proto) — контракт
- [`daemon/started_service.go`](../daemon/started_service.go) — построение дельт:
  `SubscribeConnections`, `applyConnectionEvent`, `buildTrafficUpdates`
- [lxd-daemon.ru.md](lxd-daemon.ru.md) — транспорт, mTLS, admin REST
- [SPEC 065](../SPECS/TASKS/065-LXD_OBSERVABILITY_PLANE/SPEC.md) — профилирование
  самого процесса демона (REST, не gRPC)
