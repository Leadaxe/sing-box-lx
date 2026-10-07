# IMPLEMENTATION REPORT — 116 URLTEST_FAILOVER_MODE

**Фича:** [URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · [SPEC](SPEC.md) · [PLAN](PLAN.md) · [TASKS](TASKS.md)

## Что изменено

**Код** (коммиты `7fe1a845c`, `408d198db`):

- `passive_check` удалён целиком: поле `option.URLTestOutboundOptions.PassiveCheck`,
  `passiveOK`/`markPassiveAlive`/`passiveFresh`/`selectedPassivelyConfirmed`, шов в
  `DialContext`, пропуск цикла в `urlTest`, пропуск подтверждённых слотов в
  `balancePoolFirstLive`, штамп в `penaltyFailoverDial`, предупреждение про
  `pool_tolerance > 0`, тесты флага. `grep -rni passive protocol/ option/` находит только
  тест, проверяющий, что ключ отвергается.
- `C.URLTestModeFailover`; разбор в `urltest_balance_lx.go` (`newBalancer` → nil для
  failover, `isFailoverMode`, `warnFailoverTolerance`). `balancer` с failover отвергается
  прежней проверкой.
- `protocol/group/urltest_failover_lx.go`: `failoverHeld` (TCP-выбор и UDP-выбор, если
  отличается), `failoverCheck` (тик: проба удерживаемых; отказ или нет выбора → полный
  прогон по остальным и переизбор без снятия удержания с живой сети), `failoverSelect`
  (аварийный режим SPEC 054 → `penaltyBest`; без reselect и с живой history → текущий;
  иначе `fastestByDelay`; без history → апстримный `Select`), `selectForUpdate`.
- Швы `// lx: SPEC 116` в `urltest.go`: поле и флаг в конструкторе, проброс в `Start`,
  `Mode()`, ветка в `urlTest`, `performUpdateCheck` → `performSelectionUpdate(reselect)`
  (force-прогон перевыбирает в failover; в least_test/round_robin флаг игнорируется).
  `DialContext`/`ListenPacket`/`pickForDial`/`penaltyFailoverDial` не тронуты.

**Документация** (коммит docs/spec): lx-config ×2, lx-energy ×2 (матрица §6, абзацы после
неё, мобильный пример теперь `mode: failover`), `docs/configuration/outbound/urltest.md`,
FEATURE 007/008, README ×2, SPEC 019 (раздел `passive_check` → [HISTORY](../019-URLTEST_MODE_STICKY/HISTORY.md)),
SPEC 054, roadmap, пункт в `lx-changelog.md` (rc.2).

## Результаты

- `go test -count=1 ./protocol/group/ -ldflags "-checklinkname=0"` —
  `ok github.com/sagernet/sing-box/protocol/group 4.872s`; восемь тестов
  `TestFailover_*` покрывают критерии §4 п. 1–9, п. 10 — остальные тесты пакета.
- `go vet ./protocol/group/ ./option/ ./constant/` — чисто; `gofmt -l` — пусто.
- `make -f Makefile.lx lx-build` (полный набор LX_TAGS, go1.26.8) — собирается.

## Что не проверено

- Ни устройство, ни лаунчер, ни живой прогон с реальными узлами: только юниты.
- Энергоэффект (одна проба в `interval`, сон остальных узлов при idle-suspend) не мерился.
- Лаунчер о удалении `passive_check` не уведомлялся — за владельцем.

## Замечания по дизайну

- **Аварийный режим SPEC 054 сильнее удержания.** `failoverSelect` сначала спрашивает
  `penaltyEmergency`: если у самого быстрого по history ≥ 3 штрафов, выбор ранжируется
  по штрафам, затем по задержке. В failover бывший лучший после переезда по дайлу
  сохраняет history и штрафы и не пробуется, пока жив новый удерживаемый узел, поэтому
  аварийный режим может держаться долго. Тик-проба без отказа переизбор не вызывает, так
  что удержание это не ломает; но при полном прогоне из-за отказа одной сети (TCP или
  UDP) вторая, живая, сеть тоже пройдёт через `penaltyBest` и может уйти с живого узла
  на более быстрого нештрафника. Полный прогон пробует и бывшего лучшего: ответил —
  штрафы сняты, аварийный режим кончается. Оставлено как в least_test (SPEC §2.3:
  «аварийный режим и клапан действуют как в least_test»); если нужно строгое удержание
  и здесь, проверку аварийного режима можно ограничить случаем `reselect || current без
  history`.
- **Бывший лучший после переезда по дайлу** сохраняет history (SPEC 054 её не удаляет)
  и штрафы, но не пробуется, пока жив новый удерживаемый узел. Это заявленная семантика
  («нет возврата»); при следующем полном прогоне он либо ответит (штраф снят, участвует
  в выборе по скорости), либо потеряет history.
- Не-force проба удерживаемого узла идёт через апстримный `urlTestOutbounds`, который
  пропускает узел со свежей (< `interval`) history: сразу после полного прогона лишней
  пробы не будет.
