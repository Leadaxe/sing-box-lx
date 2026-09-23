# TASKS: 078 — WIREGUARD_PACKET_SNIFFER

- [x] 1. `constant/protocol.go`: `ProtocolWireGuard` под маркером
- [x] 2. `common/sniff/wireguard_lx.go`: сниффер по типу/размеру
- [x] 3. `route/route.go`: `sniff.WireGuard` перед `sniff.UTP` в дефолтах
- [x] 4. `route/rule/rule_action.go`: имя `wireguard` в `sniffer`
- [x] 5. Тесты `common/sniff/wireguard_lx_test.go` (положительные, отрицательные, порядок vs uTP)
- [x] 6. Доки: строка в `docs/configuration/route/sniff.md` + `.zh.md`
- [x] 7. `go test ./common/sniff/ ./route/...`, `go vet`, `make -f Makefile.lx lx-build`, `sing-box check`
- [x] 8. Живой прогон через бинарь: initiation → `wireguard`, uTP SYN → `bittorrent`
- [x] 9. FEATURE 016-SNIFF, строки в `SPECS/README.md` и `SPECS/FEATURES/README.md`
- [x] 10. Секция changelog (`v1.14.0-lx.33`); полевой прогон на RouteRich закрыт решением владельца 2026-09-24
