# PLAN: 030 — FAST_BOX_SHUTDOWN

## Фазы

### Фаза 0 — Локализация (ЗАВЕРШЕНА)
- [x] Симптом: `stop` виснет 10с+ на Android при ~30 пропинганных WG/AWG-нодах.
- [x] Аудит (3 параллельных read-only + синтез): доминанта — `resumeMu`-contention
      с in-flight пинг-wake, просуммированная последовательно; НЕ worker-drain.
- [x] Порядок в `box.Close`: `{endpoint}` закрывается раньше `{router}` →
      idle-тик жив и шлёт wake, пока endpoints закрываются.
- [x] Отвергнут «жёсткий дроп drain»: use-after-free gVisor netstack (unsafe.Pointer).

### Фаза 1 — Фикс (ЗАВЕРШЕНА)
- [x] Шаг 1: `Router.QuiesceForShutdown` (stop tick + DevicePause) в
      `reachability_common_lx.go`; вызов в начале `box.Close`.
- [x] Шаг 2: `closing atomic.Bool` в WG-endpoint; ставится в `Close` до resumeMu;
      `resumeOnDial` возвращает false при closing (fast-path + под lock).
- [x] Шаг 3: следствие шага 1 (сокеты закрыты вперёд → stopping.Wait мгновенный).
- [x] Шаг 4: `endpoint.Manager.Close` → `task.Group` Concurrency(8), Run без FastFail.

### Фаза 2 — Верификация (ЗАВЕРШЕНА, кроме field)
- [x] Юниты механизма (closing-гейт): red (не компилится без поля) / green.
- [x] e2e-smoke: 20 нод, Close ~5мс, без паники/дедлока параллели.
- [x] Регресс: SPEC 020 юниты + SPEC 028/029 detour-стенды.
- [x] Обе сборки (idle-suspend on/off) + gofmt/vet.
- [ ] **Field-тест на устройстве** (SPEC §6): ~30 нод, пинг → stop → замер.

## Риски
- `task.Group` без FastFail обязателен — джойнить все closes (иначе abandon →
  use-after-free). Concurrency ограничен (память под gVisor-teardown).
- `QuiesceForShutdown` в always-compiled файле — работает и без тега idle-suspend
  (stopIdleSuspend там no-op, DevicePause валиден всегда).
