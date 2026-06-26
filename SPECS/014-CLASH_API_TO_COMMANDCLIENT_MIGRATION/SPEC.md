# SPEC: 014 — CLASH_API_TO_COMMANDCLIENT_MIGRATION

| Поле | Значение |
|------|----------|
| Тип | F (feature) — смена канала управления ядром (client-side) |
| Статус | A (accepted) — `with_clash_api` drop в `v1.14.0-lx.1-rc.1`; box.go-фикс в `v1.14.0-lx.1-rc.3` |

**Переезд управления ядром с Clash API на нативный libbox CommandClient.** LxBox перестаёт использовать Clash REST API и переходит на нативный gRPC-канал `StartedService` (поверх unix-сокета). Из сборки убирается `with_clash_api` — отпадает HTTP-сервер Clash и связанный attack surface.

Этот SPEC фиксирует **сам переезд и его последствия**. Доработки command-протокола, понадобившиеся, чтобы CommandClient заменил Clash API по функциональности (per-node delay, таблица правил, pull-снапшоты групп, фикс потери групп), вынесены в отдельный **[SPEC 015 — COMMAND_PROTOCOL_RPC_EXTENSIONS](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md)**.

Scope: **client/command-сторона only** (§3.1 client-only).

---

## 1. Проблема / контекст

Графические клиенты sing-box исторически управляли ядром через два канала: Clash REST API (`experimental.clash_api`, под build-tag `with_clash_api`) и нативный libbox **CommandClient**. Clash API — это слой совместимости со сторонними дашбордами; для **своего** клиента на устройстве разработчики предполагают именно CommandClient (gRPC поверх локального unix-сокета).

LxBox переходит на CommandClient как единственный канал управления, потому что:
- это нативный, более богатый интерфейс (closed-connections история, per-event дельты соединений, ProcessInfo раздельными полями, NQ/STUN/Tailscale-инструменты) — то, что Clash REST не покрывает;
- Clash API — это лишний HTTP-сервер в процессе и открытый локальный порт (attack surface), не нужный, когда клиент ходит по нативному каналу;
- убрав `with_clash_api`, мы уменьшаем дифф и размер AAR.

---

## 2. Цель

LxBox управляет ядром **только** через CommandClient; `with_clash_api` не входит в сборку. Конфиг, ссылающийся на `experimental.clash_api`, fail-fast с понятной ошибкой (а не молчаливо деградирует). Функциональный паритет с Clash API по нужным UI возможностям достигается доработками CommandClient — см. [SPEC 015](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md).

---

## 3. Требования

### 3.1 Дроп `with_clash_api`
- Убрать `with_clash_api` из `sharedTags` ([cmd/internal/build_libbox/main.go](../../cmd/internal/build_libbox/main.go), `// lx:`-блок) и из десктоп `LX_TAGS` (`Makefile.lx`).
- Без тега подключается `include/clashapi_stub.go` — конфиг с `experimental.clash_api` получает `clash api is not included in this build, rebuild with -tags with_clash_api` (fail-fast, **не** молчаливый отказ).
- lx-конфиги `clash_api` не используют — управление идёт через CommandClient.
- Сделано в `v1.14.0-lx.1-rc.1` (commit `57b5b5e5`).

### 3.2 Доработки CommandClient → SPEC 015
Нативный CommandClient беднее Clash API по ряду возможностей, нужных UI (per-node delay-тест, таблица правил, pull-снапшоты групп/узлов, баг потери одно-узловых групп). Все эти доработки — **в [SPEC 015](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md)** (класс §3.6, build-tag `with_lx_command`). `URLTestOutbound` и `GetRules` уже зашиплены (rc.2); `GetGroups`/`GetOutbounds` + фикс `len<2` — target rc.4. Здесь они только упоминаются как часть полного перехода; тех-спека — в 015.

### 3.3 Follow-on фикс — Android start fatal от `with_clash_api`-дропа (rc.3)
Удаление `with_clash_api` (§3.1) сделало **любой старт на Android фатальным**:
`create clash-server: clash api is not included in this build` — **даже без**
`clash_api` в конфиге. Корень в апстрим-`box.go`: `PlatformLogWriter != nil`
(всегда на Android/libbox) форсил `needClashAPI = true` (Clash-сервер исторически
был единственным наблюдателем логов/трафика), а `needClashAPI` ведёт к
`NewClashServer()`. Десктоп не затронут (`PlatformLogWriter == nil`).

**Фикс (`box.go`, `// lx:` шов, commit `029acd11`, пререлиз `v1.14.0-lx.1-rc.3`):**
расщепить заботы — `PlatformLogWriter` взводит новый `needObservable`
(`= needClashAPI || needAPIService || PlatformLogWriter != nil`) для Observable
log factory + traffic/connection-tracker; **только** явный `experimental.clash_api`
по-прежнему взводит `needClashAPI` → `NewClashServer`. daemon уже nil-safe к
отсутствию `clashServer` → Clash-mode деградирует мягко. Проверено: стартует без
`clash_api`; всё ещё fail-fast с ним. **Device-verified** на реальном устройстве
(Android-старт работает на настоящем rc.3).

**WATCH — апстрим issue [SagerNet/sing-box#4240](https://github.com/SagerNet/sing-box/issues/4240)**
(подан как чистый upstream-репро, форк НЕ упомянут). Когда апстрим починит —
**снять наш `// lx:` шов в `box.go`** на следующем ребейзе.

> ⚠️ **#4240 УДАЛЁН** (по состоянию на 2026-06-26 GitHub API отдаёт `HTTP 410
> "This issue was deleted"`). Удаление ≠ fix и ≠ closed/not-planned — это НЕ
> сигнал «можно снимать шов». Issue-ссылка как критерий снятия больше
> непроверяема. **Новый критерий снятия шва:** сверять не статус issue, а сам
> upstream-код — починили ли `PlatformLogWriter`-путь, который форсил
> Clash-сервер (Android-старт без `with_clash_api`). До подтверждения в коде —
> живём на своём `// lx:` фиксе. (Аккаунт НЕ забанен: #3858/#3806 closed как
> `completed`, #4093 + PR #4094 живут; удаление #4240 — отдельная история, не
> бан.)

---

## 4. Критерии приёмки

1. (✅ rc.1) Сборка без `with_clash_api`: компилируется; конфиг с `experimental.clash_api` → fail-fast с понятной ошибкой; lx-конфиги стартуют.
2. (✅ rc.3) Android стартует на сборке без `with_clash_api` (`PlatformLogWriter` не форсит Clash-сервер); десктоп не затронут; device-verified.
3. Функциональный паритет с Clash по нужным UI возможностям — критерии в [SPEC 015](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md).
4. Тронутые файлы переезда: `cmd/internal/build_libbox/main.go` (`// lx:`, дроп тега), `Makefile.lx` (LX_TAGS), `box.go` (`// lx:` шов §3.3).

---

## 5. Вне скоупа
- Тех-спека RPC-доработок CommandClient — в [SPEC 015](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md).
- Возврат `with_clash_api` ради delay-тестов (гибрид) — отклонён: паритет достигается нативно (SPEC 015).

---

## 6. Ссылки
- [SPEC 015 — COMMAND_PROTOCOL_RPC_EXTENSIONS](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md) — доработки CommandClient до уровня Clash-паритета.
- CONSTITUTION §3.5 (дистрибуция, `with_lx_command` в AAR), §3.6 (класс command-расширений).
- `include/clashapi_stub.go` (fail-fast при отсутствии тега); `box.go` (needClashAPI/needObservable).
- Память: `lx-commandclient-extensions-spec014`.
