# IMPLEMENTATION_REPORT — 007 AWG_OVER_WIREGUARD_DETOUR_GUARD

**Дата:** 2026-06-15, ревизия 2026-06-16 · **Статус:** Complete (код + тесты + DoD; смена подхода после field-теста) · **База:** `v1.13.13`

## Итог

AmneziaWG-нода, чей `detour` (прямо или по транзитивной цепочке) ведёт на
**любой WireGuard-based** endpoint (плоский WG или AWG), больше не вешает ядро на
Android: такая связка отвергается (**вариант B** — ядро и остальные узлы живут,
этот узел не поднимается, ошибка в лог). `detour` AWG-ноды на не-WireGuard (VLESS
и т.д.) — разрешён.

## ⚠️ Смена подхода (2026-06-16) — два эшелона вместо одного

**Первая реализация (lx.8) ловила только лениво в `DetourDialer.init()` — и на
устройстве не сработала.** Field-тест AWG→AWG-конфига на Android (lx.8, logcat
`/tmp/logcat_brokennode.txt`): ядро виснет в `Starting` — последняя строка
`LxBoxNet: defaultNetwork`, затем 27 c тишины, `Libbox.newService()` не
возвращает управление; нашей guard-ошибки `connect to server: amneziawg…` в логе
**нет вовсе**.

**Почему ленивый guard не сработал:** он живёт в `DetourDialer.init()`, который
вызывается из `ClientBind.connect()` — то есть **при первом dial**, из bind-горутин.
А зависание происходит **синхронно в `Endpoint.Start`**, раньше dial:
`transport/wireguard/endpoint.go` `Start(resolve=true)` для peer с доменным
адресом (в конфиге — `mfi.tribukvy.ltd`) синхронно зовёт `ResolvePeer` **через
detour** (нижний туннель), + device запускает junk-handshake. До `connect()` дело
не доходит → ленивый guard архитектурно мёртв для этого сценария.

**Решение — два эшелона, дополняющие друг друга:**

| Эшелон | Ловит | Где | Поведение |
|--------|-------|-----|-----------|
| **Start-guard** (новый) | `AWG→…→WG` по **прямой транзитивной** цепочке detour (без селектора по середине) | `protocol/wireguard.Endpoint.Start`, статический обход transitive-замыкания `detour` через `OutboundManager`; на группе **останавливается** | device не поднимается, `started=false`, лог — ядро/прочие узлы живут |
| **Dialer-guard** (из lx.8, оставлен) | случай с **селектором/urltest** по середине — цель рантайм-зависима | `common/dialer/detour.go` `DetourDialer.init()`, лениво, раскрывает группы | ленивая ошибка на dial |

Start-guard на селекторе пасует (цель неизвестна статически) → отдаёт этот случай
dialer-guard'у в рантайме. Прямую цепь dialer-guard не успел бы поймать (виснет в
Start) → её берёт Start-guard. Главный инвариант обоих: **ядро поднимать, коннект
не стартовать, ошибку в лог.**

Баг **нашей** дельты (фича 003 AWG2), не upstream → в скоупе (CONSTITUTION §3.1).

## Диагноз (по данным с устройства)

Матрица из тестов автора:

| Источник (`detour`-ит) | Цель | Результат |
|---|---|---|
| **AWG** | **WG** | ❌ беда |
| **AWG** | **AWG** | ❌ беда |
| **AWG** | **VLESS** | ✅ работает |
| **WG** | AWG | ✅ работает |

⇒ триггер — **источник AWG + цель = WireGuard-туннель** (по пакетам: AWG внутри
WG). Не «два junk-слоя», как предполагалось вначале, и не сам `detour`.

Механика (статич. разбор `submodules/wireguard-go/device/send.go`):
`SendHandshakeInitiation` синхронно генерирует junk и зовёт `SendBuffers` →
`bind.Send()` **без таймаута**, держа `device.net.RLock()`. Когда AWG-трафик
инкапсулируется в WireGuard-устройство, запись блокируется на нижнем туннеле; на
Android (нет watchdog/перезапуска) — зависание. Первопричину статикой **не
доказывали** — задача намеренно про **guard**, не про лечение блокировки.

## Что сделано

