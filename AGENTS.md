# Руководство для AI-агентов — sing-box-lx

`sing-box-lx` — **тонкий downstream** апстрима [SagerNet/sing-box](https://github.com/SagerNet/sing-box): upstream **плюс небольшой набор клиентских фич** (XHTTP, AWG2, MASQUE, VLESS-шифрование, DNS-группа, наблюдаемость, балансировка, энергосбережение — актуальный индекс в [SPECS/FEATURES/](SPECS/FEATURES/README.md)) и ничего больше.

Главная ценность проекта — **согласованность с upstream**. Любое изменение оценивается по тому, насколько дёшево оно переживёт следующий мерж `upstream/testing` (ритуал — [docs-lx/lx-release-runbook.md](docs-lx/lx-release-runbook.md)).

---

## Что читать в первую очередь

| Документ | Назначение |
|----------|------------|
| **SPECS/CONSTITUTION.md** | Неизменяемые принципы: приоритеты, build-tag изоляция, минимальный дифф, запреты |
| **SPECS/IMPLEMENTATION_PROMPT.md** | DoD, git-ритуал синка/релиза, команды сборки и тестов, контракт выхода |
| **docs-lx/lx-release-runbook.md** | Актуальный ритуал мержа upstream и срезания релизного тега |
| **SPECS/README.md** | Структура Spec Kit: фичи и задачи, workflow |
| **SPECS/FEATURES/README.md** | Индекс фич — начинать отсюда, чтобы понять предметную область целиком |

Документация двухуровневая: **[SPECS/FEATURES/](SPECS/FEATURES/README.md)** — фичи (актуальное состояние области), **SPECS/TASKS/`NNN-NAME`** — задачи (единицы работы). Задача несёт обратную ссылку `**Фича:**` под заголовком.

Перед реализацией задачи прочитай **FEATURE.md её фичи** (контекст и грабли области), затем её **SPEC.md → PLAN.md → TASKS.md**, и применяй **IMPLEMENTATION_PROMPT.md**.

В новых ссылках указывай **фичу**, а не задачу; ссылка на задачу — только когда нужен конкретный разбор.

---

## Жёсткие границы (детали — в CONSTITUTION)

- **Набор фич ограничен.** Новая фича — только через тест §3.1 CONSTITUTION и решение пользователя, не по собственной инициативе.
- **Go module path остаётся `github.com/sagernet/sing-box`** (для дешёвых синков).
- **Всё новое — за build-tag** (`with_xhttp`, `with_awg`, `with_lx_command`, `with_lx_idle_suspend`) и **в новых файлах/пакетах**; фичи без ручки в сборке (MASQUE, VLESS-шифрование, DNS-группа) изолируются lx-файлами.
- **Правки upstream-файлов** — только помеченными `// lx:` блоками, атомарными коммитами.
- **Синхронизация с upstream — ручной merge `upstream/testing`** (не rebase); `lx` никогда не форс-пушится. Ритуал — в runbook.
- **Два форк-сабмодуля** — `submodules/wireguard-go` и `submodules/sing-tun`: встречные upstream-бампы их гитлинков на мерже не принимать вслепую (откатят наши патчи).
- **Имя бинаря — `sing-box`** (drop-in для лаунчера); идентичность `-lx` — в версии.
- **Scope — client-only**: outbound/endpoint. Server/inbound отложены.

---

## Язык

Спеки, отчёты и ответы в чате — **русский**. Код, комментарии, коммиты — английский, в стиле upstream sing-box.
