# SPEC: 122 — UTLS_RELEASE_HANDSHAKE_STATE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (refactor, память) — после успешного uTLS/REALITY-рукопожатия соединение отпускает ClientHello, приватные ключи key share и объекты расширений; провод и поведение не меняются |
| Статус | N (new) — черновик 2026-10-10, решения владельца 2026-10-11 (§8): делать (роутерный профиль), вариант B, ALPN один раз при создании соединения вместо стража 0086 (§3.1); код не начат |
| Ветка | `lx` |
| Build-tag | нет (внутри `with_utls`, как вся фича) |
| Источник | podkop-engine [`0026 — tls: release the uTLS handshake state after the handshake`](https://github.com/FiyeroT/podkop-engine/blob/main/patches/v1.14/0026-tls-release-the-uTLS-handshake-state-after-the-hands.patch) и его обязательное дополнение [`0086 — tls: do not rebuild the Client Hello after the handshake`](https://github.com/FiyeroT/podkop-engine/blob/main/patches/v1.14/0086-tls-do-not-rebuild-the-Client-Hello-after-the-handsh.patch); обсуждение — [sing-box-extended#162](https://github.com/shtorm-7/sing-box-extended/issues/162) п. 4 |
| Связанные | [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (`AuthKey` из `KeyShareKeys` до отправки), [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) / [118](../118-UTLS_CHROME_155/SPEC.md) (форк utls, стражи), [088](../088-REALITY_FRAGMENT_BYPASS/SPEC.md) / [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) (`prepareClientHello`) |

**Touches:** `common/tls/utls_client.go` (одна `// lx:`-строка в `UTLSClientConfig.Client`), `common/tls/reality_client.go` (одна `// lx:`-строка в `ClientHandshake`), новые `common/tls/utls_release_lx.go` + `utls_release_lx_test.go`; новый файл в форке `submodules/utls` (восьмой коммит), гитлинк, абзац `lx:begin utls-firefox148` в `go.mod`, раннбук §1.1, FEATURE 017 «Особенности сопровождения», `SPECS/README.md`, `docs-lx/lx-changelog.md`. Апстримный `utlsALPNWrapper.HandshakeContext` не меняется.

## 1. Что удерживает соединение после рукопожатия

`utls.UConn` держит состояние рукопожатия в экспортированных полях для осмотра и правки ClientHello до отправки — так его читает `prepareClientHello` (`common/tls/reality_client.go:195-300`) и ALPN-обёртка (`common/tls/utls_client.go:178-191`). После рукопожатия библиотека записывает состояние обратно в соединение и больше его не читает:

| Что | Где удерживается | Размер (порядок) |
|---|---|---|
| ClientHello (`PubClientHelloMsg`: `Raw`, `KeyShares` с гибридной долей 1216 Б, списки) | `HandshakeState.Hello` — `u_handshake_client.go:387` (defer пишет обратно), `:576` (`c.HandshakeState = *handshakeState` после `hs13.handshake()`) | ~2–3 КиБ |
| Приватные ключи key share: `Ecdhe`, `Mlkem *DecapsulationKey768`, `MlkemEcdhe` (`u_public.go:912-917`) | `HandshakeState.State13.KeyShareKeys` — `u_public.go:95-120` (`toPublic13`) | ML-KEM-768: 128 + 15 × 512 = 7808 Б (`internal/mlkem/mlkem768.go:74-83,155-163`) |
| ServerHello, `Transcript` (hash), `TrafficSecret`, `Suite`, `CertReq` | `HandshakeState.ServerHello`, `.State13` | сотни байт |
| Объекты расширений (`KeyShareExtension`, `SupportedCurves`, ALPN, GREASE ECH, padding…) | **два** держателя: `UConn.Extensions` и неэкспортированный `UConn.clientHelloSpec` (`u_parrots.go:3059-3082` — спека хранится на всё время жизни, `:3163-3164` — `Extensions` = копия среза **тех же** указателей) | ~3 КиБ |
| `echCtx` (контекст ECH, при пресетах с GREASE ECH) | `UConn.echCtx` (`u_parrots.go:3106`) | сотни байт |

Следствие для патча podkop: `uConn.Extensions = nil` освобождает заголовок среза, объекты расширений остаются живы через `clientHelloSpec`. Часть объектов держат и `UConn.ech` (`u_ech.go:166`) и `sessionController` (`pskExt` у пресетов Chrome с `UtlsPreSharedKeyExtension`, `u_parrots.go:2788`); метод форка (B) может обнулять и `ech` — замер −2,9 КиБ снят с этим остатком. Это видно в замере (§2): ещё −2,9 КиБ даёт только метод внутри форка, где поле доступно.

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
### 3.1 Решение: ALPN один раз при создании соединения, а не страж

Пересборка ClientHello в `utlsALPNWrapper.HandshakeContext` — способ подменить ALPN пресета
(utls для пресетов игнорирует `Config.NextProtos`), а не потребность рукопожатия. Нужный ALPN
известен уже в `UTLSClientConfig.Client` (`utls_client.go:102`: `c.config.NextProtos` уходит в
обёртку), и подмена — разовая настройка ClientHello. Так уже устроен REALITY: `prepareClientHello`
подменяет ALPN один раз до рукопожатия (`reality_client.go:231-238`).

Решение владельца 2026-10-11 — вместо стража 0086:

- `Client` одной `// lx:`-строкой отдаёт соединение из lx-хелпера `lxNewUTLSConn(uConn, nextProtos)`.
  Хелпер при непустом `nextProtos` делает `BuildHandshakeState` → подмена в `ALPNExtension` →
  `BuildHandshakeState` один раз (код апстримной обёртки, перенесённый в момент создания) и
  возвращает lx-тип, который встраивает `utlsALPNWrapper` с **пустым** `nextProtocols`.
- Апстримный `utlsALPNWrapper.HandshakeContext` при пустом списке сразу зовёт
  `UConn.HandshakeContext` — ветка у него уже есть; функция не меняется.
- lx-тип переопределяет `HandshakeContext`: `UConn.HandshakeContext(ctx)`, при успехе —
  освобождение (R1). Первый вызов: библиотека сама ещё раз зовёт `BuildHandshakeState`
  (`u_conn.go:374-379`) — `ApplyConfig` + `Marshal` по уже построенному состоянию, ключи не
  перегенерируются (они создаются один раз в `ApplyPreset`, `u_parrots.go:3096-3104`). Повторный
  вызов: `UConn` отвечает по `isHandshakeComplete` (`u_conn.go:321-323`) до любой сборки,
  освобождение идемпотентно.

Провод не меняется: последовательность `Build` → подмена ALPN → `Build` та же, что в апстримной
обёртке, только раньше по времени. Конфиг клонируется в `UClient` (`utls_client.go:102`), поэтому
`NextProtos`, `ServerName` и `EncryptedClientHelloConfigList` к моменту `Client` уже заморожены
(ECH-список кладётся до `Client`, `ech.go:169,179`). GREASE ECH, фрагментация и spoof
(обёртки `net.Conn` до `UClient`), kTLS (берёт соединение после рукопожатия) от раннего `Build`
не зависят. Проверено ревью кода (Fable, 2026-10-11).

**Известное ограничение.** `UConn.Read`/`Write` при незавершённом рукопожатии сами зовут
`c.Handshake()` (`u_conn.go:440,862`) — мимо переопределения lx-типа, и освобождения на этом
пути не будет. В апстриме этот путь терял подмену ALPN; теперь ALPN уже в ClientHello, так что
провод там даже правильнее. Сегодня неявного рукопожатия нет: оба входа в `Client` —
`aTLS.ClientHandshake` и `ECHClientConfig` — сразу зовут `HandshakeContext`.

Что меняется в поведении: ошибка сборки ClientHello при непустом ALPN возвращается из `Client`,
а не из рукопожатия — на том же соединении, раньше на время одного TCP-коннекта; вызывающие
`Client` ошибку обрабатывают. Методы `utlsConnWrapper` (`ConnectionState`, `Upstream`,
`Reader/WriterReplaceable`) продвигаются через встраивание; `badtls` достаёт `*utls.UConn`
через `N.CastReader` по `Upstream()`, тип обёртки ему не важен.

- **Освобождение только после успеха.** При ошибке REALITY `uConn` уходит в `realityClientFallback` (`reality_client.go:159`) как `net.Conn` — состояние ему не нужно, но порядок «сначала проверка `verified`, потом release» сохраняется, как в патче.

## 4. Где делать — варианты для решения владельца

| | A — только ядро (0026 + 0086 как есть) | B — ядро + метод форка | C — автоматически внутри форка |
|---|---|---|---|
| Что освобождается | `Hello`, `ServerHello`, `State13`, срез `Extensions` | A + `clientHelloSpec`, `echCtx` (+ при желании весь `HandshakeState`, включая `State12`, `MasterSecret`, `Session`) | как B |
| Выигрыш (REALITY `chrome`) | −14,0 КиБ | −16,9 КиБ | −16,9 КиБ |
| Апстримные файлы ядра | `utls_client.go` (~9 строк в `HandshakeContext`, уже `// lx:`-зона 088/118), `reality_client.go` (1 строка в `ClientHandshake`, файл с восемью lx-коммитами) | те же две точки касания | те же (0086-страж нужен всё равно: пересборка hello в обёртке — код ядра) |
| Форк utls | не трогается | **новый файл** `u_conn_release_lx.go` с методом `(*UConn) ReleaseHandshakeState()` — ноль конфликтов на переезде форка, восьмой коммит в счётчике раннбука §1.1 | правка `u_conn.go:handshakeContext` — зона конфликта на каждом переезде форка; меняет семантику библиотеки (осмотр `HandshakeState` после рукопожатия) |
| Связь с условием снятия форка (086/087/118) | нет | вызов метода, которого нет в metacubex: снятие форка потребует вернуться к A. Развязка — вызывать через проверку интерфейса `interface{ ReleaseHandshakeState() }` на `any(uConn)`: без метода компилируется и работает как A | при снятии форка освобождение исчезает молча |

**Решение владельца 2026-10-11 — B с развязкой через интерфейс.** Форк utls уже есть (семь коммитов), восьмой — новый файл без правок файлов metacubex. Рекомендация черновика была та же — **B с развязкой через интерфейс**: вся логика в двух новых файлах (`common/tls/utls_release_lx.go`, файл форка), в апстримных файлах ядра — вызов и страж 0086; при снятии форка ничего не ломается, теряются только 2,9 КиБ. Если владелец считает лишний коммит форка дороже 2,9 КиБ на соединение — A.

## 5. Требования

- **R1. Освобождение после успеха.** В `HandshakeContext` lx-типа uTLS-соединения (§3.1) и в `RealityClientConfig.ClientHandshake` после успешного рукопожатия (для REALITY — после `verifier.verified`) обнуляются `HandshakeState.Hello`, `HandshakeState.ServerHello`, `HandshakeState.State13`, `Extensions`; при варианте B — также `clientHelloSpec` и `echCtx` методом форка. При ошибке состояние не трогается.
- **R2. Повторный `HandshakeContext` не роняет процесс — по построению.** ALPN подменяется один раз в `Client` (§3.1); наша обёртка ClientHello при рукопожатии не пересобирает, а библиотечный `BuildHandshakeState` на повторном вызове не достигается (выход по `isHandshakeComplete`) — освобождённые поля никто не читает. Страж 0086 не нужен. На `realityClientConnWrapper` поведение уже такое.
- **R2a. Провод ALPN прежний.** Для каждого транспорта ALPN в ClientHello тот же, что до задачи: `NextProtos` подменяет список пресета; при пустом `NextProtos` — список пресета.
- **R3. Провод и поведение не меняются.** ClientHello, `AuthKey`, `key_share`, фрагментация, ALPN — как до задачи: стражи 083/086/088/089/118 проходят без правок. Данные, `KeyUpdate` и `NewSessionTicket` после рукопожатия обрабатываются как раньше.
- **R4. Вариант B — метод форка в новом файле**, без правок файлов metacubex; тест метода там же; гитлинк в ядре, комментарий `go.mod`, раннбук §1.1 и FEATURE 017 обновлены на «восемь коммитов».
- **R1a. Страж ренегоциации — в ядре.** `releaseHandshakeState` ничего не освобождает, если в `utls.Config` соединения `Renegotiation != RenegotiateNever` (единственный читатель `Hello` после рукопожатия — ренегоциация TLS 1.2, `u_conn.go:945`). Ядро `Renegotiation` не выставляет; страж — на случай, если начнёт. В методе форка — та же проверка.
- **R5. Сборка без `with_utls`** не затрагивается (новые файлы под тегом, как соседи).

## 6. Критерии приёмки

1. Новый тест `common/tls/utls_release_lx_test.go` (`with_utls`): ALPN на проводе для `NextProtos` = `["h2"]`, `["http/1.1"]` и пустого совпадает с апстримной обёрткой; пресет без ALPN при непустом `NextProtos` — ALPN не добавляется, `Build` один (как в апстриме); рукопожатие uTLS+ALPN против `crypto/tls`-сервера на `net.Pipe` → `HandshakeState.Hello == nil`, `KeyShareKeys == nil`, `Extensions == nil`; данные ходят; второй `HandshakeContext` → `nil` без паники. REALITY: `utls.RealityServer` в процессе с `DialContext` на внутренний TLS-сервер → те же проверки через `realityClientConnWrapper`; рукопожатие с ошибкой (пайп, закрытый после первого flight'а, как `lxRealityFirstFlight`) состояние не трогает.
2. `go test -ldflags=-checklinkname=0 -tags with_utls ./common/tls/` — зелёный, включая стражи 083/086/118 без правок.
3. Повтор замера §2 на реализации: REALITY `chrome` ≤ 13,5 КиБ (A) или ≤ 10,5 КиБ (B) на соединение.
4. `make -f Makefile.lx lx-build` полным `LX_TAGS`; при B — `go test .` форка.
5. Стенд: VLESS+REALITY на Xray ≥ v26.9.8 (стенд 086/118), `fp=chrome`/`firefox`, трафик через соединение дольше минуты (чтобы прошёл хотя бы один `NewSessionTicket`/`KeyUpdate` сервера, если он их шлёт) → 204 и нет обрывов.

## 7. Риски

- **Чтение состояния после рукопожатия из будущего кода.** Сегодня читателей нет (§1); новый код, которому понадобится, например, `ServerHello` после рукопожатия, получит `nil`. Страховка — комментарий у функции освобождения и тест критерия 1.
- **Гонки.** Освобождение идёт в горутине, вызвавшей `HandshakeContext`, до того как соединение отдано наружу (`tls.ClientHandshake` возвращает `Conn` после него); библиотека после рукопожатия поля не читает. Отдельной синхронизации не нужно.
- **Второй вызов `HandshakeContext`** — закрыт R2 по построению (§3.1); без него паника (§2).
- **Ошибка сборки ClientHello раньше** — из `Client`, а не из рукопожатия (§3.1).
- **ECH: сборка под мьютексом.** `ECHClientConfig` зовёт `Client` под `s.access` (`ech.go:131-132,179`): при ECH с непустым ALPN генерация ключей (ML-KEM-768) и два `Marshal` идут под мьютексом конфига, раньше — в `HandshakeContext` вне его. Микросекунды на соединение; учитывать при ECH с большим числом параллельных подключений.
- **Неявное рукопожатие через `Read`/`Write`** — без освобождения (§3.1, известное ограничение).
- **Ренегоциация TLS 1.2** — недостижима (`RenegotiateNever`), но если когда-нибудь ядро выставит `Renegotiation`, `clientHandshake` упадёт на `nil` Hello. Метод форка (B) может отказываться освобождать при `config.Renegotiation != RenegotiateNever`.
- **Сопровождение.** A: две точки в апстримных файлах, обе в зонах, которые upstream правил 16 и 11 раз с 2025-01 (`git log --since=2025-01-01 upstream/stable`), конфликты ожидаемы и тривиальны. B: плюс коммит форка в новом файле — переезжает `cherry-pick` без конфликтов; счётчик коммитов в раннбуке §1.1 и `go.mod` растёт до восьми.
- **Снятие форка** (условие снятия 086/087/118): при развязке через интерфейс — без последствий, кроме потери 2,9 КиБ.

## 8. Решения владельца (2026-10-11)

1. Делать — да: выигрыш значим для роутерного профиля (сотни–тысячи одновременных соединений).
2. Вариант — B (§4).
3. Состав обнуления в форке — набор podkop + `clientHelloSpec`/`echCtx` (п. 3 ниже не решался отдельно; по умолчанию — без `State12`/`Session`).
4. Фича-владелец — 017-REALITY.
5. Страж 0086 заменён подменой ALPN при создании соединения (§3.1).

### Исходные вопросы черновика

1. **Нужна ли задача вообще** (критерий (а1) CONSTITUTION §3.1): запроса от лаунчера, LxBox или роутеров нет — есть сторонний issue sing-box-extended#162 и наш замер. Выигрыш — 14–17 КиБ на каждое живое uTLS/REALITY-соединение (десятки МБ только при сотнях одновременных соединений, роутерный профиль); на LxBox — ещё и меньше объектов для сканирования GC (§208).
2. **Вариант A или B** (§4).
3. При B — обнулять весь `HandshakeState` (и `State12`, `MasterSecret`, `Session`) или только набор podkop + `clientHelloSpec`/`echCtx`? Разница в замере — 40 Б на TLS 1.3; `State12` имеет смысл только для TLS 1.2-серверов.
4. **Фича-владелец**: 017-REALITY (форк utls и uTLS-клиент живут там) или 008-ENERGY (ресурсы на устройстве)?
