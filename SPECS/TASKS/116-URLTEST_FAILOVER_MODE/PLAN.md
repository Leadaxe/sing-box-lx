# PLAN: 116 — URLTEST_FAILOVER_MODE

## Архитектура

Режим реализуется как третье состояние рядом с `balancer == nil`
(least_test) и `balancer != nil` (round_robin): флаг `failover bool` на
`URLTest` и `URLTestGroup`, вся логика в новом lx-файле
`protocol/group/urltest_failover_lx.go`, в `urltest.go` — только помеченные
швы `lx: SPEC 116`.

### Разбор конфига

`newBalancer` возвращает `nil, nil` и для `failover` (как для least_test);
рядом вычисляется `failover := options.Mode == C.URLTestModeFailover`.
Существующая проверка «`balancer` без round_robin — ошибка» покрывает
failover автоматически. Предупреждение про `tolerance != 0` — по образцу
`warnLegacyTolerance`.

### Цикл проб (`urlTest`)

```
force (ручной тест / клапан 054)      → testNodes(all) → performUpdateCheck(reselect=true)
failover && !force:
  held = {selectedTCP, selectedUDP}\{nil}, без дублей
  held пуст (холодный старт)           → testNodes(all) → performUpdateCheck(reselect=true)
  testNodes(held, force=false)
  все held ответили                    → ничего (history обновлена)
  кто-то не ответил                    → testNodes(all) → performUpdateCheck(reselect=true)
```

### Выбор (`performUpdateCheck`)

Шов: при `failover` вместо `selectPenaltyAware` вызывается
`failoverSelect(network, reselect)`:

- аварийный режим SPEC 054 → `penaltyBest(network, "")` (как least_test);
- `!reselect` и текущий узел имеет history → текущий (удержание);
- иначе → `fastestByDelay(network)` (чистая скорость, без tolerance);
- нет history ни у кого → апстримный холодный старт (`Select` вернёт первый узел).

`reselect` передаётся только из путей с полным прогоном; обычный тик с
живым текущим узлом `performUpdateCheck` не вызывает вовсе.

### Дайл

`DialContext`/`ListenPacket` не меняются: `pickForDial` берёт кеш
`selectedOutbound*`, `penaltyFailoverDial` переносит выбор через
`moveSelection`. Оба уже дают нужную failover-семантику.

### Удаление `passive_check`

Механическое, по списку §3 SPEC. После удаления `urlTest` для least_test
становится апстримным + шов 054 (`maybeForceRetest`) + шов 116.

## Зона касания upstream

`urltest.go`: конструктор (флаг), `Start` (проброс флага), `urlTest`
(ветка), `performUpdateCheck` (вызов выбора). Все швы помечены. Удаление
passive-швов уменьшает дифф с апстримом.

## Документация

Три режима одной таблицей в `lx-config{,.ru}.md` §3, `lx-energy{,.ru}.md`
§6 (матрица «кто кого будит»: строка `passive_check` → строка `failover`),
FEATURE 007 (режимы, правила, границы, таблица задач), FEATURE 008 (смежный
ключ → `mode: failover`), README оба языка, `docs/configuration/outbound/urltest.md`.
SPEC 019: раздел `passive_check` → HISTORY.md. SPEC 054: упоминания passive
→ failover. Roadmap в `SPECS/README.md`: строка 116. Changelog: пункт в
секции `v1.14.2-lx.12-rc.2` (тег ещё не срезан).
