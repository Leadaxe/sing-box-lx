# SPEC 114 — HISTORY

Актуальный контракт — в [SPEC.md](SPEC.md); здесь только «как было и почему переделали».

---

## rc.1 (2026-10-07, утро) — пиры внутри `GetOutbounds`

`GroupItem.peers = 7` заполнялся в `GetOutbounds` у каждого WG/AWG-узла; libbox — `OutboundGroupItem.Peers()`; CLI `api peers` читал список узлов. Выпущено в `v1.14.2-lx.12-rc.1` (коммиты `ade25726a` … `46ed51699`).

Мотив тогда: один вызов на экран узлов, `endpointState` рядом.

## rc.2 (2026-10-07, вечер) — отдельный `GetWireGuardStatus(tag)`

Решение владельца при обсуждении статуса пиров Tailscale: «у каждого типа endpoint'а свой запрос статуса, `GetOutbounds` остаётся списком узлов без чужих потрохов». Параллельно заведён `GetTailscaleStatus` (SPEC 115) по тому же паттерну, что `SubscribeGroups → GetGroups`, `SubscribeOutbounds → GetOutbounds`.

Что изменилось:

- поле `GroupItem.peers = 7` удалено, номер зарезервирован; `endpointState`/`idleSinceSeconds` остались;
- `WireGuardEndpointStatus` несёт состояние устройства вместе с пирами, так что пустой список объясняется тем же ответом;
- libbox: `OutboundGroupItem.Peers()` → `CommandClient.GetWireGuardStatus(tag).Peers()`;
- CLI `api peers` ходит в новый RPC (без тега — по всем endpoint'ам с `endpointState`).

Семантика полей `PeerStatus` и руководство потребителя (вердикт «на связи» выводит потребитель) не менялись. Поскольку rc.1 — пререлиз, совместимость с его потребителем не сохранялась: лаунчер (`cacd727e`, не запушен) переводится на новый вызов после rc.2.
