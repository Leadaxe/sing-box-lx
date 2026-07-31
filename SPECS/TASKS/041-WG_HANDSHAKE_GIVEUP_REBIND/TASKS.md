# TASKS — 041-WG_HANDSHAKE_GIVEUP_REBIND

- [x] submodule: состояние `giveUpRebind` + `SetGiveUpRebind` + `handleHandshakeGiveUp` (device.go)
- [x] submodule: вызов хука в give-up ветке `expiredRetransmitHandshake` (timers.go)
- [x] submodule: тесты `lx_giveup_selfheal_test.go` (harness + red/green) и `lx_giveup_rebind_test.go` (fresh/pinned порт; дебаунс; disable = апстрим-паритет)
- [x] core: `SetGiveUpRebind(true, ListenPort==0)` в `transport/wireguard/endpoint.go`
- [x] red-прогон поведенческого теста на базе (submodule `d892107`, fix отстэшен): timeout 20 с — RED
- [x] тесты: `go test ./device/` (submodule) зелёный; `go test -tags with_gvisor,with_awg ./transport/wireguard/ ./protocol/wireguard/` зелёные
- [x] `gofmt -l` чист, `go vet` зелёный, полная сборка с lx-тегами зелёная
- [x] доки: Roadmap (SPECS/README) + реестр HOTFIXES (FEATURE.md, запись 041 с условием снятия)
- [x] UPHOLD-проход (свежий судья, 5/5, предано 0, check-uphold OK), статус C
- [ ] field-подтверждение на стенде жалобы (остаток, вне закрытия synthetic-грейда)
