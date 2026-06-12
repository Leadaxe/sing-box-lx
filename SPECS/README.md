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

> **Вне этого репозитория:** потребление ядра лаунчером (`singbox-launcher`) — парсинг `type=xhttp` в реальный XHTTP-транспорт (сейчас `023` маппит его в `httpupgrade`), AWG-поля в визарде, замена `bin/sing-box`. Это отдельные задачи в репозитории лаунчера.
