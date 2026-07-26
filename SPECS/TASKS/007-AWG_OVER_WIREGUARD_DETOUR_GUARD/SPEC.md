# SPEC: 007 — AWG_OVER_WIREGUARD_DETOUR_GUARD

**Фича:** [AWG2](../../FEATURES/003-AWG2/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) → guard снят |
| Статус | C (complete). Guard **удалён 2026-07-18** — первопричина ушла на новом графте. Ниже: чем был, почему снят, что удалено, что осталось. |

## 0. TL;DR

Эта задача поставила **guard** против связки «AmneziaWG-нода с `detour` на
WireGuard-туннель»: на старой базе (`v1.13.13`) такая конфигурация **вешала
ядро на Android**. Guard не поднимал такой узел (вариант B: ядро живёт, узел не
встаёт, ошибка в лог).

**2026-07-18 guard удалён из ядра целиком.** На текущей базе
(`v1.14.0-lx.10`) связка AmneziaWG-over-WireGuard **больше не вешает ядро** —
поднимается и несёт трафик (проверено e2e). Первопричину вылечили смежные
задачи (см. §2). Осталось — **Android field-тест** (историческое зависание было
Android-специфичным).

---

## 1. Чем был guard (историческая проблема)

### 1.1 Симптом

Матрица из тестов автора на устройстве (база `v1.13.13`):

| Источник (`detour`-ит) | Цель | Результат (тогда) |
|---|---|---|
| **AWG** | **WG** (плоский WireGuard) | ❌ ядро виснет / handshake не уходит |
| **AWG** | **AWG** | ❌ то же |
| **AWG** | **VLESS** | ✅ работает |
| **WG** (без AWG-полей) | AWG | ✅ работает |

Триггер — **источник AWG + цель = любой WireGuard-туннель** (AWG внутри WG по
пакетам). По конфигу — «у ноды AWG прописан `detour` на WG/AWG».

### 1.2 Предполагавшаяся механика (статикой не доказана)

Разбор `submodules/wireguard-go`: `SendHandshakeInitiation` (device/send.go)
синхронно генерирует junk и зовёт `SendBuffers` → `bind.Send()` **без таймаута
на запись**, удерживая `device.net.RLock()`. Когда AWG-трафик заворачивается в
WireGuard-устройство, запись блокируется на нижнем туннеле; на Android (нет
watchdog) — зависание. **Первопричину статикой не доказывали** — задача была про
guard, не про лечение.

Баг в фиче [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT) **нашей**
дельты (AWG-проводка + merged-форк wireguard-go), не upstream → был в скоупе по
CONSTITUTION §3.1.

### 1.3 Что guard делал (вариант B)

Ядро стартует, прочие узлы работают, AWG-over-WG узел не встаёт, ошибка в лог.
**Не крашились.** Реализация прошла три итерации (полностью — в
[IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md), кратко — в §5 ниже):

1. **lx.8 — ленивый guard в `DetourDialer.init()`** (на первом dial). Не
   сработал: зависание синхронно в `Endpoint.Start`, до первого dial. **Откачен.**
2. **lx.9 — Start-guard в `protocol/wireguard.Endpoint.Start`** — статический
   транзитивный обход detour-цепи; device не поднимался. Field-verified на Android.
3. **selector-guard в `protocol/group`** — для случая «селектор/urltest по
   середине» (рантайм-цель, статикой не видна): при переключении группы на
   WireGuard-член гасил AWG-потребителей **до** коммита выбора.

Итого перед удалением стояли **два дополняющих guard'а** + pause-wake-гейт, не
воскрешающий погашенный узел.

---

## 2. Почему guard снят (2026-07-18)

### 2.1 Первопричина ушла на новом графте

С момента постановки задачи ядро сменило базу и вылечило детур-путь AWG тремя
смежными задачами:

- **lx.8 — re-graft AmneziaWG 2.0 на `sagernet/wireguard-go` v0.0.5** (новый
  фундамент вместо `v1.13.13`, на котором ставился диагноз §1.2).
- **[SPEC 025](../025-AWG_TRANSPORT_PADDING_OVERRUN) — s4 transport-padding
  overrun** (краш `send.go` на каждом data-пакете при `s4>0`).
