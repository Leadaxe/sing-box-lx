# TASKS: 125 — URLTEST_TICK_SKIP

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC/PLAN/TASKS; строка 125 в Roadmap и FEATURE 007
- [x] 2. `testNodesTick` + `tickFreshWindow`; шов в `testNodes`, поле `lastTickRunEnd`
- [x] 3. Тесты `urltest_tick_lx_test.go` (падают до правки); тесты пакета зелёные
- [x] 4. SPEC 116 §2.2 (ссылка), changelog
- [x] 5. IMPLEMENTATION_REPORT.md; статус I
- [x] 6. Пауза `interval/2` после тикового прогона (`tickTooSoon`), тест `TestTick_quietPauseAfterLongRun`; симуляция `loopCheck`
- [ ] 7. Проверка на устройстве или через лаунчер: в логе пробы на каждом тике
