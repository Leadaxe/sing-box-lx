# SPEC: 115 — TAILSCALE_PEER_PATH_STATUS

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — статус Tailscale-endpoint'а по запросу `GetTailscaleStatus` с путём к каждому пиру (direct / peer relay / DERP) и предупреждениями бэкенда |
| Статус | I (implemented) — контракт согласован с лаунчером и утверждён владельцем 2026-10-07; юниты зелёные; живой Tailscale-прогон — на стороне лаунчера после rc.2 |
| Ветка | `lx` |
| Build-tag | ядро (`protocol/tailscale`) — `with_gvisor`/`with_tailscale`, как сам endpoint; RPC и CLI — `with_lx_command` |
| Связано | [114](../114-WG_PEER_STATUS/SPEC.md) (тот же паттерн для WG/AWG), [112](../112-TAILSCALE_DIRECT_PATH_SEND_HEADROOM/SPEC.md) (прямой путь не работал — теперь это видно без `trace`), [113](../113-TAILSCALE_ALWAYS_DERP_REBIND_LEAK/SPEC.md) |
| Потребителям | [CONSUMERS.md](CONSUMERS.md) |

## 1. Назначение

Tailscale-узел «неуправляем»: magicsock сам выбирает, как слать пакеты каждому пиру — прямым UDP, через peer relay или через DERP, и переключает путь на каждый пакет. Ядро знало этот выбор (`ipnstate.PeerStatus.CurAddr` / `PeerRelay` / `Relay`), но наружу не отдавало: в `TailscalePeer` были только `online` и `active`, а путь можно было узнать лишь поштучным `StartTailscalePing`. Исправность exit-node была видна только как «выбран и подключён к control-plane».

Теперь:

- в `TailscalePeer` — вердикт пути и его детали: прямой адрес, relay, домашний DERP-регион, время хендшейка;
- в `TailscaleEndpointStatus` — предупреждения бэкенда (`health`);
- унарный `GetTailscaleStatus(endpointTag)` даёт свежий снимок по запросу — для вкладки диагностики узла. Поток `SubscribeTailscaleStatus` остаётся событийным, без тика (решение владельца: «не засорять подписчиков»).

## 2. Контракт

### 2.1 Proto

```proto
rpc GetTailscaleStatus(TailscaleStatusRequest) returns (TailscaleEndpointStatus) {}

message TailscaleStatusRequest { string endpointTag = 1; }

message TailscaleEndpointStatus {
  ...
  repeated string health = 16;       // предупреждения бэкенда; пусто = здоров
}

message TailscalePeer {
  ...
  TailscalePeerPath path = 18;
  string endpoint = 19;              // ip:port прямого пути; иначе пусто
  string peerRelay = 20;             // ip:port:vni peer relay; иначе пусто
  string derpRegionCode = 21;        // домашний DERP пира; есть и при DIRECT; пусто, пока регион неизвестен
  int64 lastHandshake = 22;          // Unix-секунды последнего хендшейка WireGuard; 0 = не было
}

enum TailscalePeerPath {
  TAILSCALE_PEER_PATH_NONE = 0;        // узел ни разу не слал пиру; путь не выбран
  TAILSCALE_PEER_PATH_DIRECT = 1;
  TAILSCALE_PEER_PATH_PEER_RELAY = 2;
  TAILSCALE_PEER_PATH_DERP = 3;
}
```

Поля в апстримных сообщениях — в `lx:`-блоках; RPC, запрос и enum — в `lx:`-блоках сервиса и хвоста proto. Имена `endpoint` / `peerRelay` / `derpRegionCode` совпадают с `TailscalePingResponse`, чтобы по пиру и по пингу читалось одно и то же.

### 2.2 Вердикт `path`

Считает ядро, потому что приоритет — правило magicsock, а не потребителя. Источник — `ipnstate.PeerStatus`, который magicsock заполняет из `addrForSendLocked` («куда бы послал сейчас»):

| Условие | `path` |
|---|---|
| `CurAddr` непустой | `DIRECT` — даже у idle-пира: путь выбран, трафика может не быть |
| иначе `PeerRelay` непустой | `PEER_RELAY` |
| иначе `LastWrite` ненулевой (узел хоть раз слал пиру) | `DERP` |
| иначе | `NONE` |

`active` (поле 8) в вердикт не входит: это отдельный флаг «писали пиру в последние секунды». `derpRegionCode` берётся из `Relay` — домашний DERP пира известен и до трафика, поэтому отдаётся при любом пути.

### 2.3 Свежесть

