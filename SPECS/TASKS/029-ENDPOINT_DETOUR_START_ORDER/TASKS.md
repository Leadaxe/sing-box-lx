# TASKS: 029 — ENDPOINT_DETOUR_START_ORDER

- [x] Локализация: stack-trace repro → резолв в конструкторе через egress-каст
- [x] Фикс A: обернуть `common.Cast[dialer.UDPListener]` в `if options.Detour == ""`
- [x] Фикс B: `dialer.InitializeDetour(w.outboundDialer)` в `Start(StartStateStart)`
- [x] Red/green стенд: `test/wireguard_detour_order_lx_test.go` (потребитель раньше провайдера)
- [x] Регресс: SPEC 020 юниты + SPEC 028 стенд
- [x] gofmt/vet/build lx-теги
- [x] Changelog `#### v1.14.0-lx.12-rc.2`
- [x] SPECS/README.md: строка 029
- [ ] Field-тест на устройстве (сломанный порядок + AWG-over-AWG)
