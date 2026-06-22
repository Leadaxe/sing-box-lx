# IMPLEMENTATION_REPORT — 003 AWG2_CLIENT_ENDPOINT

**Дата:** 2026-06-09 · **Статус:** Complete — **функционален и проверен живым AWG2-сервером** · **База:** `v1.13.13`

## Итог

Полноценный клиент **AmneziaWG 2.0**: конфиг валидируется, собирается `with_awg`, и **реально подключается** к серверу AmneziaWG 2.0 с обфускацией (junk + S1–S4 + H1–H4 + CPS I1–I5).

## Архитектура (две части)

**1. sing-box-lx (скаффолдинг + S3/S4):**
- `option/wireguard_awg.go` — `AmneziaWGOptions`: `Jc/Jmin/Jmax`, **`S1/S2/S3/S4`**, `H1–H4` (uint32), `I1–I5` (string, регистр сохраняется), встроены (promoted) в `WireGuardEndpointOptions`.
- `transport/wireguard/device_awg.go` (`//go:build with_awg`) — `awgIpcLines()` шлёт IpcSet-ключи `jc=/jmin=/jmax=/s1..s4=/h1..h4=/i1..i5=` в device; `device_stub_awg.go` без тега даёт явную ошибку при заданных AWG-полях.
- Проброс опций: `option.WireGuardEndpointOptions` → `protocol/wireguard/endpoint.go` → `transport/wireguard/endpoint.go` (всё `// lx`).

**2. amneziawg-go активирован через merged-форк (главное достижение):**
- amneziawg-go основан на *upstream* wireguard-go и не имеет sagernet-добавок (`Send(offset)`, `InputPacket`, `conn` reserved/control), на которых держится `transport/wireguard`. Прямой `replace` ломает сборку.
- Решение **(A)**: `Leadaxe/wireguard-go` = **git 3-way merge** (`merge-base 469159e`) обфускации `amnezia/master` (тип `f4f4c99`, AWG2 + S4-keepalive) поверх `sagernet/wireguard-go@506b7631853c`. Контракт sing-box сохранён (база — sagernet), обфускация добавлена.
- Ключевое упрощение: **`MessageEncapsulatingTransportSize = 0`** — нейтрализует 8-байтный headroom sagernet (sing-box-lx его не использует), и обфускация Amnezia встаёт чисто без weave-конфликтов в send-пути.
- `conn/tun/ipc` оставлены **чисто sagernet** (обфускация только в `device/`: новые `obf*.go`+`magic-header.go` + графты в `send/receive/device/uapi`).
- Подключение: git submodule `submodules/wireguard-go` (Leadaxe/wireguard-go @`27290b6`) + `// lx` `replace github.com/sagernet/wireguard-go => ./submodules/wireguard-go`. Воспроизводимо для CI.

## Проверки

- `make -f Makefile.lx lx-build` → ок; `check awg2_basic.json` → pass; сборка без `with_awg` → обычный WG; gofmt/синтаксис merge чисты.
- **Лайв-тест (реальный сервер AmneziaWG 2.0):**
  ```
  peer - sending handshake initiation
  peer - received handshake response      ← обфусцированный хендшейк прошёл
  peer - receiving keepalive packet       ← keepalive 25s
  curl --socks5 через туннель → <server IP> ← трафик идёт через сервер
  ```
  Параметры: Jc=10/Jmin=50/Jmax=100, S1=S2=20/S3=S4=60, H1–H4, I1–I3 (I1/I2 — мимикрия под STUN `0x2112a442`, I3 random).

## MTU при ненулевых S3/S4 (EMSGSIZE) — дополнение 2026-06-10

При лайв-тесте AWG2-узла рукопожатие проходило, но трафик не шёл: ядро спамило `failed to send data packets: … sendmsg: message too long` (**EMSGSIZE**). Причина — прямое следствие `s3`/`s4`: junk дописывается к **каждому transport-сообщению**, и обфусцированный data-пакет перерастает path MTU физического интерфейса (1500, DF). Handshake маленький — проходит; transport — нет. Plain WG к тому же серверу с `mtu 1420` работает (S-junk нет).

Бюджет: `mtu ≤ 1500 − 28 (UDP/IP) − 32 (WireGuard) − max(S3, S4)`. Для `S3=S4=60` → `mtu ≤ 1380`; рекомендуемый клиентский MTU AmneziaWG — **1280** (запас на PPPoE/вложенные туннели). Эмпирика (тот же узел/сервер, менялся только `mtu`):

| mtu | результат |
|----:|-----------|
| 1420 | ❌ EMSGSIZE, данные не уходят |
| 1380 | ✅ ~58 ms, 0 ошибок |
| 1280 | ✅ ~55 ms |
| 1200 | ✅ ~60 ms |

Сделано:
- `docs/lx-config.md` §2 — подраздел **MTU** (механика, формула, симптом, рекомендация 1280, держать `jmax` ниже path MTU) + пример понижен `1420 → 1280`.
- `transport/wireguard/endpoint.go` (`// lx`, gated `max(s3,s4) > 0` — plain WG нетронут):
  - **auto-default**: при незаданном `mtu` на AWG-эндпоинте ставим рекомендованный **1280** вместо upstream-дефолта `1408` (который сам бы превышал бюджет и триггерил наш же warn).
  - **warn**: при явно заданном `mtu` выше бюджета — предупреждение (handshake пройдёт, данные — нет). Path MTU зашит консервативно **1492** (PPPoE): `mtu ≤ 1492 − 28 − 32 − max(s3,s4)` → для `s3=s4=60` это `1372`. Эмпирический потолок выше (1380), т.к. тест шёл по реальному 1500-Ethernet; 1492 — запас под узкие пути.
  - Проверено (`check`): AWG `s3=s4=60` без `mtu` → тихо (default 1280); `mtu=1420` → `WARN … consider mtu <= 1372`; plain WG без `mtu` → тихо (1408).

Подтверждение (amneziawg-go docs): рекомендуемый клиентский MTU 1280; если `Jmax` ≥ системного MTU — junk-пакет фрагментируется и теряется на узких путях. Это не баг ядра, а размерный оверхед S-junk. Источник находки — заметка агента лаунчера (`singbox-launcher`, 2026-06-10).

## Безопасность

Секреты сервера **никогда** не попадали в репозитории — лайв-конфиг держался только в `/tmp` и затёрт (`shred`). Репо `lx-test/config/awg2_basic.json` — с фейк-ключами.

## Зона касания upstream (ребейз)

sing-box-lx: `go.mod` (replace), `option/wireguard*`, `protocol/wireguard/endpoint.go`, `transport/wireguard/*` — всё `// lx`. Форк wireguard-go ребейзится отдельно на новый тег sagernet (повтор 3-way merge амнезии).

## Остаточное / дальше

- reserved-feature не применяет reserved-байты в obfuscated send (для plain-AWG не нужно — карта пуста).
- Можно перевести `replace` с submodule на pinned-pseudoversion (submodule достаточно).
- Лаунчер: AWG-поля (S1–S4, I1–I5) в визард + парсер `.conf`/awg-quick; рассматривает кламп MTU для AWG-узлов (первичная истина про оверхед `s3`/`s4` — здесь, см. раздел MTU).
