# TASKS: 098 — LX_ROOT_CONFIG_BLOCK

- [ ] 1. PLAN/TASKS; статус O в SPEC и Roadmap
- [x] 2. `option/lx.go`: типы блока, `ResolveLX` (алиасы, валидация с полными путями, каноникализация), строка в `options.go`, комментарии deprecated в `route.go`, `IdleTimeout` masque → указатель
- [x] 3. `box_lx.go` + строка в `box.go`: резолв, предупреждения в лог, `*LXResolved` в контексте; `route/router.go` читает idle-поля из контекста; тексты ошибок тика и stub'а → `lx.wg.*`, stub отвергает `lazy_build`/`build_max`
- [x] 4. `protocol/masque`: `idleWindow(узел, глобальный)` — приоритет узел > `lx.masque.idle_timeout` > выкл; явный `"0"` узла выключает
- [x] 5. `daemon/instance.go`: резолв перед `captureRunningConfig` — канонический блок в running-config
- [x] 6. Тесты: `option/lx_test.go` (парсинг, правила, алиасы ×4, канонизация, `naive` отвергнут), роутер (оба входа дают одинаковый тик; гейт stub), masque (приоритет ×3), running-config (канонический `lx`); `lx-test/config/lx_block.json` + `lx-check` по всем конфигам
- [x] 7. `go build` без тегов и с `LX_TAGS`; `go test` (с `-ldflags "-checklinkname=0"`) пакетов `option`, `route`, `protocol/masque`, `daemon`, `protocol/wireguard`; `gofmt` lx-файлов; стенды `lx-test`
- [x] 8. Доки: `lx-config(.ru).md` (раздел «Блок `lx`», deprecated у старых ключей, §0), `lx-energy(.ru).md`, FEATURE 008/009, changelog; IMPLEMENTATION_REPORT
- [ ] 9. Статус I; уведомить сессии LxBox/лаунчера о переходе на `lx.*` до снятия алиасов
