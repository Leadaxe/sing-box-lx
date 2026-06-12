# TASKS — 006-F-N-LINUX_MUSL_STATIC_ROUTER_BUILDS

## CI: musl-linux job
- [ ] `lx-release.yml`: новый job `build_linux_musl`, матрица amd64/arm64/armv7(GOARM=7)/mipsle(GOMIPS=softfloat)
- [ ] Clone cronet-go по `.github/CRONET_GO_VERSION` + submodules; regenerate keyring
- [ ] Cache + download Chromium **musl** toolchain (`cmd/build-naive … --libc=musl`); set env
- [ ] Build: `CGO_ENABLED=1`, теги `LX_TAGS` с `with_purego`→`with_musl`, `with_naive_outbound` сохранён
- [ ] Из старого `build` job убрать строки `linux/*`; `release.needs` += `build_linux_musl`

## Нейминг + notes
- [ ] Ассеты: `linux-amd64`, `linux-arm64`, `linux-armv7`, `linux-mipsle-softfloat` (без `-musl` суффикса)
- [ ] `notes.md`: секция про роутерные musl-static арки + что naive сохранён

## Приёмка
- [ ] Валидация YAML (actionlint/синтаксис)
- [ ] Прогон `workflow_dispatch`; зелёные musl-сборки
- [ ] В логах: `file` → statically linked, `strings|grep libdl.so.2` → 0
- [ ] (по возможности) запуск armv7/arm64 под qemu-user — `sing-box version` без ошибки загрузчика
- [ ] mipsle: подтвердить musl+naive; иначе fallback `_OTHERS` без naive (зафиксировать)

## Закрытие
- [ ] IMPLEMENTATION_REPORT.md, DoD
- [ ] `SPECS/README.md` roadmap-строка → C
- [ ] Ответ в issue #1 (Dr4tez): armv7+arm64 musl-static с naive, имена ассетов; Win7-naive невозможен
- [ ] Боевой релиз `v1.13.13-lx.7`
