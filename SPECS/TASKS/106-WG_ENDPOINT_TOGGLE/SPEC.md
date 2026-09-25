# SPEC: 106 — WG_ENDPOINT_TOGGLE

**Фича:** [008-ENERGY](../../FEATURES/008-ENERGY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — ручное включение и выключение WG/AWG-endpoint'а по gRPC |
| Статус | I (implemented) — выпущено в `v1.14.2-lx.4`; юниты зелёные, живого прогона через лаунчер нет |
| Ветка | `lx` |
| Build-tag | ядро — без тега (методы без вызова ведут себя как апстрим); RPC — `with_lx_command` |
| Связано | [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (уровни сна, `resumeOnDial`), [097](../097-LAZY_WG_DEVICE_BUILD/SPEC.md) (`IdleState`, бюджет сборок), [075](../075-CHAIN_POSITION_TOGGLE/SPEC.md) (образец RPC-переключателя) |

## 1. Назначение

Клиент (лаунчер через `lxd`) выключает WG/AWG-узел без правки конфига и перезагрузки ядра: устройство опускается, дайлы через узел отвергаются, пока узел не включат обратно. Решение владельца, дизайн зафиксирован.

## 2. Семантика

Выключение переиспользует сон SPEC 020: бодрый узел усыпляется так же, как это делает тик простоя, а флаг `disabled` не даёт дайлу его разбудить.

| Состояние до | `SetEnabled(false)` | `SetEnabled(true)` |
|---|---|---|
| `up` | `started=false`, `idleAsleep=true`, `sleepSince=now`, `Suspend()` (device.Down, установленные потоки рвутся), `budget.Notify()` | — (no-op) |
| `asleep` | только флаг | флаг снят; `Resume()` сразу; успех → `up` |
| `torn_down` / `never_built` | только флаг | флаг снят; устройство соберёт следующий дайл через бюджет |
| `down` (закрывается) | `os.ErrClosed` | `os.ErrClosed` |
| уже в целевом состоянии | no-op | no-op |

- Ошибка `Resume()` при включении возвращается; флаг уже снят, узел остаётся спящим, следующий дайл повторит пробуждение (та же логика, что в `resumeOnDial`).
- Все переходы под `resumeMu`. Выключение, пришедшее во время пересборки с ожиданием бюджета, ждёт её конца (до 15 с).
- Дайл по выключенному узлу (`DialContext`, `ListenPacket`, L3-форвард `WritePackets`) получает `WireGuard endpoint is disabled`; прежняя ошибка `WireGuard is not ready yet` остаётся для остальных отказов.
- Тик простоя: `SuspendIfIdle` выходит сразу, `TeardownIfSlept` работает — выключенный узел спит и может быть освобождён полностью. Бюджет (`lx.wg.build_max`) может выбрать выключенный узел с собранным устройством жертвой, как любой спящий.
- `IdleState()` отдаёт `disabled` поверх любого уровня сна; приоритет выше только у `building` и `down` при закрытии.
- **Не сохраняется.** Новый бокс (reload, `/admin/apply`) создаёт все endpoint'ы включёнными.

## 3. RPC

```proto
rpc SetEndpointEnabled(SetEndpointEnabledRequest) returns (SetEndpointEnabledResponse) {}

message SetEndpointEnabledRequest {
  string tag = 1;
  bool enabled = 2;
}
message SetEndpointEnabledResponse {
  string state = 1; // GroupItem.endpointState после вызова
}
```

| Код | Когда |
|---|---|
| `FailedPrecondition` | сервис не в `STARTED`; endpoint закрывается |
| `NotFound` | нет endpoint'а с таким тегом |
| `InvalidArgument` | endpoint не WG/AWG (не реализует `adapter.EndpointToggle`) |
| `Unavailable` | включение: `Resume()` не поднял устройство; узел включён, но спит |
| `Unimplemented` | сборка без `with_lx_command` |

`GetOutbounds` отдаёт новое состояние в `GroupItem.endpointState` без изменений в обработчике.

## 4. Карта реализации

| Файл | Что |
|---|---|
| `adapter/idle_suspend_lx.go` | `EndpointToggle`, `EndpointStateDisabled = "disabled"` |
| `protocol/wireguard/endpoint_toggle_lx.go` | `SetEnabled`, `Enabled`, `notReadyError` |
| `protocol/wireguard/endpoint.go` | `// lx:` поле `disabled`; ранний выход в `SuspendIfIdle`; две проверки в `resumeOnDial` (до и под `resumeMu`); пять мест отказа дайла → `w.notReadyError()` |
| `protocol/wireguard/endpoint_lazy_lx.go` | ветка `disabled` в `IdleState()` |
| `transport/wireguard/endpoint_suspended_lx.go` | `Suspended()` — для тестов протокольного слоя |
| `daemon/started_service.proto` + регенерация `*.pb.go` | RPC и два сообщения в блоке `lx_command`; комментарий `GroupItem.endpointState` |
| `daemon/started_service_endpoint_lx.go` / `_stub.go` | обработчик и близнец `Unimplemented` |
| `experimental/libbox/command_client_endpoint_lx.go` | `CommandClient.SetEndpointEnabled(tag, enabled)` → `*EndpointToggleResult{State}` для LxBox; без тега, как `command_client_chain_lx.go` |

Апстримные файлы: `protocol/wireguard/endpoint.go` (строки в наших `// lx:` зонах), `daemon/started_service.proto` (блок `lx_command`), регенерируемые `daemon/*.pb.go`. `cmd/internal/build_libbox/main.go` не тронут: `with_lx_command` уже в `sharedTags`.

## 5. Границы

- Обработчик общий для `lxd` и libbox. LxBox вызывает `CommandClient.SetEndpointEnabled`; ответ — объект, не голая строка (SPEC 037). Клиентский файл без тега: против сборки без `with_lx_command` метод получает `Unimplemented` от сервера.
- Без `with_lx_idle_suspend` (десктоп) тика нет: выключенный узел остаётся в Down с собранным устройством, пока его не включат или не закроют.
- Выключенный узел для групп = мёртвый узел (решение владельца 2026-09-26): urltest уводит трафик штрафом и failover-дайлом SPEC 054, selector с выбранным выключенным узлом отдаёт ошибку на каждом дайле, пока выбор не сменят или узел не включат. Ядро не предупреждает и не переключает; клиент при желании проверяет `GetGroups` сам.

## 6. За чем следить на мерже

- `protocol/wireguard/endpoint.go`: сигнатура и тело `resumeOnDial`, пять возвратов `notReadyError()` в `WritePackets`/`DialContext`/`ListenPacketWithDestination` (у апстрима там `E.New("WireGuard is not ready yet")` при `!w.started.Load()`), начало `SuspendIfIdle`. Если апстрим поменяет форму отказа — перенести вызов `notReadyError()`.
- `transport/wireguard/endpoint.go`: `Suspend`/`Resume` — безусловные методы; поле `suspended`, которое читает `Suspended()`.
- `daemon/started_service.proto`: блок `lx_command` переносится руками, `*.pb.go` — только регенерацией.

## 7. Тесты

- `protocol/wireguard/endpoint_toggle_lx_test.go`: выключение бодрого (Suspend, `disabled`, отказ дайла/listen/L3 с новой ошибкой, тик не трогает), спящего и разобранного; включение (Resume, `up`); ошибка Resume (узел спит, следующий дайл будит); идемпотентность; `SetEnabled` после `Close`; `TeardownIfSlept` по выключенному; включение разобранного и `never_built` без сборки, сборка следующим дайлом; жертва бюджета.
- `daemon/started_service_endpoint_lx_test.go`: коды `NotFound`/`InvalidArgument`/`FailedPrecondition`/`Unavailable`, `state` в ответе; `_stub_lx_test.go`: `Unimplemented`.
- `experimental/libbox/command_client_endpoint_lx_test.go`: запрос и `State` через подменённый `StartedServiceClient`, gRPC-код ошибки доходит до вызывающего; форма `EndpointToggleResult` (только экспортируемые скаляры). Страж SPEC 037 `TestCommandClientNoPointerBearingCValueReturns` покрывает новый метод.
