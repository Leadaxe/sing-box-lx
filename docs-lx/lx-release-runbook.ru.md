# Раннбук релиза lx

> 🌐 English version: **[lx-release-runbook.md](lx-release-runbook.md)**.

Процедура выпуска lx-релиза и пререлиза. Первое правило: перед любым тегом проверь, не ушёл ли
upstream вперёд, и по умолчанию возьми его изменения себе.

Контекст репозитория:

- `upstream` = `https://github.com/SagerNet/sing-box.git`; отслеживаем ветку `upstream/stable`.
  Дрейф меряем только против `upstream/stable`. `upstream/testing` — линия разработки апстрима:
  следующая минорная версия в alpha. Она всегда впереди на десятки или сотни коммитов (Go-бампы,
  незрелые рефакторинги) и перестраивается поверх stable force-push'ем. Дрейф по ней никогда не
  покажет ноль, и красный гейт по testing заблокирует релиз навсегда. Ноль по `upstream/stable` =
  дрейфа нет, тег резать можно.
  Плашка на странице форка «N commits behind SagerNet/sing-box:testing» считает от testing, ветки
  апстрима по умолчанию; это не дрейф. Кнопку **Sync fork** не нажимать: она вмерживает testing
  в `lx`, а при конфликтах предлагает «Discard commits» — сброс `lx` к апстриму.
- Наша рабочая ветка — `lx`, она же default на GitHub. Upstream интегрируется ручным
  `git merge upstream/stable`, не rebase; см. `wg-1.14-migration` в памяти и
  [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md).
  `lx-rebase.yml` из той же фичи описывает старый авто-rebase на стабильные upstream-теги. Он
  никогда не форс-пушит `lx`, только открывает PR или issue.
- `lx-1.14` — историческая ветка миграции на 1.14. Миграция завершена, ветка слита в `lx`
  (обе указывали на один коммит на `v1.14.0-lx.16`) и оставлена в origin только как якорь.
  Новая работа и релизы идут на `lx`.
- Prerelease определяется суффиксом тега, а не веткой: `lx-release.yml` вешает `--prerelease`
  на `-rc.N` / `-alpha.N` / `-beta.N`. Тег без суффикса публикуется как «Latest». Актуальный
  список тегов — `git tag -l 'v*lx*' --sort=-creatordate | head`; версию здесь не называем, она
  устаревает быстрее всего остального. Форк режет стабильные теги: ограничение «пока upstream
  в alpha — только rc-линия» снято после перехода upstream на beta.
- Релиз-ноты собирает `lx-release.yml` из двух источников, по приоритету:
  1. **`docs-lx/releases/v<tag-без-v>.md`** — рукописные двуязычные ноты в формате LxBox:
     TL;DR EN+RU, блоки 🇬🇧/🇷🇺 с секциями 🆕/🔧/🐛/🧰. Правила и скелет —
     [docs-lx/releases/TEMPLATE.md](releases/TEMPLATE.md). Для stable-тега файл обязателен:
     без него CI выдаст warning, и в релиз уйдёт сырой changelog.
  2. Фолбэк для rc/alpha/beta: `#### v<tag-без-v>`-секция [docs-lx/lx-changelog.md](lx-changelog.md).
  Служебный каркас (upstream-база, свёрнутые `<details>` про бинарники/AAR/теги, ссылка на
  предыдущий релиз) генерируется автоматически, в файлах его не писать. Changelog всё равно
  ведётся на каждый тег как инженерный лог и должен быть верным до тега.

## Оглавление