Поведение скопировано с ядрового запрета `detour to an empty direct outbound`
(upstream `fb622ccb`) — тот же ленивый `init()`-механизм даёт «вариант B»
бесплатно (образец взят из [LxBox §128](https://github.com/Leadaxe/LxBox/blob/develop/docs/spec/tasks/128-force-direct-out-detour.md)).

**`common/dialer/detour.go`** (`// lx:` upstream):
- поле `DetourDialer.ownerIsAmneziaWG`; новый 4-й параметр `NewDetour`;
- guard в `init()` сразу после empty-direct: если владелец AWG **и**
  `detourTargetIsWireGuard(...)` → кэшируем `initErr` («amneziawg endpoint cannot
  detour through a wireguard-based endpoint … use a non-wireguard detour»);
- `detourTargetIsWireGuard` — рекурсивный обход: цель с `Type()==C.TypeWireGuard`
  → true; группа (`adapter.OutboundGroup`) раскрывается через `All()`; set
  посещённых тегов против циклов.

**`common/dialer/dialer.go`** (`// lx:` upstream): поле `Options.IsAmneziaWG`,
проброс в `NewDetour`.

**`protocol/wireguard/endpoint.go`** (`// lx:` AWG-шов): в `dialer.Options`
выставляется `IsAmneziaWG: options.AmneziaWGOptions.IsSet()` — это и есть «источник AWG».

**`common/dialer/awg_detour_guard_test.go`** (новый lx-файл): фейковые
`Outbound`/`OutboundGroup`/`OutboundManager`; покрыта матрица + init-путь.

### Start-guard (ревизия 2026-06-16)

**`protocol/wireguard/endpoint.go`** (`// lx:` AWG-шов):
- поля `Endpoint.awgActive` (= `AmneziaWGOptions.IsSet()`), `detour`,
  `awgChainBlocked`;
- в `Start` (стадия `StartStateStart`), если `awgActive` и есть `detour`:
  `awgDetourChainReachesWireGuard(outboundManager, detour, …)` обходит
  **транзитивную** цепочку detour (через `OutboundManager.Outbound` +
  `Dependencies()`), и если достигает `Type()==wireguard` — логирует ошибку, ставит
  `awgChainBlocked=true`, **не вызывает** `w.endpoint.Start` и возвращает `nil`
  (вариант B: `started` остаётся false, dial узла → «WireGuard is not ready yet»,
  device с junk не поднимается → нет зависания). `PostStart` тоже пропускается.
- `awgDetourChainReachesWireGuard` **останавливается на группе** (selector/urltest)
  — её рантайм-цель ловит ленивый dialer-guard. Защита от циклов — set посещённых.

**`protocol/wireguard/awg_start_guard_test.go`** (новый lx-файл): direct WG,
транзитив через vless→WG, цепь без WG, селектор-в-середине (пропуск), цикл,
неизвестный тег.

## Приёмка (DoD)

- ✅ `go build ./...` без тегов — ок (для плоского WG `awgActive=false` → guard no-op).
- ✅ `go build -tags "with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_xhttp,with_awg" ./cmd/sing-box` — ок.
- ✅ `go test ./common/dialer/...` — 8 подтестов (dialer-guard: матрица + init-путь).
- ✅ `go test ./protocol/wireguard/...` — 6 подтестов
  `TestAwgDetourChainReachesWireGuard` (Start-guard: direct/транзитив/нет-WG/селектор-пропуск/цикл/unknown).
- ✅ `gofmt -l` изменённых файлов — пусто (урок 006/005 учтён).
- ✅ `go vet ./common/dialer/... ./protocol/wireguard/...` — чисто.
- ✅ **Field-verified на lx.9 (Android, 2026-06-15 22:21).** AWG→AWG конфиг
  (`warp gen` detour→`🔥☁️ WireGuard + awg`): ядро **поднимается** (не виснет, как
  lx.8), узел `warp gen` не встаёт с ошибкой `amneziawg endpoint will not start:
  its detour chain reaches wireguard-based endpoint …`, остальные узлы работают.
  Смена подхода подтверждена на устройстве.
- Текст ошибки переформулирован (ревизия 2026-06-16) по фидбэку: убрано
  «hangs the kernel on Android» из user-сообщения → «amneziawg over wireguard is
  not supported» (запрет по архитектуре, а не привязка к платформе). Касается обоих
  guard'ов (Start + dialer); технический «почему» оставлен в комментариях кода.

## Зона касания upstream (для ребейза)

- `common/dialer/detour.go`, `common/dialer/dialer.go`,
  `protocol/wireguard/endpoint.go` — upstream-файлы, правки **только** в `// lx:`
  блоках. Конфликт на ребейзе — лишь если upstream перепишет `DetourDialer.init`,
  сигнатуру `NewDetour`, `dialer.Options` или конструктор endpoint.
- Сигнатура `NewDetour` расширена 4-м параметром (`ownerIsAmneziaWG`) —
  единственный внешний вызов (в `dialer.go`) обновлён; других в дереве нет.
- `awg_detour_guard_test.go` — lx-собственный, конфликтов не даёт.

## Вне скоупа

- **Лечение первопричины** в `submodules/wireguard-go` (таймауты/неблокирующая
  отправка junk; проверка `jmin<=jmax` в device/uapi.go — отдельный найденный
  баг: при `jmin>jmax` `rand.Int` паникует) — отдельная будущая задача.
- Цепочки AWG-over-WireGuard, построенные через route-rule action, а не `detour`.
