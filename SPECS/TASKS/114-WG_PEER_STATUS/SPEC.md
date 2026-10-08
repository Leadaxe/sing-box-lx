# SPEC: 114 — WG_PEER_STATUS

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — состояние WG/AWG-endpoint'а и каждого его пира по запросу `GetWireGuardStatus` |
| Статус | I (implemented) — заведена по просьбе владельца 2026-10-07; в rc.1 отдавалась через `GetOutbounds`, по решению владельца 2026-10-07 переведена на отдельный унарный вызов (см. [HISTORY.md](HISTORY.md)); юниты и живой стенд на двух бинарях зелёные; на устройстве не проверялась |
| Ветка | `lx` |
| Build-tag | ядро (`adapter`, `transport/wireguard`, `protocol/wireguard`) — без тега, метод без вызова ничего не меняет; RPC и CLI — `with_lx_command` |
| Связано | [097](../097-LAZY_WG_DEVICE_BUILD/SPEC.md) (`endpointState`), [106](../106-WG_ENDPOINT_TOGGLE/SPEC.md) (`disabled`), [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (уровни сна), [115](../115-TAILSCALE_PEER_PATH_STATUS/SPEC.md) (тот же паттерн для Tailscale) |
| Потребителям | [CONSUMERS.md](CONSUMERS.md) — сценарии использования |

## 1. Назначение

Раньше ядро не отвечало на вопрос «есть ли связь с пиром». `endpointState` (SPEC 097) говорит, собрано ли устройство, но не прошёл ли хендшейк. Узнать это можно было только из `debug`-логов wireguard-go (`received handshake response`). Серверу, у пира которого нет `address`, неоткуда было узнать, подключался ли клиент и откуда.

Теперь `GetWireGuardStatus(tag)` отдаёт по WG/AWG-endpoint'у состояние устройства и список пиров: публичный ключ, текущий адрес, время последнего хендшейка, rx/tx.

Принцип размещения (решение владельца 2026-10-07): **у каждого типа endpoint'а свой унарный запрос статуса**, а `GetOutbounds` остаётся списком узлов. Для Tailscale тот же паттерн даёт `GetTailscaleStatus` (SPEC 115).

## 2. Контракт

### 2.1 Proto

```proto
rpc GetWireGuardStatus(WireGuardStatusRequest) returns (WireGuardEndpointStatus) {}

message WireGuardStatusRequest {
  string tag = 1;                 // тег endpoint'а из конфига
}

message WireGuardEndpointStatus {
  string endpointTag = 1;
  string endpointState = 2;       // то же значение, что GroupItem.endpointState
  int64 idleSinceSeconds = 3;     // секунд с последнего dial'а; 0 пока активен
  repeated PeerStatus peers = 4;  // в порядке конфига; пусто без устройства
}

message PeerStatus {
  string publicKey = 1;         // base64, как public_key в конфиге
  string endpoint = 2;          // ip:port; пусто, пока неизвестен
  int64 lastHandshakeUnix = 3;  // 0 = хендшейка не было
  int64 rxBytes = 4;
  int64 txBytes = 5;
}
```

Все сообщения объявлены в хвостовом `lx:`-блоке `started_service.proto`, RPC — в `lx:`-блоке сервиса. В `GroupItem` поле `peers = 7` из rc.1 **удалено**, номер зарезервирован (`reserved 7`); `endpointState` (5) и `idleSinceSeconds` (6) остаются — они нужны карточке узла в списке.

### 2.2 Семантика полей

| Поле | Смысл |
|---|---|
| `endpointState`, `idleSinceSeconds` | Состояние устройства и простой, как в `GroupItem` (SPEC 097/106). Едут в ответе, чтобы пустой `peers` объяснялся без второго вызова |
| `publicKey` | Идентификатор пира. Имени у пира нет (в `WireGuardPeer` нет такого поля); имя по ключу подставляет потребитель из своего конфига |
| `endpoint` | Адрес, на который устройство шлёт пакеты пиру. Если в конфиге задан `address`, это он. Если не задан (серверная сторона), адрес берётся из первого прошедшего проверку пакета пира и обновляется при roaming. Это **последний известный** адрес, а не признак связи: после ухода пира он остаётся. За NAT это внешний адрес NAT; через detour/relay — адрес промежуточного узла |
| `lastHandshakeUnix` | Время последнего завершённого хендшейка. Ядро вердикт «подключён» не выносит: порог зависит от таймингов (у AWG они бывают диапазонами), и его вычисляет потребитель |
| `rxBytes` / `txBytes` | Счётчики устройства wireguard-go: включают служебный трафик (хендшейки, keepalive). Обнуляются при пересборке устройства (торн-даун уровня 3, ленивая сборка); `Down` при сне их не обнуляет |

### 2.3 Когда список пуст

У endpoint'а нет устройства: `never_built`, `torn_down`, `down`, а также `building` (на время сборки снимок пропускается). Причину объясняет `endpointState` в том же ответе.

У `asleep` и `disabled` поверх живого устройства список есть: время хендшейка и выученный адрес переживают `Down`.

### 2.4 Ошибки

| Код | Когда |
|---|---|
| `NotFound` | Тега нет среди endpoint'ов (outbound с таким тегом тоже даёт `NotFound`) |
| `InvalidArgument` | Endpoint не WG/AWG (например, Tailscale) — проверяется по интерфейсу `PeerStatusReporter`, не по строке типа |
| `FailedPrecondition` | Сервис не запущен |
| `Unimplemented` | Сборка без `with_lx_command` или ядро старше lx.12-rc.2 |

### 2.5 Порядок пиров

Порядок пиров в конфиге. Дамп UAPI идёт по map, поэтому ядро пересортировывает его по конфигу.

### 2.6 CLI

`sing-box api peers [тег]` (тег `with_lx_command`) показывает таблицу с колонками `ENDPOINT STATE PEER ADDRESS HANDSHAKE RX TX`. Без тега обходит все endpoint'ы, у которых `GetOutbounds` показывает `endpointState`, и зовёт `GetWireGuardStatus` по каждому. Ключ в колонке `PEER` сокращён так же, как в логах wireguard-go (`fIrJ…Gmz4`), чтобы строку можно было сопоставить с логом. Endpoint без устройства выводится строкой с `-` в колонке пира. Неизвестный тег и тег не-WG — ошибка gRPC.

### 2.7 libbox

`CommandClient.GetWireGuardStatus(tag) (*WireGuardEndpointStatus, error)`; у объекта `EndpointTag`, `EndpointState`, `IdleSinceSeconds` и геттер `Peers() PeerStatusIterator` (gomobile не передаёт срезы структур). `OutboundGroupItem.Peers()` из rc.1 удалён.

## 3. Реализация

- `adapter/peer_status_lx.go`: тип `PeerStatus` и интерфейс `PeerStatusReporter`.
- `transport/wireguard/peer_status_lx.go`: `Endpoint.PeerStatuses()` под `pauseOpAccess` (тем же мьютексом `Close` и `Teardown` освобождают устройство) зовёт `device.IpcGet()`, разбирает блоки пиров и пересортировывает их по конфигу. Строки уровня устройства (`private_key`, AWG-ручки) идут до первого `public_key` и не читаются; `preshared_key` пропускается. Секреты наружу не попадают.
- `protocol/wireguard/peer_status_lx.go`: при `building` возвращает nil (во время сборки транспорт пишет `device` без мьютекса), иначе делегирует транспорту.
- `daemon/started_service_wireguard_lx.go`: обработчик `GetWireGuardStatus` и конверсия (нулевое время хендшейка → `0`, а не отрицательный Unix); `daemon/started_service_wireguard_lx_stub.go` — `Unimplemented` для сборки без тега. `GetOutbounds` пиров больше не заполняет.
- libbox: `command_types_wireguard_lx.go` (объект ответа, `PeerStatus`, итератор), `command_client_wireguard_lx.go` (вызов).
- CLI: `cmd/sing-box/cmd_api_peers_lx.go`.

Вызов не будит и не собирает устройство, на горячий путь не влияет: `IpcGet` выполняется только по запросу.

## 4. Границы

- Провод WG/AWG и сабмодуль `wireguard-go` не меняются.
- Имён у пиров нет; поле `name` в `WireGuardPeer` потребовало бы правки апстримного `option/wireguard.go` и отложено до явной необходимости (имена в логах ядра).
- Порог «жив/не жив» ядро не вычисляет — см. [CONSUMERS.md](CONSUMERS.md) §2.
- Tailscale-endpoint — отдельный вызов `GetTailscaleStatus` (SPEC 115).
- Поток `SubscribeOutbounds` и `GroupItem` статус пиров не несут.

## 5. Критерии приёмки

1. Юнит разбора дампа: base64-ключ, порядок по конфигу, нули у пира без хендшейка, ни `private_key`, ни `preshared_key` в результате.
2. Живой юнит на паре настоящих endpoint'ов через loopback: у серверного пира без `address` до трафика адреса и хендшейка нет, после — адрес `127.0.0.1:*`, время хендшейка и rx > 0; закрытый endpoint отдаёт nil.
3. `GetWireGuardStatus` (daemon): состояние и пиры у WG; пустой список и состояние у endpoint'а без устройства; `NotFound` / `InvalidArgument` / `FailedPrecondition`. libbox-клиент: проброс тега, итератор, ошибка.
4. Живой стенд на собранном бинаре: серверный и клиентский WG-конфиг на loopback; `sing-box api peers` до клиента, после трафика (обе стороны) и после остановки клиента; `GetOutbounds` без пиров.
5. Проверка на устройстве (LxBox, лаунчер) — после выпуска.
