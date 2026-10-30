# TASKS: 143 — DNS_GROUP_HOSTS_OVERLAY

**Фича:** [013-DNS_GROUP](../../FEATURES/013-DNS_GROUP/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC/PLAN/TASKS; решение владельца 2026-10-30: ключ `hosts`, `servers` обязателен
- [x] 2. Тесты `hosts_test.go`, правки `group_test.go` / `conntest_lx_test.go` — падают до правки
- [x] 3. `option/dns.go` (поле `Hosts`), `group.go` (валидация, зависимости, вызов слоя), `hosts.go` (слой), `conntest_lx.go`; тесты пакета зелёные с `-race`
- [x] 4. FEATURE 013, SPEC 033 §2, lx-config EN/RU §5 (+ якоря в README/README.ru), Roadmap, FEATURES/README, changelog
- [x] 5. DoD: gofmt, vet, `go build ./...`, `sing-box check` с конфигом §3 SPEC
- [x] 6. IMPLEMENTATION_REPORT.md; статус I
- [ ] 7. Полевая проверка: `wg.home` с IPv4 + IPv6 через группу на устройстве/роутере — статус D
