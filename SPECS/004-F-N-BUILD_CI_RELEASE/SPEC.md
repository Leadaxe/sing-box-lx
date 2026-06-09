# SPEC: 004 — BUILD_CI_RELEASE

Собрать воспроизводимый конвейер сборки/CI/релизов `sing-box-lx`: кросс-платформенные бинари `sing-box` **и Android `libbox.aar`** с полным feature-set + lx-фичами (`with_xhttp`/`with_awg`), версия `-lx.N`, и **авто-ребейз на новый upstream-тег**.

---

## 1. Проблема / контекст

Реальная стоимость downstream'а — не первичная разработка, а N ребейзов в год и регулярные сборки на 3 платформы. Нужен конвейер, который ловит «фичи поломались об новый upstream» раньше пользователя и выпускает drop-in бинарь для лаунчера.

## 2. Требования

### 2.1 Сборка
- **Desktop-бинарь `sing-box`** (drop-in для лаунчера): цель `make -f Makefile.lx lx-build`, output `sing-box`.
- **Набор `LX_TAGS`** — полный feature-set upstream (`release/DEFAULT_BUILD_TAGS`: gvisor/quic/dhcp/wireguard/utls/acme/clash_api/tailscale/ccm/ocm/naive_outbound + badlinkname/tfogo_checklinkname0) **+ `with_purego`** (CGO-free кросс-сборка `with_naive_outbound` через prebuilt cronet) **+ `with_xhttp,with_awg`**. `Makefile.lx` — единственный источник истины (`make -f Makefile.lx lx-print-tags`).
- **`LX_LDFLAGS` обязан содержать `-checklinkname=0`** — иначе `badlinkname`/`tfogo_checklinkname0` ломают линк (`common/badtls` использует `go:linkname` в `crypto/tls`, который Go 1.24 блокирует). Зеркалит upstream `build_libbox`.
- **Android `libbox.aar`**: `make lib_install && make lib_android` (gomobile, NDK r28 + OpenJDK 17). `with_xhttp`/`with_awg` зашиты в `cmd/internal/build_libbox` (lx:-блок) → попадают в `libbox.aar` (SDK 23) и `libbox-legacy.aar` (SDK 21). Набор тегов AAR = upstream mobile-set + наши две фичи (NDK/CGO-сборка, `with_purego` не нужен).
- Версия `vX.Y.Z-lx.N` через ldflags (из 001); для AAR — через `git describe` внутри `build_libbox`.

### 2.2 CI-матрица
- Платформы: `{linux, darwin, windows} × {amd64, arm64}` (full `LX_TAGS`, CGO=0 — здесь же проверяется, что полный набор + `with_purego` кросс-собирается везде).
- Feature-toggle: baseline / `with_xhttp` / `with_awg` / оба — каждый билд + `sing-box check` своего конфига; negative-check (без тега конфиг фичи отвергается).
- Шаги: `make lx-build` → `go vet` → `sing-box check` на sample-конфигах XHTTP и AWG2.
- **Job `android`**: `make lib_android` (NDK+JDK+gomobile) — доказывает, что libbox AAR собирается с lx-фичами.
- Сборка с submodule (`git submodule update --init`).

### 2.3 Авто-ребейз на upstream-тег
- Workflow по расписанию/`workflow_dispatch`:
  1. `git fetch upstream --tags`, определить новейший стабильный тег `> текущей базы`.
  2. Попытка `git rebase <tag>` ветки `lx` в CI.
  3. Успех + сборка/`check` зелёные → пуш ветки `lx-rebase/<tag>` и **PR**; конфликт → **issue** с диффом `// lx:` зон.
- Никогда не пушить силой в `lx` автоматически — только через PR с ревью.

### 2.4 Релизы
- Тег `vX.Y.Z-lx.N` → артефакты: desktop-архивы (`sing-box` × 6 платформ) **+ `libbox-<ver>.aar` и `libbox-legacy-<ver>.aar`**, общий `SHA256SUMS`.
- Release notes: upstream-база + состояние фич (`with_xhttp`/`with_awg`) + полный `LX_TAGS` desktop-бинаря (через `lx-print-tags`) + строка про AAR.

## 3. Критерии приёмки

- CI зелёный на push в `lx` по всей матрице (включая полный `LX_TAGS` на всех 6 таргетах).
- Артефакты собираются, бинарь называется `sing-box`, `version` → `-lx.N`.
- **libbox AAR собирается в CI (job `android`) и публикуется в Release**; `Libbox.version()` → `-lx.N`; конфиг с AWG2/XHTTP не падает с «support not built».
- Авто-ребейз workflow отрабатывает на `workflow_dispatch` (демо на текущем теге → «уже актуально» или PR).

## 4. Вне скоупа

- Подпись кода/нотаризация; публикация AAR в Maven/jitpack (отдаём только GitHub Release asset).
- Интеграция AAR в приложение-потребитель (LxBox) — задача на стороне приложения.
- Полностью автоматический мёрж ребейза (всегда ревью).

## 5. Ссылки

- [Build from source — sing-box](https://sing-box.sagernet.org/installation/build-from-source/)
