# IMPLEMENTATION_REPORT: 149 — TAILSCALE_LOGIN_TOGGLE

Контракт — [SPEC.md](SPEC.md). Состояние 2026-11-02.

## Что сделано

- `adapter/tailscale_login_lx.go`: `TailscaleLoginProvider`, `ErrTailscaleDisabled`, `ErrTailscaleLoggedIn`.
- `protocol/tailscale/toggle_lx.go`: `SetEnabled`/`Enabled` (`EditPrefs` с `WantRunningSet`, флаг до выключения и после включения), `IdleState` (`up`/`disabled`/`down`), `TailscaleLogin` (`Start` с `AuthKey` и prefs старта, `editPrefs`, `StartLoginInteractive` в `NeedsLogin`), `checkEnabled`. Все переходы под `toggleAccess`.
- `protocol/tailscale/endpoint.go`, блоки `// lx:begin tailscale-toggle`: поля `disabled`, `toggleAccess`; отказ в `DialContext` после `started` (до резолва домена), в начале `ListenPacketWithDestination`, в `Logout`.
- `daemon/started_service.proto` (блок `lx_command`): `TailscaleLogin`, `TailscaleLoginRequest{endpointTag, authKey}` — имена полей в стиле соседнего апстримного `TailscaleLogoutRequest`. `*.pb.go` перегенерированы `cmd/internal/protogen`, gofumpt только по двум файлам `started_service`; шум протогена в остальных `*.pb.go` и в `submodules/gvisor` откачен.
- `daemon/started_service_tailscale_lx.go`: обработчик по SPEC §5; близнец `Unimplemented` в `_stub.go`.
- `experimental/libbox/command_client_tailscale_lx.go`: `CommandClient.TailscaleLogin(endpointTag, authKey)`.
- lx-ci: шаг `go test (tailscale endpoint, race)` — до него `protocol/tailscale` не гонялся ни одним шагом, и тесты SPEC 112/115 там тоже не бежали.

- Taildrop (SPEC §4): опция `disable_taildrop` (`option/tailscale.go`, поле `taildropDisabled` endpoint'а); отказ 403 в `handleTaildropRequest` до апстримных проверок, `ErrTaildropDisabled` первым в `SendTaildropFile`, `false` в `taildropTargets`; карта `senderIDs` рядом с `senderNames` во всех четырёх местах её изменения; `senderID = 5` в `TaildropFile` (блок `lx_command`, регенерация только `started_service.pb.go`), `ErrTaildropDisabled` → `FailedPrecondition` в `taildropError`, `SenderID` в daemon и libbox.

## Проверка

- `go build ./...` без тегов; `go build -tags $(LX_TAGS)` всего дерева; `daemon` и `experimental/libbox` с `LX_TAGS` без `with_lx_command` — зелёные (go1.26.10, `-checklinkname=0`).
- `go test` с `LX_TAGS`: `protocol/tailscale`, `daemon`, `experimental/libbox` — ok; `protocol/tailscale` с `-race` и с набором lx-ci (`BASE_TAGS,with_lx_command`) — ok; `daemon` без `with_lx_command`: `TestTailscaleLoginStub_LX` — PASS.
- `go vet` затронутых пакетов: новых замечаний нет (два `unsafe.Pointer` в апстримных `managed_service.go`/`debug.go` — прежние, lx-ci гоняет эти пакеты с `-unsafeptr=false`).
- Taildrop: `go test` с `LX_TAGS` `protocol/tailscale` (и с `-race`), `daemon`, `experimental/libbox` — ok; `daemon` без тегов — ok; `go build ./...` без тегов и с `LX_TAGS` — ok; `go vet` `protocol/tailscale`, `option`, `adapter` — чисто. `sing-box check` принимает `disable_taildrop: true`, опечатку в ключе отвергает `unknown field`.
- AAR (`build_libbox`, `android/arm64`) против AAR той же ветки до задачи: javap всех классов `io.nekohasekai.*` отличается одной строкой — `CommandClient`: `public native void tailscaleLogin(java.lang.String, java.lang.String) throws java.lang.Exception;`. Выключение — прежний `setEndpointEnabled`.

## Живая проверка

- Локальный control (`testcontrol` из `tailscale.com` v1.102.1 на 127.0.0.1, ключ обязателен для каждой регистрации), box в процессе на коде rc.1: Running → `Logout` → `NeedsLogin` → `TailscaleLogin` → вход; старт с отвергнутым ключом → `NeedsLogin` → `TailscaleLogin` с правильным → вход. В trace ключ уходит в `RegisterReq` (`doLogin(regen=true)`), `StartLoginInteractive` вход по ключу не перебивает — та же последовательность, что у старта tsnet и `tailscale up --auth-key`.
- LxBox, `v1.14.4-lx.30-rc.1`, arm64 AVD (Android 14), login.tailscale.com, 2026-11-02: `Logout` → `TailscaleLogin(ключ)` → Running ≈2 с без reload; узел с отвергнутым ключом (`invalid key: unable to validate API key`) → `TailscaleLogin(действующий)` → Running ≈2 с; `SetEndpointEnabled` off → `disabled`/Stopped, on → `up`/Running, off→on ≈20 мс.
- Ложная тревога по ходу: первые неудачи давал обрезанный ключ (`adb input text` терял символы — в узел попали 55 и 20 символов), control отвечал `invalid key: API key does not exist`. Контрольный reload ключ не проверял: узел с сохранённым node key (`Persist{n=…}`, `regen=false`) входит без auth key. Ключ проверяет только регистрация нового node key — на это смотреть при разборе подобных жалоб.

## Не проверено

- Taildrop §4 на живом tailnet: отказ у отправителя, скрытие в UI, `senderID` у полученного файла. AAR с полем `senderID` не собирался, javap не снят.

- Поведение установленных потоков после включения (SPEC §2: должны оживать по ретрансмитам).
- Одна ранняя неудача LxBox с полным ключом (61 символ) не воспроизвелась.
