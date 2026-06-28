# 020 — Множество WG/AWG-узлов греют телефон: idle-устройства держат буферы → scan-bound GC

| Поле | Значение |
|------|----------|
| Тип | B (bug) — расследование завершено, фикс спроектирован |
| Статус | **O (open)** — корень доказан heap-профилем на устройстве (high confidence). Воркэраунд («убрать лишние WG из конфига») подтверждён на устройстве Ильи. Код-фикс НЕ реализован. |
| Зона | submodule `wireguard-go` (`device/` — slim batch) + ядро `sing-box-lx` (`transport/wireguard/endpoint.go`, `protocol/group/awg_selector_guard.go`, refcount по графу route) |
| Связь | [010-WG_ENDPOINT_GRO_SPLIT_BRAIN](../010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (GRO нужен 65535-буфер — НЕ трогать размер), [007-AWG_OVER_WIREGUARD_DETOUR_GUARD](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) (Suspend() = точка приглушения), memory `android-cpu-heat-multi-wg-gc`, `wg-1.14-migration-is-submodule-rebase` |
| Репортер | Iliya, 2026-06-28 (CPH2411 / OnePlus, Adreno 642L, Android) |

---

## Симптом

На Android новое 1.14-ядро (LxBox 2.5.0+) держит все ядра CPU на максимуме и греет
телефон. VPN внешне работает нормально. Морда закрыта — всё равно греет. Сначала
казалось типонезависимым (VLESS/Trojan/WG). «Не у всех устройств». Освобождение
RAM **не помогает**.

---

## Корень (доказан heap + CPU pprof, §207-механизм)

Греет **фоновый сборщик мусора Go**, сканирующий большую pointer-плотную живую кучу.

- **CPU-профиль (горячий, 10с):** `runtime.scanobject` 52.65%, `gcBgMarkWorker`
  доминирует, 350% CPU (~3.5 ядра), ~1516 GC-циклов / 10с. GC **scan-bound**
  (mallocgc лишь 9%). `markroot`/stack-scan минимален — дело НЕ в горутинных стеках.
- **heap inuse_space:** **224 МБ (92.75%) из 242 МБ = `wireguard-go device.(*Device).PopulatePools`** — буферы `[65535]byte` × ~10 WG-устройств.
- **A/B на устройстве:** селектор с ~10 WG/WARP → 479 горутин + нагрев; убрать WG
  (только VLESS/Trojan) → 70 горутин, 0 WG Device, норма. **Убрать лишние WG из
  конфига → нагрев уходит** (подтверждено Ильёй).

### Конфиг, на котором воспроизводится

`endpoints`: **10 WG/AWG** (WireGuard-1, WARP, QUIC-google, WireGuard-kiberportal,
WireGuardStatic, WireGuardStun, WARP-AWG-STUN/SIP/QUICK/DNS). Все перечислены в 4
группах: `vpn-1` (selector), `vpn-1-auto` (urltest), `vpn-2`, `vpn-2-auto`. Активен
1, остальные приглушены через awg-detour-guard.

### Механизм удержания (развилка решена: «помечается, но НЕ отпускает»)

1. `endpoints` стартуют **eager** все на старте инстанса (upstream-механизм
   `adapter/endpoint/manager.go:55`, одинаков в 1.13/1.14).
2. Каждое устройство в `NewDevice` зовёт `PopulatePools` (`device/pools.go:49`):
   `messageBuffers = NewWaitPool(PreallocatedBuffersPerPool=4096, new([65535]byte))`
   (`queueconstants_android.go:17-18`). Потолок одного пула ≈ 4096×65535 ≈ 256 МБ.
3. Приглушение неактивных = awg-detour-guard → `Endpoint.Suspend()` →
   `device.Down()` (`endpoint.go:294`). **`Down()`/`downLocked()` (`device.go:223`)
   делает ТОЛЬКО `BindClose()` + `peer.Stop()`** — НЕ закрывает device-очереди и НЕ
   чистит пул. Освобождение всего этого только в `Close()` (`device.go:406`:
   close очередей + `RemoveAllPeers` + `stopping.Wait`), которого guard не вызывает.
4. **Главный держатель 224 МБ:** живые device-воркеры `RoutineReceiveIncoming` /
   `RoutineReadFromTUN` держат `bufsArrs = make([]*[65535]byte, maxBatchSize)`
   (`receive.go:89`, `maxBatchSize = BatchSize() = IdealBatchSize = 128`) —
   **128 × 65535 ≈ 8 МБ СИЛЬНЫМИ ссылками** на устройство + пул. Воркеры висят на
   `for range device.queue.*.c`, выходят только при `Close()`. `Down()` их не трогает.

### Почему совпало со всем

1.14 (re-graft WG submodule на v0.0.3 + `MaxSegmentSize` 2200→65535 ради GRO) ·
память не лечит (это scan живой Go-кучи) · не у всех (только конфиги с многими WG +
устройства, где трафик активен) · типонезависимо (gVisor netstack/WG-воркеры общие).

### Ложные следы (НЕ трогать)

- **`MaxSegmentSize` 65535 → 2200** — буфер нужен для UDP_GRO (см. §010,
  upstream-родной фикс в 1.14). Откат задушит download-скорость.
- **oomkiller / FreeOSMemory** — исключён профилем (0.085%, не активен на Android).
- **Размер `sync.Pool`** — он сам чистится GC, «обрезать» нечего; держат не пул, а
  живые воркеры (сильные ссылки) + поле `device.pool` живого `*Device`.

---

## Решение: SLIM idle-устройства (ужать, не закрывать) + refcount по графу route

**Не закрывать** (`Close()`), потому что: (а) пинги/keepalive/health-check должны
проходить — устройство остаётся живым; (б) узел может быть **общим** (в нескольких
селекторах/правилах) — `Close()` по сигналу одного селектора убьёт трафик другого.

**Ужать**: у idle-устройства урезать `maxBatchSize` (главный держатель —
`bufsArrs` в receive/read-воркерах) с 128 до малого (напр. 1–4). Экономия
~8 МБ→~0.06 МБ на устройство по этому держателю + меньше элементов в обороте.
Применяется через Down→Up цикл (receive-горутины пересоздаются с новым batch в
`BindUpdate`, `device.go:558`).

### Когда устройство «used» (refcount по достижимости от трафика)

`Suspend`-сигнал одного селектора НЕ означает простой узла. Корректный критерий:

> **used(node)** = node достижим от ЛЮБОГО активного источника трафика:
> `route.rules[].outbound` ∪ `route.final` — вниз по текущим выборам групп
> (`Selector.Now()` / urltest current) и detour-цепочкам (`Dependencies`).

- Не-used (недостижим ни от одного route-источника) → **slim**.
- Used → **full**.
- Пересчёт при switch селектора / смене активного правила.

Источники, которые надо учесть (все три, не только селектор):
`rule.outbound` (`route.go:113/127`), `route.final` (`route.go:156`), текущий выбор
selector/urltest, detour-цепочки. Инфраструктура есть: `Dependencies()`,
`ConsumersOf()` (`adapter/outbound.go:48`), `Now()` (`selector.go:113`).

---

## План реализации

### Submodule `wireguard-go` (slim-режим)
- Параметризовать `maxBatchSize` для receive/read-воркеров: добавить
  `device.SetSlim(bool)` или slim-поле, которое `BatchSize()`/`BindUpdate`
  (`device.go:368/558`) читают при пере-старте receive-горутин. Slim → batch=1–4.
- НЕ трогать `MaxSegmentSize` (GRO), НЕ трогать lifecycle (`Close`/очереди).
- Обратимость: slim↔full через Down→Up (воркеры пересоздаются), `peer.Start`/keepalive
  сохраняются (upLocked это уже делает).

### Ядро `sing-box-lx`
- `transport/wireguard/endpoint.go`: `Suspend()` → перевод устройства в slim
  (`device.Down()` + slim-флаг + `Up()`), а не голый `Down()`. Возврат в full при
  попадании узла в used-множество.
- **refcount/used-множество**: вычислитель достижимости от route-источников.
  Пересчёт на switch (`selector.go:133` уже точка пересборки guard) — расширить
  guard, чтобы он не только suspend-ил AWG, но и помечал full/slim по used.
- `awg_selector_guard.go`: расширить — текущий `suspendAmneziaWGConsumers` ходит
  вверх по `ConsumersOf`; добавить расчёт used-достижимости от rules+final+Now.

### Открытые вопросы (дорешать при реализации)
1. Точное slim-значение batch (1 / 2 / 4) — измерить, чтобы keepalive/health-probe
   проходили без деградации.
2. Где держать used-множество и кто его пересчитывает на смене route-правил (не
   только селектора) — selector switch это покрывает не всё.
3. urltest health-check пингует всех членов → может временно поднимать трафик через
   slim-узел; slim batch должен выдержать health-probe (≥1).
4. Гонки: пересчёт used ↔ конкурентный dial. slim↔full переход должен быть
   идемпотентным и потокобезопасным (как уже `Suspend()` идемпотентен).

---

## Верификация (когда фикс готов)
- Re-снять heap-профиль (§207) на конфиге Ильи с 10 WG: `PopulatePools` должен
  упасть с ~224 МБ до ~(1×full + 9×slim).
- CPU-профиль: `scanobject` доля должна резко упасть, нагрев уйти.
- A/B: full-узел (активный) сохраняет download-скорость (GRO цел); переключение
  селектора между WG-узлами не теряет трафик у общих узлов.
