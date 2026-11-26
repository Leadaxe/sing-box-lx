# SPEC: 166 — UPSTREAM_SYNC_1_14_4_PLUS_146

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` v1.14.4 + 146 (64 коммита после нашей базы) в `lx`; два форк-сабмодуля |
| Статус | I — мерж `eee84c384` и правки после него в ветке `claude/upstream-sync-stable-979b66`, в `lx` не влит; сборки и тесты зелёные (§6), на устройстве не прогонялось |
| База | до: v1.14.4-lx.37-rc.1, merge-base `f0fbf236f` (v1.14.4 + 82); после: `upstream/stable` = `060f54a9a` (2026-11-26), дрейф 0 |
| Связано | [154](../154-UPSTREAM_SYNC_1_14_4_PLUS_9/SPEC.md) (предыдущий синк такого же вида), [140](../140-UPSTREAM_SYNC_1_14_4/SPEC.md) (роутер разворачивает группы), [019](../019-URLTEST_MODE_STICKY/SPEC.md)/[054](../054-URLTEST_PENALTY_FAILOVER/SPEC.md)/[139](../139-CONNECTION_TEST/SPEC.md) (urltest), [157](../157-DNS_MULTIPLEXER_WRITE_LOCK/SPEC.md), [045](../045-TLS_DISABLED_NIL_DIALER_CRASH/SPEC.md), [161](../161-HTTP_CONNECT_OUTAGE_LEAK/SPEC.md), [147](../147-CACHEFILE_RELEASE_TAKEOVER/SPEC.md), [148](../148-OOM_KILLER_RUNTIME_MEMORY/SPEC.md), [069](../069-WG_V6_BIND_FAIL_KILLS_V4/SPEC.md), [130](../130-WIN7_REGISTRY_NOTIFY_THREAD_AGNOSTIC/SPEC.md), [040](../040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md), [023](../023-MUSL_TOOLCHAIN_MIRROR/SPEC.md) |

## 1. Что принёс апстрим

64 коммита 2026-11-24…26, нового тега нет, `upstream.version` остаётся `1.14.4`. Почти всё — пачка исправлений одного автора по всему дереву. Значимое для нашего слоя:

| Коммит | Суть | Наш слой |
|---|---|---|
| `37fffa325` Fix URLTest selection and idle connection references | Выбор urltest атомарный (`TypedValue`); прогон копит результаты и пишет историю одним `UpdateURLTestHistory` после батча; ошибка дозвона, кроме закрытия/отмены/таймаута, стирает историю листа (`RealTag`) и сразу переселектит; `performUpdateCheck` переключает и при отсутствии истории; `Referrer.References()` → `(outbounds, dnsServers)`; `ReferenceManager` обходит и провайдеры сертификатов | **конфликт**, §3.1 |
| `4913f787f`, `9e0a6a393`, `4d6bcb1a5` | Порядок старта endpoint'ов внутри сортировки outbound-менеджера; сортировка и проверка ссылок (outbound'ы правил, DNS-серверы, зависимости) переехали из `Start` в `Initialize` при создании box; `adapter.Endpoint` получил `IsEndpoint()` | конфликт в `adapter/outbound/manager.go`; тихие поломки тестовых стендов, §4 |
| `3d071e6e4` Fix concurrent DNS writes corrupting shared connections | Замок записи в `queryMultiplexer` и сериализация ответов потокового DNS | это наш SPEC 157, §3.2 |
| `ef408a1e1` Fix vless and trojan crash when TLS is disabled | nil-гейт TLS-dialer | это наш SPEC 045, §3.2 |
| `8a1ddcebd`, `c7f675a68`, `27c872d81` | Рукопожатия SOCKS/HTTP, ShadowTLS v3 и httpupgrade/websocket ограничены таймаутом и контекстом | это расширение SPEC 161 (`lxhandshake`), §3.3 |
| `e13e6ce67`, `0964977b7` FakeIP | Адреса резервируются диапазоном с синхронной записью метаданных; метаданные больше не стираются при чтении | SPEC 147, §3.4 |
| `d31a43ff3` OOM killer | Состояние больше не «липнет» к triggered; повторный сброс решает `resetDue` по режиму политики | SPEC 148, §4 |
| `fe7d9661e`, `6df127451` WireGuard detour bind | Детур-бинд всегда слушает (`ListenPacket` к адресу пира), connected-ветки нет; сон устройства бинд не держит | конфликт, §3.1 |
| `0cdfc45e2` ECH | Кэш ECH по имени запроса, повтор по `RetryConfigList` | конфликт в `common/tls`, §3.1 |
| `61c94949c`, `eacdf91f1`, `298d6b7e3`, `b64a231dc`, `d553a2943` и др. | DNS: префикс длины, усечение, DHCP, mDNS, обратные записи | автослияние, наших правок нет |
| `88920c9fb` hijack-dns не закрывал TCP | `route/dns.go` | рядом с SPEC 046, тот не задет |
| `060f54a9a` naive / cronet | бамп `cronet-go`, `.github/CRONET_GO_VERSION` → `72c4e6f7…` | зеркало musl (SPEC 023) — вручную после пуша |

`go-chi/chi` и `grpc` апстрим не трогал (chi v5.2.5, grpc v1.79.1 у апстрима; у нас grpc v1.83.2) — Dependabot-алерты этим синком не снимаются.

## 2. Сабмодули (раннбук §1, до ядра)

| Форк | Пин апстрима | Мерж в форк | Конфликты |
|---|---|---|---|
| `wireguard-go` (`lx-awg2-v007`) | `afd160a` (1 коммит: `Open` сохраняет порт уцелевшего семейства) | `5e0ba64` | `conn/bind_std.go`: взята форма апстрима (`v4port`/`v6port`), наша подстановка порта снята; оставлены предикат семейства для Windows и nil-guard SPEC 069, швы SPEC 158/160 |
| `sing-tun` (`lx`) | `725b9b9` (18 коммитов: system-стек, auto-redirect, монитор интерфейсов) | `53e0731` | `monitor_windows.go`: апстрим удалил наблюдатель реестра вместе с отслеживанием DNS-серверов — патчить SPEC 130 больше нечего, файл апстримный. `stack_system.go`: апстримный `KeepAliveConfig` перенесён в наш `listenTCP` (SPEC 040, его получает и пересоздание слушателя), `NewNat` без UDP-таймаута — и в тесте самолечения, который слился без конфликта и не собирался |

`gvisor`, `utls` — пины не менялись. `go list -m`: все четыре на `./submodules/*`. Оба форка **не запушены** — пушить до суперпроекта.

## 3. Конфликты ядра (22 файла)

База честная (merge-base в графе, force-push не было), поэтому прямой `git merge`; везде — форма апстрима и наши швы поверх.

### 3.1 Перенесённые швы

- `protocol/group/urltest.go` — собран заново из апстримного файла с однострочными вставками. Выбор атомарный, поэтому `selectedAccess` SPEC 139 снят, `selectedLx`/`setSelected*Lx` — обёртки над `Load`/`Store`, `moveSelection` — `Swap`. Ошибка дозвона: апстримное условие `!E.IsClosedOrCanceled(err)`, внутри наш `deleteHistoryOnDialError` (round_robin и проба историю не трогают) и апстримный переселект — кроме мёртвого пути (`pathDead`), где выбор переносит SPEC 054 без `Interrupt` (§5); лист стирается по `RealTag`, как у апстрима (раньше мы стирали и тег члена, и лист). `URLTest.PerformUpdateCheck` в round_robin ничего не делает: одного выбора там нет (как в `afterTestLx`), а роутер зовёт его на каждом неудачном открытии. `performSelectionUpdate` возвращает «выбрано» и сам не уведомляет, уведомляют вызывающие; `invalidateReachability` (SPEC 020) осталось внутри. Touch/Close — апстримные: флаг `closed` закрывает ту же гонку, ради которой мы переносили замок, `closeLx` только отменяет контекст (050). Хуки батча `probeFailedLx`/`probeAliveLx` стоят на прежних местах, но запись истории теперь в конце батча — §5.
- `adapter/outbound/manager.go` — поля апстрима + блок `chain`.
- `common/dialer/default.go`, `resolve.go` — апстримный `DialParallelNetwork`; швы SPEC 139 на тех же вызовах (в `resolve.go` — по каждому `d.router.Lookup`).
- `common/tls/client.go` (`dialContext` принимает `config`), `ech.go` (новый `loadECHConfigList`) — швы SPEC 139/060.
- `dns/transport/fakeip/store.go` — пул `addressPool` апстрима; `store_lx.go` переведён на новые поля.
- `route/conn.go`, `route/router.go` — апстримная форма, наши SPEC 139/138 и блоки SPEC 020/046. Апстрим `37fffa325` в `ConnectionManager.deleteURLTestHistory` теперь не только стирает историю последнего элемента цепочки, но и зовёт `PerformUpdateCheck` каждой urltest-группы в ней. Наша цепочка кончается на urltest (SPEC 140), и неудачная проба SPEC 139 (она идёт через `RouteConnectionEx`) переизбирала бы группу, которую только наблюдает: три вызова обёрнуты в `deleteURLTestHistoryLx` (`route/conntest_lx.go`), проба группы не трогает.
- `transport/wireguard/client_bind.go`, `endpoint.go` — детур-бинд слушает к адресу пира; таймаут дозвона SPEC 071/072 и путь сокета SPEC 060 перенесены на `ListenPacket`.
- `protocol/shadowtls/outbound.go` — апстримное ограничение + наша строка (§3.3).
- `experimental/cachefile/cache.go` — наш `scope.Add(c.Release)` вместо апстримного замыкания (апстрим лишь добавил проверку `nil`, `Release` её уже имеет).
- `go.mod`/`go.sum` — require апстрима, наши `replace` и прежние более новые версии (§5); `go.sum` — объединение и `go mod tidy`.

### 3.2 Апстрим закрыл наш хотфикс — взята его форма

- **157** (`3d071e6e4`): `multiplexer.go`, `protocol/dns/outbound.go`, `route/dns.go`, `service/resolved/service.go` — апстримные; `protocol/dns/stream_write_lock_lx.go` (`NewSerialWriteConn`) удалён. Стражи 157 оставлены и проходят на апстримном коде (через настоящий `Outbound.NewConnection`, мультиплексор, `hijackDNSStream`, resolved).
- **045** (`ef408a1e1`): апстрим вносит тот же nil-гейт байт-в-байт; `trojan/outbound.go` равен апстримному, в `vless/outbound.go` остались только другие швы.

### 3.3 Ограничение рукопожатий (161, расширение)

Все три условия снятия из реестра выполнены апстримом. В **socks** держать оба механизма нельзя: наш `lxhandshake.Context` отменяет контекст раньше апстримного таймаута, клиент `sing` возвращает `ctx.Err()`, и вместо таймаута получается `context.Canceled` — а для SPEC 054 это «клиент ушёл», узел не штрафуется. Поэтому в socks шов снят; `handshake_timeout_lx_test.go` переведён на `client.SetHandshakeTimeout` и проходит целиком на апстримном механизме. В **httpupgrade**, **shadowtls** и **HTTP CONNECT** (основная строка 161) оба механизма давали таймаут; по решению владельца сняты и они: `common/lxhandshake/`, `protocol/{http,shadowtls}/handshake_lx.go` удалены, три файла равны апстримным. Тест HTTP укорачивает приватный `handshakeTimeout` клиента `sing` через reflect, тест httpupgrade ждёт честные `C.TCPTimeout` (константа в `DialContext`), юнит ShadowTLS удалён вместе с обёрткой. Issue об этих таймаутах у апстрима не было; tmmgn завёл [SagerNet/sing-box#4724](https://github.com/SagerNet/sing-box/issues/4724) по SPEC 157 со ссылкой на наш фикс — апстрим починил своим `3d071e6e4`, issue не закрыт и не упомянут.

### 3.4 FakeIP-половина SPEC 147

Апстрим пишет метаданные FakeIP синхронно при резервировании и не стирает их при чтении; `saveMetadata`, таймер и `FakeIPSaveMetadataAsync` удалены. Наш досыл отложенных метаданных в `Release` и «посев» прочитанных держались ровно за это — сняты вместе со швами в `fakeip.go` и тестом на `FakeIPSaveMetadataAsync`. Перехват `cache.db` внутри процесса (сама суть 147) остаётся; страж «метаданные переживают сессию без выделений» проходит на апстримном коде.

## 4. Тихие поломки, которые мерж пронёс без конфликта

Найдены сборкой и тестами, не по списку конфликтов (правило из памяти `merge-autoresolve-breaks-silently`):

1. `common/dialer/default.go` — `beginConnectLx(ctx, network, address)` в `DialParallelNetwork` ссылался на параметр, переименованный апстримом в `destination`.
2. `service/oomkiller/runtime_limit_lx.go` (SPEC 148) звал старую `nextPressureState(state, …)`; хуже — в апстримном `resetDue` наш режим `runtime_limit` попадал в `default: return false`, то есть после первого сброса второй не случился бы никогда. Добавлены case в `resetDue` и `resetDueRuntimeLimit` по той же формуле для обоих условий режима; ожидание теста про «липкое» triggered исправлено, добавлен тест `resetDue`.
3. `TransportManager.Initialize` и `outbound.Manager.Initialize` теперь сортируют и проверяют, а Go позволяет вызвать их как оператор, выбросив ошибку. Стенды DNS-группы (FEATURE 013) и цепочки (`protocol/chain/chain_test.go`) никогда не звали `Initialize` перед `Start` или звали до добавления узлов — валидация «пропала», узлы не стартовали. Боевой `box.go` зовёт `Initialize` после создания всех узлов; рантайм-создания outbound'ов у нас нет.
4. `route/reference_start_lx_test.go` (SPEC 136) — контекст без `CertificateProviderManager`, nil-паника в новом обходе `update()`.
5. Тесты на старых API: атомики выбора в тестах группы, `NewClientBind` без connected-ветки (`pipeDialer` теперь отдаёт packet-conn с записью носителя), `NewStore` с ошибкой, `HTTP3Transport.tlsConfig` типа `tls.Config`, ECH без `fetchAndHandshake`.

Аудит автослияния: для 43 файлов, которые тронули обе стороны, множество «+/−» строк lx-дельты до мержа (против `f0fbf236f`) и после (против `060f54a9a`) сверено; все расхождения объяснены §3. Файлы без lx-дельты после мержа байт-в-байт равны апстримным. `References()` везде на новой сигнатуре (старая молча перестала бы быть `Referrer`).

## 5. Изменения поведения, принятые от апстрима

- **least_test, отказ дозвона по таймауту.** Апстрим больше не стирает историю узла, если ошибка — закрытие, отмена или таймаут (`E.IsClosedOrCanceled` включает `DeadlineExceeded` и `ECONNRESET`). Штраф, запасной дозвон и перенос выбора SPEC 054 на таймауте работают как раньше (своя классификация `isPathDeadDialError`); остаётся лишь история до следующего прогона. Контроль в `conntest_lx_test.go` переписан под это.
- **Переселект после стирания истории.** Апстрим сам переизбирает узел после ошибки дозвона — частично то, что делает SPEC 054 (перенос выбора); второго дозвона в том же соединении у апстрима по-прежнему нет, поэтому 054 держим. Переизбор идёт с `Interrupt` при смене выбора и через этот код у нас проходит каждое соединение (у апстрима роутер группу обходит). Поэтому на мёртвом пути (`pathDead`: недостижимость, у прокси-узла ещё refused и reset) апстримный переизбор не запускается — выбор переносит 054 через доказавший жизнь запасной узел, без `Interrupt`; отказ назначения на узле, который сам ходит до сайта, — апстримная реакция. Стражи — `urltest_dial_reselect_lx_test.go` через настоящий `DialContext` (красные без гейтов), и `route/conn_probe_urltest_lx_test.go` через `ConnectionManager.NewConnection`.
- **Батч пишет историю в конце.** Заполнение мёртвого слота ответившим узлом во время прогона (SPEC 019) видит слот узла, упавшего в том же батче, живым до конца батча; дыра закрывается раскладкой в конце прогона. Тест `TestRRRun_layoutSkipsNodeStruckDuringRun` ждал сигнала «у e свежая история» посреди батча — переписан: узел `c` мёртв до прогона, ответ `e` ловится по пробе.
- **Переключение без истории.** `performSelectionUpdate` переключает и тогда, когда `Select` вернул узел без истории (апстрим убрал проверку `exists`).
- **Уведомления истории.** До мержа история уведомляла подписчиков на каждую запись и удаление (каждую пробу); теперь пачка пишет молча, а публичный `URLTestOutbounds` уведомляет один раз. Наши прогоны (`testNodes`, тик, failover) зовут публичную функцию — уведомление раз на пачку, всё равно реже, чем было; на приватную `urlTestOutbounds` не переведены, иначе пришлось бы уведомлять вручную в конце каждого прогона round_robin и failover.

## 6. Зависимости (раннбук §2a) и проверки

| | lx | stable |
|---|---|---|
| Go | `go1.26.10` | 1.26.10 (`build.yml`, оба setup-скрипта) |
| `go` в `go.mod` | 1.26.0 (`lx:`) | 1.25.5 |
| `require` | наши `x/*`, `grpc` v1.83.2, `go-jose` v4.1.4 новее (как в 140/154) | — |
| `CRONET_GO_VERSION` | совпадает после мержа | `72c4e6f7…` |
| NDK | r28c (`lx:`) | r28 |
| `upstream.version` | 1.14.4 | `git describe` = v1.14.4 |

- `go build ./...` без тегов и с `with_lx_command`; полный `LX_TAGS` (+ `with_lx_idle_suspend`) под darwin, linux, windows; android/arm64 без `with_purego`/`with_naive_outbound` (им нужен cgo, как и до синка) — зелёные.
- `go vet` под полным набором по `./...` для darwin, linux, windows — чисто, кроме `protocol/impair/bench_test.go` под windows (`syscall.Getrusage`, SPEC 164, мержем не тронут).
- `go test` под `LX_TAGS` (`GOTOOLCHAIN` из `go.version`, `-checklinkname=0`): все пакеты, изменённые мержем, все пакеты списка lx-ci, `./transport/...`, `./common/...`, `./experimental/clashapi/...` — зелёные. Под `-race`: `protocol/group`, `route`, `common/urltest`, `common/dialer`, `common/tls`, `experimental/cachefile`, `dns/...`, `protocol/{dns,socks,chain,http,shadowtls,wireguard}`, `transport/{wireguard,v2rayhttpupgrade}`, `adapter/...`, `service/oomkiller`. Стенд `lx-test/initerr` (тексты ошибок инициализации, SPEC 092) зелёный. Все файлы, добавленные мерж-коммитом, — апстримные. Без тегов — `protocol/group`, `route`, `common/dialer`, `transport/wireguard`, `experimental/cachefile`. Форки: `wireguard-go` `conn`/`device`, `sing-tun` пакет целиком.
- `make -f Makefile.lx lx-check` — все конфиги `lx-test/config` проходят новую проверку `box.New` (DNS-группа, chain, AWG, XHTTP/REALITY).
- `gofmt -l` по изменённым файлам — пусто.
- Прогон по раннбуку §1.4 на NanoPi (FriendlyWrt 25.12, aarch64, 2026-11-26), бинарь ветки под `LX_TAGS` без `with_purego`/`with_naive_outbound` (на musl им нужен glibc-загрузчик). Два процесса ядра: «сервер» с WG- и AWG-endpoint'ами и socks-релеем, клиент с mixed-inbound и узлами WG напрямую, WG через `detour` (socks-релей — путь детур-бинда, который переписал апстрим) и AWG2 под urltest-группой, DNS `udp 1.1.1.1` через туннель. Три круга подряд: соединение через группу — 204, выход виден снаружи; замеры всех трёх узлов и прогон группы — 76–139 мс; DNS через туннель резолвит. Сервер погашен — все три узла `Timeout`; поднят — все три снова меряются, соединение через группу 204. Ошибок, кроме ожидаемых на старте и при выключении сервера, нет. Каталог и процессы убраны.
- Второй круг на NanoPi (бинарь головы с добивкой 161/130): round_robin (`pool: 2`) над теми же тремя узлами — 6 из 6 соединений 204, прогон группы видит все три; TUN-inbound на system-стеке без `auto_route`, в TUN направлен только `1.0.0.1/32` своим маршрутом — TCP через L3-путь в WG три круга подряд, UDP-DNS через TUN резолвит, источник переписывается штатно (серверу адреса TUN не понадобились). Петля на стенде (direct сервера на том же хосте шёл в TUN) снята `bind_interface`; маршрут, TUN, процессы и каталог убраны. Зеркало musl под новый `CRONET_GO_VERSION` прогнано (45725537285).
- Третий круг на NanoPi (с разрешения владельца): TUN с `auto_route` + `auto_redirect`, `route_address` — только `1.0.0.1/32`, сторож на 300 с. Таблица `inet sing-box` и правила 9000–9002 встали, SSH через Tailscale не задет (его правила раньше). TCP к `1.0.0.1` три круга подряд прошёл через WG (сервер принял все три): SYN идёт на суждение через NFQUEUE (`queue to 100`), по метке — в TUN; перехват DNS — DNAT на адрес TUN (новая схема апстрима `7dc9c39a2`). Остальная сеть платы мимо. При выходе ядро само сняло всё: `ip rule`/`ip -6 rule` и таблицы маршрутов побайтно как до запуска, nftables — та же структура (fw4 перезагружен, обнулились только счётчики).
- Не гонялось: Android, Windows без IPv6, linux-only тесты исполнением (только компиляция).

## 7. Хотфиксы FEATURE 004 по итогам синка

| Хотфикс | Итог |
|---|---|
| 045 | снят (апстрим `ef408a1e1`) |
| 157 | снят (апстрим `3d071e6e4`), стражи остаются |
| 161 (HTTP CONNECT и расширение) | снят целиком (socks — на мерже, остальное — по решению владельца 2026-11-26); стражи HTTP, SOCKS, httpupgrade проходят на апстримном механизме |
| 139, гонка выбора | снят: апстрим сделал поля выбора атомиками, `selectedAccess` удалён |
| 147 | FakeIP-половина снята; перехват `cache.db` держим |
| 069 | половина про порт закрыта апстримом в форке; Windows-предикат держим |
| 130 | снят (решение владельца 2026-11-26): наблюдатель реестра удалён апстримом, патчить нечего |
| 054, 125 | держим: второй дозвон и пауза тика апстримом не закрыты; строка 054 реестра поправлена — апстрим теперь переизбирает сам (§5) |
| 029, 046, 050, 085, 099, 136, 140, 163 | держим: апстрим этого не трогал; страж 085 зелёный — BND.ADDR апстрим не нормализует |

## 8. Осталось

- Форки запушены в релизные ветки по слову владельца (fast-forward): `wireguard-go` `lx-awg2-v007` → `5e0ba64`, `sing-tun` `lx` → `53e0731`; суперпроект — в `lx-sync-stable-20261126`, lx-ci 45723785661 зелёный. В `lx` вливает рецензент SPEC 138 первым, поверх — SPEC 165.
- ~~Dry run релизной матрицы~~ — lx-release 45727871619 на `768b1ceaa` (tag `v1.14.4-lx.37-rc.2-dryrun`): 13 сборок зелёные, AAR `libbox.aar`; все на go1.26.10, Win7 (`windows/386`) — на своём патченном тулчейне; publish пропущен, релиза и тега нет, Latest — `v1.14.4-lx.36`. Зеркало musl прогнано.
- Раннбук §1.4, остаток: Android, Windows без IPv6.
- Будущее влитие SPEC 165 (цепочка): этот мерж правил `protocol/chain/chain_test.go` (стенд зовёт `Initialize` в `start()`), `adapter/outbound/manager.go`, `protocol/group/selector.go` (апстримный `References()`), `route/conn.go`.
- `ALGORITHM.md` SPEC 138 получил фразу о записи истории в конце пачки (факт апстрима) — на глаза владельцу.
- Строка об апстримной базе в changelog-секции следующего тега.
