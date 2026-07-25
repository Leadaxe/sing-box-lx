# lx release runbook

Процедура выпуска lx-релиза/пререлиза. Главное правило вынесено первым: **перед любым тегом
проверь, не ушёл ли upstream вперёд, и обычно — возьми его изменения себе.**

Контекст репозитория (актуально на 1.14-ветке):

- `upstream` = `https://github.com/SagerNet/sing-box.git`; отслеживаем ветку **`upstream/testing`**.
- Наша рабочая ветка — **`lx-1.14`**; интеграция upstream идёт **ручным `git merge upstream/testing`**
  (НЕ rebase: пока upstream на `v1.14.*-alpha`, rc-линия `vX-lx.1-rc.N` — это формат поставки; см.
  `wg-1.14-migration` в памяти и [BUILD_CI_CD](../SPECS/FEATURES/BUILD_CI_CD/FEATURE.md)).
  `lx-rebase.yml` из [BUILD_CI_CD](../SPECS/FEATURES/BUILD_CI_CD/FEATURE.md) описывает старый авто-rebase на **стабильные** теги — он неактуален,
  пока стабильного `v1.14.0` нет.
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
[ ] 6. ветка lx-1.14 запушена в origin ДО тега (push branch → push tag)
```

---

## 1. Проверь, не ушёл ли upstream вперёд (ОБЯЗАТЕЛЬНО перед каждым релизом)

```bash
git fetch upstream --tags
# на сколько upstream/testing впереди нашей последней интеграции:
git rev-list --count $(git merge-base lx-1.14 upstream/testing)..upstream/testing
# что именно там приехало:
git --no-pager log --oneline $(git merge-base lx-1.14 upstream/testing)..upstream/testing
# не появился ли новый upstream-тег новее нашей базы:
git tag -l 'v1.14.0*' --sort=-creatordate | grep -iv lx | head
```

- **0 коммитов впереди** → upstream синхронен, переходи к сборке/тегу (раздел 3).
- **>0 коммитов** → по умолчанию **взять и смержить** (раздел 2). Откладывать слияние можно только
  сознательно и с записанной причиной (например, upstream-коммит ломает наш слой и нужен отдельный
  разбор) — тогда зафиксируй это в changelog-записи релиза, чтобы было видно, что дрейф известен.

Почему «обычно брать»: чем дольше копится дрейф, тем дороже и рискованнее слияние (конфликты в
`.pb.go`, submodule wireguard-go, изменения интерфейсов adapter/*). Маленькие частые merge'и
дешевле одного большого перед релизом.

## 2. Возьми изменения upstream себе (merge, затем сборка) — и ТОЛЬКО потом релиз

```bash
git checkout lx-1.14
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
   (например `#### v1.14.0-lx.1-rc.11`) — `lx-release.yml` извлекает ровно эту секцию в release notes
   через `awk`. Неверный/отсутствующий заголовок → пустые/чужие ноты автоматически.
2. Commit ветки → **push ветку в origin ДО тега** (иначе тег окажется впереди ветки — было на rc.1;
   см. `git-push-auth-gh-token` в памяти про inline-токен).
3. Создать и запушить тег `vX-lx.1-rc.N`. CI `lx-release.yml` соберёт desktop-архивы + AAR
   (`libbox` + `libbox-legacy`) и опубликует prerelease с автогенеренными нотами.
4. Проверить прогон: `gh run list --workflow lx-release.yml`, дождаться `completed success`, убедиться,
   что в релизе есть AAR и ноты совпадают с changelog-секцией.

## 4. Пострелизный sanity (по желанию)

- `gh release view v<tag>` — assets на месте, prerelease=true, ноты верные.
- Для observability/attribution-фич (DNS-стрим, Detour) — **device-verify обязателен**: сборка и
  proto round-trip НЕ ловят registry-key / fast-path-hijack / ctx-timing баги (история §180/§180-2).
  См. `lx-spec018-dns-query-stream` в памяти.

---

### Одной строкой

`fetch upstream → если впереди, merge себе → build+gofmt+lx-check → changelog → push ветку → тег`.
Дрейф проверяется **каждый** раз; слияние — поведение по умолчанию, пропуск — осознанное исключение с записанной причиной.
