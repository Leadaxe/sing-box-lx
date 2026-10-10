# IMPLEMENTATION REPORT: 125 — URLTEST_TICK_SKIP

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `protocol/group/urltest_tick_lx.go` | `testNodesTick` — порог свежести из `tickFreshWindow`, прогон через `urlTestOutbounds` с хуком штрафов SPEC 054, затем отметка `lastTickRunEnd = now`; `tickFreshWindow(interval, lastEnd, now)` — `interval` до первого прогона, `now − lastEnd` с капом `interval`, 0 при отметке в будущем |
| `protocol/group/urltest.go` | поле `lastTickRunEnd atomic.Int64`; в `testNodes` ветка `if !force { return g.testNodesTick(…) }` |
| `protocol/group/urltest_tick_lx_test.go` | `TestTick_leastTestProbesEveryTick`, `TestTick_failoverHeldProbedEveryTick`, `TestTick_manualRunStaysFresh`, `TestTick_windowCappedAtInterval`, `TestTick_freshWindow` |

Апстримный код не тронут: оба шва — в lx-функции `testNodes` и в lx-полях `URLTestGroup`.

## Проверка

- `gofmt`, `go vet ./protocol/group/`, `go test -ldflags=-checklinkname=0 ./protocol/group/` (go1.26.8) — ok.
- С выключенной веткой в `testNodes`: `TestTick_leastTestProbesEveryTick` и
  `TestTick_failoverHeldProbedEveryTick` падают — `node a: want 1 probes/dials, got 0`.

## Открыто

- Проверка на устройстве или через лаунчер.
