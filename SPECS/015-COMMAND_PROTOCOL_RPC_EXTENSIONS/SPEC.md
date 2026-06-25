# SPEC: 015 — COMMAND_PROTOCOL_RPC_EXTENSIONS

| Поле | Значение |
|------|----------|
| Тип | F (feature) — расширения libbox command-протокола (CONSTITUTION §3.6) |
| Статус | M (mixed) — `URLTestOutbound` + `GetRules` сделаны/зашиплены (`v1.14.0-lx.1-rc.2`); `getGroups`/`getOutbounds` + фикс `len<2` — TODO (target `rc.4`) |

**Единый дом всех доработок нативного libbox CommandClient** (gRPC `StartedService`), доведших его до минимума, на котором UI LxBox реально работает после переезда с Clash API (см. [SPEC 014](../014-CLASH_API_TO_COMMANDCLIENT_MIGRATION/SPEC.md) — сам переезд).

Пять RPC-доработок, все одного класса §3.6 (handler'ы за `with_lx_command` по образцу `daemon/started_service_usbip{,_stub}.go`, шов в `.proto` под `// lx:`-маркером, логика в `*_lx.go`):

| # | RPC / фикс | Назначение | Статус |
|---|-----------|-----------|--------|
| 1 | `URLTestOutbound` | per-node delay-тест (outbound/endpoint), URL + timeout, синхронный ответ | ✅ rc.2 |
| 2 | `GetRules` | снапшот таблицы правил (route + DNS) | ✅ rc.2 |
| 3 | `GetGroups` | unary pull-снапшот групп (дыра pull vs push) | ⬜ rc.4 |
| 4 | `GetOutbounds` | unary pull-снапшот плоского списка узлов | ⬜ rc.4 |
| 5 | фикс `len<2` в `readGroups()` | upstream-баг: группы с 0–1 узлом молча теряются | ⬜ rc.4 |

Scope: **client/command-сторона only** (§3.1 client-only, §3.6 п.1 «только мост»). Build-tag: **`with_lx_command`**.

**Апстрим-кандидатность (см. §7):** #3/#4 (pull-геттеры) и #5 (`len<2`) — чистые upstream-дефекты, независимые от lx, кандидаты в upstream-PR. #1/#2 сейчас живут в **lx-форме** (за `with_lx_command`, `// lx:`); для upstream-PR им нужна **upstream-форма** (без тега и маркеров) — это план §7, не сделано.

---

## 1. Проблема / контекст

LxBox переехал с Clash API на нативный CommandClient ([SPEC 014](../014-CLASH_API_TO_COMMANDCLIENT_MIGRATION/SPEC.md): `with_clash_api` удалён в `v1.14.0-lx.1-rc.1`). Но нативный CommandClient **беднее** Clash API сразу по нескольким возможностям, без которых UI неполноценен:

- **Per-node delay-тест.** Существующий `rpc URLTest` ([daemon/started_service.go](../../daemon/started_service.go)) принимает только **группу** (hard type-assert на `adapter.OutboundGroup`, отказ «outbound is not a group»), меряет захардкоженным `https://www.gstatic.com/generate_204`, без таймаута, результат уходит только в стрим. Clash `/proxies/{name}/delay?url=&timeout=` ([experimental/clashapi/proxies.go:187](../../experimental/clashapi/proxies.go)) умел: одиночный узел, произвольный URL, таймаут, синхронный ответ.
- **Таблица правил.** Clash `/rules` ([experimental/clashapi/rules.go:24](../../experimental/clashapi/rules.go)) отдавал `router.Rules()` как `{type, payload, proxy}`. В CommandClient аналога нет.
- **Pull-снапшот групп/узлов.** Clash был **pull**: `GET /proxies` — дёрнул в любой момент, получил снапшот. CommandClient — **push-only**: группы приходят лишь стримом `SubscribeGroups`/`SubscribeOutbounds`. Если стартовый push не доехал (стрим не открылся из-за гонки фаз сервиса) — перезапросить нечем (см. §3.3).
- **Баг потери групп.** `readGroups()` молча выбрасывает группы с 0–1 узлом (`len<2`) — нарушает Clash-паритет, прячет одно-узловые селекторы (см. §3.5).

Движок и хранилища **уже всё умеют** — `urltest.URLTest(ctx, link, detour)` принимает URL и `ctx`-deadline; `adapter.Router.Rules()` публичен; `s.readGroups()` существует. Не хватает только **проброса через gRPC** (§3.6 «мост»: проброс существующей возможности ядра, не новая подсистема).

По CONSTITUTION §3.1(а) фича легальна: (а1) нужна LxBox; (а2) недоступна в нашем канале — была в upstream лишь через вырезанный Clash API (либо отсутствует как pull); (а3) свой дифф (швы в `.proto`/интерфейсы + handler'ы в `_lx.go`) дешевле, чем вернуть весь HTTP-сервер `with_clash_api`.

---

## 2. Общая инфраструктура (§3.6 класс)

### 2.1 Build-tag `with_lx_command` (§3.6 п.3)
- Реальные handler'ы — за `//go:build with_lx_command`; файл-близнец `*_stub.go` за `//go:build !with_lx_command` возвращает `codes.Unimplemented` (образец: `daemon/started_service_usbip{,_stub}.go`).
- Без тега сборка поведенчески эквивалентна upstream (новые RPC не обслуживаются). Регистрация самих RPC в `service` сгенерирована из `.proto` всегда — гейтится только рукописный handler.
- Тег в `sharedTags` ([cmd/internal/build_libbox/main.go](../../cmd/internal/build_libbox/main.go), `// lx:`-блок) — иначе RPC не попадёт в `libbox.aar`. Для десктопа — в `LX_TAGS` (`Makefile.lx`).

### 2.2 Детерминированная регенерация proto (§3.6 п.5)
Генерация была невоспроизводима: `make proto` ([Makefile](../../Makefile)) шеллит системный `protoc` из PATH, `proto_install` ставит `protoc-gen-go@latest` / `protoc-gen-go-grpc@latest`.
- В `Makefile.lx` добавлены `lx-proto-install` / `lx-proto` с **зафиксированными** версиями: `protoc-gen-go` = **v1.36.11** (= go.mod `google.golang.org/protobuf`), `protoc-gen-go-grpc` = **v1.5.1** (под `grpc.SupportPackageIsVersion9`). `protoc` — внешняя зависимость (`brew install protobuf`); генератор `cmd/internal/protogen` его драйвит и срезает version-баннер (`NormalizeGeneratedProtoFile`) — номер сборки `protoc` не течёт. `gofumpt -w` нормализует импорты. Регенерация **идемпотентна** (повторный прогон — нулевой дифф).
- `*.pb.go` / `*_grpc.pb.go` — машинный вывод, руками не правятся, маркеров не несут; на ребейзе **регенерируются** из смерженного `.proto`, не мёржатся текстом.
- **Цена pinned-тулчейна (audited):** committed `.pb.go` исторически сгенерированы иным тулчейном, чем пин. Первая регенерация под пином — помимо `daemon/started_service.*` — косметически переписывает соседние generated-файлы того же `make proto`-набора: `daemon/managed_service.{pb,_grpc.pb}.go`, `experimental/v2rayapi/stats.{pb,_grpc.pb}.go`, `transport/v2raygrpc/stream.{pb,_grpc.pb}.go` (`(Enum)(0)`→`Enum(0)`, `status.Error`→`Errorf`, import-order). Разовая нормализация; дальше воспроизводимо.

### 2.3 CI-инвариант (§3.6 п.7)
В `lx-ci.yml` — обе сборки: **без** `with_lx_command` (компилируется, `*_stub.go` отдаёт `Unimplemented`, поведение = upstream) и **с** тегом (RPC обслуживается). Usbip-паттерн делает проверку дешёвой.

---

## 3. RPC

### 3.1 `URLTestOutbound` ✅ rc.2
Шов в [daemon/started_service.proto](../../daemon/started_service.proto), под маркером:
```proto
// lx:begin lx_command
rpc URLTestOutbound(URLTestOutboundRequest) returns (URLTestOutboundResponse) {}

message URLTestOutboundRequest {
  string outboundTag = 1;   // тег outbound ИЛИ endpoint (НЕ группы)
  string link        = 2;   // пусто → https://www.gstatic.com/generate_204
  uint32 timeout     = 3;   // 0 → дефолт ядра (без явного deadline); иначе миллисекунды
}
message URLTestOutboundResponse {
  uint32 delay = 1;         // латентность, мс (движок: uint16 → uint32 без потерь)
  string error = 2;         // "" = ок; иначе причина (not-found / timeout / dial / bad-status)
}
// lx:end lx_command
```
Handler — `daemon/started_service_command_lx.go` (+ `_stub.go`):
- **Резолв тега в ОБОИХ менеджерах:** сначала `boxService.outboundManager.Outbound(tag)`, при промахе `boxService.endpointManager.Get(tag)`. `adapter.Endpoint` встраивает `Outbound`/`N.Dialer` → endpoint передаётся в `urltest.URLTest` без обёрток. **Никакого** type-assert на `OutboundGroup`.
- **Таймаут:** `timeout == 0` → общий `boxService.ctx`; `> 0` → `context.WithTimeout(boxService.ctx, time.Duration(timeout)*time.Millisecond)` + `defer cancel()`.
- **Вызов:** `delay, err := urltest.URLTest(ctx, req.Link, detour)`.
- **Модель ошибок — ВСЁ в payload (Вариант B), `status.Error` не используется для прикладных сбоев:**
  | Ситуация | `delay` | `error` |
  |----------|---------|---------|
  | успех (в т.ч. 0 мс) | latency | `""` |
  | узел не найден | 0 | `"outbound or endpoint not found: <tag>"` |
  | тест провален (timeout/dial/bad-status) | 0 | `err.Error()` |

  Handler **всегда** возвращает `(resp, nil)` — транспортный gRPC-error остаётся `nil`.
- **ИНВАРИАНТ для клиента:** источник истины — поле `error`. `delay` валиден ⟺ `error == ""`. `delay==0 && error==""` = **успех 0 мс**, НЕ ошибка (иначе воркер словит ложный фейл на быстром локальном узле).
- **История (запись):** при успехе `urlTestHistoryStorage.StoreURLTestHistory(group.RealTag(detour), {Time: now, Delay: delay})`; при ошибке `DeleteURLTestHistory(realTag)`. `RealTag(detour)` для одиночного outbound/endpoint = `detour.Tag()`.

**Куда попадает delay (карта каналов):** `Store/DeleteURLTestHistory` будят общий `urlTestObserver`, на который подписаны оба групповых стрима:
| Канал | Что отдаёт | Покрытие узлов |
|-------|-----------|----------------|
| **Синхронный ответ RPC** | измеренный delay немедленно | **любой** узел (outbound/endpoint). Единственный гарантированный канал; UI ручного пинга читает его. |
| **`SubscribeOutbounds`** | `GroupItem.UrlTestDelay`/`UrlTestTime` из истории | **все** outbound'ы И **все** endpoint'ы (WG/AWG/Tailscale). Канал, где delay endpoint'а появляется в стриме. |
| **`SubscribeGroups`** | то же из истории | **только** узлы внутри `OutboundGroup`. Одиночные outbound вне групп и **любые endpoint'ы НЕ попадают**. |

Клиентский метод — `experimental/libbox/command_client_command_lx.go`:
```go
func (c *CommandClient) URLTestOutbound(outboundTag, link string, timeout int32) (*URLTestOutboundResult, error)
type URLTestOutboundResult struct {
	Delay int32  // мс, валиден ⟺ Error == ""
	Error string // "" = ок; иначе причина
}
```
**Почему не `(uint16, string, error)`:** gomobile НЕ биндит ни три возврата, ни `uint16`/`uint32`. Возврат — struct-обёртка (как `*SystemProxyStatus`, геттеры `getDelay()`/`getError()`); параметр `timeout` (не `timeoutMs`); типы `int32`. Возвращаемый Go-`error` — **только транспортный сбой**; прикладной исход — в `Result.Error` (Вариант B). `timeout` в мс (`0` → дефолт).

Worker-pool (масс-пинг N узлов, concurrency=10) — **в клиенте/LxBox**, не в ядре: ядро меряет один узел синхронно и stateless. Отмена масс-пинга = закрытие/реконнект CommandClient-соединения (рвёт conn → серверный `ctx` отменяется → in-flight падают). Per-call cancel-handle НЕ вводится.

### 3.2 `GetRules` (route + DNS) ✅ rc.2
Шов под тем же `// lx:`-маркером:
```proto
// lx:begin lx_command
rpc GetRules(google.protobuf.Empty) returns (RuleList) {}

message Rule {
  string type    = 1;   // rule.Type()
  string payload = 2;   // rule.String()
  string action  = 3;   // rule.Action().String()
  bool   isDNS   = 4;   // false = route-rule, true = dns-rule
}
message RuleList { repeated Rule rules = 1; }
// lx:end lx_command
```
Handler (`started_service_command_lx.go`): снапшот, unary (правила статичны).
- **Route-rules:** `boxService.router.Rules()` ([adapter/router.go:21](../../adapter/router.go), публичный) → `{type, payload, action, isDNS:false}`. Поля — как Clash `rules.go`.
- **DNS-rules:** через **новый геттер** (см. §3.2.1) → `{..., isDNS:true}`. `adapter.DNSRule` встраивает `Rule` → те же `Type()/String()/Action()`.

#### 3.2.1 Правка апстрим-интерфейса для DNS-rules (цена «route + DNS»)
`adapter.Router` НЕ выставляет DNS-правила; `dns.Router.rules []adapter.DNSRule` приватно ([dns/router.go:44](../../dns/router.go)), `adapter.DNSRouter` ([adapter/dns.go:18](../../adapter/dns.go)) геттера не имеет. **+2 тронутых апстрим-файла**, оба за `// lx:`-маркером:
- `adapter/dns.go` — в `DNSRouter` добавить `Rules() []DNSRule`.
- `dns/router.go` — `func (r *Router) Rules() []adapter.DNSRule { return r.rules }`.

Clash DNS-правила не отдавал — эталона нет, мы превосходим Clash.

Клиент: `func (c *CommandClient) GetRules() (RuleIterator, error)`.

### 3.3 `GetGroups` ⬜ rc.4 — закрытие дыры pull vs push

**Проблема (pull vs push).** Clash был **pull**: `GET /proxies` → снапшот групп в любой момент. CommandClient — **push**: группы приходят только потоком `SubscribeGroups`. Стрим шлёт начальный снапшот первым `Send` (`readGroups()` до `select`) — НО только если открылся: `waitForStarted` ([started_service.go](../../daemon/started_service.go)) отклоняет подписку с ошибкой, когда сервис не `STARTED`/`STARTING` (`IDLE`, `STOPPING`, `FATAL` при рестарте/реконнекте). Если стрим не открылся или порвался — **перечитать нечем**: клиент вынужден пересоздавать весь `screenClient` (тяжёлый `refreshScreen`, рвущий `SubscribeConnections`). **На устройстве подтверждено:** watchdog делает 2 ретрая через `refreshScreen`, группы остаются пустыми (`tunnel=connected`, трафик идёт, но `groups=[]`, `nodes=0`). Переподписка ≠ pull.

**Решение.** Unary-снапшот поверх **существующего** `s.readGroups()` ([started_service.go:457](../../daemon/started_service.go)), переиспользуя существующий message `Groups` (новых message нет):
```proto
// lx:begin lx_command
rpc GetGroups(google.protobuf.Empty) returns (Groups) {}
// lx:end lx_command
```
Handler (`started_service_command_lx.go`): `waitForStarted(ctx)` (как стрим — вернёт `status.Error` при не-STARTED, что для unary даже честнее: клиент сразу узнаёт причину), затем под `serviceAccess.RLock` вызвать `s.readGroups()` и вернуть. Тело — калька подготовки из `SubscribeGroups`, но один `readGroups()` + `return` вместо цикла с `Send`. Ошибки — через `status.Error` (unary read-конвенция, как `GetRules`; НЕ Вариант-B).

Клиент: `func (c *CommandClient) GetGroups() (GroupIterator, error)`.

### 3.4 `GetOutbounds` ⬜ rc.4

Та же дыра pull vs push для плоского списка узлов. `SubscribeGroups` покрывает лишь узлы внутри групп; **одиночные outbound и любые endpoint'ы** (WG/AWG) видны только через `SubscribeOutbounds` (см. карту §3.1). Поэтому нужен **и** `GetOutbounds`, не только `GetGroups`.
```proto
// lx:begin lx_command
rpc GetOutbounds(google.protobuf.Empty) returns (OutboundList) {}
// lx:end lx_command
```
Handler: `readOutbounds` как отдельной функции **нет** (`SubscribeOutbounds` строит `OutboundList` инлайн: обход `outboundManager.Outbounds()` + `endpointManager.Endpoints()` + `LoadURLTestHistory`). Чтобы **не трогать апстрим-`started_service.go`** лишним рефактором — **продублировать построитель в `_lx.go`** и вернуть `OutboundList` (переиспользуя существующий message). `waitForStarted` + `status.Error`, как `GetGroups`.

Клиент: `func (c *CommandClient) GetOutbounds() (OutboundGroupItemIterator, error)`.

### 3.5 Фикс `len<2` в `readGroups()` ⬜ rc.4 — upstream-баг

`started_service.go:495`:
```go
if len(g.Items) < 2 {
    continue   // ← группа с 0 или 1 узлом молча выбрасывается
}
```
Группа с одной нодой (частый кейс — `proxy → один сервер`), пустая группа, или группа, где часть нод не зарезолвилась (`!isLoaded`) и осталась одна — **не попадают** в `Groups`. **Upstream-дефект** (пришёл с рефактором `5bc0dfa9` «platform: Refactoring libbox to use gRPC-based protocol»). Clash отдавал `group.All()` **без** фильтра по количеству → при переезде группы с 1 узлом исчезли (видимая регрессия).

`readGroups()` — единственный источник, его зовут `SubscribeGroups` (стартовый бродкаст) И будущий `GetGroups`. Фикс здесь покрывает **оба** пути.

**Фикс:** убрать условие `len(g.Items) < 2 { continue }` — группы любого размера валидны. (Если апстрим вводил его как анти-шум — обоснования в коде нет; Clash-паритет требует отдавать все.)

---

## 4. Критерии приёмки

1. (✅ rc.2) Сборка с `with_lx_command`: `URLTestOutbound` меряет outbound И endpoint (AWG/WG); кастомный `link`/`timeout` применяются; `error`-поле по таблице §3.1; `delay==0 && error==""` = успех 0 мс.
2. (✅ rc.2) `GetRules` возвращает route- и DNS-правила с `isDNS`-разделением; route-поля совпадают с Clash.
3. (⬜ rc.4) `GetGroups` возвращает тот же снапшот, что первый `Send` `SubscribeGroups`; вызываем в любой момент при `STARTED`, не трогая активные стримы; при не-`STARTED` — `status.Error` с причиной.
4. (⬜ rc.4) `GetOutbounds` возвращает то же, что `SubscribeOutbounds` (все outbound'ы + endpoint'ы с delay из истории).
5. (⬜ rc.4) После фикса `len<2`: группы с 1 узлом видны и в стартовом бродкасте `SubscribeGroups`, и в `GetGroups`.
6. Сборка **без** `with_lx_command`: компилируется, все RPC → `codes.Unimplemented`, поведение = upstream.
7. `Makefile.lx` proto-таргет регенерирует `*.pb.go` воспроизводимо; сгенерированный код gofmt-чист, без `// lx:`-маркеров.
8. CI зелёный на обеих сборках (§2.3).
9. `with_lx_command` в `sharedTags` (AAR) и `LX_TAGS` (desktop).
10. Перечень тронутых общих файлов совпадает с фактическим диффом:
    - **Швы под `// lx:`:** `daemon/started_service.proto`, `adapter/dns.go`, `dns/router.go`, `cmd/internal/build_libbox/main.go`. Фикс §3.5 — правка `daemon/started_service.go` (под `// lx:` или как upstream-багфикс, см. §7).
    - **Сборка/CI:** `Makefile.lx`, `.github/workflows/lx-ci.yml`.
    - **Регенерация SPEC-proto:** `daemon/started_service.{pb,_grpc.pb}.go`.
    - **Косметическая регенерация под пин (разовая, §2.2):** `daemon/managed_service.*`, `experimental/v2rayapi/stats.*`, `transport/v2raygrpc/stream.*`.
    - **Логика — в новых файлах:** `daemon/started_service_command_lx.go` + `_stub.go`, `experimental/libbox/command_client_command_lx.go`.

---

## 5. Вне скоупа
- Отдельный history-RPC (`GetURLTestHistory`) — отклонён: delay синхронен в ответе RPC, живая история течёт в `SubscribeOutbounds`/`SubscribeGroups`.
- Per-call cancel-handle / batch-RPC — отклонены: отмена через close conn, батч в клиенте.
- `timeout` в микросекундах / смена типа delay — отклонены: мс, `uint16`→`uint32`.
- DNS rule-set (headless) snapshot, server/inbound RPC — будущие SPEC.

---

## 6. Ссылки
- CONSTITUTION §3.6 (класс libbox command-расширений), §3.1(а) (тройной тест).
- [SPEC 014](../014-CLASH_API_TO_COMMANDCLIENT_MIGRATION/SPEC.md) — переезд Clash API → CommandClient (контекст этого SPEC).
- Эталон семантики: `experimental/clashapi/proxies.go` (per-node delay + список), `rules.go` (правила).
- Образец гейтинга: `daemon/started_service_usbip{,_stub}.go`.
- Движок/хранилище: `common/urltest/urltest.go`, `HistoryStorage`; `s.readGroups()` (`daemon/started_service.go:457`).
- Память: `lx-commandclient-extensions-spec014`.

---

## 7. Апстрим-кандидатность (план, не сделано)

Этот SPEC содержит изменения двух природ:

- **Чистые upstream-дефекты, независимые от lx — кандидаты в upstream-PR как есть:**
  - **#5 `len<2`** — баг их же рефактора `5bc0dfa9`, ломает Clash-паритет. Однострочный фикс, подаётся напрямую.
  - **#3/#4 pull-геттеры** (`GetGroups`/`GetOutbounds`) — структурная дыра их протокола (push-only, нет unary-аналога `Subscribe*`). Полезны любому клиенту CommandClient, не только нам.
- **lx-форма vs upstream-форма (#1/#2, #3/#4):** в нашем дереве все эти RPC живут в **lx-форме** — за `with_lx_command`, под `// lx:`-маркерами, handler'ы в `_lx.go`. Это правильно для форка (§3.6). Но для **upstream-PR** им нужна **upstream-форма**: без build-tag, без маркеров, handler'ы в самом `started_service.go`, RPC безусловно в `service`. Конвертация lx-форма → upstream-форма — **отдельная будущая задача** (когда/если решим подавать PR), здесь НЕ выполняется. `URLTestOutbound`/`GetRules` уже зашиплены в lx-форме (rc.2) и менять их форму ради PR — отдельное решение.

> Практически: реализуем #3/#4/#5 в lx-форме (target `rc.4`), как #1/#2. Upstream-PR — потом, отдельным заходом, с конвертацией формы. Эта секция фиксирует обязательство отслеживать кандидатов, чтобы дифф к апстриму со временем уменьшался (CONSTITUTION §2).
