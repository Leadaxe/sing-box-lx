# SPEC 077 — XHTTP: контракт dial-контекста после возврата из `DialContext`

**Фича:** [XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bugfix) — клиентский транспорт, fork-native пакет `transport/v2rayxhttp` |
| Статус | I (implemented) — код + юниты red/green 2026-09-02; остаток — полевой прогон на стенде лаунчера |
| Ветка | `lx` |
| База | `ff40cf98c` (v1.14.0-lx.29 + 3) |
| Связанные | [[SPECS/TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE]] (механизм 4 — форма ограничения подъёма стрима; переписан под эту задачу, старая форма в его HISTORY) · [[SPECS/TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART]] (происхождение снятого сторожа; дедлайны R1 живы, R2 сменил форму) · [[SPECS/TASKS/061-XHTTP_DIAL_DOWNLOAD_DEADLOCK]] (инвариант «дайл не ждёт download-ответ» — сохранён дословно) · [[SPECS/TASKS/059-XHTTP_XMUX]] (пул, через который идёт подъём; учёт слота) · [[SPECS/TASKS/076-XHTTP_XMUX_BREAKER]] (брекер; ожидание backoff теперь тоже внутри dial) |

Маркер в коде: `// lx: SPEC 077` (`transport/v2rayxhttp/conn.go`,
`client.go`; комментарий-пара в `transport/wireguard/client_bind.go`).

## 1. Проблема

Полевой случай (macOS, лаунчер в daemon-режиме, ядро `1.14.0-lx.29`,
2026-09-02). Конфиг штатный: `dns.final = google_udp` (UDP 8.8.8.8:53) с
`detour: proxy-out`, активный узел proxy-out — `vless + reality + xhttp`
в режиме `stream-one`. Симптомы:

- URL-тест любого `masque`- и `wireguard`-узла по доменному URL красный:
  `lookup www.gstatic.com: (exchange6: write request: context canceled |
  exchange4: context canceled)`. Тот же узел по IP-URL зелёный (23 мс,
  `warp=on`) — туннель исправен, не резолвится имя.
- Системный DNS через TUN (hijack-dns → тот же `google_udp`) не отвечает
  на некэшированные имена: `dig` — таймаут, поток `SubscribeDNSQueries`
  показывает `source: failed, error: write request: context canceled,
  dnsServer: google_udp`. Кэш `mDNSResponder` прикрывает известные имена,
  поэтому браузер выглядит живым.
- Не регрессия: `lx.27`, `lx.28-rc.4`, `lx.29` ведут себя одинаково. По
  коду — **никогда не работало**: до SPEC 072 запрос XHTTP ездил прямо на
  dial-контексте, и та же отмена убивала стрим целиком. Незамеченным это
  жило, потому что лаунчер по умолчанию резолвит через DoH, а `auto` +
  REALITY выбирает именно `stream-one` — то есть дефект бьёт дефолтный
  конфиг, как только `dns.final` становится `udp`/`tcp`/`tls` с detour.

