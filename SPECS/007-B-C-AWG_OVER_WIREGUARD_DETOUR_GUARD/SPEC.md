# SPEC: 007 — AWG_OVER_WIREGUARD_DETOUR_GUARD

Отклонять (по образцу ядрового запрета «empty direct detour») конфигурацию, где
AmneziaWG-endpoint (источник с AWG-полями) имеет `detour` на **любой
WireGuard-based** endpoint — плоский WireGuard **или** AmneziaWG. По пакетам это
AWG-трафик, инкапсулированный внутри WireGuard-туннеля; на Android такая связка
**вешает ядро**. `detour` AWG-ноды на не-WireGuard outbound (VLESS, Trojan,
direct, …) — рабочий сценарий и остаётся разрешённым.

Тип: **Bug** в фиче [003 AWG2_CLIENT_ENDPOINT](../003-F-C-AWG2_CLIENT_ENDPOINT).
Баг **нашей** дельты (AWG-проводка + merged-форк wireguard-go), не upstream →
в скоупе по CONSTITUTION §3.1.

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

> **Ревизия 2026-06-16 (см. IMPLEMENTATION_REPORT «Смена подхода»):** одного
> ленивого guard в `DetourDialer.init()` оказалось **недостаточно** — field-тест на
> lx.8 показал, что AWG→WG виснет синхронно в `Endpoint.Start` (резолв peer-домена
> через detour + junk-handshake), **до** первого dial, поэтому ленивый guard не
> успевает сработать. Решение — **два эшелона**: (1) **Start-guard** в
> `protocol/wireguard.Endpoint.Start` для прямой транзитивной detour-цепи (device
> не поднимается, `started=false`); (2) **dialer-guard** (ниже) остаётся для
> случая с селектором/urltest по середине, где цель известна только в рантайме.

## 3. Требования

### 3.1 Критерий «источник»
- Источник-триггер — AWG-endpoint: `option.AmneziaWGOptions.IsSet()` (любое
  AWG-поле). Плоский WG источником-триггером **не** является (WG→AWG разрешён).

### 3.2 Критерий «цель»
- Цель-триггер — **любой WireGuard-based outbound**: `Type() == C.TypeWireGuard`
  (один тип `"wireguard"` покрывает и плоский WG, и AWG — AWG отличается лишь
  набором полей, тип тот же). Решение автора: детектировать **по типу**.
- Группы (selector/urltest): раскрывать рекурсивно через `All()`; если любой член
  (транзитивно) — WireGuard, цель считается WireGuard. Защита от циклов — set
  посещённых тегов.
- Не-WireGuard цели (VLESS и т.д.) — **не** триггер.

### 3.3 Где ловить
- В `common/dialer/detour.go` `DetourDialer.init()`, **сразу после** существующей
  проверки `empty direct outbound` — тот же ленивый механизм даёт вариант B
  «бесплатно».
- Источник передаётся в dialer флагом: `dialer.Options.IsAmneziaWG` →
  `NewDetour(..., ownerIsAmneziaWG)`. Флаг выставляет `protocol/wireguard.NewEndpoint`
  из `options.AmneziaWGOptions.IsSet()`.

### 3.4 Изоляция (CONSTITUTION §3.2–3.3)
- Правки upstream-файлов (`detour.go`, `dialer.go`, `endpoint.go`) — в `// lx:`
  блоках. Тест — отдельный lx-файл `awg_detour_guard_test.go`.
- Поведение **без** `with_awg` не меняется: AWG-конфиг и так отвергается раньше
  («awg support not built»); для плоского WG `ownerIsAmneziaWG=false` → guard no-op.

## 4. Критерии приёмки

- AWG `detour`→WG и AWG `detour`→AWG: dial фейлит с понятной ошибкой; ядро и
  прочие узлы живут (вариант B).
- AWG `detour`→VLESS и WG `detour`→AWG: проходит.
- detour на группу с WireGuard-членом при AWG-источнике — фейлит; циклы групп не
  виснут.
- Юнит-тест зелёный (прямая цель WG/VLESS, группа с/без WG, цикл; init-путь для
  AWG→WG / AWG→VLESS / WG→WG).
- `go build ./...` без тегов — ок; сборка с `with_awg` — ок; `gofmt -l` пусто.

## 5. Вне скоупа

- **Лечение первопричины** (таймауты/неблокирующая отправка junk в
  `submodules/wireguard-go`, проверка `jmin<=jmax`) — отдельная будущая задача.
- Цепочки, построенные **не** через `detour`, а через route-rule action — вне
  скоупа (автор: ловим связь через `endpoint.detour`).

## 6. Ссылки

- Фича [003 AWG2_CLIENT_ENDPOINT](../003-F-C-AWG2_CLIENT_ENDPOINT)
- LxBox §128 — образец поведения «вариант B» (ленивая detour-ошибка, ядро живёт):
  `Leadaxe/LxBox/docs/spec/tasks/128-force-direct-out-detour.md`
- `submodules/wireguard-go/device/send.go` (junk-генерация в SendHandshakeInitiation)
- Образец detour-проверки: `common/dialer/detour.go` (empty-direct, upstream `fb622ccb`)
