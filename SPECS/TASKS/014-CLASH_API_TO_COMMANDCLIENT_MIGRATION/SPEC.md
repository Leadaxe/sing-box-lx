# SPEC: 014 — CLASH_API_TO_COMMANDCLIENT_MIGRATION

**Фича:** [OBSERVABILITY](../../FEATURES/OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — смена канала управления ядром (client-side) |
| Статус | C (complete) — `with_clash_api` drop из AAR в `v1.14.0-lx.1-rc.1`; box.go-фикс в `rc.3`; десктоп-регрессия исправлена в `rc.17` (§3.4) |

**Переезд управления ядром с Clash API на нативный libbox CommandClient — на Android.** LxBox перестаёт использовать Clash REST API и переходит на нативный gRPC-канал `StartedService` (поверх unix-сокета). Из **AAR-сборки** убирается `with_clash_api` — отпадает HTTP-сервер Clash и связанный attack surface. **Десктоп/CLI сохраняют `with_clash_api`** (внешние дашборды ходят по Clash REST API; нативного CommandClient-канала у CLI нет) — см. §3.4.

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

LxBox (Android) управляет ядром **только** через CommandClient; `with_clash_api` не входит в **AAR-сборку**. Конфиг, ссылающийся на `experimental.clash_api`, fail-fast с понятной ошибкой (а не молчаливо деградирует). Функциональный паритет с Clash API по нужным UI возможностям достигается доработками CommandClient — см. [SPEC 015](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md).

> **Важно (исправлено):** дроп `with_clash_api` относится **только к Android AAR**. Десктоп/CLI-бинари (mac/windows/linux) управляются внешними дашбордами (yacd/MetaCubeXD) **именно через Clash REST API** — нативного CommandClient-канала вне gomobile/libbox у них нет. Поэтому `with_clash_api` **остаётся** в десктоп `LX_TAGS`. Изначально (rc.1) тег был ошибочно убран и из десктоп-набора тоже — см. §3.4.

---

## 3. Требования

### 3.1 Дроп `with_clash_api` — **только Android AAR**
- Убрать `with_clash_api` из `sharedTags` ([cmd/internal/build_libbox/main.go](../../../cmd/internal/build_libbox/main.go), `// lx:`-блок). **Десктоп `LX_TAGS` (`Makefile.lx`) тег сохраняет** — см. §3.4.
- Без тега (в AAR) подключается `include/clashapi_stub.go` — конфиг с `experimental.clash_api` получает `clash api is not included in this build, rebuild with -tags with_clash_api` (fail-fast, **не** молчаливый отказ).
- lx-конфиги на Android `clash_api` не используют — управление идёт через CommandClient.
- Сделано в `v1.14.0-lx.1-rc.1` (commit `57b5b5e5`) — но изначально ошибочно срезано и с десктопа, исправлено в §3.4.

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

### 3.4 Фикс — `with_clash_api` ошибочно срезан и с десктопа (rc.17)

§3.1 в rc.1 убрал `with_clash_api` из **обоих** наборов тегов: и из `sharedTags`
AAR, и из десктоп `LX_TAGS` (`Makefile.lx`). Для AAR это верно (LxBox ходит по
CommandClient). **Для десктопа — ошибка:** mac/windows/linux-бинарь запускается как
CLI и управляется внешними дашбордами (yacd, MetaCubeXD, clash-dashboard)
**исключительно через Clash REST API** — нативного CommandClient-канала вне
gomobile/libbox у CLI нет. Без `with_clash_api` десктоп-юзер остался **без способа
управлять ядром**: конфиг с `experimental.clash_api` падает fail-fast.

Все релизные desktop/linux-musl сборки берут теги из
`make -f Makefile.lx -s lx-print-tags` ([lx-release.yml](../../../.github/workflows/lx-release.yml)),
поэтому баг ушёл во все desktop-артефакты rc.1…rc.16 молча (CI-проверка
`lx-ci.yml BASE_TAGS` clash_api держала, так что компиляция была зелёной — баг
не виден в CI, только в релизном артефакте).

**Фикс:** вернуть `with_clash_api` в десктоп `LX_TAGS` (`Makefile.lx`). AAR-набор
(`build_libbox`) **не трогаем** — там дроп остаётся в силе. Так два набора тегов
расходятся **по дизайну**: desktop = с Clash API, AAR = без. Десктоп-сборки снова
управляются через Clash REST API из коробки.

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
