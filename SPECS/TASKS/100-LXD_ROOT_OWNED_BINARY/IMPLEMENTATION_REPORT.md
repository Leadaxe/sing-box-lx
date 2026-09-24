# IMPLEMENTATION_REPORT: 100 — LXD_ROOT_OWNED_BINARY

Ветка `spec100-lxd-root-owned-binary` от `lx` (`4146bc8ce`), не запушена. Тег
`v1.14.1-lx.11` режет другая сессия.

## Коммиты

| Коммит | Что |
|---|---|
| `5acaa98a4` | спека, план, задачи; строки в фиче 014, индексе, Roadmap |
| `e63379c85` | предикат инварианта, обход цепочки, проход каталога, табличные тесты |
| `519e1721a` | безопасное копирование, сайдкар, решение uninstall, тесты |
| `640202632` | darwin: install через копию, `--service=copy`, `--service=status`, uninstall по сайдкару, `--exec-dir`, dry-run; `serviceEnv` и табличный тест переходов |
| `5e01d6464` | самопроверка `lxd`/`run` под root, `--allow-unsafe-exec` |
| `f77e85293` | `/admin/info`: `executable`, `executable_sha256` |
| `b9f75d0e6` | CI: `GOOS=darwin go vet ./lxd/ ./cmd/sing-box/` |
| `c0b2a2635` | формулировки dry-run/uninstall, ширина колонки отчёта |
| (этот) | доки, changelog, релиз-ноты, отчёт |

## Файлы

- **Новые (пакет `lxd/`):** `execsafe.go`, `execsafe_unix.go`, `execsafe_other.go`,
  `execcopy.go`, `execsafe_test.go`, `execcopy_test.go`.
- **Правки наших файлов:** `lxd/service_darwin.go` (+тест), `lxd/service_linux.go`
  (+тест: только сигнатуры), `lxd/service_stub.go`, `lxd/apply.go`, `lxd/daemon.go`,
  `lxd/admin.go`, `lxd/admin_test.go`, `cmd/sing-box/cmd_lxd_lx.go`.
- **`// lx:` правки апстрима:** нет. `sing-box run` обёрнут из `init()` в
  `cmd_lxd_lx.go` (подмена `commandRun.Run`), `cmd_run.go` не тронут.
- **deps / go.mod:** нет.
- **wiring / CI:** `.github/workflows/lx-ci.yml` — шаг в lint.

## Проверки

```sh
TAGS=$(make -s -f Makefile.lx lx-print-tags)
go test -tags "$TAGS" -ldflags -checklinkname=0 ./lxd/ -count=1 \
  -run 'TestService|TestBuildPlist|TestDryRun|TestInvariant|TestCopy|TestSidecar|TestAdminInfo'   # ok
go test -race -count=1 -ldflags -checklinkname=0 -tags "$TAGS" ./lxd/ ./cmd/sing-box/            # ok (14 с / 7.6 с)
GOOS=linux   go vet -tags "$TAGS" ./lxd/ ./cmd/sing-box/                                          # ok
GOOS=windows go vet -tags "$TAGS" ./lxd/                                                          # ok
CGO_ENABLED=0 GOOS=darwin GOARCH={amd64,arm64} go vet -tags "<CI BASE_TAGS>,with_xhttp,with_awg,with_lx_command,with_lxd" ./lxd/ ./cmd/sing-box/   # ok — шаг CI
make -f Makefile.lx lx-build                                                                      # ok
gofmt -l lxd/ cmd/sing-box/cmd_lxd_lx*.go                                                         # пусто
```

Мутационные проверки: снятая сверка sha в копировании и в uninstall валит
`TestCopyShaMismatchDiscardsTemp` и `TestSidecarUninstallDecisions`.

Живой прогон без root на этом Mac (стоит служба старого образца, plist → бинарь в бандле):
`--service=status` → `UNSAFE — … /Applications: owned by uid 0, mode 0775 …`, выход 2;
`--service=install --dry-run` и `--service=copy --dry-run` печатают план копии в
`/Library/PrivilegedHelperTools/com.leadaxe.sing-box-lxd/sing-box` и ничего не создают;
`--service=uninstall --dry-run` оставляет бинарь бандла («has no sidecar … not a copy this
service installed»); `--service=copy` без sudo — отказ, выход 1. Настоящие install/copy/
uninstall не запускались (нужен sudo).

## DoD

1. Сборка без тегов — `lxd` отсутствует, апстрим не тронут. ✔
2. Сборка с тегами (`lx-build`). ✔
3. `go vet` и `go test -race` по затронутым пакетам. ✔
4. `sing-box check` не затронут (нет новых ключей конфига). ✔
5. Правок апстрим-файлов нет. ✔
6. TASKS.md отражает факт, отчёт заполнен. ✔
7. Статус — **I**: ручная проверка под sudo (SPEC §5 п. 8) — за владельцем.

## Зона конфликтов при мерже апстрима

Нет: код только в `lxd/` и `cmd/sing-box/cmd_lxd_lx.go`. Точка внимания — обёртка
`commandRun.Run`: если апстрим переименует `commandRun` или переведёт его на `RunE`,
сборка с `with_lxd` упадёт на компиляции (видно сразу).

## Вне скоупа

- Linux: рецепт unit'а по-прежнему ставит бинарь, выбранный оператором; `copy`/`status` —
  «macOS only»; строгий отказ самопроверки на Linux не срабатывает (нет метки launchd).
- Блокировка против одновременных `copy`/`install` с разными бинарями (SPEC §4).
- ACL и флаги файловой системы в инварианте.
