# FEATURE 001 — BUILD_CI_CD — сборка, пайплайны, релиз

| Поле | Значение |
|------|----------|
| Тип | Процессная фича (инфраструктура форка) |
| Слой | `Makefile.lx`, `.github/workflows/lx-*.yml` |
| Состояние | ✅ Работает; релизы выходят |

Чем форк собирается и как выпускается. Отдельная фича, потому что у форка
инфраструктура своя: свой набор build-тегов, свои таргеты (роутеры на musl),
свой релизный пайплайн и свои источники зависимостей.

## Как устроено сейчас

**Принцип: нулевой дифф против upstream-инфраструктуры.** Всё наше живёт
в отдельных файлах — `Makefile.lx` рядом с `Makefile`, `lx-*.yml` рядом
с апстрим-workflow. Это условие дешёвых мержей.

### Build-теги

`LX_TAGS` — upstream feature set минус нерелевантное клиенту
(`with_tailscale`, `with_ccm`/`with_ocm`, `with_acme`) плюс `with_purego`
и наши фичи:

```
with_gvisor, with_quic, with_dhcp, with_wireguard, with_utls, with_clash_api,
with_naive_outbound, with_purego, badlinkname, tfogo_checklinkname0,
with_xhttp, with_awg, with_lx_command
```

⚠️ **Два набора тегов расходятся намеренно.** `with_clash_api` остаётся
в desktop/CLI (внешние дашборды ходят по Clash REST) и убирается **только
из AAR** — см. [OBSERVABILITY](../006-OBSERVABILITY/FEATURE.md).

⚠️ `with_purego` и `badlinkname` требуют `-checklinkname=0` в `LX_LDFLAGS`,
иначе линковка падает.

⚠️ **`badlinkname` требует Go 1.24.x.** На Go 1.25 полная сборка не линкуется
(в `crypto/tls` исчезла функция, на которую опирается linkname) — это
не дефект нашего кода. Проверять мержи на Go 1.25 можно набором тегов
**минус** `badlinkname` и `naive`.

### Таргеты

| Платформа | Особенность |
|-----------|-------------|
| Desktop ×6 | Обычные кросс-сборки |
| Linux musl ×4 | amd64 / arm64 / armv7 / mipsle-softfloat — **статика** для роутеров (AsusWRT, OpenWrt, Keenetic), без зависимости от `libdl.so.2`, NaïveProxy сохранён |
| AAR ×2 | Android-библиотека для LxBox |
| Windows | Несёт `libcronet.dll` (purego); извлечение запинено по `go.mod` |

⚠️ **`naive` на darwin мёртв без CGO-джобы**: darwin-dylib для cronet
не существует, поэтому macOS-сборка идёт на macos-раннере со статикой CGO.

### Пайплайны

| Workflow | Что делает |
|----------|------------|
| `lx-ci.yml` | Дешёвый гейт на каждый push в `lx` и любую ветку `lx-*` (включая релизную линию и экспериментальные): lint + build-check. Doc-only коммиты пропускаются (`paths-ignore` включает `SPECS/**`) |
| `lx-build.yml` | Артефакты по требованию, в том числе Apple xcframework |
| `lx-release.yml` | Релиз по тегу: cross ×6 + musl ×4 + AAR |
| `lx-musl-toolchain-mirror.yml` | Producer: заливает Chromium musl-тулчейн в release-ассет |
| `lx-rebase.yml` | Авто-ребейз на стабильные теги апстрима (исключает alpha/beta/rc). ⚠️ На 1.14-линии фактически спит: синк идёт ручным merge до появления стабильного `v1.14.0` |

**Зеркало тулчейна** — не удобство, а необходимость: `snapshot.debian.org`
периодически отдаёт 503 и блокирует релиз, а `actions/cache` промахивается
из-за ref-scoping тегов. Порядок источников: `actions/cache` → наше зеркало →
`snapshot.debian.org`. При бампе `CRONET_GO_VERSION` producer нужно запускать
вручную, иначе релиз упадёт.

### Релиз

⚠️ **`docs-lx/lx-changelog.md` — источник release notes.** Пайплайн извлекает
секцию `#### v<версия>` из changelog в описание GitHub-релиза. Заголовок должен
быть ровно `#### v<tag-without-v>`; неверная или отсутствующая секция даёт
неверные или пустые notes **автоматически**. Notes руками не пишутся.

⚠️ **Ветку пушить раньше тега** (иначе релиз выйдет с тегом впереди ветки).
Push через `gh auth token` во встроенном URL — обычный `git push origin`
падает на запросе имени пользователя.

## Задачи фичи

| Задача | Роль | Статус |
|--------|------|--------|
| [001 — FORK_BOOTSTRAP](../../TASKS/001-FORK_BOOTSTRAP/SPEC.md) | Remotes, ветка, `Makefile.lx`, версия `-lx`, скелет CI | C |
| [004 — BUILD_CI_RELEASE](../../TASKS/004-BUILD_CI_RELEASE/SPEC.md) | Теги, дешёвый CI, релизный пайплайн, поставка libcronet, авто-ребейз | C |
| [006 — LINUX_MUSL_STATIC_ROUTER_BUILDS](../../TASKS/006-LINUX_MUSL_STATIC_ROUTER_BUILDS/SPEC.md) | Статические musl-сборки под роутеры (4 арки) | C |
| [023 — MUSL_TOOLCHAIN_MIRROR](../../TASKS/023-MUSL_TOOLCHAIN_MIRROR/SPEC.md) | Durable-зеркало Chromium musl-тулчейна | C |

Полный runbook релиза — [docs-lx/lx-release-runbook.md](../../../docs-lx/lx-release-runbook.md).

## Особенности сопровождения

- **`gofmt -l` перед коммитом** по lx-файлам: линтер CI это проверяет,
  а `go vet` — нет (однажды уехало красным в релиз).
- **Проверять дрейф апстрима до тега**, а не после —
  см. [UPSTREAM_SYNC](../005-UPSTREAM_SYNC/FEATURE.md).
- **Релиз ядра — задача этого репозитория.** LxBox потребляет через
  `libbox.version` и собственных релизов ядра не выпускает.
