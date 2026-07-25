# IMPLEMENTATION_REPORT 023 — Durable-зеркало musl-тулчейна

**Статус:** реализовано и функционально проверено. Ветка `lx-1.14` (= `lx`), коммит `5e345ffe`.

## Что сделано

| Файл | Тип | Суть |
|------|-----|------|
| `.github/workflows/lx-musl-toolchain-mirror.yml` | new (lx-owned) | Producer: `workflow_dispatch`, матрица 4 арок → `download-toolchain` (с snapshot.debian.org, retry×8) → `tar --zstd` → `gh release upload` в релиз `musl-toolchain-cache`. Имя ассета `toolchain-<arch>-<cronet>.tar.zst`. |
| `.github/workflows/lx-release.yml` | edit (lx-owned) | Restore-шаг между `Cache Chromium toolchain` и `Download Chromium musl toolchain`: на cache-miss `gh release download` из `musl-toolchain-cache` → `tar -C naiveproxy/src -x`. Существующий download-шаг остаётся как 3-й fallback (snapshot.debian.org). |
| `SPECS/TASKS/023-*` | new | SPEC/PLAN/TASKS/этот отчёт. |

**Приоритет источников тулчейна:** `actions/cache` → **lx release-mirror** → `snapshot.debian.org`.

## Проверка (DoD)

- ✅ YAML обеих workflow валиден; `actionlint` — чисто.
- ✅ Producer-прогон (`28606115433`) — **success**; зеркало `musl-toolchain-cache` содержит 4 ассета (amd64/arm64/arm/mipsle, ~222–227 МБ каждый, под лимитом 2 ГБ), ключ = версия cronet-go `98d539ce…`.
- ✅ Restore проверен локально end-to-end: имя ассета совпадает с формулой restore-шага; `gh release download` резолвит ассет; структура tar (`third_party/llvm-build`, `gn/out`, `chrome/build`, `out/sysroot-build`) ложится ровно под `tar -C naiveproxy/src`.
- ⏳ Restore на реальном release-прогоне при cache-miss — подтвердится на следующем релизе (ожидаемая строка в логе: `restored toolchain from lx mirror`).

## Зона ребейза

Нулевая — обе workflow lx-owned, upstream их не содержит и не трогает.

## Эксплуатация (важно)

**При каждом бампе `.github/CRONET_GO_VERSION` — вручную запустить `lx-musl-toolchain-mirror`** (workflow_dispatch). Иначе зеркало устаревает по ключу-имени, restore промахивается, и release падает обратно на snapshot.debian.org. Producer можно запускать только когда snapshot.debian.org жив (он там разово качает тулчейн для наполнения).

## Замечания по контексту

- Родилось из падений релиза `v1.14.0-lx.2-rc.1`: snapshot.debian.org дважды отдал 503 в окне сборки. `actions/cache` не спас из-за ref-scoping (сборка тега не видит кеш другого тега).
- Producer зависит от snapshot.debian.org **один раз** (наполнение); дальше все релизы restore'ят из нашего ассета.
- Дефолт-ветка репо — `lx`; чтобы producer был dispatchable, `lx` был fast-forward'нут к `lx-1.14` (чистый предок, работа не потеряна).
