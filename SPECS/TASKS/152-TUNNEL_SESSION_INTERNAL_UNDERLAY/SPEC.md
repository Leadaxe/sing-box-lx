# SPEC: 152 — TUNNEL_SESSION_INTERNAL_UNDERLAY

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (апстримная асимметрия с mux, держим патч); затронуты [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) и [TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md).

| Поле | Значение |
|------|----------|
| Тип | B (bug) — туннель с `detour` на селектор остаётся на старом узле после смены выбора, пока жива его сессия |
| Статус | I (implemented) — 2026-11-03: помощник, четыре шва, юниты; на стенде и устройстве не прогонялось |
| Ветка | `lx` |
| Build-tag | — (`common/dialer`, `common/lxdatagram`, `protocol/masque` собираются всегда; шов hysteria2/tuic — строка SPEC 150 под апстримным `with_quic`; trusttunnel под `with_trusttunnel`) |
| Источник | Разбор владельца по жалобе LxBox 2026-11-03: «после смены узла в Направлении туннель надо перезапускать руками» |
| Связано | [146](../146-MASQUE_CLOSE_WEDGE_DETOUR_DEADLINE/SPEC.md), [150](../150-QUIC_DETOUR_OVERSIZE_DATAGRAM/SPEC.md), [151](../151-QUIC_SNIFF_DECOY/SPEC.md) (тот же шов `lxdatagram.Dialer` у hysteria2/tuic), [084](../084-INTERRUPT_GROUP_ABBA_DEADLOCK/SPEC.md) (interrupt-группа селектора), [133](../133-TRUSTTUNNEL_OUTBOUND/SPEC.md) (сессия TrustTunnel), [021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) (сессия MASQUE) |

## 0. TL;DR

Селектор при смене узла закрывает сокеты, открытые через него, но «внешние» —
пришедшие от пользователя через роутер — только при `interrupt_exist_connections: true`
(`common/interrupt/group.go`, `protocol/group/selector.go`). Туннельные outbound'ы
`masque`, `trusttunnel`, `hysteria2`, `tuic` поднимают одну сессию в контексте
**первого пользовательского соединения**, которому она понадобилась, и обслуживают через
неё все следующие. Роутер помечает этот контекст внешним (`route/route.go`), поэтому
подложка сессии регистрировалась в группе селектора как внешняя и без галки переживала
смену узла: туннель жил через старый узел, пока сессия не умирала сама.

Апстрим ту же ошибку у своих мультиплексных клиентов (mux, anytls, ssh, snell,
gRPC/HTTP-транспорты) закрыл через `adapter.ContextForMultiplexSession`: подложка сессии
дайлится во внутреннем контексте. На hysteria2 и tuic это не перенесено, а наши masque и
trusttunnel повторили апстримный паттерн. WireGuard/AWG этой проблемы не имели: bind
дайлит из собственного `bindCtx`, флага внешнего соединения там нет, сокет рвётся всегда.

Фикс: та же пометка для подложки сессии всех четырёх протоколов. Теперь при смене узла
селектор рвёт подложку независимо от галки, сессия умирает, следующий дайл поднимает её
через новый узел. Соединения внутри старой сессии рвутся — как и у mux, иначе их не
перевести.

## 1. Механизм

| Протокол | Где дайлится подложка | Контекст до фикса |
|---|---|---|
| `masque` | `ensureSession(ctx)` → `connectH3WithBudget`/`connectH2` → `o.dialer.DialContext(ctx, …)` | пользовательский, внешний |
| `trusttunnel` | `getSession(ctx)` → `connectQUIC`/`connectHTTP2` → `c.dialer.DialContext(ctx, …)` | пользовательский, внешний |
| `hysteria2` | sing-quic `offerNew(ctx)` → `c.dialer.ListenPacket(ctx, …)` | пользовательский, внешний |
| `tuic` | sing-quic `offerNew(ctx)` → `c.dialer.ListenPacket(ctx, …)` | пользовательский, внешний |
| `wireguard`/AWG endpoint | `ClientBind.connect` из `bindCtx` | внутренний — проблемы нет |

`Selector.DialContext`/`ListenPacket` регистрируют conn с флагом
`interrupt.IsExternalConnectionFromContext(ctx)`; `Interrupt(interruptExternalConnections)`
закрывает внешние только при включённой галке. Внутренние — всегда.

## 2. Решение

Один помощник `dialer.NewSessionDialerLx(N.Dialer)` (`common/dialer/session_lx.go`):
оборачивает `DialContext` и `ListenPacket`, применяя `adapter.ContextForMultiplexSession`
— ровно то, что апстрим делает для mux (флаг внешнего соединения снят, тег DNS-транспорта
сброшен, `InboundContext` новый с одним `Outbound`). Четыре шва:

| Файл | Шов |
|---|---|
| `common/lxdatagram/truncate.go` | `Dialer(...)` оборачивает входной дайлер помощником — hysteria2 и tuic получают фикс через свою строку SPEC 150, новых правок в апстримных `outbound.go` нет |
| `protocol/masque/outbound.go` | `dialer: dialer.NewSessionDialerLx(outboundDialer)` в конструкторе |
| `protocol/trusttunnel/outbound.go` | `Dialer: dialer.NewSessionDialerLx(outboundDialer)` в опциях клиента |

Остальные дайлы этих outbound'ов (DNS-резолв назначения через `dnsRouter`, стримы
внутри сессии) контекст пользователя сохраняют — обёрнут только дайлер подложки.

## 3. Проверка

