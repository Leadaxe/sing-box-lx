# TASKS: 138 — URLTEST_RR_SKIP_DEAD_SLOT

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · план — [PLAN.md](PLAN.md)

- [x] 1. SPEC/PLAN/TASKS; строка 138 в Roadmap и FEATURE 007
- [x] 2. Решение владельца по дизайну (SPEC §2); issue с отчётом по шаблону bug report ([#53](https://github.com/Leadaxe/sing-box-lx/issues/53), PR [#54](https://github.com/Leadaxe/sing-box-lx/pull/54) принят 2026-10-23)
- [x] 3. Тесты `urltest_rr_skip_lx_test.go` — падают до правки
- [x] 4. Поле `alive`, обход живых слотов в `pick`, `slotAlive`; строка в `URLTest.Start`; тесты пакета зелёные
- [x] 5. SPEC 019, FEATURE 007, `docs/configuration/outbound/urltest.md`, lx-config EN/RU, changelog
- [x] 6. IMPLEMENTATION_REPORT.md; статус I
- [x] 7. Проверка на роутере (A/B на бэкпорте v1.14.2-lx.11, 2026-10-22): простой сервера 1 — со стоковым ядром ~1/3 новых соединений падает весь простой, с патчем 0 отказов с ~20 с простоя до конца ([отчёт](IMPLEMENTATION_REPORT.md#на-устройстве))

## §3. Самовосстановление по отказу соединения

Этап 1 — отказ открытия, повтор, проверка узлов, замена, пределы, фактический узел в списке:

- [x] 8. Тесты `urltest_rr_heal_lx_test.go`, `urltest_rr_heal_probe_lx_test.go`, `conntest_actual_lx_test.go`, `tracker_actual_lx_test.go`
- [x] 9. Повтор через следующий живой слот: отмена клиентом, вложенный `urltest`, `selector`, звено цепочки, соединение теста (SPEC 139)
- [x] 10. Проверка узлов: CAS-флаг, пауза 15 с, проба без записи, лист вложенной группы, вердикт, третий узел, поиск замены как контроль
- [x] 11. Замена мёртвого узла под `checking`: `pool_tolerance: 0` пачками и `> 0` по history, замены нет, оживший узел не возвращается
- [x] 12. Заморозка: вердикт «своя сеть», пробуждение (`EventDeviceWake`/`EventNetworkWake`), сигнал и прогон `InterfaceUpdated`; обрыв идущей проверки
- [x] 13. Вызовы в `urltest.go` (`Start`, `InterfaceUpdated`, `DialContext`) — блоки `// lx: SPEC 138`
- [x] 14. Ячейка фактического узла: `adapter/inbound.go`, `route/route.go`, `tracker.go`/`tracker_lx.go`, повторное «новое соединение» демона
- [x] 15. Лог `Info`/`Debug` по SPEC §3.9

Этап 2 — первый байт, сдвиг ключа, счётчики и карантин:

- [x] 16. Тесты `urltest_rr_firstbyte_lx_test.go`, `urltest_rr_shift_lx_test.go`
- [x] 17. Обёртка первого байта: успех, закрытие без байта, закрытие и полузакрытие с нашей стороны; `NeedHandshakeForRead` + `ReaderReplaceable`
- [x] 18. Таблица сдвигов: 1024 записи, ключ + адресат, сдвиг в `pick`, пометка «проблема в адресе» и её сброс
- [x] 19. Счётчики слота, подтверждённые отказы, деление пополам при 256, порог по медиане, карантин в `slotAlive` и в выборе кандидатов, замена карантинного, снятие карантина
- [x] 20. Сброс пометок и карантина в `balancePool`, `rebuildPool` и по сигналу смены сети

Этап 3 — UDP:

- [x] 21. Тесты `urltest_rr_udp_lx_test.go`, `urltest_rr_udp_probe_lx_test.go`
- [x] 22. `ListenPacket`: повтор и проверка при ошибке открытия; обёртка потока, «пакеты без ответа» — только повод; соединённый UDP — обёртка в режиме UDP

Этап 4 — переделка по стенду и ревью (SPEC §3.15):

- [x] 23. Учёт незавершённых соединений по узлам (`urltest_rr_track_lx.go`), повод «висит дольше 5 с» при выборе слота, своя пауза 60 с у проверок по поводам
- [x] 24. Отказ ждёт вердикта по своему узлу (≤64 на узел, ≤60 с); «жив» — по началу соединения или пробы после отказа; сокращение «history моложе 30 с — жив» для вердиктов убрано, контроль пробуется всегда
- [x] 25. Вердикт «мёртв» отменяет открытия в полёте (в повтор) и закрывает ждущие первого байта
- [x] 26. Обёртка уходит с пути записи после первой записи клиента; конец потока клиента — через `route/conn.go`
- [x] 27. Замер задержки через группу вне самовосстановления (`common/urltest/measure_lx.go`); резолв в дозвоне узла без ячейки соединения
- [x] 28. Тесты по ревью: `urltest_rr_heal_review_lx_test.go`, `urltest_rr_shift_review_lx_test.go`, `urltest_rr_stand_lx_test.go`, `route/conn_halfclose_lx_test.go`, `common/dialer/actual_lx_test.go`

Этап 5 — наблюдаемость:

- [x] 29. `GetPool`: `quarantined`, `connections`, `confirmedFailures` по слоту; `shiftedKeys`, `markedKeys`, `frozen`, `frozenSeconds` по группе; libbox `GetPoolState`; `sing-box api group pool`
- [x] 30. Повторное «новое соединение» несёт приращение трафика тика

Проверка и документы:

- [x] 31. `gofmt`, `go vet`, тесты затронутых пакетов с `LX_TAGS` и без, `-race`; `common/trafficcontrol`, `common/urltest` добавлены в lx-ci
- [x] 32. Мутации и независимые ревью
- [x] 33. Стенд на NanoPi: сценарии этапов 1–2, замер обёртки первой версии
- [ ] 34. Повторный замер обёртки после ухода с пути записи и повторный прогон сценариев с зависшим узлом на NanoPi — цифры в IMPLEMENTATION_REPORT
- [x] 35. IMPLEMENTATION_REPORT (раздел §3), статус в шапке SPEC и в Roadmap, FEATURE 007, `docs/configuration/outbound/urltest.md`, `docs-lx/lx-config.md`, `docs-lx/lx-config.ru.md`, `docs-lx/lx-changelog.md`
- [ ] 36. Полевая проверка по issue #76 на настоящем роутере с подпиской: пул, один узел убит вручную, `interval` 10 мин — со sticky и без; результат в IMPLEMENTATION_REPORT и в issue

Вне ядра:

- [ ] 37. LxBox: переход на `GetPoolState`; показ карантина, счётчиков слотов, сдвинутых и помеченных ключей, заморозки; обновление цепочки соединения в профайлере по повторному «новому соединению»
- [ ] 38. Лаунчер: новые поля пула; старая ошибка в `core/backend_daemon_traffic.go` — событие обновления для соединения без записи у клиента не применяется
