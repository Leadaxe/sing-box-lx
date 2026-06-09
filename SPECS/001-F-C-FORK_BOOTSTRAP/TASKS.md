# TASKS — 001-F-N-FORK_BOOTSTRAP

## Git / GitHub
- [x] `origin` = Leadaxe/sing-box-lx, `upstream` = SagerNet/sing-box
- [x] Ветка `lx` от тега `v1.13.13`
- [x] Default branch на GitHub → `lx`, push `lx`
- [x] Коммиты переведены на GitHub noreply-email (email privacy)
- [ ] (опц., отложено) Удалить шумные зеркальные ветки с origin (`dependabot/*`, `copilot/*`, `dev-*`)

## Build / Version
- [x] Изучён механизм версии upstream: `constant/version.go` = `var Version = "unknown"`, штампуется через `-ldflags -X …constant.Version`
- [x] **`Makefile.lx`** (новый файл, ноль правок upstream-Makefile): `LX_TAGS`, `LX_LDFLAGS`, цель `lx-build` (output `sing-box`)
- [x] Версия печатает `-lx.N` через ldflags (`1.13.13-lx.1`); `constant/version.go` **не тронут**

## Конвенции
- [x] Маркеры `// lx:begin/end` зафиксированы в CONSTITUTION § 3.3
- [x] Шаблон `include/<feat>.go` + `<feat>_stub.go` подтверждён на upstream `include/wireguard.go`

## CI-скелет
- [x] `.github/workflows/lx-ci.yml`: build(lx tags) + version + vet + `sing-box check`
- [x] `lx-test/config/minimal.json` (валидный конфиг без фич)

## Закрытие
- [x] DoD: `lx-build` ок, `version` → `-lx.1`, `check` OK, `go vet` OK; имя бинаря `sing-box`
- [x] IMPLEMENTATION_REPORT.md
- [x] Папка → статус `C`
