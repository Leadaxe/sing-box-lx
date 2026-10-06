# IMPLEMENTATION_REPORT: 114 — WG_PEER_STATUS

Ветка `lx`, не запушена, не выпущена.

## Коммиты

| Коммит | Что |
|---|---|
| `ade25726a` | `adapter/peer_status_lx.go`; `transport/wireguard/peer_status_lx.go` (разбор UAPI под `pauseOpAccess`); `protocol/wireguard/peer_status_lx.go` (страж `building`); тесты разбора и живой пары |
| `e0b4a8575` | `started_service.proto`: `GroupItem.peers = 7` в `lx:`-блоке, `PeerStatus` в конце файла; регенерация `started_service.pb.go` |
| `11a4339e6` | `GetOutbounds` заполняет `peers`; конверсия в `daemon/started_service_peers_lx.go`; тест |
| `6e18dfbbe` | libbox: поле `peers` под `lx:`-блоком в `command_types.go`, `PeerStatus`/`PeerStatusIterator`/`Peers()` в новом файле; тест |
| `d741b90ee` | CLI `sing-box api peers [тег]` |
| (этот) | SPEC, CONSUMERS, PLAN, TASKS, отчёт; FEATURE 006; Roadmap; `lxd-grpc-api(.ru).md` |

## Файлы

- **Новые:** `adapter/peer_status_lx.go`, `transport/wireguard/peer_status_lx.go`, `protocol/wireguard/peer_status_lx.go`, `daemon/started_service_peers_lx.go`, `experimental/libbox/command_types_peers_lx.go`, `cmd/sing-box/cmd_api_peers_lx.go`; тесты `transport/wireguard/peer_status_lx_test.go`, `transport/wireguard/peer_status_live_lx_test.go` (`with_gvisor`), `daemon/started_service_peers_lx_test.go`, `experimental/libbox/command_types_peers_lx_test.go`.
- **`// lx:` правки апстрима:** `daemon/started_service.proto` (поле в существующем `lx:`-блоке `GroupItem`, сообщение в хвостовом `lx:`-блоке) + сгенерированный `started_service.pb.go`; `experimental/libbox/command_types.go` (две строки в существующих `lx:`-блоках).
- **lx-файл:** `daemon/started_service_command_lx.go` (заполнение в `GetOutbounds`).
- Сабмодули, `go.mod`, `option/` не тронуты.

## Проверки

- `go build ./...` без тегов — ок.
- `go build` / `go vet` с `LX_TAGS` (`-checklinkname=0`) — ок; vet выдаёт только старые `possible misuse of unsafe.Pointer` в `daemon/managed_service.go` и `experimental/libbox/debug.go`.
- `go test -race` с `LX_TAGS`: `transport/wireguard`, `protocol/wireguard`, `daemon`, `experimental/libbox`, `cmd/sing-box` — зелёные; `transport/wireguard` и `experimental/libbox` без тегов — зелёные.
- Живой стенд (бинарь с `LX_TAGS`, два процесса на loopback): сервер — конфиг владельца с новыми ключами (`listen_port: 51822`, пир без `address`, `jc 6`, `jmin/jmax 40/70`, `s1–s4 20/20/60/30`, `h1–h4`, `ib chrome`, `id www.google.com`, `ip quic`, `mtu 1280`) и `services: [{type: api}]`; клиент — то же с `address/port` сервера и mixed-inbound.
  - до клиента: `🏠 🇳🇱 Daniil  up  RIpg…A1Eo  -  never  0 B  0 B`;
  - после запроса через клиент: `… 127.0.0.1:56793  1s ago  244 B  238 B`;
  - после остановки клиента: адрес тот же, `5s ago` — последний известный адрес не стирается, как и описано в CONSUMERS §3.2;
  - `api peers <неизвестный тег>` — ошибка `endpoint not found`.

## Регенерация proto

`go run ./cmd/internal/protogen` переписал все `*.pb.go` и `sandbox_options.pb.go` в сабмодуле gvisor (шум версий плагинов). После `gofumpt` по изменённым pb.go отличия остались только в `started_service.pb.go`; правка в gvisor откачена.

## Зона конфликтов при синке

- `daemon/started_service.proto` / `.pb.go` — если апстрим добавит сообщения в конец proto или поле 7 в `GroupItem`; при конфликте регенерировать.
- `experimental/libbox/command_types.go` — `lx:`-блоки в `OutboundGroupItem` и `outboundGroupItemListFromGRPC`, те же, что у SPEC 097.

## Вне объёма

- Поле `name` у пира (правка апстримного `option/wireguard.go`).
- Tailscale-пиры, поток `SubscribeOutbounds`.
- Проверка на устройстве (LxBox, лаунчер) — после выпуска.
