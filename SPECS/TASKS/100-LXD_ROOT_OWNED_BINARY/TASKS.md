# TASKS: 100 — LXD_ROOT_OWNED_BINARY

- [x] 1. SPEC/PLAN/TASKS; строки в фиче 014, индексе фич и Roadmap
- [x] 2. Предикат инварианта + обход цепочки + табличные тесты (Linux без root)
- [x] 3. Безопасное копирование + сайдкар + uninstall-сверка + тесты
- [x] 4. darwin: install (каталог, копия, сайдкар, chown support, plist), `--service=copy`, dry-run с планом, uninstall по сайдкару (с plist и без), `--service=status` с кодами 0/2/3/4, `--exec-dir`; табличный тест переходов и аномалий, plist, dry-run, `launchctl print`
- [x] 5. Самопроверка при старте (`ppid 1` + `XPC_SERVICE_NAME`), `--allow-unsafe-exec`, та же проверка для `sing-box run`; тест контекстов
- [x] 6. `/admin/info`: `executable`, `executable_sha256`; admin_test
- [x] 7. CI: `GOOS=darwin go vet ./lxd/ ./cmd/sing-box/` в lint
- [x] 8. Доки `lxd-daemon(.ru).md`, фича 014, changelog `v1.14.1-lx.11`, релиз-ноты, IMPLEMENTATION_REPORT
- [ ] 9. Ручная проверка владельцем под sudo (install/status/reinstall/copy/uninstall) → статус D
