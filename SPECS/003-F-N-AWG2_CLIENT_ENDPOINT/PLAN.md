# PLAN: 003 — AWG2_CLIENT_ENDPOINT

## 1. Архитектура

AmneziaWG = WireGuard-девайс с расширенным конфигом. В sing-box девайс создаётся в `transport/wireguard` поверх `github.com/sagernet/wireguard-go`. Стратегия: **подменить модуль на `amneziawg-go`** (API-совместим с wireguard-go) и **под `with_awg`** прокидывать AWG-поля в строку конфигурации девайса; endpoint остаётся типом `wireguard`.

## 2. Зависимость

- Submodule: `submodules/amneziawg-go` → `amnezia-vpn/amneziawg-go` (pin commit).
- `patches/` — для локальных фиксов поверх amneziawg-go (применяются скриптом сборки; пусто, если не нужны).
- `go.mod` `// lx:` replace `github.com/sagernet/wireguard-go => ./submodules/amneziawg-go`.

> Проверить: совпадает ли публичный API amneziawg-go (пакеты `device`, `conn`, `tun`) с тем, что импортирует `transport/wireguard`. Если расходится — минимальные `patches/` или адаптерный слой в новом файле.

## 3. Изменяемые / новые файлы

| Файл | Тип | Изменения |
|------|-----|-----------|
| `.gitmodules`, `submodules/amneziawg-go` | **new** | Submodule, pinned commit |
| `patches/*.patch` | **new** | (опц.) патчи поверх amneziawg-go |
| `go.mod` / `go.sum` | `// lx:` | `replace` wireguard-go → submodule |
| `option/wireguard_awg.go` | **new** | Под-структура/поля `Jc,Jmin,Jmax,S1,S2,H1..H4,I1..I5` + парс/валидация |
| `option/…wireguard endpoint options` | `// lx:` | встроить AWG-поля в `WireGuardEndpointOptions` (минимум строк) |
| `transport/wireguard/device_awg.go` | **new** (`//go:build with_awg`) | Формирование AWG-строки конфига девайса |
| `transport/wireguard/device_stub_awg.go` | **new** (`//go:build !with_awg`) | Ошибка «awg not built», если AWG-поля заданы |
| `protocol/wireguard/endpoint.go` | `// lx:` | Прокинуть AWG-опции в создание девайса (1 ветка под флагом) |
| `include/awg.go` / правка `include/wireguard.go` | new/`// lx:` | Проводка под тегом (если нужно) |
| `test/config/awg2_*.json` | **new** | Конфиги для `sing-box check` |

## 4. Зона касания upstream (для ребейза)

`go.mod`/`go.sum`, файл опций wireguard-endpoint, `protocol/wireguard/endpoint.go`, `transport/wireguard/*` (минимально). Девайс-логика и опции AWG — в **новых** файлах под тегом → основной конфликт только в `go.mod` и одной ветке endpoint.

## 5. Порядок работ

1. Submodule + `go.mod` replace; собрать обычный WG (без `with_awg`) — поведение upstream.
2. Сверить API amneziawg-go vs `transport/wireguard`; при необходимости `patches/`.
3. Опции AWG (`wireguard_awg.go` + `// lx:` поля).
4. `device_awg.go` (формат `jc=/h1=/i1=…`) под `with_awg`; stub без тега.
5. Прокидка в endpoint; конфиги; `check`; ручной коннект к AWG2-серверу.

## 6. Риски

- **API-дрейф** amneziawg-go относительно версии wireguard-go, на которую завязан upstream (`v0.0.2-beta.1.0.20260224…`). Возможен лаг — фиксировать совместимый коммит сабмодуля, не «latest».
- **Регистр I1–I5** (uppercase) — silent ignore при ошибке; валидировать.
- Взаимодействие junk/CPS с `persistent_keepalive` и MTU — проверять на реальном сервере.
- Доменный `server` + FakeIP: может потребоваться override резолва (референс hoaxisr) — добавлять только при подтверждённой необходимости.
