# SPEC: 003 — AWG2_CLIENT_ENDPOINT

Добавить **клиентский AmneziaWG 2.0 endpoint** поверх существующего WireGuard-endpoint sing-box, используя `amneziawg-go` (как submodule + patches), за build-тегом `with_awg`.

---

## 1. Проблема / контекст

- AmneziaWG обходит DPI обфускацией: junk-пакеты (`Jc`/`Jmin`/`Jmax`), магические значения размеров/типов (`S1`/`S2`, `H1`–`H4`), а в **2.0** — CPS-пакеты `I1`–`I5` (первый — снимок реального протокола, напр. QUIC Initial) и диапазонные заголовки. См. [AmneziaWG 2.0](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/).
- Upstream sing-box AmneziaWG не принимает ([#4045](https://github.com/SagerNet/sing-box/issues/4045), closed not-planned).
- В sing-box WireGuard — это **endpoint** (`protocol/wireguard/endpoint.go`, девайс через `transport/wireguard`, зависимость `github.com/sagernet/wireguard-go`).

## 2. Цель

WireGuard-endpoint с заданными AWG-параметрами (`jc`, `jmin`, `jmax`, `s1`, `s2`, `h1`–`h4`, `i1`–`i5`) поднимает рабочее соединение к серверу **AmneziaWG 2.0**. Без тега `with_awg` бинарь = обычный WireGuard upstream.

## 3. Требования

### 3.1 Зависимость amneziawg-go (касание `go.mod`)
- Подключить [`amnezia-vpn/amneziawg-go`](https://github.com/amnezia-vpn/amneziawg-go) как **git submodule** (`submodules/amneziawg-go`) + каталог `patches/` (паттерн [`hoaxisr/amnezia-box`](https://github.com/hoaxisr/amnezia-box)).
- `go.mod`: `// lx:` `replace github.com/sagernet/wireguard-go => ./submodules/amneziawg-go` (или pin на форк-модуль). Зафиксировать **конкретный коммит** сабмодуля.
- Замена активна всегда на уровне модуля, но **AWG-поведение** включается только кодом под `with_awg` (см. 3.3); без тега девайс конфигурируется как обычный WG.

### 3.2 Опции (касание option)
- Расширить `option.WireGuardEndpointOptions` полями AWG: `Jc, Jmin, Jmax, S1, S2, H1, H2, H3, H4` (числа) и `I1, I2, I3, I4, I5` (строки, **регистр важен** — uppercase в .conf, во внутренней модели — как есть).
- Поля — в новом файле `option/wireguard_awg.go` со встраиванием/хелпером; в основной struct — минимальные `// lx:` строки (или встроенная под-структура `AmneziaWG`).
- Без `with_awg`: поля либо игнорируются, либо дают понятную ошибку «awg support not built» (выбрать и задокументировать; предпочтительно — явная ошибка, чтобы не было тихой деградации обфускации).

### 3.3 Девайс (касание transport/wireguard + protocol/wireguard)
- При `with_awg` конфигурация девайса передаёт AWG-параметры в `amneziawg-go` (формат строки конфига: `jc=`, `jmin=`, `jmax=`, `s1=`, `s2=`, `h1=`…`h4=`, `i1=`…`i5=`).
- Регистрация endpoint остаётся `C.TypeWireGuard` (AWG — это WG + доп. поля, отдельный тип не вводим — минимальный дифф). Проводка — вариант `include/wireguard.go` под тегом или новый `include/awg.go`.
- Сохранить фиксы резолва (DialContext/ListenPacket для доменов/FakeIP), если они нужны (референс hoaxisr) — но **только** если без них AWG-endpoint не работает с доменными `server`.

## 4. Критерии приёмки

- `sing-box check -c` принимает wireguard-endpoint c полями `jc/h1/i1…` под `with_awg`.
- Реальный коннект к серверу AmneziaWG 2.0 (ручная проверка), с непустыми `Jc` и хотя бы одним `I1`.
- Сборка **без** `with_awg`: обычный WG работает как upstream; AWG-поля → явная ошибка «не собрано».
- `go vet ./...`, тесты затронутых пакетов — зелёные.
- Ребейз-проверка: конфликты возможны только в `go.mod`/`go.sum`, `option/*wireguard*`, `protocol/wireguard/endpoint.go`, `transport/wireguard/*`.

## 5. Вне скоупа

- **AWG inbound/server** (отдельная задача).
- Парсинг `awg-quick`/`.conf` — это забота лаунчера/UI.
- AmneziaWG 1.x как отдельный режим — 2.0 обратно совместима по базовым полям; спец-режим 1.x не вводим.

## 6. Ссылки

- [AmneziaWG 2.0 — Amnezia Docs](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [hoaxisr/amnezia-box (референс интеграции)](https://github.com/hoaxisr/amnezia-box)
