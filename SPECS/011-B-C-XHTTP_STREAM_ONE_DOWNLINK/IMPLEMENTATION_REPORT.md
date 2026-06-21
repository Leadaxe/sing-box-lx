# IMPLEMENTATION_REPORT — 011 XHTTP_STREAM_ONE_DOWNLINK

**Дата:** 2026-06-21 · **Статус:** Complete (синтетика) · **Лайв:** ⚠️ НЕ ПРОГОНЯЛСЯ — нет доступа к reality+xhttp ноде; приёмка на синтетике по решению владельца · **База:** ветка `lx` (`1.13.13-lx.13`)

> **Honest caveat.** Фикс принят на основании: построчной сверки с исходниками Xray (контракт stream-one = голый путь / пустой sessionId), независимого подтверждения [issue #5635](https://github.com/XTLS/Xray-core/issues/5635), совпадения с портом hiddify, и зелёной синтетики (юнит-тесты URL-layout + reality-детект, `check`, сборки). Это **не** заменяет лайв против реального Xray-сервера. Лайв остаётся открытым TODO — при первом доступе к reality+xhttp ноде прогнать сценарии из раздела «Дальше» и, если что-то не так, переоткрыть задачу.

## Проблема (из жалобы)

`vless + reality + xhttp`, `mode:auto`, `path:/` работает на стороннем (Xray-логика) ядре, **не работает** на нашем. Две связанные первопричины, сверены построчно с исходниками Xray-core `transport/internet/splithttp` (`main`) и подтверждены [issue #5635](https://github.com/XTLS/Xray-core/issues/5635) + референс-портом hiddify:

1. **stream-one слал `sessionId` в пути.** Xray-сервер (`hub.go`) роутит stream-one (двунаправленный) ТОЛЬКО при пустом sessionId. Наш `dialStreamOne` строил `<path>/<sessionId>` → сервер уходил в stream-down ветку → downlink не-VLESS → VLESS `unknown version`. (Зафиксировано как known bug ещё в 002.)
2. **`mode=auto` всегда → packet-up.** Xray: auto + Reality → stream-one. У нас auto лип к packet-up (т.к. stream-one был сломан).

## Что сделано

Изменения **только** в пакете `transport/v2rayxhttp` (новый код — ребейз-зона = ∅, upstream не тронут).

**Фикс stream-one (главный):**
- `client.go` `requestURL`: ветка `len(elem)==0` → голый `c.path` (без trailing slash). Избегает ловушки `strings.Join([],"/")==""` → `<path>/`.
- `conn.go` `dialStreamOne`: `c.requestURL(sessionID)` → `c.requestURL()` — голый `<path>`, sessionId на провод не идёт.
- stream-up/packet-up URL не тронуты (sessionId там законен).

**`mode=auto` как Xray:**
- `client.go` `Client`: поле `realityEnabled bool`, проставляется в `NewClient`.
- `client.go` `DialContext`: `case modeAuto` — Reality→`dialStreamOne`, иначе→`dialPacketUp`; `modePacketUp` отдельной веткой.
- `reality_detect.go` (новый): `tlsConfigIsReality` — детект Reality по **имени типа** через рефлексию (с разворачиванием kTLS-обёртки), **без** импорта `with_utls`-only типов. Это сохраняет изоляцию build-tag: `with_xhttp` без `with_utls` собирается.

**Тесты (новые):**
- `reality_detect_test.go`: reality→true, uTLS/STD→false, nil→false, kTLS(reality)→true, kTLS(utls)→false.
- `url_test.go`: stream-one→`/xhttp` (голый), stream-up/packet-up сохраняют sessionId/seq.

**Конфиги:**
- `lx-test/config/xhttp_auto_reality.json` (новый, `mode:auto`) для `check`.

**Файлы:**
- new: `transport/v2rayxhttp/reality_detect.go`, `reality_detect_test.go`, `url_test.go`, `lx-test/config/xhttp_auto_reality.json`
- edit (lx new-pkg): `transport/v2rayxhttp/client.go`, `transport/v2rayxhttp/conn.go`

## Проверки (DoD)

- ✅ `gofmt -l` затронутых файлов — пусто.
- ✅ `go test -tags with_xhttp,with_utls ./transport/v2rayxhttp/` — PASS (reality-детект + URL-layout).
- ✅ `go build -tags with_xhttp ./transport/v2rayxhttp/` **без** `with_utls` — компилируется (изоляция тега сохранена).
- ✅ `go vet -tags with_xhttp,with_utls ./transport/v2rayxhttp/` — чисто.
- ✅ `go build ./...` без тегов — ок (xhttp отсутствует, upstream-эквивалент).
- ✅ `make -f Makefile.lx lx-build` — бинарь собран.
- ✅ `./sing-box check -c lx-test/config/xhttp_reality.json` (stream-one) и `xhttp_auto_reality.json` (auto) — pass.
- ⚠️ **Лайв против реального Xray reality+xhttp сервера** — НЕ выполнен (нет доступа к ноде). По решению владельца задача принята на синтетике (папка → **C**), лайв остаётся открытым TODO (см. «Дальше»). При первом доступе к ноде — прогнать и при расхождении переоткрыть.

## Ребейз-зона

**∅.** Все изменения — в новых файлах пакета `v2rayxhttp` и его правках (новый код фичи). Upstream-файлы (`transport/v2ray/transport.go`, `constant`, `option/v2ray_transport.go`) — не тронуты.

## Остаточные риски

- **Матч Reality по имени типа** (`reality_detect.go`) хрупок к переименованию `RealityClientConfig` в sing/upstream. Митигировано юнит-тестом (двойники с теми же именами) — но тест останется зелёным при переименовании реального типа, а лайв сломается. При ребейзе сверять имя типа в `common/tls/reality_client.go`.
- **stream-up** по-прежнему не лайв-тестился (как и в 002).

## Дальше (открытый TODO — лайв)

При первом доступе к реальной Xray reality+xhttp ноде:
1. `make -f Makefile.lx lx-build`; собрать аутбаунд с параметрами рабочей подписки (server/uuid/sni/pbk/sid/path), локальный socks/mixed на `127.0.0.1:2080`.
2. `mode:stream-one` — `curl -x socks5h://127.0.0.1:2080 https://api.ipify.org` должен вернуть IP сервера (handshake+DNS+HTTPS+download).
3. `mode:auto` на той же ноде — идентичный результат (резолв в stream-one).
4. Регрессия: `mode:packet-up` на packet-up-ноде по-прежнему работает.
5. Если ок — оставить как есть (уже C). Если расхождение — переоткрыть задачу.

Слияние в `lx` и релиз `-lx.N` — на усмотрение владельца (фикс на ветке `lx-xhttp-streamone`, в `lx` не влит).