- **[SPEC 026](../026-AWG_MAGIC_VS_RESERVED_CLEAR) — reserved-clear gate
  (lx.9)** — ключевое для detour: `ClientBind` (bind detour-пути) безусловно
  затирал байты 1-3, разрушая ranged AWG-magic → AWG через **любой** `detour` не
  поднимался вовсе. После гейта `hasReserved()` детур-путь AWG ожил.

Старый диагноз (§1.2) на новой базе не перепроверялся и статикой никогда не
доказывался. На практике связка теперь поднимается.

### 2.2 Как проверено (mac-стенд, 2026-07-18)

Два процесса `sing-box` CLI на loopback:

- **клиент**: верхний AWG-endpoint (`jc=4, jmin=8, jmax=80, s4=12`, ranged
  `h1..h4`) с `detour` на нижний **плоский WireGuard**;
- **сервер**: WG-сервер (принимает нижний туннель) + AWG-сервер за
  route-правилом с `override_address`/`override_port` (заворачивает inner-трафик
  верхнего туннеля во второй endpoint).

Результат: handshake верхнего AWG **через** нижний WG проходит, keepalive идут в
обе стороны, HTTP через socks (сквозь обе оболочки) отвечает `200/301`. Старт
ядра не виснет (~1 c), graceful shutdown чистый. Guard на время эксперимента
обходился временным env-гейтом, затем удалён насовсем.

**Диагностические грабли стенда** (не ядра, полезны на будущее):

- `allowed_ips` верхнего туннеля должен покрывать целевые адреса теста
  (`0.0.0.0/0`); суженный дал ложный след «данные не ходят» —
  `RoutineReadFromTUN` молча дропает по `allowedips.Lookup == nil`.
- `s4` клиента и сервера должны **совпадать**: приёмник парсит transport строго
  по своему `paddings.transport`; при живом handshake (s1/s2 нулевые) асимметрия
  s4 глушит данные в обе стороны.

### 2.3 Что осталось (owed)

**Android field-тест.** Историческое зависание было Android-специфичным
симптомом (`Libbox.newService` не возвращал управление; в logcat последняя
строка `defaultNetwork`, затем тишина). Mac-стенд это на 100% не покрывает —
нужна сборка ядра с снятым guard'ом → AAR → APK → прогон связки AWG→WG на
устройстве (CPH2411). App-side гейт §130 в LxBox снят синхронно (см. §4).

---

## 3. Что удалено из ядра

| Слой | Удалено |
|---|---|
| **Start-guard** — `protocol/wireguard/endpoint.go` | функция `awgDetourChainReachesWireGuard`; поля `awgActive` / `detour` / `awgChainBlocked` и их инициализация; методы `IsAmneziaWG` / `SuspendAmneziaWG`; guard-блок в `Endpoint.Start`; импорт `strconv` |
| **selector-guard** — `protocol/group/` | файл `awg_selector_guard.go` целиком (`chainReachesWireGuard`, `suspendAmneziaWGConsumers*`); вызов в `selector.go` (`SelectOutbound`); два вызова в `urltest.go` (`performUpdateCheck` auto-switch + `balancer.onChange` pool) |
| **adapter-маркеры** — `adapter/` | метод `OutboundManager.ConsumersOf` (интерфейс + реализация `Manager`); интерфейс `AmneziaWGSuspendable` |
| **тесты** | `protocol/wireguard/awg_start_guard_test.go`, `protocol/wireguard/awg_chain_group_lx_test.go`, `protocol/group/awg_selector_guard_test.go`, тест `TestSuspendAmneziaWG_clearsTeardownState` |

Коммит: `5fa3a0a1` (ветка `lx-1.14`), lx-changelog `v1.14.0-lx.11`.

## 3.1 Что сохранено и почему

- **Транспортный `Suspend()` / `Resume()` + флаг `suspended`**
  (`transport/wireguard/endpoint.go`) — их держит **[SPEC 020](../020-MULTI_WG_IDLE_BUFFER_HEAT)
  idle-suspend**, а не только снятый guard. Комментарии, ссылавшиеся на AWG-guard,
  переписаны на idle-only.
- **`onPauseUpdated`-гейт** (`suspended`-устройство не поднимается на
  `DeviceWake`/`NetworkWake`) — тоже SPEC 020: без него pause/wake воскрешал бы
  idle-опущенный узел. Сохранён.
