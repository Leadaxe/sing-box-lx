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

> **Ревизия после lx.9: ленивый dialer-guard (lx.8) удалён, оставлен только
> Start-guard.** Изначально dialer-guard оставляли «вторым эшелоном» для случая
> «селектор по середине». Но: (1) он непроверяем — в UI LxBox detour можно навести
> только на реальный сервер, не на группу; (2) он кэширует результат через
> `sync.Once`, т.е. **не ловит** смену состава/выбора селектора в рантайме; (3) в
> field-тесте он вообще не сработал (виснет в `Start` до dial). Держать
> непроверяемый и частично-неверный механизм хуже, чем не держать. Файлы
> `common/dialer/{detour,dialer}.go` возвращены к upstream. **Селектор-по-середине
> теперь — известный непокрытый случай** (SPEC §5): под него ищется надёжная точка
> перехвата на переключении селектора (`SelectOutbound`/`interruptGroup`).

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
  — её рантайм-цель НЕ ловится (известный непокрытый случай). Защита от циклов — set посещённых.

**`protocol/wireguard/awg_start_guard_test.go`** (новый lx-файл): direct WG,
транзитив через vless→WG, цепь без WG, селектор-в-середине (пропуск), цикл,
неизвестный тег.

## Приёмка (DoD)

- ✅ `go build ./...` без тегов — ок (для плоского WG `awgActive=false` → guard no-op).
- ✅ `go build -tags "with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_xhttp,with_awg" ./cmd/sing-box` — ок.
- ✅ `go test ./protocol/wireguard/...` — 6 подтестов
  `TestAwgDetourChainReachesWireGuard` (Start-guard: direct/транзитив/нет-WG/селектор-пропуск/цикл/unknown).
- ✅ `gofmt -l` изменённых файлов — пусто (урок 006/005 учтён).
- ✅ `go vet ./protocol/wireguard/...` — чисто; `common/dialer` возвращён к upstream.
- ✅ **Field-verified на lx.9 (Android, 2026-06-15 22:21).** AWG→AWG конфиг
  (`warp gen` detour→`🔥☁️ WireGuard + awg`): ядро **поднимается** (не виснет, как
  lx.8), узел `warp gen` не встаёт с ошибкой `amneziawg endpoint will not start:
  its detour chain reaches wireguard-based endpoint …`, остальные узлы работают.
  Смена подхода подтверждена на устройстве.
- Текст ошибки переформулирован (ревизия 2026-06-16): убрано «hangs the kernel on
  Android» из user-сообщения → «amneziawg over wireguard is not supported» (запрет
  по архитектуре, не платформа); технический «почему» оставлен в комментариях кода.

## Зона касания upstream (для ребейза)

- `protocol/wireguard/endpoint.go` — upstream-файл, правки **только** в `// lx:`
  блоках (Start-guard). Конфликт на ребейзе — лишь если upstream перепишет
  `Endpoint.Start` или его структуру.
- `common/dialer/detour.go`, `common/dialer/dialer.go` — ревизией 2026-06-16
  **возвращены к upstream** (lx.8-guard откачен) → конфликтов не добавляют.
- `awg_start_guard_test.go` — lx-собственный, конфликтов не даёт.

## Вне скоупа

- **Селектор/urltest по середине** detour-цепи — известный непокрытый случай
  (SPEC §5): Start-guard на группе останавливается, ленивый guard удалён. Ищется
  надёжный хук на переключении селектора.
- **Лечение первопричины** в `submodules/wireguard-go` (таймауты/неблокирующая
  отправка junk; смежный баг `jmin>jmax` закрыт задачей 008) — отдельная задача.
- Цепочки AWG-over-WireGuard через route-rule action, а не `detour`.
