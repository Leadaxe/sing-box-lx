# TASKS — 001-F-N-FORK_BOOTSTRAP

## Git / GitHub
- [x] `origin` = Leadaxe/sing-box-lx, `upstream` = SagerNet/sing-box
- [x] Ветка `lx` от тега `v1.13.13`
- [ ] Default branch на GitHub → `lx`, push `lx`
- [ ] (опц.) Удалить шумные зеркальные ветки с origin (`dependabot/*`, `copilot/*`, `dev-*`)

## Build / Version
- [ ] Изучить механизм версии upstream (`constant/version.go`, `cmd/sing-box`, ldflags)
- [ ] `Makefile`: `LX_TAGS`, `LX_LDFLAGS`, цель `lx-build` (output `sing-box`)
- [ ] Версия печатает `-lx.N` (через ldflags; иначе минимальный `// lx:` дифф)

## Конвенции
- [ ] Зафиксировать маркеры `// lx:begin/end` (в CONSTITUTION уже описаны — проверить применимость)
- [ ] Подтвердить шаблон `include/<feat>.go` + `<feat>_stub.go` на примере upstream `wireguard.go`

## CI-скелет
- [ ] `.github/workflows/lx-ci.yml`: build(lx tags) + vet + `sing-box check`
- [ ] `test/config/` sample-конфиг(и)

## Закрытие
- [ ] DoD-чеклист (CONSTITUTION § 3 / IMPLEMENTATION_PROMPT § 2)
- [ ] IMPLEMENTATION_REPORT.md
- [ ] Папка → статус `C`
