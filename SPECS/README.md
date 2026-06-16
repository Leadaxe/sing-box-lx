# SPECS — sing-box-lx (Spec Kit)

Все задачи — папки `NNN-T-S-NAME`. Внутри: SPEC.md → PLAN.md → TASKS.md → IMPLEMENTATION_REPORT.md.

## Имя папки: `NNN-T-S-NAME`

| Часть | Значение | Расшифровка |
|-------|----------|-------------|
| **NNN** | 001, 002, … | Сквозной номер |
| **T** (тип) | F / B / Q | Feature / Bug / Question (исследование) |
| **S** (статус) | N / O / W / C | New / Open (в работе) / Wait / Complete |
| **NAME** | UPPER_SNAKE | Название |

## Файлы внутри папки

| Файл | Назначение |
|------|------------|
| **SPEC.md** | Что и зачем — проблема, требования, критерии приёмки |
| **PLAN.md** | Как строить — архитектура, изменяемые файлы, зона касания upstream |
| **TASKS.md** | Чеклист по этапам |
| **IMPLEMENTATION_REPORT.md** | Отчёт после реализации |

## Конфигурация фич

Пользовательский конфиг XHTTP и AmneziaWG 2.0 (поля + примеры) — **[../docs/lx-config.md](../docs/lx-config.md)**.

## Корень SPECS

| Файл | Назначение |
|------|------------|
| **CONSTITUTION.md** | Неизменяемые принципы, приоритеты, запреты |
| **IMPLEMENTATION_PROMPT.md** | DoD, git/ребейз-ритуал, контракт выхода |

## Workflow

1. Папка `SPECS/NNN-T-S-NAME/` (следующий номер, статус `N`).
2. SPEC.md → PLAN.md → TASKS.md.
3. Реализация по TASKS с учётом IMPLEMENTATION_PROMPT и CONSTITUTION.
4. IMPLEMENTATION_REPORT.md, DoD-чеклист, переименование папки в `…-C-…`.

## Roadmap (план задач)

| # | Задача | Статус | Суть |
|---|--------|--------|------|
| **001** | FORK_BOOTSTRAP | **C** | Remotes, ветка `lx`, `Makefile.lx`, версия `-lx` (ldflags), CI-скелет, `lx-test/config` — ✅ собрано/проверено |
| **002** | XHTTP_CLIENT_TRANSPORT | **C** | ✅ **live-validated** против Xray (3x-ui): packet-up/auto работают (handshake+DNS+HTTPS+download); stream-one — известный баг (REPORT) |
| **003** | AWG2_CLIENT_ENDPOINT | **C** | ✅ **Функционален, проверен живым AWG2-сервером** (handshake+keepalive+трафик). merged-форк Leadaxe/wireguard-go (sagernet+обфускация) через submodule; S1–S4/H1–H4/I1–I5 |
| **004** | BUILD_CI_RELEASE | **C** | ✅ `Makefile.lx`/libbox-теги, дешёвый CI (lint+build-check на push; cross×6+AAR на dispatch), `lx-release.yml` (**релиз v1.13.13-lx.3 опубликован** — 6 desktop + 2 AAR), `lx-rebase.yml` (авто-ребейз → PR/issue, демо зелёное) |
| **005** | AWG2_RANGED_MAGIC_HEADERS | **C** | ✅ **Проверено живым awg2-сервером с ranged-конфигом** (handshake+трафик). Диапазонные `H1`–`H4` (`"N-M"`) из awg2-экспортов: `option.MagicHeader` (number\|string) → spec-строка в IpcSet; vendored wireguard-go уже умел |
| **006** | LINUX_MUSL_STATIC_ROUTER_BUILDS | **C** | ✅ **CI-приёмка 4/4 арки статикой** (amd64/arm64/armv7/mipsle-softfloat, `statically linked`, libdl=0, naive сохранён). musl-сборки под роутеры по подобию upstream build.yml (cronet-go + Chromium musl-toolchain, `with_musl`). Чинит [#1](https://github.com/Leadaxe/sing-box-lx/issues/1) (`libdl.so.2` на AsusWRT + armv7). CI-only, без Go-кода |
| **007** | AWG_OVER_WIREGUARD_DETOUR_GUARD | **C** | ✅ **Код+тесты+DoD, Start-guard field-verified (Android lx.9).** Bug в 003: AWG-нода с `detour` на WireGuard-туннель (плоский WG или AWG) вешает ядро на Android (AWG внутри WG). **Два дополняющих guard'а:** Start-guard (`Endpoint.Start`, статическая транзитивная detour-цепь — device не поднимается) + selector-guard (`SelectOutbound` — при переключении селектора на WG гасит AWG-потребителей **до** переключения, через `ConsumersOf`). Оба — **вариант B** (ядро живёт, узел не встаёт, ошибка в лог). Ленивый dialer-guard (lx.8) откачен. detour на VLESS и WG→AWG — разрешены. Чинит [#2](https://github.com/Leadaxe/sing-box-lx/issues/2) |
| **008** | AWG_JUNK_PARAM_VALIDATION | **C** | ✅ **Код+тесты+DoD**. Bug в 003 (найден при 007): `jmin > jmax` паникует `rand.Int` в amneziawg-go (краш в timer-горутине). `validateJunk` в `awgIpcLines` отвергает на уровне конфига (`check`/старт), без паники. Узко — только краш-кейс; jc-несогласованность осознанно не трогаем (минимальный дифф, совместимость). Чинит [#3](https://github.com/Leadaxe/sing-box-lx/issues/3) |
| **009** | WIRESOCK_MASQUERADE_PROFILES | **C** | ✅ **Код+тесты+DoD; механизм проверен вживую (туннель + трафик на 009), релиз `v1.13.13-lx.11`.** WireSock-стиль `id`/`ip`/`ib` (домен/протокол/браузер) — декларативный сахар над `I1` CPS. Профили **quic** (1-RTT short header) / **dns** (EDNS OPT response) / **stun** (Binding Success Response) / **sip** (200 OK response), структуры портированы из open-source WireSock `amneziawg-proxy/src/transform.rs` (MIT). Механизм — **I1 only** (S1–S4 невозможен против WARP, сабмодуль не трогаем). `id` обязателен только для dns/sip (там идёт на провод), для quic/stun опционален. Строгая LDH-валидация домена (security-граница: инъекция в SIP/DNS). `ib` — без JA3-fingerprint (честно задокументировано). Все профили приняты реальным `newObfChain`; `sing-box check` зелёный; адверсариальный ревью (6 агентов) — 0 находок |

> **Вне этого репозитория:** потребление ядра лаунчером (`singbox-launcher`) — парсинг `type=xhttp` в реальный XHTTP-транспорт (сейчас `023` маппит его в `httpupgrade`), AWG-поля в визарде, замена `bin/sing-box`. Это отдельные задачи в репозитории лаунчера.
