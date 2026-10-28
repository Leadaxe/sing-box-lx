# IMPLEMENTATION REPORT 139 — StartConnectionTest, релизы 1 и 2

> 🌐 English version: **[IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md)** — основной отчёт; этот файл — русская копия для ознакомления.

**Статус:** I (implemented) — релиз 1 по §9 SPEC лежит в `lx` (2026-10-27,
`a0ec238b8` … `cdf9a297a`), релиз 2 — 2026-10-28 (`980c9145d` …
`1b4aa8317`, поверх синка v1.14.4, SPEC 140). Сборки с lx-тегами и без,
`go vet`, юниты всех затронутых пакетов и стенд приёмки зелёные под `-race`.
Не сделано: прогон на устройстве (экран LxBox, роутер) и полевая проверка
критерия 1 вне стенда. Релиз 2 описан в отдельном разделе ниже.

## Что сделано

| Слой | Файлы | Содержание |
|------|-------|------------|
| Держатель трассы | `common/lxtrace/` (`trace_lx.go`, `trace_stub.go`, `doc.go`, тесты) | дерево стадий в ctx: `Begin`/`End`/`Mark`/`LostRace`, `BeginLookup` с ловлей цикла резолва, `Routed` (стадия route + развёртка selector'ов + останов `DEPTH_ROUTE`), `WithStage`, `Options{Fresh, Probe, Depth, MaxStages, Emit, Outbounds}`, `Finish` с правилом корня §5.2, потолок 512 стадий, `Summary.Dropped` для снапшотов, не влезших в буфер потока. Stub стоит одну проверку `ctx.Value` |
| Провод | `daemon/started_service.proto` + регенерированные `*.pb.go` | `rpc StartConnectionTest` и десять сообщений §3.1 внутри существующего шва `lx_command`, плюс `stagesTruncated` |
| Handler | `daemon/started_service_conntest_lx.go`, `daemon/conntest_lx.go`, `_stub.go`, тесты | валидация (§3.2), семафор на 4, таймаут 15 с / 60 с, метаданные источника (`InboundType = lx-connection-test`, package manager → uid, если есть), `app_dns` для A/AAAA/HTTPS, путь через роутер поверх `net.Pipe` → `RouteConnectionEx` (ошибки маршрута и dial через `onClose`), путь через узел, стадии `httptrace` `target_tls`/`first_byte`/`http`, глубины, синтезированные `proxy_header` (NO_ACK) и `class=auth|quic` на упавшем dial hysteria2/tuic/naive, `RootCause = 0` при пройденном пути, все соединения проверки закрываются по концу |
| Хелперы стадий (lx-файлы) | `route/conntest_lx.go`, `common/dialer/conntest_lx.go`, `common/tls/conntest_lx.go`, `protocol/wireguard/conntest_lx.go`, `dns/client_trace_lx.go`, `dns/transport/conntest_lx.go`, `protocol/group/conntest_lx.go`, `protocol/chain/conntest_lx.go`, `transport/v2rayxhttp/conntest_lx.go` | `dial` (лист через selector'ы, `via_group`), `detour`, `lookup`, `tcp_connect`/`udp` (отметка проигравших гонку), `tls`, `dns` (правило, источник кэша, атрибуция группы), `group` (режим, выбор, слот) с защитой `IsProbe` штрафов, истории, retest, `lastSelected` и счётчика round-robin, `chain`, `transport` (XHTTP: режим, метод, путь, поздний статус как `transport_response`; переиспользование пула — `REUSED` с возрастом; `fresh` открывает отдельный транспорт) |
| Вставки `// lx: SPEC 139` | см. SPEC §10 | одно- и двухстрочные вызовы хелперов |
| Клиент | `experimental/libbox/command_client_conntest_lx.go`, `command_types_conntest_lx.go`, тесты | запрос с сеттерами, handler, сессия с `Cancel`, геттеры и итераторы, безопасно для gomobile (`ErrorMessage()` вместо `Error()`), страж 038 проходит |
| CLI | `cmd/sing-box/cmd_api_connection_test_lx.go` | `sing-box api connection-test <url>` с флагом на каждое поле запроса, дерево по `parent` |
| Стенд | `lx-test/conntest/` | критерии приёмки релиза 1 (ниже) |
| CI | `.github/workflows/lx-ci.yml` | один шаг `-race` по всем затронутым пакетам и стенду |
| Хотфикс | `protocol/group/urltest.go` + `urltest_selected_lx.go` | `selectedOutboundTCP/UDP` под `selectedAccess`: гонка данных между `performSelectionUpdate` и `Select`/`Now`/`References`, вскрытая асинхронным update SPEC 136; внесена в [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) |

## Приёмка (стенд `lx-test/conntest`, `-race`)

Топология: серверный box с двумя inbound `shadowsocks` (S1, S3) и `trojan`
поверх TLS (S2, самоподписанный), тестовый DNS-сервер по TCP и цель
`httptest`; клиентский `StartedService` с N1 → S1, N2 → S2 (`server:
node2.test`, `detour: N1`, `domain_resolver: dns-detour`), N3 → S3,
`dns-detour` с `detour: N3`, selector `proxy` [N2, N1], urltest `balanced`,
inbound `mixed`, правило `reject` и `domain_suffix target.test → proxy`.

| Критерий | Итог | Наблюдение |
|----------|------|------------|
| 1 маршрут = боевой | PASS | `route` (`rule=domain_suffix=target.test`, `outbound=proxy`) и `group` (`selected=N2`) совпадают с записью списка соединений настоящего SOCKS-соединения того же источника; соединение проверки видно как `lx-connection-test` |
| 2 вложенное дерево (вывод — релиз 2) | PASS | `route → group proxy → dial N2 (via_group=proxy) → { lookup node2.test → dns dns-detour → detour N3 → tcp_connect S3; detour N1 → tcp_connect S1; tls node2.test; proxy_header NO_ACK } → first_byte → http 200`, `rootCause = 0`. При выключенном S3: корень = `tcp_connect` под `detour N3`, `dial N2` FAILED, `first_byte`/`http` SKIPPED |
| 3 цикл резолва | PASS | около 1 мс; `lookup node4.test` FAILED с `cycle_with` |
| 8 `DEPTH_ROUTE` ничего не открывает | PASS | ноль принятых соединений на S1, S2, S3 и цели; стадии только `route`, `group` |
| 9 состояние группы не меняется | PASS | выбранный узел и история urltest равны до и после проверки через мёртвый узел; контрольное настоящее соединение их меняет |
| 10 отмена | PASS | S1, S2, S3 и цель видят закрытие соединений, включая соединение пробы в пуле DNS; итог не отправлен; горутины сходятся |
| 11 stub | PASS | `codes.Unimplemented` без `with_lx_command`; страж 038 проходит |

Критерии 2 (вывод), 4, 5, 6, 7 — релиз 2 (§9 SPEC).

## Отклонения от SPEC

- **`lost_race` несёт `0`, а не id победителя** (§5.13): вставка в
  `DefaultDialer` видит только `context.Canceled`; победителя знает
  `DialParallelNetwork`, а не проигравший. На правило корня не влияет.
- **`proxy_header` для detour'ов синтезируется и при `DEPTH_CONNECT`**: их
  заголовок — часть подъёма туннеля; §7 исключает только первый хоп.
- **`http_status` XHTTP приходит после конца стадии `transport`**: поздний
  не-200 становится FAILED-потомком `transport_response` и корнем. HTTP/1.1
  не пишет `REUSED` (каждый длинный запрос диалит сам); переиспользование
  HTTP/3 — вид `quic`.
- **Стадия `dns` закрывается через log-пути клиента** (`emitQueryEvent` /
  `emitFailedQuery`), которые у работающего box есть всегда.
- **`DEPTH_ROUTE` через узел** не пишет ничего сверх `app_dns`: решения
  роутера на этом пути нет.
- **Топология стенда — не приложение А**: без AWG, Hysteria2, REALITY и
  XHTTP-сервера в процессе; shadowsocks-2022 заменён на `aes-128-gcm`, потому
  что у sing-shadowsocks2 своя гонка данных (`shadowaead_2022/method.go`
  `requestSalt`), красящая любой прогон под `-race`.

## Что выяснилось по ходу

- **`tls.NewDialer` обходил `tls.ClientHandshake`** и звал
  `aTLS.ClientHandshake` напрямую: стадия `tls` не писалась у VLESS, Trojan,
  VMess, WS, gRPC, DoT и DoH, пока путь handshake в
  `defaultDialer.dialContext` не получил свою пару `// lx:`.
- **Ревью нашло две ошибки класса «падение» раньше стенда:** send в закрытый
  канал эмита (`emit` против `Finish` без общего замка) и двойное закрытие
  `routeDone` при редиале `net/http` после GOAWAY. Обе исправлены и покрыты
  тестами (`TestFinishRacesEmitNoPanic`, тесты ревью в
  `daemon/conntest_review_lx_test.go`).
- **`net/http` передаёт в `DialContext` контекст без отмены**, из-за чего
  таймаут через узел классифицировался как проигранная гонка; путь через узел
  теперь диалит на ctx проверки.
- **`first_byte` должен ждать и `WroteRequest`, и конца верхнего `dial`**: на
  пути через роутер запись в pipe завершается, когда sniffer прочитал запрос,
  то есть до маршрута и dial.
- **`gofumpt -w .` из `lx-proto` переформатирует восемь tracked-файлов и все
  четыре сабмодуля**; откатывать `git checkout --` после регенерации.
- **lx-ci гоняет тесты только по явному списку**: у `daemon/` и
  `experimental/libbox/` до этой задачи шага тестов не было.
- **`TestFailover_moveOnDialFailure`** в `protocol/group` завязан на тайминги
  и флапает под нагрузкой (20 мс против 80 мс задержек узлов).
- **Полный `go build ./...` с `LX_TAGS` на холодном кэше идёт больше десяти
  минут** на Mac владельца; запускать в фоне.

## Проверка

```
go build ./...  (без тегов / LX_TAGS, -checklinkname=0)               rc=0 / rc=0
go vet (LX_TAGS; -unsafeptr=false для daemon, libbox)                  чисто
go test -race -count=1 -tags LX_TAGS ./common/lxtrace/ ./common/dialer/ ./daemon/
  ./experimental/libbox/ ./dns/ ./dns/transport/... ./protocol/group/
  ./protocol/chain/ ./transport/v2rayxhttp/ ./route/ ./protocol/wireguard/
  ./cmd/sing-box/ ./lx-test/conntest/                                 ok
go test -count=1 (без тегов) ./common/lxtrace/ ./daemon/ ./experimental/libbox/
  ./lx-test/conntest/ ./dns/transport/ ./route/                       ok
gofmt -l по lx-файлам                                                  чисто
```

## Релиз 2

Реализован тремя параллельными агентами на непересекающихся файлах, ревью —
четвёртым, который читал только дифф; находки ревью исправлены (ниже).

| Область | Файлы | Содержание |
|---------|-------|------------|
| `fresh` и переиспользование на вложенных уровнях DNS | `dns/client.go` (+2), `dns/client_trace_lx.go`, `dns/transport/conn_pool.go` (+11), `dns/transport/conntest_lx.go` | `fresh` ставит `DisableCache` в `beginExchange` и `Lookup` (кэш не читается и не пишется, RDRC пропускается); пул даёт fresh-проверке собственное соединение, закрываемое при release, не общее и не простаивающее; переиспользованное соединение из пула — дочерняя стадия `connection` с `REUSED` (`reuse=pooled|joined`, `age`); `connection=new|reused` на стадии `dns` |
| WG/AWG | `protocol/wireguard/endpoint.go` (+1 в lx-блоке idle-suspend), `protocol/wireguard/conntest_lx.go` | `wake` (сколько спал, resume/rebuild, FAILED, если endpoint остался лежать); `wg_handshake` перед каждым TCP-соединением внутри туннеля (новый — заканчивается временем хендшейка; существующий — REUSED с возрастом; нет — FAILED и корень; не прочитать — NOT_EVALUATED) |
| MASQUE, TrustTunnel | `protocol/masque/{outbound,conntest_lx}.go`, `protocol/trusttunnel/conntest_lx.go`, `transport/trusttunnel/{client,trace}.go` | `quic` и `auth` (статус, `refused=access_denied` / `authentication_required`), переиспользованный туннель или сессия — REUSED с возрастом. У TrustTunnel общий хук `ClientTrace` в духе httptrace без импорта lxtrace — файл остаётся в форме для апстрима |
| `reuse=uncontrolled` | `daemon/conntest_lx.go` | при `fresh` у hysteria, hysteria2, tuic, naive, anytls, `multiplex.enabled` и транспорта gRPC (из рабочего конфига), MASQUE и TrustTunnel |
| REALITY | `common/tls/reality_client.go` (+1), `common/tls/reality_conntest_lx.go` | `reality` — дочерняя `tls`: `accepted` / `dest_cert` / `invalid_cert` |
| fakeip | `dns/transport/fakeip/store.go` (+1), `store_lx.go` | доступ на чтение (диапазоны, сохранность, адрес ↔ домен) и счётчик оборотов пула; `connectIP` из диапазона — `route` FAILED с `fakeip=no_record` |
| Выводы | `common/lxtrace/findings_lx.go`, `daemon/started_service_conntest_lx.go` | все 15 кодов §6 с уровнями, чистые функции над итогом и состоянием ядра (опции TUN и правила из рабочего конфига, хранилище fakeip, `router.Rules()`); libbox и CLI их уже несли |
| Корень | `common/lxtrace/trace_lx.go`, handler | сбой внутри стадии, закончившейся OK после него, восстановлен (MASQUE h3→h2, повтор сессии TrustTunnel) и корнем не считается; `DEPTH_CONNECT` с поднятым первым хопом — без корня |
| Стенд | `lx-test/conntest/conntest_hysteria2_lx_test.go` | узел Hysteria2: путь, `reuse=uncontrolled` при `fresh`, неверный пароль → корень `class=auth` |
| CI | `.github/workflows/lx-ci.yml` | fakeip, masque и REALITY-тесты common/tls добавлены в шаг SPEC 139 |

### Исправлено после ревью

- Повтор транспорта оставлял проваленную первую попытку корнем, когда позже
  падала другая стадия: правило корня пропускает сбои, восстановленные
  предком, закончившимся OK позже них. Первая версия пропускала любой сбой
  под OK-предком и сломала поздний отказ XHTTP (лениво набранный транспорт
  возвращается раньше, чем сервер откажет); условие по времени его
  сохраняет.
- `fakeip_pool_wrapped` срабатывал после рестарта с сохранённым хранилищем:
  первый проход продолжается с середины диапазона, поэтому интервал
  меряется только между двумя оборотами.
- `excluded_from_vpn` выдавал `include_uid` для источника только с uid при
  `include_package`: вердикт по include теперь требует, чтобы каждый
  используемый список был проверяем.
- Не было `reuse=uncontrolled` для транспорта gRPC.

### Отклонения и границы (внесены в SPEC)

- Приложение А изменено: собственный detour и lookup пира WG-endpoint'а
  идут на его контексте и в трассу не попадают; `wake` — лист,
  `wg_handshake` — сосед; `reality` — дочерняя `tls`.
- Hysteria2 и TUIC открывают QUIC на долгоживущем контексте клиента: хоп —
  одна непрозрачная стадия `dial`, классифицированная по ошибке.
- Успех MASQUE даёт `status=2xx`: транспорт точный код не отдаёт.
- DoH и DoH3 идут мимо пула DNS-соединений: `fresh` не заставляет открыть
  новое соединение, переиспользование не помечается.
- Уровней выводов в SPEC не было; выбраны и внесены в §6. `quic_rejected`
  для TCP и `fakeip_pool_wrapped` приближённые (§7).
- Критерий 5: неверный `public_key` даёт `dest_cert` только при публично
  доверенном сертификате dest; при самоподписанном — `invalid_cert`.

### Известно, не исправлено

- Проба трогает общее состояние в апстримном DNS-коде: ошибка повтора на
  собственном DoQ-соединении проверки зовёт `Reset()` у боевого пула
  (`dns/transport/quic/quic.go`), а закрытие fresh-соединения мультиплексора
  сбрасывает `demoteFailures` (`dns/transport/multiplexer.go`).
- QUIC-сессия, открытая проверкой (TrustTunnel h3, MASQUE, общее
  DoQ-соединение), держит значения контекста dial, включая трассу, до конца
  жизни (quic-go строит контекст соединения через `WithoutCancel`): только
  память, на поведение не влияет.

### Проверка

```
go build ./...  (без тегов / LX_TAGS; windows, linux; android libbox с тегами AAR)      ok
go vet (LX_TAGS) по затронутым пакетам                                                   чисто
go test -race -tags LX_TAGS,with_lx_chain: common/lxtrace, common/dialer,
  common/trafficcontrol, common/interrupt, daemon, experimental/libbox, dns/...,
  protocol/{group,chain,masque/...,trusttunnel}, transport/{trusttunnel,v2rayxhttp},
  route, cmd/sing-box, lx-test/{conntest,chain}                                           ok
go test -race (теги шага wireguard в CI): transport/wireguard, protocol/wireguard, route,
  adapter/endpoint, common/lxclosewatch; lx-test/startclose                               ok
go test ./common/tls/ -run 'Reality|Lx'                                                   ok
make -f Makefile.lx lx-check                                                              ok
```

## Открыто

- Прогон на устройстве: экран LxBox на этом ядре, `api connection-test` на
  роутере с настоящим DNS-detour; до него #59 открыт.
- Две известные побочки пробы выше.
