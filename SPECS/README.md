# SPECS — sing-box-lx (Spec Kit)

Два уровня документации:

| Каталог | Уровень | Содержимое |
|---------|---------|------------|
| **[FEATURES/](FEATURES/README.md)** | Крупные блоки | Фича целиком — как она устроена сейчас, поверх всех задач, которые её строили |
| **[TASKS/](TASKS/)** | Единицы работы | Папки `NNN-NAME` — конкретная задача: фича, баг или исследование |

Задача — единица работы (её делают и закрывают). Фича — то, что живёт
в продукте и складывается из нескольких задач.

Фичи двух родов: **продуктовые** (AWG, XHTTP, балансировка, наблюдаемость,
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
| Тип | F (feature) \| B (bug) \| Q (question/исследование) \| R (refactor) |
| Статус | N (new) \| O (open) \| W (wait) \| I (implemented) \| C (complete) \| D (done, device-verified) |
```

Разница между тремя «готовыми»: **I** — код, тесты и сборка в дереве, но полевой
проверки ещё не было; **C** — задача закрыта по своим критериям приёмки;
**D** — сверх того подтверждена прогоном на живом устройстве. Задача может
уйти в релиз в статусе `I`; `D` ставится по факту полевого прогона либо
решением владельца, когда релиз с задачей прожил в поле без жалоб (так
2026-09-24 закрыт весь хвост задач в `I`/`O`).

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
| **CONSTITUTION.md** | Принципы, приоритеты реализации, правила; ориентиры и референсы — в этом файле выше |
| **IMPLEMENTATION_PROMPT.md** | DoD, git/ребейз-ритуал, контракт выхода |

## Ориентиры и референсы

Заметки для реализации, вынесенные из [CONSTITUTION.md](CONSTITUTION.md) (там — только принципы
и правила). Факты апстрим-линии 1.14, проверять на каждом мерже:

- **v2ray-транспорты** диспатчатся `switch` по `options.Type` в `transport/v2ray/transport.go`
  (`NewClientTransport`/`NewServerTransport`); константы — `constant/v2ray.go`, опции —
  `option/v2ray_transport.go`. VLESS/VMess/Trojan ходят через общий транспорт — пер-протокольных
  правок не требуется.
- **WireGuard — endpoint**: `protocol/wireguard/endpoint.go`, регистрация
  `endpoint.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, NewEndpoint)`,
  проводка в `include/wireguard.go` (+ `wireguard_stub.go`). Девайс — `transport/wireguard`;
  зависимость `github.com/sagernet/wireguard-go` заменена `replace` на форк-сабмодуль.
- **libbox command-протокол** — gRPC-сервис `StartedService` в `daemon/started_service.proto`
  (+ регенерируемые `*.pb.go`/`*_grpc.pb.go`), клиент `experimental/libbox/command_client.go`.
  Опциональные RPC гейтятся build-tag'ом по парному паттерну
  `daemon/started_service_usbip{,_stub}.go` — образец для конституции §3.6.
- **TLS-клиенты** — общий слой `common/tls`: STD, uTLS и REALITY строятся через
  `NewClientWithOptions`; всё, что должно действовать на все три (фрагментация, дефолты под
  `detour`), ставится там, а не в отдельном движке (уроки SPEC 060/088).

Референсы — только как образец, код «как есть» не тянуть:

- **AWG** — [`hoaxisr/amnezia-box`](https://github.com/hoaxisr/amnezia-box) (submodule +
  `patches/amneziawg-go`) — исторический образец; фактическая схема своя: форк-сабмодуль
  `submodules/wireguard-go` = [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx).
- **Спецификации протоколов** — Xray-core (XHTTP: `mode`/`path`/`host`/`extra`; REALITY: сервер
  в XTLS/REALITY; VLESS `encryption`), amneziawg-go / amneziawg-tools (AWG 2.0/3.x), Cloudflare
  WARP (MASQUE CONNECT-IP). Где смотреть — [docs-lx/lx-reference-cores.md](../docs-lx/lx-reference-cores.md).
- **Clash API как функциональный эталон** для расширений §3.6 — `experimental/clashapi/`
  (`proxies.go` per-node delay, `rules.go` таблица правил): что именно пробрасываем в
  CommandClient. Код не тянуть — повторяем семантику через нативный канал.

## Методология: фича → задачи → реализация

**Порядок работы — сверху вниз. Сначала фича, потом задачи.**

Фича не пишется постфактум как сводка сделанного. Она пишется **первой** —
до задач и до кода — и фиксирует полный скоуп: что система должна делать,
чем она управляется, что принимает и что отдаёт. Только затем скоуп режется
на задачи, и только затем пишется код.

### 1. Фича — полный скоуп (до задач)

`FEATURES/NNN-NAME/FEATURE.md` описывает систему **как чёрный ящик**:

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
| **001** | FORK_BOOTSTRAP | **C** | Remotes, ветка `lx`, `Makefile.lx`, версия `-lx` в ldflags, скелет CI, `lx-test/config`. [SPEC.md](TASKS/001-FORK_BOOTSTRAP/SPEC.md) |
| **002** | XHTTP_CLIENT_TRANSPORT | **C** | Клиентский XHTTP-транспорт против Xray: `packet-up` / `stream-one` / `auto`, obfs, размещение uplink-данных. Проверен на живых узлах 3x-ui и на четырёх реальных нодах подписки (v2). Дальнейшие правки транспорта — 011, 042, 043, 059, 061, 077. [SPEC.md](TASKS/002-XHTTP_CLIENT_TRANSPORT/SPEC.md) |
| **003** | AWG2_CLIENT_ENDPOINT | **C** | AmneziaWG 2.0 endpoint (`Jc`/`Jmin`/`Jmax`, `S1`–`S4`, `H1`–`H4`, `I1`–`I5`) на форк-сабмодуле Leadaxe/wireguard-go. Handshake, keepalive и трафик проверены живым AWG2-сервером. [SPEC.md](TASKS/003-AWG2_CLIENT_ENDPOINT/SPEC.md) |
| **004** | BUILD_CI_RELEASE | **C** | `Makefile.lx`, libbox-теги, CI (lint и build-check на push, кросс-сборка и AAR по dispatch), `lx-release.yml` (6 desktop-архивов + 2 AAR), поставка libcronet (dll в windows-архивах, CGO-статика на darwin), `lx-rebase.yml`. [SPEC.md](TASKS/004-BUILD_CI_RELEASE/SPEC.md) |
| **005** | AWG2_RANGED_MAGIC_HEADERS | **C** | Диапазонные `H1`–`H4` (`"N-M"`) из awg2-экспортов: `option.MagicHeader` принимает число или строку и отдаёт spec-строку в IpcSet. Проверено живым сервером с ranged-конфигом. [SPEC.md](TASKS/005-AWG2_RANGED_MAGIC_HEADERS/SPEC.md) |
| **006** | LINUX_MUSL_STATIC_ROUTER_BUILDS | **C** | musl-статические сборки под роутеры (amd64, arm64, armv7, mipsle-softfloat) по образцу upstream build.yml, тег `with_musl`, naive сохранён. Только CI, без Go-кода. Чинит [#1](https://github.com/Leadaxe/sing-box-lx/issues/1) (`libdl.so.2` на AsusWRT armv7). [SPEC.md](TASKS/006-LINUX_MUSL_STATIC_ROUTER_BUILDS/SPEC.md) |
| **007** | AWG_OVER_WIREGUARD_DETOUR_GUARD | **C** | Guard от зависания AWG-over-WireGuard снят в lx.11 (2026-07-18): после re-graft на wireguard-go v0.0.5 с фиксами 025 и 026 связка поднимается и несёт трафик (e2e на Mac). Удалены оба guard'а и adapter-хуки, idle-suspend 020 сохранён. История — в SPEC. Was [#2](https://github.com/Leadaxe/sing-box-lx/issues/2). [SPEC.md](TASKS/007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) |
| **008** | AWG_JUNK_PARAM_VALIDATION | **C** | `jmin > jmax` паниковал в `rand.Int` в timer-горутине. `validateJunk` отвергает такие значения на `check` и старте. Только краш-кейс, несогласованность `jc` не трогаем. Чинит [#3](https://github.com/Leadaxe/sing-box-lx/issues/3). [SPEC.md](TASKS/008-AWG_JUNK_PARAM_VALIDATION/SPEC.md) |
| **009** | WIRESOCK_MASQUERADE_PROFILES | **C** | Профили `id`/`ip`/`ib` в стиле WireSock как сахар над `I1` CPS: quic (1-RTT short header), dns (EDNS OPT response), stun (Binding Success), sip (200 OK); структуры из `amneziawg-proxy/transform.rs` (MIT). Только `I1`: `S1`–`S4` против WARP невозможны, сабмодуль не трогаем. LDH-валидация домена; `ib` без JA3-отпечатка. Проверено туннелем и трафиком, релиз v1.13.13-lx.11. [SPEC.md](TASKS/009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) |
| **010** | WG_ENDPOINT_GRO_SPLIT_BRAIN | **C** | WG-endpoint без `detour` на Android резал download до 0.44 Мбит/с: UDP_GRO включён, а receive-путь был linux-only. Гейт `UDP_GRO` за `!android` в сабмодуле дал 20.7 Мбит/с на устройстве. При миграции на 1.14 патч снят: апстрим починил сам (`24ea133`). [SPEC.md](TASKS/010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) |
| **011** | XHTTP_STREAM_ONE_DOWNLINK | **C** | `vless+reality+xhttp+mode:auto` не работал: stream-one слал `<path>/<sessionId>`, а Xray роутит stream-one только при пустом sessionId ([issue #5635](https://github.com/XTLS/Xray-core/issues/5635)). Фикс — путь без sessionId, `auto`+reality → stream-one. Лайв 2026-08-01 показал, что вместе с sessionId срезан и завершающий слэш: корень доделан в [043](TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md) (попутно [042](TASKS/042-XHTTP_STREAM_GRPC_CONTENT_TYPE/SPEC.md)). Путь подтверждён лайвом 002 v2. [SPEC.md](TASKS/011-XHTTP_STREAM_ONE_DOWNLINK/SPEC.md) |
| **012** | TCP_DOWNLINK_STALL_ZOMBIE_CONNS | **C** | Сталл «↑517 ↓0» (WhatsApp и Telegram висят) на разных нодах, включая WG. WG-долю закрыл [010](TASKS/010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md); для не-WG объяснения нет, на lx.14 не воспроизводится — закрыт как not reproducible. Зонд `LX_CONN_TRACE` в бою не прогонялся ([PROBE.md](TASKS/012-TCP_DOWNLINK_STALL_ZOMBIE_CONNS/PROBE.md)). [SPEC.md](TASKS/012-TCP_DOWNLINK_STALL_ZOMBIE_CONNS/SPEC.md) |
| **013** | PACKAGE_NAME_REGEX_RULE_ITEM | **C** | Бэкпорт апстрим-фичи 1.14 ([941ce58b](https://github.com/SagerNet/sing-box/commit/941ce58b)) на базу 1.13.13: rule-item `package_name_regex` для route, DNS и headless; хунк `RuleSetVersion5` не переносился. После миграции на 1.14 от задачи остался только тест (см. UPSTREAM_SYNC). [SPEC.md](TASKS/013-PACKAGE_NAME_REGEX_RULE_ITEM/SPEC.md) |
| **014–021** | *(см. шапки в `SPECS/TASKS/NNN-*/SPEC.md`)* | **C** | Command-протокол RPC (014/015), connections-мьютекс (016), Connection.Detour (017), DNS-query-стрим (018), URLTest пул/sticky (019), multi-WG idle-suspend (020), MASQUE CONNECT-IP outbound (021). Источник статуса — шапка каждого SPEC.md |
| **022** | LX_DEEP_AUDIT | **C** | Многоагентный аудит всей lx-дельты по 10 осям с адверсариальной верификацией находок: 27 подтверждённых (0 critical, 1 high, 1 medium), 24 исправлены, среди них P0 «masque h2 CONNECT висел без ctx» и P1 «idle-suspend воскрешал guard-suspended AWG». #12/#17/#18 пропущены осознанно. [SPEC.md](TASKS/022-LX_DEEP_AUDIT/SPEC.md) |
| **023** | MUSL_TOOLCHAIN_MIRROR | **C** | `snapshot.debian.org` периодически отвечает 503 и блокировал релиз. Producer-workflow заливает musl-тулчейн четырёх арок в release-ассет `musl-toolchain-cache`, `lx-release.yml` берёт его на cache-miss до фолбэка на snapshot. Зеркало заполнено, restore проверен. [SPEC.md](TASKS/023-MUSL_TOOLCHAIN_MIRROR/SPEC.md) |
| **024** | RUNTIME_LOOP_GUARD | **DEFERRED** | Кольцо detour/selector, собранное в рантайме через `SelectOutbound`, роняет процесс (`fatal stack overflow`); статическое ядро отклоняет на старте. Проработана событийная модель E1–E5, но ядерный guard негерметичен (TOCTOU через endpoint-менеджер, Remove и history). Решение 2026-07-06: защита в UI (LxBox), ядро не трогаем. [SPEC.md](TASKS/024-RUNTIME_LOOP_GUARD/SPEC.md) |
| **025** | AWG_TRANSPORT_PADDING_OVERRUN | **C** | `s4>0` писал за границу исходящего буфера: SIGABRT на первом пакете данных. Рядом ещё четыре дефекта того же вида (rx-байты считались дважды, `jmax<jmin`, длины `i1`–`i5`, полнодиапазонный magic). Всё в сабмодуле, red/green тесты, базовый WG не затронут. Релиз v1.14.0-lx.8-rc.1, device-verified. [SPEC.md](TASKS/025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md) |
| **026** | AWG_MAGIC_VS_RESERVED_CLEAR | **C** | Обнуление байтов 1–3 (WARP reserved) шло безусловно на каждом принятом пакете и при `s1`/`s2`/`s4` < 4 затирало AWG magic: узел не вставал (issue #8). Гейт `hasReserved()` на всех пяти receive-путях, e2e red/green. [SPEC.md](TASKS/026-AWG_MAGIC_VS_RESERVED_CLEAR/SPEC.md) |
| **027** | UTLS_OVER_QUIC | **C** | Исследование без реализации: `tls.utls` поверх hysteria2/tuic падает `unsupported usage for uTLS`, блокер сидит в трёх зависимостях (metacubex/utls, sagernet/quic-go, sing-quic). Реализация отложена до поддержки в апстримах. Источник симптома — подписки с `fp=chrome` на всех нодах подряд. [SPEC.md](TASKS/027-UTLS_OVER_QUIC/SPEC.md) |
| **028** | NESTED_TUNNEL_UDP_FRAGMENT | **C** | Вложенные туннели через `detour` не ходили: нижний UDP-сокет ставил DF, а внешняя датаграмма с инкапсуляцией не влезала (`message too long`). Endpoint и masque ставят `UDPFragmentDefault=true`, явный `udp_fragment: false` возвращает DF. Юнит и e2e AWG-over-AWG. В поле с lx.13. [SPEC.md](TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) |
| **029** | ENDPOINT_DETOUR_START_ORDER | **C** | WG/AWG-endpoint с `detour` был мёртв, если провайдер объявлен позже потребителя: резолв detour утекал в конструктор до сборки графа, и `sync.Once` кэшировал `not found`. `InitializeDetour` перенесён в `Start` за топосорт-барьер. e2e: 92 с таймаута → 2.8 с. Позже апстрим устранил первопричину сам. [SPEC.md](TASKS/029-ENDPOINT_DETOUR_START_ORDER/SPEC.md) |
| **030** | FAST_BOX_SHUTDOWN | **C** | `box.Close()` на Android занимал 10 с и больше при ~30 WG/AWG-нодах: закрытие endpoint'ов ждало на `resumeMu` разбуженный пингом `resumeOnDial`. Квиесценс роутера в начале Close, гейт `closing` в endpoint, параллельное закрытие. Доли секунды; e2e на 20 нодах ≈ 5 мс. [SPEC.md](TASKS/030-FAST_BOX_SHUTDOWN/SPEC.md) |
| **031** | AWG_PARITY_AUDIT_ADVANCED_SECURITY | **C** | Сверка клиента с `amneziawg-tools`: паритет полный (16 параметров, CPS-мини-язык 1:1, `IpcGet` отдаёт всё). `AdvancedSecurity` — серверное поле, у клиента смысла не имеет. Отозваны гипотезы про `J1`–`J3`/`Itime` и про ключи ветки master (тогда не выпущены; позже вышли как AWG 3, см. [080](TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md)). [SPEC.md](TASKS/031-AWG_PARITY_AUDIT_ADVANCED_SECURITY/SPEC.md) |
| **032** | VLESS_ENCRYPTION_MLKEM768 | **C** | Клиентский PQ-слой `encryption: mlkem768x25519plus…` внутри VLESS. Диагноз дал дамп базы NekoBox+ у репортёра. Клиент портирован из `starifly/sing-box` (та же лицензия и база), сервер не брали. Device-verified: +10 нод. Не покрыты формы `1rtt` с паддингом, `xorpub`/`random`. [SPEC.md](TASKS/032-VLESS_ENCRYPTION_MLKEM768/SPEC.md) |
| **033** | DNS_GROUP_SERVER | **C** | Группа DNS-серверов `stable` / `fastest` / `parallel` на TTL-модели (v2; 034 слита сюда): две TTL-записи ошибки и победы, веер по чистым, анти-шторм, single-flight выборов. v1 — в HISTORY, её конфиг не поддерживается. [SPEC.md](TASKS/033-DNS_GROUP_SERVER/SPEC.md) |
| **035** | DNS_GROUP_OBSERVABILITY | **C** | Трасса и состояние группы в наблюдаемости (v3; 036 слита сюда): `fanned`, `survival`, state-RPC с `clean` / `liveErrors` / `lastErrorAgeMs` / `liveWins` / `current`. Механизм трассы не менялся. [SPEC.md](TASKS/035-DNS_GROUP_OBSERVABILITY/SPEC.md) |
| **037** | RUNNING_CONFIG_RPC | **C** | `GetRunningConfig` — канонический JSON, из которого реально построен box (после override'ов). Снапшот один раз в `newInstance`, за `with_lx_command`. Закрывает рассинхрон профиль↔ядро в LxBox; attached-режим отдаёт `Unavailable`. Форма возврата объектом — см. [038](TASKS/038-GOMOBILE_STRING_RETURN_FRAME_KILL/SPEC.md). [SPEC.md](TASKS/037-RUNNING_CONFIG_RPC/SPEC.md) |
| **038** | GOMOBILE_STRING_RETURN_FRAME_KILL | **C** | Метод libbox с голым `string` в возврате убивал ядро на android/arm64: cgo кладёт `nstring` в `__packed__`-фрейм, и write barrier падает на невыровненном адресе. Возврат `*RunningConfig` с `Content()`; `[]byte` не лечит. Reflection-страж по всей поверхности `CommandClient`. Ломающее изменение libbox API, с lx.17. [SPEC.md](TASKS/038-GOMOBILE_STRING_RETURN_FRAME_KILL/SPEC.md) |
| **039** | REPORT_ARCHIVE_ROTATION | **C** | `oom_reports` и `crash_reports` росли без предела (575 папок, 427 МБ за 19 дней). `pruneReports` в обоих путях записи: 32 папки / 64 МБ, удаление по mtime, best-effort. Накопленное до фикса не чистит. [SPEC.md](TASKS/039-REPORT_ARCHIVE_ROTATION/SPEC.md) |
| **040** | SINGTUN_ACCEPTLOOP_SELFHEAL | **C** | System-стек TCP умирал после первой ошибки `Accept` в апстримном `acceptLoop`: каждое новое соединение получало RST до перезапуска VPN. Форк sing-tun переоткрывает листенер. Триггер — `reload`; `EINVAL` означает сокет разлушен, а не закрыт. Device-verified двумя живыми восстановлениями, lx.17-rc.4. [SPEC.md](TASKS/040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md) |
| **041** | WG_HANDSHAKE_GIVEUP_REBIND | **C** | WG/AWG-узлы после сна оставались в ERR до реконнекта (dewch #1298): состояние пути умерло, wireguard-go ретраил в мёртвый сокет. Форк: give-up переоткрывает bind и кикает handshake (v1); досрочный триггер после трёх неотвеченных инициаций и нудж `RebindStaleEndpoints()` на `USER_PRESENT` (v2). Дебаунс 90 с, конфиг не расширяется. [SPEC.md](TASKS/041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) |
| **042** | XHTTP_STREAM_GRPC_CONTENT_TYPE | **C** | Потоковые запросы шли без `Content-Type: application/grpc`, который ставит Xray. Заголовок добавлен в `stream-one` и `stream-up`, опция `no_grpc_header` стала действующей. Жалобу «14 XHTTP-нод мертвы» это не закрыло, корень нашёлся в [043](TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md); фикс ценен как паритет с Xray. [SPEC.md](TASKS/042-XHTTP_STREAM_GRPC_CONTENT_TYPE/SPEC.md) |
| **043** | XHTTP_STREAM_ONE_PATH_PREFIX | **C** | Корень жалобы «XHTTP-ноды подписки мертвы»: сервер нормализует путь в `<path>/` и отвечает 404 на всё без этого префикса, а мы в `stream-one` срезали слэш (наследие 011). `barePathForStreamOne`; доказано на проводе. hy2 0/11 той же подписки — не наш баг. Device-verified, stable lx.17. [SPEC.md](TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md) |
| **044** | ANDROID_AAR_GO124_QUIC_DEAD | **C** | AAR на Go 1.24.x убивал все quic-go-аутбаунды на вендорских Android-ядрах: dial висит до дедлайна, эмулятор ложно-зелёный. A/B на железе: go1.24.7 падает, go1.25.5 работает. Фикс — пин тулчейна в AAR-джобах (см. [049](TASKS/049-GO_TOOLCHAIN_PIN_FILE/SPEC.md)), `go.mod` апстримный. Попутно Debug-ручки GSO/ECN. С lx.19. [SPEC.md](TASKS/044-ANDROID_AAR_GO124_QUIC_DEAD/SPEC.md) |
| **045** | TLS_DISABLED_NIL_DIALER_CRASH | **C** | Trojan/VLESS с `tls.enabled: false` роняли процесс nil-паникой на первом дозвоне: регрессия апстримного ECH-коммита, где dialer создаётся под гейтом `TLS != nil`, а конструктор для `enabled: false` отдаёт `(nil, nil)`. Nil-гейт как у vmess в оба конструктора, red/green. Реестр [HOTFIXES](FEATURES/004-HOTFIXES/FEATURE.md). [SPEC.md](TASKS/045-TLS_DISABLED_NIL_DIALER_CRASH/SPEC.md) |
| **046** | DNS_HIJACK_PACKET_LOOP_STALL | **D** | Один DNS-сервер с `detour` на мёртвый outbound замораживал весь форвардинг туннеля: hijack-DNS вызывался синхронно из пакетного цикла стека, а `ExchangeAsync` блокировал до таймаута, 10 с на каждый запрос. Exchange ушёл в горутину под семафором 256, сверх лимита — дроп. Эмулятор-verified, lx.20-rc.2; в поле без жалоб, закрыта владельцем 2026-09-24. [SPEC.md](TASKS/046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md) |
| **047** | EARLY_RPC_NIL_ROUTER_CRASH | **C** | Смена сети во время `Box.Start()` роняла процесс nil-паникой в `ResetNetwork`: RPC-гейт проверял `Box() != nil`, а instance публикуется до старта. Ранний nil-guard и предикат `Ready()` из `daemon`. Крашдамп lx.19-rc.3; red/green, `-race`. [SPEC.md](TASKS/047-EARLY_RPC_NIL_ROUTER_CRASH/SPEC.md) |
| **048** | GVISOR_HANDSHAKE_NIL_CRASH | **C** | Недостижимый узел при живых ретрансмитах SYN ронял процесс nil-паникой в gvisor (`handleConnecting` на endpoint'е с занулённым `h`). Баг апстримный, защититься снаружи нельзя: паника рождается в горутине gvisor. Третий форк-сабмодуль `submodules/gvisor` (снапшот пина без истории): 12 строк guard и тест, стек воспроизведён построчно. [SPEC.md](TASKS/048-GVISOR_HANDSHAKE_NIL_CRASH/SPEC.md) |
| **049** | GO_TOOLCHAIN_PIN_FILE | **C** | Единый пин версии Go в файле `go.version`, его читают 15 шагов `setup-go` в пяти `lx-*.yml`. Не `go-version-file: go.mod`: тот даёт 1.24.7, регрессию 044. [SPEC.md](TASKS/049-GO_TOOLCHAIN_PIN_FILE/SPEC.md) |
| **050** | URLTEST_ZOMBIE_RUN_SURVIVES_RESTART | **D** | URL-тест на полуживом узле `vless + xhttp + encryption` висел навсегда и переживал Stop/Start (дамп: прогоны возрастом 100 мин, массив на 2806 узлов, 5 OOM-снимков). Три дефекта только вместе: XHTTP-conn без дедлайнов, `encryption.Handshake` без ctx, `URLTestGroup.Close()` не отменяет прогон. Все уровни в дереве (`guardHandshake` с v1.14.1-lx.2), стенд `lx-test/zombie`. В поле без жалоб, закрыта владельцем 2026-09-24. [SPEC.md](TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md) |
| **051** | UPSTREAM_MERGE_235 | **D** | Дрейф 235 коммитов от `upstream/testing` влит (`f56680e0d`): 65 конфликтов, `*.pb.go` регенерированы. Уроки: автослияние ломает молча (три файла), ловится только сборкой и тестами; отсутствие lx-маркера не значит, что файл не наш; форк-сабмодули проверять до мержа ядра (tailscale 1.102 требовал свежий wireguard-go). В поле с lx.20-rc.5, закрыта владельцем 2026-09-24. [SPEC.md](TASKS/051-UPSTREAM_MERGE_235/SPEC.md) |
| **052** | NETSTACK_CONNECT_DEADLINE | **C** | TCP-дайлы через gVisor-netstack (WG/AWG, MASQUE, openvpn, tailscale) были единственным путём без таймаута: на чёрной дыре ~127 с SYN-бэкоффа. Одноразовый connect-дедлайн `C.TCPTimeout` в leaf-швах, UDP не тронут. Стенд: 2m7s → 15.05 s. Природа — [RESEARCH.md](TASKS/052-NETSTACK_CONNECT_DEADLINE/RESEARCH.md); реакция группы на ошибки — [054](TASKS/054-URLTEST_PENALTY_FAILOVER/SPEC.md). [SPEC.md](TASKS/052-NETSTACK_CONNECT_DEADLINE/SPEC.md) |
| **053** | REALITY_MIN_CLIENT_VER | **D** | REALITY на Xray ≥ v26.7.11 без `minClientVer` уходил на камуфляжный сайт: сервер требует клиента ≥ 26.3.27, апстрим объявляет 1.8.1. Объявляем 26.3.27 в `reality_client.go` (апстримный файл, реестр [HOTFIXES](FEATURES/004-HOTFIXES/FEATURE.md)). С lx.22-rc.1; стенд [083](TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md) 2026-09-13 подтвердил. [SPEC.md](TASKS/053-REALITY_MIN_CLIENT_VER/SPEC.md) |
| **054** | URLTEST_PENALTY_FAILOVER | **C** | least_test реагирует на отказы боевых дайлов (потребитель 15-секундных ошибок 052): «путь мёртв» даёт узлу штраф и один fallback-дайл через лучшего кандидата; при трёх штрафах у лучшего выбор ранжируется штрафы → задержка; штраф снимает только доказательство жизни; тотальные штрафы запускают force-прогон не чаще раза в 2 мин. Новых таймеров нет. [SPEC.md](TASKS/054-URLTEST_PENALTY_FAILOVER/SPEC.md) |
| **055** | LXD_DAEMON_SKELETON | **C** | Headless-демон `sing-box lxd` (первая задача фичи [014-LXD_DAEMON](FEATURES/014-LXD_DAEMON/FEATURE.md)): ядро in-process за gRPC-каналом `daemon.StartedService`, который переживает смену конфига; SIGHUP подменяет box, битый конфиг оставляет демон в FATAL с живым каналом. Собственный файл, ноль правок апстрима. Демо на macOS. [SPEC.md](TASKS/055-LXD_DAEMON_SKELETON/SPEC.md) |
| **056** | LXD_APPLY_ROLLBACK | **C** | Admin-плоскость демона на том же порту (h2c-мультиплексор: gRPC и REST, Bearer). `POST /admin/apply`: валидация сабпроцессом `check`, подмена инстанса, автооткат на last-good при провале Start; рестарт демона поднимается из last-good. `/admin/rollback|config|status`, SIGHUP — тот же пайплайн. Демон стартует и без конфига. [SPEC.md](TASKS/056-LXD_APPLY_ROLLBACK/SPEC.md) |
| **057** | LXD_MTLS_SERVICE | **C** | mTLS для демона: сам себе CA, приглашение `адрес#отпечаток#код`, `POST /admin/enroll` по одноразовому коду, пин клиента на обеих плоскостях, `client add/list/remove`. `/admin/start|stop`, память `was_running`, `daemon.json` (0600), ротация `lxd.log`, `/admin/info`. Служба: на macOS install и install-user ставят по-настоящему, на linux печатается рецепт, windows — заглушка. Device-verified на macOS. [SPEC.md](TASKS/057-LXD_MTLS_SERVICE/SPEC.md) |
| **058** | GET_URL_VIA_OUTBOUND | **D** | HTTP-пробник: GET по URL через outbound или endpoint по тегу с телом ответа (exit-IP, гео, `warp=`). Unary, кламп 256 KiB, не-2xx — результат, только GET, headers через nullable-билдер. Шов — `URLTestOutbound`, возврат объектом (038). С lx.25-rc.1; в поле без жалоб, закрыта владельцем 2026-09-24. [SPEC.md](TASKS/058-GET_URL_VIA_OUTBOUND/SPEC.md) |
| **059** | XHTTP_XMUX | **D** | Переиспользование HTTP-соединений XHTTP по секции `xmux` (совместимость с Xray и экономия хендшейков): пул с вытеснением по четырём критериям, отложенное закрытие при живых потоках, диапазоны `"min-max"` и `[min,max]`, дефолты без секции. `leftRequests` считает запросы, а не потоки; `openUsage` идемпотентен. Только клиент. С lx.25-rc.3; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/059-XHTTP_XMUX/SPEC.md) |
| **060** | TLS_FRAGMENT_AUTO_ON_DETOUR | **C** | TLS-outbound через `detour` не поднимался на части нижних плеч: ClientHello больше PMTU за плечом, ICMP до нас не доходит. Под `detour` `record_fragment` включается по умолчанию одной точкой в `common/tls` для STD, uTLS и REALITY. Замер: 12 с → 0.1 с, матрица 38/38 на живых узлах. Явный `record_fragment: false` неотличим от «не задано». [SPEC.md](TASKS/060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) |
| **061** | XHTTP_DIAL_DOWNLOAD_DEADLOCK | **D** | Дайл `packet-up`/`stream-up` ждал download-ответ внутри себя, а сервер придерживает нисходящий поток до первого uplink-пакета: взаимное ожидание с самого появления фичи 002. Соединение отдаётся вызывающему сразу. Воспроизведено на проводе, регрессионный тест. С lx.30; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/061-XHTTP_DIAL_DOWNLOAD_DEADLOCK/SPEC.md) |
| **062** | MASQUE_CONFIG_SCHEMA_MIGRATION | **C** | Конфиг masque приведён к стандарту sing-box: версия HTTP в `vhttp` (раньше в `network`), TLS в штатном блоке `tls` (даром получает 060). Старые поля живут на алиасах до lx.30 с предупреждением; из legacy-булевых переносится только `true`. Дефолтный SNI больше не имя эндпоинта. Обе схемы проверены на живом WARP. [SPEC.md](TASKS/062-MASQUE_CONFIG_SCHEMA_MIGRATION/SPEC.md) |
| **063** | LXD_RESOURCE_STORE | **C** | REST-CRUD над файловыми ресурсами конфига (`.srs`, geo-базы) по имени с sha256; 409 на ресурс, занятый живым конфигом; санитизация имён. Сквозное демо на macOS. [SPEC.md](TASKS/063-LXD_RESOURCE_STORE/SPEC.md) |
| **064** | SELECTOR_INTERRUPT_DEAD_ON_INBOUND | **D** | `interrupt_exist_connections` у `selector` не работал для пользовательского трафика (апстримный дефект с 2023 года). v1 подменял `selected`; v2 оборачивает входящий conn и покрывает вложенные группы, но дал ABBA-дедлок, закрытый в [084](TASKS/084-INTERRUPT_GROUP_ABBA_DEADLOCK/SPEC.md). С lx.25-rc.7; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md) |
| **065** | LXD_OBSERVABILITY_PLANE | **C** | Диагностика самого демона за тем же mTLS: `/admin/memory`, `/admin/stats` (без ядра `null`), `/admin/logs`, `/admin/pprof/*` по whitelist с потолками. Живой прогон на macOS. [SPEC.md](TASKS/065-LXD_OBSERVABILITY_PLANE/SPEC.md) |
| **066** | LXD_CLIENT_IDENTITY | **D** | Справочник «IP → устройство» для инспектора: `GET /admin/clients-info` (`name`, `mac`, `ssid`, `iface`, `port`, `source`), метки оператора через `PUT`/`DELETE`. Провайдеры lease → arp → bridge → wireless → label за build-тегами, кеш 60 с. Device-verified на OpenWrt (MT7981). [SPEC.md](TASKS/066-LXD_CLIENT_IDENTITY/SPEC.md) |
| **067** | LXD_BUILD_TAG_SPLIT | **C** | `with_lx_command` гейтил и RPC-расширения libbox, и пакет `lxd/`. Теперь `with_lx_command` — только RPC, `with_lxd` — демон; Win7-сборка идёт без демона, но с RPC. Правки только в `//go:build`, `LX_TAGS` и CI. [SPEC.md](TASKS/067-LXD_BUILD_TAG_SPLIT/SPEC.md) |
| **068** | LXD_HOST_TELEMETRY | **C** | `GET /admin/host`: CPU по ядрам и load, память от `available`, термозоны, диски с `read_only` и `holds_state_dir`, дескрипторы свои и системные; `/admin/host/interfaces` — счётчики и скорости. Проценты и скорости считаются дельтами, первый замер `null`; кеш 500 мс. Device-verified на OpenWrt. [SPEC.md](TASKS/068-LXD_HOST_TELEMETRY/SPEC.md) |
| **069** | WG_V6_BIND_FAIL_KILLS_V4 | **D** | Windows: провал v6-бинда на адаптере вне v6-стека валил весь `StdNetBind.Open` вместе с v4-сокетом, все WG/AWG-эндпоинты были мертвы (десктоп-лаунчер, lx.25-rc.1). Форк wireguard-go: платформенный предикат «семейство недоступно», выжившее семейство работает. С lx.27-rc.1; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/069-WG_V6_BIND_FAIL_KILLS_V4/SPEC.md) |
| **070** | WG_START_CLOSE_RACE_CRASH | **C** | Поглощена задачей [072](TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md) (решение владельца 2026-08-18); механизм жив, маркеры `// lx: SPEC 070` сохранены. [SPEC.md](TASKS/070-WG_START_CLOSE_RACE_CRASH/SPEC.md) |
| **071** | WG_BIND_DIAL_PAUSE_DEADLOCK | **C** | Поглощена задачей [072](TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md) (решение владельца 2026-08-18); механизмы живы, маркеры `// lx: SPEC 071` сохранены. [SPEC.md](TASKS/071-WG_BIND_DIAL_PAUSE_DEADLOCK/SPEC.md) |
| **072** | WG_DETOUR_LIFECYCLE_FREEZE | **D** | Семья полевых отказов WG-эндпоинта с `detour` (три дампа 2026-08-12…17), поглотила 070 и 071. Четыре механизма: гейт гонки Start/Close, ограниченный dial бинда с отвязанным диспатчем pause, разрыв upload-пайпа при провале raise, контексты запросов XHTTP на время жизни conn. С lx.27-rc.4; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md) |
| **073** | CHAIN_OUTBOUND | **D** | Outbound `chain` — цепочка хопов из групп и узлов (фича [015-CHAIN](FEATURES/015-CHAIN/FEATURE.md)). Группы не копируются: хук в точках дозвона подменяет выбранный узел рантайм-звеном с `detour` на `<tag>#i`; звенья ленивые и удаляются по простою; `direct` прозрачен, `block` терминален; `strip`, `rewrite`, авто-MTU; путь в `detourList`, `ChainInfo`, задержка по слоям. Тег `with_lx_chain`. С lx.27-rc.5; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/073-CHAIN_OUTBOUND/SPEC.md) |
| **074** | MASQUE_VHTTP_AUTO | **D** | `vhttp: auto` — h3 с откатом на h2, дефолт с v2: через часть промежуточных узлов QUIC до WARP глох, и MASQUE висел 40–90 с. h3 стартует в своей горутине, через 3 с без ответа стартует h2, не дожидаясь h3; победивший режим запоминается. Стенд: h2 за 3.3 с. С lx.27-rc.6; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/074-MASQUE_VHTTP_AUTO/SPEC.md) |
| **075** | CHAIN_POSITION_TOGGLE | **D** | Рантайм-выключатель позиций `chain` (`SetChainPositionEnabled`): позиция ≥ 1 становится проходом к предыдущему хопу, позиция 0 дайлит напрямую, всё выключено = `direct`. Набор в cache-file, разрыв соединений по модели selector'а, `disabled` в `GetChains`, `GetChainCloneConfig`. С lx.28-rc.4; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/075-CHAIN_POSITION_TOGGLE/SPEC.md) |
| **076** | XHTTP_XMUX_BREAKER | **D** | Предохранитель пула `xmux` на шторм переподключений (issue #14): breaker на соединение (три отказа подряд → вытеснение), backoff открытия транспорта до 3 с, `GetBody` для ретрая после GOAWAY. Корень «висит до перезапуска» оказался в [082](TASKS/082-H2_STREAM_ERROR_TYPE_LEAK/SPEC.md). Репортёр подтвердил на lx.35; с lx.29, закрыта владельцем 2026-09-24. [SPEC.md](TASKS/076-XHTTP_XMUX_BREAKER/SPEC.md) |
| **077** | XHTTP_DIAL_CTX_CONTRACT | **D** | DNS-серверы `udp`/`tcp`/`tls` с `detour` через XHTTP падали `context canceled`: пул DNS отменяет dial-ctx сразу после `dial`, а сторож 050/072 слушал его и после возврата. Не работало никогда. `DialContext` паркуется до принятия тела запроса, сторож снят, download-ответ не ждётся (061). Четыре red/green теста. С lx.30; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) |
| **078** | WIREGUARD_PACKET_SNIFFER | **D** | Апстримный uTP-сниффер помечал plain-WG handshake initiation как `bittorrent`. Сниффер `wireguard` по типу и размеру датаграммы стоит в дефолтном порядке перед uTP; имя доступно в `sniffer` и `protocol`. AWG с не-дефолтными H/S не распознаётся намеренно. С lx.33; на роутере не гонялось, закрыта владельцем 2026-09-24. [SPEC.md](TASKS/078-WIREGUARD_PACKET_SNIFFER/SPEC.md) |
| **079** | VPN_VOIP_PACKET_SNIFFERS | **D** | Снифферы `openvpn` (hard-reset, UDP и TCP), `ike` (IKE_SA_INIT, IKEv1), `tailscale` (disco), `sip` (request-line, `Domain` из Request-URI) по форме первого пакета; RTP, L2TP, PPTP, KCP, VNC отвергнуты (решение владельца 2026-09-05). Дока `docs-lx/lx-sniff.md`. С lx.33; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/079-VPN_VOIP_PACKET_SNIFFERS/SPEC.md) |
| **080** | AWG3_HEADER_PROTECTION_TIMINGS | **C** | AmneziaWG 3.0/3.1: шифрование заголовка (`HeaderProtectionKey`), `ContentPaddingAddition`, `RandomTrailers`, `DisableCookies`, диапазонные тайминги и keepalive; SPEC 031 §3 устарела. Порт диффа v0.2.19→v3.1 в сабмодуль, `option.AWGRange` и девять полей в ядре, fail-fast в `check`. Device-verified: живой сервер и узел в лаунчере (35 Мбит/с; экспортный `mtu 1376` за MASQUE-роутером не проходит, 1280 работает). lx.32. [SPEC.md](TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md) |
| **081** | AWG_RECEIVE_INDEX_FIRST_CLASSIFICATION | **C** | Data-пакет с нашим живым receiver index классифицируется как transport до handshake-кандидатов: референсный порядок терял пакеты размером `s1+148` (AWG2) и любые крупнее при `random_trailers` (AWG 3.1). Правка только на приёме, провод не меняется. Red/green e2e с детерминированной коллизией. [SPEC.md](TASKS/081-AWG_RECEIVE_INDEX_FIRST_CLASSIFICATION/SPEC.md) |
| **082** | H2_STREAM_ERROR_TYPE_LEAK | **D** | Корень issue #14: conn'ы XHTTP, v2rayhttp и gRPC-lite отдавали наверх `http2.StreamError` типом, h2-клиент x/net принимал её за ошибку своего стрима и крутил `readLoop` без сисколлов — 100 % CPU до перезапуска. `common/badh2.HideStreamError` в шести точках, регрессия воспроизводит спин. Репортёр подтвердил на lx.35, тикет закрыт 2026-09-08. [SPEC.md](TASKS/082-H2_STREAM_ERROR_TYPE_LEAK/SPEC.md) |
| **083** | REALITY_MLKEM_KEYSHARE | **D** | REALITY на Xray ≥ v26.9.8 требует key_share `X25519MLKEM768`, а апстрим вырезал гибрид из ClientHello (SagerNet#4520). Фильтр снят, `AuthKey` по `Ecdhe` или `MlkemEcdhe`. Покрыто только chrome-семейство: `firefox` — [086](TASKS/086-UTLS_FORK_FIREFOX148/SPEC.md), `safari` — [087](TASKS/087-UTLS_SAFARI_26_3/SPEC.md), остальные отпечатки подменяют приложения (решение владельца). Стенд с живым Xray 2026-09-13; lx.36. [SPEC.md](TASKS/083-REALITY_MLKEM_KEYSHARE/SPEC.md) |
| **084** | INTERRUPT_GROUP_ABBA_DEADLOCK | **D** | Issue #20: переключение вложенного selector'а глушило трафик до рестарта — ABBA-дедлок `interrupt.Group`, наш регресс с lx.25 из-за обёртки 064 v2. Под замком остаётся только снятие записи, `Close` идёт после `Unlock`. Юнит и e2e с настоящими `Selector`, `-race`. С lx.38; закрыта владельцем 2026-09-24. [SPEC.md](TASKS/084-INTERRUPT_GROUP_ABBA_DEADLOCK/SPEC.md) |
| **085** | SOCKS5_UDP_ASSOCIATE_UNSPECIFIED_BIND | **C** | socks5-outbound: TCP ходил, UDP нет. sing диалил релей по BND.ADDR `0.0.0.0` как есть, то есть в localhost. Обёртка `relayDialer` подставляет адрес сервера из конфига (не `RemoteAddr()`: под `detour` это пир плеча), sing не форкается; стражи на апстримный клиент. lx.39. На узлах репортёра остаток серверный: UDP-релей мёртв для любого клиента. [SPEC.md](TASKS/085-SOCKS5_UDP_ASSOCIATE_UNSPECIFIED_BIND/SPEC.md) |
| **086** | UTLS_FORK_FIREFOX148 | **C** | `fp=firefox` на Xray ≥ v26.9.8: гибрид был только в chrome-пресетах metacubex/utls. Четвёртый форк-сабмодуль `submodules/utls` = [Leadaxe/utls-lx](https://github.com/Leadaxe/utls-lx): v1.8.7 плюс cherry-pick Firefox 148 и reuse X25519 из refraction. Страж на структуру ClientHello. Стенд 2026-09-16 и узел репортёра launcher#124. v1.14.1-lx.2, issue [#22](https://github.com/Leadaxe/sing-box-lx/issues/22). [SPEC.md](TASKS/086-UTLS_FORK_FIREFOX148/SPEC.md) |
| **087** | UTLS_SAFARI_26_3 | **C** | `fp=safari` тем же приёмом: cherry-pick Safari 26.3 из refraction в форк utls. Стенд 2026-09-16: 204 против v26.9.9, без регрессии на старых серверах. `edge`, `ios`, `android`, `360`, `qq` остаются без гибрида: пресетов нет ни у metacubex, ни у refraction, у Xray та же граница; подменяют приложения. v1.14.1-lx.3. [SPEC.md](TASKS/087-UTLS_SAFARI_26_3/SPEC.md) |
| **088** | REALITY_FRAGMENT_BYPASS | **D** | REALITY игнорировал `fragment` и `record_fragment`: апстримный `ClientHandshake` строит `UClient` на голом conn, минуя `tlsfragment`, и дефолт 060 до него не доходил. Общий `wrapClientConn` для uTLS и REALITY; стражи по матрице флагов и проводной. Обход DPI не обещается. v1.14.1-lx.4; полевой прогон репортёра LxBox #142 2026-09-18. [SPEC.md](TASKS/088-REALITY_FRAGMENT_BYPASS/SPEC.md) |
| **089** | REALITY_KEY_SHARE_OPTION | **D** | `tls.reality.key_share`: `hybrid` или `classical` по узлу. Повод — LxBox #142: на одном операторе двухсегментный гибридный ClientHello терялся, у Xray тот же симптом (XTLS#6256). `classical` включает апстримный фильтр по выбору, `hybrid` даёт ошибку, если отпечаток шар не несёт. Автофолбэка нет. v1.14.1-lx.4; полевой прогон 2026-09-18. [SPEC.md](TASKS/089-REALITY_KEY_SHARE_OPTION/SPEC.md) |
| **090** | REALITY_SHORT_ID_OVERFLOW_PANIC | **C** | `short_id` длиннее 16 hex ронял процесс: `hex.Decode` пишет за границы `[8]byte`, а апстримная проверка длины стоит после него и не срабатывает. Проверка `len > 16` до декодирования на клиенте и сервере, текст ошибки апстримный. Найдено DRIFT-инвентарём LxBox. Хотфикс lx.5, реестр [HOTFIXES](FEATURES/004-HOTFIXES/FEATURE.md). [SPEC.md](TASKS/090-REALITY_SHORT_ID_OVERFLOW_PANIC/SPEC.md) |
| **091** | CONFIG_VALIDATION_TUIC_MASQUE | **C** | Две дыры валидации: `tuic.udp_relay_mode` без `default` принимал опечатки как `native`, теперь ошибка; у `masque` с `profile: standard` без `uri` ошибка про h2 выдавалась раньше ошибки про `uri`, порядок проверок исправлен и текст самодостаточен. Провод и дефолты не меняются. lx.6. [SPEC.md](TASKS/091-CONFIG_VALIDATION_TUIC_MASQUE/SPEC.md) |
| **092** | INIT_ERROR_NAMES_TAG | **C** | Ошибка инициализации называет элемент типом и тегом: `initialize outbound[0] vless[proxy-de-1]: …` в шести циклах `box.go`, апстримный префикс сохранён дословно. Пожелание LxBox после lx.5. Страж `lx-test/initerr`. lx.7, реестр [HOTFIXES](FEATURES/004-HOTFIXES/FEATURE.md). [SPEC.md](TASKS/092-INIT_ERROR_NAMES_TAG/SPEC.md) |
| **093** | GRPC_SERVICE_NAME_CUSTOM_PATH | **D** | `service_name` с ведущим `/` трактуется как custom path в конвенции Xray: сегменты экранируются по отдельности, последний становится именем стрима; без ведущего `/` поведение прежнее байт в байт. Helper `common/grpcname` (порт из Xray), правки в lite-клиенте и сервере (`// lx: SPEC 093`). Против живого Xray не прогонялось (заявка launcher#130). v1.14.1-lx.8; закрыта владельцем 2026-09-24. Дока — [lx-protocols-transports.md §4.1](../docs-lx/lx-protocols-transports.md#41-service_name-the-xray-forms). [SPEC.md](TASKS/093-GRPC_SERVICE_NAME_CUSTOM_PATH/SPEC.md) |
| **094** | XHTTP_LOCAL_CLOSE_NOT_FAILURE | **I** | LxBox #148: под urltest в selector'е с `interrupt_exist_connections: true` XHTTP-соединения вытеснялись как `failing`, а лог был полон `connection download closed: http2: response body closed` на ERROR. Два наших дефекта на границе xhttp-conn: брейкер xmux считал сбоем `context.Canceled` от нашего же `Close()`; закрытое нами тело отдавало релею неизвестный sing sentinel. Теперь `Canceled` нейтрален (DeadlineExceeded и удалённые ошибки остаются сбоями), под гейтом `localClosed` Read отдаёт `net.ErrClosed` или `os.ErrDeadlineExceeded`. Стражи red/green, `option/` не тронут. Живой A/B на двух узлах 2026-09-24: ERROR-строки 13/13/15 → 0/0/0 при всех curl 200. Подтверждение репортёра впереди. [SPEC.md](TASKS/094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md) |
| **095** | UPSTREAM_SYNC_1_14_1_PLUS_34 | **D** | Синк с `upstream/stable` (v1.14.1 + 34) по раннбуку: сначала форк-сабмодули (`wireguard-go` re-graft на v0.0.7, `sing-tun` merge осиротевшего пина 3e03774a), потом мерж ядра с шестью конфликтами в наших швах. Апстрим закрыл SPEC 082 в `v2rayhttp`/`v2raygrpclite` (`baderror.WrapH2`) и гонку из 047; SPEC 084 и 040 остаются. Живой прогон по раннбуку §1.4 на эмуляторе 2026-09-24 (три цикла, 8 узлов): регрессий нет, апстримный фикс хендшейка доменных пиров подтверждён (retry 0, переключение 1,1 с против 5,4–5,7 с на lx.9), счётчики SPEC 094 нулевые. Выпущено в v1.14.1-lx.10. [SPEC.md](TASKS/095-UPSTREAM_SYNC_1_14_1_PLUS_34/SPEC.md) |
| **096** | NAIVE_ENGINE_POOL | **W** | Черновик по просьбе владельца 2026-09-24: каждый naive-outbound поднимает свой движок Cronet на старте (при `insecure_concurrency: N` — N движков вне iOS). Варианты: единый движок на узел (флаг из iOS-ветки cronet-go), ленивый старт при первом дайле, усыпление по простою как у WG, общий движок на группу узлов через форк cronet-go. Оценка 2–5 МБ на движок; замер на эмуляторе и сама задача отложены владельцем до релиза lx.10 и живого naive-сервера. [SPEC.md](TASKS/096-NAIVE_ENGINE_POOL/SPEC.md) |
| **097** | LAZY_WG_DEVICE_BUILD | **I** | Заведена по решению владельца 2026-09-24. Каждый WG/AWG-endpoint держит ≈17.5 МБ стартовых буферов с момента старта ядра (11 узлов — 192 МБ, профиль LxBox 519). Решение: старт в торн-даун состоянии SPEC 020 уровня 3, сборка устройства при первом дайле (`lx.wg.lazy_build`); потолок одновременно собранных устройств `lx.wg.build_max` (иначе пробы urltest соберут все), жертва на разборку — по ссылкам (ручные → авто → давность, решение владельца 2026-09-24), живые соединения не рвутся, `build_overflow` = `wait`/`build`; состояние endpoint'а (`never_built`/`building`/`up`/`asleep`/`torn_down`/`down`) в `GetOutbounds`. Буферы и провод не трогаются. Реализована 2026-09-24, выпущено в v1.14.1-lx.13 (2026-09-24); замер на эмуляторе — при перепине LxBox. [SPEC.md](TASKS/097-LAZY_WG_DEVICE_BUILD/SPEC.md) |
| **098** | LX_ROOT_CONFIG_BLOCK | **I** | Решение владельца 2026-09-24: корневой блок `lx` конфига со всеми глобальными ручками форка по подсистемам — `lx.wg` (три ключа сна переезжают из `route`, плюс `lazy_build`/`build_max`/`build_overflow` для 097), `lx.masque.idle_timeout` (глобальный дефолт, ключ узла сильнее), `lx.naive` (резерв под 096). Старые `route.lx_idle_*` — алиасы на один релиз с предупреждением, конфликт значений — ошибка; running-config отдаёт канонический блок. Порядок: 098 → 097 → релиз после lx.12; выпущено в v1.14.1-lx.13 (2026-09-24). [SPEC.md](TASKS/098-LX_ROOT_CONFIG_BLOCK/SPEC.md) |
| **099** | GETURL_NAIVE_NIL_REMOTEADDR_PANIC | **I** | 4PDA, Huawei P50 Pro, ядро lx.9: Диагностика naive-узла при живом туннеле роняла приложение мгновенно. Пробник `GetURLViaOutbound` звал `info.Conn.RemoteAddr().String()` в трассе GotConn, а conn naive (cronet-go `BidirectionalConn`) отдаёт nil-адрес: паника, конец процесса. Теперь nil-адрес = пустое поле `remoteAddr`. Страж red/green, провод не тронут. Гипотеза о переполнении JNI-ссылок в `system_android.c` отклонена по коду. Выпущено в v1.14.1-lx.10; подтверждение репортёра впереди. [SPEC.md](TASKS/099-GETURL_NAIVE_NIL_REMOTEADDR_PANIC/SPEC.md) |
| **100** | LXD_ROOT_OWNED_BINARY | **D** | Решение владельца 2026-09-24: `lxd --service=install` писал в plist LaunchDaemon путь, из которого запущен (бинарь в бандле лаунчера, владелец — пользователь), и root исполнял user-writable файл — LPE. Служба исполняет root-owned копию `/Library/PrivilegedHelperTools/sing-box-lxd` — плоский файл по соглашению Apple, имя короче 16 символов `comm` (temp+fsync+chown+sha+rename, пропуск при совпадении sha) с сайдкаром `sing-box-lxd.install.json` (`plist_path`, `label`); каталог старой раскладки на месте копии — отказ с подсказкой `sudo rm -rf`; `--exec-dir` с проверкой цепочки от `/`; `--service=copy` — копия без службы для classic TUN лаунчера; `--service=status` с кодами 0/2/3/4; uninstall только по сайдкару; самопроверка `lxd`/`run` под root (отказ только в launchd-службе); `/admin/info` отдаёт `executable`/`executable_sha256`; `GOOS=darwin go vet` в CI. Табличный тест переходов состояний. Выпущено в v1.14.1-lx.11 (файл по ярлыку) и v1.14.1-lx.12 (файл `sing-box-lxd`); живой install на Mac владельца 2026-09-24: копия, plist, гонка bootout→bootstrap закрыта, status OK, сопряжение сохранено. [SPEC.md](TASKS/100-LXD_ROOT_OWNED_BINARY/SPEC.md) |
| **101** | WG_HANDSHAKE_GSO_RETRY_LOG_NOISE | **I** | Заявка LxBox #95 (Proton AWG): `ERROR … failed to send handshake initiation: disabled UDP GSO on …` пугал пользователей, хотя bind уже переотправил пакет без GSO и `RetryErr` был nil. Data-путь форка wireguard-go разворачивал `ErrUDPGSODisabled` через `errors.As` и писал Verbose, хендшейк-пути `SendHandshakeInitiation`/`SendHandshakeResponse` печатали обёртку как ERROR. Один хелпер `unwrapGSODisabled` на три места: Verbose при успешной переотправке, ERROR только с реальной ошибкой повтора. Страж на поддельном bind, red/green. Только сабмодуль (d0568ce) + гитлинк. Выпущено в v1.14.1-lx.13. [SPEC.md](TASKS/101-WG_HANDSHAKE_GSO_RETRY_LOG_NOISE/SPEC.md) |
| **102** | UPSTREAM_SYNC_1_14_2 | **I** | Решение владельца 2026-09-24 (после lx.13): дрейф устранён — мерж `upstream/stable` v1.14.2 (7 коммитов: network reset только по смене интерфейса, `resolved` D-Bus, hysteria2 realm STUN и port hopping, hijack-dns handshake report, `common/dialer`). Сабмодуль `sing-tun` слит с пином ddaa4ca25e3b первым (дельта форка = SPEC 040), `sing-quic` бамп. Четыре конфликта: `go.mod`/`go.sum` (replace-блоки на месте), `service/resolved` ×2 — форма апстрима, нашей дельты нет. Швы SPEC 047/073 в `route/network.go` целы. `upstream.version` → 1.14.2, линия `v1.14.2-lx.N`. Выпущено в v1.14.2-lx.1; прогон на устройстве — при перепине LxBox. [SPEC.md](TASKS/102-UPSTREAM_SYNC_1_14_2/SPEC.md) |
| **103** | LXD_WINDOWS_SERVICE | **D** | Решение владельца 2026-09-24: `lxd` как служба Windows (SCM `sing-box-lxd`, LocalSystem, автозапуск, recovery), перенос модели 100: служба исполняет защищённую копию `<ProgramFiles>\sing-box-lxd\` (exe + `libcronet.dll`, сайдкар с `files[]`/`service`/`warnings`), инвариант по владельцу и DACL от корня тома, захват `<ProgramData>\sing-box-lxd` до записи секрета, install со stop → замена образа → configure → start и откатом на прежний образ, `--service=copy/uninstall/status` с кодами 0/2/3/4/5/1, `svc.Run`, лог `logs\lxd.log` с ротацией копированием, `SetDefaultDllDirectories` и закрепление `libcronet.dll`; `--invite-out`/`--invite-name`, норма имени клиента, именной enroll заменяет запись; джоба CI `test-windows`. Пара SPEC 141 лаунчера; выйдет в v1.14.2-lx.2, живой прогон на Windows — за лаунчер-сессией. [SPEC.md](TASKS/103-LXD_WINDOWS_SERVICE/SPEC.md) |
| **104** | XHTTP_HTTP_VERSION_PARITY | **I** | Issue #25: клиент XHTTP был HTTP/2-only, сервер Xray с `alpn: ["h3"]` не поднимался. Правило Xray по `tls.alpn`: `["h3"]` → HTTP/3 по QUIC через тот же detour, `["http/1.1"]` и без TLS → HTTP/1.1 (было h2c), REALITY → HTTP/2. Новых ключей нет; без `with_quic` конфиг с h3 отвергается. Живой стенд `lx-test/xhttp_h3`. Выпущено в v1.14.2-lx.2; прогон на Android — за владельцем. [SPEC.md](TASKS/104-XHTTP_HTTP_VERSION_PARITY/SPEC.md) |
| **105** | VISION_OVER_VLESS_ENCRYPTION | **I** | Issue #29: `flow: xtls-rprx-vision` вместе с VLESS-шифрованием падал с `not a valid supported TLS connection: *encryption.CommonConn`. `*CommonConn` добавлен в реестр TLS-conn'ов Vision из sing-vmess через `//go:linkname`, прямое копирование — в conn под слоем, как в Xray; транспорт любой, XHTTP тоже. Стенд на живых узлах tcp/xhttp+REALITY зелёный. Выпущено в v1.14.2-lx.3. [SPEC.md](TASKS/105-VISION_OVER_VLESS_ENCRYPTION/SPEC.md) |
| **106** | WG_ENDPOINT_TOGGLE | **I** | Решение владельца: ручное включение и выключение WG/AWG-endpoint'а по gRPC `SetEndpointEnabled` (`with_lx_command`). Выключение усыпляет узел механизмом SPEC 020 (установленные потоки рвутся) и отвергает дайлы ошибкой `WireGuard endpoint is disabled`; включение будит спящий узел сразу, разобранный или несобранный собирает следующий дайл. Состояние `disabled` в `GetOutbounds`; не сохраняется, reload включает все узлы. Ядро без тега. Обёртка libbox `CommandClient.SetEndpointEnabled` для LxBox. Юниты зелёные, не выпущено. [SPEC.md](TASKS/106-WG_ENDPOINT_TOGGLE/SPEC.md) |

> **Вне этого репозитория:** потребление ядра лаунчером (`singbox-launcher`) — парсинг `type=xhttp` в реальный XHTTP-транспорт (сейчас `023` маппит его в `httpupgrade`), AWG-поля в визарде, замена `bin/sing-box`. Это отдельные задачи в репозитории лаунчера.
