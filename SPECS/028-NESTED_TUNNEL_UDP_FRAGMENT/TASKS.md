# TASKS: 028 — NESTED_TUNNEL_UDP_FRAGMENT

- [x] Фикс: `UDPFragmentDefault = true` в `protocol/wireguard/endpoint.go`
      (NewEndpoint + CreateDialer)
- [x] Фикс: `UDPFragmentDefault = true` в `protocol/masque/outbound.go`
- [x] Юнит-тесты DF-флага: `common/dialer/udp_fragment_lx_test.go` +
      пер-OS хелперы (darwin/linux/other)
- [x] e2e-стенд: `test/wireguard_detour_lx_test.go` (AWG-over-AWG, fits +
      fragments) + replace submodule в `test/go.mod`
- [x] Доки: lx-config §MTU (EN+RU) — заметка о снятом DF
- [x] Changelog: `#### v1.14.0-lx.12`
- [x] SPECS/README.md: строка 028 в Roadmap
- [x] gofmt/go vet/сборка с lx-тегами
- [ ] Field-тест CPH2411 (после сборки релиза)
