# SPEC: 124 — LXD_DEFAULT_EXEC_DIR_CREATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — `lxd --service=install`/`copy` не работают на чистой macOS |
| Статус | I (implemented) — 2026-10-10: код, табличный тест без root ([IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md)); живой прогон на чистом маке — впереди |
| Ветка | `lx` |
| Build-tag | `with_lxd` (lx-файл `lxd/service_darwin.go`) |
| Источник | Полевой отказ: MacBook Pro 2016 (Intel), macOS 12.7.6, ядро `1.14.3-lx.14` |
| Связано | [100](../100-LXD_ROOT_OWNED_BINARY/SPEC.md) (root-owned копия, §2.3 — пункт «дефолтный каталог не создаётся» заменён этой SPEC) |

`install` и `copy` создают отсутствующий `/Library/PrivilegedHelperTools` `root:wheel 0755`,
если отсутствует только он, а `/Library` проходит проверку root-owned цепочки.

## 1. Проблема

`sudo sing-box lxd --service=install` на чистой macOS:

```
FATAL exec dir /Library/PrivilegedHelperTools does not exist; it is part of macOS —
recreate it root:wheel 0755 (sudo mkdir -m 0755 /Library/PrivilegedHelperTools) or pass --exec-dir
```

macOS не создаёт `/Library/PrivilegedHelperTools` при установке системы — его создаёт
SMJobBless при первой установке чьего-то root-помощника. На маке, где ни одна программа
помощника не ставила, каталога нет. 100 считал его частью системы и запрещал создавать.
Так же падают `--service=copy` (копия для TUN в classic-режиме лаунчера), кнопка Install в
лаунчере и `install-macos.sh` — все зовут ядро; лаунчер строит команду верно.

## 2. Поведение

Решение владельца 2026-10-10: запрет снять. `sudo mkdir -m 0755` в `/Library` даёт то же,
что создал бы SMJobBless; безопасность не меняется, ручной шаг на каждом чистом маке
требовать нельзя.

1. **install, copy** с дефолтным каталогом: если отсутствует только последний компонент, а
   родитель `/Library` существует и проходит инвариант 100 §2.2 по всей цепочке от `/`, —
   каталог создаётся тем же путём, что для `--exec-dir` (`ensureRootOwnedDir`, `dirCreate`):
   `os.Mkdir`, явный chown `0:0`, chmod `0755`, затем `checkRootOwnedChain` по итоговому
   пути. Печатается `lxd: created /Library/PrivilegedHelperTools (root:wheel 0755)`.
2. **Только последний компонент.** Нет `/Library` — отказ
   `exec dir …: its parent /Library does not exist; it is part of macOS — the system is damaged; pass --exec-dir`.
   `/Library` не проходит инвариант — отказ с нарушением, как раньше. Ничего не создаётся.
3. **Существующий каталог** проверяется как раньше: не root, запись для group/other,
   симлинк, не каталог — отказ. Права не переписываются. Sticky-бит (`1755` у SMJobBless)
   допустим; создаётся `0755` без него.
4. **`--dry-run`** (`planCopy`, `planExecCopy` из `printPlan`) для отсутствующего дефолта
   печатает `lxd: would create /Library/PrivilegedHelperTools (root:wheel 0755)` вместо отказа.
5. **status, uninstall** каталог не создают и не проверяют на отсутствие: status —
   `NOT INSTALLED`, uninstall — нечего удалять, без ошибки.
6. **`--exec-dir`** — без изменений (создаются все недостающие компоненты).

## 3. Критерии приёмки

- Табличный тест `serviceEnv` без root (как 100 §2.7): отсутствующий дефолт → создаётся при
  install и copy (`0755`, строка `created`); dry-run copy и install — `would create`, каталога
  нет; status/uninstall — без ошибки, каталога нет; нет родителя — отказ, родитель не создан;
  небезопасный родитель — отказ до создания; существующий чужой каталог и симлинк — отказ,
  в том числе в dry-run.
- Существующие тесты 100 зелёные.
- Апстримных файлов ноль.
