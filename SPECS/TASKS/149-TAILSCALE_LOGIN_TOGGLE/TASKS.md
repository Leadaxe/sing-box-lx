# TASKS: 149 — TAILSCALE_LOGIN_TOGGLE

Порядок — по [PLAN.md](PLAN.md).

- [x] 1. SPEC/PLAN/TASKS
- [x] 2. Ядро: `TailscaleLoginProvider`, `SetEnabled`/`Enabled`/`IdleState`/`TailscaleLogin`, проверки в `DialContext`/`ListenPacketWithDestination`/`Logout`
- [x] 3. RPC: `.proto`, регенерация `pb.go`, обработчик и заглушка, обёртка libbox
- [x] 4. Юниты `protocol/tailscale`, `daemon` (обе сборки), `experimental/libbox`
- [x] 5. Сборки и тесты по PLAN этапу 3; javap; gofmt
- [x] 6. Доки
- [x] 7. Живой прогон: вход по ключу из `NeedsLogin`, выключение и включение на tailnet (LxBox, AVD, SaaS, 2026-11-02)
- [x] 8. Taildrop (SPEC §4): `disable_taildrop`, `senderID` в `TaildropFile`, юниты, доки
- [ ] 9. Живой прогон Taildrop: отказ приёма и отправки, `senderID` у полученного файла (LxBox)
