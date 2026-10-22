# TASKS: 138 — URLTEST_RR_SKIP_DEAD_SLOT

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC/PLAN/TASKS; строка 138 в Roadmap и FEATURE 007
- [ ] 2. Решение владельца по дизайну (SPEC §2); issue с отчётом по шаблону bug report
- [x] 3. Тесты `urltest_rr_skip_lx_test.go` — падают до правки
- [x] 4. Поле `alive`, обход живых слотов в `pick`, `slotAlive`; строка в `URLTest.Start`; тесты пакета зелёные
- [x] 5. SPEC 019, FEATURE 007, `docs/configuration/outbound/urltest.md`, lx-config EN/RU, changelog
- [x] 6. IMPLEMENTATION_REPORT.md; статус I
- [x] 7. Проверка на роутере (A/B на бэкпорте v1.14.2-lx.11, 2026-10-22): простой сервера 1 — со стоковым ядром ~1/3 новых соединений падает весь простой, с патчем 0 отказов с ~20 с простоя до конца ([отчёт](IMPLEMENTATION_REPORT.md#на-устройстве))
