# IMPLEMENTATION REPORT: 126 — URLTEST_FAILOVER_DEGRADATION

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `protocol/group/urltest_failover_lx.go` | `failoverCheck`: после пробы удерживаемых — для живых `failoverDegraded` → `probeNow` (порог свежести 0) → нет history = отказ, снова больше 2× = медленный; эскалация без отказавших и медленных; множество медленных (`tagSet`) кладётся в `failoverSlow` на время `performSelectionUpdate(false)`. `failoverSelect(!reselect)`: медленный текущий уступает `fastestByDelay`, если `slowerBy(current, fastest, 3, 2)`. `failoverDegraded` — по каждой сети, за которую узел держится, против лучшего соседа с history, поддерживающего эту сеть. Константы `failoverTrigger` 2/1, `failoverMove` 3/2; `slowerBy` — целочисленно |
| `protocol/group/urltest.go` | поле `failoverSlow atomic.Pointer[tagSet]` (одна строка, выравнивание соседей не тронуто) |
| `protocol/group/urltest_failover_lx_test.go` | `spike` у тестового узла — разовая добавка к задержке; `holdProbesOnlyCurrent` — задержки соседей так, чтобы удерживаемый был медленнее, но не 2× |
| `protocol/group/urltest_degrade_lx_test.go` | `moveWhenConfirmed`, `noTriggerBelowTwice`, `spikeFiltered`, `stayWhenNobodyMuchFaster`, `ratios` |

Первый вариант сравнивал удерживаемый UDP-узел с TCP-only соседом — тест 116
`udpHeldSeparately` поймал лишнюю эскалацию; сравнение сделано по сети.

## Проверка

- `gofmt`, `go vet ./protocol/group/`, `go test -ldflags=-checklinkname=0 -count=3 ./protocol/group/` (go1.26.8):
  тесты 126 зелёные все три прогона. Машина под нагрузкой (load average 30–40):
  тесты на задержках в десятки мс шумят, разрывы задержек в тестах 126 взяты с запасом
  (×4 и больше к порогу). Старый `TestFailover_moveOnProbeFailure` (116, выбор 20 против
  80 мс после отказа) под этой нагрузкой один раз упал — путь отказа 126 не меняет.

## Открыто

- Проверка на устройстве или через лаунчер на бесплатной подписке.
