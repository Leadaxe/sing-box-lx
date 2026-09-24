# TASKS: 100 — LXD_ROOT_OWNED_BINARY

- [ ] 1. SPEC/PLAN/TASKS; строки в фиче 014, индексе фич и Roadmap
- [ ] 2. Предикат инварианта + обход цепочки + табличные тесты (Linux без root)
- [ ] 3. Безопасное копирование + сайдкар + uninstall-сверка + тесты
- [ ] 4. darwin: install (каталог, копия, сайдкар, chown support, plist), dry-run с планом, uninstall по сайдкару, `--service=status`, `--exec-dir`; тесты plist/dry-run/launchctl
- [ ] 5. Самопроверка при старте (`ppid 1` + `XPC_SERVICE_NAME`), `--allow-unsafe-exec`; тест контекстов
- [ ] 6. `/admin/info`: `executable`, `executable_sha256`; admin_test
- [ ] 7. CI: `GOOS=darwin go vet ./lxd/ ./cmd/sing-box/` в lint
- [ ] 8. Доки `lxd-daemon(.ru).md`, фича 014, changelog `v1.14.1-lx.11`, релиз-ноты, IMPLEMENTATION_REPORT
- [ ] 9. Ручная проверка владельцем под sudo (install/status/reinstall/uninstall) → статус D
