# TASKS: 079 — VPN_VOIP_PACKET_SNIFFERS

- [x] 1. Спека/план/фича/roadmap
- [x] 2. `constant/protocol.go`: четыре константы (маркер `sniff-lx`)
- [x] 3. `common/sniff/openvpn_lx.go` + тесты (plain, tls-auth, tls-crypt, TCP, stream)
- [x] 4. `common/sniff/ike_lx.go` + тесты (v2 500/4500, v1 main/aggressive)
- [x] 5. `common/sniff/tailscale_lx.go` + тесты
- [x] 6. `common/sniff/sip_lx.go` + тесты (INVITE/REGISTER, domain, stream)
- [x] 7. Перекрёстный тест дефолтных списков на всех векторах (свои + апстримные)
- [x] 8. `route/route.go` (packet + stream дефолты), `route/rule/rule_action.go`
- [x] 9. `go test` (+`-race`), `go vet`, `lx-build`, `sing-box check`
- [x] 10. Прогон через `direct`-inbound бинаря (все четыре, синтетические пакеты; реального сервера нет)
- [x] 11. Доки: sniff.md/.zh.md; `docs-lx/lx-sniff.md` + `.ru.md`; ссылка из lx-config
- [ ] 12. Changelog при срезе тега; статус I в SPEC/фиче/roadmap
