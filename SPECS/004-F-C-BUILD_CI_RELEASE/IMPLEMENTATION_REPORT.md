# IMPLEMENTATION_REPORT — 004 BUILD_CI_RELEASE

**Дата:** 2026-06-09 · **Статус:** Complete — конвейер сборки/CI/релиза/ребейза рабочий, **релиз `v1.13.13-lx.3` опубликован** · **База:** `v1.13.13`

## Итог

Воспроизводимый downstream-конвейер: кросс-платформенный бинарь `sing-box` + Android `libbox.aar`, дешёвый per-commit CI, авто-ребейз на upstream-теги и публикация релизов. End-to-end доказано релизом **`v1.13.13-lx.3`** (6 desktop-архивов + 2 AAR + `SHA256SUMS`, весь прогон зелёный).

## Сборка

**Desktop** — `Makefile.lx` (`lx-build`, output `sing-box`); единый источник тегов — `make -f Makefile.lx lx-print-tags`:
`with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg`
= upstream-клиент **− acme/tailscale/ccm/ocm** **+ `with_purego`** (CGO-free кросс-сборка `with_naive_outbound`/cronet при CGO=0) **+ `with_xhttp,with_awg`**. `LX_LDFLAGS` += **`-checklinkname=0`** (badtls `go:linkname` в `crypto/tls`, Go 1.24).

**Android AAR** — `cmd/internal/build_libbox/main.go` (`// lx`-блок): `with_xhttp+with_awg` зашиты в `sharedTags`, `with_tailscale` снят (`// lx:no-tailscale`) → `libbox.aar` (SDK23) + `libbox-legacy.aar` (SDK21) через `make lib_install && make lib_android` (NDK r28 + OpenJDK 17 + gomobile). `Libbox.version()` → `-lx.N`.

## CI — `lx-ci.yml` (политика «дёшево на коммит»)

- Триггеры: `push`/`pull_request` (`paths-ignore`: `**.md`/`docs/**`/`SPECS/**`/`LICENSE`) + `workflow_dispatch`; `concurrency: cancel-in-progress`.
- **push/PR → только дешёвое:** `lint` (go vet lx-пакетов + `gofmt` lx-файлов `v2rayxhttp|_xhttp|_awg`) + `build-check` (1 нативный build `full` + `sing-box check` XHTTP/AWG2 + tagless baseline → negative-check, что фичеконфиги без тегов отвергаются).
- **`workflow_dispatch` → тяжёлое (вручную):** `cross` `{linux,darwin,windows}×{amd64,arm64}` (полный `LX_TAGS`, CGO=0 — проверка `with_purego` кросс-сборки) + `android` AAR. На push **не** запускаются.
- doc-only коммиты CI не триггерят.

## Релизы — `lx-release.yml`

- on tag `v*-lx.*` → `build` (6 desktop, tar.gz/zip) + `build_android` (2 AAR) → `release`: `SHA256SUMS` + GitHub Release с notes (база `v1.13.13` + фичи + `lx-print-tags` + строка про AAR). Версия из тега, `sing-box version` → `-lx.N`.
- **`v1.13.13-lx.3` опубликован** (Latest): 6 архивов + `libbox-1.13.13-lx.3.aar` + `libbox-legacy-1.13.13-lx.3.aar` + `SHA256SUMS` — всё зелёное. Этот прогон впервые вживую подтвердил тяжёлый путь (cross ×6 с naive/cronet/purego + gomobile AAR + publish).
- **Windows 7 (32-bit)** legacy-таргет: `windows/386` собирается **пропатченным Go** (`.github/setup_go_for_windows7.sh` — реверты удаления Win7 из `MetaCubeX/go`, как в upstream `build.yml`) и **без `with_naive_outbound`** (`cronet-go` не имеет windows/386 — build constraints исключают всё). Артефакт `sing-box-<ver>-windows-386-legacy-windows-7.zip` — под лаунчер-сборку `singbox-launcher-win7-32` (она тоже 386). Остальной `LX_TAGS` (gvisor/quic/xhttp/awg/…) под 386 компилируется — проверено.

## Авто-ребейз — `lx-rebase.yml`

- `schedule` (Пн 06:00 UTC) + `workflow_dispatch` (опц. `tag`).
- fetch upstream → новейший **стабильный** тег (`^v[0-9]+\.[0-9]+\.[0-9]+$` — отсекает `-alpha/-beta/-rc` и наши `-lx.N`).
- **up-to-date** → no-op; **чистый ребейз + build + `check`** → ветка `lx-rebase/<tag>` + **PR**; **конфликт / build-fail** → **issue** с конфликтными файлами и рецептом ручного ребейза.
- **Никогда не force-push'ит `lx`** — только новая ветка + PR/issue на ревью.
- Демо (`workflow_dispatch tag=v1.13.13`): `Pick target` → `Up to date?` → success, остальное skipped, **0 side-effects** (ни веток, ни PR, ни issue).

## Операционные настройки репозитория (критично для CI)

`gh api repos/OWNER/REPO/actions/permissions/workflow`:
- **`default_workflow_permissions: write`** — иначе `gh release create` падает с `403 Resource not accessible by integration` (это и был корень падений первых релизных прогонов lx.2). NB: «релиз для тега уже существует» — **другая** ошибка (`already exists`), не 403.
- **`can_approve_pull_request_reviews: true`** («Allow GitHub Actions to create and approve pull requests») — иначе авто-PR ребейза ботом блокируется (есть fallback в issue).
- Оба включены 2026-06-09.

## Зона касания upstream (ребейз)

Все lx-артефакты — **новые файлы**: `.github/workflows/lx-{ci,release,rebase}.yml`, `Makefile.lx`, `lx-test/config/`. Единственная правка upstream-файла — `// lx`-блок в `cmd/internal/build_libbox/main.go` (теги AAR). При ребейзе новые файлы переносятся как есть, блок в `build_libbox` — вручную по маркеру.

## Остаточное / дальше

- Старый релиз `v1.13.13-lx.1` можно удалить (предшествует XHTTP-фиксу / полным тегам / libbox; `lx.3` его замещает).
- Лаунчер (репо `singbox-launcher`, отдельно): маппинг `type=xhttp` → реальный xhttp (его задача 023 сейчас в httpupgrade); AWG-поля (Jc/S1–S4/H1–H4/I1–I5) в визард + парсер `awg.conf`; замена бандлового `bin/sing-box` на lx-релиз.
- (опц.) XHTTP `stream-one` framing-баг (`auto`/`packet-up` работают, не блокер).
