# PLAN: 126 — URLTEST_FAILOVER_DEGRADATION

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `protocol/group/urltest_failover_lx.go` | `failoverCheck`: после пробы удерживаемых — для живых `failoverDegraded` → `probeNow` → отказ / медленный; эскалация без отказавших и медленных; перед `performSelectionUpdate(false)` множество медленных кладётся в `failoverSlow`. `failoverSelect(!reselect)`: медленный текущий уступает самому быстрому, если тот больше 1,5× быстрее. `failoverDegraded(node)` — по сетям, за которые узел держится, против лучшего соседа той же сети. `probeNow` — `urlTestOutbounds` с порогом свежести 0. `slowerBy` — целочисленное сравнение отношений |
| `protocol/group/urltest.go` | поле `failoverSlow atomic.Pointer[tagSet]` |
| `protocol/group/urltest_failover_lx_test.go` | `spike` — разовая добавка задержки следующему дайлу |
| `protocol/group/urltest_degrade_lx_test.go` | тесты по SPEC §3 |
| `SPECS/…/116/SPEC.md`, `SPECS/FEATURES/007-…/FEATURE.md`, `SPECS/README.md`, `docs-lx/lx-changelog.md`, `docs-lx/lx-config{,.ru}.md` | правило удержания с исключением, строка 126 |

## Почему поле, а не правило в `failoverSelect` всегда

`performSelectionUpdate(false)` зовут и эскалация по отказу одной сети, и `performUpdateCheck`.
Правило 1,5× без пометки увело бы живой узел другой сети после чужого отказа — вопреки 116.
Пометка живёт только на время переизбора в тике, атомарно (переизбор SPEC 054 из дайлов может
идти параллельно и видит ту же пометку — решение то же).

## Проверка

- `gofmt`, `go vet ./protocol/group/`, `go test -ldflags=-checklinkname=0 ./protocol/group/`.
