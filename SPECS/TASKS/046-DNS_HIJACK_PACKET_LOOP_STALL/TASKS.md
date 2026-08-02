# TASKS: 046 — DNS_HIJACK_PACKET_LOOP_STALL

- [ ] 1. `route/router.go`: поле `dnsHijackSem` + инициализация (cap 256)
- [ ] 2. `route/dns.go`: `HijackDNSPacket` → TryAcquire + go-wrap, `// lx: 046`
- [ ] 3. Behaviour-тест `route/dns_hijack_test.go`: зависший exchange не блокирует следующий hijack; переполнение лимита дропает, не блокирует
- [ ] 4. `go build ./...` + `go test ./...`
- [ ] 5. AAR-сборка, эмуляторное репро (конфиг TEST-NET detour + поток ru-доменов ≥5 мин): форвардинг стабилен
- [ ] 6. Device-верификация CPH2411 (конфиг инцидента)
- [ ] 7. Строка в реестр `SPECS/FEATURES/004-HOTFIXES/FEATURE.md`
- [ ] 8. IMPLEMENTATION_REPORT.md, релиз rc