- **`groupTag`** в urltest — нужен SPEC 020 (probe gating), не только guard'у.
  Сохранён; из его комментария убрана ссылка на AWG-guard.
- **`dependByTag`-леджер** в `adapter/outbound/manager.go` — upstream-механизм
  (есть и в `dns/transport_manager.go`); guard читал его через `ConsumersOf`.
  Леджер сохранён, снят только `ConsumersOf`.
- **Инвариант idle-suspend «остановленный endpoint не воскрешать по dial»** —
  жив и важен независимо от guard'а (Close / неудавшийся Start дают
  `started=false && idleAsleep=false`). Три теста, проверявшие его, сохранены,
  переименованы `guardSuspended*` → `stopped*`.

---

## 4. Синхронный снос гейта в LxBox (app-side §130)

Пока стоял ядровой guard, LxBox прятал WireGuard-цели из пикера detour для
AWG-узлов (§130), чтобы юзер не собрал заведомо мёртвую связку. Гейт снят одним
коммитом с этим (LxBox `b1ffc66`, ветка `develop`):

- `node_settings_screen.dart` — фильтр `excludeWireguard`, сброс сохранённого
  detour, `_detourTargetIsWireguard` / `_logResetDetour`, инфо-нотка;
- `detour_target_picker.dart` — параметр `excludeWireguard` и два фильтра;
- `build_config.dart` — advisory `_warnAwgDetourViaWgChannels` (его причина
  отпала) и структуры, что строились только под него;
- словарный ключ ru.json + advisory-тест.

`_isAwg` в app **оставлен** — он ещё нужен для подписи схемы «AmneziaWG
(wireguard)».

---

## 5. История реализации guard'а (для контекста)

> Ниже — как guard эволюционировал, пока стоял. Полностью — в
> [IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md). Актуально только как
> объяснение, что именно снято §3.

- **lx.8 — ленивый dialer-guard** (`DetourDialer.init()`, на первом dial):
  копировал ядровой запрет «empty direct detour». Field-тест: **не сработал** —
  AWG→WG виснет синхронно в `Endpoint.Start` (резолв peer-домена через detour +
  junk-handshake), **до** первого dial. Откачен, `common/dialer/*` возвращены к
  upstream.
- **lx.9 — Start-guard** (`Endpoint.Start`, стадия `StartStateStart`): статический
  транзитивный обход detour-цепи через `OutboundManager` + `Dependencies()`;
  группа разрешалась через текущий выбор (`ActiveTags()`/`Now()`, не `All()`).
  При достижении `Type()==wireguard` — device не поднимался (`return nil`,
  вариант B). Field-verified на Android.
- **selector-guard** (`SelectOutbound`, **до** `s.selected.Store`): при
  переключении группы на WireGuard-член шёл **вверх** по `ConsumersOf`
  (reverse-deps) и гасил AWG-потребителей (`SuspendAmneziaWG` → `device.Down`).
  Гашение до коммита закрывало гонку.
- **Ревизия 2026-07-15** (аудит SPEC 019/020) закрыла три дыры покрытия:
  Start-guard через текущий выбор группы (рестарт с восстановленным WG-выбором),
  urltest-guard (auto-switch + pool rebuild), pause-wake-гейт. Все три механизма
  сняты вместе с guard'ом; **pause-wake-гейт сохранён** как часть SPEC 020 (§3.1).

---

## 6. Вне скоупа

- **Лечение первопричины** в `submodules/wireguard-go` (таймауты/неблокирующая
  отправка junk) — если Android field-тест выявит остаточную блокировку, это
  отдельная задача. По e2e-стенду отдельного лечения пока не потребовалось.
- Цепочки AWG-over-WireGuard через route-rule action, а не `detour`.

## 7. Ссылки

- Фича [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT)
- Смежные фиксы: [SPEC 025](../025-AWG_TRANSPORT_PADDING_OVERRUN),
  [SPEC 026](../026-AWG_MAGIC_VS_RESERVED_CLEAR)
- Общая машинерия, сохранённая при сносе: [SPEC 020](../020-MULTI_WG_IDLE_BUFFER_HEAT)
- Коммиты сноса: ядро `5fa3a0a1` (`lx-1.14`), LxBox `b1ffc66` (`develop`)
- `submodules/wireguard-go/device/send.go` — junk в `SendHandshakeInitiation`
  (историческая механика §1.2)
