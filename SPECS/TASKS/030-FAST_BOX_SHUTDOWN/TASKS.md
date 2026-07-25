# TASKS: 030 — FAST_BOX_SHUTDOWN

- [x] Шаг 1: `Router.QuiesceForShutdown` (route/reachability_common_lx.go) + вызов в box.Close
- [x] Шаг 2: `closing` флаг в protocol/wireguard/endpoint.go (Close set + resumeOnDial abort)
- [x] Шаг 4: параллельное закрытие в adapter/endpoint/manager.go (task.Group Concurrency 8)
- [x] Юниты механизма: protocol/wireguard/endpoint_fast_close_lx_test.go (red/green)
- [x] e2e-smoke: test/wireguard_fast_close_lx_test.go
- [x] Регресс: SPEC 020 юниты + SPEC 028/029 стенды
- [x] Обе сборки (idle-suspend on/off) + gofmt/vet
- [x] Changelog + SPECS/README.md строка 030
- [ ] Field-тест на устройстве (~30 нод, пинг → stop → замер)
