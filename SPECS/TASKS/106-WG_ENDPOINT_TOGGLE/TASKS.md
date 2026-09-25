# TASKS: 106 — WG_ENDPOINT_TOGGLE

Порядок — по [PLAN.md](PLAN.md).

- [x] 1. SPEC/PLAN/TASKS
- [x] 2. Ядро: `EndpointToggle`, `SetEnabled`/`Enabled`, `disabled` в `resumeOnDial`/`SuspendIfIdle`/`IdleState`, `notReadyError()` в пяти местах
- [x] 3. RPC: `.proto`, регенерация `pb.go` (шум protogen и gofumpt откачен), обработчик и заглушка
- [x] 4. Юниты `protocol/wireguard` и `daemon` (обе сборки)
- [x] 5. Сборки и тесты по PLAN этапу 3; gofmt
- [x] 6. Доки и changelog
- [ ] 7. Живой прогон через лаунчер (`lxd`): выключение активного узла, отказ дайла, включение, `GetOutbounds`
- [ ] 8. Обёртка в `experimental/libbox` для LxBox — по заявке
