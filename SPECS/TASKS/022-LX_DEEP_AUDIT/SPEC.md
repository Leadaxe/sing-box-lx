# SPEC 022 — Глубокий аудит LX-дельты форка

**Фича:** [AUDITS](../../FEATURES/AUDITS/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | Q (question/исследование) |
| Статус | C (complete — все действимые находки исправлены; ветка `lx-spec022-audit-fixes`) |

> **Ремедиация (2026-07-02, ветка `lx-spec022-audit-fixes`).** Исправлены и
> проверены (сборка с тегами и без, `go vet -unsafeptr=false`, тесты затронутых
> пакетов, race-тесты для #2/#7): **P0** #1 · **P1** #2 · **P2** #3, #4, #5, #6,
> #7, #8, #22 · **P3** #9, #10, #11, #13, #14, #15, #16, #20, #21, #23, #24, #25,
> #26, #27. **#19** — тест SPEC 013 сохранён как единственное покрытие (upstream
> своего не поставляет), SPEC 013 обновлён. **Сознательно НЕ трогали** (по
> правилам форка, см. рекомендацию ниже): **#12** и **#17** (санкционированные
> компромиссы), **#18** (тег-гейтинг раздул бы ребейз upstream-производного
> файла). Итог: 24 из 27 находок исправлены, 3 пропущены осознанно.

**Суть.** Полный аудит всего LX-кода форка (дельта над upstream-базой
`b3c1634d` = `v1.14.0-alpha.35`) по десяти осям: ошибки логики, точки
оптимизации, мёртвый код, устаревшие решения, невынесенные повторы,
нарушения слоёв, неточности, избыточные/неверные комментарии, пробелы в
документации, влияние на энергопотребление/стабильность.

**Метод.** Многоагентный аудит: LX-корпус (~70 файлов, ~14.5k добавленных
строк) разбит на 11 доменов; каждый домен прошёл глубокий аудит (high-effort),
затем **каждая** находка — адверсариальную верификацию (скептик читает реальный
код, пытается опровергнуть, пересматривает severity). Топ-находки (high/medium)
перепроверены вручную по первоисточнику. Оценка велась **сквозь принципы
`CONSTITUTION.md`**: «тонкий дифф > красивая абстракция», build-tag-изоляция,
client-only — поэтому предложения «вынести в общий слой» намеренно НЕ считались
дефектами, если они расширяют зону ребейз-конфликта с upstream.

**Итог.** 32 находки → верификация → **27 подтверждено** (24 CONFIRMED, 3
PLAUSIBLE), **5 опровергнуто**. Из 27: **1 high**, **1 medium**, **6 low**,
**19 nit**. Критических нет. Форк здоров: маркеры `lx:` сбалансированы, gofmt
чист, изоляция за build-tag соблюдена, энергетически-критичные узлы
(idle-suspend loop, sticky-балансер, dnstrack observable) — корректны.

Ниже — полный реестр. `Verdict`: CONFIRMED (воспроизводимо) / PLAUSIBLE
(фактически верно, но узкий/неадверсариальный триггер). Severity —
**после** верификации.

---

## Сводка по severity

| Severity | Кол-во | Находки |
|----------|--------|---------|
| **high** | 1 | #1 (masque h2 CONNECT висит навечно) |
| **medium** | 1 | #2 (idle-suspend воскрешает guard-suspended AWG) |
| **low** | 6 | #3 #4 #5 #6 #7 #8 |
| **nit** | 19 | #9 … #27 |

### По осям