- `common/dialer`: `TestSessionDialerMarksUnderlayInternal` — после обёртки флаг снят
  на обоих методах, метаданные свежие, `Upstream()` отдаёт исходный дайлер.
- `common/lxdatagram`: `TestDialerMarksSessionUnderlayInternal` — шов hysteria2/tuic.
- Пакеты `protocol/masque`, `protocol/trusttunnel`, `transport/trusttunnel` под `-race`
  зелёные; сборка под `with_quic,with_trusttunnel`.
- Не прогнано: живой стенд «masque/trusttunnel за селектором из двух узлов без галки,
  смена узла → в логе сессия закрыта и поднята через новый». Ожидаемый лог masque:
  `masque: tunnel died: … closed` сразу после переключения, затем `establishing … tunnel`.

## 4. Исследование — открытые пункты задачи

Три остатка разбора 2026-11-03. Фикс §2 их не закрывает. **Решение владельца
2026-11-03: по всем трём не делаем** — ни кода, ни отдельных задач; пункты остаются здесь
как запись того, что установлено, и как условия, при которых решение пересматривается.

### 4.1. Гонка «выбор до дайла, регистрация после» в селекторе

**Установлено по коду.** `Selector.DialContext`/`ListenPacket` (апстрим,
`protocol/group/selector.go`) читают `s.selected` до дайла, а регистрируют conn в
interrupt-группе после него. `SelectOutbound`, пришедший в окно дайла, вызывает
`Interrupt` до регистрации — этот conn никто не закрывает, туннель поднимается через
старый узел. Окно = длительность дайла: у WG/AWG до `C.TCPTimeout`
(`clientBindDialTimeout`), у masque/trusttunnel — дайл подложки плюс хендшейк. Окно
реально именно тогда, когда переключают: старый узел полуживой, дайл через него долгий.
То же в `urltest.go` и `chain.go` (тот же паттерн регистрации).

**Что проверить.** Воспроизводится ли в поле: смена узла во время подъёма туннеля через
полуживой узел, затем проверка `RealTag`/трассы SPEC 139, через какой узел живёт сессия.

**Варианты, если воспроизводится.** (а) После дайла сверить `s.selected.Load()` с
выбранным до дайла и закрыть conn при расхождении — одна проверка в двух методах,
апстримный файл, `// lx:` блок; тот же блок в `urltest.go` и `chain.go`. (б) Регистрация
до дайла — ломает контракт interrupt-группы (закрытие nil-conn), не вариант. (в) Не
трогать: после §2 такой туннель умрёт при отказе старого узла и переподнимется через
новый. **Решение: (в), не делаем.** Пересмотр — только по воспроизводимому полевому
случаю с трассой SPEC 139, где сессия после смены узла живёт через старый.

### 4.2. Tailscale: прямой путь мимо `detour`

**Установлено по коду.** Через дайлер endpoint'а (и значит через `detour`) идут только
control-соединение и DERP (TCP). Прямой UDP-путь magicsock слушает через
`net.ListenConfig` в `listenPacket` (`protocol/tailscale/endpoint.go`, хук
`netns.SetListenPacketFunc`) — `detour` на него не влияет по архитектуре. DERP и control
переподключаются сами (`derphttp`, `controlclient`), ручной перезапуск им не нужен.

**Что проверить.** Что именно ожидает пользователь от `detour` у Tailscale: если
«весь трафик узла через Направление», то ожидание не выполняется никогда, и это надо
проговорить в документации endpoint'а (границы FEATURE), а не чинить. Если только
control/DERP — после смены узла они переедут сами, проверить на живом tailnet по логам
`derphttp`.

**Решение: не делаем.** Прямой путь мимо `detour` — свойство Tailscale, править нечего;
проверка на tailnet не планируется, пока нет жалобы именно на control/DERP после смены
узла.

### 4.3. Поведение QUIC-туннелей и AWG на обрыве подложки

**Установлено по коду и библиотекам.** Обёртка interrupt закрывает нижележащий conn;
quic-go `Transport.listen` получает ошибку чтения, закрывает транспорт и все его
соединения — до idle-таймаута ждать не придётся (masque h3, trusttunnel h3, hysteria2,
tuic). WG/AWG: смена адреса источника после переподъёма сокета — штатный роуминг ядра
WireGuard, endpoint пира обновляется по любому валидному аутентифицированному пакету,
новое рукопожатие не требуется, магические заголовки AWG на это не влияют.

**Что проверить.** Стенд §3 (два узла, смена без галки) с замером времени от
переключения до первого ответа через новый узел: для QUIC-туннелей ожидание — секунды,
не `idle_timeout`; для AWG — что жёсткий сервер (AWG 1.5+/3.x с `I1`–`I5`) принимает
пакет с нового адреса без повторного `I1`.

**Решение: не делаем.** Стенд не ставим; поведение следует из кода quic-go и ядра
WireGuard. Пересмотр — по полевому отчёту, где после смены узла QUIC-туннель молчит
до `idle_timeout` или AWG не поднимается без ручного перезапуска.

## 5. Условие снятия

Апстрим применит `ContextForMultiplexSession` (или эквивалент) к подложке hysteria2 и
tuic — тогда шов в `lxdatagram.Dialer` становится двойной пометкой (безвредной) и
снимается; masque и trusttunnel — наш код, их шов остаётся. На мерже сверять
`protocol/hysteria2/outbound.go`, `protocol/tuic/outbound.go` и сам
`adapter.ContextForMultiplexSession`: если его семантика изменится (например, перестанет
снимать флаг внешнего соединения), помощник должен снимать флаг сам.
