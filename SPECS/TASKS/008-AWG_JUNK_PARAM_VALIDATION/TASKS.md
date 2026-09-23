# TASKS — 008-B-AWG_JUNK_PARAM_VALIDATION

## Код
- [x] `device_awg.go`: `validateJunk(o)` — единственное правило `jmin <= jmax`
- [x] Вызвать `validateJunk` в начале `awgIpcLines` (после IsSet)

## Тест
- [x] `device_awg_test.go`: jmin>jmax → ошибка + `require.NotPanics`; валидная триада `jc4/jmin40/jmax70` ок; junk-off (только header) ок
- [x] Существующий `TestAwgIpcLinesUnsetHeadersOmitted` (`jc=4` без размеров) не сломан

## Приёмка (DoD)
- [x] `go build ./...` без тегов — ок
- [x] `go build -tags "...,with_awg" ./cmd/sing-box` — ок
- [x] `go test -tags with_awg ./transport/wireguard/...` — зелёный (7 ipc-тестов)
- [x] `gofmt -l` изменённых lx-файлов — пусто

## Закрытие
- [x] IMPLEMENTATION_REPORT.md, DoD
- [x] `SPECS/README.md` roadmap-строка 008
- [x] Коммит (со ссылкой), затем GH issue + комментарий с ссылкой на коммит + закрыть — коммит `2adaac46`, issue #3 закрыт комментарием со ссылкой на него
- [x] Папка `008-B-O-…` → `008-B-C-…` — выполнено иначе: соглашение о статусе в имени папки отменено
