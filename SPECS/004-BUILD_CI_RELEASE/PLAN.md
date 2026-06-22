# PLAN: 004 — BUILD_CI_RELEASE

## 1. Файлы

| Файл | Тип | Изменения |
|------|-----|-----------|
| `Makefile.lx` | дополнение (из 001) | `lx-build` + новый `lx-print-tags`; `LX_TAGS` = клиентский feature-set (upstream минус `tailscale`/`ccm`/`ocm`/`acme`) + `with_purego` + наши 2; `LX_LDFLAGS` += `-checklinkname=0` |
| `cmd/internal/build_libbox/main.go` | upstream-правка (lx:-маркер, §3.3) | `with_xhttp,with_awg` в `sharedTags` → попадают в `libbox.aar` (SDK23) и `libbox-legacy.aar` (SDK21) |
| `.github/workflows/lx-ci.yml` | расширение (из 001) | full-tag матрица OS×ARCH (CGO=0) + feature-toggle + `go vet` + job **`android`** (`make lib_android`) |
| `.github/workflows/lx-rebase.yml` | **new** | schedule/dispatch: fetch upstream tags → rebase → build → PR/issue |
| `.github/workflows/lx-release.yml` | расширение | on tag `v*-lx.*`: cross-build desktop (через `Makefile.lx`, без дублирования тегов) + job **`build_android`** (AAR) → zip/checksums/GitHub Release |
| `lx-test/config/xhttp_reality.json`, `awg2_basic.json` | из 002/003 | Используются в CI `check` |

## 2. Версия / ldflags

`LX_LDFLAGS = -X github.com/sagernet/sing-box/constant.Version=<upstream>-lx.<N> -checklinkname=0 -s -w -buildid=`. `<N>` — счётчик lx-релизов поверх upstream-тега. **`-checklinkname=0` обязателен** для полного набора тегов (`badlinkname` → `go:linkname` в `crypto/tls` через `common/badtls`; Go 1.24 блокирует без флага). AAR версионируется отдельно — `build_libbox` берёт `git describe`, поэтому в обоих workflow перед сборкой AAR создаётся/обновляется тег.

**Наборы тегов (два разных, по типу сборки):**
- Desktop (cross, `CGO_ENABLED=0`): `release/DEFAULT_BUILD_TAGS` **минус `tailscale`/`ccm`/`ocm`/`acme`** + `with_purego` + `with_xhttp,with_awg` → `Makefile.lx` `LX_TAGS`.
- AAR (gomobile, NDK/CGO): upstream `build_libbox` mobile-set **минус `with_tailscale`** (`// lx:no-tailscale`, как desktop) + `with_xhttp,with_awg`. `with_purego`/`with_acme` не добавляем — это desktop/server-теги.

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

1. `Makefile.lx`: полный `LX_TAGS` + `-checklinkname=0` + `lx-print-tags`; `build_libbox` — теги фич в AAR.
2. `lx-ci.yml`: full-tag матрица OS×ARCH + feature-toggle + job `android`.
3. `lx-release.yml`: desktop cross-build (через `Makefile.lx`) + job `build_android` → публикация desktop-архивов + AAR.
4. `lx-rebase.yml` (сначала `workflow_dispatch`, потом cron).
5. Демо-прогон ребейз-workflow на текущем теге.

## 5. Риски

- **`-checklinkname=0`** (РЕШЕНО): без него полный набор не линкуется (`badtls`/`crypto/tls`). Локально подтверждено для linux/amd64 и windows/arm64; остальные 4 таргета верифицирует CI-матрица.
- **`with_naive_outbound` через cronet** тянет prebuilt `cronet-go/lib/<os>_<arch>` — если под какой-то таргет prebuilt отсутствует, naive там не соберётся → дропнуть naive на этой платформе (или из набора целиком). Проверяет CI-матрица.
- **AAR-сборка**: требует NDK r28 + OpenJDK 17 + gomobile (`make lib_install`); `build_libbox.checkJavaVersion()` ждёт строго `openjdk 17`. Версия AAR = `git describe`, поэтому тег должен существовать в чекауте.
- Авто-ребейз на **alpha/beta** теги нежелателен — фильтровать только стабильные (`vX.Y.Z` без суффиксов).
- `git submodule` в CI — не забыть `--init --recursive` и pin (нужно и для `with_awg`, и для AAR).
