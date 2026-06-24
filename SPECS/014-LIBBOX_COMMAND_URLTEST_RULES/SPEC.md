# SPEC: 014 — LIBBOX_COMMAND_URLTEST_RULES

| Поле | Значение |
|------|----------|
| Тип | F (feature) — расширение libbox command-протокола (§3.6) |
| Статус | D (draft) |

Добавить в нативный libbox **CommandClient** (gRPC `StartedService`) два RPC, восстанавливающих в нашем канале управления то, что upstream отдаёт только через вырезанный Clash API:

1. **`URLTestOutbound`** — delay-тест одного узла (outbound **или** endpoint) с произвольным URL и таймаутом.
2. **`GetRules`** — снапшот таблицы правил маршрутизации (**route + DNS**).

Это первый SPEC класса **«расширения libbox command-протокола»** (CONSTITUTION §3.6): handler'ы за build-tag `with_lx_command` по образцу `daemon/started_service_usbip{,_stub}.go`, шов в `.proto` под `// lx:`-маркером, регенерация `*.pb.go` — детерминированная часть lx-build.

Scope: **client/command-сторона only** (§3.1 client-only, §3.6 п.1 «только мост»). Build-tag: **`with_lx_command`**.

---

## 1. Проблема / контекст

LxBox переходит на управление ядром через нативный libbox **CommandClient** (gRPC поверх unix-сокета) и отказывается от Clash API: `with_clash_api` удалён из Android AAR и десктоп `LX_TAGS` (v1.14.0-lx.1-rc.1, commit `57b5b5e5`). Но нативный CommandClient **беднее** Clash API по двум возможностям, которые LxBox-у нужны:

- **Per-node delay-тест.** Существующий `rpc URLTest` ([daemon/started_service.go:574](../../daemon/started_service.go)) принимает только **группу** (hard type-assert на `adapter.OutboundGroup`, отказ «outbound is not a group»), меряет захардкоженным `https://www.gstatic.com/generate_204`, без таймаута, результат уходит в стрим `OutboundGroupItem.URLTestDelay`. Clash `/proxies/{name}/delay?url=&timeout=` ([experimental/clashapi/proxies.go:187](../../experimental/clashapi/proxies.go)) умеет: одиночный узел, произвольный URL, таймаут, синхронный ответ. Эта возможность вырезана вместе с `with_clash_api`.
- **Таблица правил.** Clash `/rules` ([experimental/clashapi/rules.go:24](../../experimental/clashapi/rules.go)) отдаёт `router.Rules()` как `{type, payload, proxy}`. В CommandClient аналога нет (`Connection.Rule` даёт лишь per-connection строку сматчившего правила, не таблицу).

Движок и хранилище **уже всё умеют** — `urltest.URLTest(ctx, link, detour)` ([common/urltest/urltest.go:80](../../common/urltest/urltest.go)) принимает URL и работает через `ctx`-deadline; `adapter.Router.Rules()` публичен. Не хватает только **проброса через gRPC** (§3.6 «мост»: проброс существующей возможности ядра, не новая подсистема).

