# TASKS — 004-F-N-BUILD_CI_RELEASE

## Сборка (desktop)
- [x] `Makefile.lx`: `lx-build` (output `sing-box`), `LX_TAGS`, `LX_LDFLAGS`, версия `-lx.N`
- [x] `LX_TAGS` → клиентский feature-set (`DEFAULT_BUILD_TAGS` минус tailscale/ccm/ocm/acme) + `with_purego` + `with_xhttp,with_awg`
- [x] `LX_LDFLAGS` += `-checklinkname=0` (иначе линк падает на `badtls`/`crypto/tls`)
- [x] `lx-print-tags` — единый источник истины для CI/release (без дублирования строки тегов)

## Сборка (Android libbox AAR)
- [x] `cmd/internal/build_libbox/main.go`: `with_xhttp,with_awg` в `sharedTags` + дроп `with_tailscale` (lx:-маркеры) → обе AAR-варианта
- [ ] CI собирает `libbox.aar` + `libbox-legacy.aar` зелёным (NDK r28 + OpenJDK 17 + gomobile)
- [ ] `Libbox.version()` в AAR отдаёт `-lx.N`; конфиг с AWG2/XHTTP не падает «support not built»

## CI
- [x] `lx-ci.yml`: матрица `{linux,darwin,windows}×{amd64,arm64}` на полном `LX_TAGS`, submodule init, vet
- [x] feature-toggle (baseline/xhttp/awg/оба) + `sing-box check` + negative-check
- [x] job `android` (`make lib_android`)
- [ ] Зелёный прогон всей матрицы на GH (верификация полного набора + cronet/naive под 6 таргетов)

## Авто-ребейз
- [ ] `lx-rebase.yml`: fetch upstream tags → выбрать новейший **стабильный** → rebase в CI
- [ ] Успех → ветка `lx-rebase/<tag>` + PR; конфликт/падение → issue с диффом `// lx:` зон
- [ ] Запрет авто-force-push в `lx`; только PR
- [ ] Демо-прогон `workflow_dispatch`

## Релизы
- [x] `lx-release.yml`: on tag `v*-lx.*` → cross-build desktop, zip, checksums, GitHub Release
- [x] job `build_android` → `libbox-<ver>.aar` + `libbox-legacy-<ver>.aar` в Release
- [x] Release notes: upstream-база + `with_xhttp`/`with_awg` + `LX_TAGS` (через `lx-print-tags`) + AAR
- [ ] Проверить релиз end-to-end на тестовом теге `v*-lx.*`

## Закрытие
- [ ] DoD-чеклист; зелёная CI-матрица (desktop ×6 + AAR)
- [ ] IMPLEMENTATION_REPORT.md
- [ ] Папка → `C`
