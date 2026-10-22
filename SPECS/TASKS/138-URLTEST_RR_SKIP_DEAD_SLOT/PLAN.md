# PLAN: 138 — URLTEST_RR_SKIP_DEAD_SLOT

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `protocol/group/urltest_balance_lx.go` | поле `alive func(tag string) bool` у `balancer` (рядом с `onChange`; `nil` — проверки нет, поведение до правки). `pick`: снимок `b.slots` под `access`, номер слота как сейчас; с закреплением — `slot[номер]` живой, иначе `liveSlot(hash / pool)`; без закрепления — `liveSlot(счётчик)`; `liveSlot(k)` — `k % число живых`-й живой слот, живых нет — `slot[номер]`. Дальше `resolve`/fallback без изменений. `(*URLTest).slotAlive(tag)`: `s.outbound.Outbound(tag)` → `s.group.history.LoadURLTestHistory(RealTag(s.outbound, node)) != nil`; нерезолвимый тег — мёртв (у `Pool()` фолбэк на сырой тег, здесь он не нужен: такой тег `resolve` всё равно отдаёт в fallback) |
| `protocol/group/urltest.go` | `URLTest.Start`, внутри `if s.balancer != nil {` рядом с `onChange`: `s.balancer.alive = s.slotAlive // lx: SPEC 138` |
| `protocol/group/urltest_rr_skip_lx_test.go` | тесты по SPEC §3: `pick` на `rrBalancer` с `alive` из множества тегов; живой путь — `newPoolTestGroup` + `URLTest{outbound, group, balancer}` с `alive = s.slotAlive` и реальной `HistoryStorage` |
| `SPECS/TASKS/019-URLTEST_MODE_STICKY/SPEC.md` | «Выбор узла» — подраздел про мёртвый слот; «Замена → Следствие» и «Ошибка дайла» — ссылка; журнал п. 20; тест-план |
| `SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md` | Data flow (ветка «узел слота мёртв → живой слот пула», строка дотеста), «Правила и гарантии», статус строки 138 |
| `docs/configuration/outbound/urltest.md`, `docs-lx/lx-config.md`, `docs-lx/lx-config.ru.md` | фраза «A dead pool node keeps its slot…»: слот остаётся, но новые соединения его обходят, пока в пуле есть живые |
| `SPECS/README.md`, `docs-lx/lx-changelog.md` | статус в Roadmap, запись в `#### Unreleased` |

## Почему поле, а не параметр `pick`

Тот же приём, что у `onChange`: функцию проводит `URLTest.Start`, балансировщик не знает про
history и менеджер узлов. Сигнатура `pick` и её вызовы в тестах SPEC 019 не
меняются: у `rrBalancer` поле пустое, и старые тесты проверяют прежнее поведение как есть.
Правка `urltest.go` — одна строка внутри lx-блока, который уже переносится на мерже.

## Почему снимок слотов, а не вызов под замком

`setSlots` публикует новый срез и опубликованный не меняет, поэтому срез, взятый под
`access`, после отпускания замка неизменен. `alive` читает менеджер узлов и history под их
замками — вызывать её под `access` значит вкладывать чужие замки в замок пула; файл уже держит
это правило для `onChange` («never nests under it»). Аллокаций на соединение нет.

## Обход живых слотов (`liveSlot`)

Два прохода по слотам: число живых `m`, затем `k % m`-й живой. Между проходами проба может
стереть или записать history; не нашли — выбор остаётся `slot[номер]`, как сейчас. Без
закрепления `k = счётчик`: при `m == pool` результат равен `счётчик % pool`, порядок
ротации у здорового пула не меняется. С закреплением `liveSlot` зовётся только для мёртвого
слота, с `k = hash / pool`: остаток хеша, которым номер слота не определяется, разносит ключи
мёртвых слотов по живым поровну; «первый живой по кругу» сваливал бы ключи нескольких мёртвых
подряд на один живой. Редукции — в `uint64` до сужения (32-битная защита SPEC 019 п. 19a).

## Что не меняется

- `planFirstLivePool`, `planTolerantPool`, `balancePool*`, `rebuildPool`, `seedPool` —
  планирование пула и его инварианты.
- `DialContext`/`ListenPacket`: ошибка дайла в `round_robin` не трогает пул и history.
- `Pool()`/`GetPool`, `ActiveTags`, `poolReferences`, `Now()`.
- `least_test` и `failover`: `balancer == nil`, `pick` не вызывается.

## Проверка

- `gofmt -l protocol/group/`, `go vet ./protocol/group/`,
  `go test -count=1 -ldflags=-checklinkname=0 ./protocol/group/` (go из `go.version`);
  `-race` — там, где есть cgo.
- С выключенным обходом (вернуть `slot[номер]`) новые тесты падают, старые — зелёные.
- CI `lx-ci.yml` тесты `protocol/group` не гоняет — результат прогона идёт в
  IMPLEMENTATION_REPORT.md.
