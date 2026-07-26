# SPEC 003 — AmneziaWG 2.0 клиентский endpoint

**Фича:** [AWG2](../../FEATURES/003-AWG2/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) — функционален, проверен живым AWG2-сервером |
| Тег сборки | `with_awg` (обфускация) поверх `with_gvisor` (стек) |

Клиентский **AmneziaWG 2.0** endpoint: обычный WireGuard-endpoint sing-box плюс обфускация DPI (junk-пакеты, магические заголовки, размерный padding, CPS-пакеты `I1–I5`). Без тега `with_awg` — обычный WireGuard upstream; AWG-поля в конфиге дают явную ошибку «не собрано».

---

## Что это

AmneziaWG обходит DPI, маскируя WireGuard-трафик:
- **Junk** (`Jc`/`Jmin`/`Jmax`) — `Jc` случайных пакетов размером `rand(Jmin..Jmax)` перед handshake initiation.
- **Магические заголовки** (`H1–H4`) — подменяют 4-байтный тип сообщения (init/response/cookie/transport); в AWG 2.0 — диапазоны `"N-M"`, из которых значение генерируется на лету.
- **Размерный padding** (`S1/S2` — на handshake, `S3/S4` — на **каждый** transport-пакет).
- **CPS-пакеты** (`I1–I5`) — снимки реального протокола (напр. QUIC Initial, STUN), которые уходят вперемешку с handshake, имитируя посторонний трафик. `I1` — центральный (см. [SPEC 009](../009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) — декларативные masquerade-профили `ip=quic/sip/dns`, которые генерируют `I1`).

