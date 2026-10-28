# PLAN 139 — StartConnectionTest

> 🌐 English version: **[PLAN.md](PLAN.md)** — основной план; этот файл — русская копия для ознакомления.

Класс §3.6 «мост» (шов в `.proto` под `// lx:`, handler за `with_lx_command`,
stub-двойник, обёртка в `CommandClient`) плюс **трасса**: держатель стадий
в ctx и однострочные вставки в точках конвейера, которые пишут в него,
только если он есть. Образцы: `GetURLViaOutbound` (058) — модель ошибок,
кламп тела, HTTP-клиент на вызов; `dnstrack.QueryTrace` (035) — держатель
в ctx предка, write-once атрибуция, снапшот при эмите. Мост — часть
по §3.6; трасса — lx-логика по §3.2–3.3, цена мержа и релизы — SPEC §9–10.

## 1. Держатель трассы — `common/lxtrace`

Новый пакет, пара файлов по тегу:

- `trace_lx.go` (`with_lx_command`) — реализация;
- `trace_stub.go` (`!with_lx_command`) — те же функции пустыми телами,
  чтобы вставки в апстрим-файлы компилировались в любой сборке и
  без тега не стоили ничего.

API (эскиз):

```go
type Trace struct { ... }                         // стадии, счётчик id, эмиттер в поток, флаги запроса
func With(ctx context.Context, t *Trace) context.Context
func Begin(ctx context.Context, kind, subject string) (context.Context, *Stage) // nil без трассы
func (s *Stage) Attr(key, value string)           // nil-safe
func (s *Stage) End(err error)                    // OK / FAILED; nil-safe
func (s *Stage) Mark(result Result)               // REUSED / NO_ACK / NOT_EVALUATED / SKIPPED
func Fresh(ctx context.Context) bool              // запрос с fresh=true
func IsProbe(ctx context.Context) bool            // соединение проверки — для групп (§5.7 SPEC)
func Routed(ctx context.Context, metadata *adapter.InboundContext, rule adapter.Rule, outbound adapter.Outbound) (stop bool) // стадия route; true = DEPTH_ROUTE, вызывающий закрывает
func (s *Stage) LostRace(winner *Stage)           // SKIPPED + lost_race=<id победителя> (§5.13 SPEC)
```

- **Потолок стадий:** 512 записанных стадий на трассу (§5.15 SPEC); сверх
  него `Begin` возвращает nil-стадию и увеличивает счётчик, который
  выставляет `stagesTruncated` в итоге.

- `Begin` кладёт новую стадию в ctx как текущего родителя: всё, что
  начнётся в дочернем ctx, станет её потомком. Так вложенность резолва
  (§4 SPEC) получается из обычной передачи ctx, без знания уровней.
- **Цикл резолва:** `lookup`-стадия при `Begin` проходит предков; та же
  пара «резолвер, домен» среди предков — `FAILED` с атрибутом `cycle_with`
  и немедленный возврат ошибки вызывающему.
- Эмит в поток — при `Begin` (RUNNING) и `End`; канал в handler
  с буфером, переполнение не блокирует конвейер (стадия дописывается
  в итоговый снапшот).
- Без трассы в ctx каждая функция — одна проверка `ctx.Value` и возврат.

## 2. Handler

`daemon/started_service_conntest_lx.go` + stub. Шаги:

1. **Валидация** (ошибки §3.2 SPEC): URL, тег узла в обоих менеджерах
   (как 058), тег inbound, сочетания `depth`/`appDNS`/`network`, лимит
   одновременных проверок (семафор на 4).
2. **ctx проверки:** per-call ctx стрима + общий таймаут (дефолт 15 с,
   потолок 60 с) + `lxtrace.With`.
