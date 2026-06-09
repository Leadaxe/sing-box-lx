# Руководство для AI-агентов — sing-box-lx

`sing-box-lx` — **тонкий downstream** апстрима [SagerNet/sing-box](https://github.com/SagerNet/sing-box): upstream **плюс ровно две фичи** и ничего больше:

1. **XHTTP** — клиентский v2ray-транспорт (совместимость с Xray XHTTP).
2. **AmneziaWG 2.0 (AWG2)** — клиентский endpoint поверх WireGuard.

Главная ценность проекта — **согласованность с upstream**. Любое изменение оценивается по тому, насколько легко оно переживёт ребейз на следующий тег upstream.

---

## Что читать в первую очередь

| Документ | Назначение |
|----------|------------|
| **SPECS/CONSTITUTION.md** | Неизменяемые принципы: приоритеты, build-tag изоляция, минимальный дифф, запреты |
| **SPECS/IMPLEMENTATION_PROMPT.md** | DoD, git/ребейз-ритуал, команды сборки и тестов, контракт выхода |
| **SPECS/README.md** | Формат задач `NNN-T-S-NAME` (Spec Kit), workflow |

Перед реализацией задачи из `SPECS/` обязательно прочитай её **SPEC.md → PLAN.md → TASKS.md** и применяй **IMPLEMENTATION_PROMPT.md**.

---

## Жёсткие границы (детали — в CONSTITUTION)

- **Только две фичи.** Любая фича вне XHTTP/AWG2 — вне скоупа, спросить пользователя.
- **Go module path остаётся `github.com/sagernet/sing-box`** (для чистых ребейзов).
- **Всё новое — за build-tag** (`with_xhttp`, `with_awg`) и **в новых файлах/пакетах**.
- **Правки upstream-файлов** — только помеченными `// lx:` блоками, атомарными коммитами.
- **Никаких merge с upstream — только rebase.** `origin/lx` всегда ребейзится на тег.
- **Имя бинаря — `sing-box`** (drop-in для лаунчера); идентичность `-lx` — в версии.
- **Scope — client-only**: outbound/endpoint. Server/inbound отложены.

---

## Язык

Спеки, отчёты и ответы в чате — **русский**. Код, комментарии, коммиты — английский, в стиле upstream sing-box.
