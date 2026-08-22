# gRPC API демона lxd — наблюдаемость для клиентских инструментов

> 🌐 English version: **[lxd-grpc-api.md](lxd-grpc-api.md)**.

Как клиент (лаунчер, профайлер, любой диагностический инструмент) читает живое
состояние демона `sing-box lxd` по gRPC.

**Контракт — это прото; документ его объясняет.** При расхождении прав
[`daemon/started_service.proto`](../daemon/started_service.proto). Ради того,
чего в прото нет, этот файл и существует: семантика дельт, какие поля заполнены
в каком событии, и ловушки, которые уже стоили нам инцидентов в бою.

Как подключиться к демону (mTLS, клиентские сертификаты, admin REST) —
[lxd-daemon.ru.md](lxd-daemon.ru.md).

> ⚠️ **Файл прото лежит в `daemon/`, а не в `lxd/`.** Демон отдаёт тот же
> `StartedService`, которым говорит Android-линия, — поэтому всё, что
> дорабатывается для мобильной наблюдаемости, достаётся серверу даром,
> и наоборот.

## Один контракт, два транспорта

Эта плоскость наблюдаемости **не привязана к lxd**. Один и тот же набор RPC и
сообщений выставлен через два носителя:

| Носитель | Где | Точка входа клиента |
|---|---|---|
| **gRPC** (`StartedService`) | демон `sing-box lxd` — десктоп / сервер | `daemon/started_service.proto` |
| **libbox `CommandClient`** | Android / iOS **AAR** (duplex-соединение, не gRPC) | `experimental/libbox/command_client_command_lx.go` |

lx-расширения (`SubscribeDNSQueries`, `GetRules`, `GetGroups`, `GetOutbounds`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `URLTestOutbound`,
`GetURLViaOutbound`, `GetChains`) реализованы **один раз в ядре** и доступны через любой из
носителей — семантика полей ниже идентична для обоих. То есть таблица полей здесь
описывает Android-AAR ровно так же, как удалённую lxd-машину; различаются лишь
обрамление на проводе и синтаксис вызова метода. Где правило транспорт-специфично —
это отмечено (например, gRPC-код `Unimplemented` не имеет эквивалента в libbox — там
отсутствие тега проявляется обычной ошибкой).

## Объём

`StartedService` — плоскость данных демона: всё о **работающем ядре** (статус,
соединения, DNS, группы, правила). `ManagedService` — плоскость управления
(остановка, reload, системный прокси), она достаточно мала, чтобы читать её
прямо в [`daemon/managed_service.proto`](../daemon/managed_service.proto).

Этот документ описывает подмножество, отвечающее за наблюдаемость.
В `StartedService` есть ещё десятки RPC для Tailscale, OpenVPN, OpenConnect
и USB-IP — к наблюдению за трафиком они отношения не имеют, здесь их нет,
смотри прото.

REST-ручки для профилирования **самого процесса демона** (память, pprof, его
собственный лог) живут отдельной плоскостью на admin-порту — `/admin/memory`,
`/admin/stats`, `/admin/logs`, `/admin/pprof/*`, плюс телеметрия хоста
в `/admin/host`. Описаны в [lxd-daemon.ru.md](lxd-daemon.ru.md) (SPEC 065, 068).

## Гейт по build-тегу

Девять RPC живут внутри блока `lx:begin lx_command` … `lx:end lx_command`
и существуют **только в сборке с `with_lx_command`**:

`URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `SubscribeDNSQueries`,
`GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetURLViaOutbound`, `GetChains`.

Против сборки без тега они возвращают `codes.Unimplemented`. Сгенерированные
клиентские заглушки методы всё равно содержат — гейт серверный, поэтому это
ошибка времени выполнения, а не компиляции. Клиенту, зависящему от DNS-плоскости,
стоит один раз проверить это на старте (дешевле всего `GetRules`) и явно
деградировать, а не выдавать `Unimplemented` на каждый вызов.

Всё остальное в этом документе — `SubscribeConnections`, `SubscribeStatus`,
`CloseConnection`, `GetStartedAt` — без гейта и есть в любой сборке.

## Правила, общие для всех стримов

**`interval` — в наносекундах.** Любой `Subscribe*Request.interval` — это счёт
`time.Duration`, по конвенции sing-box/libbox. Значения ниже пола в 200 мс
прижимаются и логируются предупреждением (`minSubscribeInterval` /
`clampSubscribeInterval` в [daemon/started_service.go](../daemon/started_service.go)).

> Это не гипотетика. Лаунчер однажды прислал `1000`, имея в виду миллисекунды;
> сервер прочитал 1 мкс, и тикер сжёг целое ядро CPU. Починено в `fca1d367e`
> добавлением клампа — но клиент, присылающий `1000`, по-прежнему получит
> 200 мс, а не задуманную секунду. Слать `int64(time.Second)`, никогда не голое
> число.

**Стримы ждут ядро.** Обработчики зовут `waitForStarted` до всего остального:
подписка во время старта ядра блокируется, а не падает. Клиент может открывать
свои стримы сразу после подключения.

**Нет ядра — нет учёта соединений.** Если у инстанса нет `trafficManager`,
`SubscribeConnections` вернёт `Unimplemented` — от случая с build-тегом это
отличается только контекстом, поэтому не трактуй `Unimplemented` как «не та
сборка», не проверив, запущено ли ядро вообще.

**Обрыв стрима означает, что твоё состояние протухло.** При ошибке `Recv`
ни одно запомненное соединение больше не подтверждено. Таблицу — сбросить.
Если её удержать, давно закрытые соединения будут показаны живыми.

## Плоскость соединений

### `SubscribeConnections(SubscribeConnectionsRequest) → stream ConnectionEvents`

**Это протокол дельт, а не повторяющийся снимок.** Самая частая ошибка —
принять каждый `ConnectionEvents` за текущий список соединений. Это не он —
это то, что изменилось.

```
ConnectionEvents { repeated ConnectionEvent events; bool reset; }
ConnectionEvent  { ConnectionEventType type; string id; Connection connection;
                   int64 uplinkDelta; int64 downlinkDelta; int64 closedAt; }
