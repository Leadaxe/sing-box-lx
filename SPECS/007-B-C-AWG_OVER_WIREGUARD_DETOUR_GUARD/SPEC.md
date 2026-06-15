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

> **Ревизия 2026-06-16 (см. IMPLEMENTATION_REPORT):** реализация прошла две
> итерации.
>
> 1. **lx.8 — ленивый guard в `DetourDialer.init()`** (рантайм, на первом dial).
>    Field-тест на устройстве показал, что он **не срабатывает**: AWG→WG виснет
>    синхронно в `Endpoint.Start` (резолв peer-домена через detour + junk-handshake),
>    **до** первого dial.
> 2. **lx.9 — Start-guard в `protocol/wireguard.Endpoint.Start`** (статический обход
>    транзитивной detour-цепи; device не поднимается, `started=false`). **Field-verified
>    на Android** — работает.
>
> **Итог (после ревизии): оставлен только Start-guard.** Ленивый dialer-guard
> **удалён** — он непроверяем в реальном UI (LxBox не даёт detour на группу) и
> кэширует результат через `sync.Once`, т.е. не ловит смену состава селектора в
> рантайме; в прошлый раз он вообще не сработал. Случай «селектор/urltest по
> середине» Start-guard **намеренно пропускает** (на группе цель рантайм-зависима) —
> это **известный непокрытый случай** (см. §5), под него ищется отдельная надёжная
> точка перехвата на переключении селектора.

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
- На группе (selector/urltest) обход **останавливается** — выбранный член известен
  только в рантайме (см. §5). Не-WireGuard цели (VLESS и т.д.) — **не** триггер.

### 3.3 Где ловить
- В `protocol/wireguard.Endpoint.Start` (стадия `StartStateStart`), **до**
  `w.endpoint.Start()`. Это единственная точка, ловящая баг: зависание происходит
  синхронно в `Start` (резолв peer-домена через detour + junk-handshake), до
  первого dial — ленивый dialer-guard туда не успевает (доказано на lx.8).
- Источник — `options.AmneziaWGOptions.IsSet()` (сохранён в `Endpoint.awgActive`);
  цель — `awgDetourChainReachesWireGuard(outboundManager, detour, …)`.
- Поведение — **вариант B**: device не поднимается, `started=false`, `return nil`
  (НЕ error — иначе abort всего инстанса), ошибка в лог. Все outbound'ы
  зарегистрированы до любого `Start`, поэтому цепочка резолвится по тегам.

### 3.4 Изоляция (CONSTITUTION §3.2–3.3)
- Правка upstream-файла `protocol/wireguard/endpoint.go` — в `// lx:` блоках. Тест
  — отдельный lx-файл `awg_start_guard_test.go`. `common/dialer/{detour,dialer}.go`
  ревизией 2026-06-16 **возвращены к upstream** (ленивый guard удалён).
- Поведение **без** `with_awg` не меняется: AWG-конфиг отвергается раньше
  («awg support not built»); для плоского WG `awgActive=false` → guard no-op.

## 4. Критерии приёмки

- AWG `detour`→WG и AWG `detour`→AWG: узел не поднимается, ошибка в лог; ядро и
  прочие узлы живут (вариант B). ✅ field-verified на Android lx.9.
- AWG `detour`→VLESS и WG `detour`→AWG: проходит.
- AWG→X→…→WG (транзитивно по detour): ловится; циклы не виснут.
- Юнит-тест зелёный (`awgDetourChainReachesWireGuard`: direct/транзитив/нет-WG/
  селектор-пропуск/цикл/unknown).
- `go build ./...` без тегов — ок; сборка с `with_awg` — ок; `gofmt -l` пусто.

## 5. Вне скоупа

- **Селектор/urltest по середине** цепочки (`AWG→…→selector(с WG внутри)`):
  Start-guard его пропускает (цель рантайм-зависима, меняется переключением). В UI
  LxBox недостижим (detour только на реальные серверы), но другие потребители ядра
  могут такое собрать. **Известный непокрытый случай** — под него ищется надёжная
  точка перехвата на переключении селектора (отдельная задача).
- **Лечение первопричины** (таймауты/неблокирующая отправка junk в
  `submodules/wireguard-go`) — отдельная будущая задача.
- Цепочки через route-rule action, а не `detour` — вне скоупа.

## 6. Ссылки

- Фича [003 AWG2_CLIENT_ENDPOINT](../003-F-C-AWG2_CLIENT_ENDPOINT)
- LxBox §128 — образец поведения «вариант B» (ленивая detour-ошибка, ядро живёт):
  `Leadaxe/LxBox/docs/spec/tasks/128-force-direct-out-detour.md`
- `submodules/wireguard-go/device/send.go` (junk-генерация в SendHandshakeInitiation)
- Образец detour-проверки: `common/dialer/detour.go` (empty-direct, upstream `fb622ccb`)
