# PLAN: 028 — NESTED_TUNNEL_UDP_FRAGMENT

## Фазы

### Фаза 0 — Локализация (ЗАВЕРШЕНА)
- [x] Гипотеза «WG не фрагментирует крупные UDP» сведена к коду:
      `common/dialer/default.go:173-182` — без `UDPFragmentDefault` дайлер
      вешает `DisableUDPFragment()` (DF) на dial- И listener-контролы.
- [x] Подтверждено, что флаг доезжает до обоих бинд-путей endpoint'а
      (StdNetBind через `UDPListenerControl()`, ClientBind через дайлер).
- [x] Сверка с upstream-паттерном: direct/hysteria/hysteria2/tuic ставят
      `UDPFragmentDefault = true`; wireguard/masque — нет.
- [x] Математика пакетов вложенных туннелей (SPEC §2): оверсайз нижнего плеча
      — норма, а не аномалия.

### Фаза 1 — Фикс (ЗАВЕРШЕНА)
- [x] `protocol/wireguard/endpoint.go`: `UDPFragmentDefault = true` в
      `NewEndpoint` + в per-interface `CreateDialer`.
- [x] `protocol/masque/outbound.go`: то же перед созданием дайлера.

### Фаза 2 — Верификация (ЗАВЕРШЕНА, кроме field)
- [x] Юнит DF-флага на реальном сокете (darwin/linux getsockopt), оба пути,
      3 кейса (default/протокольный default/явный override).
- [x] e2e-стенд AWG-over-AWG через detour (два box-инстанса, loopback),
      режимы fits (1420/1280) и fragments (1280/1280) — TCP+UDP+large-data.
- [x] `test/go.mod`: replace на submodule wireguard-go (без него AWG в
      тест-модуле резолвится в upstream и падает на `jc`).
- [ ] **Field-тест CPH2411** (SPEC §6): MASQUE-over-AWG и AWG-over-AWG на
      реальном пути телефон→дом.

## Риски
- Снятие DF маскирует кривой MTU (фрагментация вместо явного EMSGSIZE) —
  принятое поведение, рекомендации по MTU остаются в lx-config §MTU.
- DPLPMTUD MASQUE может завышать оценку MTU через detour (SPEC §5) —
  наблюдать в field, фикс отдельной задачей при подтверждении.
