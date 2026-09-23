# TASKS — 003-AWG2_CLIENT_ENDPOINT

## Зависимость
- [x] Submodule `submodules/amneziawg-go` (pin коммит, совместимый с wireguard-go upstream) — выполнено иначе: не сабмодуль amneziawg-go, а форк-сабмодуль `submodules/wireguard-go` (Leadaxe/wireguard-go = 3-way merge обфускации amnezia поверх sagernet/wireguard-go)
- [x] `// lx:` `replace` в `go.mod`; `go mod tidy`; сборка обычного WG без `with_awg` = upstream — `replace github.com/sagernet/wireguard-go => ./submodules/wireguard-go`
- [x] Сверить API amneziawg-go vs `transport/wireguard`; при расхождении — `patches/` — выполнено иначе: расхождение API (sagernet-добавки `Send(offset)`, `InputPacket`, `conn`) снято merged-форком, `patches/` не понадобился

## Опции
- [x] `option/wireguard_awg.go`: `Jc,Jmin,Jmax,S1,S2,H1..H4` (int), `I1..I5` (string, регистр)
- [x] `// lx:` встроить AWG-поля в `WireGuardEndpointOptions`
- [x] Без `with_awg` + заданы AWG-поля → явная ошибка «awg not built»

## Девайс
- [x] `transport/wireguard/device_awg.go` (`//go:build with_awg`): строка конфига `jc=/jmin=/jmax=/s1=/s2=/h1..h4=/i1..i5=`
- [x] `device_stub_awg.go` (`//go:build !with_awg`)
- [x] `// lx:` прокидка опций в `protocol/wireguard/endpoint.go`
- [x] Проводка под тегом (`include/awg.go` или правка `include/wireguard.go`) — выполнено иначе: проводка через build-tag файлы `transport/wireguard/device_awg.go`/`device_stub_awg.go`, `include/` не трогали

## Проверки
- [x] `lx-test/config/awg2_basic.json` + `sing-box check`
- [x] Ручной коннект к серверу AmneziaWG 2.0 (непустой `Jc`, хотя бы `I1`)
- [x] Сборка без тега: обычный WG ок; AWG-поля → ошибка
- [x] `go vet ./...`, тесты затронутых пакетов — тесты в `transport/wireguard/device_awg_test.go`; отдельной записи о `go vet` в отчёте нет

## Закрытие
- [x] DoD-чеклист
- [x] IMPLEMENTATION_REPORT.md (зафиксировать pin-коммит сабмодуля, формат конфиг-строки)
- [x] Папка → `C` — выполнено иначе: соглашение о статусе в имени папки отменено
