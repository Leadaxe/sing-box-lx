# TASKS: 103 — LXD_WINDOWS_SERVICE

- [x] 1. SPEC/PLAN/TASKS; строки в Roadmap и фиче 014; решения по §4.2
- [x] 2. Портируемое: развязка имён копии и сайдкара, сайдкар набора `files[]` и решения uninstall/keep-copy по набору, `--invite-out`/`--invite-name` (+ `client add --invite-out`), строка `INFO` самопроверки, предикат «привилегирован», ветка `ctx.Done()` в `lxd.Run`; тесты на Linux, golden сайдкара macOS
- [x] 3. Инвариант Windows: портируемый предикат ACL + табличный тест; `execsafe_windows.go` (чтение владельца/DACL без следования ссылкам, цепочка от тома, NTFS/fixed), самопроверка по контекстам 2.11
- [x] 4. Копия и данные: `PrepareServiceHome` (захват `ProgramData`, `state\`, `logs\`), размещение набора с `.old`/temp/sha/rename, DACL каталога копии, лишние файлы
- [x] 5. SCM: install (stop → набор → `CreateService`/`UpdateConfig`, recovery с `NonCrashFailures`, DACL службы → start), uninstall (`--keep-copy`, `--purge`, dry-run), status без прав с кодами 0/2/3/4/5/1; `svc.Run` и обработчик; Windows-тесты через подменный SCM
- [x] 6. Лог Windows: один дескриптор, `SetStdHandle`, `SetStdLogger`, ротация копированием; `log_file` в `daemon.json` при install
- [x] 7. `dllsearch_windows_lx.go` (`SetDefaultDllDirectories` с проверкой процедуры); закрепление отказа `libcronet.dll` (§4.2 п. 2); `Chdir` в state-dir в `Execute` (§4.2 п. 1)
- [x] 8. CI: `GOOS=windows go vet ./lxd/ ./cmd/sing-box/` в lint; джоба `test-windows` (§4.2 п. 9)
- [x] 9. Доки `lxd-daemon(.ru).md` (раздел Windows по образцу §7/§7.1), фича 014 (ручки, пути Windows), changelog `v1.14.2-lx.2`, IMPLEMENTATION_REPORT; релиз-ноты — ведущая сессия при релизе
- [ ] 10. Живой прогон на Windows (§5 п. 8, пункты §4.3), сопряжение лаунчера через `--invite-out` → статус D
