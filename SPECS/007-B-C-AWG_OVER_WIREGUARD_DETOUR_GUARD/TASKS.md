# TASKS — 007-B-AWG_OVER_WIREGUARD_DETOUR_GUARD

## Код
- [x] `common/dialer/dialer.go`: поле `Options.IsAmneziaWG`, проброс в `NewDetour`
- [x] `common/dialer/detour.go`: поле `ownerIsAmneziaWG`, новый параметр `NewDetour`
- [x] `common/dialer/detour.go`: guard в `init()` (после empty-direct) + рекурсивный `detourTargetIsWireGuard` (тип `wireguard`, раскрытие групп, защита от циклов)
- [x] `protocol/wireguard/endpoint.go`: `IsAmneziaWG: options.AmneziaWGOptions.IsSet()` в `dialer.Options`

## Тест
- [x] `awg_detour_guard_test.go`: прямая цель WG→guarded, VLESS→allowed; группа с WG→guarded, без WG→allowed; цикл групп не виснет; init-путь AWG→WG (reject), AWG→VLESS (allow), WG→WG (allow)

## Приёмка (DoD)
- [x] `go build ./...` без тегов — ок
- [x] `go build -tags "...,with_awg" ./cmd/sing-box` — ок
- [x] `go test ./common/dialer/...` — зелёный (8 подтестов)
- [x] `gofmt -l` изменённых lx-файлов — пусто
- [x] `go vet` затронутых пакетов — чисто

## Закрытие
- [x] IMPLEMENTATION_REPORT.md, DoD
- [ ] `SPECS/README.md` roadmap-строка 007
- [ ] GH issue: завести + комментарий + закрыть
- [ ] Переименовать папку `007-B-O-AWG_OVER_AWG_…` → `007-B-C-AWG_OVER_WIREGUARD_DETOUR_GUARD`
- [ ] (по запросу) `./sing-box check` на реальном AWG→WG конфиге — поведение варианта B
