# IMPLEMENTATION REPORT: 121 — MASQUE_AUTO_MEMORY_TTL

**Фича:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `protocol/masque/outbound.go` | `autoMemory{network, window, expires}`; лестница `autoH2MemoryLadder` = 1 с, 10 с, 30 с, 5 мин, 10 мин; `effectiveNetwork()` сбрасывает просроченную память на чтении (Info `remembered h2 … expired after …; trying h3 first again`); `rememberNetwork("h2")` берёт текущую ступень и взводит следующую (верхняя повторяется), `rememberNetwork("h3")` возвращает на первую; повторная запись того же режима окно не трогает; Info при победе h2 называет окно |
| `protocol/masque/auto_memory_lx_test.go` | Первая победа h2 — первая ступень и h3 на следующем подъёме; подъём по ступеням до верхней; подъём по памяти окно не продлевает; победа h3 возвращает на первую; без проводки — без окна; лестница вменяема |

Апстримных файлов ноль. Решение владельца 2026-10-09: лестница вместо удвоения с 0.1 с — при молчащем h3 и `idle_timeout: 5m` это 4 трёхсекундные пробы за ~20 мин вместо 12 за час.

## Проверка

- `gofmt`, `go vet`, `go test -ldflags=-checklinkname=0 -tags with_quic,with_gvisor,with_utls ./protocol/masque/...` (go1.26.8) — ok; тесты 074/108 без изменений.
- GL (Амстердам), `vhttp: auto`, `idle_timeout: 3s`, 16 подъёмов подряд против живого WARP, сборка с окном первой ступени: после каждого `PROTOCOL_VIOLATION` — `h2 remembered for …; then h3 is tried first again` → `h2 tunnel established` → на следующем подъёме `remembered h2 expired …; trying h3 first again` → `h3 tunnel established`. Два отказа подряд дали вторую ступень, и она тоже истекла к следующему подъёму. Ни одного «залипания» на h2 при живом h3.

## Открыто

- Причина `PROTOCOL_VIOLATION` (программа опытов — в отчёте [120](../120-PATH_MTU_ALIGN/IMPLEMENTATION_REPORT.md)); повтор h3 в той же попытке — отдельное решение после неё.