```

| Поле `ConnectionEvent` | Тип | Смысл |
|---|---|---|
| `type` | enum | `CONNECTION_EVENT_NEW` (0) \| `CONNECTION_EVENT_UPDATE` (1) \| `CONNECTION_EVENT_CLOSED` (2) |
| `id` | string | UUID соединения — ключ индексации, есть в каждом событии |
| `connection` | `Connection` | полный объект на `NEW`, **всегда `nil` на `UPDATE`**, может быть `nil` на `CLOSED` (см. ниже) |
| `uplinkDelta` / `downlinkDelta` | int64 | **байты с прошлого тика** (не rate), только на `UPDATE` |
| `closedAt` | int64 | Unix **миллисекунды**, только на `CLOSED` |
| `ConnectionEvents.reset` | bool | этот кадр заменяет всю твою таблицу (см. ниже) |

**Первый кадр несёт `reset = true`** и содержит всё текущее состояние пачкой
`NEW`-событий: каждое активное соединение плюс недавно закрытые (уже с
`closedAt`). `reset` приходит и посреди стрима, когда ядро перезапустилось
под клиентом — `apply`, Start/Stop. По `reset` очищай таблицу **до** применения
событий из того же кадра.

Дальнейшие кадры приходят по двум расписаниям: **немедленно**, когда ядро
открывает или закрывает соединение (сливаются пачкой, поэтому в одном кадре
может быть много событий), и **по тику интервала** — с дельтами трафика.

### Какие поля заполнены

| Тип события | `connection` | `uplinkDelta` / `downlinkDelta` | `closedAt` |
|---|---|---|---|
| `NEW` | **полный объект** | — | — |
| `UPDATE` | **всегда `nil`** | дельта с прошлого тика | — |
| `CLOSED` | обычно полный, **может быть `nil`** | — | заполнен |

**`UPDATE` никогда не несёт объект соединения.** Только `id` и две дельты.
Клиент, который читает `ev.GetConnection()` и пропускает событие при `nil`,
покажет соединения, у которых трафик никогда не набегает. Счётчики трафика
клиент обязан вести сам, прибавляя дельты к итогам из события `NEW`.

**У `CLOSED` соединение может быть `nil`**, когда метаданные уже недоступны.
`id` и `closedAt` есть всегда, поэтому удаляй по `id`, а не по вложенному
объекту.

**Соединение может закрыться так, что событие до тебя не дойдёт.** Если оно
исчезло из активного набора между тиками и не породило события подписки, сервер
синтезирует `CLOSED` на следующем тике — с `closedAt`, равным *текущему моменту*,
а не реальному времени закрытия, и с `connection`, заполненным только если оно
ещё было в кольце закрытых. Считай `closedAt` приблизительным.

**`UPDATE` с нулевой дельтой осмыслен.** Он помечает соединение, которое
*передавало* и перестало, — тот самый фронт, по которому клиент опускает
скорость строки в ноль, а не оставляет навсегда последнее ненулевое значение.
Простаивающие соединения, которые вообще не двигались, событий не порождают.

**Счётчики могут идти назад.** Идентификаторы соединений переиспользуются.
Увидев отрицательную дельту, сервер переустанавливает базу и шлёт нулевой
`UPDATE` вместо отрицательного; клиент отрицательной дельты не получит никогда,
но и считать итоги монотонными для уже виденного `id` не должен.

### `Connection`

| Поле | Тип | Смысл |
|---|---|---|
| `id` | string | UUID; ключ для `CloseConnection` и для твоей собственной таблицы |
| `inbound`, `inboundType` | string | какой inbound принял соединение |
| `network` | string | `tcp` / `udp` |
| `ipVersion` | int32 | 4 или 6 |
| `source`, `destination` | string | `host:port`; назначением может быть и IP, и имя хоста |
| `domain` | string | **сниффнутый** домен — пусто, если сниффинг не сработал |
| `protocol` | string | сниффнутый прикладной протокол (`http`, `tls`, `quic`, …) |
| `user` | string | пользователь inbound-авторизации, если она есть |
| `createdAt`, `closedAt` | int64 | Unix-**миллисекунды**; `closedAt` = 0, пока открыто |
| `uplink`, `downlink` | int64 | текущая скорость (байт/с) |
| `uplinkTotal`, `downlinkTotal` | int64 | байты, накопленные этим соединением |
| `rule` | string | сработавшее правило, отрисованное строкой — за структурой в `GetRules` |
| `outbound`, `outboundType` | string | итоговый outbound |
| `chainList` | repeated string | цепочка outbound'ов, изнутри наружу |
| `detourList` | repeated string | **lx**: хвост транспортного detour'а итогового outbound'а (SPEC 017) |
| `processInfo` | `ProcessInfo` | `processId` (uint32), `userId` (int32), `userName`, `processPath`, `packageNames` (repeated) |
| `fromOutbound` | string | выставлен, когда соединение породил сам outbound |

**`chainList` не включает detour по замыслу** — отсюда `detourList`, который
добавляет форк. Порядок: итоговый outbound → наружу. Для outbound'ов без
detour пусто. Профайлеру, показывающему «весь путь», нужны оба, склеенные.

**`domain` против `destination`.** Клиенту, которому нужно «что это за хост»,
следует брать `domain`, а при пустом — хостовую часть `destination`. Ровно так
делает лаунчер в `ProtoConnToClash`
(`singbox-launcher/internal/traffic/grpc_tracker.go`).

### Закрытие соединений

- `CloseConnection(CloseConnectionRequest{id})` — одно соединение, по UUID.
- `CloseAllConnections(Empty)` — все.

## DNS-плоскость

### `SubscribeDNSQueries(SubscribeDNSQueriesRequest) → stream DnsQueryEvent`

**Удалённому клиенту не нужен текстовый лог ядра ради DNS.** Этот стрим
(SPEC 018/035) — структурный эквивалент, и он несёт привязку, которой у лога
не было никогда. Для удалённых машин это принципиально: файл лога лежит на чужой
файловой системе, а Clash API `/connections` наружу вообще не смотрит.

У запроса одно поле: `includeAnswers` (bool). Ставь `true`, чтобы получать записи
ответа в `answers`.

| Поле | Тип | Смысл |
|---|---|---|
| `domain` | string | запрошенное имя |
| `queryType` | uint32 | код типа DNS-вопроса (1 = A, 28 = AAAA, 65 = HTTPS, …) |
| `rcode` | int32 | код ответа; **`-1`, когда ответа не было вовсе** |
| `ttl` | uint32 | TTL ответа, секунды |
| `source` | string | глагол резолвера: `exchanged` / `cached` / `optimistic` / `refreshed` / `failed` |
| `failed` | bool | true на таймаут / SERVFAIL / отказ — ошибки полноправны |
| `error` | string | детали ошибки, когда `failed` |
| `answers` | repeated `DnsAnswer` | `{name, type (uint32), rdata, ttl (uint32)}`, **в проводном порядке**; только при `includeAnswers` |
| `dnsServer` | string | какой транспорт (тег DNS-сервера) разрешил запрос |
| `dnsServerType` | string | тип этого сервера |
| `outbound` | repeated string | канал(ы), к которому привязан сервер; **пусто на cached/optimistic** |
| `processInfo` | `ProcessInfo` | привязка к приложению — пакет / uid |
| `dnsGroupPath` | repeated string | вложенность групп, изнутри наружу; пусто = группа не участвовала (SPEC 035) |
| `attempts` | repeated `DnsGroupAttempt` | хронология проб на момент ответа: `{server, serverType, outcome, rttMs (uint32)}` |
| `fanned` | bool | был задействован веер (rescue / election / parallel) |
| `survival` | bool | ответ пришёл от наименее грязного сервера, когда чистых не было |

Словарь `DnsGroupAttempt.outcome`: `answered` \| `timeout` \| `network_error` \|
`servfail` — где `answered` включает NXDOMAIN и пустые ответы (это валидные ответы,
не ошибки).

**CNAME-цепочки берутся из `answers`.** Это записи ответа в проводном порядке:
сначала CNAME-переходы, затем A/AAAA — пройди их и восстанови цепочку.
Отдельного поля CNAME нет, и оно не нужно. Лаунчер делает это в
`core/services/lxd_remote_transport.go` (см. `dnsTypeCNAME`).

**Пустой `outbound` — это состояние, а не баг.** Он означает, что запрос
вообще не покидал устройство: ответ из кеша либо оптимистичный. Точно так же
пустой `dnsGroupPath` означает, что запрос не шёл через DNS-группу. Сверься
с семантикой SPEC, прежде чем подозревать ядро.

**`attempts` — снимок, а не вся правда.** Отставшие участники веера, ответившие
уже после возврата ответа, отсутствуют там по замыслу; полная картина —
в `GetDNSGroups`.

## Вспомогательные RPC

| RPC | Зачем |
|---|---|
| `SubscribeStatus(interval) → stream Status` | память, горутины, `connectionsIn`/`connectionsOut`, скорость и итоги — шапка профайлера |
| `GetStartedAt → StartedAt` | время старта ядра, для uptime |
| `GetRules → RuleList` | **lx**: структурные правила — раскрывает `Connection.rule` |
| `GetGroups → Groups` / `SubscribeGroups` | **lx** (`GetGroups`) / апстрим (стрим) — состояние групп |
| `GetOutbounds → OutboundList` | **lx**: теги outbound'ов, чтобы раскрывать элементы цепочки |
| `GetPool(GetPoolRequest) → PoolList` | **lx**: состояние ротации балансируемой urltest-группы (SPEC 019) |
| `GetDNSGroups → DnsGroupList` | **lx**: полное состояние DNS-групп |
| `GetRunningConfig → RunningConfig` | **lx**: конфиг, на котором ядро реально работает |
| `URLTestOutbound → delay` | **lx**: проба одного узла |
| `GetURLViaOutbound → body` | **lx**: проба с возвратом тела ответа — «какой exit-IP даёт *этот* узел» (SPEC 058) |
| `GetChains → ChainList` | **lx**: состояние каждого outbound'а `chain` — позиции, разрешённый узел, звено (состояние/MTU/strip/rewrite), счётчики (SPEC 073) |
| `SubscribeLog → stream Log` | лог **ядра**, не демона — про демонский см. SPEC 065 |

`Status.trafficAvailable` отличает «нулевой трафик» от «учёт трафика
недоступен»; когда там `false`, нули рисовать не нужно.

### Справочник полей сообщений

Формы ответов вспомогательных RPC, для полноты.

**`Status`** (`SubscribeStatus`) — шапка профайлера:

| Поле | Тип | Смысл |
|---|---|---|
| `memory` | uint64 | резидентная память процесса ядра, байты |
| `goroutines` | int32 | число живых горутин |
| `connectionsIn` / `connectionsOut` | int32 | активные in / out соединения |
| `trafficAvailable` | bool | false = учёт выключен; **не рисуй байтовые поля нулями** |
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
| `Group.selected` | string | текущий узел — **лишь подсказка для `round_robin`** (читай `GetPool`) |
| `Group.isExpand` | bool | состояние разворота в UI |
| `Group.mode` | string | **lx**: `least_test` / `round_robin`; пусто для не-urltest групп (SPEC 019) |
| `Group.items` | repeated `GroupItem` | узлы-участники |
| `GroupItem.tag`, `GroupItem.type` | string | тег и тип узла |
| `GroupItem.urlTestTime` | int64 | Unix мс последней пробы |
| `GroupItem.urlTestDelay` | int32 | задержка последней пробы в **мс**; `0` = мёртв / не измерен |

**`PoolSlot`** (`GetPool` → `PoolList.slots`) — **lx**, SPEC 019:

| Поле | Тип | Смысл |
|---|---|---|
| `slot` | uint32 | фиксированный индекс слота в ротации |
| `tag` | string | узел, сейчас стоящий в этом слоте |
| `delay` | uint32 | результат последнего теста в мс; **`0` = мёртв / не измерен** (живой узел клампится к ≥ 1 на сервере) |

Не-`round_robin` группа возвращает **пустой список `slots`** — «пула тут нет», не
ошибка.

**`DnsGroupState`** / **`DnsGroupMember`** (`GetDNSGroups`) — **lx**, SPEC 035:

| Поле | Тип | Смысл |
|---|---|---|
| `DnsGroupState.tag`, `.mode`, `.current` | string | тег группы, режим, sticky-цель |
| `DnsGroupMember.tag`, `.serverType` | string | тег участника и тип сервера |
| `DnsGroupMember.clean` | bool | ноль живых ошибок |
| `DnsGroupMember.liveErrors` | uint32 | текущее число живых ошибок |
| `DnsGroupMember.lastErrorAgeMs` | int64 | возраст свежайшей живой ошибки; **`-1` = нет** |
| `DnsGroupMember.liveWins` | uint32 | записи живых побед (режим fastest) |
| `DnsGroupMember.current` | bool | это ли sticky-цель группы |
| `DnsGroupMember.lastRttMs` | uint32 | последняя успешная проба; `0` = не измерялась |

**`URLTestOutbound`** (**lx**) — запрос `{outboundTag, link, timeout}`, ответ
`{delay (uint32, мс), error}`. `timeout` — в **миллисекундах**; `0` = без доп.
дедлайна (ограничено только вызовом). При неудаче `delay` не задан, `error` заполнен.

**`GetURLViaOutbound`** (**lx**, SPEC 058) — диагностическая HTTP-проба через один
узел, возвращающая **тело ответа**, отвечает на «какой exit-IP / гео / warp-статус
даёт *этот* узел»:

| Поле запроса | Тип | Смысл |
|---|---|---|
| `outboundTag` | string | узел, через который пробить |
| `link` | string | URL для запроса |
| `timeout` | uint32 | **мс**; `0` = без доп. дедлайна |
| `maxBytes` | uint32 | лимит тела; `0` → дефолт **256 KiB**, жёсткий потолок **1 MiB** |
| `headers` | repeated `{key, value}` | доп. заголовки запроса |

| Поле ответа | Тип | Смысл |
|---|---|---|
| `httpStatus` | uint32 | HTTP-код |
| `body` | bytes | тело ответа (**bytes**, не string — произвольный endpoint не гарантирует UTF-8) |
| `truncated` | bool | тело упёрлось в `maxBytes` и обрезано |
| `contentType` | string | `Content-Type` ответа |
| `remoteAddr` | string | exit-адрес, с которого ушёл запрос |
| `elapsedMs` | uint32 | round-trip время |
| `error` | string | заполнен, когда запрос не завершился (неизвестный тег, dial/TLS-сбой, таймаут) |

**`RunningConfig`** (`GetRunningConfig`, **lx** SPEC 037): единственное `content`
(string) — каноническая сериализация опций, из которых ядро реально собралось
(post-override). **Не** байт-в-байт равна тексту профиля, что прислал клиент
(порядок полей, `omitempty`, `[]`→`null`); сравнивай семантически, не текстово.

## observability-api-lx

Что форк делает с этой плоскостью относительно апстримного sing-box.

### Добавлено

Девять RPC за `with_lx_command`, все отсутствуют в апстриме:

| RPC | Зачем | SPEC |
|---|---|---|
| `SubscribeDNSQueries` | структурный DNS-стрим; ошибки полноправны; заменяет разбор лога | [018](../SPECS/TASKS/018-DNS_QUERY_STREAM/SPEC.md), 035 |
| `GetRules` | таблица правил, чтобы раскрывать `Connection.rule` | 014/015 |
| `GetGroups`, `GetOutbounds` | разовое чтение там, где апстрим даёт только стримы | 014/015 |
| `GetPool` | состояние ротации балансируемой urltest-группы | [019](../SPECS/TASKS/019-URLTEST_MODE_STICKY/SPEC.md) |
| `GetDNSGroups` | состояние DNS-групп | 035 |
| `GetRunningConfig` | конфиг, реально действующий сейчас | — |
| `URLTestOutbound` | проба одного узла с возвратом задержки | 014/015 |
| `GetURLViaOutbound` | проба одного узла с возвратом тела ответа | [058](../SPECS/TASKS/058-GET_URL_VIA_OUTBOUND/SPEC.md) |
| `GetChains` | состояние каждого outbound'а `chain` | [073](../SPECS/TASKS/073-CHAIN_OUTBOUND/SPEC.md) |

### Расширено

Апстримные сообщения, в которые форк добавляет поля. Оба — обычные
proto-дополнения: апстримные клиенты их игнорируют, форковые получают больше.

| Сообщение | Поле | Зачем | SPEC |
|---|---|---|---|
| `Connection` | `detourList` | хвост транспортного detour'а, который `chainList` не несёт по замыслу | [017](../SPECS/TASKS/017-CONNECTION_DETOUR_CHAIN/SPEC.md) |
| `Group` | `mode` | `least_test` / `round_robin`; для не-urltest групп пусто, поэтому поле заодно отвечает «балансируется ли группа» без вызова гейтованного `GetPool` | [019](../SPECS/TASKS/019-URLTEST_MODE_STICKY/SPEC.md) v2 |

### Изменено

Тот же проводной контракт, другое поведение.

| Что | Изменение |
|---|---|
| `Subscribe*.interval` | приживается к полу в 200 мс с предупреждением, называющим единицу; апстрим брал что дадут, из-за чего клиент мог раскрутить CPU (`fca1d367e`) |
| `Group.selected` | при `mode: round_robin` единственного текущего узла нет — поле несёт последний узел, который случайно выбрал балансировщик. Считай это подсказкой; настоящее состояние ротации — в `GetPool` |

## Рецепт: профилирование удалённой машины

Удалённая lxd-машина отдаёт наружу **только** эту gRPC-плоскость. Её Clash API
недостижим, а её `sing-box.log` лежит на файловой системе, которой у тебя нет.
Всё ниже заменяет и то и другое.

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

Клиентские инварианты — каждый добыт дорогой ценой:

1. Держи таблицу по ключу `id`. `NEW` вставляет, `UPDATE` **прибавляет дельты
   к накопленным итогам** (объекта соединения в событии нет), `CLOSED` удаляет.
2. По `reset` — и по любой ошибке стрима — таблицу очищать.
3. Отличай «данных ещё нет» от «соединений нет». Пустая таблица до первого
   кадра — это не пустая машина; отрисовка её нулём роняет график в пол на
   каждом переподключении. Лаунчер отслеживает это флагом `live` в `ConnTracker`.
4. Интервалы слать как `int64(time.Second)`, а не `1000`.

Рабочий клиент ровно такой формы живёт в лаунчере:
`internal/traffic/grpc_tracker.go` (таблица),
`core/services/lxd_remote_transport.go` (удалённый стрим и разбор DNS).

## Источники

- [`daemon/started_service.proto`](../daemon/started_service.proto) — контракт
- [`daemon/started_service.go`](../daemon/started_service.go) — построение дельт:
  `SubscribeConnections`, `applyConnectionEvent`, `buildTrafficUpdates`
- [lxd-daemon.ru.md](lxd-daemon.ru.md) — транспорт, mTLS, admin REST
- [SPEC 065](../SPECS/TASKS/065-LXD_OBSERVABILITY_PLANE/SPEC.md) — профилирование
  самого процесса демона (REST, не gRPC)
