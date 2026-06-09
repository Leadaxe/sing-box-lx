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
| **002** | XHTTP_CLIENT_TRANSPORT | N | Registry-рефактор диспетчера v2ray + пакет `transport/v2rayxhttp` (client) за `with_xhttp` |
| **003** | AWG2_CLIENT_ENDPOINT | N | amneziawg-go (submodule+patches) + расширение wireguard-endpoint за `with_awg` |
| **004** | BUILD_CI_RELEASE | N | Списки build-тегов, CI-матрица платформ, авто-ребейз на upstream-тег, релизные артефакты |

> **Вне этого репозитория:** потребление ядра лаунчером (`singbox-launcher`) — парсинг `type=xhttp` в реальный XHTTP-транспорт (сейчас `023` маппит его в `httpupgrade`), AWG-поля в визарде, замена `bin/sing-box`. Это отдельные задачи в репозитории лаунчера.
