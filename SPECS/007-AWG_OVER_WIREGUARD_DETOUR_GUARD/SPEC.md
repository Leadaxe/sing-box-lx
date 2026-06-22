# SPEC: 007 — AWG_OVER_WIREGUARD_DETOUR_GUARD

| Поле | Значение |
|------|----------|
| Тип | B (bug) |
| Статус | C (complete) |

Отклонять (по образцу ядрового запрета «empty direct detour») конфигурацию, где
AmneziaWG-endpoint (источник с AWG-полями) имеет `detour` на **любой
WireGuard-based** endpoint — плоский WireGuard **или** AmneziaWG. По пакетам это
AWG-трафик, инкапсулированный внутри WireGuard-туннеля; на Android такая связка
**вешает ядро**. `detour` AWG-ноды на не-WireGuard outbound (VLESS, Trojan,
direct, …) — рабочий сценарий и остаётся разрешённым.

Баг в фиче [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT) **нашей**
дельты (AWG-проводка + merged-форк wireguard-go), не upstream → в скоупе по
CONSTITUTION §3.1.

---

## 1. Проблема / контекст

Матрица из тестов на устройстве (автор):

| Источник (`detour`-ит) | Цель | Результат |
|---|---|---|
| **AWG** | **WG** (плоский WireGuard) | ❌ беда (ядро виснет / handshake не уходит) |
| **AWG** | **AWG** | ❌ беда |
| **AWG** | **VLESS** | ✅ работает |
| **WG** (без AWG-полей) | AWG | ✅ работает |

Вывод: триггер — **источник AWG + цель = любой WireGuard-туннель**, а не «два junk-слоя».
По пакетам — AWG внутри WG. По конфигу — «у ноды AWG прописан `detour` на WG/AWG».

Механика (статический разбор `submodules/wireguard-go`):
`SendHandshakeInitiation` (device/send.go) синхронно генерирует junk и зовёт
`SendBuffers` → `bind.Send()` **без таймаута на запись**, удерживая
`device.net.RLock()`. Когда AWG-трафик заворачивается в WireGuard-устройство,
запись блокируется на нижнем туннеле; на Android (нет watchdog) это проявляется
как зависание. Точную первопричину статикой **не доказали** — для guard это и не
нужно: цель — **быстро отклонять** заведомо опасную связку, пока (отдельной
задачей) не вылечена сама блокировка в wireguard-go.

## 2. Цель

