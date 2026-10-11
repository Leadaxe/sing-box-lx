# TASKS: 124 — LXD_DEFAULT_EXEC_DIR_CREATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC/PLAN/TASKS; строка 124 в Roadmap и FEATURE 014
- [x] 2. `checkExecDir(leafOnly)`: создание последнего компонента дефолта в install/copy, `would create` в dry-run
- [x] 3. `TestServiceDefaultExecDirCreated`; тесты 100 зелёные
- [x] 4. usage `--exec-dir`, lxd-daemon EN/RU, SPEC 100 (пометка о замене), changelog
- [x] 5. IMPLEMENTATION_REPORT.md; статус I
- [ ] 6. Живой прогон `sudo sing-box lxd --service=install` на чистом маке (без `/Library/PrivilegedHelperTools`)
