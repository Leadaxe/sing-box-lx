# IMPLEMENTATION_REPORT: 098 — LX_ROOT_CONFIG_BLOCK

Ветка `lx`, не запушена. Не выпущена: войдёт в v1.14.1-lx.13 вместе с 097.

## Коммиты

| Коммит | Что |
|---|---|
| `6e234fb3f` | `option/lx.go`: типы блока, `LXResolved`, `ResolveLX`; строка `LX` в `option/options.go` |
| `ccc3c8962` | `option/route.go`: три поля — устаревшие алиасы `lx.wg.*` (комментарии) |
| `97b05ae45` | masque: `IdleTimeout *badoption.Duration`, `idleWindow(узел, глобальный)`, глобальный дефолт из контекста; тесты приоритета |
| `68e6bd420` | `box_lx.go`: `applyLXOptions` |
| `b906a489a` | `box.go`: вызов `applyLXOptions` до `routeOptions`/`NewRouter` под `lx:begin lx-block` |
| `94d9abdeb` | `route`: `lxWG(ctx)`, тексты ошибок → `lx.wg.*`, stub отвергает все ключи `lx.wg` |
| `63d423d89` | `route/router.go`: idle-поля из контекста, `idleTeardownOf` удалён; тесты роутера через контекст |
| `4090c5ede` | daemon: running-config в канонической форме |
| `ece5daf09` | `option/lx_test.go` |
| `3cb2c63ac` | `lx-test/config/lx_block.json`, `lx-check` по всем конфигам |
| (этот) | доки, FEATURE 008/009, changelog, статус, отчёт |

## Файлы

- **Новые:** `option/lx.go`, `option/lx_test.go`, `box_lx.go`, `route/lx_block_lx_test.go`,
  `lx-test/config/lx_block.json`.
- **`// lx:` правки апстрима:** `option/options.go` (1 строка), `option/route.go` (комментарии внутри
  `lx:begin idle-suspend`), `box.go` (блок `lx:begin lx-block`, 4 строки кода), `route/router.go`
  (4 строки инициализации + комментарии; `idleTeardownOf` с его lx-блоком удалён).
- **Правки наших файлов:** `option/masque.go`, `protocol/masque/outbound.go`,
  `route/reachability_common_lx.go`, `route/reachability_lx.go`, `route/idle_suspend_stub_lx.go`,
  `daemon/instance_command_lx.go`, тесты route/masque/daemon, `Makefile.lx`.
- **deps / go.mod / build-теги:** без изменений.

## Отступления от PLAN

1. **Ключ контекста.** PLAN пишет `service.FromContext[*option.LXResolved]`. Эта пара с
   `ContextWithPtr` не работает: `FromContext[*T]` ищет ключ `**T` и молча отдаёт nil (грабля §180).
   Регистрация — `service.ContextWithPtr(ctx, resolved)`, чтение — `service.PtrFromContext[option.LXResolved]`;
   у `*LXResolved` nil-безопасные `WGOrZero()`/`MASQUEOrZero()`. SPEC §4 поправлена.
2. **`daemon/instance.go` не тронут.** Резолв перенесён в `captureRunningConfig`
   (`daemon/instance_command_lx.go`, файл под `with_lx_command`): функция получает `Options` по значению,
   а `ResolveLX` заменяет указатели `LX`/`Route` новыми копиями и не пишет сквозь них. Снимок
   канонический, а `box.New` получает исходные опции и сам пишет предупреждения алиасов. Если бы демон
   канонизировал опции до `box.New`, второй резолв был бы no-op и предупреждения пропали бы как раз
   на пути LxBox/лаунчера. Зона касания апстрима на одну строку меньше плана.
3. **`box.go` — 4 строки, не одна:** вызов плюс проверка ошибки, под `lx:begin/lx:end`.
4. **Отрицательные длительности `lx.wg.*` и `lx.masque.idle_timeout` — ошибка** (`must be >= 0`).
   Раньше отрицательный `route.lx_idle_suspend` молча означал «выкл». Отрицательный `idle_timeout`
   узла masque по-прежнему «не усыплять».