3. **Метаданные источника** → `adapter.InboundContext`:
   - `Inbound` = тег из запроса; `InboundType` = маркер
     `lx-connection-test` — так соединение отличимо в списке (§5.8),
     а правила `inbound` матчат по тегу и не задеты;
   - `ProcessInfo` из `packageName`/`uid`/`processPath`. UID по пакету —
     через `adapter.NetworkManager.PackageManager()`, который есть только
     при inbound `tun` в платформенной сборке; иначе `uid` остаётся 0
     (SPEC §7). Роутер ищет процесс только при
     пустом `ProcessInfo` (`route/process_cache.go:39`) — заранее
     заполненный не перезаписывается, вставка не нужна;
   - `Source` из `sourceIP` (порт — эфемерный), `SourceMACAddress`
     из `sourceMAC`, `User` из `authUser`.
4. **appDNS:** для A, AAAA и HTTPS — `dnsRouter.Exchange` с ctx,
   расширенным метаданными источника, как это делает hijack-путь
   (`route/dns.go:20`, `dnsOutbound.HandleStreamDNSRequest`).
   Каждому запросу — стадия `app_dns`.
5. **Через роутер:** `net.Pipe()`. Серверная сторона уходит
   в `router.RouteConnectionEx(ctx, server, metadata, onClose)` с
   назначением `connectIP` / ответ `app_dns` / домен URL; ошибка
   из `onClose` — отказ маршрута или dial (в том числе `reject`
   и «missing fakeip record», которые до трекера не доходят).
   Клиентская сторона — транспорт HTTP-клиента проверки.
   **Через узел:** `DialContext` узла, как в 058.
   Стадия `dial` первого хопа открывается вокруг `DialContext` /
   `DialSerialNetwork` outbound'а в `route/conn.go` (`NewConnection`,
   `NewPacketConnection`); вложенные хопы — в `DetourDialer.DialContext`.
   `DEPTH_CONNECT` завершает проверку по концу `dial` первого хопа.
   Закрывать `dial` из обёртки conn трекера отвергнуто: обёртка видит
   только первые байты с удалённой стороны, и для протоколов с одним
   заголовком (VLESS, Trojan) так и не узнаёт, что туннель поднят.
6. **HTTP-клиент проверки** — как в 058 (клиент на вызов, certificate
   store на Android, `DisableKeepAlives`, кламп тела) плюс
   `httptrace.ClientTrace`: `TLSHandshakeStart/Done` → `target_tls`,
   `GotFirstResponseByte` → `first_byte`, ответ → `http`.
7. **Итог:** корень (§4 ниже), выводы (§5), HTTP-поля, закрытие всех
   соединений проверки.

UDP (`network = "udp"`, только `DEPTH_ROUTE`): `RoutePacketConnectionEx`
с пакетным пайпом без полезной нагрузки. Sniff не срабатывает — правила
по `protocol` попадают в «не вычислены».

## 3. Точки съёма

### 3.1 Без правок апстрим-файлов

| Стадия | Где | Как |
|--------|-----|-----|

| `group` для urltest / `round_robin` / failover | lx-файлы `protocol/group/urltest_*_lx.go` | выбор и слот пишутся в трассу; при `IsProbe` — без штрафов, истории, деградации и закреплений |
| `chain` | `protocol/chain` (наш пакет) | позиции и звенья |
| `transport` XHTTP | `transport/v2rayxhttp` (наш пакет) | метод, путь, режим, статус |
| `auth` / `quic` MASQUE, TrustTunnel | `protocol/masque`, `protocol/trusttunnel` (наши пакеты) | статус CONNECT |
| `wake`, `wg_handshake` | lx-код endpoint'а WG/AWG (SPEC 020, 114) | подъём из сна; хендшейк — по `LastHandshake` пира до и после |
| `dns` (сервер, группа, пробы) | `dns/client_log.go` (lx) | тот же сбор, что для `QueryEvent`, но в трассу и без гейта подписчиков |
| `proxy_header` | lx-код handler'а | синтезируется по типу узла: для VLESS/VMess/Trojan/Shadowsocks после `tls`/`transport` — `NO_ACK` |
| `target_tls`, `first_byte`, `http` | handler | `httptrace` |
| Пакет исключён из VPN | handler | опции TUN из `GetRunningConfig`-снапшота |
| Состояние fakeip | lx-файл в `dns/transport/fakeip` | диапазон, текущий адрес, счётчик оборотов (вставка 3.2) |

