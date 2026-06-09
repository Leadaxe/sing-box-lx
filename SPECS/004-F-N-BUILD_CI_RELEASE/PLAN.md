# PLAN: 004 — BUILD_CI_RELEASE

## 1. Файлы

| Файл | Тип | Изменения |
|------|-----|-----------|
| `Makefile` | дополнение (из 001) | `lx-build`, `lx-release` (cross-compile матрица), `LX_TAGS`, `LX_LDFLAGS` |
| `.github/workflows/lx-ci.yml` | расширение (из 001) | Матрица OS×ARCH, submodule init, build+vet+test+`check` |
| `.github/workflows/lx-rebase.yml` | **new** | schedule/dispatch: fetch upstream tags → rebase → build → PR/issue |
| `.github/workflows/lx-release.yml` | **new** | on tag `v*-lx.*`: cross-build, zip, checksums, GitHub Release |
| `lx-test/config/xhttp_reality.json`, `awg2_basic.json` | из 002/003 | Используются в CI `check` |
| `scripts/lx/cross_build.sh` | **new** | Матрица `GOOS/GOARCH` → `dist/<os>-<arch>/sing-box` |

## 2. Версия / ldflags

`LX_LDFLAGS = -X github.com/sagernet/sing-box/constant.Version=<upstream>-lx.<N>` (точный путь/символ — по факту из 001). `<N>` — счётчик lx-релизов поверх данного upstream-тега.

## 3. Авто-ребейз (lx-rebase.yml) — логика

```
fetch upstream --tags
latest = max(stable tags)
if base(lx) == latest: exit "up to date"
git checkout -b lx-rebase/$latest lx
git rebase $latest        # конфликты → собрать дифф // lx: зон, открыть issue, stop
make lx-build && go vet && sing-box check ...   # упало → issue
push origin lx-rebase/$latest ; gh pr create
```

## 4. Порядок работ

1. Расширить `Makefile` (cross-build) и `lx-ci.yml` до полной матрицы.
2. `lx-rebase.yml` (сначала `workflow_dispatch`, потом cron).
3. `lx-release.yml` + `cross_build.sh`.
4. Демо-прогон ребейз-workflow на текущем теге.

## 5. Риски

- Кросс-сборка с `with_gvisor`/cgo-зависимостями (wireguard/amneziawg) — проверить, что AWG-submodule собирается под все `GOOS/GOARCH` (особенно windows/arm64).
- Авто-ребейз на **alpha/beta** теги нежелателен — фильтровать только стабильные (`vX.Y.Z` без суффиксов).
- `git submodule` в CI — не забыть `--init --recursive` и pin.
