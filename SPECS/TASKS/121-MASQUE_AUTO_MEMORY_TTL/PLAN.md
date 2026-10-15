# PLAN: 121 — MASQUE_AUTO_MEMORY_TTL

**Фича:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `protocol/masque/outbound.go` | `autoMemory{network, window, expires}` вместо `*string`; лестница `autoH2MemoryLadder` (1 с, 10 с, 30 с, 5 мин, 10 мин), поле `autoH2Ladder`, ступень `autoH2Rung` (atomic); `effectiveNetwork()` сбрасывает просроченную память на чтении с Info; `rememberNetwork("h2")` берёт текущую ступень и взводит следующую, `rememberNetwork("h3")` возвращает на первую; Info при победе h2 называет окно |
| `protocol/masque/auto_memory_lx_test.go` | Критерии §3 SPEC: первая победа h2 — короткое окно и h3 на следующем подъёме; ступени до верхней; подъём по памяти окно не продлевает; победа h3 возвращает на первую; без проводки — без окна; лестница |
| `SPECS/TASKS/074-MASQUE_VHTTP_AUTO/SPEC.md` | Правило «Память по процессу» → окно по лестнице (ссылка на 121) |
| `SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md` | Пункт «Границы» про `auto`; строка 121 |
| `docs-lx/lx-config.*` §4, `docs-lx/protocols-transports.*` §3.7 | Фраза «победивший режим запоминается» → окно по лестнице |
| `docs-lx/lx-changelog.md`, `SPECS/README.md` | Пункт в секции `v1.14.3-lx.14`; строка 121 |

## Что не меняется

- `connect()`: порядок ног, wall-clock-таймер, drain поздней h3, отчёт обеих ошибок (074), сброс памяти при отказе h2 (108).
- `vhttp: h3` / `h2` явные — без памяти, как раньше.
- Апстримных файлов ноль.

## Проверка

- `gofmt`, `go vet`, `go test -ldflags=-checklinkname=0 -tags with_quic,with_gvisor,with_utls ./protocol/masque/...`.
- GL: `vhttp: auto`, `idle_timeout: 3s`, 16 подъёмов подряд против живого WARP — после `PROTOCOL_VIOLATION` следующий подъём снова идёт по h3.
