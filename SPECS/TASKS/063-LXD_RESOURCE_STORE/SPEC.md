# SPEC: 063 — LXD_RESOURCE_STORE

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — вторая полезная нагрузка admin-плоскости: REST-CRUD над файловыми ресурсами (`.srs` rule-set, geo-базы), на которые ссылается конфиг |
| Статус | C (complete) — в дереве, юниты зелёные (`go test -tags with_lxd ./lxd/`), бинарник `make -f Makefile.lx lx-build` собран, сквозное демо на macOS проведено живьём (реальный `.srs` → PUT → apply `type:local` → 409-гуард → освобождение → delete → санитизация) |
| Ветка | `lx` |
| Base | `25f82921a` |
| Связанные | [SPEC 056](../056-LXD_APPLY_ROLLBACK/SPEC.md) (apply/rollback/store), [SPEC 057](../057-LXD_MTLS_SERVICE/SPEC.md) (mTLS-пин, операторские routes), [FEATURE 014](../../FEATURES/014-LXD_DAEMON/FEATURE.md) |

**Touches:** пакет `lxd/` — новый файл `resources.go` (стор ресурсов) +
маршруты в [lxd/admin.go](../../../lxd/admin.go); переиспользует `store.writeAtomic`
из [lxd/store.go](../../../lxd/store.go) и `contentSHA` из
[lxd/apply.go](../../../lxd/apply.go). Гуард ссылок читает `activeContent` и
last-good через `controller`. Апстримные файлы — не трогаются.

## Why

Admin-плоскость сейчас знает ровно одну полезную нагрузку — тело конфига
(`POST /admin/apply`, одна JSON-строка). Но конфиг сплошь и рядом ссылается на
внешние файлы: скомпилированные rule-set `.srs` (`type: local`, `format:
binary`), geo-базы. Сегодня их некуда положить через демон — только мимо
(scp/rsync прямо в `state_dir`), а это ломает модель «управление настройкой
снаружи через один канал».

`type: remote` (ядро само тянет по URL) не подходит: источник правды по
ресурсам — **оператор снаружи**, а не публичный HTTP-хост. Прогонять свои `.srs`
через промежуточный веб-сервер, чтобы ядро их оттуда скачало, — крюк, а не
управление.

Нужен второй канал доставки: залить ресурс по имени, спросить «что лежит и с
каким хешом», получить путь для конфига. Хеш обязателен — по нему клиент делает
diff перед заливкой (свежее у меня или на демоне) и не гоняет байты зря.

## Design

**Адресация по имени, хеш — метадата версии.** Ресурс кладётся под стабильным
именем: путь = `<state_dir>/resources/<name>`, всегда один. В конфиге оператор
раз прописывает `path`, дальше только перезаливает содержимое под тем же именем.
`sha256` возвращается в каждом ответе — клиент сравнивает локально и льёт только
изменённое. (Модель B по обсуждению владельца: named снаружи, не
content-addressed.)

**`path` в ответе — абсолютный.** Каждый ответ, где есть `path`, отдаёт
**абсолютный** путь, равный `<info.state_dir>/resources/<name>`, где
`info.state_dir` — тот самый абсолютный `controller.infoStateDir` (уже прогнан
через `filepath.Abs` в [daemon.go](../../../lxd/daemon.go)), что отдаёт
`GET /admin/info`. Клиент копирует его в конфиг как есть и не собирает путь сам —
тот же принцип «clients use it instead of hard-coding the daemon's paths», что
уже заложен в `handleInfo`. Абсолютность обязательна: `type: local, path:`
резолвит относительный путь от cwd процесса ядра, и относительный `path` сломал
бы конфиг при другом cwd. Решение владельца: только абсолютный.

**Эндпоинты** (та же аутентификация, что у остальных admin-routes: mTLS-пин, либо
Bearer в dev/loopback — регистрируются на основном `mux`, НЕ на операторском
loopback-only; лаунчер обязан доставать до них по сети):

| Эндпоинт | Семантика |
|---|---|
| `GET /admin/resources` | список: `[{name, sha256, size, path}]` — клиент забирает разом, дифает хеши |
| `PUT /admin/resources/{name}` | залить/перезаписать; тело = сырые байты; ответ `{name, sha256, size, path}`; **409** если на `name` ссылается активный или last-good конфиг (гуард B2) |
| `GET /admin/resources/{name}` | метадата одного имени `{name, sha256, size, path}`; 404 если нет — «дай хеш по имени» |
| `GET /admin/resources/{name}/content` | скачать сырые байты (проверка/бэкап); `Content-Type: application/octet-stream`; 404 если нет |
| `DELETE /admin/resources/{name}` | удалить; **409** если на `name` ссылается активный или last-good конфиг; 404 если нет |

**Клиентский цикл** (то, ради чего фича):

```
GET /admin/resources          → список с хешами
  ▼ (клиент сравнивает локально)
PUT /admin/resources/{name}   → только для новых/изменённых
  ▼
POST /admin/apply             → конфиг, где path ссылается на <state_dir>/resources/<name>
```

