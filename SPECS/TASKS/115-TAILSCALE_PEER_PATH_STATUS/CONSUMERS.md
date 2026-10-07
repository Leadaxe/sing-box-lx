# Путь пира Tailscale: руководство для потребителей

**Задача:** [SPEC 115](SPEC.md) · **Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

## 1. Где брать

| Канал | Вызов | Что читать |
|---|---|---|
| gRPC | `GetTailscaleStatus(TailscaleStatusRequest{endpointTag})` → `TailscaleEndpointStatus` | у каждого `TailscalePeer` (в `userGroups[].peers[]`, `exitNode`): `path`, `endpoint`, `peerRelay`, `derpRegionCode`, `lastHandshake`; у статуса — `health[]` |
| libbox | `CommandClient.GetTailscaleStatus(endpointTag)` → `*TailscaleEndpointStatus` | `TailscalePeer.Path` (строка), `Endpoint`, `PeerRelay`, `DERPRegionCode`, `LastHandshake`; `Health()` |
| CLI | `sing-box api tailscale peers [--endpoint тег]` | таблица `PEER IP ONLINE PATH VIA HANDSHAKE RX TX` + строка `Health` |

Список устройств для вкладки Network по-прежнему даёт поток `SubscribeTailscaleStatus`: те же поля там есть, но это снимок на момент последнего события — путь мог смениться после. Вкладка диагностики узла опрашивает `GetTailscaleStatus`, пока открыта; ориентир — раз в 2–5 с, в фоне не опрашивать.

## 2. Что показывать

| `path` | Строка | Откуда детали |
|---|---|---|
| `DIRECT` | `direct 1.2.3.4:41641` | `endpoint`; домашний регион в `derpRegionCode` — справочно («home fra») |
| `PEER_RELAY` | `peer relay` | `peerRelay` |
| `DERP` | `relay fra` | `derpRegionCode` |
| `NONE` | пусто | узел ни разу не слал пиру |

`DIRECT` у idle-пира — нормально: путь выбран, трафика нет. Точку активности рисуйте по `active`, как раньше. `lastHandshake` показывайте возрастом (`now − lastHandshake`), давность снимка на это не влияет; `0` — «хендшейка не было».

**Exit-node работает?** `exitNode.online` — виден control-plane; `exitNode.path != NONE` и свежий `lastHandshake` — с ним есть туннель; `health` — что мешает (нет DERP, exit-node offline и т.п.).

## 3. Ошибки

`NotFound` — тега нет; `InvalidArgument` — не Tailscale; `FailedPrecondition` — сервис или endpoint не запущен; `Unimplemented` — ядро без `with_lx_command` или старше lx.12-rc.2 (показывайте то, что даёт поток, без пути).

## 4. Чего нет

- Тика в потоке; смена пути событием не является.
- Привязки соединения к пиру — следующая спека (`Connection.tailscalePeerID`).
- Вердиктов «быстро/медленно», истории переключений, счётчиков по путям.
