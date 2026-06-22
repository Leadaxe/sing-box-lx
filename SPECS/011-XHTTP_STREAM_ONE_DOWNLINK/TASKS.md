# TASKS — 011-XHTTP_STREAM_ONE_DOWNLINK

## Подготовка
- [x] Ветка `lx-xhttp-streamone` от `lx` (имя `lx/xhttp` невозможно — ref `lx` уже лист)

## Фикс stream-one (главный баг)
- [x] `client.go` `requestURL`: ветка `len(elem)==0` → `fullPath = c.path` (голый путь, без trailing slash)
- [x] `conn.go` `dialStreamOne`: `c.requestURL(sessionID)` → `c.requestURL()` (голый `<path>`, без sessionId)
- [x] Не трогать stream-up/packet-up URL — sessionId там законен (покрыто `url_test.go`)

## mode=auto как Xray
- [x] `client.go` `Client`: добавлено поле `realityEnabled bool`
- [x] `reality_detect.go`: детект REALITY по имени типа tlsConfig (с разворачиванием KTLS), без импорта with_utls-типов → проставляется в `NewClient`
- [x] `client.go` `DialContext`: выделен `case modeAuto` — reality→`dialStreamOne`, иначе→`dialPacketUp`; `modePacketUp` отдельно
- [x] Юнит-тест детекта REALITY (`reality_detect_test.go`): reality/ktls(reality)→true; uTLS/STD/nil→false

## Изоляция / build-tags
- [x] Сборка `with_xhttp` БЕЗ `with_utls` компилируется (нет жёсткой связи тегов)
- [x] Сборка полного LX_TAGS — ок (`lx-build`)

## Конфиги / проверки
- [x] `lx-test/config/xhttp_reality.json` (`mode:stream-one`) — `sing-box check` зелёный
- [x] Новый `lx-test/config/xhttp_auto_reality.json` (`mode:auto`) — `check` зелёный
- [x] `go vet` (lx-теги) по `transport/v2rayxhttp` — чисто
- [x] `gofmt -l` по затронутым файлам — пусто
- [x] `go build ./...` без тегов — ок (xhttp отсутствует)

## Лайв — ⚠️ ОТЛОЖЕН (нет доступа к ноде; принято на синтетике, открытый TODO)
- [ ] Реальная Xray reality-XHTTP-нода: `mode:stream-one` — handshake + DNS + HTTPS + загрузка
- [ ] Та же нода `mode:auto` — идентичный результат (резолвится в stream-one)
- [ ] Регрессия: `mode:packet-up` на packet-up-ноде по-прежнему работает

## Закрытие
- [x] TASKS отражают факт (`[x]`)
- [x] IMPLEMENTATION_REPORT.md (корень бага, фикс, синтетика; лайв — honest caveat)
- [x] Обновить [002 IMPLEMENTATION_REPORT](../002-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md): stream-one fixed на синтетике (ссылка на 011)
- [x] Статус → `C` (шапка SPEC.md + Roadmap; принято на синтетике, лайв — открытый TODO в REPORT)
- [ ] (опц.) Закрыть GH-issue по жалобе — комментарий со ссылкой на коммит (lx-память)