5. **Пустой блок канонизируется в отсутствующий:** `{"lx":{}}` и пустые подблоки в running-config
   не попадают.
6. **Stub отвергает и `build_overflow: "build"`** (PLAN называл `lazy_build`/`build_max`): правило
   «любой ненулевой ключ `lx.wg.*`». Явный `"wait"` — дефолт, не отвергается.
7. **`build_max` не требует `idle_suspend`** — в SPEC такого правила нет; `lazy_build` требует.
8. Проверки в тегированном `startIdleSuspend` оставлены как защита для роутеров, собранных вне
   `box.New`; тексты переведены на `lx.wg.*`.

## Поведение, которое стоит знать

- Валидация и алиасы срабатывают в `box.New`, то есть и в `sing-box check`. Гейт тега — на старте
  роутера, поэтому desktop-`check` принимает `lx.wg.*`, а `run` отвергает.
- Предупреждения алиасов видны в логе `run` (буфер логгера до старта); `check` их не печатает —
  логгер там не стартует, как и у апстримных pre-start сообщений.
- Роутер больше не читает `RouteOptions.LXIdle*`. Тест `TestRouterIdleFields_ignoreRouteOptions`
  это фиксирует: тест, собирающий роутер по-старому, не может пройти с включённым тиком.

## Тесты и проверки (go1.26.8, `GOTOOLCHAIN=go1.26.8`)

| Команда | Результат |
|---|---|
| `go build ./...` | ok |
| `go build -tags with_lx_idle_suspend ./...` | ok |
| `go build -ldflags "-checklinkname=0" -tags "$(make -s -f Makefile.lx lx-print-tags),with_lx_idle_suspend" ./...` | ok |
| `make -f Makefile.lx lx-build` | ok |
| `go test -ldflags "-checklinkname=0" ./option/ ./route/ ./protocol/masque/ ./protocol/wireguard/ ./daemon/` | ok |
| то же с `-tags with_lx_idle_suspend` для `./route/ ./protocol/wireguard/` | ok |
| `go test … -tags with_lx_command ./daemon/` | ok |
| `go test … -tags "$LX_TAGS" . ./option/ ./route/ ./protocol/masque/ ./protocol/wireguard/ ./daemon/` и с `,with_lx_idle_suspend` | ok |
| `go test … -tags "$LX_TAGS" ./lx-test/...` (chain, initerr, startclose, zombie) | ok |
| `make -f Makefile.lx lx-check` (9 конфигов, включая `lx_block.json`) | ok |
| `go vet -tags "$LX_TAGS"` по затронутым пакетам | ok, кроме известного апстримного `unsafeptr` в `daemon/managed_service.go` (в CI гасится `-unsafeptr=false`) |
| `gofmt -l` по всем тронутым `.go` | чисто |

Ручная проверка собранным бинарём: `route.lx_idle_*` — `check` проходит, `run` пишет
`WARN lx: route.lx_idle_suspend is deprecated, use lx.wg.idle_suspend`; разные значения — `FATAL
route.lx_idle_suspend conflicts with lx.wg.idle_suspend`; `lx.naive` — `unknown field "naive"`;
`lx.wg.idle_suspend` на desktop-`run` — `lx.wg.* is set but this build lacks idle-suspend support…`.

## Не сделано / вне скоупа

- П. 9 TASKS: уведомить сессии LxBox и лаунчера о переходе на `lx.*` — за владельцем.
- `docs-lx/lx-protocols-transports(.ru).md` §3 (таблица полей masque: `idle_timeout` «off by default»)
  и комментарий в `cmd/internal/build_libbox/main.go` (про `route.lx_idle_suspend`) не в списке файлов
  PLAN и не правились; оба устарели по формулировке.
- Ключи 097 (`lazy_build`, `build_max`, `build_overflow`) только разбираются и валидируются; читателя
  у них нет до 097.
