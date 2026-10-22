# TASKS: 030 — FAST_BOX_SHUTDOWN

- [x] Шаг 1: `Router.QuiesceForShutdown` (route/reachability_common_lx.go) + вызов в box.Close
- [x] Шаг 2: `closing` флаг в protocol/wireguard/endpoint.go (Close set + resumeOnDial abort)
- [x] Шаг 4: параллельное закрытие в adapter/endpoint/manager.go (task.Group Concurrency 8)
- [x] Юниты механизма: protocol/wireguard/endpoint_fast_close_lx_test.go (red/green)
- [x] e2e-smoke: lx-test/startclose/wireguard_fast_close_lx_test.go (до 2026-10-23 лежал в test/, который не собирается и не гонялся)
- [x] Регресс: SPEC 020 юниты + SPEC 028/029 стенды
- [x] Обе сборки (idle-suspend on/off) + gofmt/vet
- [x] Changelog + SPECS/README.md строка 030
- [x] Field-тест на устройстве (~30 нод, пинг → stop → замер) — не проводился; в поле с `v1.14.0-lx.14` без жалоб, снят владельцем 2026-09-24
- [x] 2026-10-23: пре-пасс восстановлен явным `QuiesceForShutdown` (adapter/protocol/transport), юнит транспорта `endpoint_quiesce_lx_test.go` (сокет закрывается), юнит роутера `quiesce_shutdown_lx_test.go` (обход), юнит протокола (гейт `closing`); CONSTRAINT в FEATURES/005; шаг lx-ci для `transport/wireguard`, `protocol/wireguard`, `route`, `lx-test/startclose`
