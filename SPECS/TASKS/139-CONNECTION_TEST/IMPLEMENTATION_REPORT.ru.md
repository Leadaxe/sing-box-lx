# IMPLEMENTATION REPORT 139 — StartConnectionTest, релиз 1

> 🌐 English version: **[IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md)** — основной отчёт; этот файл — русская копия для ознакомления.

**Статус:** I (implemented) — релиз 1 по §9 SPEC лежит в `lx` (2026-10-27,
`a0ec238b8` … `cdf9a297a`). Сборки с lx-тегами и без, `go vet`, юниты всех
затронутых пакетов и стенд приёмки зелёные под `-race`. Не сделано: прогон на
устройстве (экран LxBox, роутер), ответ в #59 и полевая проверка критерия 1
вне стенда. Релиз 2 (§9) не начат.

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

## Открыто

- Ответ в #59 и закрытие (после отмашки владельца, со ссылкой на коммит).
- Прогон на устройстве: экран LxBox против этого ядра, `api connection-test`
  на роутере с настоящим DNS-детуром.
- Релиз 2 (§9 SPEC): `fresh` на вложенных уровнях DNS, вердикт REALITY,
  `wake`/`wg_handshake`, fakeip, выводы.
