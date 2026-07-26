# SPECS — sing-box-lx (Spec Kit)

Два уровня документации:

| Каталог | Уровень | Содержимое |
|---------|---------|------------|
| **[FEATURES/](FEATURES/README.md)** | Крупные блоки | Фича целиком — как она устроена сейчас, поверх всех задач, которые её строили |
| **[TASKS/](TASKS/)** | Единицы работы | Папки `NNN-NAME` — конкретная задача: фича, баг или исследование |

Задача — единица работы (её делают и закрывают). Фича — то, что живёт
в продукте и складывается из нескольких задач.

Фичи двух родов: **продуктовые** (AWG2, XHTTP, балансировка, наблюдаемость,
MASQUE, энергосбережение, VLESS-шифрование, DNS-группа) и **процессные** — постоянная
работа форка поверх чужой базы: чинить за апстримом (`HOTFIXES`),
синхронизироваться с ним (`UPSTREAM_SYNC`), собирать и выпускать
(`BUILD_CI_CD`), проверять себя (`AUDITS`), исследовать (`RESEARCH`).

**Ссылаться лучше на фичу, а не на задачу** — фича переживает переномерацию
и рефакторинг. Ссылка на задачу уместна, когда нужен конкретный разбор
внутри фичи. Каждая задача несёт обратную ссылку `**Фича:**` под заголовком.

## Задачи: `TASKS/NNN-NAME`

Внутри: SPEC.md → PLAN.md → TASKS.md → IMPLEMENTATION_REPORT.md.

| Часть | Значение | Расшифровка |
|-------|----------|-------------|
| **NNN** | 001, 002, … | Сквозной номер — **стабильный якорь**, не меняется никогда |
| **NAME** | UPPER_SNAKE | Название |

Имя папки **не несёт** тип/статус: они меняются по ходу задачи, а имя должно
оставаться стабильным (на него ссылаются из кода, доков и сабмодуля). Ссылки
давай короткой формой `SPECS/TASKS/NNN-NAME` — она переживёт смену статуса.

## Тип и статус — в шапке SPEC.md + Roadmap

