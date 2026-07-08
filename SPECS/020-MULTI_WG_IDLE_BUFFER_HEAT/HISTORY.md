# HISTORY — SPEC 020 idle-suspend

Хронология: найденный баг, отвергнутые альтернативы, эволюция механизма. Актуальное состояние — в [SPEC.md](SPEC.md); первопричина нагрева и heap A/B — в [RESEARCH.md](RESEARCH.md).

---

## Баг: idle-тик был слеп к эндпоинтам (найден и исправлен)

**Симптом.** Первый live-прогон (selector-сценарий, 8 WG/AWG-эндпоинтов, `lx_idle_suspend: 8s`) дал **0 строк `lx idle:` за 2+ минуты простоя**, где ожидалось 7 suspend'ов. Бокс стартовал чисто, конфиг декодировался — фича просто была инертной.

**Первопричина (source-verified).** `idleSuspendLoop` перебирал `r.outbound.Outbounds()` и type-assert'ил каждый к `adapter.IdleSuspendable`. Но WG/AWG **эндпоинты живут в endpoint-менеджере, а не в outbound-менеджере**. `outbound.Manager.Outbounds()` возвращает ТОЛЬКО `m.outbounds` — НЕ включает `m.endpoint.Endpoints()`. (Контраст: `Outbound(tag)` специально делает fallback на `m.endpoint.Get(tag)` — поэтому *walk* достижимости резолвил теги эндпоинтов нормально, что и маскировало щель.) Список, по которому шёл тик, не содержал **ни одного** `IdleSuspendable` → `SuspendIfIdle` не вызывался никогда → фича мертва.

**Почему юниты были зелёными.** Две половины тестировались изолированно: walk (`reachability_lx_test.go`) и решение на эндпоинт (`endpoint_idle_lx_test.go`, прямой вызов `SuspendIfIdle`). Кэш-тест стабил `Outbounds()`→nil. **Ничто не проверяло, что петля реально ДОСТАЁТ эндпоинты** — этот интеграционный шов был слепым пятном.

**Фикс.** Router получает доступ к endpoint-менеджеру (`endpoint adapter.EndpointManager`, из ctx в `NewRouter` через `service.FromContext` — без правок `box.go`, менеджер там уже зарегистрирован) и перебирает его в тике. Тело петли вынесено в `suspendIdleEndpoints(reachable)`, сканирующий **оба** списка: `r.endpoint.Endpoints()` (где IdleSuspendable реально есть) и `r.outbound.Outbounds()` (для будущего не-endpoint IdleSuspendable; сегодня no-op). Nil-guard на `r.endpoint`. Семантика публичного `Outbounds()` не тронута.

**Регрессионный тест.** `route/idle_tick_endpoints_lx_test.go` гоняет `suspendIdleEndpoints` через стаб endpoint-менеджера, проверяет, что каждый эндпоинт посещён один раз с правильным `reachable`. **Падает на до-фиксовом коде** (`wg-1=0 wg-2=0` — тик слеп), проходит после. Тот самый шов, что пропустил исходный набор. См. [[spec020-idle-tick-misses-endpoints]].

Связанная находка: post-hoc профайлер §208 «лгал» про то, что видел hot-path (sticky-domain-empty), потому что router резолвил домен→IP до входа в группу. См. [[sticky-domain-empty-at-dial]].

## Почему Down, а не «лёгкий» timersStop (обоснование выбора механизма)

Рассматривался light-sleep (`timersStop` — остановить только per-peer таймеры, не трогая сокет/воркеры). **Отвергнут:** измерение в [RESEARCH.md](RESEARCH.md) доказало, что держатель GC-нагрева — recv-воркерные `bufsArrs`, а light-sleep их НЕ освобождает (экономит только батарею/таймеры). Реализован Down — он бьёт по **измеренному** держателю. Цена — handshake на пробуждении (см. SPEC.md § «Механизм Down/Up»); это сознательный компромисс ради нулевой recv-памяти спящей ноды. Source-verified разбор цены Down/Up — [[wg-suspend-cost-and-gc-source]].

## Эволюция путей: B реализован, A/Hybrid отложены

