# PLAN: 149 — TAILSCALE_LOGIN_TOGGLE

Контракт — [SPEC.md](SPEC.md).

## Этап 1. Ядро

1. `adapter/tailscale_login_lx.go`: `TailscaleLoginProvider { TailscaleLogin(ctx, authKey) error }`, `ErrTailscaleDisabled`, `ErrTailscaleLoggedIn`.
2. `protocol/tailscale/toggle_lx.go`: `SetEnabled`/`Enabled` по SPEC §2 (под `toggleAccess`, `EditPrefs` с `WantRunningSet`), `IdleState` по §2, `TailscaleLogin` по §3 (под `toggleAccess`, `State()` → отказы, `Start` с `AuthKey`, `editPrefs`, `StartLoginInteractive` в `NeedsLogin`), `checkEnabled()` для дайлов.
3. `protocol/tailscale/endpoint.go` (`// lx:`): поля `disabled atomic.Bool`, `toggleAccess sync.Mutex`; `checkEnabled()` в `DialContext` после проверки `started` и в начале `ListenPacketWithDestination` (до резолва домена); отказ в `Logout` выключенного.

## Этап 2. RPC

1. `daemon/started_service.proto`: `TailscaleLogin` и `TailscaleLoginRequest` в блоке `lx_command`.
2. `make -f Makefile.lx lx-proto`; оставить только `daemon/started_service.pb.go` и `daemon/started_service_grpc.pb.go`, шум protogen и gofumpt по дереву и сабмодулям откатить.
3. `daemon/started_service_tailscale_lx.go`: обработчик по SPEC §4 (`resolveTailscaleEndpoint`, `TailscaleLoginProvider`, коды по ошибкам); близнец в `_stub.go`.
4. `experimental/libbox/command_client_tailscale_lx.go`: `CommandClient.TailscaleLogin`.

## Этап 3. Тесты и сборка

- Юниты по SPEC §8.
- `go test -ldflags "-checklinkname=0"` пакетов `protocol/tailscale`, `daemon`, `experimental/libbox` с `LX_TAGS`; `daemon` и без `with_lx_command`.
- `go build ./...` без тегов; `make -f Makefile.lx lx-build`; AAR через `build_libbox` и javap `CommandClient`.
- gofmt новых и тронутых файлов; пакеты в списке lx-ci.

## Этап 4. Доки

FEATURE 006 (вызов и строка задачи), Roadmap в `SPECS/README.md`, `lxd-grpc-api(.ru).md` (список RPC блока `lx_command`), секция changelog при релизе.
