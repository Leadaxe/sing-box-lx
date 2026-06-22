# SPEC: 006 — LINUX_MUSL_STATIC_ROUTER_BUILDS

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) |

Публиковать **статические musl-бинари** `sing-box` под роутерные Linux-арки, **сохраняя NaïveProxy-outbound**. Закрывает [issue #1](https://github.com/Leadaxe/sing-box-lx/issues/1): нужен `linux-armv7`, и текущий `linux-arm64` не запускается на AsusWRT Merlin (`libdl.so.2: cannot open shared object file`).

---

## 1. Проблема / контекст

- Лаунчер-аудитория ставит ядро на роутеры (AsusWRT Merlin, OpenWrt, Keenetic) — это **musl**-окружения.
- Текущие релизные `linux-amd64/arm64` собраны в режиме `with_purego` (`CGO_ENABLED=0`). `purego` через `//go:cgo_import_dynamic … "libdl.so.2"` делает бинарь **динамическим** и вешает зависимость от `libdl.so.2`. На glibc-десктопе ок, на musl — загрузчик падает до старта. Проверено эмпирически: `file` → `dynamically linked`, `strings | grep libdl.so.2` → 1 совпадение.
- `linux-armv7` в релизе **отсутствует** вовсе.
- **NaïveProxy-outbound — штатная upstream-фича** (`release/DEFAULT_BUILD_TAGS` содержит `with_naive_outbound`; `protocol/naive/outbound.go` — upstream-код без `lx:`-маркеров). По CONSTITUTION (upstream + ровно 2 фичи, из upstream ничего не выкусываем) её **нельзя** дропать ради статики.

## 2. Цель

Релиз публикует статические, самодостаточные (без `libdl`) musl-бинари под 4 роутерные арки, **с работающим naive**:

| Арка | Параметры | Покрытие |
|------|-----------|----------|
| `linux-amd64` | musl | x86 софт-роутеры, контейнеры, универсальный |
| `linux-arm64` | musl | современные роутеры (ASUS AX/AXE, GL.iNet, Xiaomi), arm64-роутер автора |
| `linux-armv7` | musl, `GOARM=7` | ASUS на старых SoC — прямой запрос issue |
| `linux-mipsle` | musl, `GOMIPS=softfloat` | классика OpenWrt (MediaTek/Atheros) |

Без `with_awg`/`with_xhttp` поведение не меняется — это чисто сборочная задача (CI), **Go-кода нет**.

## 3. Требования

### 3.1 Механизм — по подобию upstream `build.yml`
- Сборка musl-варианта повторяет upstream: clone `cronet-go` по pin `.github/CRONET_GO_VERSION` (уже совпадает с `go.mod`: `2faf34666c2c`), regenerate Debian keyring, download Chromium **musl** toolchain через `go run ./cmd/build-naive --target=linux/<arch> --libc=musl download-toolchain`, выставить env (`… env >> $GITHUB_ENV`), затем `CGO_ENABLED=1 go build` с тегом `with_musl` — `libcronet.a` линкуется статически.
- **zig не используется** — официальный путь cronet-go (Chromium toolchain) надёжнее и совпадает с upstream.

### 3.2 Теги
- Брать `LX_TAGS` (Makefile.lx, single source of truth), заменить `with_purego` → `with_musl`. `with_naive_outbound` **остаётся**. Остальные фичи (`with_xhttp,with_awg,…`) без изменений.

### 3.3 Нейминг артефактов
- По upstream-схеме арочных суффиксов: `armv7` (= `arm` + `v` + `GOARM`), `mipsle-softfloat` (= arch + `GOMIPS`).
- **Без суффикса `-musl`**: upstream добавляет его, т.к. собирает и glibc, и musl на арку; у нас на Linux единственный вариант — musl, поэтому суффикс избыточен и сломал бы ожидание скриптов (`linux-arm64`/`linux-armv7`).
- Итог: `sing-box-<ver>-linux-amd64.tar.gz`, `…-linux-arm64.tar.gz`, `…-linux-armv7.tar.gz`, `…-linux-mipsle-softfloat.tar.gz`.

### 3.4 Не-Linux платформы — без изменений
- darwin amd64/arm64, windows amd64/arm64 — текущий `with_purego` путь (libdl чисто Linux-glibc, проблемы нет).
- Win7-386 — без naive (нет cronet под windows/386 — отдельная физическая причина, не линковка).
- Android AAR — отдельный job, без изменений.

## 4. Критерии приёмки

- Релизные `linux-{amd64,arm64,armv7,mipsle}` — `file` → **statically linked**, `strings | grep libdl.so.2` → **0**.
- naive присутствует (тег `with_naive_outbound` в сборке; по возможности — функциональная проверка).
- armv7/mipsle запускаются на реальном/эмулированном musl-роутере (`sing-box version` без ошибки загрузчика).
- darwin/windows/win7/android-ассеты не изменились по составу.
- **Верификация — через CI** (`workflow_dispatch`): Chromium musl-toolchain (гигабайты) недоступен локально на macOS, поэтому локальной сборки musl нет — приёмка по прогону workflow.

## 5. Вне скоупа

- Экзотические арки (`386`, `riscv64`, `loong64`, `mips64le`) — добавляются точечно строкой матрицы по запросу; cronet-musl под `mips64le` вообще нет.
- DEB/RPM/Pacman/OpenWrt-пакеты (upstream их делает) — нам не нужны, публикуем `.tar.gz`.
- naive на Win7 — физически невозможен (нет `cronet-go/lib/windows_386`).

## 6. Ссылки

- [issue #1](https://github.com/Leadaxe/sing-box-lx/issues/1)
- upstream `.github/workflows/build.yml` (v1.13.13) — референс musl-pipeline.
