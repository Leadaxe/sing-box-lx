# SPEC: 117 — UPSTREAM_SYNC_SCOPED_LIFECYCLE

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` (v1.14.2 + 23, без нового тега) в `lx`: апстрим перевёл жизненный цикл компонентов на `adapter.Scope` |
| Статус | I (implemented) — 2026-10-07, ветка `lx-sync-stable-2026-10` в worktree `../sing-box-lx-sync`, в `lx` не влита; сборка (darwin, linux, windows, android libbox) и тесты зелёные, живой прогон на бинаре; на устройстве не прогонялось |
| Ветка | `lx-sync-stable-2026-10` → `lx` |
| База | до: `aecb87441` (v1.14.2-lx.12-rc.1, merge-base `4537a1ac0`); после: `upstream/stable` = `b93f56a7a` |
| Связано | [109](../109-UPSTREAM_SYNC_1_14_2_PLUS_15/SPEC.md) (предыдущий синк), [030](../030-FAST_BOX_SHUTDOWN/SPEC.md) (быстрая остановка), [070](../070-WG_START_CLOSE_RACE_CRASH/SPEC.md) (Close во время Start), [073](../073-CHAIN_OUTBOUND/SPEC.md) (chain), [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md)/[097](../097-LAZY_WG_DEVICE_BUILD/SPEC.md)/[106](../106-WG_ENDPOINT_TOGGLE/SPEC.md) (сон и сборка WG) |

Решение владельца 2026-10-07: rc.1 линии lx.12 срезан без синка (дрейф записан в changelog), синк сразу после тега.

## 1. Что принёс апстрим

После мержа lx.11 апстрим снова переписал историю `stable`: merge-base отстал на 23 коммита, из них новых по subject — 4.

| Коммит | Суть |
|---|---|
| `813ddfa98` Refactor lifecycle to scoped cleanup | `adapter.Lifecycle` = `Start(stage, scope *adapter.Scope)`. У компонентов и менеджеров больше нет `Close()`: очистка регистрируется `scope.Add(...)`, `Scope.Close` выполняет её в обратном порядке. `Box.Close` = `s.scope.Close()`. Удалены `adapter.LegacyStart`, `PostStart()`, логгер в `outbound/endpoint/dns.NewTransportManager` |
| `16c898a82` Remove unimplemented hot reload from managers | У менеджеров удалены `Remove`, повторный `Create` по тегу — ошибка `duplicate ... tag` |
| `9593bc106` Fix usage and cache files overwritten when loading fails | `service/{ccm,ocm,ssmapi}` |
| `b93f56a7a` Move auto-redirect and bridge index allocation out of constructors | `protocol/{bridge,tun}` |

Модули: `sing-quic` → `75c3ac4fa12b`, `sing-cloudflared` → `c1255ae368f2`. Пины форк-сабмодулей в `require` не менялись — работы в сабмодулях нет.

## 2. Разрешение

Мерж `upstream/stable` дал 29 конфликтов. Рецепт прошлых синков: файл без нашей дельты (`HEAD:f == be6558602^2:f`, чистая апстримная база прошлого мержа) берётся у апстрима (15 файлов); остальные — `git merge-file` с этой базой. Честных конфликтов осталось 7.

| Файл | Разрешение |
|---|---|
| `adapter/endpoint/manager.go` | форма апстрима + поля chain (073). **SPEC 030 перенесён**: каждый endpoint стартует в собственном scope (`manager_close_lx.go`), а в scope менеджера одна очистка закрывает их параллельно (`task.Group`, 8 одновременно). Без этого апстримный scope закрывал бы endpoint'ы по одному. `NewManager` сохраняет параметр логгера (`box.go`, строка `lx:`) |
| `adapter/outbound/manager.go` | форма апстрима + поля и `Outbound()`-ветка chain; `Remove` удалён вслед за апстримом (наш код его не зовёт) |
| `box.go` | `Close` = однократный `router.QuiesceForShutdown()` (SPEC 030) + `scope.Close()`. CAS SPEC 070 против двойного `close(done)` больше не нужен: `Scope.Close` под мьютексом отдаёт очистку ровно одному вызывающему |
| `dns/router.go` | `Rules()` (SPEC 014) + новая сигнатура `Start` |
| `protocol/group/urltest.go` | `Start(stage, scope)` апстрима; наши поля группы (019/020) — в ветке `StartStateStart` |
| `protocol/wireguard/endpoint.go` | наш `Close()` (020/030/070/097/106) сохранён и регистрируется `scope.Add(w.Close)` на `Initialize` до ленивой ветки; апстримные `scope.Add(endpoint.Close)` и сброс `started` не нужны — `Close` делает то и другое |
| `route/router.go` | форма апстрима; остановка idle-тика (020) — `scope.Add` на `PostStart` перед `startIdleSuspend` |
| `go.sum` | наш + строки двух новых модулей |

**Автослияние.** У 11 файлов, где правили и мы, и апстрим, число строк нашей дельты к апстриму до и после мержа совпало; правок, опирающихся на `Close`/`Start`, среди них нет. Файлы без нашей дельты, которые git слил сам, сверены с `upstream/stable`; `service/ccm/service.go` (задвоенный метод `References`) и `service/usbip/client.go` приведены к апстримным.

## 3. lx-код на новом жизненном цикле

Компилятор ловит только сигнатуры. Вторая, тихая часть: апстримные менеджеры стартуют и закрывают **только** `adapter.Lifecycle` — outbound со старым `Start() error` или одним `Close()` больше не стартовал бы и не закрывался. Поиск по типам с `DialContext` и `Close` без новой `Start` нашёл два таких outbound'а.

| Компонент | Было | Стало |
|---|---|---|
| `protocol/chain.Chain` | `Start()`, `Close()` (через `LegacyStart`) | `Start(stage, scope)`: `Initialize` → `scope.Add(c.Close)`, `Start` → прежний `start()` |
| `protocol/masque.Outbound` | только `Close()` | `Start(stage, scope)` регистрирует `Close` |
| звено chain (`clone.go`) | `LegacyStart` по стадиям + `common.Close` | собственный `adapter.Scope` на звено: старт через `scope.Start`, закрытие — `scope.Close`; узел без `Lifecycle` закрывается напрямую |
| `common/dnstrack.Manager` | `Start(stage)` + `Close` | `scope.Add(m.Close)` на `Initialize` |
| `dns/transport/group.Transport` | `Start(stage)` | новая сигнатура, `Close` — no-op |
| `GetURLViaOutbound` (daemon) | `certificate.Store.Close` | шаблон libbox `NewHTTPClient`: `store.Start(Initialize, scope)` + `scope.Close` |

Тесты переведены на `Start(stage, scope)`; стенд chain стартует менеджеры через `adapter.Scope`.

## 4. Найдено, не чинится

- **Апстрим: `close service/api[0]: use of closed network connection`** при каждой остановке с `services: [{type: api}]` — `service/api` регистрирует в scope и `httpServer.Close` (закрывает listener), и `listener.Close`. Воспроизводится на чистом `upstream/stable`. Одна ERROR-строка при остановке, на работу не влияет. `service/api` у нас без дельты — не трогаем.

## 5. Проверки

- `go build ./...` без тегов; полный `LX_TAGS` и `LX_TAGS`+`with_lx_idle_suspend` — darwin; `GOOS=linux`, `GOOS=windows` (`cmd/sing-box` + libbox); `GOOS=android` libbox (теги без naive/purego/clash_api).
- `go vet` с полными тегами и без — чисто (кроме старых `unsafe.Pointer`).
- `go test` с `LX_TAGS`+`with_lx_idle_suspend`: `.`, `adapter/...`, `route/...`, `dns/...`, `protocol/...`, `transport/...`, `common/...`, `daemon`, `experimental/...`, `lxd/...`, `cmd/sing-box`; `-race` — `.`, `adapter/...`, `route`, `protocol/{wireguard,group,chain}`, `transport/wireguard`, `daemon`, `dns/transport/group`.
- Стенды `lx-test/{chain,initerr,startclose,zombie}`; `sing-box check` по 9 конфигам `lx-test/config`.
- Живой прогон: серверный AWG-конфиг (SPEC 114) + клиент, `api peers` показывает хендшейк, оба процесса останавливаются по SIGTERM за 0,03 с.
- `make -f Makefile.lx lx-check` — релизный бинарь, 9 конфигов.
- Стенд жизненного цикла на бинаре с `with_lx_idle_suspend` (AWG-сервер на три пира; клиент: три ленивых WG-узла, urltest, selector, `lx.wg` с `idle_suspend 10s`, `idle_teardown 20s`, `build_max 2`): `never_built` → сборка пробой urltest → `asleep` → `torn_down` → пробуждение и пересборка дайлом, переключение selector на несобранный узел, три цикла старт/стоп (0,1–0,33 с). `lx idle`-строки — в ожидаемом порядке, ошибок, кроме §4, нет.
- `TestGroupStateSnapshotV3` (`dns/transport/group`) однажды упал в полном прогоне. Тест гонкой сравнивает участников с задержкой 1 мс и 8 мс; под нагрузкой процессора падает 10/200 на `lx` до мержа и 7/200 после — нестабильность теста, не регрессия синка.

## 6. Осталось

- Слить ветку в `lx` и выпустить rc.2 линии lx.12.
- Прогон на устройстве по раннбуку §1.4 (старт/стоп ×3, сон и пробуждение WG, смена сети): жизненный цикл — то, что юниты покрывают хуже всего.
