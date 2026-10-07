# TASKS: 116 — URLTEST_FAILOVER_MODE

- [ ] 1. `constant/proxy.go`: `URLTestModeFailover = "failover"`
- [ ] 2. `option/group.go`: удалить `PassiveCheck`
- [ ] 3. `protocol/group`: удалить passive-логику (urltest.go, urltest_penalty_lx.go, тесты)
- [ ] 4. `urltest_balance_lx.go`: разбор `failover`, предупреждение про `tolerance`
- [ ] 5. `urltest_failover_lx.go`: `failoverCheck`, `failoverSelect`, held-набор
- [ ] 6. Швы `lx: SPEC 116` в `urltest.go`: конструктор, Start, urlTest, performUpdateCheck
- [ ] 7. `urltest_failover_lx_test.go`: критерии §4 SPEC (1–9); пакет зелёный (10)
- [ ] 8. Документация: lx-config ×2, lx-energy ×2, docs/configuration/outbound/urltest.md, FEATURE 007/008, README ×2
- [ ] 9. SPEC 019 → HISTORY.md (passive_check), SPEC 054 правки упоминаний
- [ ] 10. Roadmap SPECS/README.md (116), changelog rc.2
- [ ] 11. gofmt lx-файлов, go vet пакета, сборка под полным набором тегов
- [ ] 12. Коммит(ы), уведомление лаунчера
