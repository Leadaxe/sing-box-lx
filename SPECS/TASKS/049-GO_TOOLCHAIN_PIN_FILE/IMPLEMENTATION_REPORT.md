# IMPLEMENTATION_REPORT 049 — `go.version` как единый пин Go-тулчейна

**Статус:** реализовано и проверено на релизном прогоне. Ветка `lx`, коммиты
`c895e6c8` (спека), `3641cfed` (реализация), `137890db` (ноты).
Уехало в [`v1.14.0-lx.20-rc.3`](https://github.com/Leadaxe/sing-box-lx/releases/tag/v1.14.0-lx.20-rc.3).

## Что сделано

| Файл | Тип | Суть |
|------|-----|------|
| `go.version` | new (lx-owned) | Единственное значение — `go1.25.12`. Формат с префиксом `go` = тег `golang/go`, чтобы годился и для `git checkout` в клоне тулчейна, и для `setup-go` после отрезания префикса. |
| `.github/workflows/lx-build.yml` | edit (lx-owned) | 4 джоба: шаг `Go toolchain pin` → `setup-go` читает `steps.go_pin.outputs.version`. |
| `.github/workflows/lx-ci.yml` | edit (lx-owned) | 5 джобов, то же. |
| `.github/workflows/lx-release.yml` | edit (lx-owned) | 4 джоба, то же. В матричном `build` шаг не под `if:` — он дешёвый, а `setup-go` остаётся под `if: ! matrix.legacy_win7`. |
| `.github/workflows/lx-rebase.yml` | edit (lx-owned) | Было `go-version-file: go.mod` → **собиралось на 1.24.7**, ровно на регрессии SPEC 044. |
| `.github/workflows/lx-musl-toolchain-mirror.yml` | edit (lx-owned) | То же; зеркало теперь строится тем же тулчейном, что и джоб, который его восстанавливает. |
| `SPECS/TASKS/049-*` | new | SPEC + этот отчёт. |

Извлечение: `tr -d '[:space:]' < go.version | sed 's/^go//'` → `1.25.12`.

**`check-latest` снят** там, где стоит точный патч: он искал бы более свежий
и ломал воспроизводимость, ради которой файл и заводится.

## Проверка (DoD)

- ✅ `go.version` в корне, содержимое `go1.25.12` (hexdump: завершающий `\n`, без CR).
- ✅ Все не-матричные `go-version:` в `lx-*.yml` читают файл — литеральных пинов
  и `go-version-file` в lx-workflow не осталось (15 шагов в пяти файлах).
- ✅ `go.mod` не изменён (`git diff --name-only` по нему пуст).
- ✅ CI зелёный, в логе `setup-go` видна 1.25.12 — **все** джобы релизного
  прогона [`30915254216`](https://github.com/Leadaxe/sing-box-lx/actions/runs/30915254216):
  `Successfully set up Go version 1.25.12`, в том числе
  `build android (libbox.aar)` → `go version go1.25.12 linux/amd64`.
- ✅ Значение попало в релизный тег: `v1.14.0-lx.20-rc.3`, ассеты
  `libbox-1.14.0-lx.20-rc.3.aar` и `libbox-legacy-*.aar` собраны на go1.25.12.

Отдельно ценно, что прогон покрыл `lx-musl-toolchain-mirror` и `lx-rebase`-соседей
по правке: `linux-musl` ×4 раньше брали версию из `go.mod` (1.24.7), теперь 1.25.12.

## Что НЕ тронуто (и почему)

- **`go.mod`** — там `1.24.7` — языковой floor, не тулчейн. Менять его = менять
  требование к компилятору у потребителей библиотеки.
- **upstream-workflow** (`build.yml`, `linux.yml`, `docker.yml`, `lint.yml`,
  `test.yml`) — иначе конфликт на каждом мерже с upstream. Принцип фичи 001 —
  нулевой дифф против upstream-инфраструктуры.
- **`.github/setup_go_for_windows7.sh`** — upstream-файл, байт в байт совпадает
  с `upstream/testing`, версию в нём бампает upstream (последний коммит —
  «Update Go to 1.25.12»), и его же использует upstream-овский `build.yml`.
  Правка дала бы конфликт на каждом upstream-бампе Go.

  ⚠️ **Win7 не может следовать общему пину в принципе.** Патчи MetaCubeX,
  возвращающие поддержку Windows 7, существуют только для ветки
  `release-branch.go1.25` (это написано в самом скрипте). Когда `go.version`
  уедет на 1.26, Win7 за ним не пойдёт. Сейчас значения совпадают (1.25.12) —
  **при бампе `go.version` сверять `VERSION=` в скрипте вручную.**

## Зона ребейза

Нулевая — `go.version` новый, все пять правленых workflow lx-owned, upstream их
не содержит и не трогает.

## Замечания по контексту

- **Повод внешний:** F-Droid ([fdroiddata!44731](https://gitlab.com/fdroid/fdroiddata/-/merge_requests/44731))
  собирает `libbox.aar` из исходников в своей песочнице и должен откуда-то
  прочитать версию тулчейна. Очевидный кандидат `go.mod` дал бы 1.24.7 —
  то есть незаметно сломанную сборку (SPEC 044: все quic-go-аутбаунды мертвы
  на вендорских Android-ядрах).
- До правки версия была размазана по workflow пятью спеллингами: `1.25.x`,
  `^1.25`, `^1.25.3`, `^1.25.4`, `1.25.12` — плюс два файла брали её из `go.mod`.
- Про `badlinkname` в FEATURE 001 было записано «требует Go 1.24.x» — это
  **устарело**: после upstream-перегейта `badtls` на `go1.25 && badlinkname`
  полный lx-набор снова собирается на go1.25 (подтверждено этим прогоном:
  AAR + CLI + musl). Формулировка в FEATURE.md исправлена вместе с этой задачей.
- Ноты релиза генерируются по коммитам; каркас `v1.14.0-lx.20-rc.3.md` собрался
  из SPEC 047/048 **до** появления 049, поэтому раздел про `go.version`
  дописан вручную (коммит `137890db`). При следующем релизе стоит проверить,
  по какому диапазону коммитов хук собирает файл.