### 3.2 Вставки `// lx:` в апстрим-файлы

Каждая — вызов `lxtrace`, логика в lx-файлах.

| Файл | Вставка | Зачем |
|------|---------|-------|
| `common/dialer/default.go` | `DialContext` / `ListenPacket`: стадия `tcp_connect` / `udp`, обёртка conn для первой записи и первого чтения | время соединения; «обрыв сразу после ClientHello» |
| `common/dialer/detour.go` | стадия `detour` (тег, домен или адрес) | ветвление дерева на detour |
| `common/dialer/resolve.go` | стадия `lookup` (`domain_resolver` / `default_domain_resolver`, сервер), проверка цикла | резолв адреса сервера узла |
| `protocol/wireguard/endpoint.go` | `DialContext` (строка ~642): стадия `lookup` с `via=dns_rules` вокруг `dnsRouter.Lookup` (строки ~655, ~678) и стадия `tcp_connect` вокруг dial через netstack | домен, переданный детуру, резолвится по правилам (вывод `resolve_via_rules_in_source_context`); соединение внутри туннеля открывает netstack, не `DefaultDialer`, и `default.go` его не видит |
| `dns/router.go` | `exchangeWithRules` / `lookupWithRules` / legacy-ветка: атрибут «правило `dns[i]`» в текущую стадию | какое DNS-правило сработало |
| `dns/client.go` | `Exchange` / `Lookup`: при `lxtrace.Fresh(ctx)` выставить `options.DisableCache` (апстрим под ним уже пропускает и чтение, и запись кэша, строки ~287 и ~386); ответ из кэша — `REUSED` с возрастом | `fresh` на вложенных резолвах, чьи опции handler не контролирует; `app_dns` передаёт `DisableCache` сам, без вставки |
| `dns/transport/conn_pool.go` | `Acquire*`: `created == false` — `REUSED` с возрастом; при `Fresh` — новое соединение, закрыть после ответа. Dial уже идёт на ctx вызывающего (`dialAndInstall`, строка ~283), так что трасса доходит до вложенного dial без вставки | переиспользованное DNS-соединение не выдаётся за новое |
| `common/tls/client.go` | `ClientHandshake`: стадия `tls` (SNI, отпечаток, ALPN, версия, key share) | — |
| `common/tls/reality_client.go` | вердикт `accepted` / `dest_cert` / `invalid_cert` в существующем месте проверки `verifier.verified` | различение REALITY |
| `route/route.go` | до трекеров в `routeConnection` / `routePacketConnection`: `if lxtrace.Routed(ctx, &metadata, selectedRule, selectedOutbound) { conn.Close(); return nil }` — один вызов пишет стадию `route` (правило, действие, outbound, итоговые метаданные) и останавливает `DEPTH_ROUTE` | стадия `route` для TCP и UDP из одной точки, без трекера; `DEPTH_ROUTE` не открывает соединений и не мигает в списке. Источник домена выводится из метаданных: `FakeIP` → fakeip, `Protocol` + `Domain` → sniff, `Domain` без `Protocol` → reverse mapping, `Destination.Fqdn` → request |
| `route/conn.go` | `NewConnection` / `NewPacketConnection`: стадия `dial` вокруг `DialContext` / `DialSerialNetwork` первого хопа | вершина поддерева dial; `DEPTH_CONNECT` |
| `dns/transport/fakeip/store.go` | `Create`: счётчик и время оборота пула при переходе в начало диапазона | вывод `fakeip_pool_wrapped` |

Транспорты gRPC, WS, HTTPUpgrade (апстрим) — статус ответа вставкой
по необходимости; в первую итерацию не входят, их ошибка видна в `tls`
или в следующей стадии.

### 3.3 Внешние модули — без вставок

