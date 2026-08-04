# SPEC: 047 — EARLY_RPC_NIL_ROUTER_CRASH

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — гонка старта апстрима, корень доказан крашдампом с устройства |
| Статус | C (complete) — корень доказан крашдампом + red/green юнитами; остаток: field после AAR |

`ResetNetwork`, пришедший по command-протоколу в окно между созданием `Box` и
его запуском, роняет **весь процесс** nil-pointer паникой: поля
`NetworkManager` ещё не присвоены. Гейт вызова проверяет `Box() != nil`, но
`Box` перестаёт быть nil в момент создания, а не готовности.

Фикс — гейтить ранние RPC по статусу сервиса (`ServiceStatus_STARTED`), как это
уже делают `URLTest` и все наши lx-RPC, плюс ранний nil-guard в самом
`ResetNetwork`.

Build-tag: нет (фикс безусловный). Scope: **client-only**.

---

## 1. Проблема

Жалоба (Telegram, 2026-08-03): «приложение крашнулось». Крашбандл с устройства,
ядро `1.14.0-lx.19-rc.3`, `go1.26.5, android/arm64`:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x50 ...]

goroutine 1942 [running, locked to thread]:
route.(*NetworkManager).ResetNetwork(0x7c949da600)
    route/network.go:485 +0x1a0
experimental/libbox.(*CommandServer).ResetNetwork(...)
    experimental/libbox/command_server.go:265 +0x34
main.proxylibbox_CommandServer_ResetNetwork(...)
```

`route/network.go:485` — это `r.router.ResetNetwork()`, последняя строка метода.
`addr=0x50` — разыменование нулевого указателя со смещением поля, то есть
`r.router == nil`.

### 1.1 Корень: RPC обгоняет старт box

`NetworkManager.router` присваивается **единственный раз**, на стадии
`StartStateInitialize` ([route/network.go:143](../../../route/network.go)):

```go
case adapter.StartStateInitialize:
    r.router = service.FromContext[adapter.Router](r.ctx)
