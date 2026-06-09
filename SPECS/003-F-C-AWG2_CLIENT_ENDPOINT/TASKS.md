# TASKS — 003-F-N-AWG2_CLIENT_ENDPOINT

## Зависимость
- [ ] Submodule `submodules/amneziawg-go` (pin коммит, совместимый с wireguard-go upstream)
- [ ] `// lx:` `replace` в `go.mod`; `go mod tidy`; сборка обычного WG без `with_awg` = upstream
- [ ] Сверить API amneziawg-go vs `transport/wireguard`; при расхождении — `patches/`

## Опции
- [ ] `option/wireguard_awg.go`: `Jc,Jmin,Jmax,S1,S2,H1..H4` (int), `I1..I5` (string, регистр)
- [ ] `// lx:` встроить AWG-поля в `WireGuardEndpointOptions`
- [ ] Без `with_awg` + заданы AWG-поля → явная ошибка «awg not built»

## Девайс
- [ ] `transport/wireguard/device_awg.go` (`//go:build with_awg`): строка конфига `jc=/jmin=/jmax=/s1=/s2=/h1..h4=/i1..i5=`
- [ ] `device_stub_awg.go` (`//go:build !with_awg`)
- [ ] `// lx:` прокидка опций в `protocol/wireguard/endpoint.go`
- [ ] Проводка под тегом (`include/awg.go` или правка `include/wireguard.go`)

## Проверки
- [ ] `lx-test/config/awg2_basic.json` + `sing-box check`
- [ ] Ручной коннект к серверу AmneziaWG 2.0 (непустой `Jc`, хотя бы `I1`)
- [ ] Сборка без тега: обычный WG ок; AWG-поля → ошибка
- [ ] `go vet ./...`, тесты затронутых пакетов

## Закрытие
- [ ] DoD-чеклист
- [ ] IMPLEMENTATION_REPORT.md (зафиксировать pin-коммит сабмодуля, формат конфиг-строки)
- [ ] Папка → `C`