Источник правды по типу/статусу — **таблица-шапка в начале `SPEC.md`** каждой
задачи; [Roadmap](#roadmap-план-задач) ниже её агрегирует.

```markdown
| Поле | Значение |
|------|----------|
| Тип | F (feature) \| B (bug) \| Q (question/исследование) |
| Статус | N (new) \| O (open) \| W (wait) \| C (complete) |
```

## Файлы внутри папки

| Файл | Назначение |
|------|------------|
| **SPEC.md** | Что и зачем — проблема, требования, критерии приёмки |
| **PLAN.md** | Как строить — архитектура, изменяемые файлы, зона касания upstream |
| **TASKS.md** | Чеклист по этапам |
| **IMPLEMENTATION_REPORT.md** | Отчёт после реализации |
| **HISTORY.md** | Хронология: как делали раньше, почему переделали. Только при смене архитектуры фичи (см. правило структуры ниже) |

### Структура SPEC.md — актуальное состояние сверху, НЕ хронология

**Правило:** SPEC.md описывает ТЕКУЩУЮ (актуальную) архитектуру фичи в первую очередь. Порядок разделов — от актуального состояния к деталям, НЕ по хронологии разработки.

- Верх SPEC.md = как фича устроена СЕЙЧАС (актуальная архитектура), затем детали/контракт/критерии.
- НЕ вести SPEC.md как дневник («сначала сделали так, потом в rc.9 исправили…»). Пометки вида «ИСПРАВЛЕНО в rc.N», «прежнее утверждение неверно», отвергнутые подходы — НЕ в SPEC.md.
- Когда архитектура фичи МЕНЯЕТСЯ (переделали механизм): SPEC.md переписывается под новое состояние, а старое состояние + обоснование смены (как делали, почему это оказалось неверно, почему выбрали новое) выносятся в **HISTORY.md** этой же папки.
- Читатель SPEC.md должен понять, как всё работает СЕЙЧАС, не продираясь через историю решений. История — по запросу, в HISTORY.md.

## Конфигурация фич

Пользовательский конфиг XHTTP и AmneziaWG 2.0 (поля + примеры) — **[../docs-lx/lx-config.md](../docs-lx/lx-config.md)**.

## Корень SPECS

| Файл | Назначение |
|------|------------|
| **CONSTITUTION.md** | Неизменяемые принципы, приоритеты, запреты |
| **IMPLEMENTATION_PROMPT.md** | DoD, git/ребейз-ритуал, контракт выхода |

## Методология: фича → задачи → реализация

**Порядок работы — сверху вниз. Сначала фича, потом задачи.**

Фича не пишется постфактум как сводка сделанного. Она пишется **первой** —
до задач и до кода — и фиксирует полный скоуп: что система должна делать,
чем она управляется, что принимает и что отдаёт. Только затем скоуп режется
на задачи, и только затем пишется код.

### 1. Фича — полный скоуп (до задач)

`FEATURES/<NAME>/FEATURE.md` описывает систему **как чёрный ящик**:

| Раздел | Содержание |
|--------|------------|
| **Назначение** | Что фича даёт пользователю и зачем существует |
| **Контролируемые параметры** | Все ручки управления: конфиг-ключи (тип, значения, дефолт), build-теги, переменные окружения |
| **Входы** | Что ящик принимает: конфиг, трафик, внешние события |
| **Выходы** | Что отдаёт: провод, RPC/UI, наблюдаемое поведение |
| **Data flow** | Путь данных через ящик — от входа до выхода, в терминах стадий, не файлов |
| **Правила и гарантии** | Инварианты, взаимоисключения, fail-fast, что валидируется и когда |
| **Границы** | Что фича намеренно **не** делает; ограничения платформ |

### 2. Разбиение на задачи

Скоуп фичи режется на задачи `TASKS/NNN-NAME` — каждая берёт свой кусок
и ссылается на фичу строкой `**Фича:**` под заголовком. Фича при этом
получает задачу в свою таблицу «Задачи фичи».

### 3. Реализация

По задаче: SPEC.md → PLAN.md → TASKS.md → код, с учётом
IMPLEMENTATION_PROMPT и CONSTITUTION. Заканчивается IMPLEMENTATION_REPORT.md,
DoD-чеклистом и статусом `C`.

### 4. Поддержание фичи

`FEATURE.md` — **живой дизайн системы**, а не архив. Меняется поведение,
параметр или гарантия → правится фича, а не только задача. Действует то же
правило, что для SPEC.md: описывается **текущее** состояние, без хронологии.

### ⚠️ Главное правило: фича отвечает ЧТО, а не КАК

В `FEATURE.md` **не место** именам файлов, функций, полей структур, пакетов
и слоёв кода. Реализация — в задачах (`PLAN.md`), фича описывает наблюдаемое
поведение и контракт.

Критерий проверки: **если реализацию завтра переписать с нуля, FEATURE.md
не должен измениться — пока не изменилось поведение.**

| ❌ КАК (реализация) | ✅ ЧТО (контракт) |
|---|---|
| «Sticky-ключ читает `metadata.Domain`, а не `destination.Fqdn`, потому что роутер перезаписывает `metadata.Destination`» | «Закрепление считается от домена исходного запроса, а не от резолвленного IP — смена IP узла закрепление не срывает» |
| «Слой `transport/wireguard/masque_awg.go` генерирует `i1` из полей `option.AmneziaWGOptions`» | «`id`/`ip`/`ib` и явный `i1` взаимоисключаются: конфиг с обоими отвергается до старта туннеля» |
| «Тик роутера обходит `Endpoints()`, а не `Outbounds()`, тянет `EndpointManager` из ctx» | «Засыпанию подлежат все WG/AWG-узлы профиля независимо от способа объявления» |

Что **остаётся** в фиче, несмотря на близость к коду: **конфиг-ключи**
(это пользовательский контракт), **build-теги** (внешняя ручка сборки),
**имена RPC и полей протокола** (контракт с клиентом).

Процессные фичи (`HOTFIXES`, `UPSTREAM_SYNC`, `BUILD_CI_CD`, `AUDITS`,
`RESEARCH`) под шаблон чёрного ящика не подпадают — у них нет конфига
и провода. Их форма — реестры и watchlist, см. каждую.

## Roadmap (план задач)

| # | Задача | Статус | Суть |
|---|--------|--------|------|
| **001** | FORK_BOOTSTRAP | **C** | Remotes, ветка `lx`, `Makefile.lx`, версия `-lx` (ldflags), CI-скелет, `lx-test/config` — ✅ собрано/проверено |
| **002** | XHTTP_CLIENT_TRANSPORT | **C** | ✅ **live-validated** против Xray (3x-ui): packet-up/auto работают (handshake+DNS+HTTPS+download); stream-one — был баг, **исправлен в 011** |
| **003** | AWG2_CLIENT_ENDPOINT | **C** | ✅ **Функционален, проверен живым AWG2-сервером** (handshake+keepalive+трафик). merged-форк Leadaxe/wireguard-go (sagernet+обфускация) через submodule; S1–S4/H1–H4/I1–I5 |
| **004** | BUILD_CI_RELEASE | **C** | ✅ `Makefile.lx`/libbox-теги, дешёвый CI (lint+build-check на push; cross×6+AAR на dispatch), `lx-release.yml` (**релиз v1.13.13-lx.3 опубликован** — 6 desktop + 2 AAR), поставка libcronet: dll в windows-архивах (purego), darwin — CGO-статика на macos-раннере (naive работает), `lx-rebase.yml` (авто-ребейз → PR/issue, демо зелёное) |
| **005** | AWG2_RANGED_MAGIC_HEADERS | **C** | ✅ **Проверено живым awg2-сервером с ranged-конфигом** (handshake+трафик). Диапазонные `H1`–`H4` (`"N-M"`) из awg2-экспортов: `option.MagicHeader` (number\|string) → spec-строка в IpcSet; vendored wireguard-go уже умел |
| **006** | LINUX_MUSL_STATIC_ROUTER_BUILDS | **C** | ✅ **CI-приёмка 4/4 арки статикой** (amd64/arm64/armv7/mipsle-softfloat, `statically linked`, libdl=0, naive сохранён). musl-сборки под роутеры по подобию upstream build.yml (cronet-go + Chromium musl-toolchain, `with_musl`). Чинит [#1](https://github.com/Leadaxe/sing-box-lx/issues/1) (`libdl.so.2` на AsusWRT + armv7). CI-only, без Go-кода |
| **007** | AWG_OVER_WIREGUARD_DETOUR_GUARD | **C** | ⛔️ **Guard СНЯТ (lx.11, 2026-07-18).** Первопричина ушла на новом графте (re-graft AWG2 на wireguard-go v0.0.5 + SPEC 025 padding + SPEC 026 reserved-clear): AWG-over-WireGuard больше не вешает ядро, поднимается и несёт трафик (e2e mac-стенд: AWG s4=12/ranged h1..h4 через плоский WG, handshake+keepalive+HTTP ок). Удалены оба guard'а (Start-guard + selector-guard), adapter-хуки `ConsumersOf`/`AmneziaWGSuspendable` и все guard-тесты; SPEC 020 idle-suspend сохранён. Осталось: Android field-тест (старый hang был Android-only). App-side §130-гейт LxBox снят синхронно. Исторический контекст — в [SPEC.md](TASKS/007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md). Was [#2](https://github.com/Leadaxe/sing-box-lx/issues/2) |
| **008** | AWG_JUNK_PARAM_VALIDATION | **C** | ✅ **Код+тесты+DoD**. Bug в 003 (найден при 007): `jmin > jmax` паникует `rand.Int` в amneziawg-go (краш в timer-горутине). `validateJunk` в `awgIpcLines` отвергает на уровне конфига (`check`/старт), без паники. Узко — только краш-кейс; jc-несогласованность осознанно не трогаем (минимальный дифф, совместимость). Чинит [#3](https://github.com/Leadaxe/sing-box-lx/issues/3) |
| **009** | WIRESOCK_MASQUERADE_PROFILES | **C** | ✅ **Код+тесты+DoD; механизм проверен вживую (туннель + трафик на 009), релиз `v1.13.13-lx.11`.** WireSock-стиль `id`/`ip`/`ib` (домен/протокол/браузер) — декларативный сахар над `I1` CPS. Профили **quic** (1-RTT short header) / **dns** (EDNS OPT response) / **stun** (Binding Success Response) / **sip** (200 OK response), структуры портированы из open-source WireSock `amneziawg-proxy/src/transform.rs` (MIT). Механизм — **I1 only** (S1–S4 невозможен против WARP, сабмодуль не трогаем). `id` обязателен только для dns/sip (там идёт на провод), для quic/stun опционален. Строгая LDH-валидация домена (security-граница: инъекция в SIP/DNS). `ib` — без JA3-fingerprint (честно задокументировано). Все профили приняты реальным `newObfChain`; `sing-box check` зелёный; адверсариальный ревью (6 агентов) — 0 находок |
| **010** | WG_ENDPOINT_GRO_SPLIT_BRAIN | **C** | ✅ **Корень подтверждён на железе, фикс верифицирован** (download 0.44→20.7 Mbps), вмержен в `lx` (lx.14). Bug: WG-**endpoint** без `detour` на Android режет download (GRO split-brain — UDP_GRO включён, а receive-путь linux-only). Фикс — гейт `UDP_GRO` за `!android` в сабмодуле `wireguard-go` (`conn/`). UDP/WG-only |
| **011** | XHTTP_STREAM_ONE_DOWNLINK | **C** | ⚠️ **Принято на синтетике; лайв НЕ прогонялся** (нет reality+xhttp ноды). Bug в 002 (жалоба): `vless+reality+xhttp+mode:auto` не работал. Корень (сверено с Xray + [issue #5635](https://github.com/XTLS/Xray-core/issues/5635) + hiddify): stream-one слал `<path>/<sessionId>`, а Xray-сервер роутит stream-one только при пустом sessionId → downlink не-VLESS → `unknown version`. Фикс: голый путь без sessionId; `mode:auto`+reality → stream-one (детект reality по имени типа, без with_utls-зависимости). Юнит-тесты (URL-layout + reality-детект), `check`, сборки зелёные. Ветка `lx-xhttp-streamone`, **в `lx` не влито**; лайв — открытый TODO в REPORT |
| **012** | TCP_DOWNLINK_STALL_ZOMBIE_CONNS | **C** | ⚠️ **НЕ воспроизводится на lx.14** (статус закрытия — «not reproducible»). Симптом (↑517 ↓0, WhatsApp/Telegram «висят») наблюдался на РАЗНЫХ нодах, включая WG → зонтик над «↓0»-сталлом, не один баг. WG-долю закрыл [010](TASKS/010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (UDP/WG-only). Для не-WG (VLESS/reality) отдельного код-фикса нет — симптом сейчас не воспроизводится без подтверждённого объяснения. Артефакты: зонд `LX_CONN_TRACE` (не прогнан в бою), [PROBE.md](TASKS/012-TCP_DOWNLINK_STALL_ZOMBIE_CONNS/PROBE.md) |
| **013** | PACKAGE_NAME_REGEX_RULE_ITEM | **C** | ✅ **Код+тесты, сборки/vet/gofmt зелёные.** Бэкпорт апстрим-фичи 1.14 ([941ce58b](https://github.com/SagerNet/sing-box/commit/941ce58b)) на базу 1.13.13 **без** полной миграции: rule-item `package_name_regex` (regex-матчинг Android-пакета) для route/DNS/headless. Новый `route/rule/rule_item_package_name_regex.go` + поля/регистрация/cond в 6 файлах роутинга. Хунк `RuleSetVersion5` из коммита НЕ переносился (это rule-set v5, не фича). Полная миграция на 1.14 отложена до **v1.14.0 stable** (feasibility: ~1,5–2 дня, риск — ребейз AWG-подмодуля; `lx-rebase.yml` сам исключает alpha) |
| **014–021** | *(см. шапки в `SPECS/NNN-*/SPEC.md`)* | **C** | Command-протокол RPC (014/015), connections-мьютекс (016), Connection.Detour (017), DNS-query-стрим (018), URLTest пул/sticky (019), multi-WG idle-suspend (020), MASQUE CONNECT-IP outbound (021). Источник статуса — шапка каждого SPEC.md |
| **023** | MUSL_TOOLCHAIN_MIRROR | **C** | 🛠 **Durable-зеркало Chromium musl-тулчейна.** `snapshot.debian.org` периодически 503-ит и блокирует релиз (`v1.14.0-lx.2-rc.1` падал дважды на нём; `actions/cache` промахивается из-за ref-scoping тегов). Producer-workflow (`lx-musl-toolchain-mirror.yml`) заливает собранный тулчейн 4 арок в release-ассет `musl-toolchain-cache`; `lx-release.yml` восстанавливает его на cache-miss ДО фолбэка на snapshot. Источники: actions/cache → lx-mirror → snapshot. Обе workflow lx-owned, upstream-дифф нулевой. Зеркало заполнено, restore проверен |
| **022** | LX_DEEP_AUDIT | **C** | 🔍 **Аудит-исследование + ремедиация.** Многоагентный аудит всей LX-дельты по 10 осям + адверсариальная верификация каждой находки. 32→**27 подтверждено** (0 critical, 1 high, 1 medium, 6 low, 19 nit), 5 опровергнуто. **Исправлено 24/27** (ветка `lx-spec022-audit-fixes`): P0 [#1](TASKS/022-LX_DEEP_AUDIT/SPEC.md) masque h2 CONNECT висел навечно (ctx игнорировался); P1 #2 idle-suspend воскрешал guard-suspended AWG (hang ядра Android); + все P2/P3. Осознанно пропущены #12/#17/#18 (санкционированные компромиссы / ребейз-цена). Реестр + ремедиация в [SPEC.md](TASKS/022-LX_DEEP_AUDIT/SPEC.md) |
| **024** | RUNTIME_LOOP_GUARD | **DEFERRED** | 🔁 **Guard от runtime-колец в detour/selector.** Статическое кольцо ядро отклоняет на старте (`circular outbound dependency`); собранное в рантайме через `SelectOutbound` — **нет** защиты → `fatal stack overflow`, падение процесса. Проработана событийная модель E1–E5 + `topologyMu`-линеаризация гонки; адверсарская проверка (7 агентов): deadlock/false-positives **HOLDS**, но TOCTOU **BREAKS** — даже полный ядерный guard негерметичен без покрытия endpoint-mgr/Remove/history-side-channels и без устранения pointer↔tag расхождения после рантайм-`Create`. **Решение (2026-07-06): защита на уровне UI (LxBox), ядро не трогаем.** Design record: [SPEC.md](TASKS/024-RUNTIME_LOOP_GUARD/SPEC.md) |
| **026** | AWG_MAGIC_VS_RESERVED_CLEAR | **C** | 🎭 **Reserved-clear уничтожал AWG magic.** Байты 1-3 (Cloudflare WARP reserved) обнулялись БЕЗУСЛОВНО на каждом принятом пакете во всех bind'ах. AWG читает magic как `Uint32(packet[padding:])` — при малом `s1/s2/s4` (0-3) magic пересекает байты 1-3 → обнуление рушит его → пакет (вкл. handshake) дропается → нода не встаёт. Затрагивало ОБА пути (StdNetBind без detour + ClientBind с detour). Обычный WG и AWG с padding≥4 (issue #8: s1=15) не задеты. Фикс: гейт `hasReserved()` на всех 5 receive-clear (linux/darwin/windows/detour) — обнулять только при заданном WARP-reserved. e2e red/green (padding=0). [SPEC.md](TASKS/026-AWG_MAGIC_VS_RESERVED_CLEAR/SPEC.md) |
| **030** | FAST_BOX_SHUTDOWN | **C** | ⏱️ **`box.Close()` виснет 10с+ на Android при остановке ~30 пропинганных WG/AWG-нод.** `box.Close` рвал endpoints, пока idle/urltest-тик ещё слал wake-пинги → каждый `Endpoint.Close()` блокировался на `resumeMu`, ожидая, пока пинг-разбуженный `resumeOnDial` доделает полный device-rebuild + handshake (~0.5–5с), суммируясь последовательно по всем нодам. Аудит (3 агента+синтез): доминанта — resumeMu×wake-сумма, НЕ worker-drain. Фикс (4 шага, drain НЕ трогаем — иначе use-after-free gVisor netstack): (1) `Router.QuiesceForShutdown` в начале `box.Close` — стоп тика + `DevicePause` broadcast закрывает все UDP-сокеты вперёд; (2) гейт `closing` в WG-endpoint прерывает in-flight wake; (3) сокеты закрыты → `stopping.Wait` мгновенный; (4) параллельное закрытие (`task.Group` Concurrency 8, все джойнятся). 10с+ → доли секунды, ничего не бросается недозакрытым. Юниты гейта red/green + e2e-smoke (20 нод ~5мс). Дополняет клиентский таймаут LxBox вокруг `closeService()`. Остаток: field. [SPEC.md](TASKS/030-FAST_BOX_SHUTDOWN/SPEC.md) |
| **029** | ENDPOINT_DETOUR_START_ORDER | **C** | 🔗 **WG/AWG-endpoint с `detour` навсегда мёртв, если провайдер detour объявлен в конфиге ПОЗЖE потребителя.** Резолв detour утекал в конструктор `NewEndpoint` (egress-anchor-каст SPEC 020 идёт по `Upstream()`, `DetourDialer.Upstream()` жадно резолвит) — в фазе Create, до сборки графа. Endpoints создаются в порядке массива, регистрируются в конце своего Create → провайдер, объявленный позже, ещё не в реестре → `sync.Once` кэширует `outbound detour not found` навсегда, нода не шлёт ни байта. Смена порядка чинила случайно. Ядро упорядочивает старт по зависимостям (топосорт `startOutbounds`, detour = `Dependencies()`), но резолв утекал из-под барьера. Фикс (1 файл): (A) каст egress-anchor обёрнут в `if Detour==""` — на detour-пути он и так всегда `false`; (B) `InitializeDetour` в `Start(StartStateStart)` за топосорт-барьером → `not found` = fail-fast, без ретраев. Red/green e2e (потребитель раньше провайдера): RED ~92с таймаут, GREEN ~2.8с. Остаток: field. [SPEC.md](TASKS/029-ENDPOINT_DETOUR_START_ORDER/SPEC.md) |
| **028** | NESTED_TUNNEL_UDP_FRAGMENT | **C** | 🧩 **Вложенные туннели через `detour` не ходили — нижнее плечо форсило DF.** Реальный (нижний) UDP-сокет `wireguard`/`masque` открывается через `common/dialer`, а тот по умолчанию ставит DF (`IP_PMTUDISC_DO`/`IP_DONTFRAG`). Для вложения (masque/wg/awg в `detour` в любых комбинациях) внешняя датаграмма штатно великовата (инкапсуляция +32 WG +`s4` AWG на каждый пакет) → с DF молча дропается (`sendmsg: message too long`) вместо фрагментации → внутренний туннель не встаёт или не несёт данные. Direct-ноды через тот же detour работали (мелкие датаграммы). Фикс: endpoint+masque ставят `UDPFragmentDefault=true` (opt-out как у direct/hysteria2/tuic); явный `"udp_fragment": false` возвращает DF. Юнит DF-флага (оба bind-пути) + e2e AWG-over-AWG через detour (fits+фрагментация). Остаток: field CPH2411. [SPEC.md](TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) |
| **031** | AWG2_TIMED_JUNK_J_ITIME | **N** | ⏲️ **Таймированный junk AWG2 (`Itime` + `J1–J3`) — до полного набора 20 параметров.** Сейчас покрыто 16 (`Jc/Jmin/Jmax`, `S1–S4`, `H1–H4`, `I1–I5`); `j1/j2/j3/itime` отсутствуют на всех слоях (конфиг, обвязка→UAPI, UAPI подмодуля, send). Следствие: к серверу, где этот уровень маскировки **обязателен**, клиент не сойдётся по хендшейку. Дёшево (~60%): `J1–J3` — тот же CPS-мини-язык, что `I1–I5`, парсер `newObfChain` уже есть; конфиг/UAPI — по образцу [005](TASKS/005-AWG2_RANGED_MAGIC_HEADERS/SPEC.md). Дорого: периодическая отправка junk в установившемся туннеле — **новый механизм** (таймер на peer по образцу `persistentKeepalive`, гейт `timersActive` обязателен, иначе регресс к [020](TASKS/020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) — будим спящие WG). ~250–400 LOC. Открыто: точная семантика `Itime` (период vs jitter, один пакет за тик vs `J1→J2→J3`) — сверить по коду `amneziawg-go`, не по памяти. [SPEC.md](TASKS/031-AWG2_TIMED_JUNK_J_ITIME/SPEC.md) |
| **032** | VLESS_ENCRYPTION_MLKEM768 | **N** | 🔐 **Клиентская поддержка `encryption: mlkem768x25519plus…` — пост-квантовый слой шифрования ВНУТРИ VLESS (не key exchange REALITY).** Зазор на трёх слоях: поля нет в схеме (`option/vless.go`), параметра нет в `vless.NewClient(uuid, flow)`, самого слоя нет в `sing-vmess`. Такие конфиги не идут ни у нас, ни в любом клиенте на sing-box; совместимы Xray (PR [#5067](https://github.com/XTLS/Xray-core/pull/5067)) и mihomo. Работы: +1 поле, парсер spec-строки и 5-шаговый handshake в изолированном `common/vlessenc/` (ML-KEM-768 из stdlib `crypto/mlkem` — Go 1.24.7 хватает, внешних зависимостей нет); точек касания апстрима — 2 файла. ~650–1000 LOC, риск сосредоточен в handshake (ошибка в HKDF/паддинге = молча не коннектится → нужен эталонный Xray-стенд). PQ-фичи самого REALITY (`mldsa65Verify`, вырезанный `X25519MLKEM768`) — осознанно вне scope, §7. [SPEC.md](TASKS/032-VLESS_ENCRYPTION_MLKEM768/SPEC.md) |
| **033** | DNS_GROUP_SERVER | **C** | 🧭 ✅ **Код+тесты+DoD.** DNS-группа: тип `group`, `failover`, `down_time` (1 сбой = down, all-down ротация «самый давний, 1 попытка»), валидации (циклы через Dependencies, состав, `servers`, badjson `[]`). 3 маркированных шва (constant/option/registry), остальное — новый пакет `dns/transport/group`. Живой `box.New`: старт-порядок из зависимостей, группа в `final`. Разграничение с upstream rule-level `race`/`evaluate` — в SPEC §3. Фича [DNS_GROUP](FEATURES/DNS_GROUP/FEATURE.md). [SPEC.md](TASKS/033-DNS_GROUP_SERVER/SPEC.md) |
| **034** | DNS_GROUP_RACE | **C** | 🏁 ✅ **Код+тесты+DoD (`-race`).** Режим `race`: ленивая гонка на реальном запросе (без таймеров — ENERGY-инвариант тестируется), победитель + рейтинг по приходу, отставшие доигрывают на `WithoutCancel`-контексте, `Reset` обесценивает замеры (gen-guard). [SPEC.md](TASKS/034-DNS_GROUP_RACE/SPEC.md) |
| **035** | DNS_GROUP_OBSERVABILITY | **C** | 👁 ✅ **Код+тесты+DoD.** Фактический ответивший участник в `SubscribeDNSQueries` через ctx-холдер (`common/dnstrack`); кеш/полный сбой честно остаются тегом группы; proto не тронут — LxBox совместим. Шов: 3 строки в `dns/client.go`, гейт на тип `group`. RPC состояния группы отложен до запроса LxBox. [SPEC.md](TASKS/035-DNS_GROUP_OBSERVABILITY/SPEC.md) |
| **025** | AWG_TRANSPORT_PADDING_OVERRUN | **C** | 💥 **Класс config-value крашей AWG-графта, device-verified.** Основной: transport padding (`s4>0`) писал за границу исходящего буфера → `SIGABRT` (`index out of range [123] length 76`) в `RoutineSequentialSender` на первом пакете данных (буфер `InputPacket`/`InputPackets` без запаса под сдвиг). Разбор вскрыл ещё 4 того же вида: rx-байты считались ×2 (дубль блока в `receive.go`), `jmax<jmin`/`i1`–`i5`-длины/полнодиапазонный magic-header роняли handshake. Все — в `submodules/wireguard-go` (форк, `with_awg`). Красно-зелёный `transport_padding_test.go` + `obf_guards_test.go`. Базовый WG (`s4=0`) не затронут. Второй слой к fail-fast [008](TASKS/008-AWG_JUNK_PARAM_VALIDATION/SPEC.md). Релиз `v1.14.0-lx.8-rc.1`. [SPEC.md](TASKS/025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md) |

> **Вне этого репозитория:** потребление ядра лаунчером (`singbox-launcher`) — парсинг `type=xhttp` в реальный XHTTP-транспорт (сейчас `023` маппит его в `httpupgrade`), AWG-поля в визарде, замена `bin/sing-box`. Это отдельные задачи в репозитории лаунчера.
