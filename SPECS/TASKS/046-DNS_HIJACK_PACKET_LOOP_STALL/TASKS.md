# TASKS: 046 — DNS_HIJACK_PACKET_LOOP_STALL

- [x] 1. `route/router.go`: поле `dnsHijackSem` + инициализация (cap 256)
- [x] 2. `route/dns.go`: `HijackDNSPacket` → TryAcquire + go-wrap, `// lx: 046`
- [x] 3. Behaviour-тест `route/dns_hijack_test.go`: зависший exchange не блокирует следующий hijack; переполнение лимита дропает, не блокирует
- [x] 4. `go build ./...` + `go test ./...`
- [x] 5. AAR-сборка (CI run 30760597706), эмуляторное репро: 6 мин долбёжки мёртвого ru-пути → ICMP 120/120 ok, DNS живых путей 117/120 (3 фейла — пул netd, до ядра не дошли), ядро отвечает 30–50ms. На старом ядре тот же сценарий — полная смерть ICMP+DNS волнами
- [ ] 6. Device-верификация CPH2411 (конфиг инцидента)
- [x] 7. Строка в реестр `SPECS/FEATURES/004-HOTFIXES/FEATURE.md`
- [ ] 8. IMPLEMENTATION_REPORT.md, релиз rc
