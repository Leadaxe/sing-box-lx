# TASKS: 127 — LXD_INFO_PLATFORM

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. PLAN/TASKS; строка 127 в Roadmap и FEATURE 014
- [x] 2. Решения владельца 2026-10-11: `init_system` хоста + `service` процесса; один детектор linux; `platform` из build info — спека переписана
- [x] 3. `detectLinuxInit` общий для рецепта и паспорта; `detectInitSystem`; `releasePlatform`; платформенные `platformInitSystemEnv`
- [x] 4. Пять полей в `/admin/info`
- [x] 5. Табличные тесты; `fakeInit` на `linuxInitProbe`; тесты пакета зелёные, linux-тесты компилируются
- [x] 6. lxd-daemon EN/RU, changelog, FEATURE, Roadmap
- [x] 7. IMPLEMENTATION_REPORT.md; статус I
- [ ] 8. Ручная проверка после тега: роутер `linux-arm64`/`procd`/`true`, Mac со службой `darwin-arm64`/`launchd`/`true`, `sing-box lxd` руками — `service:false`
