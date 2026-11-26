# TASKS 164 — тестовый outbound `impair`

**Фича:** [IMPAIR](../../FEATURES/020-IMPAIR/FEATURE.md) · [SPEC](SPEC.md) · [PLAN](PLAN.md)

## Этап 1 — код

- [x] `constant/impair_lx.go`, `option/impair_lx.go`: тип и схема JSON
- [x] пакет `protocol/impair/`: модель, доставка датаграмм, потоки, общее узкое место, адаптер, `RegisterOutbound`
- [x] `include/lx_impair.go` / `include/lx_impair_stub.go` под `with_lx_impair`
- [x] вставка в `include/registry.go` за `// lx:begin impair`

## Этап 2 — тесты

- [x] юниты модели и бенчмарки без тега
- [x] интеграционные тесты во внешнем пакете под тегом, счётчики через `export_test.go`
- [x] `go vet` с тегом и без

## Этап 3 — DoD

- [x] `go build ./...` без тегов; `type: impair` отвергается ошибкой про тег
- [x] `go build -tags with_lx_impair ./...`; `sing-box check` на конфиге с `impair`
- [x] ошибка проверки на неверной единице
- [x] правка апстримного файла отдельным коммитом за маркером

## Этап 4 — документация

- [x] `docs-lx/lx-impair.md` и `lx-impair.ru.md`
- [x] FEATURE 020, строка в `SPECS/FEATURES/README.md`, строка в Roadmap `SPECS/README.md`
- [x] раздел в `README.md` / `README.ru.md`, ссылка в `docs-lx/lx-config{,.ru}.md`, запись в `lx-changelog.md`

## Не сделано

- [ ] полевой прогон сборки с тегом на роутере