- [0. Pre-release gate — НЕ резать тег, пока все пункты не зелёные](#0-pre-release-gate--не-резать-тег-пока-все-пункты-не-зелёные)
- [1. Сначала — форк-сабмодули, потом ядро (ПОРЯДОК ОБЯЗАТЕЛЕН)](#1-сначала--форк-сабмодули-потом-ядро-порядок-обязателен)
  - [1.1 Машинная проверка: вершина форка против `go.mod`](#11-машинная-проверка-вершина-форка-против-gomod)
  - [1.2 Брать ленту ЦЕЛИКОМ, а не выборочные коммиты](#12-брать-ленту-целиком-а-не-выборочные-коммиты)
  - [1.3 Второй класс дрейфа: API, которого нет в базе форка](#13-второй-класс-дрейфа-api-которого-нет-в-базе-форка)
  - [1.4 После закрытия дрейфа — обязательный прогон на устройстве](#14-после-закрытия-дрейфа--обязательный-прогон-на-устройстве)
- [2. Проверь, не ушёл ли upstream вперёд (ОБЯЗАТЕЛЬНО перед каждым релизом)](#2-проверь-не-ушёл-ли-upstream-вперёд-обязательно-перед-каждым-релизом)
- [2a. Сверь версии зависимостей — не только коммиты (ОБЯЗАТЕЛЬНО перед каждым релизом)](#2a-сверь-версии-зависимостей--не-только-коммиты-обязательно-перед-каждым-релизом)
- [2b. Полная релизная сборка без публикации (dry run)](#2b-полная-релизная-сборка-без-публикации-dry-run)
- [3. Возьми изменения upstream себе (merge, затем сборка) — и ТОЛЬКО потом релиз](#3-возьми-изменения-upstream-себе-merge-затем-сборка--и-только-потом-релиз)
- [4. Обнови changelog и релиз-ноты, затем режь тег](#4-обнови-changelog-и-релиз-ноты-затем-режь-тег)
- [5. Пострелизный sanity](#5-пострелизный-sanity)
  - [Одной строкой](#одной-строкой)

---

## 0. Pre-release gate — НЕ резать тег, пока все пункты не зелёные

```
[ ] 1. дрейф в форк-САБМОДУЛЯХ проверен и закрыт ДО мержа ядра (раздел 1)
[ ] 2. upstream-дрейф проверен (раздел 2)
[ ] 2a. версии зависимостей сверены с upstream/stable, а не только коммиты (раздел 2a): тулчейн Go,
        go.mod, CRONET_GO_VERSION, NDK/JDK, protoc-плагины, upstream.version, GitHub Actions;
        каждое расхождение устранено ИЛИ помечено `lx:` с причиной и записано в changelog
[ ] 3. если upstream впереди — взят/смержен/собран (раздел 3), ИЛИ сознательно отложен с причиной
[ ] 4. go build ./... и build -tags with_lx_command — зелёные; полный набор — make -f Makefile.lx lx-build
       (⚠️ SPEC 049: версия тулчейна живёт в ЕДИНСТВЕННОМ файле `go.version` в корне — его читают
        все шаги `setup-go` во всех `lx-*.yml`. НЕ `go-version-file: go.mod`:
        оттуда приедет языковой floor, а не пин тулчейна (когда там стояло 1.24.x, это была регрессия
        SPEC 044 — go1.24-AAR убивает quic-go-аутбаунды на вендорских Android-ядрах, и badtls там
        заглушка). Win7-джоба — свой патченный тулчейн,
        её и апстримный workflow этот пин не трогает. Поднимать минорку пина
        только после прогона AAR на реальном устройстве)
[ ] 4a. если с прошлого релиза менялся тулчейн или версия зависимости из раздела 2a — полная
        релизная сборка без публикации (dry run `lx-release.yml`) зелёная (раздел 2b)
[ ] 5. gofmt -l по lx-owned файлам — пусто
[ ] 6. docs-lx/lx-changelog.md содержит секцию #### v<этот-тег> с верным содержимым
       (проверить ИМЕННО тем же awk, что в CI — см. раздел 4);
       для stable-тега ДОПОЛНИТЕЛЬНО: docs-lx/releases/v<этот-тег>.md написан по
       TEMPLATE.md, вычитан в рендере и очищен от draft-комментария
[ ] 7. ветка lx запушена в origin ДО тега (push branch → push tag)
```

---

## 1. Сначала — форк-сабмодули, потом ядро (ПОРЯДОК ОБЯЗАТЕЛЕН)

Дрейф в дочерних форк-репозиториях разбирается до мержа ядра, а не после.
Наши `replace` в `go.mod` подменяют апстримовые модули на форк-сабмодули
(`wireguard-go`, `sing-tun`, `gvisor`, `utls`). Мерж ядра поднимает версии в `require`,
но `replace` продолжает подставлять нашу ветку. Сборка молча идёт на коде, которого
апстрим уже не ожидает.

### 1.1 Машинная проверка: вершина форка против `go.mod`

Вопрос не в том, есть ли у апстрима новые коммиты, а в том, содержит ли наша ветка
коммит, который требует `go.mod`. Глазами это не проверить: `go build` пройдёт, тесты
пройдут, а в рантайме будет гонка.

```bash
for m in wireguard-go sing-tun gvisor; do
  req=$(grep -E "sagernet/$m v" go.mod | grep -oE "[0-9a-f]{12}$")
  echo "=== $m (go.mod требует: ${req:-снапшот без хеша}) ==="
  [ -z "$req" ] && continue
  git -C submodules/$m fetch sagernet 2>/dev/null
  if ! git -C submodules/$m cat-file -e "$req" 2>/dev/null; then
    echo "  ❌ коммита нет в форке — ДРЕЙФ"; continue
  fi
  if git -C submodules/$m merge-base --is-ancestor "$req" HEAD 2>/dev/null; then
    echo "  ✅ содержится в нашей ветке"
  else
    echo "  ❌ ДРЕЙФ: недостаёт $(git -C submodules/$m rev-list --count HEAD..$req) коммитов"
    git -C submodules/$m log --oneline HEAD..$req | head -10
  fi
done
```

Для `gvisor` версия в `require` без хеша, это снапшот. Строку версии сверяют с датой
снапшота в истории сабмодуля.

Для `utls` пин — тег `metacubex/utls vX.Y.Z` (сейчас `v1.9.0-alpha.1`), тоже без хеша.
Ветка `lx` форка должна содержать этот тег и отличаться от него пятью коммитами:

- cherry-pick refraction 6ebdceb — пресет Chrome 155, SPEC 118;
- три наших вокруг него. Перед ним — только кодпоинты подписей ML-DSA. После — убранный
  тест рукопожатия ML-DSA и `HelloChrome_Auto`, оставленный на Chrome 133; Chrome 155
  включается явно именем `chrome_155`;
- наш поверх всех — новый файл `u_conn_release_lx.go` с `UConn.ReleaseHandshakeState` (SPEC 122).

Firefox 148 с reuse key share и Safari 26.3 (SPEC 086/087) с `v1.9.0-alpha.1` приходят из
самого metacubex.

```bash
req=$(grep -oE 'metacubex/utls v[0-9a-z.-]+' go.mod | head -1 | awk '{print $2}')
git -C submodules/utls fetch metacubex --tags 2>/dev/null
git -C submodules/utls merge-base --is-ancestor "$req" HEAD && echo "✅ lx содержит $req" || echo "❌ ДРЕЙФ: go.mod требует $req"
git -C submodules/utls diff --stat "$req" HEAD   # только файлы Chrome 155 / кодпоинтов ML-DSA / trust_anchors и u_conn_release_lx*.go
```

metacubex пересобирает историю между тегами: `v1.9.0-alpha.1` не потомок `v1.8.7`. Поэтому
`git log "$req..HEAD"` показывает и старую линию форка и для проверки не годится —
сравниваются деревья. Апстримный бамп `metacubex/utls` означает перенос:

1. боковая ветка от нового тега;
2. те же коммиты cherry-pick'ом поверх, без тех, что тег уже несёт;
3. один коммит на `lx` форка с деревом боковой ветки и двумя родителями — старым `lx` и
   боковой веткой.

Ветка `lx` форка не форс-пушится, старые гитлинки остаются достижимыми. metacubex внешние PR
не принимает, синк только своими силами. Условие снятия форка — в SPEC 086/087/118.

### 1.2 Брать ленту ЦЕЛИКОМ, а не выборочные коммиты

⚠️ Не черри-пикать «те коммиты, на которые ругается компилятор». Апстримовая лента
рассчитана на работу целиком. Среди пропущенных коммитов почти наверняка окажутся
исправления гонок и рефакторинг блокировок: ошибок компиляции они не дают, а плавающие
падения в рантайме дают.

Так сломался `v1.14.0-lx.20-rc.5`: из 14 недостающих коммитов `wireguard-go` взяли 3, по
признаку `undefined: device.PeerLookupFunc`. Пропущенными остались
`15b912c device: fix TOCTOU race during session state update` и
`2ad9837 device: refactor container locking for lock-order clarity`.
Получилось состояние, которого у апстрима никогда не было: сборка зелёная, юнит-тесты
зелёные, а на устройстве через раз nil-паника в `udpNat.Start()` при старте
WireGuard-эндпоинта.

⚠️ Fast-forward форка невозможен: в апстримовых ветках нет ни одного нашего lx-коммита,
и обновление снесёт AWG-обфускацию и SPEC 041. Правильный путь — перенести наши патчи
поверх полной апстримовой ленты (re-graft), а не тянуть апстримовые куски на нашу старую
базу.

Ветки форка `wireguard-go` повторяют суперпроект: `lx` — основная линия, `lx-1.15` — линия
1.15; пин сабмодуля всегда на вершине своей ветки. Re-graft даёт историю, не связанную с
`lx`, поэтому он вливается в `lx` слиянием `-s ours --allow-unrelated-histories` со стороны
новой ленты (дерево = новая лента, старые коммиты остаются в истории), а не уходит в новую
ветку. Старые `lx-awg2-v003`/`v005`/`v007` — архив, туда не коммитить.

⚠️ После re-graft бампнуть `upstream.version` (в корне, рядом с `go.version`). Это база
для `LX_VERSION` при локальной сборке `make -f Makefile.lx lx-build`. Релизный CI берёт
версию из имени тега и этот файл не читает. Поэтому забытый бамп в релизе не всплывёт,
сломается только сборка у пользователей по README.

Проверка только ручная: автогейта в CI нет и быть не может, потому что пин не с чем
сверять. Апстрим-версия в дереве не записана: `constant/version.go` = `"unknown"`, версию
ставит ldflags — это наш zero-diff. Апстрим-теги в форке неполные: на `origin` из чистых
апстримовых лежат только старые (≤ `v1.13.11`), а `v1.13.13` и `v1.14.0-beta.*` есть лишь
в тех локальных клонах, где делали `git fetch upstream --tags`. Гейт на тегах пробовали
(коммит `5c621c089`, откачен `d7fa017a8`): локально зелёный, в CI красный. CI доложил
`1.13.11` — ту версию, которую видит пользователь с чистым клоном. Поэтому после re-graft
этот пункт сверяют глазами.

### 1.3 Второй класс дрейфа: API, которого нет в базе форка

Мерж может притащить внешнюю зависимость, которой нужен API из свежего сабмодуля.
Ядро при этом собирается, а AAR — нет, и узнаёшь об этом только от CI.

Случай из SPEC 051: апстрим поднял `tailscale` 1.92 → 1.102, а тот потребовал
`device.PeerLookupFunc`/`NewPeerConfig`/`SetPeerLookupFunc` из свежего `wireguard-go`.
Цепочка: `libbox/native_shell_session.go` → `protocol/tailscale/tailssh` →
`wgengine/wgcfg` → `wireguard-go/device`. Тегами сборки это не обойти: `tailssh`
гейтится по `with_gvisor`, который включён всегда.

### 1.4 После закрытия дрейфа — обязательный прогон на устройстве

Сборка и юнит-тесты не ловят этот класс багов (проверено на rc.5). Если трогали
сабмодуль, до тега нужен живой прогон: туннель поднимается, DNS ходит, URL-тест меряет,
WG/AWG-узлы живы. Прогон повторяют несколько раз подряд, потому что гонки плавают.

## 2. Проверь, не ушёл ли upstream вперёд (ОБЯЗАТЕЛЬНО перед каждым релизом)

Линия отсчёта — `upstream/stable`, и только она. По `upstream/testing` дрейф не мерят: это
ветка разработки апстрима, она всегда впереди и нулём не станет никогда. Замер 2026-08-14:
stable — ahead=0, testing — ahead=233 от своей merge-base; 2026-09-14, после перестройки
testing поверх stable, — 21. Цифра меняется сама по себе и приведена только как порядок
величины. Гейт по testing всегда красный и блокирует релиз без причины.

```bash
git fetch upstream --tags
# Надёжная проверка: если merge-base == tip upstream/stable, дрейфа нет
git merge-base lx upstream/stable
git rev-parse upstream/stable
# что именно приехало (пусто = ничего):
git --no-pager log --oneline $(git merge-base lx upstream/stable)..upstream/stable
# не появился ли новый upstream-тег новее нашей базы:
git tag -l 'v1.14.0*' --sort=-creatordate | grep -iv lx | head
```

⚠️ Не мерить дрейф от `<наш-merge-коммит>^2`. Апстримовые ветки регулярно force-push'атся,
поэтому второй родитель нашего merge-коммита указывает на переписанную историю.
`git log/diff <merge>^2..upstream/stable` показывает мусор, в том числе нашу дельту в обратную
сторону («upstream удалил `option/platform.go`»), которой нет. На релизе `v1.14.0-lx.16`
(2026-07-26) картина выглядела как «8 новых коммитов upstream», хотя все 8 уже были влиты и
merge-base совпадал с tip. Единственный надёжный сигнал — merge-base. Коммиты сверяют по
subject (`git log --format=%s`), а не по хешам: после force-push хеши другие.

Если merge-base уехала, потому что force-push прошёл уже после вашего мержа, сверяй по темам,
а не по хешам. Так было 2026-07-30: сразу после мержа 235 коммитов дрейф показал «210 впереди».

```bash
comm -23 <(git log --format=%s $(git merge-base lx upstream/stable)..upstream/stable | sort) \
         <(git log --format=%s -260 lx | sort)
```

Что осталось в выводе — то и новое. Тогда из 210 «новых» в выводе осталось 5.
Такой хвост берётся `cherry-pick`, а не вторым мержем: второй мерж заново поднимет уже
разрешённые конфликты (в том случае — 49) против устаревшей базы.

- **merge-base == tip / 0 коммитов впереди** → upstream синхронен, переходи к сборке и тегу (раздел 4).
- **>0 коммитов** → по умолчанию взять и смержить (раздел 3). Откладывать слияние можно только
  сознательно и с записанной причиной: например, upstream-коммит ломает наш слой и нужен отдельный
  разбор. Тогда запиши это в changelog-секцию релиза, чтобы дрейф был виден как известный.

Чем дольше копится дрейф, тем дороже и рискованнее слияние: конфликты в `.pb.go`, в
форк-сабмодулях wireguard-go и sing-tun, изменения интерфейсов adapter/*. Поэтому по умолчанию
изменения берут: маленькие частые мержи дешевле одного большого перед релизом.

## 2a. Сверь версии зависимостей — не только коммиты (ОБЯЗАТЕЛЬНО перед каждым релизом)

Нулевой дрейф по merge-base ещё не значит, что мы собираем тем же, чем апстрим. Мерж приносит
апстримные файлы (`build.yml`, `go.mod`, `.github/CRONET_GO_VERSION`, скрипты тулчейна). Наши
пины остаются как были: `go.version`, NDK и JDK в `lx-*.yml`, плагины protoc в `Makefile.lx`.
Расхождение копится молча: после мержа stable 2026-09-05 апстрим собирался на Go 1.26.7, а мы
до 2026-09-14 — на 1.26.6.

Ориентир — `upstream/stable`. Каждое расхождение либо устраняется до тега, либо остаётся
осознанно. Тогда причина пишется `lx:`-комментарием рядом с пином (как у `ndk-version: r28c` в
`lx-release.yml`) и строкой в changelog-секции релиза.

| Зависимость | Наш пин | С чем сверять |
|---|---|---|
| Язык: тулчейн Go | `go.version` | `go-version:` в `upstream/stable:.github/workflows/build.yml`; `VERSION=` в `.github/setup_go_for_windows7.sh` и `setup_go_for_macos1013.sh` — это файлы апстрима, они приезжают мержем и должны совпасть с `go.version` |
| Язык: `go.mod` | директивы `go` / `toolchain` | `upstream/stable:go.mod` — совпадают |
| Go-модули | блок `require` в `go.mod` | `upstream/stable:go.mod` — набор совпадает; `replace` на форк-сабмодули — раздел 1.1 |
| Форк-сабмодули | `submodules/{wireguard-go,sing-tun,gvisor,utls}` | раздел 1.1 |
| naive / cronet-go | `.github/CRONET_GO_VERSION` | тот же файл в `upstream/stable`; после бампа — вручную `lx-musl-toolchain-mirror.yml` (SPEC 023) |
| Android | `ndk-version`, OpenJDK в `lx-*.yml` | `ndk-version` / `java-version` в upstream `build.yml` |
| Кодогенерация | `LX_PROTOC_GEN_GO_VERSION`, `LX_PROTOC_GEN_GO_GRPC_VERSION` в `Makefile.lx` | `google.golang.org/protobuf` / `grpc` в `go.mod` и стиль сгенерированного кода апстрима |
| База версии | `upstream.version` | `git describe --tags --abbrev=0 upstream/stable` |
| GitHub Actions | мажоры `uses:` в `lx-*.yml` | те же actions в upstream-workflow — наши не старше |

```bash
git fetch upstream --tags
S=upstream/stable
modlist() { awk '/^require \($/{r=1;next} r&&/^\)$/{r=0;next} r&&NF>=2&&$1!~/^\/\//{print $1, $2} /^require [^(]/{print $2, $3}'; }
acts() { grep -hoE 'uses: [A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[^ ]+( # v[0-9.]+)?' \
  | sed -E 's/@[0-9a-f]{40} # /@/; s/^uses: //; s/@/ /' | sort -u \
  | awk '{v[$1]=(v[$1]?v[$1]",":"")$2} END{for(k in v)print k, v[k]}' | sort; }
echo "Go:     lx=$(cat go.version)  stable=$(git show $S:.github/workflows/build.yml | grep -oE 'go-version: *[0-9.]+' | awk '{print $2}' | sort -u | xargs)  scripts: $(grep -hoE 'VERSION="[0-9.]+"' .github/setup_go_for_*.sh | sort -u | xargs)"
echo "go.mod: lx=$(grep -E '^(go|toolchain) ' go.mod | xargs)  stable=$(git show $S:go.mod | grep -E '^(go|toolchain) ' | xargs)"
diff <(git show $S:go.mod | modlist | sort) <(modlist < go.mod | sort) && echo "require: == stable"
echo "cronet: lx=$(cat .github/CRONET_GO_VERSION)  stable=$(git show $S:.github/CRONET_GO_VERSION)"
echo "NDK:    lx=$(grep -hoE 'ndk-version: *[a-z0-9]+' .github/workflows/lx-*.yml | awk '{print $2}' | sort -u | xargs)  stable=$(git show $S:.github/workflows/build.yml | grep -oE 'ndk-version: *[a-z0-9]+' | awk '{print $2}' | sort -u | xargs)"
echo "JDK:    lx=$(grep -hoE 'openjdk-[0-9]+' .github/workflows/lx-*.yml | sort -u | xargs)  stable=$(git show $S:.github/workflows/build.yml | grep -oE 'java-version: *[0-9]+' | awk '{print $2}' | sort -u | xargs)"
echo "protoc: $(grep -E '^LX_PROTOC' Makefile.lx | tr -s ' ' | xargs)  go.mod: $(grep -E 'google.golang.org/(protobuf|grpc) ' go.mod | xargs)"
echo "base:   upstream.version=$(cat upstream.version)  stable=$(git describe --tags --abbrev=0 $S)"
join -a2 -e '—' -o 0,1.2,2.2 <(git show $S:.github/workflows/build.yml | acts) <(cat .github/workflows/lx-*.yml | acts) | column -t
# для сведения — актуальные выпуски Go (ориентир всё равно stable):
curl -s 'https://go.dev/dl/?mode=json' | grep -oE '"version": *"go[0-9.]+"' | grep -oE 'go[0-9.]+' | sort -u
```

- Всё совпадает или расхождение уже помечено `lx:` с причиной → дальше.
- Апстрим поднял версию → поднять следом до тега; смена тулчейна или версии зависимости требует
  полной релизной сборки (раздел 2b).
- У Go вышел патч, а stable на нём ещё не стоит → по умолчанию остаёмся со stable, известное
  отставание записываем в changelog-секцию релиза.
- `require` отличается от stable → это либо недомерженный апстрим (раздел 3), либо наша новая
  зависимость — её назвать в changelog.

## 2b. Полная релизная сборка без публикации (dry run)

`lx-ci` на пуше собирает только нативный linux. Ручной запуск `lx-ci` добавляет cross, AAR и
musl, но не Win7, mips и darwin на macOS-раннере. Всю релизную матрицу собирает только
`lx-release.yml`: windows вместе с Win7 на патченном тулчейне, linux mips, darwin, linux-musl,
`libbox` + `libbox-legacy`. Флаг `dry_run` запускает её без публикации:

```bash
gh workflow run lx-release.yml --ref lx -f tag=v<следующая-версия>-dryrun -f dry_run=true
gh run list --workflow lx-release.yml --limit 1      # id прогона
gh run watch <id> --exit-status
```

- Джоба `publish release` пропускается: GitHub Release не создаётся, тег в origin не появляется.
  AAR-джоба ставит тег только локально в раннере, для `git describe`. Артефакты остаются в прогоне.
- `tag` в dry run только прошивает версию в бинарники. Суффикс `-dryrun` нужен, чтобы артефакт
  нельзя было спутать с релизом.
- Dry run обязателен, если с прошлого релиза менялся тулчейн или версия любой зависимости из
  раздела 2a. Иначе первой полной сборкой на новом тулчейне станет сам релиз.
- Проверить, каким тулчейном собрана каждая джоба. Сводный `gh run view <id> --log` отдаёт логи
  не всех джоб, поэтому проверять по одной:

  ```bash
  gh run view <id> --json jobs --jq '.jobs[] | select(.conclusion=="success") | "\(.databaseId) \(.name)"' |
    while read -r jid name; do
      echo "$name: $(gh api repos/Leadaxe/sing-box-lx/actions/jobs/$jid/logs | grep -oE 'Successfully set up Go version [0-9.]+|Environment: go[0-9.]+' | sort -u | xargs)"
    done
  ```

  У `build windows/386` строка пустая: это Win7 на своём патченном тулчейне из кеша. Его версия —
  `VERSION=` в `.github/setup_go_for_windows7.sh` (раздел 2a).
- Убедиться, что ничего не опубликовано: `gh release view v<версия>-dryrun` → `release not found`,
  а `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` возвращает прежний тег.

## 3. Возьми изменения upstream себе (merge, затем сборка) — и ТОЛЬКО потом релиз

```bash
git checkout lx
git merge upstream/stable             # ручной merge, НЕ rebase
```

При конфликтах — зоны, которые трогаем чаще всего. Правило для всех: держать lx-семантику,
принимать upstream-логику.

- `daemon/*.pb.go` / `*.proto` — наши поля аддитивны (`detourList=23`, DnsQueryEvent 1..12). Если
  upstream перегенерировал дескрипторы, перегенерируй через `make -f Makefile.lx lx-proto` и заново
  наложи lx-поля. Либо вручную: см. `lx-commandclient-extensions` в памяти (запиненный protoc-тулчейн).
- `submodules/wireguard-go`, `submodules/sing-tun`, `submodules/gvisor` и `submodules/utls` — наши
  форк-сабмодули. Upstream-бамп не принимать вслепую, в том числе коммит вида «Update sing-tun» и
  бамп `sagernet/gvisor` или `metacubex/utls` в `go.mod`. Такой бамп молча уводит `replace` с форка
  и откатывает наши патчи: обфускацию AWG, SPEC 040 self-heal acceptLoop, SPEC 041 rebind,
  SPEC 048 nil-guard в gvisor `handleConnecting`, SPEC 086/087 Firefox 148 + reuse key share + Safari 26.3 в utls.
  См. `wg-1.14-migration` и синк 2026-08-01 в changelog.
  Откат бесшумный: всё собирается, тесты пакета зелёные, а баг возвращается в поле. Поэтому
  после мержа, тронувшего `go.mod`, сверять `go list -m` по всем четырём:

  ```bash
  go list -m github.com/sagernet/wireguard-go github.com/sagernet/sing-tun github.com/sagernet/gvisor github.com/metacubex/utls
  # каждый должен резолвиться в => ./submodules/<name>
  ```

  Для `utls` страж есть и в тестах: `go test -tags with_utls ./common/tls/` (`TestLxFirefox…`,
  `TestLxRealityFingerprints…`) падает, если `replace` съехал на голый metacubex. Признаки:
  `HelloFirefox_Auto` перестал быть Firefox 148, `HelloSafari_Auto` — Safari 26.3, или гибридный
  key share ушёл из `chrome`/`firefox`/`safari`.

  `submodules/gvisor` ведётся снапшотом пина без истории: полная история апстрима —
  1.45 ГБ на каждый CI-клон. Новый пин вливается новым снапшот-коммитом, патч
  накладывается поверх, red/green-тест едет вместе с ним. Подробности — SPEC 048 §6.
- `cmd/internal/build_libbox/main.go` — единственная правка upstream-файла в CI-зоне, по `// lx`-маркеру.
- `common/dialer/dialer.go` / `default.go` — если апстрим поменял, какие поля dial `NewWithOptions`
  применяет под `detour`, обновить список Д1 SPEC 165 (`box_lx_detour.go`, `ignoredUnderDetour`,
  `readByProtocol`). Устаревший список ошибается только в показе `GetOutboundConfig`, поведение
  остаётся за апстримом.
- `box.go`, `dns/client*.go`, `route/route.go`, `common/trafficcontrol/tracker.go` — несут lx-наблюдаемость
  поверх upstream-логики. При конфликте сохранить upstream-поведение резолва и роутинга; наши
  emit/Detour аддитивны (см. аудит чистоты, коммит 3505beb6).

После мержа обязательно собрать и прогнать оба пути перед тегом:

```bash
go build ./...
go build -tags with_lx_command ./...
gofmt -l box.go common/dnstrack/manager.go dns/client.go dns/client_log.go \
        dns/transport_adapter.go route/route.go common/trafficcontrol/tracker.go \
        daemon/started_service_command_lx.go experimental/libbox/command_client_command_lx.go
make -f Makefile.lx lx-check     # собрать lx-бинарь + check минимального конфига
```

Если мерж принёс заметные upstream-изменения, добавь строку про upstream-базу в changelog-секцию
релиза, как в `b8ff5c78`: «rc.6 также несёт upstream alpha.35 merge».

## 4. Обнови changelog и релиз-ноты, затем режь тег

1. Допиши секцию в [docs-lx/lx-changelog.md](lx-changelog.md). Заголовок строго `#### v<tag-без-v>`,
   например `#### v1.14.0-lx.16`. Для rc/alpha/beta `lx-release.yml` извлекает эту секцию в
   release notes через `awk`, если нет файла из шага 1b. Неверный или отсутствующий заголовок
   молча даёт пустые или чужие ноты.
   Промоут rc-линии в stable — отдельная секция, а не переиспользование секции последней rc:
   для тега `v1.14.0-lx.16` нужна секция `#### v1.14.0-lx.16`, сводящая содержимое rc.1–rc.N.

   1b. Для stable-тега напиши `docs-lx/releases/v<tag-без-v>.md` по правилам и скелету из
   [TEMPLATE.md](releases/TEMPLATE.md): двуязычный TL;DR + блоки 🇬🇧/🇷🇺 с секциями
   🆕/🔧/🐛/🧰. Заголовки пунктов — симптом или выгода глазами пользователя; rc.1–rc.N сводятся
   одной историей на тему. Файл стоит вести черновиком по ходу rc-линии; перед тегом —
   вычитать в рендере и убрать draft-комментарий. Если файл существует, он полностью замещает
   changelog-секцию в теле релиза, для rc-тегов тоже.

   Проверить извлечение фолбэка до тега тем же кодом, что в CI:

   ```bash
   VERSION=1.14.0-lx.16   # тег без ведущей v
   awk -v v="#### v${VERSION}" '$0==v {f=1; next} /^#### / {f=0} f' docs-lx/lx-changelog.md
   ```

   Пустой вывод → ноты уедут заглушкой. Захват соседних `####` → в ноты попадут чужие секции.
2. Закоммить ветку и запушь её в origin до тега. Иначе тег окажется впереди ветки — так было
   на rc.1. Про inline-токен см. `git-push-auth-gh-token` в памяти.
3. Создать и запушить тег. CI `lx-release.yml` соберёт desktop-архивы и AAR
   (`libbox` + `libbox-legacy`) и опубликует релиз с автогенерированными нотами. Prerelease или
   stable решает суффикс тега: `-rc./-alpha./-beta.` → prerelease, без суффикса → stable «Latest».
4. Проверить прогон: `gh run list --workflow lx-release.yml`, дождаться `completed success`. Затем
   убедиться, что в релизе есть AAR, ноты совпадают с источником (release-файл или changelog-секция
   для rc), `<details>`-блоки отрендерились, а футер «Previous release» указывает на верный тег.

## 5. Пострелизный sanity

- `gh release view v<tag>`: assets на месте, флаг prerelease соответствует суффиксу тега, ноты верные.
  Для stable дополнительно: `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` должен
  вернуть этот тег.
- Ссылки в нотах ведут на ветку `lx`: `/blob/lx/...` захардкожено в `lx-release.yml`. Если `lx`
  отстала от релизного коммита, ссылки на новые файлы отдают 404. На `v1.14.0-lx.16` ветка
  отставала на 28 коммитов, и ссылки на `SPECS/FEATURES/013-DNS_GROUP` были бы битыми. Проверить:

  ```bash
  grep -ohE 'https://github.com/Leadaxe/sing-box-lx/blob/[^)"]+' <(gh release view v<tag> --json body -q .body) \
    | sort -u | while read -r u; do echo "$(curl -s -o /dev/null -w '%{http_code}' -L "$u")  $u"; done
  ```

  Работа идёт прямо на `lx`, поэтому расхождение возможно, только если тег срезан с другой ветки.
- Скачать один архив, сверить checksum с `SHA256SUMS` и запустить бинарь. `sing-box version` должен
  показать версию тега, тот же revision и полный набор тегов сборки. В desktop-архивах
  `with_clash_api` обязан присутствовать, его дропает только AAR; см. `desktop-keeps-clash-api-aar-drops`.
- Для фич наблюдаемости и атрибуции (DNS-стрим, Detour) проверка на устройстве обязательна.
  Сборка и proto round-trip не ловят баги registry-key, fast-path-hijack и ctx-timing
  (история §180/§180-2). См. `lx-spec018-dns-query-stream` в памяти.

---

### Одной строкой

`fetch upstream → merge-base сверить с tip → версии зависимостей сверить со stable → если впереди,
merge себе → build+gofmt+lx-check → сменился тулчейн или зависимость — dry run lx-release →
changelog (+проверить awk; для stable ещё docs-lx/releases/v<тег>.md) → push ветку lx → тег →
сверить ноты/ассеты/суммы`.
Дрейф и версии зависимостей проверяются каждый раз, дрейф — только по merge-base. Слияние
и подъём версий вслед за stable — поведение по умолчанию; пропуск — осознанное исключение с
записанной причиной.
