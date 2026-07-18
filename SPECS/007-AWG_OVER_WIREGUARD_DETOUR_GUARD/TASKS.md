# TASKS — 007-B-AWG_OVER_WIREGUARD_DETOUR_GUARD

> ⛔️ **ИСТОРИЧЕСКИЙ ДОКУМЕНТ.** Guard **удалён из ядра 2026-07-18** (см.
> [SPEC.md](SPEC.md)). Чек-лист ниже относится к реализации, которой больше нет.

## Итерация 1 — ленивый dialer-guard (lx.8) — ОТКАЧЕНА
- [x] guard в `common/dialer/detour.go` `init()` + проброс через `dialer.Options`
- [x] **Удалено** (не сработало на устройстве; `common/dialer/{detour,dialer}.go` → upstream)

## Итерация 2 — Start-guard (lx.9) — field-verified
- [x] `protocol/wireguard/endpoint.go`: поля `awgActive`/`detour`/`awgChainBlocked`,
      `awgDetourChainReachesWireGuard` (транзитивный обход detour, стоп на группе),
      вариант B в `Start` (device не поднимается, `started=false`, лог, `return nil`)
- [x] `awg_start_guard_test.go`: direct/транзитив/нет-WG/селектор-пропуск/цикл/unknown
- [x] Текст ошибки — «amneziawg over wireguard is not supported» (архитектура, не платформа)

## Итерация 3 — selector-guard (рантайм-переключение)
- [x] `adapter/outbound.go`: `OutboundManager.ConsumersOf` + маркер `AmneziaWGSuspendable`
- [x] `adapter/outbound/manager.go`: реализация `ConsumersOf` (reverse-deps под RLock)
- [x] `protocol/wireguard/endpoint.go`: `IsAmneziaWG()` + `SuspendAmneziaWG()` (CAS+лог)
- [x] `transport/wireguard/endpoint.go`: `Suspend()` (device.Down, идемпотентно)
- [x] `protocol/group/selector.go`: гашение **до** `Store` (Swap→Load+Store)
- [x] `protocol/group/awg_selector_guard.go`: `chainReachesWireGuard` + `suspendAmneziaWGConsumers`
- [x] `awg_selector_guard_test.go`: прямой/транзитивный AWG suspend, не-AWG не трогается, не-WG → no-op

## Приёмка (DoD)
- [x] `go build ./...` без тегов — ок
- [x] `go build -tags "...,with_awg" ./cmd/sing-box` — ок
- [x] `go test ./protocol/wireguard/... ./protocol/group/...` — зелёные
- [x] `gofmt -l` изменённых lx-файлов — пусто
- [x] `go vet` затронутых пакетов — чисто
- [x] **Field-verified** Start-guard на Android lx.9; selector-guard — юнит-тестами

## Закрытие
- [x] IMPLEMENTATION_REPORT.md, SPEC, DoD
- [x] `SPECS/README.md` roadmap-строка 007
- [x] GH issue #2: комментарии со ссылками на коммиты + закрыт
- [x] Статус → `C` (шапка SPEC.md + Roadmap)
