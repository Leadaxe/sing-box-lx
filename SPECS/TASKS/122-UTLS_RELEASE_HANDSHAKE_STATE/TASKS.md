# TASKS: 122 — UTLS_RELEASE_HANDSHAKE_STATE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 0. Проверка идеи по нашему дереву и замер на loopback (SPEC §1–3), черновик SPEC/PLAN/TASKS — 2026-10-10
- [ ] 1. Решение владельца: делать ли; вариант A или B; состав обнуления; фича-владелец (SPEC §8)
- [ ] 2. Форк (B): `u_conn_release_lx.go` + тест, `go test .`, push форка
- [ ] 3. Ядро: `utls_release_lx.go`, `utls_release_lx_test.go`; `// lx:` в `utls_client.go` (страж 0086 + release) и `reality_client.go` (release)
- [ ] 4. `go test -tags with_utls ./common/tls/` зелёный, стражи 083/086/118 без правок; `make -f Makefile.lx lx-build`
- [ ] 5. Гитлинк, `go.mod` (восемь коммитов), раннбук §1.1, FEATURE 017, Roadmap `SPECS/README.md`, changelog
- [ ] 6. Повтор замера §2 на реализации; стенд Xray ≥ v26.9.8 (критерий 5)
- [ ] 7. IMPLEMENTATION_REPORT.md; статус I
