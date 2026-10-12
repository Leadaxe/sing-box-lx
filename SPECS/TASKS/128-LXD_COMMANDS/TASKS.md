# TASKS: 128 — LXD_COMMANDS

**Фича:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC утверждена 2026-10-11; раунд вопросов 2026-10-12 (гейт однородный, исход рядом с логом, тело → 400, unix, `LXD_CLIENT`)
- [x] 2. Ключ `commands` + валидация; `Options.Commands`; runner, detached-обёртка, файлы исхода, окружение
- [x] 3. Маршруты `GET/POST /admin/commands[/{name}]`; аудит
- [x] 4. Тесты: валидация, исход, окружение, `409`+detached+рестарт+`lost`, маршруты/`400`/`404`, каталог и очистка
- [x] 5. lxd-daemon EN/RU (§3, §8.5, §10), changelog, FEATURE, Roadmap; IMPLEMENTATION_REPORT; статус I
- [ ] 6. Инсталлятор OpenWrt пишет `commands.update` — после 129
- [ ] 7. Живая проверка на роутере (вместе со 127/129): `"echo": "echo $LXD_PLATFORM"` → `POST` → `GET` = `linux-arm64`, `exit_code: 0`. 2026-10-12: на RouteRich стоит тестовая сборка с `commands` (`update`, `echo`) в `daemon.json` — демон стартовал, каталог `/root/commands/` создан; `POST`/`GET` — через лаунчер, когда получит поддержку
