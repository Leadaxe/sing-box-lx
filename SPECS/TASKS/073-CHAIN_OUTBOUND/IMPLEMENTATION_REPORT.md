# IMPLEMENTATION REPORT 073 — outbound `chain`

Дата: 2026-08-22. Статус: **I** — код, юниты и приёмочный стенд зелёные; полевой проверки
WG-звеньев на устройстве не было.

## Что сделано (по этапам PLAN)

| # | Этап | Результат |
|---|------|-----------|
| 1 | Опции, константа, include-гейт, `Chain`/hop | `option/chain_lx.go`, `constant/chain_lx.go`, `include/lx_chain{,_stub}.go`, `protocol/chain/{chain,hop}.go`; тег `with_lx_chain` в `Makefile.lx` и `build_libbox` |
| 2 | Звенья через реестр | менеджеры помнят опции (`OptionsOf`), внутренний реестр хопов (`AddInternal/RemoveInternal`, видим `Outbound(tag)`, не в `Outbounds()`); фабрика `protocol/chain/clone.go`: копия опций → strip → rewrite → MTU → `detour=<tag>#(i−1)` → реестр → стадии, singleflight, счётные обёртки conn, логгер `chain[tag]#i/<leaf>`, pprof-метки |
| 3 | Группы | `adapter/chain_lx.go` (привязка, `ResolveChainLeaf`); хук в `selector.go` ×2, `urltest.go` ×2, `urltest_penalty_lx.go` ×1 |
| 4 | direct/block | passthrough для `direct` на позициях ≥ 1, `block` терминален, `dns`/вложенный `chain` отвергаются |
| 5 | Жизненный цикл | прогрев детерминированных позиций (селекторы да, urltest нет), эвикшн по `idle_timeout` при нуле соединений, `CloneEndpoints()` в idle-тике (`route/reachability_lx.go`) и смене сети (`route/network.go`), закрытие от выхода ко входу |
| 6 | MTU | `protocol/chain/mtu.go`: WG −60/−80 по семейству адреса пира, MASQUE ≈ −90, min по группе ниже, потоковые/датаграммные — ∞, предупреждение tuic-native |
| 7 | strip/rewrite | `protocol/chain/transform.go`: каталог как патчи JSON-карты, карта `strip`, merge-patch RFC 7396 по типу, сухой прогон на старте, `tls.utls`×`reality` — отказ |
| 8 | Наблюдаемость | `ChainPath()` → `detourList` (tracker), `ChainStatus()` → RPC `GetChains` (proto §3.6 + handler/stub + libbox-клиент) и Clash API `/proxies/<tag>`.`chain`, ошибки с позициями, логи create/evict |
| 9 | Приёмка | юниты `protocol/chain/chain_test.go` (фейковые узлы, 13 тестов с подтестами, `-race` зелёный), стенд `lx-test/chain` на живых shadowsocks-хопах (три слоя + direct в середине на лету), `sing-box check lx-test/config/chain_basic.json`, `make -f Makefile.lx lx-build`, дока §9 EN/RU, changelog |

## Отклонения от SPEC/PLAN

- Хопы регистрируются как **внутренние** outbound'ы (`AddInternal`), а не через
  `Manager.Create` — без стадий менеджера и без попадания в списки (как и планировалось
  в «ключевых решениях»).
- `OptionsOf`/`AddInternal` — не в upstream-интерфейсах `OutboundManager`/`EndpointManager`,
  а отдельные lx-интерфейсы в `adapter/chain_lx.go`, обнаруживаемые type-assertion'ом:
  меньше маркеров в upstream-файлах (интерфейсы в `adapter/outbound.go`/`endpoint.go` не
  тронуты).
- `ChainInfo` — отдельный RPC `GetChains`, а не поле в `OutboundInfo`: `SubscribeOutbounds`
  собирается в upstream-файле, расширять его shape не хотелось; дока отражает.
- Лог «path changed» не реализован (требовал бы опроса); путь виден в `ChainPath()`/RPC.
- WG-over-WG на живом стенде не гонялся (нет пары); MTU-правила покрыты юнитами на
  настоящих `WireGuardEndpointOptions`.

## Ребейз-цена (факт)

Upstream-файлы с маркером `lx:begin chain`: `adapter/outbound/manager.go` (+21),
`adapter/endpoint/manager.go` (+8), `protocol/group/selector.go` (2 хука),
`protocol/group/urltest.go` (2 хука), `common/trafficcontrol/tracker.go` (ветка в lx-зоне
SPEC 017), `route/network.go` (обход звеньев), `experimental/clashapi/proxies.go` (поле),
`include/registry.go` (регистрация), `cmd/internal/build_libbox/main.go` (тег),
`Makefile.lx` (тег), `daemon/started_service.proto` (rpc + 4 message под `lx_command`) +
регенерированные `started_service{,_grpc}.pb.go`. lx-файлы: `route/reachability_lx.go`,
`protocol/group/urltest_penalty_lx.go` — без маркеров.

## Проверки

- `go test -race ./protocol/chain/` — ok; `go test ./protocol/group/ ./adapter/... ./route/` — ok;
  `go test -tags with_lx_command,… ./daemon/ ./experimental/libbox/` — ok.
- `go test -tags with_lx_chain,… ./lx-test/chain/` — ok (e2e через три ss-хопа + direct в середине).
- `make -f Makefile.lx lx-build` — ok; `./sing-box check -c lx-test/config/chain_basic.json` — ok.
- Сборка без `with_lx_chain`: `include`/`protocol`/`route`/`daemon` — ok (стаб отвергает `type: chain`).

## Остаток

- Полевая проверка: WG-звено над реальным хопом (хендшейк через `detour=<tag>#i`, MTU), поведение
  при переключении селектора с `interrupt_exist_connections` под WG-звеном, idle-suspend звена.
- UI (LxBox/лаунчер): потребление `GetChains` и `detourList` цепочки.
