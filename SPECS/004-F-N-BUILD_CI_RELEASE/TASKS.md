# TASKS — 004-F-N-BUILD_CI_RELEASE

## Сборка
- [ ] `Makefile`: `lx-build` (output `sing-box`), `lx-release` (cross), `LX_TAGS`, `LX_LDFLAGS`
- [ ] `scripts/lx/cross_build.sh`: матрица `GOOS/GOARCH` → `dist/<os>-<arch>/sing-box`
- [ ] Версия `vX.Y.Z-lx.N` через ldflags

## CI
- [ ] `lx-ci.yml`: матрица `{linux,darwin,windows}×{amd64,arm64}`, submodule init, build+vet+test
- [ ] `sing-box check` на `xhttp_reality.json` и `awg2_basic.json`

## Авто-ребейз
- [ ] `lx-rebase.yml`: fetch upstream tags → выбрать новейший **стабильный** → rebase в CI
- [ ] Успех → ветка `lx-rebase/<tag>` + PR; конфликт/падение → issue с диффом `// lx:` зон
- [ ] Запрет авто-force-push в `lx`; только PR
- [ ] Демо-прогон `workflow_dispatch`

## Релизы
- [ ] `lx-release.yml`: on tag `v*-lx.*` → cross-build, zip, checksums, GitHub Release
- [ ] Release notes: upstream-база + состояние `with_xhttp`/`with_awg`

## Закрытие
- [ ] DoD-чеклист; проверить кросс-сборку AWG-submodule под windows/arm64
- [ ] IMPLEMENTATION_REPORT.md
- [ ] Папка → `C`
