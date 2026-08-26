# SPEC 076 — XHTTP XMUX: circuit breaker на шторм переподключений

**Фича:** [XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bugfix) — клиентский транспорт (CONSTITUTION §3.6) |
| Статус | I (implemented) — код + тесты + сборка; ждёт полевой проверки репортёром issue #14 |
| Ветка | `lx` |
| Связанные | [[SPECS/TASKS/059-XHTTP_XMUX]] (пул соединений — точка врезки) · [[SPECS/TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE]] (жизненный цикл conn, `fail()`) · issue [Leadaxe/sing-box-lx#14](https://github.com/Leadaxe/sing-box-lx/issues/14) |

## 1. Проблема

Полевой случай (issue #14, device-verified): XHTTP `packet-up` через CDN,
который рубит HTTP/2-стримы `INTERNAL_ERROR` при выгрузке. Каждый сброс
убивает VLESS-соединение → приложение немедленно переоткрывает → новый
стрим → снова сброс. Цикл dial→reset→dial крутится на скорости канала и
вешает оба ядра ARM64-роутера на 100% **до перезапуска ядра**.

CPU-профиль подтвердил реконструкцию: 94% времени `ReadFrame` — чтение
9-байтных **заголовков** кадров (шторм служебных RST_STREAM/SETTINGS, а
не данные), плюс 17% в `tls.handshakeContext` — пересоздание соединений.

У пула (SPEC 059) нет ни одного механизма, гасящего этот цикл:

- `evictCause` считает соединение мёртвым только если **мы сами** его
  закрыли (`IsClosed`). Транспорт, который стабильно отдаёт ошибки на
  каждый запрос, остаётся в пуле и раздаётся новым стримам.
- Провал dial'а ничем не тормозится: `fail()` освобождает пул, следующая
  попытка идёт сразу.

Ошибки — вина CDN. Незатухающий 100% CPU от чужих ошибок — наша.

Побочная находка из тех же логов: на upload-POST при GOAWAY —
`cannot retry err [...] after Request.Body was written; define
Request.GetBody to avoid this error`. Payload у нас уже лежит копией в
`[]byte`, но `GetBody` не выставлен, и http2 не может прозрачно
повторить запрос на новом соединении — сессия падает зря.

## 2. Контракт

Конфиг не меняется. Поведение — только под отказом:

**Breaker (per pooled connection).** На каждом `xmuxClient` — счётчик
подряд идущих отказов стрима. Отказ = любое из:

- `RoundTrip` вернул ошибку;
- ответ пришёл со статусом ≠ 200 (все точки XHTTP ждут ровно 200);
- тело download-стрима умерло **удалённой** ошибкой (не `io.EOF`, не
  наш локальный Close/deadline).

Успех (сброс счётчика в 0) = реальные данные, не заголовки:

- upload-POST завершился 200 и тело дочитано (`sendPacket`);
- первый успешный `Read` download-тела на стриме.

Счётчик достиг порога (**3**) → соединение помечается `failing` →
`evictCause` его выкидывает на ближайшем `get()`, teardown отложенный
(живые стримы дорабатывают, как при любой эвикции 059).

**Backoff (per manager).** Каждый trip брекера удваивает задержку на
открытие **нового** транспорта: 100мс → 200 → 400 → … → потолок **3с**
(решение владельца, issue-тред 2026-08-26). Задержка действует только
когда пул пуст и dial'у нужен новый транспорт: живое соединение из пула
выдаётся без ожидания. Любой успех (по определению выше) сбрасывает и
счётчики, и backoff. Ожидание уважает контекст dial'а.

**GetBody (packet-up upload).** `applyUplinkData` в body-placement
выставляет `request.GetBody`, возвращающий свежий reader над тем же
payload — http2 сам молча ретраит POST после graceful GOAWAY.

Итог под полным отказом: вместо сотен циклов в секунду — одна связка
«транспорт + до 3 запросов» в ~3 секунды; при оживлении сервера всё
восстанавливается первым же успешным стримом, без перезапуска ядра.

## 3. Реализация

[transport/v2rayxhttp/xmux.go](../../../transport/v2rayxhttp/xmux.go):

- `xmuxClient`: `consecFails atomic.Int32`, `failing atomic.Bool`,
  backlink `manager`. `noteFailure()` инкрементирует; на пороге ставит
  `failing` и зовёт `manager.noteBreakerTrip()`. `noteSuccess()`
  обнуляет счётчик и снимает backoff менеджера (дёшево: атомарный гейт
  `backoffArmed`, мьютекс берётся только когда backoff реально взведён).
- Классификация централизована в `xmuxClient.roundTrip`: ошибка или
  статус ≠ 200 → `noteFailure`. Точки вызова не трогаются.
- `evictCause`: новая причина `"failing"`.
- `xmuxManager`: `backoffDelay`/`blockedUntil` под `m.access`;
  `noteBreakerTrip` удваивает с клампом [100мс, 3с]. `get()` возвращает
  `(client, wait)`: пул пуст и окно backoff открыто → `(nil, остаток)`.
  `getContext(ctx)` — цикл ожидания, ctx-aware.
- Пороги/тайминги — переменные пакета (тестовый шов, как
  `packetUpPostTimeout`): `xmuxBreakerThreshold = 3`,
  `xmuxBackoffInitial = 100ms`, `xmuxBackoffCap = 3s`.

[transport/v2rayxhttp/conn.go](../../../transport/v2rayxhttp/conn.go):

- `streamConn`/`splitConn`/`packetConn`: флаг `localClosed atomic.Bool`
  (ставится в `Close()` и в expire read-deadline — ДО закрытия reader'а,
  чтобы разбуженный `Read` уже видел его). В `Read`: `err == nil` →
  разовый `noteSuccess` (гейт-поле, reader один per conn); удалённая
  ошибка → `noteFailure`.
- `sendPacket`: после `drainAndClose` — `noteSuccess`.

[transport/v2rayxhttp/client.go](../../../transport/v2rayxhttp/client.go)
/ `DialContext`: `c.xmux.get()` → `c.xmux.getContext(ctx)`.

**Попутная находка (баг SPEC 059, независимый от брекера).** `newSplitConn`
принимал параметр `xmux *xmuxRelease` и **не записывал его в структуру** —
поле оставалось nil. Значит `c.xmux.release()` в `splitConn.Close()` и
`splitConn.fail()` был nil-no-op: stream-up **никогда** не возвращал
pooled-соединение, `openUsage` рос монотонно, и соединение, снятое с
ротации, не сносилось никогда (отложенный teardown ждёт `openUsage <= 0`).
На `max_concurrency` это ещё и вытесняло переиспользование: пул считал
занятыми давно мёртвые стримы. Исправлено записью поля; брекеру то же
поле нужно для `noteRead`, поэтому находка всплыла здесь.

[transport/v2rayxhttp/meta.go](../../../transport/v2rayxhttp/meta.go)
/ `applyUplinkData`: `request.GetBody`.

## 4. Сознательно не сделано

- **Backoff на открытие дополнительных соединений при непустом пуле**
  (`max_connections`-ветка, все-на-потолке-concurrency-ветка): в шторме
  пул пуст — эти ветки не участвуют; блокировать их значит стопорить
  здоровые dial'ы из-за хвостовых ошибок доживающих стримов.
- **Отличение причин отказа** (GOAWAY vs RST vs timeout): брекеру всё
  равно — любой стабильно умирающий путь должен затухать одинаково.
- **Health-check / полуоткрытое состояние**: первый dial после окна и
  есть проба; отдельный пробник — лишняя сущность.

## 5. Верификация

Юнит (fake-транспорты, как в `xmux_test.go`; тайминги через переменные):

- 3 подряд ошибки roundTrip → `evictCause() == "failing"`, соединение
  эвиктится, `get()` после этого отдаёт новый транспорт.
- Успех между ошибками сбрасывает счётчик — trip не происходит.
- Trip №1/№2/№3 → окно 100/200/400мс; потолок 3с; успех сбрасывает.
- `getContext` в окне backoff ждёт; отмена ctx возвращает ошибку сразу.
- Read-классификация: удалённая ошибка тела = failure; `io.EOF` и
  локальный Close/deadline = нейтрально; первый успешный Read = success.
- `sendPacket`-запрос несёт `GetBody`, повторный вызов отдаёт тот же
  payload с нулевого смещения.

Поле: сценарий issue #14 (CDN, рубящий upload-стримы) — ЦП не залипает
на 100%, после закрытия speedtest нагрузка спадает сама. Ждёт прогона
репортёром.
