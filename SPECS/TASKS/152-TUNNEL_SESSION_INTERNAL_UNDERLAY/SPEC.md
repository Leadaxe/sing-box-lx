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

## 4. Границы — что сознательно не делаем

- **Гонка «выбор до дайла, регистрация после»** в `Selector.DialContext` (апстрим):
  туннель, дайл которого пришёлся на момент переключения, остаётся на старом узле. Окно —
  длительность дайла (у WG до `C.TCPTimeout`). Правка — апстримный `selector.go` и
  `urltest.go`; после этого фикса такой туннель всё равно умрёт при отказе старого узла и
  переподнимется через новый. Цена синка выше пользы — не трогаем.
- **Tailscale.** Через `detour` идут только control и DERP (TCP); прямой UDP-путь
  magicsock слушает через `net.ListenConfig` мимо detour
  (`protocol/tailscale/endpoint.go`, `listenPacket`). Смена узла в detour на прямые
  соединения не влияет по архитектуре, DERP и control переподключаются сами.
- **Проактивный переподъём по событию смены узла** не нужен: обрыв подложки роняет
  сессию, следующий дайл поднимает её заново.
- **Обработка в LxBox** не нужна после этого фикса ядра.

## 5. Условие снятия

Апстрим применит `ContextForMultiplexSession` (или эквивалент) к подложке hysteria2 и
tuic — тогда шов в `lxdatagram.Dialer` становится двойной пометкой (безвредной) и
снимается; masque и trusttunnel — наш код, их шов остаётся. На мерже сверять
`protocol/hysteria2/outbound.go`, `protocol/tuic/outbound.go` и сам
`adapter.ContextForMultiplexSession`: если его семантика изменится (например, перестанет
снимать флаг внешнего соединения), помощник должен снимать флаг сам.
