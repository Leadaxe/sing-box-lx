# lx release runbook

Процедура выпуска lx-релиза/пререлиза. Главное правило вынесено первым: **перед любым тегом
проверь, не ушёл ли upstream вперёд, и обычно — возьми его изменения себе.**

Контекст репозитория:

- `upstream` = `https://github.com/SagerNet/sing-box.git`; отслеживаем ветку **`upstream/testing`**.
- Наша рабочая ветка — **`lx`** (она же default на GitHub); интеграция upstream идёт **ручным
  `git merge upstream/testing`** (НЕ rebase; см. `wg-1.14-migration` в памяти и
  [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md)).
  `lx-rebase.yml` из [BUILD_CI_CD](../SPECS/FEATURES/001-BUILD_CI_CD/FEATURE.md) описывает старый
  авто-rebase на стабильные upstream-теги; он никогда не форс-пушит `lx` — только открывает PR/issue.
- **`lx-1.14` — историческая ветка миграции на 1.14.** Миграция завершена, ветка слита в `lx`
  (обе указывали на один коммит на `v1.14.0-lx.16`) и оставлена в origin только как якорь.
  Новую работу и релизы вести на `lx`.
- **Prerelease определяется суффиксом тега**, а не веткой: `lx-release.yml` вешает `--prerelease`
  на `-rc.N` / `-alpha.N` / `-beta.N`. Тег без суффикса (`v1.14.0-lx.16`) публикуется как stable
  «Latest». Старое ограничение «пока upstream в alpha — только rc-линия» снято: upstream перешёл
  на beta, форк снова режет стабильные теги.
- Релиз-ноты генерятся автоматически из `#### v<tag-без-v>`-секции [docs-lx/lx-changelog.md](lx-changelog.md)
  (`lx-release.yml`); поэтому changelog должен быть верным ДО тега.

---

## 0. Pre-release gate — НЕ резать тег, пока все пункты не зелёные

```
[ ] 1. upstream-дрейф проверен (раздел 1)
[ ] 2. если upstream впереди — взят/смержен/собран (раздел 2), ИЛИ сознательно отложен с причиной
[ ] 3. go build ./... и build -tags with_lx_command — зелёные
[ ] 4. gofmt -l по lx-owned файлам — пусто
[ ] 5. docs-lx/lx-changelog.md содержит секцию #### v<этот-тег> с верным содержимым
       (проверить ИМЕННО тем же awk, что в CI — см. раздел 3)
[ ] 6. ветка lx запушена в origin ДО тега (push branch → push tag)
```

---

## 1. Проверь, не ушёл ли upstream вперёд (ОБЯЗАТЕЛЬНО перед каждым релизом)

```bash
git fetch upstream --tags
# ЧЕСТНАЯ проверка: если merge-base == tip upstream/testing, дрейфа нет
git merge-base lx upstream/testing
git rev-parse upstream/testing
# что именно приехало (пусто = ничего):
git --no-pager log --oneline $(git merge-base lx upstream/testing)..upstream/testing
# не появился ли новый upstream-тег новее нашей базы:
git tag -l 'v1.14.0*' --sort=-creatordate | grep -iv lx | head
```

⚠️ **Не мерить дрейф от `<наш-merge-коммит>^2`.** Ветка `upstream/testing` регулярно
**force-push'ится**, поэтому второй родитель нашего merge-коммита указывает на переписанную
историю, и `git log/diff <merge>^2..upstream/testing` показывает мусор — в том числе НАШУ
дельту в обратную сторону («upstream удалил `option/platform.go`»), которой на деле нет.
Так на релизе `v1.14.0-lx.16` (2026-07-26) картина выглядела как «8 новых коммитов upstream»,
хотя все 8 уже были влиты и merge-base совпадал с tip. Единственный надёжный сигнал — merge-base;
сверять коммиты по subject (`git log --format=%s`), а не по хешам — после force-push хеши другие.

- **merge-base == tip / 0 коммитов впереди** → upstream синхронен, переходи к сборке/тегу (раздел 3).
- **>0 коммитов** → по умолчанию **взять и смержить** (раздел 2). Откладывать слияние можно только
  сознательно и с записанной причиной (например, upstream-коммит ломает наш слой и нужен отдельный
  разбор) — тогда зафиксируй это в changelog-записи релиза, чтобы было видно, что дрейф известен.