AWG-нода с `detour` на WireGuard-based цель **не поднимает соединение**.
Поведение — **вариант B** (согласовано с автором и
[LxBox §128](https://github.com/Leadaxe/LxBox/blob/develop/docs/spec/tasks/128-force-direct-out-detour.md)):
ядро стартует, остальные узлы работают, эта нода не встаёт, ошибка в логе. **Не
крашимся.**

> **Ревизия 2026-06-16 (см. IMPLEMENTATION_REPORT):** реализация прошла итерации.
>
> 1. **lx.8 — ленивый guard в `DetourDialer.init()`** (на первом dial). Field-тест
>    показал: **не срабатывает** — AWG→WG виснет синхронно в `Endpoint.Start`
>    (резолв peer-домена через detour + junk-handshake), **до** первого dial.
>    **Удалён** (непроверяем в UI, `sync.Once`-кэш не ловит смену селектора).
> 2. **lx.9 — Start-guard в `protocol/wireguard.Endpoint.Start`** (статический обход
>    транзитивной detour-цепи; device не поднимается). **Field-verified на Android.**
> 3. **selector-guard в `protocol/group` (`SelectOutbound`)** — закрывает случай
>    «селектор по середине», который Start-guard статически пропускает: при
>    переключении селектора на член, ведущий к WireGuard, **до** коммита выбора
>    гасятся (suspend → `device.Down`, `started=false`) все AmneziaWG-потребители,
>    что detour-ят на эту группу (транзитивно вверх через `ConsumersOf`).
>
> **Итог: два дополняющих guard'а — Start-guard (статический, прямая цепь) +
> selector-guard (рантайм, переключение селектора).** Оба дают вариант B и не
> крашатся. Гашение selector-guard'ом — **до** переключения, поэтому AWG-потребитель
> опущен раньше, чем группа укажет на WG → гонки нет.

## 3. Требования

### 3.1 Критерий «источник»
- Источник-триггер — AWG-endpoint: `option.AmneziaWGOptions.IsSet()` (любое
  AWG-поле). Плоский WG источником-триггером **не** является (WG→AWG разрешён).

### 3.2 Критерий «цель»
- Цель-триггер — **любой WireGuard-based outbound**: `Type() == C.TypeWireGuard`
  (один тип `"wireguard"` покрывает и плоский WG, и AWG — AWG отличается лишь
  набором полей, тип тот же). Решение автора: детектировать **по типу**.
- Цепочка обходится **транзитивно** по `detour` (через `OutboundManager.Outbound`
  + `Dependencies()`): AWG→X→…→WG ловится на любой глубине. Защита от циклов — set
  посещённых тегов.
- На группе (selector/urltest) Start-guard обход **останавливается** — выбранный
  член рантайм-зависим; этот случай ловит selector-guard (§3.3). Не-WireGuard цели
  (VLESS и т.д.) — **не** триггер.

### 3.3 Где ловить — два guard'а

**(a) Start-guard** — `protocol/wireguard.Endpoint.Start` (стадия `StartStateStart`),
**до** `w.endpoint.Start()`. Зависание происходит синхронно в `Start` (резолв
peer-домена через detour + junk-handshake), до первого dial — ленивый guard туда не
успевает (доказано на lx.8). Источник — `Endpoint.awgActive`
(`AmneziaWGOptions.IsSet()`); цель — `awgDetourChainReachesWireGuard(...)`
(транзитивный обход detour, **на группе останавливается**). Поведение — **вариант B**:
device не поднимается, `started=false`, `return nil` (НЕ error — иначе abort
инстанса), ошибка в лог. Все outbound'ы зарегистрированы до любого `Start`.

**(b) selector-guard** — `protocol/group.Selector.SelectOutbound`, **до** коммита
выбора (`s.selected.Store`). Если новый член ведёт к WireGuard
(`chainReachesWireGuard`: сам тип / detour вниз / вложенные группы), идём **вверх**
по `OutboundManager.ConsumersOf` (reverse-deps, транзитивно: AWG→vless→group) и для
каждого потребителя с `IsAmneziaWG()` зовём `SuspendAmneziaWG()` (`device.Down`,
`started=false`). Гашение **до** `Store` → к моменту переключения AWG уже опущен,
его reconnect вернёт «not ready» → junk в WG не уйдёт (**гонки нет**). Лог — при
реальном гашении (`CompareAndSwap(true,false)`), без спама на повторных переключениях.

### 3.4 Изоляция (CONSTITUTION §3.2–3.3)
- Start-guard — `// lx:` блоки в `protocol/wireguard/endpoint.go` +
  `transport/wireguard/endpoint.go` (`Suspend()`); тест `awg_start_guard_test.go`.
- selector-guard — новый файл `protocol/group/awg_selector_guard.go` + один вызов в
  `selector.go`; маркер `adapter.AmneziaWGSuspendable` и `OutboundManager.ConsumersOf`
  в `adapter/outbound.go` + `adapter/outbound/manager.go` (`// lx:`); тест
  `awg_selector_guard_test.go`.
- `common/dialer/{detour,dialer}.go` ревизией 2026-06-16 **возвращены к upstream**.
- Поведение **без** `with_awg` не меняется: AWG-конфиг отвергается раньше; для
  плоского WG `awgActive=false`/`IsAmneziaWG()==false` → guard'ы no-op.

## 4. Критерии приёмки

- AWG `detour`→WG и AWG `detour`→AWG: узел не поднимается, ошибка в лог; ядро и
  прочие узлы живут (вариант B). ✅ field-verified на Android lx.9.
- AWG `detour`→VLESS и WG `detour`→AWG: проходит.
- AWG→X→…→WG (транзитивно по detour): ловится Start-guard'ом; циклы не виснут.
- Переключение селектора на WG-член при AWG-потребителе: AWG suspend **до** выбора;
  не-AWG потребители не трогаются; переключение на не-WG ничего не гасит.
- Юнит-тесты зелёные: `awgDetourChainReachesWireGuard` (Start-guard) +
  `chainReachesWireGuard`/`suspendAmneziaWGConsumers` (selector-guard).
- `go build ./...` без тегов — ок; сборка с `with_awg` — ок; `gofmt -l` пусто.

## 5. Вне скоупа

- **Гонка selector-guard:** закрыта порядком (гасим до `Store`). Остаётся
  теоретический случай, если потребитель переподключается строго между нашим
  suspend и его собственным dial в том же тике — практически невозможен, т.к. suspend
  синхронен и до коммита выбора.
- **Лечение первопричины** (таймауты/неблокирующая отправка junk в
  `submodules/wireguard-go`) — отдельная будущая задача.
- Цепочки через route-rule action, а не `detour` — вне скоупа.

## 6. Ссылки

- Фича [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT)
- LxBox §128 — образец поведения «вариант B» (ленивая detour-ошибка, ядро живёт):
  `Leadaxe/LxBox/docs/spec/tasks/128-force-direct-out-detour.md`
- `submodules/wireguard-go/device/send.go` (junk-генерация в SendHandshakeInitiation)
- Образец detour-проверки: `common/dialer/detour.go` (empty-direct, upstream `fb622ccb`)
