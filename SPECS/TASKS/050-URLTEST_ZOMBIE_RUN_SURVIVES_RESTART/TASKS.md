# TASKS: 050 — URLTEST_ZOMBIE_RUN_SURVIVES_RESTART

## Стенд (первым — красный тест до любого фикса)

- [x] 1. `transport/v2rayxhttp/deadline_test.go`: `hangingTransport` — RoundTrip входит и не возвращается, тело запроса не читается (модель полуживого узла); узлы пользователя не нужны
- [x] 2. Red-тест воспроизвёл баг: `SetWriteDeadline` → `invalid argument`, отмена ctx не освобождает `Write`

## Уровень 1 — дедлайны XHTTP-conn

- [x] 3. `writeDeadline` + `readDeadline` в `conn.go` (`// lx:begin 050 deadline-support`)
- [x] 4. `streamConn`: рабочие `SetDeadline`/`SetWriteDeadline`/`SetReadDeadline`; `Read` ждёт `created` и `readDeadline.dead` одновременно (до привязки reader'а закрывать нечего)
- [x] 5. `splitConn`: то же; reader связан сразу, поэтому истёкший read-дедлайн просто закрывает его
- [x] 6. Таймеры гасятся в `Close()`; `-race` чистый
- [x] 7. `NeedAdditionalReadDeadline` разобран: потребители (`route/route.go:111`, `dns/transport/udp.go:174`, `common/stun`) через `deadline.NeedAdditionalReadDeadline` оборачивают conn в `deadline.NewConn`. Наш read-дедлайн **одноразовый** (рвёт ожидание, не восстанавливает conn для следующего чтения), поэтому флаг оставлен `true` — обёртка нужна, менять значение было бы заявкой на семантику, которой нет

## Уровень 2 — отмена ctx рвёт хендшейк

- [x] 8. `watchDialContext` + сторож в `dialStreamOne`; выход по `conn.created` — сторож не переживает диал
- [x] 9. `protocol/vless/lx_encryption.go`: `wrapEncryption(ctx, conn)` ставит дедлайн ctx на время `Handshake` и снимает после
- [x] 10. `protocol/vless/outbound.go`: **два** вызова (строки 190 и 237, не один), метка в lx-файле
- [x] 11. Тест `TestStreamOneDialCancelUnblocksWrite`: отмена диал-контекста освобождает висящий `Write`

**Решение по `dialStreamUp`:** сторож НЕ ставится. Его download-body уже открыт к моменту возврата (сервер ответил), а upload-`RoundTrip` живёт всё время соединения — сторож, привязанный к нему, рвал бы живые потоки (нарушение R4). Висящий upload там покрывается write-дедлайном.

## Уровень 3 — `Close()` отменяет прогон

- [x] 12. `URLTestGroup`: поле `cancel`, child-ctx в `NewURLTestGroup`, `g.cancel()` в `Close()` **до** раннего возврата по `ticker == nil`
- [x] 13. `testNodes`: `testCtx` от `batchCtx` вместо `g.ctx` — теперь и отмена группы, и контекст вызывающего доходят до задач
- [x] 14. Регрессия `protocol/group/urltest_cancel_lx_test.go`: 3 теста. `WaitGroup` в `Close` не понадобилась — отмены достаточно

## Верификация

- [x] 15. Green на стенде: все 4 теста XHTTP + 3 теста группы проходят
- [x] 16. `go build ./...` чистый; `go test -race` по `transport/v2rayxhttp`, `protocol/group`, `protocol/vless` — ok
- [x] 17. `gofmt -l` по всем затронутым файлам — пусто
- [x] 18. **Red/green проверен откатом**: со снятым `g.cancel()` тесты падают с «run survived Close() — this is the zombie that outlives box shutdown»; фикс возвращён
- [x] 19. R4 закрыт тестом `TestStreamOneCancelAfterStreamUpKeepsConnAlive`: отмена ctx после подъёма потока живое соединение не рвёт
- [x] 20. **Критерий №3 закрыт локально** — `lx-test/zombie`: полный `box.New` → `Start` → `Close` с реальным узлом `vless + xhttp(stream-one) + encryption` на молчащий listener, два цикла Stop → Start. Red/green на живом ядре: без фикса «cycle 1: 2 test goroutine(s) survived box.Close», с фиксом 0 сразу
- [ ] 21. Device-верификация на полевой подписке (эмулятор для этого не нужен — механизм воспроизведён в ядре; остаётся подтверждение на конфиге инцидента)

**Ключевая ловушка стенда (стоила первого ложно-зелёного):** нельзя отменять родительский ctx после `box.Close()`. На устройстве Stop → Start — это `Close()` плюс новый box, а процессный контекст живёт дальше; отмена родителя убирает горутины по пути, которого девайс не проходит, и стенд проходит **против** бага. Проверено: с откаченным фиксом вариант с `cancel()` даёт зелёный, вариант без него — красный.

- [ ] 22. Регрессия на живом XHTTP-узле: `stream-one` с REALITY и `packet-up` проходят URL-тест и держат трафик
- [x] 23. Строка в реестр `SPECS/FEATURES/004-HOTFIXES/FEATURE.md` + разбор записи + перекрёстные ссылки из фич 002/007/012
- [ ] 24. Релиз-rc: секция changelog + ноты

**Примечание к прогону:** `go test ./...` роняет `transport/wireguard` с «gVisor is not included in this build» — падение предсуществующее (воспроизводится на дереве без правок 050), лечится `-tags with_gvisor`, с которым пакет зелёный.
