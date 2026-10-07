# TASKS: 114 — WG_PEER_STATUS

- [x] 1. `adapter/peer_status_lx.go`: `PeerStatus`, `PeerStatusReporter`
- [x] 2. `transport/wireguard/peer_status_lx.go`: `PeerStatuses()` под `pauseOpAccess`, разбор UAPI, порядок по конфигу, без секретов
- [x] 3. `protocol/wireguard/peer_status_lx.go`: страж `building`
- [x] 4. proto: `GetWireGuardStatus`, `WireGuardStatusRequest`, `WireGuardEndpointStatus`, `PeerStatus`; `GroupItem.peers = 7` из rc.1 → `reserved 7`; регенерация `started_service.pb.go` (шум protogen в других pb.go и gvisor откачен)
- [x] 5. daemon: обработчик `GetWireGuardStatus` + заглушка без тега; `GetOutbounds` пиров не заполняет
- [x] 6. libbox: `WireGuardEndpointStatus.Peers()`, `CommandClient.GetWireGuardStatus(tag)`; `OutboundGroupItem.Peers()` удалён
- [x] 7. CLI `sing-box api peers [тег]` через новый RPC
- [x] 8. Тесты: разбор, живая пара на loopback, daemon (коды ошибок), libbox-клиент; `go test -race` по `transport/wireguard`, `protocol/wireguard`, `daemon`, `experimental/libbox`, `cmd/sing-box` с `LX_TAGS`; `go build ./...` без тегов
- [x] 9. Живой стенд на бинаре: сервер и клиент на loopback (rc.1 — серверный AWG-конфиг владельца; rc.2 — пара WG с ключами `generate wg-keypair`)
- [x] 10. SPEC, HISTORY, CONSUMERS, PLAN, отчёт; FEATURE 006; Roadmap; `lxd-grpc-api(.ru).md`; changelog
- [ ] 11. Проверка на устройстве после выпуска rc.2 (LxBox, лаунчер) → D
