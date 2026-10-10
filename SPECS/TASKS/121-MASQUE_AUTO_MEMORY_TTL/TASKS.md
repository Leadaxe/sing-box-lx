# TASKS: 121 — MASQUE_AUTO_MEMORY_TTL

**Фича:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC/PLAN/TASKS; строка 121 в Roadmap и FEATURE 009
- [x] 2. `outbound.go`: окно по лестнице 1 с → 10 с → 30 с → 5 мин → 10 мин, сброс на чтении, возврат на первую ступень победой h3
- [x] 3. `auto_memory_lx_test.go`; существующие тесты 074/108 зелёные
- [x] 4. SPEC 074 (правило памяти), FEATURE 009 (границы), lx-config §4, protocols-transports §3.7, changelog
- [x] 5. GL: 16 подъёмов в `auto` против живого WARP — после отказа h3 следующий подъём снова h3
- [x] 6. IMPLEMENTATION_REPORT.md; статус I
