# IMPLEMENTATION_REPORT: 115 — TAILSCALE_PEER_PATH_STATUS

Ветка `lx`, не выпущена (идёт в `v1.14.2-lx.12-rc.2` вместе с переездом SPEC 114).

## Коммиты

| Коммит | Что |
|---|---|
| `93272b057` | proto: RPC, запрос, поля 18–22, `health`, enum; регенерация; `adapter/tailscale_status_lx.go` + `lx:`-блоки в `adapter/tailscale.go`; `daemon/started_service_tailscale_lx.go` + `_stub.go` + тест; `daemon/tailscale_path_lx.go`; `lx:`-блок в `tailscalePeerToProto` (общий коммит с SPEC 114) |
| `04d2a598e` | `protocol/tailscale/status_lx.go` (общий сборщик, `TailscaleStatus()`, вердикт) + тест; три `lx:`-строки в `status.go`; CLI `api tailscale peers` |
| `8c5e23f16` | libbox `GetTailscaleStatus`, поля, `Health()` + тест (общий коммит с SPEC 114) |
| (этот) | SPEC, PLAN, TASKS, CONSUMERS, отчёт; FEATURE 006; Roadmap; `lxd-grpc-api(.ru).md`; changelog |

## Файлы

- **Новые:** `adapter/tailscale_status_lx.go`, `protocol/tailscale/status_lx.go`, `daemon/started_service_tailscale_lx.go`, `daemon/started_service_tailscale_lx_stub.go`, `daemon/tailscale_path_lx.go`, `experimental/libbox/command_client_tailscale_lx.go`, `cmd/sing-box/cmd_api_tailscale_peers_lx.go`; тесты `protocol/tailscale/status_lx_test.go`, `daemon/started_service_tailscale_lx_test.go`, `experimental/libbox/command_client_tailscale_lx_test.go`.
- **`// lx:` правки апстрима:** `daemon/started_service.proto` (+ `.pb.go`), `adapter/tailscale.go`, `protocol/tailscale/status.go`, `daemon/started_service.go`, `experimental/libbox/command_types_tailscale.go`.
- Форк tailscale, сабмодули, `go.mod`, `option/` не тронуты.

## Проверки

- `go build ./...` без тегов; `go build` / `go vet` с `LX_TAGS` — ок.
- `go test -race` с `LX_TAGS`: `protocol/tailscale` (вердикт, health), `daemon`, `experimental/libbox` — зелёные.
- Живого tailnet-прогона не было: ядро само тайлнет не поднимает, прогон — через лаунчер после rc.2 (сессия лаунчера «Пути пиров и соединений в Tailscale» ждёт тег).

## Зона конфликтов при синке

- `daemon/started_service.proto` / `.pb.go` — если апстрим добавит поля в `TailscalePeer` (18+) или `TailscaleEndpointStatus` (16+); при конфликте перенумеровать наши и регенерировать.
- `protocol/tailscale/status.go` — три `lx:`-строки в `SubscribeTailscaleStatus` / `convertTailscalePeer` / `convertTailscaleStatus`; если апстрим перепишет сборку снимка, перенести вызов `collectTailscaleStatus`.
