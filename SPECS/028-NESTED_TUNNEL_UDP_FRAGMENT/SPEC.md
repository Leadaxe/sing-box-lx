# SPEC: 028 — NESTED_TUNNEL_UDP_FRAGMENT

| Поле | Значение |
|------|----------|
| Тип | B (bug) — вложенные туннели (AWG/WG/MASQUE через `detour`) не ходят на устройстве; direct-ноды через тот же detour работают |
| Статус | C — фикс реализован, стенд зелёный (юнит DF-флага + e2e AWG-over-AWG); остался field-тест на устройстве |
| Владелец конфигурации | Поведение `udp_fragment` **по умолчанию** для `wireguard`-endpoint и `masque`-outbound (протокольный default, само поле — upstream). Взаимодействие «вложенный туннель ↔ detour ↔ MTU» — эта спека |

## 0. TL;DR

Нижнее (реальное) UDP-«плечо» любой detour-цепочки открывается через
`common/dialer` — а он **по умолчанию запрещает IP-фрагментацию**
([default.go:179-182](../../common/dialer/default.go): нет
`UDPFragmentDefault` → `control.DisableUDPFragment()`, т.е.
`IP_MTU_DISCOVER=IP_PMTUDISC_DO` на linux/android, `IP_DONTFRAG` на darwin).
Upstream протоколы, которые заведомо шлют крупные датаграммы
(direct/hysteria/hysteria2/tuic), от этого **отказываются**
(`options.UDPFragmentDefault = true`), а `wireguard`-endpoint и
`masque`-outbound — не отказывались.

Для плоского WG это незаметно: датаграмма ≤ MTU+60 обычно влезает в путь. Для
**вложенных туннелей** оверсайз внешней датаграммы — норма (см. математику §2):
инкапсуляция добавляет +60…+120 байт к уже полноразмерному внутреннему пакету.
С DF такая датаграмма **молча умирает** (локальный `EMSGSIZE: message too
long` либо DF-дроп на маршруте с зарезанным ICMP) — внутренний туннель
«не встаёт» или встаёт и не несёт данные.

**Фикс:** `wireguard`-endpoint и `masque`-outbound ставят
`UDPFragmentDefault = true` — внешний сокет остаётся способным фрагментировать
(поведение самого wireguard-go, который DF никогда не форсировал). Явный
`"udp_fragment": false` в конфиге по-прежнему возвращает DF. Остальные
протоколы не затронуты.

Результат: **masque/awg/wg смешиваются в detour в любых комбинациях** — любое
промежуточное плечо (gVisor-стек detour-эндпоинта) фрагментирует оверсайз само
(§4, доказано стендом), а нижнее плечо теперь фрагментирует на уровне ОС.

---

## 1. Симптом (устройство)

Конфиг: `awg2-home` (`type: wireguard` + AWG, `mtu: 1280`, `s4: 60`),
MASQUE-ноды `mtu 1280`, вложенные AWG-ноды.

| Конфигурация | Результат |
|---|---|
| MASQUE / AWG напрямую (без detour) | ✅ работает |
| MASQUE (h3/h2) через `detour: awg2-home` | ❌ не встаёт |
| AWG через `detour: awg2-home` (AWG-in-AWG) | ❌ не встаёт / не несёт данные |
| direct-нода через тот же detour | ✅ работает (мелкие датаграммы) |

Документированный локальный вариант того же класса (без detour): AWG с
завышенным `mtu` при `s4>0` — handshake проходит, данные нет:
`failed to send data packets: … sendmsg: message too long` (см.
[lx-config.md §MTU](../../docs-lx/lx-config.md)). Это тот же DF: с
`IP_PMTUDISC_DO` ядро отвергает датаграмму крупнее MTU вместо фрагментации.

## 2. Математика пакетов

Оверхед на инкапсуляцию: WG data = 16 (header) + 16 (poly1305) с паддингом до
16; AWG добавляет `s4` junk-байт на **каждый** data-пакет; IPv4/UDP = 28.

Вложение с одинаковым MTU (типовой пользовательский конфиг, 1280/1280):

