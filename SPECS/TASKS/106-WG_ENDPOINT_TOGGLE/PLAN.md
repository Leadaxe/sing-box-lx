# PLAN: 106 — WG_ENDPOINT_TOGGLE

Контракт — [SPEC.md](SPEC.md).

## Этап 1. Ядро

1. `adapter/idle_suspend_lx.go`: интерфейс `EndpointToggle`, константа `EndpointStateDisabled`.
2. `protocol/wireguard/endpoint_toggle_lx.go`: `SetEnabled`/`Enabled` по таблице SPEC §2, ошибки `errEndpointDisabled`/`errEndpointNotReady`, `notReadyError()`.
3. `protocol/wireguard/endpoint.go` (`// lx:`): поле `disabled` в блоке idle-suspend; ранний выход в `SuspendIfIdle`; проверка в `resumeOnDial` сразу после `stampActivity` и повторно под `resumeMu` (дайл, прошедший первую проверку до выключения, не должен разбудить узел); пять отказов дайла через `notReadyError()`.
4. `protocol/wireguard/endpoint_lazy_lx.go`: `disabled` в `IdleState()` после `building` и `closing`.
5. `transport/wireguard/endpoint_suspended_lx.go`: `Suspended()`.

Бюджет (`TeardownForBudget`) не меняется: выключенный узел с собранным устройством уже `idleAsleep && !started && !torndown`, то есть кандидат.

## Этап 2. RPC

1. `daemon/started_service.proto`: RPC и сообщения в блоке `lx_command`, комментарий `GroupItem.endpointState`.
2. `make -f Makefile.lx lx-proto`; из изменений оставить только `daemon/started_service.pb.go` и `daemon/started_service_grpc.pb.go`, остальное (gofumpt по дереву и сабмодулям) откатить.
3. `daemon/started_service_endpoint_lx.go` по образцу `started_service_chain_lx.go`: `endpointManager.Get(tag)`, коды по SPEC §3, `state` из `IdleStateReporter`. Близнец `_stub.go` с `Unimplemented`.

## Этап 3. Тесты и сборка

- Юниты по SPEC §7.
- `go test -ldflags "-checklinkname=0"` пакетов `protocol/wireguard`, `transport/wireguard`, `daemon`, `adapter`, `route` с тегами `LX_TAGS` + `with_lx_idle_suspend`, с `LX_TAGS` и без тегов; `-race` для `protocol/wireguard`, `transport/wireguard`.
- `go build ./...` без тегов; `LX_TAGS` без `with_lx_command`; `LX_TAGS` + `with_lx_idle_suspend`; `make -f Makefile.lx lx-check`.
- gofmt новых и тронутых файлов.

## Этап 4. Доки

FEATURE 008 (состояние, RPC, строка задачи), индекс FEATURES (номер задачи), FEATURE 006 (вызов), `lx-energy(.ru).md` (состояние, раздел о переключателе), `lx-config(.ru).md` §8 (состояние), `lxd-grpc-api(.ru).md` (список RPC блока `lx_command`), Roadmap в `SPECS/README.md`, секция changelog.
