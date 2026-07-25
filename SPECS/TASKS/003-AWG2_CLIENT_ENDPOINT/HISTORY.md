# HISTORY — SPEC 003 AWG2 client endpoint

Хронология вендоренного wireguard-go: базы графта, миграции, что менял upstream. Актуальное состояние — в [SPEC.md](SPEC.md); здесь только «как было раньше и почему переделали».

---

## Почему граф, а не прямой `replace` на amneziawg-go

Первая идея — подключить `amnezia-vpn/amneziawg-go` напрямую через `replace`. **Не работает:** amneziawg-go основан на *upstream* wireguard-go и не имеет sagernet-добавок (`Send(offset)`, `InputPacket`, `conn` reserved/control), на которых держится `transport/wireguard` sing-box. Прямой replace ломает сборку.

Решение — **3-way graft**: обфускация Amnezia накладывается поверх `sagernet/wireguard-go` (а не наоборот). Так контракт sing-box↔device остаётся sagernet'овским, обфускация аддитивна. Форк-модуль — `Leadaxe/wireguard-go-awg2-lx`.

## База графта: эволюция

| Дата | Submodule commit | Sagernet-база | wireguard-go версия | Контекст |
|------|------------------|---------------|---------------------|----------|
| 2026-06-09 | `27290b6` | `506b7631853c` | (pre-v0.0.3) | Первый граф на `v1.13.13`. 3-way merge `amnezia/master` (`f4f4c99`, AWG2 + S4-keepalive) поверх sagernet. |
| ~2026-07-02 | `e5feca7` | `19b0d35` (v0.0.3) | v0.0.3 | Миграция форка на ветку `lx-1.14` (upstream `v1.14.0-alpha.*`). Re-graft на v0.0.3. §010-GRO-guard из графа выброшен (v0.0.3 фиксит на источнике). |
| 2026-07-08 | `4b3a6c9` | `2c27bbf4f97f` (v0.0.5) | v0.0.5 | Re-graft на v0.0.5 вслед за upstream/testing. См. ниже. |

## Re-graft v0.0.3 → v0.0.5 (2026-07-08)

**Триггер:** upstream `sagernet/wireguard-go` двинулся `v0.0.3 → v0.0.5` (коммит-пин `2c27bbf4f97f`) ради **L3-forwarding**. sing-box upstream/testing забампил pin; форк нужно догнать.

**Что upstream изменил (v0.0.3 → v0.0.5), 5 коммитов, 8 файлов:**
- `9de6dc3 Add batched InputPackets` + `2c27bbf FIx batched InputPackets` — новый батч-вход `InputPackets([]*InputPacketRef) []*InputPacketRef` (возвращает unmatched refs — для L3-forward, где нет пира → вызывающий строит ICMP-unreachable). **`InputPacket` (singular) НЕ удалён** — переписан на size-based буфер + backpressure-кап `maxQueuedInputPackets`.
- `8403cdb Rework outbound buffer management` — **`QueueOutboundElement.buffer` сменил тип `*[MaxMessageSize]byte` → `[]byte`** (size-based пул через `GetOutboundBuffer(n)`/`PutOutboundBuffer` из sing-аллокатора, вместо фиксированного `messageBuffers`-пула). Элемент-пулы `outboundElements*` перешли с `WaitPool` на `sync.Pool`. Добавлен `peer.queuedOutboundPackets atomic.Int32` (backpressure-счётчик).
- `57baac9 Add batched UDP I/O on Darwin` + `fcbb7c4 Coalesce UDP GSO segments` — новый `conn/msgx_darwin.go` (sendmsg_x/recvmsg_x), GSO-iovec coalescing в `bind_std.go`.

**Оценка риска для графа ДО работы** (по памяти) была завышена: «`buffer`-type change ломает все AWG-хуки в send.go — основная работа». **По факту оказалось иначе:**

**Итог re-graft (`git apply --3way` граф-diff'а на v0.0.5):**
- **15 из 16** граф-файлов легли **чисто**. Конфликт — **только `send.go`**, и **на одной строке**: upstream добавил `peer.queuedOutboundPackets.Add(-…)` там, где граф добавил пустую строку. Взяли upstream (backpressure нужен).
- **Почему `buffer`-type change НЕ сломал граф:** AWG-хуки уже везде работают с `elem.buffer` как со **срезом** (`buffer[:MessageTransportHeaderSize]`, сдвиг `buffer[i+padding]`), а не как с массивом-указателем. Переход `*[N]byte → []byte` для них прозрачен.
- **Почему upstream `InputPacket`/`InputPackets` встали verbatim:** граф `send.go` их **не трогает** (junk-логика графа — в `SendHandshakeInitiation`, а не в input-пути), поэтому конфликта не было — upstream-версии сохранились.
- **Почему `RoutineEncryption` сшилась без ручного weave:** при `MessageEncapsulatingTransportSize = 0` upstream-offset `buffer[METS:METS+HeaderSize]` схлопывается к графовому `buffer[:HeaderSize]`. Граф-версия (заголовок в начале, без финального encapsulating re-slice) наложилась как есть.

**Вывод:** несущий инвариант `MessageEncapsulatingTransportSize = 0` — то, что делает re-graft дешёвым: он нейтрализует единственную точку, где upstream и граф расходятся по layout буфера.

Сборка после re-graft: device/conn/tun на linux/android/windows/darwin ✅; полный sing-box CLI с LX_TAGS (Go 1.24.7) ✅; тесты `transport/wireguard` + `protocol/wireguard` зелёные ✅.

## MTU / EMSGSIZE — находка 2026-06-10

При лайв-тесте AWG2-узла рукопожатие проходило, но трафик не шёл: `sendmsg: message too long` (**EMSGSIZE**). Причина — `S3`/`S4`: junk дописывается к **каждому** transport-сообщению, и обфусцированный data-пакет перерастает path MTU (1500, DF). Handshake маленький — проходит; transport — нет. Plain WG к тому же серверу с `mtu 1420` работает (S-junk нет).

Эмпирика (тот же узел, менялся только `mtu`, `S3=S4=60`):

| mtu | результат |
|----:|-----------|
| 1420 | ❌ EMSGSIZE |
| 1380 | ✅ ~58 ms |
| 1280 | ✅ ~55 ms |
| 1200 | ✅ ~60 ms |

Результат — MTU-политика в текущем SPEC.md (auto-default 1280 + warn при превышении бюджета). Источник находки — заметка агента лаунчера (`singbox-launcher`). Это не баг ядра, а размерный оверхед S-junk.

## Безопасность

Секреты живого AWG-сервера **никогда** не попадали в репозитории — лайв-конфиг держался только в `/tmp` и затирался (`shred`). Репо-конфиг `lx-test/config/awg2_basic.json` — с фейк-ключами.
