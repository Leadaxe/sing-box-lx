# TASKS — 004-F-N-BUILD_CI_RELEASE

## Сборка (desktop)
- [x] `Makefile.lx`: `lx-build` (output `sing-box`), `LX_TAGS`, `LX_LDFLAGS`, версия `-lx.N`
- [x] `LX_TAGS` → клиентский feature-set (`DEFAULT_BUILD_TAGS` минус tailscale/ccm/ocm/acme) + `with_purego` + `with_xhttp,with_awg`
- [x] `LX_LDFLAGS` += `-checklinkname=0` (иначе линк падает на `badtls`/`crypto/tls`)
- [x] `lx-print-tags` — единый источник истины для CI/release (без дублирования строки тегов)

## Сборка (Android libbox AAR)
- [x] `cmd/internal/build_libbox/main.go`: `with_xhttp,with_awg` в `sharedTags` + дроп `with_tailscale` (lx:-маркеры) → обе AAR-варианта
- [x] CI собирает `libbox.aar` + `libbox-legacy.aar` зелёным (NDK r28 + OpenJDK 17 + gomobile) — подтверждено в релизе v1.13.13-lx.3 (job `build android` = success)
- [x] `Libbox.version()` отдаёт `-lx.N`; AAR (libbox-1.13.13-lx.3.aar + legacy) опубликованы в Release

## CI
- [x] `lx-ci.yml` триггеры: push/PR (paths-ignore docs) + `workflow_dispatch`; `concurrency: cancel-in-progress`
- [x] **push/PR — дёшево:** `lint` (go vet lx-пакетов + gofmt lx-файлов) + `build-check` (1 нативный build `full` + `check`, baseline-бинарь + negative-check)
- [x] **`workflow_dispatch` — тяжёлое (вручную):** `cross` `{linux,darwin,windows}×{amd64,arm64}` на полном `LX_TAGS` + `android` (`make lib_android`); на push не запускаются
- [x] submodule init (`submodules: recursive`) + `fetch-depth: 0` во всех job'ах
- [x] Зелёный прогон `lint`+`build-check` на push (подтверждено); `cross` ×6 + `android` AAR — зелёные в релизном прогоне v1.13.13-lx.3 (cronet/naive/purego на всех 6)

## Авто-ребейз
- [x] `lx-rebase.yml`: fetch upstream tags → выбрать новейший **стабильный** (`^v[0-9]+\.[0-9]+\.[0-9]+$`) → rebase в CI
- [x] Успех+билд → ветка `lx-rebase/<tag>` + PR; конфликт/билд-фейл → issue с диффом `// lx:` зон; up-to-date → no-op
- [x] Запрет авто-force-push в `lx` (пушим только в `lx-rebase/<tag>`); PR/issue; `schedule` (еженедельно) + `workflow_dispatch`
- [x] Демо-прогон `workflow_dispatch` (tag=v1.13.13 → «up to date», 0 side-effects). PR-путь требует чекбокс «Allow Actions to create PRs» (иначе fallback в issue)

## Релизы
- [x] `lx-release.yml`: on tag `v*-lx.*` → cross-build desktop, zip, checksums, GitHub Release
- [x] job `build_android` → `libbox-<ver>.aar` + `libbox-legacy-<ver>.aar` в Release
- [x] Release notes: upstream-база + `with_xhttp`/`with_awg` + `LX_TAGS` (через `lx-print-tags`) + AAR
- [x] Проверить релиз end-to-end — **v1.13.13-lx.3 опубликован** (6 desktop + 2 AAR + SHA256SUMS, всё зелёное). Корень 403: Workflow permissions = Read/write

## Закрытие
- [x] DoD: зелёная CI-матрица (desktop ×6 + AAR) — релиз v1.13.13-lx.3
- [ ] IMPLEMENTATION_REPORT.md
- [ ] Папка → `C`