| Канал | Что в полях пути |
|---|---|
| `GetTailscaleStatus` | Свежий `LocalBackend.Status()` на каждый вызов. Правда на момент запроса |
| `SubscribeTailscaleStatus` | Снимок на момент последнего события IPN-шины (пир появился/ушёл, exit-node, prefs). Смена пути событием **не является**, тика нет — поля могут отставать до следующего события |

Дешёвого сигнала о смене пути в форке tailscale нет: magicsock публикует в eventbus только `HomeDERPChanged` (про себя), смена `bestAddr` пира — только лог; `Status()` «только пиры» у `LocalBackend` нет. Форк — go.mod-зависимость, не сабмодуль, хук добавить нельзя. Потому опрос по запросу, частоту задаёт вкладка.

### 2.4 Ошибки

| Код | Когда |
|---|---|
| `NotFound` | Тега нет среди endpoint'ов |
| `InvalidArgument` | Endpoint не Tailscale |
| `FailedPrecondition` | Сервис не запущен; endpoint ещё не стартовал (`Status()` до `Start` невозможен) |
| `Unimplemented` | Сборка без `with_lx_command` или ядро старше lx.12-rc.2 |

Ответ — **полный** `TailscaleEndpointStatus` с `self`, `exitNode`, `userGroups`, тем же конвертером, что в потоке: диагностике не нужно склеивать два источника.

### 2.5 CLI

`sing-box api tailscale peers [--endpoint тег]` (тег `with_lx_command`) — таблица `PEER IP ONLINE PATH VIA HANDSHAKE RX TX`; `VIA` — адрес прямого пути (с домашним регионом в скобках), relay или код DERP-региона. Ниже — строка `Health`, если предупреждения есть. Endpoint выбирается как у остальных подкоманд `tailscale`.

### 2.6 libbox

`CommandClient.GetTailscaleStatus(endpointTag) (*TailscaleEndpointStatus, error)`. У `TailscalePeer` — `Path` строкой (`"direct"` / `"peer_relay"` / `"derp"` / `""`), `Endpoint`, `PeerRelay`, `DERPRegionCode`, `LastHandshake`; у `TailscaleEndpointStatus` — `Health() StringIterator`. Те же поля заполняются и в объектах потока.

## 3. Реализация

- `adapter/tailscale_status_lx.go`: константы `TailscalePeerPath*` и интерфейс `TailscaleStatusProvider`; поля в `adapter/tailscale.go` — `lx:`-блоки.
- `protocol/tailscale/status_lx.go`: `TailscaleStatus()` (ошибка до старта), общий сборщик `collectTailscaleStatus` для потока и запроса, `fillTailscalePeerPath` — вердикт. В `status.go` — три `lx:`-строки: вызов сборщика, вызов `fillTailscalePeerPath`, `Health`.
- `daemon/started_service_tailscale_lx.go`: обработчик (`resolveTailscaleEndpoint` даёт `NotFound`/`InvalidArgument`); `_stub.go` — `Unimplemented`; `daemon/tailscale_path_lx.go` — маппинг в enum без тега (его зовёт апстримный конвертер потока). В `started_service.go` — `lx:`-блок в `tailscalePeerToProto` и строка `Health`.
- libbox: `command_client_tailscale_lx.go` (вызов, маппинг enum → строка), `lx:`-блоки в `command_types_tailscale.go`.
- CLI: `cmd/sing-box/cmd_api_tailscale_peers_lx.go`.

Стоимость запроса: `LocalBackend.Status()` = обход peerMap под локом magicsock + UAPI-дамп wireguard-go по всем пирам. Для десятков пиров незаметно; выполняется только по запросу.

## 4. Границы

- Тик и `NotifyPeerWireGuardState` в поток — нет (решение владельца).
- `Connection.tailscalePeerID` (к какому пиру идёт соединение, через `PeerForIP`) — **следующая спека**, после ресинка daemonpb у лаунчера.
- Вердикты «быстро/медленно», история переключений, счётчики по путям, имя пира в соединении — уровень потребителя.
- Пин конкретного endpoint'а на пир у Tailscale нет и без правки форка не появится.

## 5. Критерии приёмки

1. Юнит вердикта над `ipnstate.PeerStatus`: direct (в т.ч. idle), peer relay, DERP, none, регион неизвестен; `Health` проходит через конвертер.
2. daemon: полный статус с путём/регионом/хендшейком у `exitNode` и пиров; `NotFound` / `InvalidArgument` / `FailedPrecondition`.
3. libbox-клиент: проброс тега, строки пути, `Health()`.
4. Живой прогон на tailnet (лаунчер после rc.2): пир с прямым путём показывает `direct ip:port`, пир за DERP — `derp <регион>`; после `TS_DEBUG_NEVER_DIRECT_UDP` все `derp`.
