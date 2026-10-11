# SPEC: 122 — UTLS_RELEASE_HANDSHAKE_STATE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (refactor, память) — после успешного uTLS/REALITY-рукопожатия соединение отпускает ClientHello, приватные ключи key share и объекты расширений; провод и поведение не меняются |
| Статус | N (new) — черновик 2026-10-10: идея проверена по нашему дереву и измерена на loopback; код не менялся, решение владельца впереди (§8) |
| Ветка | `lx` |
| Build-tag | нет (внутри `with_utls`, как вся фича) |
| Источник | podkop-engine [`0026 — tls: release the uTLS handshake state after the handshake`](https://github.com/FiyeroT/podkop-engine/blob/main/patches/v1.14/0026-tls-release-the-uTLS-handshake-state-after-the-hands.patch) и его обязательное дополнение [`0086 — tls: do not rebuild the Client Hello after the handshake`](https://github.com/FiyeroT/podkop-engine/blob/main/patches/v1.14/0086-tls-do-not-rebuild-the-Client-Hello-after-the-handsh.patch); обсуждение — [sing-box-extended#162](https://github.com/shtorm-7/sing-box-extended/issues/162) п. 4 |
| Связанные | [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (`AuthKey` из `KeyShareKeys` до отправки), [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) / [118](../118-UTLS_CHROME_155/SPEC.md) (форк utls, стражи), [088](../088-REALITY_FRAGMENT_BYPASS/SPEC.md) / [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) (`prepareClientHello`) |

**Touches (предлагаемые):** `common/tls/utls_client.go` (`utlsALPNWrapper.HandshakeContext`, `// lx:`), `common/tls/reality_client.go` (`ClientHandshake`, одна строка `// lx:`), новый `common/tls/utls_release_lx.go` + `utls_release_lx_test.go`; при варианте B (§4) — новый файл в форке `submodules/utls` (восьмой коммит), гитлинк, абзац `lx:begin utls-firefox148` в `go.mod`, раннбук §1.1 (ожидаемое число коммитов форка), FEATURE 017 «Особенности сопровождения», `SPECS/README.md` (Roadmap), `docs-lx/lx-changelog.md`.

## 1. Что удерживает соединение после рукопожатия

`utls.UConn` держит состояние рукопожатия в экспортированных полях для осмотра и правки ClientHello до отправки — так его читает `prepareClientHello` (`common/tls/reality_client.go:195-300`) и ALPN-обёртка (`common/tls/utls_client.go:178-191`). После рукопожатия библиотека записывает состояние обратно в соединение и больше его не читает:

| Что | Где удерживается | Размер (порядок) |
|---|---|---|
| ClientHello (`PubClientHelloMsg`: `Raw`, `KeyShares` с гибридной долей 1216 Б, списки) | `HandshakeState.Hello` — `u_handshake_client.go:387` (defer пишет обратно), `:576` (`c.HandshakeState = *handshakeState` после `hs13.handshake()`) | ~2–3 КиБ |
| Приватные ключи key share: `Ecdhe`, `Mlkem *DecapsulationKey768`, `MlkemEcdhe` (`u_public.go:912-917`) | `HandshakeState.State13.KeyShareKeys` — `u_public.go:95-120` (`toPublic13`) | ML-KEM-768: 128 + 15 × 512 = 7808 Б (`internal/mlkem/mlkem768.go:74-83,155-163`) |
| ServerHello, `Transcript` (hash), `TrafficSecret`, `Suite`, `CertReq` | `HandshakeState.ServerHello`, `.State13` | сотни байт |
| Объекты расширений (`KeyShareExtension`, `SupportedCurves`, ALPN, GREASE ECH, padding…) | **два** держателя: `UConn.Extensions` и неэкспортированный `UConn.clientHelloSpec` (`u_parrots.go:3059-3082` — спека хранится на всё время жизни, `:3163-3164` — `Extensions` = копия среза **тех же** указателей) | ~3 КиБ |
| `echCtx` (контекст ECH, при пресетах с GREASE ECH) | `UConn.echCtx` (`u_parrots.go:3106`) | сотни байт |

Следствие для патча podkop: `uConn.Extensions = nil` освобождает заголовок среза, объекты расширений остаются живы через `clientHelloSpec`. Это видно в замере (§2): ещё −2,9 КиБ даёт только метод внутри форка, где поле доступно.

### Кто читает состояние после рукопожатия

Проверено по дереву (`grep HandshakeState|\.Extensions|UConn` вне `submodules/`, вне тестов):

- **Ядро** — только до рукопожатия: `reality_client.go:195-300` (`prepareClientHello`), `utls_client.go:178-191` (ALPN). Стражи 083/086/118 (`utls_firefox148_lx_test.go`, `reality_client_lx_test.go`) осматривают `UConn` после `BuildHandshakeState`/`prepareClientHello`, не после рукопожатия.
- **`ConnectionState()`** обеих обёрток (`utls_client.go:140`, `reality_client.go:389`) берёт `c.Conn.ConnectionState()` — состояние `Conn`, не `HandshakeState`.
- **Vision** (`sing-vmess/vless/vision_utls.go:17-19`) и **badtls** (`common/badtls/registry_utls.go:19`) берут у `UConn` только `uConn.Conn`; **ktls** — то же через linkname в `Conn`.
- **Библиотека после рукопожатия**: `handlePostHandshakeMessage` (`conn.go:1358`) → `handleNewSessionTicket` (`handshake_client_tls13.go:1029`, читает `c.resumptionSecret`, `c.config`) и `handleKeyUpdate` (`conn.go:1387`, `c.in`/`c.out`). `HandshakeState` там не участвует. Ренегоциация TLS 1.2 (`conn.go:1312-1334`): `Renegotiation` в `utls.Config` ядро не выставляет → `RenegotiateNever` → alert `no_renegotiation`, повторный `clientHandshake` недостижим.
- **Повторный `HandshakeContext`** на `UConn` — быстрый выход по `isHandshakeComplete` (`u_conn.go:321-323`), `BuildHandshakeState` не вызывается. На нашей ALPN-обёртке — иначе, см. §3.

Особенности форка, названные в задаче, после рукопожатия не нужны: reuse ключа Firefox 148 (`Ecdhe` и `MlkemEcdhe` — один объект) живёт в `KeyShareKeys`, который обнуляется целиком; `AuthKey` REALITY считается в `prepareClientHello` **до** отправки ClientHello (`reality_client.go:268-293`) и хранится в `realityVerifier.authKey` (32 Б); `VerifyPeerCertificate` выполняется внутри рукопожатия; PSK/resumption выключены (`SessionTicketsDisabled` в REALITY `:176`, `ClientSessionCache` у uTLS-клиента не задаётся).

## 2. Замер

Loopback, go1.26.8 darwin/arm64, 200 соединений, 3 раунда, медиана; `runtime.GC()` ×2 + `ReadMemStats().HeapAlloc` до и после; серверы в отдельных процессах (обычный TLS 1.3 на `crypto/tls`; REALITY на `utls.RealityServer` — тот же код, что оборачивает `reality_server.go:204`, dest = тот TLS-сервер). Прототип — вне дерева репо: `scratchpad/0026/bench/` (`client/` — чистый utls с вариантами освобождения через reflect для неэкспортированных полей; `sb/` — через наш `common/tls`, `tls.NewClient` + `tls.ClientHandshake`, теги `with_utls`).

### Через `common/tls` ядра (наш путь, Б на соединение)

| Клиент | как есть | podkop 0026 | выигрыш |
|---|---|---|---|
| uTLS + ALPN `chrome` | 25 164 | 11 099 | −14 065 (−56 %) |
| uTLS + ALPN `firefox` | 25 153 | 11 116 | −14 037 |
| uTLS + ALPN `chrome_155` | 26 294 | 12 018 | −14 276 |
| **REALITY `chrome`** | **27 354** | **13 313** | **−14 041 (−51 %)** |
| REALITY `firefox` | 27 364 | 13 346 | −14 018 |
| REALITY `chrome_155` | 28 509 | 14 231 | −14 278 |

Заявка автора «69 → 56 КиБ на idle VLESS REALITY» (−13 КиБ на всё соединение вместе с буферами VLESS) совпадает с нашими −14,0 КиБ TLS-слоя.

### Чистый utls (что даёт форк сверх патча, Б на соединение)

| Отпечаток | как есть | podkop 0026 | + `clientHelloSpec`/`echCtx` = nil | всё `HandshakeState` = {} |
|---|---|---|---|---|
| `chrome` (133) | 25 273 | 11 118 | 8 230 | 8 190 |
| `chrome_155` | 26 277 | 12 031 | 8 254 | 8 210 |
| `firefox` (148) | 25 119 | 11 085 | 8 189 | 8 225 |
| `safari` (26.3) | 24 159 | 10 429 | 7 645 | 7 633 |
| `ios` (без гибрида) | 10 033 | 7 059 | 6 037 | 6 025 |
| TCP без TLS | 313 | — | — | — |

Гибридные отпечатки стоят +15 КиБ против `ios` в основном из-за ключа ML-KEM-768 и доли в `Raw`. Остаток ~8 КиБ — `Conn`, два `halfConn` с AEAD, буферы записи, разобранный сертификат сервера, клон `utls.Config` на соединение (`utls_client.go:102`, `reality_client.go:174`); в этой задаче не разбирается.

### Функциональная проверка (тот же прототип)

- После освобождения данные ходят в обе стороны по обоим путям (echo через uTLS+ALPN и через REALITY).
- Повторный `HandshakeContext` на `realityClientConnWrapper` после освобождения → `nil`.
- Повторный `HandshakeContext` на `utlsALPNWrapper` после освобождения → **паника** `nil pointer dereference` (см. §3).

## 3. Применимость патча к нашему дереву

Патч 0026 ложится на наши файлы без адаптации: `reality_client.go:163` (`return &realityClientConnWrapper{uConn}`) и `utls_client.go:193` (`return c.UConn.HandshakeContext(ctx)`) — те же строки, что у podkop. Но один 0026 недостаточен:

- **ALPN-обёртка пересобирает ClientHello на каждом вызове** `HandshakeContext` (`utls_client.go:177-191`: `BuildHandshakeState` → `MarshalClientHello`, `u_conn.go:599` разыменовывает `HandshakeState.Hello`). После освобождения второй вызов роняет процесс — воспроизведено в §2. Сегодня второго вызова нет: `tls.ClientHandshake` (`client.go:168`) → `aTLS.ClientHandshake` вызывает его один раз, `ECHClientConfig.ClientHandshake` (`ech.go:123`) — один раз; но обёртка — `net.Conn`, уходящий в чужие транспорты (http2, gRPC, XHTTP), и гарантии единственного вызова у неё нет. Поэтому podkop добавил 0086: если `HandshakeState.Hello == nil`, обёртка идёт сразу в `UConn.HandshakeContext`, который отвечает «готово». Берём вместе, как 086 брала два коммита refraction.
- **Освобождение только после успеха.** При ошибке REALITY `uConn` уходит в `realityClientFallback` (`reality_client.go:159`) как `net.Conn` — состояние ему не нужно, но порядок «сначала проверка `verified`, потом release» сохраняется, как в патче.

## 4. Где делать — варианты для решения владельца

| | A — только ядро (0026 + 0086 как есть) | B — ядро + метод форка | C — автоматически внутри форка |
|---|---|---|---|
| Что освобождается | `Hello`, `ServerHello`, `State13`, срез `Extensions` | A + `clientHelloSpec`, `echCtx` (+ при желании весь `HandshakeState`, включая `State12`, `MasterSecret`, `Session`) | как B |
| Выигрыш (REALITY `chrome`) | −14,0 КиБ | −16,9 КиБ | −16,9 КиБ |
| Апстримные файлы ядра | `utls_client.go` (~9 строк в `HandshakeContext`, уже `// lx:`-зона 088/118), `reality_client.go` (1 строка в `ClientHandshake`, файл с восемью lx-коммитами) | те же две точки касания | те же (0086-страж нужен всё равно: пересборка hello в обёртке — код ядра) |
| Форк utls | не трогается | **новый файл** `u_conn_release_lx.go` с методом `(*UConn) ReleaseHandshakeState()` — ноль конфликтов на переезде форка, восьмой коммит в счётчике раннбука §1.1 | правка `u_conn.go:handshakeContext` — зона конфликта на каждом переезде форка; меняет семантику библиотеки (осмотр `HandshakeState` после рукопожатия) |
| Связь с условием снятия форка (086/087/118) | нет | вызов метода, которого нет в metacubex: снятие форка потребует вернуться к A. Развязка — вызывать через проверку интерфейса `interface{ ReleaseHandshakeState() }` на `any(uConn)`: без метода компилируется и работает как A | при снятии форка освобождение исчезает молча |

Рекомендация черновика — **B с развязкой через интерфейс**: вся логика в двух новых файлах (`common/tls/utls_release_lx.go`, файл форка), в апстримных файлах ядра — вызов и страж 0086; при снятии форка ничего не ломается, теряются только 2,9 КиБ. Если владелец считает лишний коммит форка дороже 2,9 КиБ на соединение — A.

## 5. Требования

- **R1. Освобождение после успеха.** В `utlsALPNWrapper.HandshakeContext` и `RealityClientConfig.ClientHandshake` после успешного рукопожатия (для REALITY — после `verifier.verified`) обнуляются `HandshakeState.Hello`, `HandshakeState.ServerHello`, `HandshakeState.State13`, `Extensions`; при варианте B — также `clientHelloSpec` и `echCtx` методом форка. При ошибке состояние не трогается.
- **R2. Повторный `HandshakeContext` не роняет процесс.** `utlsALPNWrapper.HandshakeContext` при `HandshakeState.Hello == nil` не пересобирает ClientHello и возвращает результат `UConn.HandshakeContext` (0086). На `realityClientConnWrapper` поведение уже такое.
- **R3. Провод и поведение не меняются.** ClientHello, `AuthKey`, `key_share`, фрагментация, ALPN — как до задачи: стражи 083/086/088/089/118 проходят без правок. Данные, `KeyUpdate` и `NewSessionTicket` после рукопожатия обрабатываются как раньше.
- **R4. Вариант B — метод форка в новом файле**, без правок файлов metacubex; тест метода там же; гитлинк в ядре, комментарий `go.mod`, раннбук §1.1 и FEATURE 017 обновлены на «восемь коммитов».
- **R5. Сборка без `with_utls`** не затрагивается (новые файлы под тегом, как соседи).

## 6. Критерии приёмки

1. Новый тест `common/tls/utls_release_lx_test.go` (`with_utls`): рукопожатие uTLS+ALPN против `crypto/tls`-сервера на `net.Pipe` → `HandshakeState.Hello == nil`, `KeyShareKeys == nil`, `Extensions == nil`; данные ходят; второй `HandshakeContext` → `nil` без паники. REALITY: `utls.RealityServer` в процессе с `DialContext` на внутренний TLS-сервер → те же проверки через `realityClientConnWrapper`; рукопожатие с ошибкой (пайп, закрытый после первого flight'а, как `lxRealityFirstFlight`) состояние не трогает.
2. `go test -ldflags=-checklinkname=0 -tags with_utls ./common/tls/` — зелёный, включая стражи 083/086/118 без правок.
3. Повтор замера §2 на реализации: REALITY `chrome` ≤ 13,5 КиБ (A) или ≤ 10,5 КиБ (B) на соединение.
4. `make -f Makefile.lx lx-build` полным `LX_TAGS`; при B — `go test .` форка.
5. Стенд: VLESS+REALITY на Xray ≥ v26.9.8 (стенд 086/118), `fp=chrome`/`firefox`, трафик через соединение дольше минуты (чтобы прошёл хотя бы один `NewSessionTicket`/`KeyUpdate` сервера, если он их шлёт) → 204 и нет обрывов.

## 7. Риски

- **Чтение состояния после рукопожатия из будущего кода.** Сегодня читателей нет (§1); новый код, которому понадобится, например, `ServerHello` после рукопожатия, получит `nil`. Страховка — комментарий у функции освобождения и тест критерия 1.
- **Гонки.** Освобождение идёт в горутине, вызвавшей `HandshakeContext`, до того как соединение отдано наружу (`tls.ClientHandshake` возвращает `Conn` после него); библиотека после рукопожатия поля не читает. Отдельной синхронизации не нужно.
- **Второй вызов `HandshakeContext`** — закрыт R2; без него паника (§2).
- **Ренегоциация TLS 1.2** — недостижима (`RenegotiateNever`), но если когда-нибудь ядро выставит `Renegotiation`, `clientHandshake` упадёт на `nil` Hello. Метод форка (B) может отказываться освобождать при `config.Renegotiation != RenegotiateNever`.
- **Сопровождение.** A: две точки в апстримных файлах, обе в зонах, которые upstream правил 16 и 11 раз с 2025-01 (`git log --since=2025-01-01 upstream/stable`), конфликты ожидаемы и тривиальны. B: плюс коммит форка в новом файле — переезжает `cherry-pick` без конфликтов; счётчик коммитов в раннбуке §1.1 и `go.mod` растёт до восьми.
- **Снятие форка** (условие снятия 086/087/118): при развязке через интерфейс — без последствий, кроме потери 2,9 КиБ.

## 8. Открытые вопросы владельцу

1. **Нужна ли задача вообще** (критерий (а1) CONSTITUTION §3.1): запроса от лаунчера, LxBox или роутеров нет — есть сторонний issue sing-box-extended#162 и наш замер. Выигрыш — 14–17 КиБ на каждое живое uTLS/REALITY-соединение (десятки МБ только при сотнях одновременных соединений, роутерный профиль); на LxBox — ещё и меньше объектов для сканирования GC (§208).
2. **Вариант A или B** (§4).
3. При B — обнулять весь `HandshakeState` (и `State12`, `MasterSecret`, `Session`) или только набор podkop + `clientHelloSpec`/`echCtx`? Разница в замере — 40 Б на TLS 1.3; `State12` имеет смысл только для TLS 1.2-серверов.
4. **Фича-владелец**: 017-REALITY (форк utls и uTLS-клиент живут там) или 008-ENERGY (ресурсы на устройстве)?
