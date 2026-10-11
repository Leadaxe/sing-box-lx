# TASKS: 122 — UTLS_RELEASE_HANDSHAKE_STATE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 0. Проверка идеи по нашему дереву и замер на loopback (SPEC §1–3), черновик SPEC/PLAN/TASKS — 2026-10-10
- [x] 1. Решение владельца 2026-10-11: делать; B; ALPN однократно в первом `HandshakeContext` lx-типа вместо стража 0086; фича 017 (SPEC §8)
- [x] 2. Форк (B): `u_conn_release_lx.go` + тест, `go test .`, push форка
- [x] 3. Ядро: `utls_release_lx.go`, `utls_release_lx_test.go`; `// lx:` в `utls_client.go` (`Client` → `lxUTLSConn`) и `reality_client.go` (release)
- [x] 4. `go test -tags with_utls ./common/tls/` зелёный, стражи 083/086/118 без правок (`lx-build` — открыто)
- [x] 5. Гитлинк, `go.mod` (восемь коммитов), раннбук §1.1, FEATURE 017, Roadmap `SPECS/README.md`, changelog
- [ ] 6. Повтор замера — сделан (−18…−25 КБ, отчёт); стенд Xray ≥ v26.9.8 (критерий 5)
- [x] 7. IMPLEMENTATION_REPORT.md; статус I
