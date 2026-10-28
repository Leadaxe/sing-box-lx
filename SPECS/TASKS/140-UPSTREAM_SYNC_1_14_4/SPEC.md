# SPEC: 140 — UPSTREAM_SYNC_1_14_4

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` до `v1.14.4` (66 коммитов) в `lx`; два форк-сабмодуля, Go 1.26.10 |
| Статус | I (implemented) — 2026-10-28: влит в `lx` (`3ff086951`), выпуск в v1.14.4-lx.24-rc.3; сборка и тесты зелёные, на устройстве не прогонялось |
| База | до: v1.14.3-lx.24-rc.2, merge-base `d630fa94c` (v1.14.3); после: `upstream/stable` = `c005f3530` (v1.14.4), дрейф 0 |
| Связано | [117](../117-UPSTREAM_SYNC_SCOPED_LIFECYCLE/SPEC.md) (предыдущий синк), [019](../019-URLTEST_MODE_STICKY/SPEC.md)/[054](../054-URLTEST_PENALTY_FAILOVER/SPEC.md)/[116](../116-URLTEST_FAILOVER_MODE/SPEC.md) (выбор urltest на соединение), [064](../064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md)/[084](../084-INTERRUPT_GROUP_ABBA_DEADLOCK/SPEC.md) (прерывания групп), [017](../017-CONNECTION_DETOUR_CHAIN/SPEC.md) (detour-хвост), [071](../071-WG_BIND_DIAL_PAUSE_DEADLOCK/SPEC.md) (пауза WG), [130](../130-WIN7_REGISTRY_NOTIFY_THREAD_AGNOSTIC/SPEC.md) (реестр Windows 7), [139](../139-CONNECTION_TEST/SPEC.md) (проверка соединения) |

Синк откладывался на rc.1 и rc.2 линии lx.24 (записано в changelog); перед стабильным lx.24 закрыт.

## 1. Что принёс апстрим

66 коммитов от `v1.14.3` до `v1.14.4`. Значимые для нашего слоя:

| Коммит | Суть |
|---|---|
| `03335338a` Fix outbound group resolution and interruption for nested groups | Роутер сам разворачивает цепочку групп: `resolveOutbound` по `Selected(network)`, цепочка в `metadata.OutboundChain`, соединение отдаётся конечному узлу. Каждой группе цепочки соединение прикрепляется для прерываний (`AttachConnection`). У `OutboundGroup` удалён `Now()`, добавлены `Selected(network)` и `AttachConnection`; у `Selector`/`URLTest` удалены `NewConnection`/`NewPacketConnection`; `RealTag(detour, network)`; трекер строит `Chain` из `OutboundChain`, у менеджера трафика нет ссылки на менеджер outbound'ов |
| `5372a195b` Fix URL test failover for routed connections | `ConnectionManager` при ошибке dial удаляет историю urltest для последнего звена цепочки |
| `cc022098f` Fix WireGuard pause and sleep handling | пауза WG применяется асинхронно по текущему состоянию (`IsNetworkPaused`), не по событию |
| `db4e5da71` Check DNS address filter rules on cache hit | ответ из кэша проверяется `responseChecker`, отказ — `ErrResponseRejectedCached` |
| `2ca6ab970` Update Go to 1.26.10 | тулчейн |
| `760f1edce`, `2054998ce`, `9c1efcc18`, `85157f4c2` и др. | direct и fallback delay, порядок адресов в параллельном dial, длина reserved WG, усечённый IPv6-адрес отправки WG |

Модули: `sing`, `sing-tun`, `wireguard-go`, `quic-go` mod.12, `sing-quic`, `sing-vmess`, `sing-openvpn`, `sing-cloudflared`, `gliderssh`, `tailscale`.

## 2. Сабмодули (раннбук §1, до ядра)

| Форк | Пин апстрима | Мерж в форк | Конфликты |
|---|---|---|---|
| `wireguard-go` (`lx-awg2-v007`) | `e4d85ef` (3 коммита: сон в возрасте ключей и хендшейков, пауза вне замков таймеров, усечённый IPv6-адрес отправки) | `7188e15` | 9 строк в `device/{device,receive,send}.go`: апстрим заменил `time.Since`/`Add().Before` на `sinceIncludingSleep`, у нас в тех же строках таймауты AWG 3.x (`keychainExpireTime`, `keyRefreshTimeout*`, `rekeyMinTimeout`). Взяты обе правки: `sinceIncludingSleep` с нашими таймаутами |
| `sing-tun` (`lx`) | `040db71` (11 коммитов) | `1cb218d` | `stack_system.go`: наш `tcpPort` атомарный (SPEC 040) + новый аргумент `LookupBack(port, flags)` апстрима |

`sing-tun`, SPEC 130: апстрим `fe26f6a` убрал `REG_NOTIFY_THREAD_AGNOSTIC` из фильтра на всех версиях Windows. Наш повтор без флага на Windows 7 стал мёртвым и снят; регистрация всех уведомлений на закреплённом потоке цикла осталась (без флага уведомление отменяется вместе с регистрирующим потоком, апстрим этого не учитывает). `gvisor` и `utls` пины не меняли. `go list -m`: все четыре модуля на `./submodules/*`.

## 3. Ядро: группы на новом пути маршрутизации

Главный риск синка — молчаливый. После `03335338a` роутер больше не зовёт `DialContext` группы для маршрутизируемого соединения, а наш urltest выбирает узел именно там — во всех режимах:

- least_test: штрафы и переход на другой узел в том же соединении при ошибке dial (SPEC 054);
- `round_robin` и sticky: слот и ключ на соединение (SPEC 019, 138);
- `failover` (SPEC 116);
- стадия `group` проверки соединения (SPEC 139).

Сборка и тесты этого не видят. Решение:

| Место | Что сделано |
|---|---|
| `route/group_resolve_lx.go` | `resolveOutboundLx` — обёртка над апстримным `resolveOutbound` для `routeConnection`/`routePacketConnection`: цепочка обрезается после первой группы с маркером `SelectsPerConnectionLx()` (urltest), соединение уходит в её `DialContext`, как до синка. Группы до обрезки получают `AttachConnection`. Проверка сети остаётся на узле, который группа выбрала бы сейчас. Pre-match flow (`preMatchFlow`) идёт через апстримный `resolveOutbound` — он и раньше проходил группы насквозь через `Now()`. Вставка в `route.go` — 2 изменённые строки |
| `protocol/group/selector.go` | форма апстрима. Наша регистрация входящего соединения в `Selector.NewConnection` (SPEC 064) снята: роутер прикрепляет соединение к каждой группе цепочки сам. Тесты 064 и 084 (ABBA-дедлок вложенных селекторов) переведены на тот же путь (`routeLikeRouter` в тесте) и зелёные |
| `protocol/group/urltest.go` | `Selected(network)`: в сбалансированных режимах — последний выбор (`lastSelected`), иначе `selectedLx()` (хотфикс гонки) с фолбэком на `Select`; `AttachConnection` — апстрима; `Now()` удалён |
| `adapter/group_lx.go` | `GroupNow(group)` — выбор для TCP, иначе для UDP: одна сетенезависимая точка для lx-читателей (reachability SPEC 020, chain SPEC 073, статус-RPC, трасса SPEC 139) вместо удалённого `Now()` |
| `common/trafficcontrol` | `tracker.go` строит `Chain` из `OutboundChain` (апстрим) через `displayChainLx`: группа, на которой роутер остановился, спускается до текущего выбора для отображения, как это делал `Now()`; иначе LxBox и Clash API показывали бы группу вместо узла. Обход detour-хвоста (SPEC 017) вынесен в `tracker_lx.go`; ссылку на менеджер outbound'ов менеджеру трафика даёт `box.go` (`SetOutboundManagerLx`) |
| `route/conntest_lx.go` | `dial` первого хопа берёт пройденные группы (`via_group`) из `OutboundChain` |

Остальные разрешения:

| Файл | Разрешение |
|---|---|
| `transport/wireguard/endpoint.go` | структура SPEC 071 (асинхронное применение, побеждает последний штамп, `pauseOpAccess`) сохранена, тело применяет текущее состояние паузы (`IsNetworkPaused`) по апстриму; страж SPEC 020 для усыплённого устройства остался; апстримный `stateAccess` не нужен |
| `dns/client.go` | форма апстрима (проверка ответа из кэша) + наши аргументы `transport` в логе (SPEC 018), `cache_age` (SPEC 139); отказ ответа из кэша шлёт `emitFailedQuery` «rejected (cached)», как ветка rdrc — иначе он пропадал бы из потока DNS и трассы |
| `common/dialer/default.go` | апстрим убрал помощник `listenSerialInterfacePacket`; стадия `udp` (SPEC 139) обёрнута вокруг новых трёх строк |
| `daemon/started_service.go` | `Selected(N.NetworkTCP)` апстрима + наш `Mode` |
| `protocol/wireguard/endpoint.go` | обе стороны (наш idle-suspend + апстримный `MultiplexEnabled`) |
| `protocol/chain/chain.go` | `ContextWithIsExternalConnection(ctx, true)` — новая сигнатура |

Автослияние: в каждом конфликтном файле все добавленные апстримом строки есть в результате, кроме заменённых нами намеренно (сверка по строкам `+` диффа `d630fa94c..upstream/stable`).

## 4. Зависимости (раннбук §2a)

| | lx | stable |
|---|---|---|
| Go | `go1.26.10` (`go.version`) | 1.26.10 |
| `go`/`toolchain` | 1.25.5 | 1.25.5 |
| `require` | наши `grpc` v1.83.1, `go-jose` v4.1.4, `genproto` | v1.79.1, v4.1.3 |
| `CRONET_GO_VERSION` | совпадает | |
| NDK | r28c (`lx:` в `lx-release.yml`) | r28 |
| `upstream.version` | 1.14.4 | v1.14.4 |

Алерты Dependabot (`grpc` < 1.83.2 — падение xDS-сервера; `chi` < 5.4.0 — `ClientIPFromXFF`) не закрыты: ни xDS, ни `ClientIPFromXFF` в коде не используются, а оба бампа тянут `golang.org/x/{net,crypto,text,mod}` новее апстрима — `x/net` лежит под http2 всех транспортов. Ждём бампа в апстриме.

## 5. Проверки

- `go build ./...` без тегов; `LX_TAGS` — darwin, `GOOS=linux`, `GOOS=windows`; `GOOS=android` libbox с тегами AAR (`with_lx_idle_suspend`, без naive/purego/clash_api).
- `go test -race` с `LX_TAGS`: `protocol/group/...`, `route/...`, `common/{trafficcontrol,interrupt,dialer,lxtrace}`, `dns/...`, `transport/wireguard`, `protocol/{wireguard,chain}`, `daemon`, `experimental/{clashapi,libbox}`, `adapter/...`, `transport/v2rayxhttp`; с тегами шагов lx-ci (`with_lx_idle_suspend`): `transport/wireguard`, `protocol/wireguard`, `route`, `adapter/endpoint`, `common/lxclosewatch`, `lxd`, `cmd/sing-box`.
- Стенды `lx-test/{conntest,chain,startclose}`; `make -f Makefile.lx lx-check`.
- Новый тест `route/group_resolve_lx_test.go`: маршрутизируемое соединение останавливается на urltest-группе и проходит селектор насквозь.
- Тестовые заглушки групп в `route`, `protocol/chain`, `common/lxtrace` переведены на `Selected`/`AttachConnection`: со старым `Now()` они перестали быть группами, и тесты `route` проходили случайно.

## 6. Осталось

- Прогон на устройстве по раннбуку §1.4: urltest во всех режимах через роутер (выбор, переход при ошибке dial, round_robin), переключение вложенных селекторов с `interrupt_exist_connections`, соединения в LxBox показывают узел, а не группу; сон и пробуждение WG, смена сети.
