# IMPLEMENTATION_REPORT — 006 LINUX_MUSL_STATIC_ROUTER_BUILDS

**Дата:** 2026-06-12 · **Статус:** Complete — **приёмка CI пройдена (4/4 арки статикой)** · **База:** `v1.13.13`

## Итог

Релизные Linux-бинари переведены на **статическую musl-сборку с сохранением NaïveProxy**, добавлены роутерные арки. Закрывает [issue #1](https://github.com/Leadaxe/sing-box-lx/issues/1): `libdl.so.2: cannot open shared object file` на AsusWRT Merlin + отсутствие `linux-armv7`.

**Go-кода нет** — задача чисто инфраструктурная (CI). Единственная Go-правка в этой ветке — hotfix gofmt-выравнивания в `option/wireguard_awg.go` (хвост 005, см. ниже).

## Диагноз (эмпирически подтверждён)

`with_naive_outbound` тянет `cronet-go`. Прежний релиз собирался в режиме `with_purego` (`CGO_ENABLED=0`): `purego` на Linux содержит `//go:cgo_import_dynamic … "libdl.so.2"` → бинарь **динамический**, требует `libdl.so.2`. На glibc ок, на musl (роутеры) — падает до старта. Кросс-сборкой проверено: `file` → `dynamically linked`, `strings|grep libdl.so.2` → 1; без naive/purego → `statically linked`, 0.

naive **сохраняем** — это upstream-фича (`release/DEFAULT_BUILD_TAGS` содержит `with_naive_outbound`; `protocol/naive/outbound.go` без `lx:`-маркеров). Поэтому не дропаем, а используем третий режим cronet-go — `with_musl` (статический `libcronet.a` + musl-toolchain), как делает upstream `build.yml`.

## Что сделано

**`.github/workflows/lx-release.yml`** — новый job `build_linux_musl` (зеркало upstream musl-pipeline):
- матрица: `linux-amd64`, `linux-arm64`, `linux-armv7` (`GOARM=7`), `linux-mipsle-softfloat` (`GOMIPS=softfloat`);
- clone `cronet-go` по pin `.github/CRONET_GO_VERSION` (`2faf34666c2c`, = go.mod) + submodules; regenerate keyring; cache + download Chromium **musl** toolchain через `cmd/build-naive --libc=musl`; set env;
- `CGO_ENABLED=1 go build` с тегами `LX_TAGS` (с заменой `with_purego`→`with_musl`, `with_naive_outbound` сохранён);
- verify-шаг: `file → statically linked`, `! grep libdl.so.2`;
- Linux убран из desktop-job `build` (остаются darwin/windows/win7); `release.needs += build_linux_musl`; release-notes обновлены.

**`.github/workflows/lx-ci.yml`** — dispatch-only smoke-job `linux_musl` (те же 4 арки): полный musl-pipeline + build + verify static, **без публикации** — безопасная приёмка. Помечен «keep in sync with build_linux_musl».

**Нейминг** — по upstream-схеме арочных суффиксов (`armv7` = arm+`v`+GOARM; `mipsle-softfloat` = arch+GOMIPS), но **без суффикса `-musl`**: upstream добавляет его, т.к. собирает и glibc, и musl на арку; у нас Linux — единственный (musl) вариант, и `linux-arm64`/`linux-armv7` совпадают с ожиданием скриптов потребителей.

## Приёмка

- ✅ YAML валиден (`python yaml`), `actionlint` чист (rc=0) для обоих workflow.
- ✅ CI smoke (`lx-ci` workflow_dispatch, job `linux_musl` ×4, run [27407702652](https://github.com/Leadaxe/sing-box-lx/actions/runs/27407702652)) — все 4 **success**, `file` → `statically linked`, `libdl.so.2=0`:
  - `linux-amd64`: ELF 64-bit x86-64, statically linked;
  - `linux-arm64`: ELF 64-bit ARM aarch64, statically linked;
  - `linux-armv7`: ELF 32-bit ARM EABI5, statically linked;
  - `linux-mipsle-softfloat`: ELF 32-bit MIPS32 rel2, statically linked — **mipsle+naive собрался musl-статикой**, fallback не понадобился.
- ✅ Боевой релиз [v1.13.13-lx.7](https://github.com/Leadaxe/sing-box-lx/releases/tag/v1.13.13-lx.7) опубликован — 4 musl-арки + desktop (darwin/win/win7) + 2 AAR + SHA256SUMS.
- ✅ **Field-verified** репортером issue #1 на AsusWRT Merlin RT-AX (`linux/arm64`): ядро устанавливается, стартует, работает; `sing-box version` → `1.13.13-lx.7`, теги включают `with_naive_outbound,with_musl`, `CGO: enabled`. Подтверждение на реальном устройстве, не только в CI.

> **Нейминг — апдейт от потребителя:** репортер подтвердил, что суффикс `-musl` для его скрипта **некритичен** (берёт архив с суффиксом или без). То есть наш выбор «без `-musl`» валиден без оглядки на чужой скрипт — это просто следствие единственного варианта на арку. `-softfloat` у mipsle при этом **обязателен** (FP-ABI, не линковка): softfloat запускается на любом MIPS-роутере, hardfloat — только на чипах с FPU.

## Побочный hotfix (хвост 005)

`option/wireguard_awg.go`: расширение `H1..H4 uint32 → MagicHeader` сменило самый длинный тип в struct, gofmt перевыровнял json-теги. `go vet` это не ловит, поэтому ушло в lx.6 с красным дешёвым CI (`lint` job). Исправлено `gofmt -w`, format-only. Урок: прогонять `gofmt -l` на lx-owned файлах перед коммитом.

## Зона касания upstream (для ребейза)

`lx-release.yml` / `lx-ci.yml` — **lx-собственные** файлы (в upstream их нет) → конфликтов на ребейзе не дают. `.github/CRONET_GO_VERSION` — upstream-файл, **только читаем**. Паттерн musl-pipeline заимствован из upstream `build.yml` как референс.

## Вне скоупа

- Экзотика (`386`/`riscv64`/`loong64`/`mips64le`) — точечно по запросу; cronet-musl под `mips64le` нет.
- DEB/RPM/Pacman/OpenWrt-пакеты — не публикуем (только `.tar.gz`).
- naive на Win7 (windows/386) — физически невозможен (нет `cronet-go/lib/windows_386`).
- Перепин `libbox.version` в лаунчере — на стороне LxBox.
