# IMPLEMENTATION_REPORT 164 — тестовый outbound `impair`

**Фича:** [IMPAIR](../../FEATURES/020-IMPAIR/FEATURE.md) · [SPEC](SPEC.md)

## Что сделано

Outbound `impair` за тегом `with_lx_impair`: новый пакет `protocol/impair/`, схема в
`option/impair_lx.go`, тип в `constant/impair_lx.go`, пара `include/lx_impair{,_stub}.go` и одна
вставка в `include/registry.go`. Пользовательская документация — `docs-lx/lx-impair{,.ru}.md`.

## Цена мержа

Апстримный файл один: `include/registry.go`, 3 строки за `// lx:begin impair`. `go.mod` не тронут.

## DoD

| Шаг | Команда | Результат |
|---|---|---|
| сборка без тегов | `go build ./...` | ок |
| сборка с тегом | `go build -tags with_lx_impair ./...` | ок |
| vet | `go vet ./protocol/impair/ ./include/` с тегом и без | ок |
| тесты без тега | `go test ./protocol/impair/` | ok, 0,4 с (юниты модели) |
| тесты с тегом | `go test -tags with_lx_impair ./protocol/impair/` | ok, 30 с (юниты и интеграционные на loopback) |
| check с тегом | `sing-box check` на конфиге с двумя `impair` (под прокси и над `direct`, `stall`, `schedule` с `reset`, `profile`, `seed`) и на рецепте из документации | принят |
| check без тега | тот же конфиг | `impair outbound is not included in this build, rebuild with -tags with_lx_impair` |
| неверная единица | `"rate": "10mbps"` | `impair: rate: invalid rate "10mbps": want a number with bit, kbit, mbit or gbit ("10mbit"), or "unlimited"` |

Полный набор `make -f Makefile.lx lx-build` не прогонялся: тег в `LX_TAGS` не входит, а пакет
не зависит от остальных тегов.

## Бенчмарки (Apple M1 Pro, `go test -bench . -benchmem ./protocol/impair/`)

| Бенчмарк | Процессор | Доля ядра на 10 Мбит/с | Выделения |
|---|---|---|---|
| `PacketUp` (датаграмма 1400 байт) | 442 нс | 0,039 % | 0 allocs/op |
| `StreamUp` (запись 64 КБ) | 56 мкс (0,86 нс/байт) | 0,107 % | 0 allocs/op |
| `AdmitPacket` (только модель) | 32 нс | — | 0 allocs/op |

## Открыто

- Полевого прогона сборки с тегом на роутере нет.
