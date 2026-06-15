# IMPLEMENTATION_REPORT — 007 AWG_OVER_WIREGUARD_DETOUR_GUARD

**Дата:** 2026-06-15 · **Статус:** Complete (код + тесты + DoD; field-verify по запросу) · **База:** `v1.13.13`

## Итог

AmneziaWG-нода с `detour` на **любой WireGuard-based** endpoint (плоский WG или
AWG) больше не вешает ядро на Android: такая связка ленивой ошибкой в
`DetourDialer.init()` **не поднимает соединение**, ядро и остальные узлы при этом
работают (**вариант B**). `detour` AWG-ноды на не-WireGuard (VLESS и т.д.) —
разрешён, как и раньше.

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

## Приёмка (DoD)

- ✅ `go build ./...` без тегов — ок (поведение upstream: для плоского WG
  `ownerIsAmneziaWG=false` → guard no-op).
- ✅ `go build -tags "with_gvisor,with_quic,with_wireguard,with_utls,with_clash_api,with_xhttp,with_awg" ./cmd/sing-box` — ок.
- ✅ `go test ./common/dialer/...` — зелёный, 8 подтестов:
  - `TestDetourTargetIsWireGuard`: direct WG→true, direct VLESS→false, группа с WG→true, группа без WG→false, цикл групп→false (не виснет);
  - `TestDetourGuardInit`: AWG→WG reject, AWG→VLESS allow, WG→WG allow.
- ✅ `gofmt -l` изменённых файлов — пусто (урок 006/005 учтён).
- ✅ `go vet ./common/dialer/... ./protocol/wireguard/...` — чисто.
- ⏳ **Field-verify** (по запросу автора): `./sing-box check`/реальный AWG→WG
  конфиг на Android — поведение варианта B (узел не встаёт, лог-ошибка, ядро живо).

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