Независимое полевое подтверждение — [LxBox #100](https://github.com/Leadaxe/LxBox/issues/100)
(подано 2026-09-02, до выхода lx.30): Android `lx.28-rc.1` и macOS `lx.27`,
`tcp`-DNS `127.0.0.1:5353` с detour на `vless+reality+xhttp mode=auto`;
83 `dns: exchange` / 0 `dns: exchanged`. Серверный tcpdump на `lo`: Xray
шлёт запрос в Unbound, сразу FIN, через ~125 мс на готовый ответ отвечает
RST — это отменённый нами upload-body (EOF → half-close бэкенда) и
сброшенный h2-стрим (ответу некуда лечь). Смена только клиентского режима
на `packet-up` чинила DNS. Ответ и регрессионный тест — в §5.

Изоляция на стенде (одно и то же XHTTP-плечо):

| DNS-сервер через XHTTP `stream-one` | Результат |
|---|---|
| `udp` | падает с первого запроса |
| `tcp` | первый запрос проходит (probe reuse идёт мимо пула — `exchangeSingle`), второй и далее падают |
| `https` (DoH) | работает — свой HTTP-клиент, пула нет |
| сырой UDP через socks5 UDP-associate (не DNS-транспорт) | работает — честный NXDOMAIN от 8.8.8.8 |

То есть XHTTP как транспорт исправен, UDP через него ходит. Ломалась
связка двух штатных механизмов:

1. **Пул DNS-транспорта** ([dns/transport/conn_pool.go](../../../dns/transport/conn_pool.go),
   `dialAndInstall`, upstream): диалит с контекстом, производным от
   контекста первого запроса, и **сразу после возврата `dial`** зовёт
   `dialCancel(nil)` — соединение общее на всех (`ConnPoolSingle`) и не
   может жить контекстом случайного первого запроса; дальше им владеет
   `sharedCtx` пула. Это буквальный контракт `net.Dialer`: после
   успешного возврата `DialContext` отмена контекста на соединение не
   влияет. Все транспорты ядра, кроме нашего, на это рассчитывали.
2. **XHTTP `stream-one`/`stream-up`** (было: `dialStreamOne`/`dialStreamUp`,
   сторож `watchDialContext` — SPEC 050, переставлен в SPEC 072 механизм 4):
   `DialContext` возвращал conn **до** подъёма HTTP-стрима и продолжал
   слушать dial-контекст до `created` (пришёл download-ответ). Отмена до
   этого момента рвала upload-pipe с `ctx.Err()` и отменяла `connCtx`. В
   `stream-one` download-ответ приходит только после первой записи, так
   что «до `created`» на практике означало «всегда после возврата».

Итог: пул отменял контекст через микросекунды после возврата, сторож
трактовал это как «клиент передумал», первая же запись DNS-запроса
получала `context canceled`, retry пула повторял то же самое.
Первопричина в проводе не видна: ошибка — наш собственный `ctx.Err()`.

Сторож ставился под реальный случай (SPEC 072, hole C): WG-bind диалит
detour под `WithTimeout(15 s)` и синхронно пишет VLESS-заголовок в pipe,
который никто не читает, пока `RoundTrip` не поднял стрим на
полуживом узле; без сторожа запись держала `connAccess` 38 минут. Этот
случай обязан остаться закрытым — и остался, см. §2.

## 2. Контракт

Конфиг не меняется. Меняется одно обещание транспорта, и оно приводится
к общему для Go:

**`DialContext` возвращает conn только с поднятым upload-стримом.**
«Поднят» = HTTP-слой принял тело запроса (вызвал `Read` на pipe:
pooled-соединение установлено, стрим открыт, записи будут вычитываться).
Download-ответ при этом **не** ждётся — инвариант SPEC 061 сохранён
дословно (`TestDialDoesNotBlockOnDownloadResponse` зелёный без правок).

**После возврата dial-контекст на conn не влияет.** Ни отмена, ни
дедлайн. Жизнь conn ограничивают только `Close`, `fail` (SPEC 072
механизм 3), дедлайны записи/чтения (SPEC 050 R1) и `connCtx`.

**Пока `DialContext` не вернулся, dial-контекст ограничивает всё
ожидание** — backoff брекера (076), TCP+TLS(+REALITY) до узла, слот стрима
в пуле, отправку заголовков. Конец dial-контекста до подъёма — ошибка
контекста из `DialContext`, pipe разорван, `connCtx` отменён (висящий
`RoundTrip` умирает, не утекает), слот пула возвращён. Провал подъёма до
принятия тела (ошибка `RoundTrip`, не-200) — ошибка **с причиной** из
`DialContext`, а не `context canceled`.

**Что это закрывает и что нет — честно.** Случай 072-C по записи дампа
(g275: pooled-соединение уронило `RoundTrip`, не приняв тело) закрывается
штатно: ошибка из dial, запись не блокируется. Полуживой узел на холодном
пуле (TCP/TLS-хендшейк не отвечает, слот не даётся) — ошибка из dial по
дедлайну вызывающего. **Не закрывается этим контрактом** тёплый h2-conn к
молча умершему пиру: заголовки уходят в буфер ядра, http2 вызывает `Read`
тела, dial успешен мгновенно, а смерть видна лишь по отсутствию ответа.
До 077 такой узел рубил 15-секундный dial-дедлайн WG-bind через сторож;
после 077 его ловят `ReadIdleTimeout` (`xmux.h_keep_alive_period`; 0 по
умолчанию = пингов нет), give-up SPEC 041 или read-deadline вызывающего.
Это сознательная цена (см. §4): общая для всех транспортов Go, а сторож
снаружи dial в любой форме ломает пул.

Следствие для потребителей: `stream-one`/`stream-up` больше не
«мгновенный» dial — на холодном пуле он включает хендшейк, как у
`ws`/`grpc`/`httpupgrade`. Для WG-bind суммарное время до первого байта
не меняется: раньше та же пауза сидела в первой записи.

## 3. Реализация

Только `transport/v2rayxhttp/conn.go`, `client.go` и тесты пакета;
комментарий-пара в `transport/wireguard/client_bind.go` (код bind не
менялся). Upstream-файлы не тронуты.

- **Приёмный reader `adoptedBody`** — обёртка над `*io.PipeReader`,
  передаваемая в `newRequest`: первый **вызов** `Read` закрывает канал
  `adopted` ровно один раз (`sync.Once`). `io.Pipe` сохранён — семантика
  записи, `writeDeadline` (050) и `CloseWithError` из `fail` (072) не
  менялись.
- **`awaitRaise`** — общее ожидание для `dialStreamOne`/`dialStreamUp`:
  `adopted` → conn; `created` → conn или ошибка `readerErr` (ответ или
  провал пришёл раньше принятия тела; статус проверяется существующим
  кодом); `uploadFailed` (только stream-up: провал upload-`RoundTrip` до
  принятия тела) → ошибка; `ctx.Done()` → ошибка контекста, **с повторной
  неблокирующей проверкой `adopted`/`created`** — подъём, совпавший с
  отменой, побеждает, иначе вызывающий передиалит тот же узел.
- **`abortRaise`** — уборка на ошибочном выходе из dial: `CloseWithError`
  на читающей половине pipe (причина, не голый `ErrClosedPipe` — правило
  050), `connCancel()`, `release.release()`. `created` не трогается —
  им владеет только горутина `RoundTrip`, двойного `close` нет; все три
  шага идемпотентны, потому что на пути провала `fail()` уже сделал их.
- **Единый хэндл релиза слота xmux.** `DialContext` создаёт
  `newXmuxRelease` один раз и передаёт в `dialMode` → `dial*`; conn'ы
  получают его же. Ловушка, которой не было в исходной постановке:
  `fail()` уже релизит слот, а `DialContext` на ошибке `dialMode` делал
  `addOpenUsage(-1)` ещё раз — до 077 dial не мог упасть после создания
  conn, теперь может, и счётчик уехал бы в минус (retired-соединение
  никогда не закрылось бы). Once-хэндл на весь dial закрывает это
  (`TestStreamDialSlotReleasedOnceOnRaiseFailure`).
- **Сторож снят.** `watchDialContext` и оба вызова удалены, `stopGuard`
  в горутинах `RoundTrip` — тоже. `fail` и `Close` — единственные точки
  отмены `connCtx` после возврата.
- **`dialPacketUp` не меняется** (кроме сигнатуры под хэндл релиза): pipe
  нет, посты идут на своём `postCtx` от `connCtx`; DNS через `packet-up`
  работал и раньше.
- **Тесты.** `deadline_test.go`: сценарий «отмена dial-контекста до
  подъёма» переписан под контракт (`TestStreamOneDialCancelBeforeRaiseFailsDial`:
  ошибка `context.Canceled` из dial, слот возвращён), write-deadline-тест
  переведён на транспорт «принял тело и замолчал» (`stallingTransport` —
  пост-077 форма полуживого узла для R1). `raise_failure_test.go`: четыре
  теста провала raise переписаны на «валит dial с причиной»
  (`...DialFailsOn...`), не подогнаны. Новый `dial_ctx_contract_test.go`
  — четыре теста контракта (§5). `dial_deadlock_test.go` не тронут.
- **Документация владельца механизма (§3.2 конституции).** SPEC 072
  §«Механизм 4», Related и Verification переписаны под актуальное
  состояние; старая форма и обоснование смены — `072/HISTORY.md`. SPEC 050
  R2 — пометка о смене формы. HOTFIXES P12 и строки реестра 050/072,
  FEATURE 002, Roadmap — обновлены.

Ловушки:

- Принятие тела фиксировать по **вызову** `Read`, не по возврату:
  первый `Read` HTTP-слоя блокируется на pipe до первой записи
  вызывающего — ждать его возврата значит вернуть дедлок 061 в новой
  форме.
- В пакете единственный транспорт — `http2.Transport` (h2c и h2 поверх
  TLS; h1/h3 здесь нет). x/net http2 вызывает `Read` тела сразу после
  отправки заголовков, до ответа (`clientStream.writeRequest` →
  `writeRequestBody`), и только `Expect: 100-continue` отложил бы это —
  мы его не ставим. Если транспорт ждёт свободный стрим
  (`awaitOpenSlotForStreamLocked`, `xmux.max_concurrency`), ожидание
  законно ограничено dial-контекстом.
- Пул `xmux` (059): `getContext(ctx)` уже ждёт backoff брекера (076) на
  dial-контексте — это ожидание теперь тоже «внутри dial», ничего
  дополнительно не нужно.
- `created` до `adopted` — не ошибка: conn отдаётся, статус ответа
  обрабатывается существующим кодом (stream-up: download-GET ответил
  раньше, чем http2 добрался до тела POST).
- Тестовые заглушки, отвечающие 200 без чтения тела, отдают conn через
  `created`; заглушка, которая молчит и не читает, паркует dial навсегда —
  в тестах такой dial запускается в горутине с бюджетом (`dialUnderTest`).

## 4. Сознательно не сделано

- **Пул DNS-транспорта не трогается.** Апстримный код, контракт
  соблюдает; правка там — merge-долг ради нашего дефекта.
- **Никакого различения причин отмены** (`Canceled` vs
  `DeadlineExceeded`, cause): пул отменяет через `dialCancel(nil)`,
  WG-bind — дедлайном или смертью поколения; по причине не разделить,
  и контракт `net.Dialer` этого не требует.
- **Никакого «сторож только при записи в полёте» / отложенного дедлайна
  после отмены.** Оставляет окно между возвратом и первой записью,
  удваивает бюджет WG-bind до 30 с и — главное — пул отменяет контекст
  раньше любого подъёма, так что сторож снаружи dial в любой форме
  ломает его. Ожидание внутри dial закрывает всё одним правилом.
- **Никакого «raise-таймаута» после принятия тела** (adopted → `created`
  за `C.TCPTimeout`, иначе `fail`). Закрыл бы тёплый мёртвый conn из §2,
  но это новый механизм со своей ловушкой (протоколы, где клиент долго
  молчит после dial, получили бы ложный обрыв) — решение владельца, не
  часть этого контракта. Штатный рычаг уже есть: `xmux.h_keep_alive_period`
  (h2-пинг `ReadIdleTimeout`).
- **Обход на стороне потребителя** (лаунчер: `dns.final` → DoH,
  `domain_resolver` у masque/wg, IP-URL для теста) — паллиатив, маскирует
  класс: любой пул поверх `stream-one`/`stream-up` остаётся сломанным.
  Лаунчерная задача отдельно, здесь не заменяет фикс.

## 5. Верификация

Юнит, red/green (красный прогон снят на базе `ff40cf98c` с теми же
тестами, 2026-09-02):

- `TestStreamDialCancelAfterReturnKeepsConnAlive` (stream-one, stream-up;
  живой h2c echo-сервер, `WithCancelCause` + `cancel(nil)` сразу после
  возврата — форма пула) — red: «Write after the dial context was
  cancelled: context canceled»; green.
- `TestStreamDialWaitsForAdoption` (оба режима; транспорт принимает тело
  через 100 мс, download-GET отвечает только после принятия — порядок
  Xray/061) — red: «DialContext returned before the upload body was
  adopted»; green.
- `TestStreamDialHalfAliveNodeFailsWithinDeadline` (оба режима; `RoundTrip`
  висит до смерти контекста, тело не читает; dial под `WithTimeout(300ms)`)
  — red: «DialContext succeeded on a raise that failed»; green: ошибка
  `context.DeadlineExceeded` в бюджете, оба pending `RoundTrip` (GET+POST
  в stream-up) отменены, `openUsage` пула = 0.
- `TestStreamDialSlotReleasedOnceOnRaiseFailure` (три провальных dial
  подряд) — red: «DialContext succeeded on a raise that failed»; green:
  `openUsage` ровно 0 (не отрицательный — ловушка double-release).
- `TestStreamOneShortExchangesAfterDialCancel` (форма LxBox #100, добавлен
  2026-09-05: DNS-подобный h2c-сервер отвечает 76 байтами через 2 мс после
  того, как дочитал 60-байтный запрос; 100 dial'ов по одному обмену и один
  dial на 100 обменов, `cancel(nil)` сразу после возврата) — red на базе
  `f05fc7063^`: «exchange 0: write: context canceled» в обоих подтестах;
  green: 100/100.
- Переписанные: `TestStreamOneDialCancelBeforeRaiseFailsDial`,
  `TestStreamOneDialFailsOnRoundTripError` / `...OnBadStatus`,
  `TestStreamUpDialFailsOnDownloadError` / `...OnUploadError` — зелёные;
  061: `TestDialDoesNotBlockOnDownloadResponse` зелёный **без правок**;
  072: `TestStreamOneConnSurvivesDialContextExpiry`, packet-up-тесты —
  зелёные без правок.
- Сюиты: `go test ./transport/v2rayxhttp/ -race`,
  `go test ./transport/wireguard/ -race -tags with_gvisor`,
  `go test ./protocol/wireguard/ ./dns/transport/ -race` — зелёные;
  `go build` потребителей под полным `LX_TAGS` — ок; `gofmt`/`go vet`
  чистые.

Поле (macOS-стенд лаунчера, тот же конфиг) — **остаток**:

- `URLTestOutbound` masque-узла по доменному URL → задержка, не ошибка.
- `dig` случайного поддомена через TUN при `dns.final = google_udp` с
  detour на XHTTP-узел → ответ; `SubscribeDNSQueries` без `failed`.
- `tcp`-DNS через тот же узел: три запроса подряд — все NOERROR (второй
  и третий идут через пул).
- WG-узел через XHTTP-detour: реконнект-цикла нет, время до первого
  байта прежнее.

## 6. Условия снятия

Нет: `transport/v2rayxhttp` существует только в форке, контракт
`net.Dialer` для его dial — постоянное обещание пакета (как механизмы
3–4 SPEC 072). Инварианты пришпилены `dial_ctx_contract_test.go`.
