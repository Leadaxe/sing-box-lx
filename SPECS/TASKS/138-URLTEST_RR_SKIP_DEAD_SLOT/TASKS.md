# TASKS: 138 — URLTEST_RR_SKIP_DEAD_SLOT

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · план — [PLAN.md](PLAN.md)

Обход мёртвого слота:

- [x] 1. Обход в выборе, тесты `urltest_rr_skip_lx_test.go`, A/B на роутере; PR [#54](https://github.com/Leadaxe/sing-box-lx/pull/54) влит 2026-10-23, в релизах с `v1.14.3-lx.23`

Самовосстановление по отказам:

- [x] 2. Первая версия (этапы 1–6) и стенды на NanoPi — [HISTORY.md](HISTORY.md)
- [x] 3. [ALGORITHM.md](ALGORITHM.md) — утверждён владельцем 2026-11-25
- [x] 4. Код по алгоритму: классы исходов, проба жизни, проверка пула с курсором, K1/K2/K5, Q1 с правилом половины, эпоха по N3 (`630d081a6`, `1f960c2e9`, `176c18538`, `8ab84cbef`, `1acc12a38`, `0fddeff77`, `94122a533`)
- [x] 5. Тесты правил по одному (`f965d6020`); тесты пакетов с `LX_TAGS` и `-race`
- [x] 6. Повтор в заморозке разрешён, выводы запрещены (решение 2026-11-25): `c0349345a`, `78eacdb71`
- [ ] 7. Финальный стенд на NanoPi по алгоритму — итог в SPEC и IMPLEMENTATION_REPORT
- [ ] 8. Пользовательские документы: `docs/configuration/outbound/urltest.md`, `docs-lx/lx-urltest.md` и `.ru.md`, `docs-lx/lx-changelog.md`, FEATURE 007
- [ ] 9. Пуш `lx` и релиз
- [ ] 10. Полевая проверка по issue #76 на роутере с подпиской: узел убит вручную, `interval` 10 мин, со sticky и без; результат в issue

Вне ядра:

- [ ] 11. LxBox: `GetPoolState`, показ карантина, счётчиков, сдвинутых и помеченных ключей, заморозки
- [ ] 12. Лаунчер: новые поля пула; событие обновления для соединения без записи у клиента не применяется (`core/backend_daemon_traffic.go`)