Hysteria2, TUIC (`sing-quic`), mux (`sing-mux`), naive (`cronet-go`) не
сообщают промежуточных шагов. Одна стадия `quic + auth` (или `auth`) на
вызов dial, итог классифицируется по ошибке: у Hysteria2 отказ приходит
как `authentication failed, status code: N`. Переиспользование QUIC- и
mux-соединений извне не управляется: при `fresh` такие стадии
помечаются атрибутом `reuse=uncontrolled`.

## 4. Корень

Буквально §5.2 SPEC: среди `FAILED`-стадий, без отмеченных `lost_race`
(§5.13 SPEC) и без тех, у кого предок закончился OK позже них (шаг восстановился:
MASQUE откатился с h3 на h2, TrustTunnel заменил протухшую сессию), берётся завершившаяся первой и без упавших потомков.
Стадии, не выполненные из-за неё, — `SKIPPED`. По таймауту незавершённые
стадии закрываются `FAILED/timeout`, и к ним применяется то же правило.
Отметка проигравших: `DialParallelNetwork` и `DialSerialNetwork`
в `common/dialer/default_parallel_network.go` отменяют проигравших —
вставка `tcp_connect` в `default.go` видит `context.Canceled` при
наличии победителя у родителя и зовёт `LostRace` вместо `End(err)`.

## 5. Выводы

`common/lxtrace/findings_lx.go`: чистые функции над снапшотом трассы
и состоянием ядра (опции TUN, fakeip-хранилище, правила). Каждая
возвращает ноль или один вывод. Юнит-тестируются на синтетических
трассах.

- `resolve_via_rules_in_source_context` — стадия `lookup` с `via=dns_rules`
  под стадией `detour`.
- `dependency_outside_route` — узел из стадии `dial` под `dns`, которого нет
  в цепочке маршрута.
- `ip_rules_not_evaluated` — без вставки в `matchRule`: каждое правило
  с индексом меньше выбранного не совпало (применённые по пути нефинальные
  действия видны в метаданных: `Protocol` для sniff, `DestinationAddresses`
  для resolve). Среди них правила с адресными условиями в опциях при
  неизвестном адресе — «не вычислены». Опции правил — из снапшота конфига,
  как в `GetRules` (014).

## 6. libbox и CLI

- `experimental/libbox/command_client_conntest_lx.go` + типы: запрос
  с сеттерами, handler, сессия с `Cancel`, стадия/итог/вывод с геттерами,
  итераторы атрибутов. Форма — по образцу апстримного `StartSTUNTest`.
- Страж 038 по поверхности `CommandClient` — без исключений.
- `cmd/sing-box/cmd_api_connection_test_lx.go`: `api connection-test`,
  печать дерева по `parent`.

## 7. Провод

`daemon/started_service.proto` — внутри существующего блока
`lx:begin/end lx_command`. Регенерация `make -f Makefile.lx lx-proto`.
После неё откатить шум protogen и правки gofumpt в сабмодуле
`wireguard-go`.

## Зона касания upstream

| Файл | Вставок |
|------|---------|
| `daemon/started_service.proto` | внутри существующего lx-блока |

| `common/dialer/default.go`, `detour.go`, `resolve.go` | по 1–2 |
| `common/tls/client.go`, `reality_client.go` | по 1 (во втором уже есть lx-правки) |
| `dns/router.go`, `dns/client.go`, `dns/transport/conn_pool.go` | по 1–3 |
| `dns/transport/fakeip/store.go` | 1 |
| `route/route.go` | 1 (`Routed`, до трекеров) |
| `route/conn.go` | 1–2 |
| `protocol/wireguard/endpoint.go` | 2 (рядом с lx-блоками SPEC 020) |

Все вставки — однострочный вызов `lxtrace`. Та же таблица по релизам —
SPEC §10. Самые конфликтные места при мерже — `exchangeWithRules`
в `dns/router.go` и `NewConnection` в `route/conn.go`: апстрим правит
их чаще остальных. Их вставки сделаны отдельным коммитом, чтобы при
конфликте их можно было снять и вернуть без остального.
