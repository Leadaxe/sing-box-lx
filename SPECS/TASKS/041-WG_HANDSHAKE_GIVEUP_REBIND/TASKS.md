# TASKS — 041-WG_HANDSHAKE_GIVEUP_REBIND

## v1 (give-up rebind, rc.4)

- [x] submodule: состояние `giveUpRebind` + `SetGiveUpRebind` + `handleHandshakeGiveUp` (device.go)
- [x] submodule: вызов хука в give-up ветке `expiredRetransmitHandshake` (timers.go)
- [x] submodule: тесты `lx_giveup_selfheal_test.go` (harness + red/green) и `lx_giveup_rebind_test.go` (fresh/pinned порт; дебаунс; disable = апстрим-паритет)
- [x] core: `SetGiveUpRebind(true, ListenPort==0)` в `transport/wireguard/endpoint.go`
- [x] red-прогон поведенческого теста на базе (submodule `d892107`, fix отстэшен): timeout 20 с — RED
- [x] тесты: `go test ./device/` (submodule) зелёный; `go test -tags with_gvisor,with_awg ./transport/wireguard/ ./protocol/wireguard/` зелёные
- [x] `gofmt -l` чист, `go vet` зелёный, полная сборка с lx-тегами зелёная
- [x] доки: Roadmap (SPECS/README) + реестр HOTFIXES (FEATURE.md, запись 041 с условием снятия)
- [x] UPHOLD-проход (свежий судья, 5/5, предано 0, check-uphold OK), статус C
- [x] field-подтверждение v1: живые `BindUpdate` в дампе `lxbox-dump-2026-08-01T18-49-34` (5/5 девайсов пересозданы, входящие идут) — но окно 0–90 с осталось жалобой → v2

## v2 (досрочный rebind + wake-нудж)

- [x] submodule: механизм вынесен в `device/lx_giveup_rebind.go` (`selfHealRebind` с триггер-меткой в логе, `sessionProvablyDead`, `maybeEarlyGiveUpRebind`, публичный `RebindIfSessionStale`)
- [x] submodule: lx-блок early-триггера в retry-ветке `expiredRetransmitHandshake` (timers.go)
- [x] red-прогон `TestEarlyRebindSelfHeal` на v1-базе (f007282, до правок): timeout 20 с — RED; GREEN после фикса
- [x] submodule: юниты `lx_stale_rebind_test.go` — нудж stale/healthy/expired/down/pinned, общий дебаунс (скользящее окно), порог ≥3 попыток, fresh-сессия = апстрим-паритет, гонки нуджа с Close/Down-Up под `-race`
- [x] core: проброс `RebindIfSessionStale` в `transport/wireguard/endpoint.go` (nil-safe)
- [x] core: `adapter.StaleRebindable` + `protocol/wireguard/endpoint_rebind_lx.go` (`RebindStale` с гейтами сна) + тесты гейтов на харнесе SPEC 020
- [x] libbox: `CommandServer.RebindStaleEndpoints()` (`command_server_rebind_lx.go`, зеркало `ResetNetwork`, горутина — не блокирует вызывающего)
- [x] тесты: полный `go test ./device/` (submodule) + `go test -tags with_gvisor,with_awg ./transport/wireguard/ ./protocol/wireguard/ -race` зелёные
- [x] `gofmt -l` чист по lx-файлам, `go vet` зелёный (warning в `debug.go` — апстримный, был до правок), полная сборка `-tags with_gvisor,with_awg,with_lx_command,with_lx_idle_suspend ./...` зелёная (go1.25.5 darwin)
- [x] доки: SPEC чекбоксы, PLAN v2, IMPLEMENTATION_REPORT v2; changelog-долг на релиз
- [x] UPHOLD-проход v2 (свежий судья, 6/6 с уликой, предано 0, 6 кандидатов опровергнуты, check-uphold OK), статус C
- [x] коммит ядра + submodule (768398e12 + 1255464; push сабмодуля ДО суперпроекта)
- [ ] field-подтверждение нуджа на стенде жалобы (CPH2411: сон → разблокировка → пинг в первые секунды зелёный) — остаток, вне закрытия synthetic-грейда
