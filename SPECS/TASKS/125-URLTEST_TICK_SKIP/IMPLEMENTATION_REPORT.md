# IMPLEMENTATION REPORT: 125 — URLTEST_TICK_SKIP

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `protocol/group/urltest_tick_lx.go` | `testNodesTick` — порог свежести из `tickFreshWindow`, прогон через `urlTestOutbounds` с хуком штрафов SPEC 054, затем отметка `lastTickRunEnd = now`; `tickFreshWindow(interval, lastEnd, now)` — `interval` до первого прогона, `now − lastEnd` с капом `interval`, 0 при отметке в будущем |
| `protocol/group/urltest.go` | поле `lastTickRunEnd atomic.Int64`; в `testNodes` ветка `if !force { return g.testNodesTick(…) }` |
| `protocol/group/urltest_tick_lx_test.go` | `TestTick_leastTestProbesEveryTick`, `TestTick_failoverHeldProbedEveryTick`, `TestTick_manualRunStaysFresh`, `TestTick_windowCappedAtInterval`, `TestTick_freshWindow` |

| пауза | `tickTooSoon` в lx-файле; в `urlTest` пропуск нефорсированного прогона, если конец прошлого тикового прогона ближе `interval/2`; `ageHistory` тестов 116 двигает отметку вместе с history. Тест `TestTick_quietPauseAfterLongRun` |

Апстримный код не тронут: оба шва — в lx-функции `testNodes` и в lx-полях `URLTestGroup`.

## Проверка

- `gofmt`, `go vet ./protocol/group/`, `go test -ldflags=-checklinkname=0 ./protocol/group/` (go1.26.8) — ok.
- С выключенной веткой в `testNodes`: `TestTick_leastTestProbesEveryTick` и
  `TestTick_failoverHeldProbedEveryTick` падают — `node a: want 1 probes/dials, got 0`.

- Симуляция настоящего `loopCheck` (узел 100 мс, волны по 10; временный тест, в репо нет), проб на узел:

  | Сценарий | Апстрим | Без паузы | С паузой |
  |---|---|---|---|
  | 5 узлов, прогон ≪ `interval` 200 мс | 5 из 10 обещанных (2 с) | 9 из 10 (2 с) | 15 из 15 (3 с) |
  | 40 узлов, прогон ~400 мс > `interval` 200 мс, 3 с | 7 (встык) | 7 (встык) | 5 |
  | 40 узлов, прогон ~400 мс ≳ `interval` 350 мс, 3 с | 4 | 7 (встык) | 4 |

## Открыто

- Проверка на устройстве или через лаунчер.
