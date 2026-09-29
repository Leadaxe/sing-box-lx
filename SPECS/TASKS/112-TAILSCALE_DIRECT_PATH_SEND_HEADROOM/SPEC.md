# SPEC: 112 — TAILSCALE_DIRECT_PATH_SEND_HEADROOM

**Фича:** [AWG](../../FEATURES/003-AWG/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B — регрессия от нашего графта: AWG-правка wireguard-go ломает Tailscale |
| Статус | I (implemented) — вариант A, юниты зелёные; проверки на устройстве (тесты 4–5) не было |
| Ветка | `lx` |
| Base | `350f92ae1` (v1.14.2-lx.9) |
| Связанные | Issue [#33](https://github.com/Leadaxe/sing-box-lx/issues/33); SPEC 003 (инвариант `MessageEncapsulatingTransportSize = 0`); SPEC 051 (перепрививка); полевой разбор 2026-09-29 |

**Touches:** `submodules/wireguard-go` (`device/noise-protocol.go`, возможно `device/send.go`,
`device/peer.go`), SPEC 003 (раздел про несущие инварианты и процедуру перепрививки).

## Why

Узел Tailscale не передаёт трафик пиру, как только до пира установлен прямой UDP-путь. Через DERP
трафик идёт. Снаружи узел выглядит исправным: статус running, список устройств заполнен, проверка
устройства (`tailcfg.PingDisco`) отвечает. TCP-соединение к пиру падает через 15 с
(`context deadline exceeded`, таймаут SPEC 052).

### Причина

1. Графт AWG обнулил запас перед пакетом в нашем wireguard-go:
   `submodules/wireguard-go/device/noise-protocol.go:71`, `MessageEncapsulatingTransportSize = 0`.
   В sagernet/wireguard-go значение 8.
2. Это значение уходит параметром `offset` в `conn.Bind.Send()` (`device/peer.go:191`,
   `device/peer.go:502`).
3. `replace` в `go.mod` глобальный, поэтому устройство WireGuard внутри Tailscale собрано из того же
   форка.
4. magicsock на прямом UDP-пути принимает только `offset == 8`
   (`sagernet/tailscale@v1.102.1-sing-box-1.14-mod.5`, `wgengine/magicsock/rebinding_conn.go:85`) и
   иначе возвращает ошибку. В запас он пишет Geneve-заголовок для peer relay.
5. Путь через DERP проверки не содержит (`buff[offset:]`), disco-сообщения через эту функцию не
   идут. Поэтому ping и трафик через релей работают.

SPEC 003 называет ноль несущим инвариантом графта с обоснованием «sing-box-lx запас не использует».
Для узла WireGuard это верно, для Tailscale нет.

### Полевые факты (2026-09-29)

- Ядро 1.14.2-lx.8, Android, узел `tailscale` без `detour` и без exit node, пир в той же Wi-Fi.
- Политика tailnet по умолчанию (allow all), все узлы одного пользователя, тегов нет.
- Тот же пир и порт с того же телефона доступны через другие клиенты Tailscale.
- Лог уровня trace, порядок событий:
  1. `wg: [peer] - Sending handshake initiation` (прямого пути ещё нет, уходит через DERP);
  2. `magicsock: disco: node [peer] now using <lan-ip>:<port>`, `new contact ... via=direct`;
  3. `wg: [peer] - Received handshake response`;
  4. через 15 с `Retrying handshake because we stopped hearing back after 15 seconds`;
  5. `wg: [peer] - Failed to send handshake initiation: RebindingUDPConn.WriteWireGuardBatchTo: [unexpected] offset (0) != Geneve header length (8)`.
- На уровнях `info` и `debug` ошибки не видно: tsnet `Logf` отображён на `trace`.

### Почему не заметили

- Проверки графта (SPEC 003, процедура перепрививки) покрывают `transport/wireguard`,
  `protocol/wireguard` и живой AWG-туннель. Узла Tailscale в них нет.
- Константа не меняет типов, сборка проходит.
- Отказывает только прямой путь; узел за `detour`, где прямой путь не строится, работает.

## What

Устройство WireGuard, которое создаёт Tailscale, передаёт в `Bind.Send()` `offset == 8`, и перед
каждым пакетом в буфере есть 8 свободных байт. Поведение узлов WireGuard и AWG не меняется.

### Условия

1. Узел Tailscale, прямой UDP-путь до пира: рукопожатие и данные уходят без ошибки `offset`.
2. Узел Tailscale, путь через DERP: работает как сейчас.
3. Узел WireGuard без AWG-полей: байты на проводе те же, что до правки.
4. Узел AWG (2.0 и 3.x: `s1`–`s4`, `h1`–`h4`, `i1`–`i5`, header protection, паддинг): байты на
   проводе те же, что до правки. Запас в датаграмму не попадает.
5. Смена `s4` на лету (перенос элемента в `RoutineEncryption`) работает как сейчас.

### Варианты правки

Выбирает исполнитель по результату проверок, в этом порядке.

**A. Вернуть константу 8 глобально.** Текущий `send.go` уже считает раскладку как
`[запас][паддинг s4][заголовок][содержимое]` и везде складывает запас с паддингом
(`plaintextOffset`, `outboundLayout`, `RoutineReadFromTUN`, `RoutineEncryption`). Если это так на
всех путях отправки, правка сводится к одной константе и снимает отличие от апстрима. Проверить
обязательно:
- пути рукопожатия, cookie, junk и `i1`–`i5`: буфер собран с запасом и уходит с тем же `offset`;
- `Bind` узла WireGuard (`transport/wireguard`) отрезает запас и не отправляет его в сеть;
- `MaxContentSize` уменьшается на 8: MTU и `allocLength` не дают `EMSGSIZE` на AWG с паддингом
  (находка SPEC 003 от 2026-06-10).

**B. Запас на устройство.** Если A ломает раскладку AWG: поле устройства, 8 без обфускации и 0 с
AWG-параметрами; константа в смещениях заменяется полем. Дороже при перепрививке, поэтому только
если A не проходит.

Правка в tailscale (снять проверку `offset`) не рассматривается: форка tailscale у нас нет, и запас
нужен для peer relay.

## Тест

1. Юнит в `submodules/wireguard-go/device`: `Bind`-заглушка фиксирует `offset` и содержимое буфера;
   для устройства без обфускации `offset == 8`, пакет начинается с `buf[8]`.
2. Юнит на AWG: датаграмма на выходе `Bind` побайтно равна эталону до правки (существующие 8
   AWG-тестов остаются зелёными).
3. Юнит или интеграционный тест на узел Tailscale: отправка через `RebindingUDPConn` без ошибки
   `offset`.
4. Устройство: узел Tailscale без `detour`, пир в той же сети, TCP к пиру открывается; в trace-логе
   нет строки `WriteWireGuardBatchTo: [unexpected] offset`.
5. Устройство: живой AWG-туннель (2.0 и 3.x) и обычный WireGuard работают.

## Документы

- SPEC 003: инвариант `MessageEncapsulatingTransportSize = 0` заменить фактическим правилом;
  в процедуру перепрививки добавить проверку узла Tailscale с прямым путём.
- `docs-lx/lx-changelog.md`, индекс `SPECS/README.md`: строка задачи.

## Реализация

Вариант A. В `submodules/wireguard-go`:

- `device/noise-protocol.go`: `MessageEncapsulatingTransportSize = 8`, строка как в апстриме.
- `device/send.go`: буферы рукопожатия, ответа, cookie и `i1`–`i5` выделяются с запасом, датаграмма
  собирается после него; cookie уходит с `offset` = запас (было 0).
- `device/noise-protocol.go`, `JunkPackets`: junk-буферы с запасом.
- Путь данных и keepalive (`plaintextOffset`, `outboundLayout`, `RoutineReadFromTUN`,
  `RoutineEncryption`) уже складывал запас с паддингом, правка не понадобилась.

Проверено по коду: `StdNetBind`, `WinRingBind` и `ClientBind` отрезают `offset` до записи в сокет,
`reserved` пишется после запаса. `MaxContentSize` уменьшился на 8, как в апстриме; буфер
`MaxMessageSize` вмещает запас при любом `s4` из uapi на MTU туннеля.

Тесты:

- `device/lx_send_headroom_test.go`: пара устройств (WireGuard, AWG 2.0 с `jc`/`s1`–`s4`/`h`/`i1`/`i3`,
  AWG 3.x с header protection и паддингом). `Bind`-обёртка проверяет `offset == 8`, затирает запас
  `0xff`, как magicsock Geneve-заголовком, и пропускает трафик в обе стороны. С константой 0 тест
  падает.
- `protocol/tailscale/send_headroom_lx_test.go`: запас равен `packet.GeneveFixedHeaderLength`.
- Существующие тесты `device`, `transport/wireguard`, `protocol/wireguard` зелёные; сабмодуль
  собирается под windows/linux/android/ios.