Три пути к «усыпить эндпоинт» (SPEC §13 ранних версий):
- **Путь B (Down) — РЕАЛИЗОВАН.** `device.Down()`: рвёт ключи → handshake на wake, но RAM спящей ноды = **ноль**. Проще и безопаснее. Ветка `lx-spec020-idle-suspend`, промоут в rc.19.
- **Путь A (BindUpdate, keys-safe) — ОТЛОЖЕН.** `Device.BindUpdate()` урезает bind на живом устройстве БЕЗ зануления ключей → wake без handshake. Но RAM ~0.5 МБ (не ноль) + три ловушки: мутабельный `BatchSize()`, надо резать и TUN BatchSize (иначе `max()` клампит обратно к 128), открывать с GRO off (иначе паника при batch<128). См. [[wg-bindupdate-keys-safe]].
- **Hybrid** — отложен вместе с A.

Путь A эскалируем по данным, если флап handshake'ов на пробуждении реально начнёт мешать.

## ОТВЕРГНУТО: эксперимент «GRO off + batch 8»

Вопрос: нужен ли вообще Down/Up, если глобально урезать recv-batch (128→8) — тогда `bufsArrs` малы у ВСЕХ нод всегда, без suspend/wake, без reachability-walk, без handshake. **Замерили. Отвергнуто по трём независимым причинам.** Артефакты: `ANDROID_RESEARCH/nogro-experiment/` (`RESULT-2026-07-01.md` + heap); код на ветках `lx-1.14-nogro-exp`/`lx-1.14-nogro-hard` (сабмодуль `lx-awg2-v003-nogro-*`), в prod НЕ вливается.

**Причина 1 — не тот держатель (главное, меняет постановку).** On-device heap показал: основной RAM-холдер на android — `device.pool.messageBuffers` (`pools.go`, `PreallocatedBuffersPerPool = 4096 × ~64 КБ ≈ 100 МБ`, `PopulatePools.func3`, 82–86% кучи), и он **от `BatchSize()` НЕ зависит**. batch влияет только на `bufsArrs` recv-воркеров (в том прогоне ~14 МБ). Даже при успешном batch 128→8 основные ~100 МБ остались бы. Настоящий рычаг RAM на android, если понадобится — **`PreallocatedBuffersPerPool`**, а не GRO/batch. (Down/Up при этом всё равно освобождает ВСЮ recv-инфраструктуру спящей ноды — потому и работает.)

> **⚠️ Коррекция RESEARCH.md.** Ранний вывод «GC = netstack» и «держатель = bufsArrs» уточнён этим замером: главный RAM-держатель — `messageBuffers`-пул (batch-независимый), `bufsArrs` — вторичный. Down/Up освобождает обе recv-структуры выходом воркеров, поэтому механизм остаётся верным независимо от того, какой из двух держателей доминирует. См. [[android-cpu-heat-multi-wg-gc]], [[wg-suspend-cost-and-gc-source]].

**Причина 2 — переключатель не доставляется на android.** env-канал `LX_WG_NO_GRO` через `wrap.<pkg>` prop: переменная видна в `/proc/<pid>/environ`, но `os.Getenv` в Go-рантайме libbox её НЕ видит (Go снимает `environ` при инициализации `.so`, до/мимо prop-инъекции). Пришлось хардкодить `lxNoGRO()=true`. См. [[android-goos-linux-file-suffix]].

**Причина 3 — хрупкая связка, паника.** Хардкод batch=8 крашил туннель при старте (**SIGABRT**): `device.BatchSize() = max(bind, tun)` — Linux-TUN через vnet-hdr offload всё равно отдавал `IdealBatchSize=128`, `max()`→128, урезанный `msgsPool`=8 → `Send`: `(*msgs)[:len(bufs)]` = `[:128]` при cap 8 → out-of-range. Чинить пришлось гейтингом ЕЩЁ и TUN-offload (vnetHdr off, batchSize=1) — согласованно три слоя сабмодуля (bind + msgsPool + tun), ровно ловушка `max(bind,tun)` из [[wg-bindupdate-keys-safe]]. Фикс на ветке `-hard`, НЕ проверен на устройстве — эксперимент свернули.

**Вывод.** Путь «GRO off + batch» — сложный, недоставляемый штатно, и бьёт не в тот держатель. Down/Up остаётся единственным жизнеспособным механизмом: измеренно освобождает recv-инфраструктуру, не жертвует GRO активной ноды.

## Хронология релизов

- Реализация фичи — ветка `lx-spec020-idle-suspend`, база lx-1.14.
- rc.18 — Android device-verification (CPH2411): heap A/B подтвердил `bufsArrs` −60% на целевой платформе.
- rc.19 — промоут Down/Up-модели.
- Стабильный `v1.14.0-lx.1` срезан как non-prerelease 2026-07-02 (rc-линия rc.1–22 закрыта промоутом). См. [[wg-1.14-migration-is-submodule-rebase]].
