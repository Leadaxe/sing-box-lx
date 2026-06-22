# TASKS — 006-LINUX_MUSL_STATIC_ROUTER_BUILDS

## CI: musl-linux job
- [x] `lx-release.yml`: новый job `build_linux_musl`, матрица amd64/arm64/armv7(GOARM=7)/mipsle(GOMIPS=softfloat)
- [x] Clone cronet-go по `.github/CRONET_GO_VERSION` + submodules; regenerate keyring
- [x] Cache + download Chromium **musl** toolchain (`cmd/build-naive … --libc=musl`); set env
- [x] Build: `CGO_ENABLED=1`, теги `LX_TAGS` с `with_purego`→`with_musl`, `with_naive_outbound` сохранён
- [x] Из старого `build` job убрать строки `linux/*`; `release.needs` += `build_linux_musl`

## Нейминг + notes
- [x] Ассеты: `linux-amd64`, `linux-arm64`, `linux-armv7`, `linux-mipsle-softfloat` (без `-musl` суффикса)
- [x] `notes.md`: секция про роутерные musl-static арки + что naive сохранён

## Приёмка
- [x] Валидация YAML (actionlint/синтаксис)
- [x] Прогон `workflow_dispatch`; зелёные musl-сборки
- [x] В логах: `file` → statically linked, `strings|grep libdl.so.2` → 0
- [x] (по возможности) запуск armv7/arm64 под qemu-user — `sing-box version` без ошибки загрузчика
- [x] mipsle: подтвердить musl+naive; иначе fallback `_OTHERS` без naive (зафиксировать)

## Закрытие
- [x] IMPLEMENTATION_REPORT.md, DoD
- [x] `SPECS/README.md` roadmap-строка → C
- [x] Ответ в issue #1 (Dr4tez): armv7+arm64 musl-static с naive, имена ассетов; Win7-naive невозможен
- [ ] Боевой релиз `v1.13.13-lx.7` (ждёт ОК пользователя)
