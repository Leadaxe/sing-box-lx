# PLAN: 029 — ENDPOINT_DETOUR_START_ORDER

## Фазы

### Фаза 0 — Локализация (ЗАВЕРШЕНА)
- [x] Telemetry устройства (:9269): `awg2-home` (detour→`wg-parnas`) мёртв при
      порядке «потребитель раньше провайдера»; смена порядка чинит.
- [x] Источник ошибки: только `DetourDialer.init` (`common/dialer/detour.go`),
      кэш через `sync.Once` навсегда.
- [x] Инструментированный stack-trace repro: резолв triggered
      `common.Cast[dialer.UDPListener]` в `NewEndpoint` (фаза Create), до
      сборки графа.
- [x] Подтверждено: топосорт `startOutbounds` упорядочивает старт по
      `Dependencies()` (detour = зависимость), endpoints в него влиты — но
      резолв утекал в Create, до барьера.

### Фаза 1 — Фикс (ЗАВЕРШЕНА)
- [x] A: каст egress-anchor обёрнут в `if options.Detour == ""` (убрать
      преждевременный резолв). Верифицировано: egress-pool неприменим к detour,
      `UDPListener` только у `DefaultDialer`, bind пере-выводится в транспорте.
- [x] B: `dialer.InitializeDetour(w.outboundDialer)` в
      `Start(StartStateStart)` после `endpoint.Start(false)` — за
      топосорт-барьером. Стадия Start (не Started) — самая ранняя безопасная.

### Фаза 2 — Верификация (ЗАВЕРШЕНА, кроме field)
- [x] Red/green e2e: `test/wireguard_detour_order_lx_test.go` (потребитель
      раньше провайдера). RED без фикса (FAIL ~92с), GREEN с фиксом (~2.8с).
- [x] Регресс: SPEC 020 idle-suspend юниты PASS; SPEC 028 AWG-over-AWG PASS.
- [x] gofmt/vet/build с lx-тегами чисто.
- [ ] **Field-тест на устройстве** (SPEC §6): сломанный порядок +
      AWG-over-AWG через `awg2-home`.

## Риски
- Фикс трогает `Endpoint.Start` — общий для SPEC 020 wake-пути; проверено, что
  `InitializeDetour` durable через suspend/resume (dialer один на процесс).
- `StartStateStart` полагается на топосорт-порядок endpoints — верифицировано
  на исходниках (`startOutbounds` + `Dependencies()` = detour).
