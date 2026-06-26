# SPEC: 004 — BUILD_CI_RELEASE

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) |

Собрать воспроизводимый конвейер сборки/CI/релизов `sing-box-lx`: кросс-платформенные бинари `sing-box` **и Android `libbox.aar`** с клиентским feature-set (полный upstream минус серверные/AI-теги) + lx-фичами (`with_xhttp`/`with_awg`), версия `-lx.N`, и **авто-ребейз на новый upstream-тег**.

> **Процедура выпуска → [docs/lx-release-runbook.md](../../docs/lx-release-runbook.md).**
> Главное правило: **перед любым тегом проверить дрейф upstream и обычно смержить его себе, и только
> потом резать релиз/пререлиз.** На ветке `lx-1.14` авто-ребейз на стабильный тег (ниже) заменён
> ручным `git merge upstream/testing` — пока upstream на `v1.14.*-alpha`, стабильного тега нет, а
> rc-линия `vX-lx.1-rc.N` сама является форматом поставки.

---

## 1. Проблема / контекст

Реальная стоимость downstream'а — не первичная разработка, а N ребейзов в год и регулярные сборки на 3 платформы. Нужен конвейер, который ловит «фичи поломались об новый upstream» раньше пользователя и выпускает drop-in бинарь для лаунчера.

## 2. Требования

### 2.1 Сборка
- **Desktop-бинарь `sing-box`** (drop-in для лаунчера): цель `make -f Makefile.lx lx-build`, output `sing-box`.
- **Набор `LX_TAGS`** — upstream feature-set (`release/DEFAULT_BUILD_TAGS`) **минус нерелевантные клиенту**: `with_tailscale` (нет tailscale-endpoint'ов), `with_ccm`/`with_ocm` (прокси Claude Code / OpenAI Codex — серверные AI-сервисы), `with_acme` (серверный выпуск TLS-сертов). Итог = `gvisor/quic/dhcp/wireguard/utls/clash_api/naive_outbound + badlinkname/tfogo_checklinkname0` **+ `with_purego`** (CGO-free кросс-сборка `with_naive_outbound` через prebuilt cronet) **+ `with_xhttp,with_awg`**. `Makefile.lx` — единственный источник истины (`make -f Makefile.lx lx-print-tags`).
- **`LX_LDFLAGS` обязан содержать `-checklinkname=0`** — иначе `badlinkname`/`tfogo_checklinkname0` ломают линк (`common/badtls` использует `go:linkname` в `crypto/tls`, который Go 1.24 блокирует). Зеркалит upstream `build_libbox`.
- **Android `libbox.aar`**: `make lib_install && make lib_android` (gomobile, NDK r28 + OpenJDK 17). `with_xhttp`/`with_awg` зашиты в `cmd/internal/build_libbox` (lx:-блок) → попадают в `libbox.aar` (SDK 23) и `libbox-legacy.aar` (SDK 21). Набор тегов AAR = upstream mobile-set **минус `with_tailscale`** (как desktop — самая тяжёлая либа в APK; правка обёрнута `// lx:no-tailscale`) + наши две фичи (NDK/CGO-сборка, `with_purego` не нужен).
- Версия `vX.Y.Z-lx.N` через ldflags (из 001); для AAR — через `git describe` внутри `build_libbox`.

### 2.2 CI-матрица
- **Политика триггеров (стоимость per-commit ↓).** Doc-only коммиты (`**.md`/`docs/**`/`SPECS/**`/LICENSE) **не запускают CI** (`paths-ignore`). На каждый push/PR — **только дешёвые** job'ы `lint` + `build-check`. Тяжёлые `cross` (6 таргетов) и `android` (gomobile AAR) — **только вручную, на `workflow_dispatch`** (`gh workflow run lx-ci.yml --ref lx` или кнопка Actions → Run workflow); на push их нет. Полную кросс-сборку + обе AAR на каждый релиз-тег и так гарантирует `lx-release.yml`. Серия быстрых пушей отменяет устаревшие прогоны (`concurrency: cancel-in-progress`).
- **`lint`** (push/PR): `go vet` по lx-пакетам с полными тегами + `gofmt` только по lx-файлам (`v2rayxhttp|_xhttp|_awg`, не по всему дереву upstream).
- **`build-check`** (push/PR): один нативный build `with_xhttp,with_awg` + `sing-box check` XHTTP/AWG2-конфигов (должны пройти); затем tagless baseline-бинарь → `check minimal.json` (проходит) + negative-check (XHTTP/AWG2-конфиги без тегов отвергаются).
- **`cross`** (dispatch): `{linux, darwin, windows} × {amd64, arm64}`, full `LX_TAGS`, CGO=0 — проверка, что полный набор + `with_purego` кросс-собирается везде.
- **`android`** (dispatch): `make lib_android` (NDK r28 + JDK17 + gomobile) — libbox AAR собирается с lx-фичами.
- Все job'ы — с submodule (`submodules: recursive`) и `fetch-depth: 0` (для `-lx` версии через `git describe`).

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

- CI зелёный: на push/PR — дешёвые `lint` + `build-check`; полная матрица (`cross` ×6 с полным `LX_TAGS` + `android` AAR) — вручную на `workflow_dispatch`. Doc-only коммиты CI не триггерят; релиз-тег собирает всё через `lx-release.yml`.
- Артефакты собираются, бинарь называется `sing-box`, `version` → `-lx.N`.
- **libbox AAR собирается в CI (job `android`) и публикуется в Release**; `Libbox.version()` → `-lx.N`; конфиг с AWG2/XHTTP не падает с «support not built».
- Авто-ребейз workflow отрабатывает на `workflow_dispatch` (демо на текущем теге → «уже актуально» или PR).

## 4. Вне скоупа

- Подпись кода/нотаризация; публикация AAR в Maven/jitpack (отдаём только GitHub Release asset).
- Интеграция AAR в приложение-потребитель (LxBox) — задача на стороне приложения.
- Полностью автоматический мёрж ребейза (всегда ревью).

## 5. Ссылки

- [Build from source — sing-box](https://sing-box.sagernet.org/installation/build-from-source/)
