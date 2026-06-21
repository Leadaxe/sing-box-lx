# 010 — Верификация фикса (GRO split-brain) на железе

Фикс из SPEC шаг 1 применён. Этот `.aar` = **фикс + probe** (probe оставлен
специально, чтобы прогон показал, что фикс взвёл `rxoffload=false`).

## Что в фиксе

Гейт за `!android` (TX/GSO не тронуты):
- `controlfns_linux.go`: `setsockopt(UDP_GRO)` пропускается на android.
- `features_linux.go`: `rxOffload` не читается на android → всегда `false`.

submodule `wg-gro-android-fix` @ `fb8d8d8` (чистый фикс) →
verify-вариант `wg-gro-android-fix-verify` @ `08947cc` (фикс + probe-геттер).

## Артефакт (verify)

| Файл | SDK | sha256 |
|------|-----|--------|
| `dist/lx-wg-gro-fix-verify/libbox-gro-fix-verify.aar` | 23 | `b454c35b08aa7ada6634a41278fa6caf281a7a5a64488231f757c4df48d3d098` |
| `dist/lx-wg-gro-fix-verify/libbox-legacy-gro-fix-verify.aar` | 21 | `6eacc5b2b6c2166d35531a58404964e3e61404a8d458b631958b55b680cbe2c1` |

Собрано из `lx-wg-gro-fix-verify` @ `9996dc0a`; submodule `08947cc` — подтверждено
по логу checkout CI. probe-маркер вкомпилирован.

## Прогон (тот же, что для probe)

1. Вложить `.aar`, `core_logs_enabled=true`.
2. WARP-endpoint **без `detour`** (та же нода, где download был мёртв).
3. Дать трафик, снять core-log: `GET /logs?source=core&q=gro-probe`.
4. **Прогнать реальный download** (плотный поток) и сравнить с предыдущим прогоном.

## Критерий приёмки

| Сигнал | Вывод |
|---|---|
| `rxoffload4=false rxoffload6=false` (+ `dispatch=single`) | фикс взвёлся — GRO на android выключен |
| **download ожил** (сопоставим с `detour: direct`), upload жив | **фикс подтверждён на железе** |

Если `rxoffload` стал `false`, **но download всё ещё мёртв** → GRO был не единственной
причиной; открываем кандидат №2 (тихий хэндовер, `monitor.go`) — см. `SPEC.md`.

## После подтверждения

- probe удаляется (endpoint.go Errorf-строка + OffloadState-геттер) — он временный.
- на merge в `lx` идёт чистый фикс: submodule `wg-gro-android-fix` (`fb8d8d8`),
  main — бамп pin без probe.
- временные ветки (`*-verify`, `gro-probe-010`, `lx-gro-probe-010`) удаляются.