```
внутренний IP-пакет 1280
→ AWG data: pad(1280)=1280 +32 +s4(60) = 1372  (UDP-payload в detour-стек)
→ 1372 > 1280−28=1252 → gVisor-стек detour ФРАГМЕНТИРУЕТ на 2 IP-пакета ≤1280
→ каждый фрагмент шифруется внешним туннелем: ≤1280+32(+s4_outer)+28 на провод
```

Нижнее плечо без вложения, но с s4: `1420+32+60+28 = 1540 > 1500` — DF-дроп
даже на чистом Ethernet. С `mtu 1280`: `1280+32+60+28 = 1400` — влезает в
1500, но **не влезает** в типичные мобильные/PPPoE/CGNAT-пути с path-MTU
меньше 1400. QUIC Initial внутреннего MASQUE (1242+28=1270 ≤ 1280) едет
целиком и даёт внешнюю датаграмму ровно 1400 → на «коротком» пути дропается
каждый handshake-пакет → `dial quic: timeout`.

## 3. Первопричина и фикс

Цепочка: `NewEndpoint` → `dialer.NewWithOptions(options.DialerOptions)` →
без `UDPFragmentDefault` в [default.go:173-182](../../common/dialer/default.go)
оба контрола (dialer **и** listener) получают `DisableUDPFragment()`. Флаг
доезжает до обоих реальных бинд-путей endpoint'а:

- **StdNetBind** (без detour): `UDPListenerControl()` отдаёт listener-контролы
  ([transport/wireguard/endpoint.go:276-286](../../transport/wireguard/endpoint.go));
- **ClientBind** (fallback-путь с реальным сокетом): `DialContext`/`ListenPacket`
  дайлера.

Фикс (по образцу hysteria2/tuic/direct):

| Файл | Что |
|---|---|
| [protocol/wireguard/endpoint.go](../../protocol/wireguard/endpoint.go) | `options.UDPFragmentDefault = true` в начале `NewEndpoint` + `UDPFragmentDefault: true` в per-interface `CreateDialer` (system-режим) |
| [protocol/masque/outbound.go](../../protocol/masque/outbound.go) | `options.UDPFragmentDefault = true` перед созданием дайлера (QUIC-паритет с hysteria2/tuic; ceiling пакета 1452 > коротких path-MTU) |

Семантика: **default, не принуждение** — явный `"udp_fragment": false|true`
пользователя всегда сильнее (`UDPFragment *bool` имеет приоритет).

Detour-плечи фикс не трогает: там нет реального сокета (gVisor `gonet`-коннекты,
socket-контролы не применяются), а фрагментацию оверсайза gVisor уже делает сам.

## 4. Что доказано стендами

### 4.1 Юнит: DF-флаг на реальном сокете

[common/dialer/udp_fragment_lx_test.go](../../common/dialer/udp_fragment_lx_test.go)
(+ пер-OS `getsockopt`-хелперы darwin/linux) — оба пути (`DialContext` и
`UDPListenerControl`+`ListenConfig`):

- дефолт без опций → DF **установлен** (пин upstream-поведения);
- `UDPFragmentDefault=true` (то, что теперь ставят endpoint/masque) → DF **снят**;
- явный `udp_fragment` бьёт протокольный default в обе стороны.

### 4.2 e2e: AWG внутри AWG через detour