По CONSTITUTION §3.1(а) фича легальна: (а1) нужна LxBox; (а2) недоступна в нашем канале — есть в upstream лишь через вырезанный Clash API; (а3) свой дифф (шов в `.proto` + handler'ы в `_lx.go` + геттер DNS-rules) дешевле, чем вернуть весь HTTP-сервер `with_clash_api`.

---

## 2. Цель

LxBox через CommandClient может: (1) измерить latency произвольного узла — outbound или WG/AWG/Tailscale-endpoint — с кастомным URL и таймаутом, синхронным ответом `{delay, error}`; (2) получить снапшот route- и DNS-правил для экрана «Rules». Семантика delay — паритет с Clash; таблица правил — превосходит Clash (Clash DNS-правила не отдаёт).

---

## 3. Требования

### 3.1 Build-tag `with_lx_command` (§3.6 п.3)
- Реальные handler'ы — за `//go:build with_lx_command`; файл-близнец `*_stub.go` за `//go:build !with_lx_command` возвращает `codes.Unimplemented` (образец: `daemon/started_service_usbip{,_stub}.go`).
- Без тега сборка поведенчески эквивалентна upstream (новые RPC не обслуживаются). Регистрация самих RPC в `service` сгенерирована из `.proto` всегда — гейтится только рукописный handler.
- Тег добавляется в `sharedTags` в [cmd/internal/build_libbox/main.go](../../cmd/internal/build_libbox/main.go) (`// lx:`-блок) — иначе RPC не попадёт в `libbox.aar`. Для десктопа — в `LX_TAGS` (`Makefile.lx`).

### 3.2 RPC #1 — `URLTestOutbound`
Шов в [daemon/started_service.proto](../../daemon/started_service.proto), внутри `service StartedService {}` и в зоне message-ей, под маркером:
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

  Handler **всегда** возвращает `(resp, nil)` — транспортный gRPC-error остаётся `nil` для всех прикладных исходов.
- **ИНВАРИАНТ для клиента (записать в доке/комменте):** источник истины — поле `error`. `delay` валиден ⟺ `error == ""`. Случай `delay==0 && error==""` = **успех 0 мс**, НЕ ошибка (иначе воркер словит ложный фейл на быстром локальном узле).
- **История:** при успехе `urlTestHistoryStorage.StoreURLTestHistory(group.RealTag(detour), {Time: now, Delay: delay})`; при ошибке `DeleteURLTestHistory(realTag)` — паритет с Clash и групповым URLTest (история затем течёт в `OutboundGroupItem` для узлов в группах).

Клиентский метод — `experimental/libbox/command_client_command_lx.go`:
```go
func (c *CommandClient) URLTestOutbound(outboundTag, link string, timeoutMs uint32) (delay uint16, errMsg string, err error)
```
Worker-pool (параллельный масс-пинг N узлов, concurrency=10) живёт **в клиенте/LxBox**, не в ядре: ядро меряет один узел синхронно и stateless. Отмена масс-пинга = закрытие/реконнект CommandClient-соединения (рвёт conn → серверный `ctx` отменяется → in-flight тесты падают), как старый `cancelDelays`. Отдельный per-call cancel-handle НЕ вводится.

### 3.3 RPC #2 — `GetRules` (route + DNS)
Шов в `.proto` под тем же `// lx:`-маркером:
```proto
// lx:begin lx_command
rpc GetRules(google.protobuf.Empty) returns (RuleList) {}

message Rule {
  string type    = 1;   // rule.Type()
  string payload = 2;   // rule.String()
  string action  = 3;   // rule.Action().String()
  bool   isDNS   = 4;   // false = route-rule, true = dns-rule
}
message RuleList {
  repeated Rule rules = 1;
}
// lx:end lx_command
```
Handler (`daemon/started_service_command_lx.go`): снапшот (unary, правила статичны — стрим не нужен).
- **Route-rules:** `boxService.router.Rules()` ([adapter/router.go:21](../../adapter/router.go), уже публичный) → `{type, payload, action, isDNS:false}`. Поля — как Clash `rules.go`.
- **DNS-rules:** требуют **нового геттера** (см. §3.4) → `{..., isDNS:true}`. `adapter.DNSRule` встраивает `Rule` → те же `Type()/String()/Action()`.

### 3.4 Правка апстрим-интерфейса для DNS-rules (цена «route + DNS»)
`adapter.Router` НЕ выставляет DNS-правила; `dns.Router.rules []adapter.DNSRule` приватно ([dns/router.go:44](../../dns/router.go)), `adapter.DNSRouter` ([adapter/dns.go:18](../../adapter/dns.go)) геттера не имеет. Требуется **+2 тронутых апстрим-файла**, оба за `// lx:`-маркером:
- `adapter/dns.go` — в интерфейс `DNSRouter` добавить `Rules() []DNSRule`.
- `dns/router.go` — реализовать `func (r *Router) Rules() []adapter.DNSRule { return r.rules }`.

Это аудируемая цена по §3.1(а3): route-only её не несёт; «route + DNS» добавляет 2 маркированных шва. Clash DNS-правила не отдаёт — эталона нет, мы превосходим Clash.

### 3.5 Детерминированная регенерация proto (ОБЯЗАТЕЛЬНЫЙ deliverable, §3.6 п.5)
Сегодня генерация невоспроизводима: `make proto` ([Makefile](../../Makefile)) шеллит системный `protoc` из PATH, `proto_install` ставит `protoc-gen-go@latest` / `protoc-gen-go-grpc@latest`. На ребейзе `.pb.go` нельзя воспроизвести байт-в-байт.
- Добавить в `Makefile.lx` proto-таргет с **зафиксированными** версиями `protoc` и обоих плагинов (pin в команде/переменных).
- `*.pb.go` / `*_grpc.pb.go` — машинный вывод, руками не правятся, маркеров не несут; на ребейзе **регенерируются** из смерженного `.proto`, не мёржатся текстом.

### 3.6 CI-инвариант (§3.6 п.7)
В `lx-ci.yml` — проверка обеих сборок: **без** `with_lx_command` (компилируется, `*_stub.go` отдаёт `Unimplemented`, поведение = upstream) и **с** тегом (RPC обслуживается). Usbip-паттерн делает проверку дешёвой.

### 3.7 Что НЕ трогаем
- Существующий `rpc URLTest` (групповой) — **без изменений** (нулевой дифф в `started_service.go`).
- Data-path, серверная/inbound-логика — §3.1 client-only.
- `OutboundGroupItem` / стрим групп — история по-прежнему течёт туда; отдельный history-RPC НЕ вводится (delay возвращается синхронно из `URLTestOutbound`).
- `experimental/clashapi/` — эталон семантики, код не тянуть.

---

## 4. Критерии приёмки

1. Сборка с `with_lx_command`: `URLTestOutbound` меряет outbound И endpoint (AWG/WG); кастомный `link` и `timeout` применяются; `error`-поле заполняется по таблице §3.2; `delay==0 && error==""` трактуется как успех 0 мс.
2. `GetRules` возвращает route- и DNS-правила с `isDNS`-разделением; поля совпадают с Clash для route-части.
3. Сборка **без** `with_lx_command`: компилируется, оба RPC → `codes.Unimplemented`, `sing-box`/libbox по поведению эквивалентны upstream.
4. `Makefile.lx` proto-таргет регенерирует `*.pb.go` воспроизводимо (зафиксированные версии); сгенерированный код gofmt-чист и не содержит `// lx:`-маркеров.
5. CI зелёный на обеих сборках (§3.6).
6. `with_lx_command` в `sharedTags` (AAR) и `LX_TAGS` (desktop); `Libbox.version()` → `1.14.0-lx.N`.
7. Перечень тронутых общих файлов в SPEC совпадает с фактическим диффом (§3.6 п.8): `daemon/started_service.proto` (+регенерация `.pb.go`/`_grpc.pb.go`), `adapter/dns.go`, `dns/router.go`, `cmd/internal/build_libbox/main.go`, `Makefile.lx`, `lx-ci.yml`. Логика — в новых `*_lx.go`/`*_stub.go`.

---

## 5. Вне скоупа
- Отдельный history-RPC (`GetURLTestHistory`) — отклонён: delay синхронен, групповая история в стриме.
- Per-call cancel-handle / batch-RPC — отклонены: отмена через close conn, батч в клиенте.
- `timeout` в микросекундах / смена типа delay — отклонены: мс, `uint16`→`uint32`.
- DNS rule-set (headless) snapshot, server/inbound RPC — будущие SPEC при необходимости.

---

## 6. Ссылки
- CONSTITUTION §3.6 (класс libbox command-расширений), §3.1(а) (тройной тест).
- Эталон семантики: `experimental/clashapi/proxies.go` (per-node delay), `rules.go` (таблица правил).
- Образец гейтинга: `daemon/started_service_usbip{,_stub}.go`.
- Движок: `common/urltest/urltest.go`, хранилище `HistoryStorage`.
- Предыстория: v1.14.0-lx.1-rc.1 (`with_clash_api` dropped), memory `lx-commandclient-extensions-spec014`.
