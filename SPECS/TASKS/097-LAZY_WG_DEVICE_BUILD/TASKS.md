# TASKS: 097 — LAZY_WG_DEVICE_BUILD

- [x] 1. PLAN/TASKS; статус O в SPEC и Roadmap (после мержа 098)
- [x] 2. Транспорт: `EndpointOptions.LazyDevice` — конструктор без `NewDevice`, адреса из опций, nil-безопасный `returnDevice`; подменяемый `newDeviceFn` для тестов
- [x] 3. Endpoint: `lazy`/`neverBuilt`/`building`; `Start` под lazy = уровень 3 с рождения; `resumeOnDial(ctx)`; `IdleState()`; `TeardownForBudget()`
- [x] 4. `BuildBudget`: реестр, `Acquire` (жертва спящая → бодрая без потоков → `wait`/`build`), `Release`, сигналы ожидающим; регистрация в `box_lx.go`; порядок блокировок по PLAN
- [x] 5. Наблюдаемость: `adapter.IdleStateReporter`; proto `GroupItem.endpointState`/`idleSinceSeconds`, регенерация pb.go; `GetOutbounds`; libbox `OutboundGroupItem`
- [x] 6. Тесты по PLAN §Тесты (1–6); стенд `lx-test/lazywg` (7) не заведён — инвариант «не больше N живых устройств» покрыт юнитом `TestLazyBudget_probesKeepAtMostN` (см. IMPLEMENTATION_REPORT)
- [x] 7. `go build` без тегов / `LX_TAGS` / `with_lx_idle_suspend`; `go test` `transport/wireguard`, `protocol/wireguard`, `route`, `daemon`, `experimental/libbox` (с `-checklinkname=0`); gofmt
- [x] 8. Доки: `lx-energy(.ru).md` (ленивая сборка, бюджет, состояния), `lx-config(.ru).md` (ключи действуют), FEATURE 008, changelog; IMPLEMENTATION_REPORT; статус I
- [ ] 9. Замер на эмуляторе (11 endpoint'ов, `build_max: 3`): heap после старта и после «проверки на всех», время до `tunnel=connected`; прогон раннбука §1.4 → D
