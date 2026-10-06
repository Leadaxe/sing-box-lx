# TASKS: 114 — WG_PEER_STATUS

- [x] 1. `adapter/peer_status_lx.go`: `PeerStatus`, `PeerStatusReporter`
- [x] 2. `transport/wireguard/peer_status_lx.go`: `PeerStatuses()` под `pauseOpAccess`, разбор UAPI, порядок по конфигу, без секретов
- [x] 3. `protocol/wireguard/peer_status_lx.go`: страж `building`
- [x] 4. proto: `GroupItem.peers = 7`, `PeerStatus` в конце файла; регенерация `started_service.pb.go` (шум protogen в других pb.go и gvisor откачен)
- [x] 5. `GetOutbounds` заполняет `peers`; libbox `OutboundGroupItem.Peers()`
- [x] 6. CLI `sing-box api peers [тег]`
- [x] 7. Тесты: разбор, живая пара на loopback, daemon, libbox; `go test -race` по `transport/wireguard`, `protocol/wireguard`, `daemon`, `experimental/libbox`, `cmd/sing-box` с `LX_TAGS`; `go build ./...` без тегов
- [x] 8. Живой стенд на бинаре с серверным AWG-конфигом владельца
- [x] 9. SPEC, CONSUMERS, PLAN, отчёт; FEATURE 006; Roadmap; `lxd-grpc-api(.ru).md`
- [ ] 10. Проверка на устройстве после выпуска (LxBox, лаунчер) → D
