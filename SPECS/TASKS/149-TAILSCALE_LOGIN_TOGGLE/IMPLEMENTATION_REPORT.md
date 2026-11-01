# IMPLEMENTATION_REPORT: 149 — TAILSCALE_LOGIN_TOGGLE

Контракт — [SPEC.md](SPEC.md). Состояние 2026-11-02.

## Что сделано

- `adapter/tailscale_login_lx.go`: `TailscaleLoginProvider`, `ErrTailscaleDisabled`, `ErrTailscaleLoggedIn`.
- `protocol/tailscale/toggle_lx.go`: `SetEnabled`/`Enabled` (`EditPrefs` с `WantRunningSet`, флаг до выключения и после включения), `IdleState` (`up`/`disabled`/`down`), `TailscaleLogin` (`Start` с `AuthKey` и prefs старта, `editPrefs`, `StartLoginInteractive` в `NeedsLogin`), `checkEnabled`. Все переходы под `toggleAccess`.
- `protocol/tailscale/endpoint.go`, блоки `// lx:begin tailscale-toggle`: поля `disabled`, `toggleAccess`; отказ в `DialContext` после `started` (до резолва домена), в начале `ListenPacketWithDestination`, в `Logout`.
- `daemon/started_service.proto` (блок `lx_command`): `TailscaleLogin`, `TailscaleLoginRequest{endpointTag, authKey}` — имена полей в стиле соседнего апстримного `TailscaleLogoutRequest`. `*.pb.go` перегенерированы `cmd/internal/protogen`, gofumpt только по двум файлам `started_service`; шум протогена в остальных `*.pb.go` и в `submodules/gvisor` откачен.
- `daemon/started_service_tailscale_lx.go`: обработчик по SPEC §4; близнец `Unimplemented` в `_stub.go`.
- `experimental/libbox/command_client_tailscale_lx.go`: `CommandClient.TailscaleLogin(endpointTag, authKey)`.
- lx-ci: шаг `go test (tailscale endpoint, race)` — до него `protocol/tailscale` не гонялся ни одним шагом, и тесты SPEC 112/115 там тоже не бежали.

## Проверка

- `go build ./...` без тегов; `go build -tags $(LX_TAGS)` всего дерева; `daemon` и `experimental/libbox` с `LX_TAGS` без `with_lx_command` — зелёные (go1.26.10, `-checklinkname=0`).
- `go test` с `LX_TAGS`: `protocol/tailscale`, `daemon`, `experimental/libbox` — ok; `protocol/tailscale` с `-race` и с набором lx-ci (`BASE_TAGS,with_lx_command`) — ok; `daemon` без `with_lx_command`: `TestTailscaleLoginStub_LX` — PASS.
- `go vet` затронутых пакетов: новых замечаний нет (два `unsafe.Pointer` в апстримных `managed_service.go`/`debug.go` — прежние, lx-ci гоняет эти пакеты с `-unsafeptr=false`).
- AAR (`build_libbox`, `android/arm64`) против AAR той же ветки до задачи: javap всех классов `io.nekohasekai.*` отличается одной строкой — `CommandClient`: `public native void tailscaleLogin(java.lang.String, java.lang.String) throws java.lang.Exception;`. Выключение — прежний `setEndpointEnabled`.

## Не проверено

- Живой вход по ключу из `NeedsLogin` и цикл выключение → включение против control (tailnet или Headscale). Юниты покрывают отказы, коды и проброс ключа; вызовы `LocalBackend` (`Start`, `EditPrefs`, `StartLoginInteractive`) — нет.
- Поведение установленных потоков после включения (SPEC §2: должны оживать по ретрансмитам) — на живом узле.
