# PLAN: 124 — LXD_DEFAULT_EXEC_DIR_CREATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `lxd/service_darwin.go` | `checkExecDir(…, leafOnly)`: для дефолта сначала `checkRootOwnedChain` родителя (нет родителя — отказ с причиной), затем `ensureRootOwnedDir` с `dirCreate`/`dirPlan`; `placeCopy` и `planExecCopy` всегда создают/планируют, `leafOnly = !createExecDir`; комментарии `defaultExecDir`, `serviceEnv.createExecDir` |
| `lxd/service_darwin_test.go` | `TestServiceDefaultExecDirCreated`; из `TestServiceExecDirAndLegacyLayout` убрана проверка «дефолт не создаётся» |
| `cmd/sing-box/cmd_lxd_lx.go` | usage `--exec-dir` |
| `docs-lx/lxd-daemon.md`, `.ru.md` | таблица флагов, абзац о каталоге |
| `SPECS/TASKS/100-…/SPEC.md`, `SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md`, `SPECS/README.md`, `docs-lx/lx-changelog.md` | пометка о замене, строки 124 |

## Что не меняется

- `ensureRootOwnedDir`, `checkRootOwnedChain`, инвариант 100 §2.2 — без правок.
- status, uninstall, `--exec-dir`, Windows/linux.

## Проверка

- `gofmt`, `go vet`, `go test -tags "$(make -s -f Makefile.lx lx-print-tags)" -ldflags=-checklinkname=0 ./lxd/` (go1.26.8).
