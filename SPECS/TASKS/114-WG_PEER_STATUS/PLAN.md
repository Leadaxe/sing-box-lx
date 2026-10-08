# PLAN: 114 — WG_PEER_STATUS

1. **Источник данных** — `device.IpcGet()`: на каждого пира `public_key` (hex), `endpoint`, `last_handshake_time_sec/nsec`, `rx_bytes`, `tx_bytes`. Готовая точка чтения уже есть (`TransferTotals`, SPEC 020). Сабмодуль не трогается.
2. **Слои**:
   - `adapter` — тип `PeerStatus` и интерфейс `PeerStatusReporter` (новый файл, без тега);
   - `transport/wireguard` — разбор дампа под `pauseOpAccess`, пересортировка по `e.peers`, hex → base64;
   - `protocol/wireguard` — страж `building`, делегирование;
   - `daemon` — `GetWireGuardStatus` в `lx:`-блоке сервиса, сообщения `WireGuardStatusRequest` / `WireGuardEndpointStatus` / `PeerStatus` в хвостовом `lx:`-блоке proto; обработчик и `Unimplemented`-заглушка в новых файлах; `GroupItem` не трогается (поле 7 зарезервировано);
   - libbox — объект ответа и итератор в новом файле, вызов `CommandClient.GetWireGuardStatus(tag)`;
   - CLI — `sing-box api peers [тег]` в новом файле под `with_lx_command`.
3. **Конкурентность**. `Close`/`Teardown` обнуляют `device` под `pauseOpAccess`, а чтение идёт под тем же мьютексом. Сборка (`Start` после `Rebuild`) пишет `device` без мьютекса; это окно закрыто стражем `building` на протокольном слое. Начальный `Start` на старте ядра завершается до `STARTED` сервиса, поэтому вызов в него не попадает.
4. **Тесты**: разбор дампа; живая пара endpoint'ов на loopback (`with_gvisor`); daemon — обработчик с кодами ошибок; libbox — клиент; `-race`.
5. **Живой стенд**: два процесса `sing-box run` (сервер с пиром без `address` и клиент) и `sing-box api peers`.
6. **Доки**: SPEC, HISTORY, CONSUMERS, FEATURE 006, Roadmap, `lxd-grpc-api(.ru).md`, changelog.