Upstream sing-box AWG не принимает ([#4045](https://github.com/SagerNet/sing-box/issues/4045), closed not-planned) — реализовано в форке.

## Архитектура (два слоя)

Обфускация живёт **в вендоренном wireguard-go** (submodule), а sing-box только пробрасывает параметры. Это ключевое разделение: контракт `transport/wireguard` ↔ device остаётся sagernet'овским, обфускация — аддитивна.

### Слой 1 — вендоренный wireguard-go (`submodules/wireguard-go`)

Форк `Leadaxe/wireguard-go-awg2-lx` = **3-way graft** обфускации AmneziaWG 2.0 поверх `sagernet/wireguard-go`. Подключён как git submodule + `// lx` `replace github.com/sagernet/wireguard-go => ./submodules/wireguard-go` в `go.mod`; pin на конкретный graft-коммит.

**Что граф добавляет** (16 файлов в `device/`):
- **10 net-new**: `magic-header.go` (генератор `H1–H4` из спеки `"N"`/`"N-M"`) + `obf*.go` (CPS-цепочки `I1–I5`, junk-байты, timestamp/datasize кодеки).
- **6 modified**: `device.go` (AWG-state: `junk`, `headers`, `paddings`, `ipackets [5]*obfChain`), `send.go` (junk + CPS + padding в handshake/transport-путях), `receive.go` (детект magic-header на входе), `cookie.go`/`noise-protocol.go`/`uapi.go` (типы сообщений через генератор, парсинг AWG-ключей в IpcSet).

**Ключевой инвариант — `MessageEncapsulatingTransportSize = 0`** ([device/noise-protocol.go](../../../submodules/wireguard-go/device/noise-protocol.go)). Upstream держит 8-байтный headroom перед transport-заголовком (для `conn.Bind.Send()`-префикса). Граф его **обнуляет**: AWG-обфускация формирует префикс сама (junk/CPS уходят отдельными буферами через `SendBuffers`, а не через encapsulating-space). При `= 0` upstream-выражения вида `buffer[MessageEncapsulatingTransportSize+MessageTransportHeaderSize:]` схлопываются к графовому виду `buffer[MessageTransportHeaderSize:]` — поэтому большинство upstream-функций компонуются с графом **без ручного weave**. Это несущий инвариант re-graft (§ ниже).

**Что граф НЕ трогает:** `conn/`, `tun/` — чисто sagernet (берутся из upstream verbatim). Обфускация замкнута в `device/`.

### Слой 2 — sing-box (проброс параметров, всё `// lx`)

- **`option/wireguard_awg.go`** — `AmneziaWGOptions`: `Jc/Jmin/Jmax`, `S1–S4`, `H1–H4` (тип `MagicHeader` — строка `"N"` или диапазон `"N-M"`, JSON-совместим с прежним uint32), `I1–I5` (string, регистр сохраняется). Promoted-встроены в `WireGuardEndpointOptions`.
- **`transport/wireguard/device_awg.go`** (`//go:build with_awg`) — `awgIpcLines()` рендерит IpcSet-ключи `jc=/jmin=/jmax=/s1..s4=/h1..h4=/i1..i5=`, дописываемые к WireGuard-конфигу устройства. `device_stub_awg.go` (`//go:build !with_awg`) даёт явную ошибку при заданных AWG-полях.
- **`transport/wireguard/endpoint.go`** — MTU-политика для AWG (см. ниже).
- **`validateJunk`** — отвергает `jmin > jmax` до старта: `amneziawg-go` считает `rand(0..jmax-jmin)+jmin`, и `jmax < jmin` даёт `rand.Int` с аргументом `≤ 0` → **паника ядра**. Гардим только этот crash-кейс.

Регистрация endpoint остаётся `C.TypeWireGuard` (AWG = WG + доп. поля, отдельный тип не вводим).

## MTU-политика (следствие S3/S4)

`S3`/`S4` дописывают junk к **каждому** transport-сообщению → обфусцированный data-пакет перерастает path MTU физического интерфейса (1500, DF) → ядро спамит `sendmsg: message too long` (**EMSGSIZE**), handshake проходит, а трафик — нет.

Бюджет: `mtu ≤ pathMTU − 28 (UDP/IP) − 32 (WireGuard) − max(S3, S4)`.

Логика в [transport/wireguard/endpoint.go](../../../transport/wireguard/endpoint.go) (gated `max(s3,s4) > 0`, plain WG нетронут):
- **auto-default**: при незаданном `mtu` на AWG-эндпоинте — рекомендованный **1280** вместо upstream-дефолта 1408.
- **warn**: при явном `mtu` выше бюджета — предупреждение (`pathMTU = 1492`, консервативно под PPPoE). Для `s3=s4=60` → `mtu ≤ 1372`.

Держать `Jmax` ниже системного MTU (иначе junk-пакет фрагментируется и теряется на узких путях). Подробности: `docs-lx/lx-config.md` §2 (MTU).

## Процедура re-graft (при бампе upstream wireguard-go)

Когда upstream `sagernet/wireguard-go` двигает версию, граф переносится на новую базу. **Не merge, а controlled 3-way apply** граф-diff'а:

1. **База**: submodule → новый sagernet-коммит.
2. **Apply graft**: `git diff <старая-база> <старый-graft> | git apply --3way`. По практике 15/16 файлов ложатся чисто; конфликтует обычно только `send.go` (плотный upstream-путь).
3. **Разрешить конфликты вручную**, порядок по риску: `cookie`→`device`→`noise-protocol`→`uapi`→`receive`→**`send.go`** (высший — junk/padding-хуки в hot-path).
4. **Сверить несущие инварианты**: `MessageEncapsulatingTransportSize = 0`; графовый `RoutineEncryption` (заголовок в начале буфера, без финального encapsulating re-slice); AWG-state поля в `device.go`.
5. **Проверки**: сборка `device/conn/tun` на linux/android/windows/**darwin** (darwin особо — там upstream добавляет платформенный batch-send), затем полный `sing-box` с LX_TAGS, `go test ./transport/wireguard/ ./protocol/wireguard/`, **device-verify** живого AWG-туннеля (junk/handshake/трафик).

История конкретных re-graft'ов (какие базы, что менял upstream) — в [HISTORY.md](HISTORY.md).

## Критерии готовности

- `sing-box check -c` принимает wireguard-endpoint c `jc/h1/i1…` под `with_awg`.
- Реальный коннект к AmneziaWG 2.0 (device-verify): `sending handshake initiation` → `received handshake response` → keepalive → трафик через сервер, с непустыми `Jc` и хотя бы одним `I1`.
- Сборка **без** `with_awg`: обычный WG как upstream; AWG-поля → явная ошибка.
- `gofmt -l` чист на граф-файлах; `go vet`, тесты `transport/wireguard` + `protocol/wireguard` — зелёные.

## Изоляция и merge-зона

- **sing-box-lx**: `go.mod` (replace + pin), `option/wireguard_awg.go` + `// lx`-поля в основной struct, `transport/wireguard/device_awg*.go`, MTU-блок в `transport/wireguard/endpoint.go`, проброс в `protocol/wireguard/endpoint.go` — всё `// lx`.
- **submodule wireguard-go**: ребейзится отдельно (см. процедуру re-graft), не входит в merge-зону основного репо кроме pin в `go.mod`.

## Смежные фичи

- [SPEC 009](../009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) — декларативные masquerade-профили (`ip=quic/sip/dns`), генерирующие `I1`/`I2`.
- [SPEC 020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) — idle-suspend WG/AWG-устройств (Down/Up); опирается на стабильный device-API той же вендоренной базы.
- [SPEC 025](../025-AWG_TRANSPORT_PADDING_OVERRUN/SPEC.md) — класс рантайм-крашей графта (transport-padding overrun + rx-дубль + config-value guards); device-verified фиксы в `submodules/wireguard-go`.

## Вне скоупа

- AWG inbound/server — форк client-focused.
- Парсинг `awg-quick`/`.conf` — забота лаунчера/UI.
- AmneziaWG 1.x как отдельный режим (2.0 обратно совместима по базовым полям).

## Ссылки

- [AmneziaWG 2.0 — Amnezia Docs](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [hoaxisr/amnezia-box (референс интеграции)](https://github.com/hoaxisr/amnezia-box)
