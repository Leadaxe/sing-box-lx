# IMPLEMENTATION REPORT: 124 — LXD_DEFAULT_EXEC_DIR_CREATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `lxd/service_darwin.go` | `checkExecDir(out, execDir, action, chown, leafOnly)`: при `leafOnly` (дефолтный каталог) сначала `checkRootOwnedChain` родителя — отсутствие даёт отказ `its parent … does not exist`, нарушение — отказ как в 100; затем `ensureRootOwnedDir`, которому остаётся создать или объявить только последний компонент. `placeCopy` зовёт с `dirCreate`, `planExecCopy` — с `dirPlan`; `dirCheck` в путях службы больше не используется. Текст старой ошибки «it is part of macOS — recreate it» ушёл |
| `lxd/service_darwin_test.go` | `TestServiceDefaultExecDirCreated`: status/uninstall над отсутствующим дефолтом — без ошибки и без создания; dry-run copy и install — `would create`, каталога нет; copy и install — каталог `0755` и строка `lxd: created … (root:wheel 0755)`; нет родителя — отказ, родитель не создан; родитель uid 501 — отказ до создания; существующий каталог uid 501 — отказ в copy и dry-run; симлинк — отказ. Из `TestServiceExecDirAndLegacyLayout` убрана проверка «дефолт не создаётся» |
| `cmd/sing-box/cmd_lxd_lx.go` | usage `--exec-dir`: «created root:wheel 0755 if missing» |

`ensureRootOwnedDir` и инвариант 100 не тронуты. Апстримных файлов ноль.

## Проверка

- `gofmt`, `go vet`, `go build ./cmd/sing-box`, `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8) — ok.

## Открыто

- Живой прогон на чистом маке (без каталога) — за владельцем/репортёром.