логика — 4 (#1 #2 #6 #7) · неточность — 5 (#3 #5 #8 #16 #22) · документация — 1
(#4) · комментарий — 7 (#9 #10 #13 #15 #21 #24 #25 #26) · мёртвый код — 2 (#23
#27) · оптимизация — 3 (#12 #18 #20) · энергия/стабильность — 1 (#17) · слои — 1
(#11) · устаревшее — 1 (#19)

---

## P0 — исправить (влияет на стабильность в проде)

### #1 · HIGH · логика · `transport/masque/client_h2.go:158` · SPEC 021 (MASQUE)

**h2 CONNECT read-loop игнорирует ctx — может навечно заклинить весь outbound.**

`ConnectTunnelH2(ctx, …)` получает dial-ctx, но использует его только для
`url.Parse`. Дальше `sendConnect()` крутит `for { c.framer.ReadFrame() }`
(строки 158–192) на «сыром» TLS-conn **без read-deadline и без проводки
ctx**. `connectH2` (`protocol/masque/outbound.go:365`) проводит ctx только в
`tlsConn.HandshakeContext`; после хендшейка чтение блокируется бессрочно.

- **Сценарий провала:** `network=h2`-нода за middlebox, который завершает
  TCP+TLS, но никогда не отдаёт `:status` HEADERS (или WARP-столл). Dial с
  дедлайном (urltest-проба или любой `DialContext` с таймаутом) истекает, но
  горутина остаётся припаркованной в `ReadFrame`. Установка туннеля
  сериализована под `o.runMu`; **`Close()` (`outbound.go:477`) виснет на том же
  `runMu`** → заклиненный masque-outbound нельзя ни переиспользовать, ни
  корректно снести. Отказ **перманентный**, не транзиентный. (Класс «зомби-conn»
  из SPEC 012.)
- **Фикс (тонкий, внутри LX-файла):** перед read-loop в `ConnectTunnelH2`
  выставить дедлайн из ctx — `if d, ok := ctx.Deadline(); ok {
  tlsConn.SetReadDeadline(d) }` (и снять после успеха), либо watcher-горутина,
  закрывающая `tlsConn` по `<-ctx.Done()`. Upstream не трогается.

---

## P1 — исправить (корректность/безопасность состояния)

### #2 · MEDIUM · логика · `protocol/wireguard/endpoint.go:307` · SPEC 020

**Idle-suspended AWG-эндпоинт может быть воскрешён dial'ом после guard-suspend —
обходит защиту от AmneziaWG-over-WireGuard hang.**

Две сосуществующие фичи делят состояние `started`/`idleAsleep`. Idle-suspend
(SPEC 020) ставит `idleAsleep=true, started=false`. AWG-guard
(`SuspendAmneziaWG`, стр. 247) ставит `started=false`, **не трогая
`idleAsleep`**, и полагается на «`started==false` держит эндпоинт выключенным
навсегда» (весь смысл: AWG-over-WG вешает ядро Android). Но `resumeOnDial`
(стр. 307) отличает idle- от guard-suspend **только** по `idleAsleep`.

- **Сценарий провала (AAR, оба тега):** AWG-эндпоинт E недостижим+простаивает →
  idle-suspend (`idleAsleep=true`). Затем селектор в detour-цепочке E
  переключается на WireGuard-члена → guard зовёт `E.SuspendAmneziaWG()` (CAS на
  `started` — no-op, `idleAsleep` не тронут). Приходит dial по сохранившемуся
  пути → `resumeOnDial` видит `idleAsleep==true` → `Resume()`, поднимает E,
  возвращает `true` → **AWG-over-WG hang-риск ядра Android** — ровно то, что
  guard призван исключить. Триггер узкий (нужен порядок idle→guard + выживший
  путь dial), но не адверсариальный. Ср. `[[awg-detour-guard-must-be-at-start]]`.
- **Фикс:** сделать `SuspendAmneziaWG` авторитетным «stay-down» — пусть он
  **также** сбрасывает `idleAsleep=false` (тогда `resumeOnDial` пойдёт по
  fast-path `!idleAsleep` → вернёт `w.started.Load()==false`, не воскресит).
  Правка внутри уже помеченного `// lx:begin awg` блока.

---

## P2 — low (узкие/косметические баги, наблюдаемость, документация)

### #3 · LOW · неточность · `dns/client.go:396` · SPEC 018
Свежий (не stale) cache-hit в `questionCache` (стр. 383–400) возвращает адреса
на стр. 399, **не** вызывая `logCachedResponse`/`emitQueryEvent` — эмитит только
optimistic-ветка (стр. 394). Путь `Client.Lookup → lookupToExchange →
questionCache` (internal-резолв доменов из `route.go:811`). SPEC 018 требует
«каждый успешный резолв (exchanged/**cached**/optimistic/refreshed) эмитит
`DnsQueryEvent`». Профайлер не увидит эти резолвы (в отличие от Exchange-пути,
где cached эмитится на стр. 204). **Фикс:** добавить
`logCachedResponse(c.logger, ctx, transport, response, ttl)` в свежую ветку
перед стр. 396 — целиком внутри LX-логики.

### #4 · LOW · документация · `docs-lx/lx-config.md:370` · SPEC 009
Доки (`lx-config.md:370-371`, `lx-config.ru.md:365-367`) описывают `ip=sip` как
INVITE «с SDP-offer + `m=audio`». Генератор `masqueSIPInviteCPS`
(`sip_invite_awg.go:98-115`) шлёт INVITE с `Content-Type: application/sdp` +
`Content-Length: 0` и **без тела**. Ср. `[[sip-decoy-device-result]]` (i2=100
Trying). **Фикс:** переписать пункт ip=sip: i1 = body-less INVITE
(`Content-Length: 0`), i2 = `100 Trying` того же диалога; убрать «SDP
offer»/`m=audio`. Только доки.

### #5 · LOW · неточность · `protocol/group/urltest.go:163` · SPEC 019
`Pool()` (источник RPC `GetPool`) читает историю по неверному ключу:
`LoadURLTestHistory(tag)`, где `tag = detour.Tag()` (из `poolTags()`), тогда как
везде в балансере история хранится/читается по `RealTag(detour)` (`testNodes`
стр. 523; `seedPool`/`rebuildPool`/`Select` стр. 415/717/769). Для **вложенной
группы** в `outbounds` (селектор/urltest) слот покажет `Delay=0` (живой узел —
как мёртвый) в UI. Роутинг/liveness/sticky не задеты (они резолвят по
`detour.Tag()`). **Фикс:** в `Pool()` тоже читать по `RealTag`. Внутри
`urltest.go`.

### #6 · LOW · логика · `transport/masque/connectip/connectip.go:425` · SPEC 021
Пересчёт IPv4-checksum после TTL-декремента считает только первые 20 байт
(`b[:ipv4.HeaderLen]`), а `checksum.go` покрывает фиксированные 20. При IP-опциях
(IHL>5) checksum должна покрывать весь заголовок → битая сумма → peer дропает
пакет. Унаследовано из upstream connect-ip-go. **Крайне редкий** (стеки опции не
эмитят). **Фикс (опционально):** считать по `b[:IHL*4]`, либо оставить с
комментарием об ограничении.

### #7 · LOW · логика · `transport/v2rayxhttp/conn.go:141` · SPEC 002/011
`streamConn.Read` читает `c.reader` (стр. 141) без синхронизации, тогда как
горутина `setupReader` пишет его (стр. 135) до `close(c.created)`. Fast-path `if
c.reader == nil` обходит happens-before барьер `<-c.created`. По модели памяти Go
— гонка данных; `go test -race` пометит. Практический отказ маловероятен (reader
пишется один раз, обычный исход — прочитать stale-nil и корректно уйти в
`<-c.created`). **Фикс:** убрать fast-path, всегда блокироваться на
`<-c.created` (fast-path не даёт выигрыша — reader поздне-связан). Внутри
LX-файла.

### #8 · LOW · неточность · `transport/wireguard/endpoint.go:117` · SPEC 003/005/008
`awgJunk := max(s3, s4)` для расчёта MTU/junk-overhead, но по вендоренному
amneziawg-go `s3 → cookie padding`, `s4 → transport padding` (`uapi.go:374/385`)
— **s3 к транспортным/data-пакетам не применяется**. Комментарий (стр. 114-116)
неверно утверждает «s3/s4 prepend junk to every transport message». При
атипичном `s3>s4` (напр. `s3=200, s4=0`) без явного `mtu` код молча роняет MTU
до 1280 и шлёт ложное предупреждение против бюджета, урезанного на 200 байт.
Канонически s3==s4, так что вред узкий и безопасный. **Фикс:** считать overhead
только из `s4`; переформулировать комментарий/warning; поправить
`lx-config.md:407`. Внутри `// lx:begin awg`.

---

## P3 — nit (гигиена: комментарии, мёртвый код, микро-оптимизации, маркеры)

| # | Ось | Файл:стр | Суть | Фикс |
|---|-----|----------|------|------|
| **9** | комментарий | `daemon/started_service_command_lx.go:139` | Комментарий `GetGroups` врёт: «unary read convention, **like GetRules**» — а `GetRules` (стр. 101–104) возвращает `os.ErrInvalid`, не `status.Error`. Разные конвенции ошибок. | Убрать «like GetRules», сослаться на `GetOutbounds`/`GetPool`. |
| **10** | комментарий | `daemon/started_service_command_lx.go:186` | «Only urltest groups **in round_robin mode** implement it» — неточно: `Pool()` объявлен на `*group.URLTest` безусловно; assertion проходит для любой urltest-группы (least_test → `Pool()` вернёт nil). | «Every urltest group implements this; least_test returns nil → empty pool». |
| **11** | слои | `dns/client.go:262` | 5 вызовов `log*Response` получили новый позиционный аргумент `transport` (стр. 200/204/262/394/525) **без** `// lx:`-маркеров, хотя соседние `emitFailedQuery` помечены `// lx: SPEC 018`. Голая правка upstream-файла (§4). Тихого выпадения нет (параметр обязателен → компилятор поймает), но ребейз-конфликт не подсветит происхождение. | Пометить изменённые строки inline `// lx: SPEC 018`. |
| **12** | оптимизация | `dns/client_log.go:40` | `answersFromMessage(response)` строится для **каждого** события при активном подписчике, даже если `includeAnswers=false` (фильтр — на стороне сервера). Аллокации строк на каждый резолв. Санкционированный SPEC-компромисс (несколько подписчиков с разными флагами). | Оставить; при желании — агрегированный счётчик `includeAnswers`-подписок. |
| **13** | комментарий | `option/route.go:31` | Закрывающий маркер `// lx:end` **без имени фичи** — асимметрично `// lx:begin idle-suspend` (стр. 24) и всем прочим блокам. | `// lx:end idle-suspend`. |
| **14** | неточность | `option/route.go:31` | (та же строка) — **единственный** голый `// lx:end` в репо (46 других именованы). Ломает repo-wide конвенцию именованных маркеров (§4). | То же: `// lx:end idle-suspend`. |
| **15** | комментарий | `option/route.go:25` | Остаточный плейсхолдер `(XX)` в doc-комментарии `LXIdleSuspend` — «idle threshold **(XX)** for SPEC 020». | Убрать `(XX)` или заменить на unit/дефолт/ссылку. |
| **16** | неточность | `option/v2ray_xhttp.go:130` | Комментарий `sc_stream_up_server_secs`: «Ignored by the client (**it discards those bytes**)» — такой логики нет: `splitConn.Read` (`conn.go:197`) отдаёт тело в VLESS без фильтрации. Ложная уверенность, что клиент чистит keepalive-padding. | «client sets no server keepalive; does not strip injected padding — verify against target». |
| **17** | энергия/стабильность | `protocol/group/urltest_balance_lx.go:146` | `setSlots` безусловно зовёт `onChange()` (инвалидация reachability-кэша) на **каждом** health-check, даже если состав пула не изменился. Инертно без `with_lx_idle_suspend`; под тегом — лишняя рекомпутация reachability. PLAUSIBLE. | Сравнивать новый список тегов со `slots` под локом, звать `onChange` только при реальном изменении. |
| **18** | оптимизация | `protocol/wireguard/endpoint.go:307` | `resumeOnDial` делает безусловный atomic-store `stampActivity()` на **каждом** dial, даже в сборках без `with_lx_idle_suspend` (тик гейтится, stamp — нет). Тривиальный overhead + мёртвая работа. PLAUSIBLE. Санкционированный minimal-diff. | Не заводить фикс (тег-гейтинг 5 call-site'ов раздует ребейз-зону upstream-производного файла). |
| **19** | устаревшее | `route/rule/rule_item_package_name_regex_test.go:11` | SPEC 013 бэкпортил `package_name_regex` на базу 1.13.13; текущая база `v1.14.0-alpha.35` уже несёт фичу нативно (`rule_item_package_name_regex.go` — unchanged upstream; `option/rule.go:94` — нативно). Тест стал устаревшим артефактом. НЕ нарушение (изолирован в своём `_test.go`). | Пометить SPEC 013 «superseded-by-upstream» ИЛИ удалить тест. Без срочности. |
| **20** | оптимизация | `transport/masque/connectip/connectip.go:355` | `ipVersion(data)` вычисляется дважды в замыкании `ContainsFunc` по `localRoutes` (per-route, per-packet), хотя версия уже известна из `switch` (стр. 315, `v`). | Захватить `v` из switch, использовать в замыкании. |
| **21** | комментарий | `transport/masque/connectip/icmp.go:45` | Магическое `1232` (= `minMTU 1280 − 40 − 8`, RFC 4443) для усечения IPv6 ICMP payload — без пояснения; соседняя IPv4-ветка использует осмысленное `ipv4.HeaderLen+8`. PLAUSIBLE. | Добавить однострочный комментарий `1232 = 1280−40−8 (RFC 4443)`. |
| **22** | неточность | `transport/masque/masque.go:50` | Детект login-fail по **точному** строковому равенству `err.Error() == "CRYPTO_ERROR 0x131 (remote): tls: access denied"`. Bump quic-go, меняющий формат строки → дружелюбная подсказка о TLS-key/cert молча отваливается (функциональность цела). | `strings.Contains(err.Error(), "access denied")` (+ опц. `"0x131"`). Внутри LX-файла. |
| **23** | мёртвый код | `transport/v2rayxhttp/client.go:64` | Поле `Client.noGRPCHeader` (стр. 64) заполняется из опции (стр. 172), но **нигде не читается** — клиент gRPC-заголовки вообще не эмитит (`meta.go:347` ставит только `Content-Type: application/octet-stream`), омитить нечего. Поле помечено forward-compat, но лежит в основной таблице доки, а не в bucket «Accepted but IGNORED». | Убрать поле+присваивание; перенести `no_grpc_header` в блок IGNORED `lx-config.md:252`. |
| **24** | комментарий | `transport/v2rayxhttp/meta.go:152` | Битая склейка редакций: «…would create a cycle **is not a concern here**, but…». Смысл искажён (цикла импорта и не было бы; реальная причина — тестируемость). | Переписать в одно предложение про тестируемость `normalizeMeta` без пакета `option`. |
| **25** | комментарий | `transport/v2rayxhttp/meta.go:289` | Doc-комментарий `applyMeta` обещает «returns the possibly-updated URL path», но функция ничего не возвращает (путь пишется в `request.URL.Path`, стр. 324). Остаток прежнего API. | Убрать фразу про возврат пути. |
| **26** | комментарий | `transport/wireguard/device_awg.go:19` | Пример формата в doc-блоке обрывается на `…s1=<n>\ns2=<n>…` — s3/s4 отсутствуют, хотя тело эмитит их (стр. 97-98). | Дописать `\ns3=<n>\ns4=<n>` после s2. |
| **27** | мёртвый код | `transport/wireguard/stun_request_awg.go:77` | `ufrag := make([]byte, 9)` заполняется 9 crypto/rand-байтами, но `stunICEUsername` читает только `seed[0..7]` — байт 8 не читается (безопасная over-allocation, не off-by-one). | `make([]byte, 8)`. |

---

## Опровергнуто верификацией (5 — НЕ дефекты)

Зафиксировано, чтобы не переоткрывать:

| Файл:стр | Заявлено | Почему REFUTED |
|----------|----------|----------------|
| `connectip.go:309` | allow-list входящих расходится между h3/h2 | `localRoutes` пишется ровно в одном месте (`AdvertiseRoute`, стр. 145), вызываемом только из `advertiseDefaultRoute` — расхождение недостижимо. |
| `client_h2.go:356` | `receiveDatagram` reslice без reclaim → рост backing-массива ∝ throughput | Неверная семантика Go: `append` в forward-sliced slice реаллоцирует при исчерпании cap; монотонного роста нет. |
| `conn.go:257` | конкурентные `Write` доставляют POST-ы с seq не по порядку | Порядок гарантирован конструкцией (seq присваивается под тем же локом, что и отправка). |
| `connectip.go:257` | ошибка записи не отменяет чтение → `readFromStream` залипает | Механизм закрытия (CancelRead+Close в `Close()`) разблокирует чтение; сценарий не воспроизводится. |
| `dnstrack/manager.go:139` | счётчик подписчиков декрементируется безусловно, инкремент — под `err==nil` | Единственный вызывающий использует канонический паттерн `if err != nil { return }` до `defer UnSubscribe` — рассинхрон недостижим. |

---

## Что аудит подтвердил как здоровое (baseline)

Независимая проверка (вне агентов) + верификация подтвердили:
`gofmt -l` чист на всех LX-файлах · маркеры `// lx:begin/end` **сбалансированы**
во всех upstream-файлах (единственная асимметрия — #13/#14, косметика) · изоляция
за build-tag соблюдена (`with_xhttp`/`with_awg`/`with_lx_command`/`with_lx_idle_suspend`/`with_utls`) ·
proto-шов §3.6 за маркерами · LX-`panic()` только в AWG-генераторах и защитимы
(сбой `crypto/rand`/инварианты, зеркалят stdlib/qtls) · idle-suspend loop
энергетически аккуратен (гейт за тегом+`idleSuspend>0`, floor 5s, кэш-lookup) ·
sticky-балансер: lock-дисциплина корректна (`resolve`/`onChange` вне лока) ·
dnstrack: observable-паттерн upstream, atomic-гейт подписчиков, drop-oldest,
без утечек горутин/каналов.

---

## Рекомендация по приоритизации

1. **#1** (P0) — единственная находка с продовым импактом (перманентное зависание
   masque-outbound). Внести до следующего rc.
2. **#2** (P1) — узкий триггер, но последствие = hang ядра Android; фикс
   тривиален (1 строка в `SuspendAmneziaWG`).
3. **#3–#8** (P2) — по возможности; #4 (доки sip) и #8 (доки+логика s3/s4) — самые
   заметные пользователю.
4. **#9–#27** (P3) — гигиена; собрать в один «lx(audit): docs/comment/dead-code
   cleanup»-коммит. Исключения: **#18** — НЕ трогать (раздует ребейз);
   **#12/#17** — оставить (санкционированные компромиссы). **#13/#14** —
   обязательны (маркерная конвенция §4, дёшево).
