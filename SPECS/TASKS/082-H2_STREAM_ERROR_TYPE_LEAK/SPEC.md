# SPEC 082 — Утечка типа `http2.StreamError` из транспортных conn'ов: спин readLoop у потребителя

**Фича:** [XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bugfix) — клиентские транспорты (CONSTITUTION §3.6) |
| Статус | D (done, device-verified) — код + тесты + сборка; коммиты `9d96cef5e` (XHTTP), п.3 — `v2rayhttp`/`v2raygrpclite`; репортёр issue #14 подтвердил на `lx.35` (2026-09-06: readLoop в `runnable` нет, CPU в норме), тикет закрыт 2026-09-08. Апстрим закрыл класс в `sing` v0.9.6 (`baderror.WrapH2`, sing-box `18b031701`, синк [095](../095-UPSTREAM_SYNC_1_14_1_PLUS_34/SPEC.md)): обёртки в `v2rayhttp`/`v2raygrpclite` сняты, файлы равны апстриму; `HideStreamError` в форк-нативном `v2rayxhttp` остаётся |
| Ветка | `lx` |
| Связанные | [[SPECS/TASKS/076-XHTTP_XMUX_BREAKER]] (предыдущая, неполная реконструкция того же тикета) · [[SPECS/TASKS/077-XHTTP_DIAL_CTX_CONTRACT]] · issue [Leadaxe/sing-box-lx#14](https://github.com/Leadaxe/sing-box-lx/issues/14) |

## 1. Проблема

Issue #14: XHTTP через CDN, который рубит upload-стримы `INTERNAL_ERROR`.
На выгрузке процессор ARM64-роутера уходит в 100% и **не отпускает до
перезапуска ядра**. SPEC 076 (breaker + backoff + `GetBody`) и SPEC 077
(контракт dial-ctx) симптом не сняли — репортёр прислал профиль с `lx.30`.

Реконструкция в SPEC 076 §1 («шторм служебных кадров, цикл
dial→reset→dial») оказалась неполной: она объясняла нагрузку *во время*
сбросов, но не «висит после, пока не перезапустишь». Профиль с `lx.30`
показал, что в момент «висит» кадров от CDN нет вовсе.

## 2. Механизм (каждый шаг сверен по исходникам x/net v0.57.0 и crypto/tls)

1. CDN сбрасывает download-стрим. `transportResponseBody.Read` отдаёт
   **значение** `http2.StreamError{ID, INTERNAL_ERROR, errFromPeer}`.
   Наши `packetConn.Read` / `streamConn.Read` / `splitConn.Read`
   (и `readerErr` из провалившегося raise), а выше — `vless.Conn.Read`,
   возвращали её **как есть**. Строка в логе репортёра
   `connection download closed: stream error: stream ID 21; INTERNAL_ERROR;
   received from peer` — ровно эта ошибка, дошедшая до relay.
2. Для relay это безвредно. Но если conn аутбаунда читает **h2-клиент
   x/net поверх crypto/tls** — DoH с `detour`, rule-set `download_detour`,
   chained outbound — цепочка фатальна:
   - `crypto/tls.readRecordOrCCS`: ошибка reader'а, не являющаяся
     `net.Error`, **залипает** в `c.in.err` и отдаётся на каждый Read
     до обращения к сети (`conn.go:637` в go1.25/1.26);
   - `http2.Framer.ReadFrame` пробрасывает ошибку reader'а без изменений;
   - `clientConnReadLoop.run` (`transport.go:1886`): `err.(StreamError)`
     → `streamByID(чужой ID)` → nil → `continue`. Ветка рассчитана на
     ошибки *собственного* фреймера, отличить чужую она не может.
3. Итог — бесконечный цикл с нулём сисколлов. Его ничто не останавливает:
   `run()` не проверяет `cc.closed`, закрытие нижнего conn'а не помогает
   (залипшая ошибка отдаётся раньше), `CloseIdleConnections` не при чём.

**Улики из профиля `lx.30`** (cpu 30 с = 191% CPU; goroutines в пике):
`ReadFrameForHeader` 0.10 с из 36.58 с в `ReadFrame` — тел кадров нет;
`readRecordOrCCS` 1.13 с flat **без детей** — ни `readFromUntil`, ни
`decrypt`; две readLoop-горутины в `runnable` на `transport.go:1886`;
крутящиеся conn'ы — на `crypto/tls`, живой трафик к CDN — на `utls`.
`runtime._panic.*` в профиле = deferreturn-оверхед быстрого пути
`handshakeContext` (хендшейки завершены), не паники.

Тот же класс утечки — в `v2rayhttp.HTTP2Conn` и `v2raygrpclite.GunConn`:
`baderror.WrapH2` переводит в `net.ErrClosed` только `CANCEL` и локальные
закрытия, `INTERNAL_ERROR`/`PROTOCOL` проходят насквозь.

## 3. Контракт

Конфиг и провод не меняются. Правило одно: **тип `http2.StreamError` не
выходит за границу транспортного conn'а.**

- `common/badh2.HideStreamError(err)`: `StreamError` (голый или обёрнутый,
  проверка `errors.As`) → `*RemoteStreamError` с тем же текстом и
  **намеренно без `Unwrap`** — `errors.As` у потребителя тоже не должен
  до него добраться. `io.EOF`, `nil`, прочие ошибки — без изменений.
- Точки: XHTTP — три `Read` (после `noteRead`: брейкер SPEC 076
  классифицирует сырую ошибку) и три `setupReader` (`readerErr`);
  `v2rayhttp.HTTP2Conn` — `Read` (поверх `WrapH2`) и `Setup`;
  `v2raygrpclite.GunConn` — `Read` (поверх `WrapH2`) и `setup`.
- Текст в логах пользователей не меняется — жалобы по нему остаются
  сопоставимыми.

Не делаем: `Write`-путь (ошибка записи в x/net не зацикливается — уходит
в `writeRequest` и абортит стрим); защиту на стороне потребителя (обёртка
raw-conn'а до `aTLS.ClientHandshake` в `common/tls`) — отложено, п.3
закрывает всех производителей в ядре; форк/патч x/net — в v0.58.0 `run()`
тот же, апстрим-issue не заводим ([[upstream-banned-no-contributions]]).

## 4. Тесты

- `common/badh2`: юнит на обёртку (текст, отсутствие типа, обёрнутый
  через `E.Cause`, passthrough `io.EOF`/`nil`/прочих).
- `transport/v2rayxhttp/stream_error_test.go` —
  **`TestStreamErrorDoesNotSpinConsumerReadLoop`**: механизм §2 в
  миниатюре — `http2.Transport` (потребитель) поверх нашего `streamConn`,
  тело которого отдаёт `StreamError`. С выключенным фиксом: 13 185 319
  чтений тела за 3 с, `RoundTrip` умирает только по контексту. С фиксом:
  `RoundTrip` падает сразу, ≤16 чтений. Полевой спин воспроизведён на Mac.
- `v2rayhttp`/`v2raygrpclite`: чтение тела и провал late-setup не отдают
  `StreamError`.

## 5. Полевая проверка и остаток

- Критерий на `lx.35`: тот же speedtest, в пике
  `goroutine?debug=2` — ни одной `clientConnReadLoop).run` в `runnable`.
- У репортёра запрошен конфиг: какой потребитель шёл сквозь XHTTP-аутбаунд
  (DoH `detour` / rule-set `download_detour` / chain). Не блокирует.
- Вариант VerentiX (hard-close `ClientConn` на `INTERNAL_ERROR`) гасил
  спин попутно, на стороне потребителя; отклонён как не тот слой.

## Файлы

- `common/badh2/stream_error.go`, `common/badh2/stream_error_test.go`
- `transport/v2rayxhttp/conn.go` (маркеры `lx: SPEC 082`),
  `transport/v2rayxhttp/stream_error_test.go`
- `transport/v2rayhttp/conn.go`, `transport/v2rayhttp/stream_error_test.go`
- `transport/v2raygrpclite/conn.go`, `transport/v2raygrpclite/stream_error_test.go`