**Гуард ссылок (B2) — целостность вместо атомарности.** Named-стор мутабелен:
перезапись имени затирает прежнюю версию. Если бы `PUT`/`DELETE` разрешались для
имени, на которое ссылается активный или last-good конфиг, rollback ядра стал бы
дырявым — текст конфига откатился бы, а `.srs` под именем уже другой. Поэтому
`PUT` и `DELETE` для «занятого» имени отбиваются **409**. Естественный порядок
операций от этого не страдает: ресурс сначала льют, потом на него ссылаются;
чтобы заменить занятый ресурс — сперва `apply` конфига без этой ссылки, затем
трогать файл.

- «Занятость» = подстрочное вхождение `<state_dir>/resources/<name>` (или просто
  `/resources/<name>` — окончательная форма проверки в реализации) в
  `controller.activeContent` **или** в last-good из `store.LoadLastGood()`.
  Дешёвая текстовая проверка; она умышленно консервативна (лучше ложно-занят и
  409, чем ложно-свободен и дырявый rollback).
- Проверка сериализуется тем же `applyAccess`-мьютексом, что и apply: гонка
  «apply ссылается ↔ DELETE удаляет» исключена.

**Стор.** Новый `resourceStore` в `<state_dir>/resources/`, `0o700`. Запись —
через существующий `store.writeAtomic` (tmp + fsync + rename): перезапись
атомарна, крэш не оставляет рваной версии. `sha256` — через `contentSHA`.

**Имена — санитизация.** `{name}` обязан быть безопасным базовым именем: без
разделителей пути, без `..`, без ведущей точки, непустой, разумный предел длины.
Любой обход (`../`, абсолютный путь) — **400**, файл наружу `resources/` не
пишется никогда.

**Лимит тела** — как у apply (`applyBodyLimit`, 32 MiB) или отдельная константа;
`.srs`/geo-базы бывают крупнее конфига — прикинуть по реальным geoip/geosite.

## Out of scope

- Версионирование ресурсов / теневые хеши / честный rollback самих `.srs`
  (вариант B3) — отклонено владельцем в пользу B2. Rollback ресурсов — забота
  оператора (перезалить прежний `.srs`).
- Автоматический GC осиротевших ресурсов — удаление только явным `DELETE`.
- Загрузка ресурсов внутри самого `apply` одним bundle-запросом — раздельные
  каналы (`PUT` ресурсов, затем `apply` конфига).
- `type: remote` менеджмент — ортогонально, ядро тянет само.

## Acceptance

- [x] `PUT` нового имени → 200 `{name, sha256, size, path}`; файл лежит в
  `<state_dir>/resources/<name>`, sha256 совпадает с телом.
  (`TestResourcePutThenStatThenContent`, `TestResourceStorePutStatReadDelete`)
- [x] `path` во всех ответах абсолютный и равен `<info.state_dir>/resources/<name>`
  (совпадает с `GET /admin/info` → `state_dir`).
  (`TestResourcePutThenStatThenContent`, `TestResourceListEndpoint`)
- [x] `GET /admin/resources` перечисляет залитое с верными хешами/размерами.
  (`TestResourceListEndpoint`, `TestResourceStoreListSortedSkipsTempAndMissing`)
- [x] `GET /admin/resources/{name}` отдаёт метадату; `.../content` отдаёт байты
  идентичные залитым; отсутствие → 404.
  (`TestResourcePutThenStatThenContent`, `TestResourceStatNotFound`)
- [x] `apply` конфига с `type: local, path: <state_dir>/resources/<name>` →
  ядро видит файл, валидация проходит. **(живьём на macOS: реальный `.srs` из
  `rule-set compile`, apply → `applied:true`, `status:started`; освобождение +
  delete + traversal-400 проверены на живом демоне)**
- [x] `PUT`/`DELETE` для имени, на которое ссылается активный ИЛИ last-good
  конфиг → 409; файл не тронут.
  (`TestResourcePutConflictWhenReferenced`, `TestResourceDeleteConflictWhenReferenced`,
  `TestResourceReferencedByActiveConfig`, `TestResourceReferencedByLastGood`)
- [x] `PUT`/`DELETE` для свободного имени → успех.
  (`TestResourceDeleteFreeNameSucceeds`, `TestResourcePutThenStatThenContent`)
- [x] Санитизация: `PUT /admin/resources/../evil`, абсолютный путь, пустое имя →
  400; ничего не пишется вне `resources/`.
  (`TestResourcePutRejectsBadName`, `TestResourceNameSanitization`; проверено, что
  Go 1.25 ServeMux декодирует `%2F`→`/`, санитизация ловит слэш в имени)
- [x] Перезапись существующего свободного имени атомарна (writeAtomic); хеш
  обновляется. (`TestResourceStoreOverwriteUpdatesHash`)
- [x] Юниты `resources_test.go` зелёные. Сквозное демо на macOS живьём (залить
  `.srs`, сослаться из конфига, apply, дифнуть хеш) — **долг**.
