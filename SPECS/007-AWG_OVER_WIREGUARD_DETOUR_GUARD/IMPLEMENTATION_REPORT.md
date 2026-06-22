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

**Решение — Start-guard** (`protocol/wireguard.Endpoint.Start`): статический обход
транзитивного замыкания `detour` через `OutboundManager` + `Dependencies()`; при
достижении `Type()==wireguard` — device не поднимается, `started=false`, лог,
`return nil`. **Вариант B**: ядро и прочие узлы живут. На группе (selector/urltest)
обход **останавливается** (цель рантайм-зависима). Field-verified на Android lx.9.

> **Ревизия после lx.9: ленивый dialer-guard (lx.8) удалён.** Изначально его
> оставляли «вторым эшелоном» для случая «селектор по середине». Но: (1) непроверяем
> — в UI LxBox detour наводится только на реальный сервер, не на группу;
> (2) кэширует через `sync.Once` → **не ловит** смену селектора в рантайме; (3) в
> field-тесте вообще не сработал (виснет в `Start` до dial). Файлы
> `common/dialer/{detour,dialer}.go` возвращены к upstream.

**Решение «селектор по середине» — selector-guard** (`protocol/group.Selector.SelectOutbound`):
при переключении селектора на член, ведущий к WireGuard, **до** коммита выбора
(`s.selected.Store`) идём вверх по `OutboundManager.ConsumersOf` (reverse-deps,
транзитивно) и для каждого AmneziaWG-потребителя зовём `SuspendAmneziaWG()`
(`device.Down`, `started=false`). Гашение **до** переключения закрывает гонку: к
моменту, когда группа укажет на WG, AWG-потребитель уже опущен, его reconnect →
«not ready» → junk в WG не уйдёт. Маркер `adapter.AmneziaWGSuspendable` и метод
`OutboundManager.ConsumersOf` добавлены, чтобы `protocol/group` действовал без
импорта `protocol/wireguard`.

**Итог: два дополняющих guard'а** — Start-guard (статическая прямая цепь,
field-verified) + selector-guard (рантайм-переключение селектора). Оба — вариант B.

Главный инвариант: **ядро поднимать, коннект не стартовать, ошибку в лог.**

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

### Итерация 1 (lx.8) — ленивый dialer-guard, **откачена**

Поведение копировало ядровой запрет `detour to an empty direct outbound`
(upstream `fb622ccb`) — guard в `DetourDialer.init()` (`common/dialer/detour.go`),
флаг владельца через `dialer.Options.IsAmneziaWG`. **Не сработал на устройстве**
(см. выше) и удалён ревизией 2026-06-16: `common/dialer/{detour,dialer}.go`
возвращены к upstream, тест `awg_detour_guard_test.go` удалён. Описание оставлено
как история — в текущем коде этого guard нет.

### Итерация 2 (lx.9) — Start-guard (актуальное)

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
  — её рантайм-цель ловит selector-guard (ниже). Защита от циклов — set посещённых.

**`protocol/wireguard/awg_start_guard_test.go`** (новый lx-файл): direct WG,
транзитив через vless→WG, цепь без WG, селектор-в-середине (пропуск), цикл,
неизвестный тег.

### Итерация 3 — selector-guard (рантайм-переключение)

- **`adapter/outbound.go`** (`// lx:`): метод `OutboundManager.ConsumersOf(tag)`
  (reverse-deps) + интерфейс-маркер `AmneziaWGSuspendable {IsAmneziaWG; SuspendAmneziaWG}`.
- **`adapter/outbound/manager.go`** (`// lx:`): реализация `ConsumersOf` из
  `dependByTag` (копия под RLock).
- **`protocol/wireguard/endpoint.go`** (`// lx:`): `IsAmneziaWG()` и
  `SuspendAmneziaWG()` (CAS `started`→false + лог при реальном гашении; `Suspend()`
  device).
- **`transport/wireguard/endpoint.go`** (`// lx:`): `Suspend()` → `device.Down()`
  без Close (идемпотентно).
- **`protocol/group/selector.go`** (`// lx:`): в `SelectOutbound` — `Swap`→`Load`+`Store`,
  вызов `suspendAmneziaWGConsumersOnWireGuardSwitch` **до** `Store`.
- **`protocol/group/awg_selector_guard.go`** (новый lx-файл): `chainReachesWireGuard`
  (член ведёт к WG?) + `suspendAmneziaWGConsumers` (обход вверх по `ConsumersOf`).
- **`protocol/group/awg_selector_guard_test.go`** (новый lx-файл): тесты.

## Приёмка (DoD)

- ✅ `go build ./...` без тегов — ок (для плоского WG `awgActive=false`/`IsAmneziaWG()==false` → guard'ы no-op).
- ✅ `go build -tags "with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_xhttp,with_awg" ./cmd/sing-box` — ок.
- ✅ `go test ./protocol/wireguard/...` — `TestAwgDetourChainReachesWireGuard` (Start-guard, 6 кейсов).
- ✅ `go test ./protocol/group/...` — `chainReachesWireGuard` + `suspendAmneziaWGConsumers`
  + `…SkippedForNonWireGuardSwitch` (selector-guard: прямой/транзитивный AWG suspend,
  не-AWG не трогается, не-WG переключение — no-op).
- ✅ `gofmt -l` изменённых файлов — пусто (урок 006/005 учтён).
- ✅ `go vet ./protocol/wireguard/... ./protocol/group/... ./adapter/...` — чисто.
- ✅ **Field-verified на lx.9 (Android, 2026-06-15 22:21)** — Start-guard: AWG→AWG
  конфиг, ядро поднимается, узел не встаёт, остальное работает. selector-guard —
  юнит-тестами (в UI LxBox недостижим: detour только на реальные серверы).
- Текст ошибок — единый, по архитектуре: «amneziawg over wireguard is not supported».

## Зона касания upstream (для ребейза)

- `protocol/wireguard/endpoint.go`, `transport/wireguard/endpoint.go`,
  `protocol/group/selector.go`, `adapter/outbound.go`, `adapter/outbound/manager.go`
  — upstream-файлы, правки **только** в `// lx:` блоках. Конфликт на ребейзе — лишь
  если upstream перепишет `Endpoint.Start`/`Endpoint.Close`, `Selector.SelectOutbound`,
  интерфейс `OutboundManager` или конструкторы.
- `common/dialer/{detour,dialer}.go` — ревизией 2026-06-16 **возвращены к upstream**.
- `awg_start_guard_test.go`, `awg_selector_guard.go`, `awg_selector_guard_test.go`
  — lx-собственные, конфликтов не дают.

## Вне скоупа

- **Лечение первопричины** в `submodules/wireguard-go` (таймауты/неблокирующая
  отправка junk; смежный баг `jmin>jmax` закрыт задачей 008) — отдельная задача.
- Цепочки AWG-over-WireGuard через route-rule action, а не `detour`.
