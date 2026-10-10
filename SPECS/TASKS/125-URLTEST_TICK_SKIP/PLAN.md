# PLAN: 125 — URLTEST_TICK_SKIP

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `protocol/group/urltest_tick_lx.go` | `testNodesTick`: порог `tickFreshWindow(interval, lastEnd, now)` → `urlTestOutbounds(…, window, false, …)` → отметка `lastTickRunEnd`; `tickFreshWindow` — время с конца прошлого тикового прогона, кап `interval`, `interval` до первого |
| `protocol/group/urltest.go` | поле `lastTickRunEnd atomic.Int64`; `testNodes` при `!force` уходит в `testNodesTick` — два шва `// lx: SPEC 125` внутри lx-кода (`testNodes` — lx-функция) |
| `protocol/group/urltest_tick_lx.go` (пауза) | `tickTooSoon`: конец прошлого тикового прогона меньше `interval/2` назад |
| `protocol/group/urltest.go` (пауза) | в `urlTest` после `checking`: `if !force && g.tickTooSoon() { return }` |
| `protocol/group/urltest_tick_lx_test.go` | тесты по SPEC §3 на фикстуре 116; `ageHistory` в тестах 116 двигает и отметку конца |
| `SPECS/README.md`, `SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md`, `SPECS/TASKS/116-…/SPEC.md`, `docs-lx/lx-changelog.md` | строка 125, пометка о частоте проб, запись Unreleased |

## Почему не `interval − 15 с`

Порог «`interval` минус таймаут пробы» верен для одной пробы, но в большой группе пробы
идут волнами по 10 (`batch.WithConcurrencyNum(10)`), и узлы поздних волн получают отметку
позже 15 с от начала тика — для них пропуск остался бы. Отметка конца прогона от размера
группы не зависит.

## Что не меняется

- `urlTestBatch.test`, `URLTestOutbounds` (публичная, апстримная семантика), форсированные пути.

## Проверка

- `gofmt`, `go vet ./protocol/group/`, `go test -ldflags=-checklinkname=0 ./protocol/group/` (go из `go.version`).
- С выключенной веткой в `testNodes` тесты least_test и failover падают.
