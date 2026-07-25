# PLAN: 006 — LINUX_MUSL_STATIC_ROUTER_BUILDS

## 1. Архитектура

Один новый job в `lx-release.yml` — `build_linux_musl` — повторяющий upstream `build.yml` musl-секцию, но с нашим `LX_TAGS`. Существующий desktop-путь не ломаем: из старого `build` job убираем строки `linux/*`, остальное (darwin/windows/win7) остаётся на `Makefile.lx`/purego.

```
build              (desktop, как сейчас минус linux): darwin×2, windows×2, win7-386
build_linux_musl   (NEW): linux musl-static + naive: amd64, arm64, armv7, mipsle
build_android      (без изменений): libbox.aar ×2
release            (как сейчас): собирает артефакты всех job
```

## 2. `build_linux_musl` — шаги (зеркало upstream)

Матрица:
```yaml
- { arch: amd64,  asset: linux-amd64 }
- { arch: arm64,  asset: linux-arm64 }
- { arch: arm,    goarm: "7",            asset: linux-armv7 }
- { arch: mipsle, gomips: softfloat,     asset: linux-mipsle-softfloat }
```

Шаги:
1. `checkout` (submodules: recursive — нужен `submodules/wireguard-go` для with_awg).
2. `setup-go` (go-version-file: go.mod).
3. **Clone cronet-go**: `CRONET=$(cat .github/CRONET_GO_VERSION)`; `git init ~/cronet-go`; remote `sagernet/cronet-go`; `fetch --depth=1 $CRONET`; checkout; `submodule update --init --recursive --depth=1` (тянет naiveproxy/src — Chromium toolchain source).
4. **Regenerate Debian keyring**: `…/sysroot_scripts/generate_keyring.sh` (для sysroot download).
5. **Cache Chromium toolchain** (`actions/cache`): пути `llvm-build/`, `gn/out/`, `pgo_profiles/`, `out/sysroot-build/`; key по arch+`CRONET_GO_VERSION`. Гигабайты — кеш обязателен.
6. **Download toolchain**: `cd ~/cronet-go && go run ./cmd/build-naive --target=linux/<arch> --libc=musl download-toolchain`.
7. **Set toolchain env**: `… --libc=musl env >> $GITHUB_ENV` (выставляет `CC`/`CXX`/CGO_*).
8. **Build tags**: `TAGS=$(make -f Makefile.lx -s lx-print-tags)`; `TAGS="${TAGS/with_purego/with_musl}"`.
9. **Build (musl)**: `CGO_ENABLED=1 GOOS=linux GOARCH=<arch> GOARM=<goarm> GOMIPS=<gomips> go build -trimpath -tags "$TAGS" -ldflags "<LX_LDFLAGS>" -o dist/sing-box ./cmd/sing-box`.
10. **Verify static** (приёмка в логах): `file dist/sing-box` → `statically linked`; `! strings dist/sing-box | grep -q libdl.so.2`.
11. **Package**: `sing-box-<ver>-<asset>.tar.gz` (binary + LICENSE/README), как в desktop job.
12. `upload-artifact`.

> `LX_LDFLAGS` берём из Makefile.lx (`-checklinkname=0 -s -w -buildid=` + Version). Тот же набор, что у desktop-сборки.

## 3. Изменяемые / новые файлы

| Файл | Тип | Изменение |
|------|-----|-----------|
| `.github/workflows/lx-release.yml` | lx-own CI | новый job `build_linux_musl`; из `build` убрать linux-строки; `release.needs` += новый job; notes.md — роутерные арки |
| `SPECS/006/*`, `SPECS/README.md` | docs | спека + roadmap |

**Go-кода нет.** `Makefile.lx` править не обязательно (теги берём из него же; musl-логика живёт в CI, т.к. требует Chromium-toolchain env, которого в Makefile не выразить переносимо).

## 4. Зона касания upstream (для ребейза)

`lx-release.yml` — **lx-собственный** файл (создан в 004), в upstream его нет → конфликтов на ребейзе не даёт. Паттерн заимствован из upstream `build.yml`, но как референс, не как правка upstream-файла. `.github/CRONET_GO_VERSION` — upstream-файл, мы его **только читаем** (pin уже совпадает с go.mod), не меняем.

## 5. Порядок работ

1. SPEC/PLAN/TASKS (done).
2. Реализовать `build_linux_musl` + почистить linux из `build` + `release.needs` + notes.
3. Локально: валидация YAML (actionlint/python-yaml). Полную musl-сборку локально не проверить — Chromium toolchain только в CI.
4. Прогон `workflow_dispatch` (dev-тег) → читать логи, чинить toolchain/линковку итеративно.
5. На зелёном — verify-шаги (static/libdl) в логах; по возможности запуск armv7 под qemu-user.
6. REPORT, статус C, ответ в issue #1. Боевой релиз — тегом `v1.13.13-lx.7`.

## 6. Риски

- **Локально не верифицируемо** — отладка только через CI; закладываем несколько прогонов. Кеш toolchain критичен для скорости.
- **Время/размер**: Chromium toolchain — гигабайты; musl-бинарь крупнее (вшит libcronet, +неск. МБ). Приемлемо для релиза по тегу (не на каждый push).
- **mipsle softfloat**: проверить, что toolchain build-naive поддерживает target `linux/mipsle` + `GOMIPS=softfloat`. Если cronet-musl/mipsle не соберётся — fallback: mipsle через `DEFAULT_BUILD_TAGS_OTHERS` (без naive, `CGO_ENABLED=0`, статика) как делает upstream для арок без cronet. Зафиксировать в REPORT.
- **keyring/sysroot download** может флапать (внешняя Chromium infra) — ретраи.
