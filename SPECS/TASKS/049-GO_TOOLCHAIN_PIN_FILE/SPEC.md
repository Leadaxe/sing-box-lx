# SPEC: 049 — GO_TOOLCHAIN_PIN_FILE

**Фича:** [BUILD_CI_CD](../../FEATURES/001-BUILD_CI_CD/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | T (task) — инфраструктура сборки, кода форка не касается |
| Статус | C (complete) — в `v1.14.0-lx.20-rc.3`, см. [IMPLEMENTATION_REPORT](IMPLEMENTATION_REPORT.md) |
| Повод | Публикация LxBox в F-Droid ([fdroiddata!44731](https://gitlab.com/fdroid/fdroiddata/-/merge_requests/44731)): их сборщик компилирует ядро из исходников и должен откуда-то узнать версию тулчейна |

Версия Go-тулчейна записана одним значением в `go.version` в корне
репозитория; его читают все шаги `setup-go` в наших `lx-*.yml` и внешние
сборщики (F-Droid). `go.mod` для этого не годится — там `1.24.7`, языковой
floor, и сборка на нём воспроизводит
[SPEC 044](../044-ANDROID_AAR_GO124_QUIC_DEAD/SPEC.md) (мёртвые
quic-go-аутбаунды на вендорских Android-ядрах).

До этой задачи единого значения не было: версия была размазана по нашим
workflow пятью спеллингами, а два файла брали её из `go.mod`. Подробности —
в [IMPLEMENTATION_REPORT](IMPLEMENTATION_REPORT.md).

---

## 1. Зачем

**Внешний потребитель.** F-Droid собирает `libbox.aar` сам, из исходников,
в своей песочнице. Мейнтейнер ([licaon-kter](https://gitlab.com/fdroid/fdroiddata/-/merge_requests/44731))
требует, чтобы версии тулчейнов не дублировались в их метаданных, а
вычитывались из исходного репозитория:

> we pin these to `@stable` here, then in `prebuild:` we do grep/sed/whatever
> to extract exact version from your sourcode… because you should pin these
> libs versions in YOUR source code anyway

Сейчас вычитывать нечего: `go.mod` даёт `1.24.7` — ровно ту версию, на
которой SPEC 044 доказал отказ hysteria2/tuic/masque-h3 на CPH2411.

**Внутренняя причина.** Пин в семи местах с четырьмя разными спеллингами
переживёт не всякую правку CI. `1.25.x` и `^1.25.4` вдобавок плавающие:
воспроизвести вчерашнюю сборку по коммиту нельзя.

## 2. Что сделать

### 2.1. Файл

`go.version` в корне репозитория, одна строка:

```
go1.25.12
```

Формат — как у тегов `golang/go` (`go1.25.12`, с префиксом `go`), чтобы
значение годилось и для `git checkout` в клоне тулчейна, и для
`actions/setup-go` после отрезания префикса.

Почему 1.25.12: старший патч ветки 1.25 на момент написания; именно он
стоит в `build.yml`, `linux.yml`, `docker.yml`.

### 2.2. CI читает файл

Во всех джобах, где сейчас `go-version:` задан литералом, заменить на чтение
из файла. Шаблон:

```yaml
- name: Go version pin
  id: go_pin
  run: echo "version=$(tr -d '[:space:]' < go.version | sed 's/^go//')" >> "$GITHUB_OUTPUT"

- uses: actions/setup-go@v5
  with:
    go-version: ${{ steps.go_pin.outputs.version }}
```

Затронутые файлы (по `grep -rn "go-version:" .github/workflows/`):
`build.yml`, `linux.yml`, `docker.yml`, `lint.yml` и остальные с
литеральным пином.

⚠️ **`go-version-file: go.mod` не использовать** — это и есть регрессия
SPEC 044.

### 2.3. Что НЕ трогать

- `go.mod` остаётся `go 1.24.7` — это языковой floor, а не тулчейн.
  Поднимать его — отдельное решение с оглядкой на upstream-parity.
- Матричные джобы с `${{ matrix.go }}` — там версия задаётся осознанно.
- `badlinkname` и его требование Go 1.24.x
  ([FEATURE 001](../../FEATURES/001-BUILD_CI_CD/FEATURE.md)) — ограничение
  desktop-сборки, к Android-AAR не относится: в AAR этот тег идёт с
  `-checklinkname=0`, и SPEC 044 device-verified именно на 1.25.

## 3. Как проверить

1. `cat go.version` → `go1.25.12`.
2. В логе любого CI-джоба, ставящего Go, — версия `1.25.12`, а не `1.24.x`.
3. `grep -rn "go-version:" .github/workflows/` — литеральных пинов не
   осталось (кроме матричных).
4. Смена значения в `go.version` меняет версию во всех джобах разом.

## 4. Кто это потребляет снаружи

После среза тега метаданные F-Droid будут делать примерно так:

```yaml
- export GO_VERSION=$(tr -d '[:space:]' < go.version)
- '[[ $GO_VERSION ]]'
- git -C $$go$$ checkout -q $GO_VERSION
```

То есть значение обязано совпадать с именем тега в `golang/go` — отсюда
префикс `go` в формате.

## 5. Definition of Done

- [x] `go.version` в корне, содержимое `go1.25.12`
- [x] Все не-матричные `go-version:` в workflows читают этот файл
- [x] `go.mod` не изменён
- [x] CI зелёный, в логе setup-go видна 1.25.12
- [x] Значение попадает в следующий релизный тег (нужно LxBox для F-Droid)
