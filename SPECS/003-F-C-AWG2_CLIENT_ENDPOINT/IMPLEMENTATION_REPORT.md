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

## Безопасность

Секреты сервера **никогда** не попадали в репозитории — лайв-конфиг держался только в `/tmp` и затёрт (`shred`). Репо `lx-test/config/awg2_basic.json` — с фейк-ключами.

## Зона касания upstream (ребейз)

sing-box-lx: `go.mod` (replace), `option/wireguard*`, `protocol/wireguard/endpoint.go`, `transport/wireguard/*` — всё `// lx`. Форк wireguard-go ребейзится отдельно на новый тег sagernet (повтор 3-way merge амнезии).

## Остаточное / дальше

- reserved-feature не применяет reserved-байты в obfuscated send (для plain-AWG не нужно — карта пуста).
- Можно перевести `replace` с submodule на pinned-pseudoversion (submodule достаточно).
- Лаунчер: AWG-поля (S1–S4, I1–I5) в визард + парсер `.conf`/awg-quick.