```

Стадию раздаёт `Box.preStart` ([box.go:560](../../../box.go)). До неё поле — nil,
и никакой гейт внутри `NetworkManager` его не прикрывает.

Окно, в которое RPC проходит гейт и попадает на неинициализированное поле,
задаётся порядком в `StartOrReloadService`
([daemon/started_service.go:210-232](../../../daemon/started_service.go)):

| Строка | Событие | Что видит RPC |
|---|---|---|
| 208 | `updateStatus(STARTING)` | — |
| 210 | `newInstance(...)` — создан `Box`, поля `NetworkManager` ещё nil | — |
| **214** | **`s.instance = instance`** | `Instance() != nil`, `Box() != nil` → **гейт пройден** |
| 220 | `instance.Start()` — вся стадийная инициализация | **окно паники** |
| 232 | `updateStatus(STARTED)` | окно закрыто |

То есть уязвимо **всё время работы `Box.Start()`**, а не только его начало.
`preStart` лишь делает окно широким: перед стадией `StartStateInitialize` стоит
`StartNamed(..., s.internalService)` (cache-file, clash-api, v2ray-api), и в том
же крашдампе видно, что процесс стоял именно там — на дисковой bbolt-транзакции:

```
goroutine 17 [chan receive, locked to thread]:
bbolt.(*DB).Batch(...)
experimental/cachefile.(*CacheFile).start(...)
sing-box.(*Box).preStart(...)  box.go:560
sing-box.(*Box).Start(...)     box.go:529
daemon.(*StartedService).StartOrReloadService(...)
```

Обе стороны гонки сняты одним дампом: `goroutine 17` открывает `cache.db`,
`goroutine 1942` в это время входит в `ResetNetwork` и падает.

### 1.2 Почему существующий гейт не ловит

[experimental/libbox/command_server.go:261](../../../experimental/libbox/command_server.go):

```go
func (s *CommandServer) ResetNetwork() {
    instance := s.StartedService.Instance()
    if instance == nil || instance.Box() == nil {
        return
    }
    instance.Box().Network().ResetNetwork()
}
```

`Instance()` возвращает `s.instance`, присвоенный на строке 214 — до `Start()`.
Проверка `Box() != nil` отсекает только состояния «сервис не стартовал вообще»
и «остановлен», но по времени не защищает ничего: между присваиванием и
готовностью лежит весь `Box.Start()`.

### 1.3 Зона поражения

**Поля.** До падения на 485 `ResetNetwork` обходит `r.endpoint`, `r.inbound`,
`r.outbound` ([route/network.go:459-486](../../../route/network.go)) — все три
присваиваются на той же стадии `StartStateInitialize`. Пользователь упал на
последней строке; при чуть ином тайминге паника пришла бы из любого цикла выше.
Поэтому guard обязан быть **одним ранним `return`**, а не четырьмя точечными
проверками: `connectionManager.CloseAll()` в начале метода не должен работать по
полуинициализированному менеджеру.

**Методы.** Слабый гейт `Box() != nil` стоит на четырёх методах
`CommandServer`. Проверены все:

| Метод | Разыменование | Уязвим |
|---|---|---|
| `ResetNetwork` (261) | `Network().ResetNetwork()` → `r.router`, `r.endpoint`, `r.inbound`, `r.outbound` | **да** — краш в поле |
| `NeedWIFIState` (219) | `Network().NeedWIFIState()` → `return r.needWIFIState` | нет — плоское поле, ставится в конструкторе |
| `UpdateWIFIState` (269) | `Network().UpdateWIFIState()` | нет — все ветки nil-safe, есть `else { return }` |
| `NeedFindProcess` (227) | `Box().Router()` → `return r.needFindProcess` | нет — `Box.router` присваивается в `box.New` ([box.go:498](../../../box.go)), не на стадии |

Доказан **один** метод — `ResetNetwork`. Остальные три приводятся к тому же
гейту как гигиена (единый инвариант «RPC не обслуживается до готовности»), а не
как лечение краша.

### 1.4 Эталон гейта уже есть в репозитории

Правильная проверка — статус, а не наличие объекта
([daemon/started_service.go:608](../../../daemon/started_service.go)):

```go
s.serviceAccess.RLock()
if s.serviceStatus.Status != ServiceStatus_STARTED {
    s.serviceAccess.RUnlock()
    return nil, os.ErrInvalid
}
```

Этот паттерн держат `URLTest`, `SelectOutbound`, `SetGroupExpand` и все наши
lx-RPC ([daemon/started_service_command_lx.go](../../../daemon/started_service_command_lx.go),
7 мест). Фикс приводит отставшие методы к уже принятому в репозитории
инварианту, а не вводит новый механизм.

**Препятствие:** `serviceStatus`/`serviceAccess` приватны в пакете `daemon`, а
`CommandServer` живёт в `experimental/libbox` — прямая проверка оттуда
невозможна. Нужен экспортируемый предикат на `StartedService` (сейчас его нет:
из статус-поверхности экспортированы только `SubscribeServiceStatus` и
`SetError`).

### 1.5 Кто стреляет в поле

Штатный источник — авто-`resetNetwork()` на смену интерфейса (WiFi↔LTE),
клиентская задача LxBox §087: `DefaultNetworkMonitor` → `BoxService.kt:421`.
Монитор регистрируется в `startSingbox()` **до** `startOrReloadService(...)`,
поэтому переключение сети, попавшее на старт туннеля, бьёт точно в окно.

Прочие вызыватели того же RPC — `ACTION_RESET_NETWORK` из шторки,
automation-хендлер `actionResetNetwork`, кнопка recovery в UI, Debug API.
Они остаются и после клиентского фикса, поэтому гейт нужен в ядре.

## 2. Доказательство

- Крашдамп с устройства (`1.14.0-lx.19-rc.3`): падение на `network.go:485`,
  `addr=0x50`; в том же дампе `goroutine 17` стоит в `bbolt.Batch` внутри
  `preStart` — обе стороны гонки видны одновременно.
- Присваивание `r.router` — единственное, на `StartStateInitialize`
  (`grep -n "r.router" route/network.go` → 39, 143, 485).
- Порядок `s.instance = instance` (214) → `instance.Start()` (220) →
  `updateStatus(STARTED)` (232) в `StartOrReloadService` задаёт границы окна.

## 3. Требования

- `ResetNetwork`, вызванный до готовности сервиса, — тихий no-op, не паника.
- Гейт по статусу (`ServiceStatus_STARTED`), а не по `Box() != nil`: последнее
  не отражает готовность.
- `NetworkManager.ResetNetwork` держит ранний nil-guard (защита в глубину на
  случай других путей вызова), выходящий **до** `connectionManager.CloseAll()`.
- Соседи из таблицы 1.3 приведены к тому же гейту — единый инвариант.
- После готовности сервиса поведение `ResetNetwork` не меняется: §087
  продолжает закрывать стейл-сокеты на смену NIC.
- Дифф минимален и следует существующему паттерну репозитория; новых
  RPC, полей протокола и build-тегов не появляется.

## 4. Критерии приёмки

- Юнит red/green: `ResetNetwork` на `NetworkManager` без пройденного
  `StartStateInitialize` не паникует (на текущем коде падает).
- Юнит: `CommandServer.ResetNetwork` при статусе `STARTING` не доходит до
  `Network()`.
- Гоночный тест под `-race`: `StartOrReloadService` + параллельный
  `ResetNetwork` в цикле — чисто.
- `go vet`, `gofmt -l` по тронутым файлам — чисто.
- Field: смена WiFi↔LTE в момент старта туннеля не роняет процесс.
  ⚠️ На эмуляторе не проверяется (ложно-зелёный для стартовых гонок) —
  остаток на устройство после AAR.

## 5. Границы

- **Порядок стадий старта не меняем.** Правим гейты, не архитектуру `preStart`.
- **Синхронность `cachefile.start` (bbolt) вне scope** — она расширяет окно,
  но не создаёт баг: окно существует всё время `Box.Start()` и при мгновенном
  открытии `cache.db`.
- **Клиентская сторона** (не звать RPC до `Started`) — задача LxBox, здесь не
  описывается; ядро обязано быть устойчивым независимо от неё.
- **Не вводим гейт по статусу в `Pause`/`Wake`**: они обязаны работать в
  `STARTING` (их собственный гейт — `PauseManager() != nil` — корректен).
- Серверная сторона (inbound) вне scope форка.

## 6. Условие снятия

Апстрим введёт собственный гейт готовности на ранних command-RPC (проверять
`experimental/libbox/command_server.go` на мержах) либо перестанет публиковать
`s.instance` до завершения `Start()` в `daemon/started_service.go`. До тех пор
запись держится в реестре [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md).
