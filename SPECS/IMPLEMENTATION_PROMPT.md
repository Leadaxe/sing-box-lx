# IMPLEMENTATION_PROMPT — sing-box-lx

Правила реализации задач. Применять вместе с **CONSTITUTION.md** и SPEC/PLAN/TASKS конкретной задачи.

---

## 1. Философия

Ты строишь не «ещё один sing-box», а **дельту над upstream**. Хорошая дельта — та, которую через месяц можно за 10 минут переставить на новый тег. Поэтому: меньше тронутых upstream-строк > красивая абстракция; новый файл > правка существующего; build-tag > безусловный код.

---

## 2. Definition of Done

Задача закрыта, когда:

1. **Сборка без тегов** = поведение upstream:
   `go build ./...` — ок, фичи отсутствуют.
2. **Сборка с тегами** включает фичи:
   `make -f Makefile.lx lx-build` — единственный источник истины по набору тегов
   (`make -f Makefile.lx lx-print-tags`) и обязательному `-checklinkname=0` в ldflags.
   ⚠️ Полный набор (с `badlinkname`) линкуется только на **go1.24.x** (см. `go.mod`);
   на go1.25-хосте проверять сборку набором тегов минус `badlinkname`/`with_naive_outbound`.
3. `go vet ./...` и `go test ./...` зелёные (для затронутых пакетов как минимум).
4. `./sing-box check -c <тест-конфиг>` принимает конфиг с новой фичей (xhttp-транспорт / awg-endpoint).
5. Все правки upstream-файлов обёрнуты `// lx:begin <feat>` / `// lx:end <feat>` и лежат в отдельных коммитах.
6. **TASKS.md** отражает факт (`[x]`), заполнен **IMPLEMENTATION_REPORT.md**.
7. Статус задачи (шапка SPEC.md + Roadmap в SPECS/README.md) выставлен в **C** при приёмке.

---

## 3. Git-дисциплина

### 3.1 Ветки
- `lx` — рабочая и релизная ветка (default). Синхронизация с upstream — ручной **merge** `upstream/testing` (см. §3.3); `lx` никогда не форс-пушится.
- Фичевые ветки от `lx`: `lx-specNNN-<кратко>` (примеры из истории: `lx-spec020-idle-suspend`, `lx-spec022-audit-fixes`, `lx-xhttp-streamone`) — вливаются в `lx` обычным способом.

### 3.2 Коммиты
- Формат: `lx(<feat>): <что>` — напр. `lx(xhttp): add v2rayxhttp client package`, `lx(awg): extend wireguard endpoint with junk/header opts`.
- **Атомарность по зонам:** правки upstream-файлов (диспетчер, опции, константы, go.mod) — **отдельными** коммитами от нового кода. Так при ребейз-конфликте видно ровно, что и где.
- Порядок коммитов в дельте (от «нулевого конфликта» к «точкам касания»):
  1. новые пакеты/файлы;
  2. `// lx:` правки upstream-файлов;
  3. `go.mod`/`go.sum`/submodule;
  4. build-tag проводка (`include/*`), CI.

### 3.3 Ритуал синка с upstream и релиза
Полный актуальный ритуал — **[docs-lx/lx-release-runbook.md](../docs-lx/lx-release-runbook.md)**. Скелет:
```sh
git fetch upstream
git merge-base lx upstream/testing   # дрейфа нет, если == tip upstream/testing
git checkout lx
git merge upstream/testing           # ручной merge; НЕ rebase, НЕ force-push
# конфликты ожидаемы ТОЛЬКО в // lx: зонах upstream-файлов, go.mod и гитлинках сабмодулей
# пересобрать (make -f Makefile.lx lx-build), gofmt lx-файлов, lx-check; прогнать DoD
git push origin lx                   # ветку ДО тега
git tag v<X.Y.Z>-lx.<N> && git push origin v<X.Y.Z>-lx.<N>   # тег из СУПЕРПРОЕКТА, не из каталога сабмодуля
```
Если merge дал конфликт вне `// lx:` зоны — значит изоляция нарушена, это дефект дельты, а не upstream. Секция `#### v<X.Y.Z>-lx.<N>` в `docs-lx/lx-changelog.md` должна существовать **до** пуша тега — из неё `lx-release.yml` собирает релиз-ноты.

---

## 4. Консоль и осторожность

- Не запускать интерактивные git-флаги (`-i`).
- Ветку `lx` не форс-пушить; `--force-with-lease` — только на фичевых ветках.
- **Оба форк-сабмодуля** (`submodules/wireguard-go` = Leadaxe/wireguard-go-awg2-lx, `submodules/sing-tun` = Leadaxe/sing-tun-lx) обновлять осознанно: фиксировать конкретный коммит, встречные upstream-бампы гитлинков на мерже не принимать вслепую. Коммит сабмодуля пушить **до** суперпроекта (иначе CI падает «not our ref»).

---

## 5. Контракт выхода (что вернуть после реализации)

1. Список изменённых/новых файлов с разбивкой **new pkg / `// lx:` upstream / deps / wiring**.
2. Команды сборки (с тегами) и результат DoD-чеклиста.
3. Точная зона возможных конфликтов при следующем ребейзе (какие upstream-файлы тронуты).
4. Что осталось вне скоупа (server-сторона и т.п.).
