# IMPLEMENTATION_REPORT — 001 FORK_BOOTSTRAP

**Дата:** 2026-06-09 · **Статус:** Complete · **База:** upstream `v1.13.13`

## Что сделано

Заложен скелет downstream'а `sing-box-lx`: репозиторий, ветка, воспроизводимая сборка drop-in бинаря с версией `-lx`, CI-скелет. Фич-кода (XHTTP/AWG) нет — это 002/003.

## Изменённые / новые файлы

**Новые (зона касания upstream = ноль):**
- `Makefile.lx` — `LX_TAGS` (канонический набор), `LX_VERSION = <upstream>-lx.<N>`, цели `lx-build` / `lx-version` / `lx-check`. Версия штампуется **только ldflags** (`-X …constant.Version`), output — `sing-box`.
- `.github/workflows/lx-ci.yml` — build(lx tags) → version → `go vet` → `sing-box check` (linux/amd64; полная матрица — в 004).
- `lx-test/config/minimal.json` — валидный конфиг для `check` (mixed-in + direct-out). Положен в `lx-test/`, **не** в upstream `test/` (там отдельный Go-модуль).
- `AGENTS.md` — указатель для агентов (force-add: upstream его `.gitignore`-ит; новый файл → нулевой конфликт при ребейзе).
- `SPECS/**` — Spec Kit (CONSTITUTION, IMPLEMENTATION_PROMPT, README, задачи 001–004).

**Правок upstream-файлов: 0.** `constant/version.go`, `Makefile`, `.gitignore` — не тронуты.

## Проверки (DoD)

```
$ make -f Makefile.lx lx-version      → 1.13.13-lx.1
$ make -f Makefile.lx lx-build        → ./sing-box (28 MB)
$ ./sing-box version                  → 1.13.13-lx.1
    Tags: …,with_xhttp,with_awg        (пока no-op — кода нет)
$ ./sing-box check -c lx-test/config/minimal.json  → OK
$ go vet ./constant/...                → OK
```

## Решения по ходу

- **`Makefile.lx` вместо правки upstream `Makefile`** — отдельный файл = нулевая зона касания (CONSTITUTION § 3.2). Цели вызываются `make -f Makefile.lx <target>`.
- **Версия через ldflags** — upstream и сам так делает (`PARAMS = -ldflags "-X …constant.Version=…"`), поэтому `constant/version.go` не правим.
- **Email privacy** — коммиты переведены на `247031499+Leadaxe@users.noreply.github.com` (репо-локальный `user.email`), иначе GitHub отклоняет push.
- **Sample-конфиги** — каталог `lx-test/config/` (upstream `test/` — обособленный Go-модуль со своим `config/`).

## Зона касания upstream для будущих ребейзов

**Нулевая** (всё в новых файлах). Первый реальный `// lx:` дифф появится в 002 (диспетчер транспортов) и 003 (`go.mod`, wireguard-endpoint).

## Вне скоупа (передано дальше)

- Полная CI-матрица, авто-ребейз, релизы → **004**.
- Прунинг зеркальных веток origin — опционально, отложено.
