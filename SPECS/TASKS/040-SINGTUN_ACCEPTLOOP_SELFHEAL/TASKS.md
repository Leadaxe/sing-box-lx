# TASKS — 040-SINGTUN_ACCEPTLOOP_SELFHEAL

- [x] Сабмодуль `submodules/sing-tun` на upstream `2d9b8aed5fe2` + `replace` в go.mod; сборка ядра без правок — идентична (санити)
- [x] Патч `stack_system.go`: closing-флаг, атомарные `tcpPort`/`tcpPort6`, самолечение `acceptLoop` (warn + relisten + счётчик)
- [x] Тест red/green: убийство listener'а мимо `System.Close` → восстановление; красный на апстрим-коде подтверждён прогоном (FAIL 5s timeout)
- [x] Регрессия: штатный `System.Close()` — без warn/восстановлений (`TestSystemAcceptLoopQuietOnClose`)
- [x] `go build ./...` ядра с replace; `gofmt -l` чист; `go test -race -count=5` зелёный; полный тест-набор форка зелёный
- [x] Скорректировать по результатам воркфлоу-аудита LxBox: все 16 статических сценариев опровергнуты → план поимки триггера = fdsan FATAL на устройстве (задача в репо LxBox, чип создан)
- [x] Доки: строка реестра HOTFIXES + changelog-секция v1.14.0-lx.17-rc.4 (тег не резался)
- [ ] Device-прогон (pending, отдельно: AAR + LxBox) — критерий SPEC §4
