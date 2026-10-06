# SPEC: 114 — WG_PEER_STATUS

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — состояние каждого пира WG/AWG-endpoint'а в `GetOutbounds` |
| Статус | I (implemented) — заведена и реализована по просьбе владельца 2026-10-07; юниты и живой стенд на двух бинарях зелёные; не выпущена, на устройстве не проверялась |
| Ветка | `lx` |
| Build-tag | ядро (`adapter`, `transport/wireguard`, `protocol/wireguard`) — без тега, метод без вызова ничего не меняет; RPC и CLI — `with_lx_command` |
| Связано | [097](../097-LAZY_WG_DEVICE_BUILD/SPEC.md) (`endpointState` в том же `GroupItem`), [106](../106-WG_ENDPOINT_TOGGLE/SPEC.md) (`disabled`), [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (уровни сна) |
| Потребителям | [CONSUMERS.md](CONSUMERS.md) — сценарии использования |

## 1. Назначение

Раньше ядро не отвечало на вопрос «есть ли связь с пиром». `endpointState` (SPEC 097) говорит, собрано ли устройство, но не прошёл ли хендшейк. Узнать это можно было только из `debug`-логов wireguard-go (`received handshake response`). Серверу, у пира которого нет `address`, неоткуда было узнать, подключался ли клиент и откуда.

Теперь `GetOutbounds` отдаёт у WG/AWG-endpoint'а список пиров. По каждому пиру: публичный ключ, текущий адрес, время последнего хендшейка, rx/tx.

## 2. Контракт

### 2.1 Proto

```proto
message GroupItem {
  ...
  // lx:begin lx_command
  string endpointState = 5;
  int64 idleSinceSeconds = 6;
  repeated PeerStatus peers = 7;
  // lx:end lx_command
}

message PeerStatus {
  string publicKey = 1;         // base64, как public_key в конфиге
  string endpoint = 2;          // ip:port; пусто, пока неизвестен
  int64 lastHandshakeUnix = 3;  // 0 = хендшейка не было
  int64 rxBytes = 4;
  int64 txBytes = 5;
}
```

`PeerStatus` объявлен в конце `started_service.proto`, чтобы не сдвигать индексы апстримных сообщений в `started_service.pb.go`.

### 2.2 Семантика полей

| Поле | Смысл |
|---|---|
| `publicKey` | Идентификатор пира. Имени у пира нет (в `WireGuardPeer` нет такого поля); имя по ключу подставляет потребитель из своего конфига |
| `endpoint` | Адрес, на который устройство шлёт пакеты пиру. Если в конфиге задан `address`, это он. Если не задан (серверная сторона), адрес берётся из первого прошедшего проверку пакета пира и обновляется при roaming. Это **последний известный** адрес, а не признак связи: после ухода пира он остаётся. За NAT это внешний адрес NAT; через detour/relay — адрес промежуточного узла |
| `lastHandshakeUnix` | Время последнего завершённого хендшейка. Ядро вердикт «подключён» не выносит: порог зависит от таймингов (у AWG они бывают диапазонами), и его вычисляет потребитель |
| `rxBytes` / `txBytes` | Счётчики устройства wireguard-go: включают служебный трафик (хендшейки, keepalive). Обнуляются при пересборке устройства (торн-даун уровня 3, ленивая сборка); `Down` при сне их не обнуляет |

### 2.3 Когда список пуст

- Outbound не WG/AWG (включая Tailscale — у него свой `api tailscale`).
- У endpoint'а нет устройства: `never_built`, `torn_down`, `down`, а также `building` (на время сборки снимок пропускается). Причину объясняет `endpointState`.

У `asleep` и `disabled` поверх живого устройства список есть: время хендшейка и выученный адрес переживают `Down`.

### 2.4 Порядок пиров

Порядок пиров в конфиге. Дамп UAPI идёт по map, поэтому ядро пересортировывает его по конфигу.

### 2.5 CLI

`sing-box api peers [тег]` (тег `with_lx_command`) показывает таблицу с колонками `ENDPOINT STATE PEER ADDRESS HANDSHAKE RX TX`. Ключ в колонке `PEER` сокращён так же, как в логах wireguard-go (`fIrJ…Gmz4`), чтобы строку можно было сопоставить с логом. Endpoint без устройства выводится строкой с `-` в колонке пира. Неизвестный тег и тег не-WG — ошибка.

## 3. Реализация

- `adapter/peer_status_lx.go`: тип `PeerStatus` и интерфейс `PeerStatusReporter`.
- `transport/wireguard/peer_status_lx.go`: `Endpoint.PeerStatuses()` под `pauseOpAccess` (тем же мьютексом `Close` и `Teardown` освобождают устройство) зовёт `device.IpcGet()`, разбирает блоки пиров и пересортировывает их по конфигу. Строки уровня устройства (`private_key`, AWG-ручки) идут до первого `public_key` и не читаются; `preshared_key` пропускается. Секреты наружу не попадают.
- `protocol/wireguard/peer_status_lx.go`: при `building` возвращает nil (во время сборки транспорт пишет `device` без мьютекса), иначе делегирует транспорту.
- `daemon/started_service_command_lx.go`: `GetOutbounds` заполняет `peers` через `daemon/started_service_peers_lx.go`; нулевое время хендшейка → `0`, а не отрицательный Unix.
- libbox: у `OutboundGroupItem` неэкспортируемое поле `peers` под `lx:`-блоком и геттер `Peers() PeerStatusIterator` (gomobile не передаёт срезы структур).
- Поток `SubscribeOutbounds` (апстримный builder) поле не заполняет, как и `endpointState`.

Вызов не будит и не собирает устройство, на горячий путь не влияет: `IpcGet` выполняется только по pull-запросу.

## 4. Границы

- Провод WG/AWG и сабмодуль `wireguard-go` не меняются.
- Имён у пиров нет; поле `name` в `WireGuardPeer` потребовало бы правки апстримного `option/wireguard.go` и отложено до явной необходимости (имена в логах ядра).
- Порог «жив/не жив» ядро не вычисляет — см. [CONSUMERS.md](CONSUMERS.md) §2.
- Tailscale-endpoint не охвачен.

## 5. Критерии приёмки

1. Юнит разбора дампа: base64-ключ, порядок по конфигу, нули у пира без хендшейка, ни `private_key`, ни `preshared_key` в результате.
2. Живой юнит на паре настоящих endpoint'ов через loopback: у серверного пира без `address` до трафика адреса и хендшейка нет, после — адрес `127.0.0.1:*`, время хендшейка и rx > 0; закрытый endpoint отдаёт nil.
3. `GetOutbounds` (daemon) и libbox-конверсия: поле у WG, пусто у прочих и у endpoint'а без устройства.
4. Живой стенд на собранном бинаре: серверный AWG-конфиг владельца (без `address` у пира, `listen_port`, `jc`/`s1–s4`/`h1–h4`, `ip: quic`) и клиент; `sing-box api peers` до клиента, после хендшейка и после остановки клиента.
5. Проверка на устройстве (LxBox, лаунчер) — после выпуска.
