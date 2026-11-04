# SPEC: 154 — UPSTREAM_SYNC_1_14_4_PLUS_9

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` v1.14.4 + 9 коммитов без тега в `lx`; один форк-сабмодуль |
| Статус | I (implemented) — 2026-11-05: влит в `lx` (`eb1c58d1d`); сборка и тесты зелёные, на устройстве не прогонялось; тег не срезан |
| База | до: v1.14.4-lx.30 + 7, merge-base `c005f3530` (v1.14.4); после: `upstream/stable` = `8eb4395a3` (2026-11-03), дрейф 0 |
| Связано | [140](../140-UPSTREAM_SYNC_1_14_4/SPEC.md) (предыдущий синк), [052](../052-NETSTACK_CONNECT_DEADLINE/SPEC.md) (таймаут connect-фазы в gVisor-стеках), [046](../046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md) (DNS-hijack и пакетный цикл), [040](../040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md)/[130](../130-WIN7_REGISTRY_NOTIFY_THREAD_AGNOSTIC/SPEC.md) (патчи форка `sing-tun`), [023](../023-MUSL_TOOLCHAIN_MIRROR/SPEC.md) (зеркало musl при бампе cronet) |

## 1. Что принёс апстрим

9 коммитов от `c005f3530` (v1.14.4) до `8eb4395a3`, все от 2026-11-03; нового тега апстрима нет, `upstream.version` остаётся `1.14.4`.

| Коммит | Суть | Наш слой |
|---|---|---|
| `04b42a812` Fix DNS connection setup blocking TUN packet processing | `dns/transport/multiplexer.go`: `exchangeAsync` уходит в горутину — установка соединения DNS-транспорта больше не держит пакетный цикл TUN | автослияние; наших правок в файле нет. Родственно SPEC 046 (там чинился блокирующий `ExchangeAsync` в hijack-пути `route`), апстримный фикс ниже — в мультиплексоре транспорта |
| `a0bd90942` Fix concurrent DNS request packing mutating shared message | `exMessage := message.Copy()` вместо `*message` в https/h3/tcp/udp и libbox: параллельная упаковка одного запроса портила общее сообщение | автослияние; наши маркеры `// lx:` в `https.go` и `quic/http3.go` (SPEC 018, 139) не задеты |
| `a9c21da53` Close UDP sockets when endpoint addresses change | gVisor-стеки OpenVPN и OpenConnect: `bindAddressLocked` под `stateAccess`, UDP-соединения в `interrupt.Group`, закрываются при смене адресов туннеля | **конфликт**, §3 |
| `2d662a310` Fix strict route without kernel IPv6 support, `9474fb05f` Fix TUN default route with RAS connections on Windows | бамп `sing-tun` → `620991cbdec9` | форк, §2 |
| `447cb47a3`, `a62f8f1f5`, `606ec5f06` | GPST reauth на HTTP 200, AnyConnect CSD, парсинг опций F5 — бамп `sing-openconnect` | — |
| `8eb4395a3` Update naiveproxy to v154.0.8037.49-2 | бамп `cronet-go`, `.github/CRONET_GO_VERSION` → `095cb9e0…` | зеркало musl-тулчейна перезапускается вручную (SPEC 023) |

## 2. Сабмодули (раннбук §1, до ядра)

| Форк | Пин апстрима | Мерж в форк | Конфликты |
|---|---|---|---|
| `sing-tun` (`lx`) | `620991c` (2 коммита: strict route без IPv6 в ядре, маршрут по умолчанию при RAS на Windows; `tun_bind_windows.go` → `tun_interface_windows.go`) | `94280f6` | нет; дельта форка после мержа — `monitor_windows.go` (SPEC 130), `stack_system.go` + тест (SPEC 040), как и была |

`wireguard-go`, `gvisor`, `utls` — пины не менялись. `go list -m`: все четыре модуля на `./submodules/*`.

## 3. Ядро: конфликт в gVisor-стеках OpenVPN/OpenConnect

`transport/openvpn/device_stack.go` и `transport/openconnect/device_stack.go`, одинаковая пара мест:

- блок импортов — наш `C "…/constant"` (SPEC 052) против апстримного `"…/common/interrupt"`: оставлены оба;
- ветка `case N.NetworkTCP` в `DialContext` — апстрим заменил расчёт `bind`/`networkProtocol` в теле на `bindAddressLocked(destination)` под `stateAccess.RLock()` и диалит с голым `ctx`; у нас в тех же строках `connectCtx` с `C.TCPTimeout` (иначе ~127 с SYN-бэкоффа gVisor). Взята форма апстрима, поверх неё наш `connectCtx` вместо `ctx` в `gonet.DialTCPWithBind`; маркер `// lx: SPEC 052` на месте.

UDP-ветка (`interrupt.Group`) — чисто апстримная, наших правок там не было. `go.sum` — объединение обеих сторон + `go mod tidy`; `go.mod` слился сам.

## 4. Зависимости (раннбук §2a)

| | lx | stable |
|---|---|---|
| Go | `go1.26.10` (`go.version`) | 1.26.10 |
| `go`/`toolchain` в `go.mod` | 1.26.0 (`lx:`, под `x/net` v0.62.0) | 1.25.5 |
| `require` | наши `x/{net,crypto,sys,…}`, `grpc` v1.83.2, `go-jose` v4.1.4 новее (как в SPEC 140 §4) | — |
| `CRONET_GO_VERSION` | совпадает после мержа | `095cb9e0…` |
| NDK | r28c (`lx:` в `lx-release.yml`) | r28 |
| `upstream.version` | 1.14.4 | `git describe` = v1.14.4 |

## 5. Проверки

- `go build` с полным `LX_TAGS` (`Makefile.lx lx-print-tags`), darwin/arm64 — зелёный.
- `go test` с `LX_TAGS`: `dns/...`, `dns/transport/...` (включая fresh-тесты lx у https/h3), `common/tls` (страж `replace` utls), `route`, `transport/{openvpn,openconnect}` (без тестов) — зелёные. `gofmt -l` по lx-файлам раннбука и обоим device_stack — пусто.
- Полный релизный dry-run не гонялся: тулчейн и наши пины не менялись; бамп cronet проверит зеркало SPEC 023.

## 6. Осталось

- Запустить `lx-musl-toolchain-mirror.yml` вручную после пуша (SPEC 023) — новый `CRONET_GO_VERSION`.
- Раннбук §1.4: прогон на устройстве после смены сабмодуля — TUN поднимается, DNS резолвит, urltest меряет; OpenVPN/OpenConnect через смену сети (новая логика закрытия UDP-сокетов апстрима).
- Строка об апстримной базе в changelog-секции следующего тега.
