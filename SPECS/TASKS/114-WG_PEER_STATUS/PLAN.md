# PLAN: 114 — WG_PEER_STATUS

1. **Источник данных** — `device.IpcGet()`: на каждого пира `public_key` (hex), `endpoint`, `last_handshake_time_sec/nsec`, `rx_bytes`, `tx_bytes`. Готовая точка чтения уже есть (`TransferTotals`, SPEC 020). Сабмодуль не трогается.
2. **Слои**:
   - `adapter` — тип `PeerStatus` и интерфейс `PeerStatusReporter` (новый файл, без тега);
   - `transport/wireguard` — разбор дампа под `pauseOpAccess`, пересортировка по `e.peers`, hex → base64;
   - `protocol/wireguard` — страж `building`, делегирование;
   - `daemon` — `GroupItem.peers = 7` и сообщение `PeerStatus` в конце proto (минимальный дифф `pb.go`), заполнение в `GetOutbounds`;
   - libbox — поле под `lx:`-блоком в `command_types.go` и геттер-итератор в новом файле;
   - CLI — `sing-box api peers` в новом файле под `with_lx_command`.
3. **Конкурентность**. `Close`/`Teardown` обнуляют `device` под `pauseOpAccess`, а чтение идёт под тем же мьютексом. Сборка (`Start` после `Rebuild`) пишет `device` без мьютекса; это окно закрыто стражем `building` на протокольном слое. Начальный `Start` на старте ядра завершается до `STARTED` сервиса, поэтому `GetOutbounds` в него не попадает.
4. **Тесты**: разбор дампа; живая пара endpoint'ов на loopback (`with_gvisor`); daemon и libbox — конверсия; `-race`.
5. **Живой стенд**: два процесса `sing-box run` (серверный AWG-конфиг владельца и клиент) и `sing-box api peers`.
6. **Доки**: SPEC, CONSUMERS (сценарии), FEATURE 006, Roadmap, `lxd-grpc-api(.ru).md`.