[test/wireguard_detour_lx_test.go](../../test/wireguard_detour_lx_test.go) —
два in-process box-инстанса на loopback: socks → **awg-inner** (jc/s1/s2/s4=60/
h1..h4) → `detour` → **awg-outer** (свои jc/s1/s2/h1..h4) → реальный UDP-сокет →
сервер-box (оба endpoint'а в listen-режиме, hairpin-роутинг до эхо-сервера).
TCP+UDP ping-pong и large-data в обе стороны:

| Режим | outer/inner MTU | Путь внутренней датаграммы | Результат |
|---|---|---|---|
| fits | 1420/1280 | 1372 ≤ 1392 — целиком | ✅ PASS |
| fragments | 1280/1280 | 1372 > 1252 — IP-фрагментация на detour-стеке + сборка на той стороне, в обе стороны | ✅ PASS |

Требует `replace github.com/sagernet/wireguard-go => ../submodules/wireguard-go`
в `test/go.mod` (replace главного модуля не наследуется тест-модулем; без него
AWG-ключи падают с `invalid UAPI device key: jc`).

### 4.3 Наследованная loopback-матрица (расследование июль 2026)

Проверенные факты предыдущего расследования, на которые опирается эта спека:

- **gVisor-стек detour'а фрагментирует оверсайз-UDP, не дропает**: для локально
  сгенерированного UDP `ErrMessageTooLong` требует DF **и**
  `IsForwardedPacket`; локальный пакет без DF → `handleFragments`. Порог при
  MTU 1280: payload ≥ 1253.
- **MASQUE-over-WG-detour на loopback зелёный при всех MTU** {1420, 1280,
  1000}, включая фрагментацию Initial при mtu=1000 → in-process код
  MASQUE/detour исправен. Loopback **принципиально не воспроизводит** сам баг:
  MTU lo = 16384, нижнее плечо там никогда не оверсайз — поэтому отказ
  наблюдался только на реальном пути.
- Стендовая ловушка: inner-dst на loopback дропается gVisor'ом WG-сервера как
  martian — в стендах inner-адреса не-loopback.

## 5. Residual (наблюдать в field)

- **quic-go DPLPMTUD через detour**: форк объявляет `supportsDF=true` для conn
  без `SyscallConn` (detour отдаёт именно такой), MASQUE не задаёт
  `DisablePathMTUDiscovery` → после handshake зонды растут до 1452. Со снятым
  DF на нижнем плече оверсайз-зонд теперь фрагментируется и «проходит» —
  оценка MTU может завышаться, трафик едет фрагментированным (работает, но
  субоптимально). Если field покажет деградацию — отдельной задачей
  detour-aware `DisablePathMTUDiscovery` (проект был в бэкапе расследования).
- **Перф фрагментации**: 2 датаграммы на пакет на нижнем плече — цена
  работоспособности при неоптимальных MTU. Рекомендация пользователям прежняя:
  внутренний `mtu ≤ внешний − 60 − s4_внутр` убирает фрагментацию вовсе
  (см. [lx-config.md §MTU](../../docs-lx/lx-config.md)).

## 6. Field-план (CPH2411)

Debug API :9269 (`adb forward tcp:9269 tcp:9269`, Bearer-токен у пользователя).
До фикса: MASQUE/AWG через `detour: awg2-home` — `dial quic: timeout` /
туннель без данных; после: сессии встают и несут трафик. Локальный маркер
старого поведения в логе ядра — `sendmsg: message too long` (исчезает).
Проверить обе комбинации: MASQUE-over-AWG и AWG-over-AWG, плюс регресс
плоских нод через тот же detour.

## 7. Инварианты

- Явный `udp_fragment` из конфига всегда сильнее протокольного default.
- Прочие протоколы: дефолты не изменены (hy2/tuic/direct и так true, остальные
  false).
- Detour-плечи (gVisor) не затронуты; s4-junk не ослаблен; MTU-эвристики AWG
  (default 1280 при `s4>0`, startup-warning) не изменены.
- MASQUE direct: QUIC сам держит датаграммы ≤1452 — снятие DF меняет только
  судьбу пакетов на путях с MTU < ~1480 (раньше дроп, теперь фрагментация).

## 8. Ссылки

- [SPEC 007](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) — снятый guard AWG-over-WG (без него вложение вообще не стартовало)
- [SPEC 021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) — MASQUE-outbound (владелец masque-конфига)
- [SPEC 025](../025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md) / [SPEC 026](../026-AWG_MAGIC_VS_RESERVED_CLEAR/SPEC.md) — предыдущие слои AWG-datapath
- [docs-lx/lx-config.md §MTU](../../docs-lx/lx-config.md) — бюджет MTU и рекомендация 1280
