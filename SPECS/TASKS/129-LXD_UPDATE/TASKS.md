# TASKS: 129 — LXD_UPDATE

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC утверждена 2026-10-11; раунд вопросов 2026-10-12 (`--service`, restart упал ≠ откат, прокси из env, репо зашит, `check` пропускается без `last_good.json`)
- [x] 2. Чистые функции: версии, `SHA256SUMS`, потоковая распаковка, команды init
- [x] 3. `runUpdate` с инжектируемым окружением; `Update()`; подкоманда `lxd update`
- [x] 4. `sing-box version`: `Platform:` / `Init:` / `Update:` через `// lx:`-хук
- [x] 5. Инсталлятор OpenWrt: `commands.update`; README
- [x] 6. Тесты: версии, `SHA256SUMS`, распаковка/сумма/два бинаря, успех, откат, restart упал, `--no-restart`/неизвестный init, `--dry-run`/актуально/даунгрейд, `--prerelease`/гварды
- [x] 7. lxd-daemon EN/RU, changelog, FEATURE, Roadmap; IMPLEMENTATION_REPORT; статус I
- [ ] 8. Живой прогон на роутере до тега: lx.N-1 → `POST /admin/commands/update` → `done: … is running`, `/admin/info.version` новая; повтор → `already up to date`; `--dry-run`
