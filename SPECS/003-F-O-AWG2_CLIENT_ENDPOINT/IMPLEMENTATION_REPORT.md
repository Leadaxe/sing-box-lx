# IMPLEMENTATION_REPORT — 003 AWG2_CLIENT_ENDPOINT

**Дата:** 2026-06-09 · **Статус:** O (скаффолдинг влит, собирается/check; **зависимость amneziawg-go НЕ активна**) · **База:** `v1.13.13`

## Что сделано

Полный скаффолдинг AmneziaWG 2.0 endpoint (workflow, изолированный worktree, влит в `lx` коммитом `0e8894d2`). AWG смоделирован как **обычный WireGuard + device-global IpcSet-строки** — тип endpoint остаётся `C.TypeWireGuard` (минимальный дифф, без нового типа).

**Файлы:**
- `option/wireguard_awg.go` (new) — `AmneziaWGOptions`: `Jc,Jmin,Jmax,S1,S2,H1..H4` (uint32), `I1..I5` (string, регистр сохраняется).
- `option/wireguard.go` (// lx) — `AmneziaWGOptions` встроены (promoted) в `WireGuardEndpointOptions`.
- `protocol/wireguard/endpoint.go`, `transport/wireguard/endpoint.go`, `transport/wireguard/endpoint_options.go` (// lx) — проброс опций → device.
- `transport/wireguard/device_awg.go` (`//go:build with_awg`) — `awgIpcLines()` форматирует ключи; `device_stub_awg.go` (`//go:build !with_awg`) — явная ошибка «awg support not built» при заданных AWG-полях (без тихой деградации).
- `go.mod` (// lx) — replace-блок на amneziawg-go **закомментирован осознанно** (см. ниже).
- `lx-test/config/awg2_basic.json` (new) — для `check`.

**Формат device-строки** (по одному ключу на строку, `\n`-префикс; числовые — если ≠0, `i`-ключи — если непусты, verbatim): `jc=/jmin=/jmax=/s1=/s2=/h1..h4=/i1..i5=`, добавляется после `private_key`/`listen_port`, до первого peer.

## Проверки (DoD = compiles + check)

- `make -f Makefile.lx lx-build` → ок; `check -c awg2_basic.json` → **pass**; `go vet` (lx-теги) чисто; `go build ./...` без `with_awg` → ок.
- Негатив: без `with_awg` AWG-конфиг → FATAL «rebuild with -tags with_awg»; plain-WG принимается обоими.

## ⚠️ Главный пробел: зависимость не активна

`replace github.com/sagernet/wireguard-go => amneziawg-go` **закомментирован**, т.к. подтверждён **API-дрейф**: amneziawg-go — форк *upstream* wireguard-go и не содержит sagernet-добавок, на которые опирается sing-box:
- `(*device.Device).InputPacket(dst, packetSlices)` — используется `transport/wireguard/device_system_stack.go` (gVisor system-stack);
- `conn.Bind.SetReservedForEndpoint(...)` и `conn.NewStdNetBind(control.Func)` — sagernet-специфика, на которой держится `transport/wireguard/client_bind.go`.

Активация replace «как есть» ломает сборку **всего** модуля. Поэтому при `with_awg` бинарь сейчас собирается и `check` проходит (валидация конфига), но при **реальном коннекте** IpcSet-ключи `jc=/h1=/i1=` отвергнет `sagernet/wireguard-go` — AWG обфускация **не активна**.

## План активации (требует сети — отдельный под-проект)

Референс — `hoaxisr/amnezia-box` (дуальная схема: оба модуля в require + tag-gated импорт amneziawg-go + `patches/amneziawg-go/apply.sh`). Шаги:
1. Вендорить amneziawg-go (pin коммит с AWG2/I1-I5; hoaxisr пинит `v0.2.17-0.20251219…449d7cffd4ad`) под `submodules/amneziawg-go`.
2. Наложить sagernet-compat патчи (`InputPacket` в `device/send.go`; `conn.Bind`/`NewStdNetBind` в `conn/`) + AWG-feature патчи hoaxisr (`0001-add-counter-obf-tag`, `0002-fix-s4-keepalive-padding`).
3. Раскомментировать `replace … => ./submodules/amneziawg-go`, `go mod tidy`.
4. Пересборка `with_awg` + лайв-тест против сервера AmneziaWG 2.0.

> Альтернатива чище: собственный форк `Leadaxe/amneziawg-go` с применёнными патчами и `replace => github.com/Leadaxe/amneziawg-go <commit>` (без submodule-pain в worktree/CI).

## Дальше

Активация зависимости (под-проект выше) → затем лайв-тест → `C`. До активации AWG-фича — «валидируется, но не подключает обфускацию».
