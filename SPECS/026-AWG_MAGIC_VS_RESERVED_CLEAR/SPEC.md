# SPEC: 026 — AWG_MAGIC_VS_RESERVED_CLEAR

| Поле | Значение |
|------|----------|
| Тип | B (bug) |
| Статус | C (complete) |

Reserved-байты Cloudflare WARP (байты 1–3 датаграммы) обнулялись **безусловно
на каждом принятом пакете** во всех bind'ах. AmneziaWG 2.0 пишет свой
magic-заголовок в те же байты при малом padding → обнуление уничтожает magic →
пакет молча отбрасывается, включая handshake → AWG-endpoint не поднимается
вообще. Фикс: обнулять reserved только когда он реально сконфигурирован (WARP).

Баг в фиче [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT) /
[005 AWG2_RANGED_MAGIC_HEADERS](../005-AWG2_RANGED_MAGIC_HEADERS). Найден по коду
при разборе [issue #8](https://github.com/Leadaxe/sing-box-lx/issues/8) (там
корень оказался другой — path-MTU; этот баг у того юзера НЕ проявлялся, см. §1).
Дефект **нашей** дельты (reserved-clear — lx-механизм для WARP поверх форка) → в
скоупе (CONSTITUTION §3.1).

---

## 1. Проблема / контекст

**Где живёт magic на приёме.** `submodules/wireguard-go/device/receive.go`
`DeterminePacketTypeAndPadding` читает тип как
`binary.LittleEndian.Uint32(packet[padding:])`, где `padding` = `s1`/`s2`
(handshake init/response) или `s4` (transport). То есть magic лежит в байтах
**`[padding .. padding+3]`** от начала датаграммы. `magicHeader.Validate`
проверяет `start <= val <= end` для сконфигурированного диапазона `h1..h4`.

**Reserved-clear.** Для Cloudflare WARP bind обнуляет «reserved» байты 1–3
входящей датаграммы (WARP их эхо-ит, а ядру нужны нули). Это делалось
**безусловно** на каждом принятом пакете, во всех receive-путях всех bind'ов.

**Столкновение.** Когда `padding` мал (0–3), диапазон magic `[padding..padding+3]`
**пересекает** байты 1–3:

| `s`-padding | magic в байтах | пересекает [1..3]? |
|---|---|---|
| 0 | [0..3] | да |
| 1 | [1..4] | да |
| 2 | [2..5] | да |
| 3 | [3..6] | да |
| ≥4 | [≥4..] | нет |

При пересечении обнуление старших байтов оставляет `val ≤ 255`, что выпадает из
любого реального `h1..h4` (сотни млн) → `MessageUnknownType` → пакет
отбрасывается. Рушит **и handshake, и transport** → нода не поднимается.

**Почему долго не замечали:**
- Обычный WireGuard: тип = 1/2/3/4, байты 1–3 уже нули → обнуление безвредно.
- AWG с `padding ≥ 4` (типичные экспорты задают `s1`/`s2` вместе с `h1..h4`):
  magic смещён за байты 1–3 → обнуление безвредно. Так, issue #8 (`s1=15`,
  `s4=12`) от ЭТОГО бага не страдал — magic жил в байтах 12–18.
- Проявляется только при `s1`/`s2`/`s4` = 0..3 с заданными ranged `h1..h4`.

**Два пути bind** ([transport/wireguard/endpoint.go](../../transport/wireguard/endpoint.go)
`bindDevice`):
- Без detour: dialer = `DefaultDialer` (реализует `WireGuardListener`) → форк
  `StdNetBind` (linux `receiveIP`, darwin msgx, windows `WinRingBind`).
- С detour: dialer иной → ядровый `ClientBind` (`transport/wireguard/client_bind.go`).

Обнуление было безусловным в **обоих** → баг затрагивал и detour, и не-detour.

## 2. Цель

AWG-endpoint с ranged `h1..h4` и малым padding поднимается и держит трафик.
Reserved-байты трогаются только в WARP-режиме (reserved сконфигурирован).
Поведение WARP не меняется; обычный WG не затронут.

## 3. Требования

### 3.1 Гейт по наличию reserved
- Обнулять байты 1–3 на приёме **только** когда сконфигурирован ненулевой
  reserved (`hasReserved()`): глобальный (`ClientBind.reserved`) или любой
  per-endpoint (`reservedForEndpoint`).
- Send-стороны уже условны (штамп reserved под `loaded` / ненулевой) — не трогать.

### 3.2 Все receive-clear
Пять мест в форке + одно в ядре, все под `hasReserved()`:
- `submodules/wireguard-go/conn/bind_std.go` `receiveIP` (linux/android batch);
- `conn/msgx_darwin.go` `receiveSingle` + `makeReceiveMsgX` (darwin msgx —
  активный путь на darwin, `supportsMsgX=true`);
- `conn/bind_windows.go` `receiveIPv4` + `receiveIPv6`;
- `transport/wireguard/client_bind.go` `receive` (detour-путь, ядро).

### 3.3 Изоляция
- Форк: правки в `conn/` под маркером `// lx:`, новый метод `hasReserved()` на
  `StdNetBind` и `WinRingBind`. Правка сабмодуля — осознанный шаг (CONSTITUTION §4).
- Ядро: `ClientBind` — lx-файл целиком, добавлен `hasReserved()`.

## 4. Критерии приёмки

- e2e red/green: пара Device через `StdNetBind` и через `ClientBind` с `padding=0`
  (magic в байтах 0–3) — доставка пакета GREEN с фиксом, timeout RED без.
  (`device/awg_stdnetbind_reserved_lx_test.go`,
  `transport/wireguard/awg_detour_clientbind_lx_test.go`.)
- `hasReserved()`-гейт запиннен юнит-тестами (`conn/reserved_gate_lx_test.go`,
  `transport/wireguard/client_bind_reserved_lx_test.go`).
- WARP: при заданном reserved обнуление/штамп работают как раньше.
- Все GOOS (linux/darwin/windows) собираются; форк `go test ./device ./conn` и
  ядро `-tags with_awg,with_gvisor` зелёные; `gofmt` чисто.

## 5. Вне скоупа

- Гонка `c.conn` в `ClientBind.connect()` (fast-path чтение без `connAccess`) —
  пре-существующий латентный баг кэширования соединения, всплывает под `-race`,
  к reserved отношения не имеет. Отдельная задача.
- Per-source-endpoint гейт на receive: сейчас `hasReserved()` глобальный (любой
  reserved в бинде). Смешанный WARP+AWG в одном бинде нереалистичен (AWG —
  device-global поле, reserved — per-peer; режимы взаимоисключающие), поэтому
  глобального гейта достаточно. Если per-peer микс когда-нибудь станет реальным —
  ключевать receive-гейт по адресу источника, как уже делает send.

## 6. Ссылки

- `submodules/wireguard-go/device/receive.go` `DeterminePacketTypeAndPadding`
  (magic = `Uint32(packet[padding:])`), `magic-header.go` `Validate`
- `submodules/wireguard-go/conn/{bind_std,msgx_darwin,bind_windows}.go`
  (receive-clear + `hasReserved`)
- `transport/wireguard/client_bind.go` (detour-bind, receive+send гейты)
- [issue #8](https://github.com/Leadaxe/sing-box-lx/issues/8) — где найден (корень
  того тикета иной: path-MTU)
- Смежное: [025](../025-AWG_TRANSPORT_PADDING_OVERRUN) (тот же графт, крэш-класс)