Почему «обычно брать»: чем дольше копится дрейф, тем дороже и рискованнее слияние (конфликты в
`.pb.go`, submodule wireguard-go, изменения интерфейсов adapter/*). Маленькие частые merge'и
дешевле одного большого перед релизом.

## 2. Возьми изменения upstream себе (merge, затем сборка) — и ТОЛЬКО потом релиз

```bash
git checkout lx
git merge upstream/testing            # ручной merge, НЕ rebase
```

При конфликтах — зоны, которые трогаем чаще всего (держать lx-семантику, принимать upstream-логику):

- `daemon/*.pb.go` / `*.proto` — наши поля аддитивны (`detourList=23`, DnsQueryEvent 1..12). Если
  upstream регенерил дескрипторы, перегенери через `make -f Makefile.lx lx-proto` и заново наложи
  lx-поля, либо вручную: см. `lx-commandclient-extensions` в памяти (pinned protoc-toolchain).
- `submodules/wireguard-go` — это наш форк-сабмодуль; upstream-bump не принимать вслепую,
  см. `wg-1.14-migration`.
- `cmd/internal/build_libbox/main.go` — единственная правка upstream-файла в CI-зоне, по `// lx`-маркеру.
- `box.go`, `dns/client*.go`, `route/route.go`, `common/trafficcontrol/tracker.go` — несут lx-наблюдаемость
  поверх upstream-логики; при конфликте сохранить upstream-поведение резолва/роутинга, наши emit/Detour —
  аддитивны (см. аудит чистоты, коммит 3505beb6).

После merge — обязательно собрать и прогнать оба пути перед тегом:

```bash
go build ./...
go build -tags with_lx_command ./...
gofmt -l box.go common/dnstrack/manager.go dns/client.go dns/client_log.go \
        dns/transport_adapter.go route/route.go common/trafficcontrol/tracker.go \
        daemon/started_service_command_lx.go experimental/libbox/command_client_command_lx.go
make -f Makefile.lx lx-check     # собрать lx-бинарь + check минимального конфига
```

Если merge принёс заметные upstream-изменения — добавь строку про upstream-базу в changelog-секцию
релиза (как `b8ff5c78`: «rc.6 также несёт upstream alpha.35 merge»).

## 3. Обнови changelog, затем режь тег

1. Допиши секцию в [docs-lx/lx-changelog.md](lx-changelog.md). Заголовок **строго** `#### v<tag-без-v>`
   (например `#### v1.14.0-lx.16`) — `lx-release.yml` извлекает ровно эту секцию в release notes
   через `awk`. Неверный/отсутствующий заголовок → пустые/чужие ноты автоматически.
   **Промоут rc-линии в stable — это отдельная секция**, а не переиспользование секции последней rc:
   для тега `v1.14.0-lx.16` нужна секция `#### v1.14.0-lx.16`, сводящая содержимое rc.1–rc.N.
   Проверить извлечение ДО тега тем же кодом, что в CI:

   ```bash
   VERSION=1.14.0-lx.16   # тег без ведущей v
   awk -v v="#### v${VERSION}" '$0==v {f=1; next} /^#### / {f=0} f' docs-lx/lx-changelog.md
   ```

   Пустой вывод → ноты уедут заглушкой. Захват соседних `####` → в ноты попадут чужие секции.
2. Commit ветки → **push ветку в origin ДО тега** (иначе тег окажется впереди ветки — было на rc.1;
   см. `git-push-auth-gh-token` в памяти про inline-токен).
3. Создать и запушить тег. CI `lx-release.yml` соберёт desktop-архивы + AAR
   (`libbox` + `libbox-legacy`) и опубликует релиз с автогенеренными нотами; **prerelease или stable
   решает суффикс тега** (`-rc./-alpha./-beta.` → prerelease, без суффикса → stable «Latest»).
4. Проверить прогон: `gh run list --workflow lx-release.yml`, дождаться `completed success`, убедиться,
   что в релизе есть AAR и ноты совпадают с changelog-секцией.

## 4. Пострелизный sanity

- `gh release view v<tag>` — assets на месте, флаг prerelease соответствует суффиксу тега, ноты верные.
  Для stable дополнительно: `gh api repos/Leadaxe/sing-box-lx/releases/latest -q .tag_name` должен
  вернуть этот тег.
- **Ссылки в нотах ведут на ветку `lx`** (`/blob/lx/...` захардкожено в `lx-release.yml`). Если `lx`
  отстала от релизного коммита, ссылки на новые файлы отдают **404** — на `v1.14.0-lx.16` ветка
  отставала на 28 коммитов и ссылки на `SPECS/FEATURES/013-DNS_GROUP` были бы битыми. Проверить:

  ```bash
  grep -ohE 'https://github.com/Leadaxe/sing-box-lx/blob/[^)"]+' <(gh release view v<tag> --json body -q .body) \
    | sort -u | while read -r u; do echo "$(curl -s -o /dev/null -w '%{http_code}' -L "$u")  $u"; done
  ```

  Теперь, когда работа идёт прямо на `lx`, расхождение возможно только если тег срезан с другой ветки.
- Скачать один архив и сверить сумму с `SHA256SUMS`, запустить бинарь: `sing-box version` должен
  показать версию тега, тот же revision и полный набор тегов сборки (в desktop-архивах
  **`with_clash_api` обязан присутствовать** — его дропает только AAR; см. `desktop-keeps-clash-api-aar-drops`).
- Для observability/attribution-фич (DNS-стрим, Detour) — **device-verify обязателен**: сборка и
  proto round-trip НЕ ловят registry-key / fast-path-hijack / ctx-timing баги (история §180/§180-2).
  См. `lx-spec018-dns-query-stream` в памяти.

---

### Одной строкой

`fetch upstream → merge-base сверить с tip → если впереди, merge себе → build+gofmt+lx-check →
changelog (+проверить awk) → push ветку lx → тег → сверить ноты/ассеты/суммы`.
Дрейф проверяется **каждый** раз и **только по merge-base**; слияние — поведение по умолчанию,
пропуск — осознанное исключение с записанной причиной.
