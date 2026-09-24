# SPEC: 104 — XHTTP_HTTP_VERSION_PARITY

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — выбор версии HTTP в клиентском XHTTP-транспорте по правилам Xray: HTTP/1.1, HTTP/2, HTTP/3 |
| Статус | O (open) — спека; доки, реализация и проверка — по [PLAN.md](PLAN.md) |
| Ветка | `lx` |
| Build-tag | `with_xhttp`; ветка HTTP/3 — `with_xhttp && with_quic` |
| Заявка | [Leadaxe/sing-box-lx#25](https://github.com/Leadaxe/sing-box-lx/issues/25) |
| Связано | [059](../059-XHTTP_XMUX/SPEC.md) (пул `xmux`, место под `quic.Config.KeepAlivePeriod`), [061](../061-XHTTP_DIAL_DOWNLOAD_DEADLOCK/SPEC.md) (dial не ждёт download-ответ), [077](../077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) (контракт dial-ctx), [050](../050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md) (одноразовые дедлайны), [076](../076-XHTTP_XMUX_BREAKER/SPEC.md) / [094](../094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md) (брейкер, нейтральная локальная отмена), [082](../082-H2_STREAM_ERROR_TYPE_LEAK/SPEC.md) (типы ошибок не выходят за conn) |
| Эталон | Xray-core `main` @ [`60e2a0c5`](https://github.com/XTLS/Xray-core/tree/60e2a0c502d3f5f191d71b4343efe09c32338399) (2026-09-24); второй источник — mihomo `Alpha` @ [`8d57a8c5`](https://github.com/MetaCubeX/mihomo/tree/8d57a8c57c44125f2a59899f0e40efd3557f694f) |

Ссылки вида `dialer.go:83` ниже — на `transport/internet/splithttp/` Xray-core @ `60e2a0c5`, если не указано иное.

---

## 1. Проблема

Issue #25: Xray-сервер с `security: tls`, `tlsSettings.alpn: ["h3"]` слушает XHTTP только по QUIC/UDP (`hub.go:463` — `isH3`, когда ALPN состоит из одного элемента `h3`). mihomo к нему подключается, наше ядро нет. Конфиг репортёра: VLESS, `tls.alpn: ["h3"]`, `utls.fingerprint: "chrome"`, `certificate_public_key_sha256`, `transport.mode: "stream-up"`.

Причина: `transport/v2rayxhttp/client.go` строит только `http2.Transport` поверх TCP-TLS. При непустом `tls.alpn` клиент его не переписывает, и в TCP-ClientHello уходит `h3`. Сервер по TCP на этом порту не отвечает, dial падает по таймауту. Ограничение было записано в задаче 002 (`IMPLEMENTATION_REPORT.md` §«Остаточные пробелы» п. 2, `URL_PARSING.md` §6).

Шире: Xray выбирает версию HTTP по `tls.alpn` и REALITY (`dialer.go:83-100`), у нас версия одна. Узлы с `alpn: ["http/1.1"]` у нас тоже работают по HTTP/2, а конфиг без TLS идёт h2c, где Xray использует HTTP/1.1.

## 2. Цель

Паритет с `decideHTTPVersion` Xray: клиент выбирает HTTP/1.1, HTTP/2 или HTTP/3 по тем же правилам, по тем же входам (`tls.alpn`, REALITY, наличие TLS), с тем же поведением транспорта на каждой версии. Новых ключей конфига нет.

## 3. Как это устроено у Xray (@ `60e2a0c5`)

### 3.1 Выбор версии

`decideHTTPVersion` (`dialer.go:83-100`), вызывается при построении клиента (`dialer.go:106`) и в `Dial` (`dialer.go:298`):

1. REALITY → `"2"` (`dialer.go:84-86`), ALPN не смотрится;
2. нет TLS → `"1.1"` (`dialer.go:87-89`);
3. в `alpn` не один элемент (пусто или два и больше) → `"2"` (`dialer.go:90-92`);
4. единственный элемент `http/1.1` → `"1.1"` (`dialer.go:93-95`);
5. единственный элемент `h3` → `"3"` (`dialer.go:96-98`);
6. любой другой единственный элемент → `"2"` (`dialer.go:99`).

При `"3"` адрес назначения переводится в UDP (`dialer.go:107-109`, `299-301`). Сервер зеркален: QUIC-листенер поднимается только при ALPN из одного `h3` (`hub.go:463`), иначе TCP с HTTP/1.1 и h2c одновременно (`hub.go:556-564`).

ALPN в ClientHello Xray берёт из `tlsSettings.alpn`; пустой список заменяется на `["h2", "http/1.1"]` (`transport/internet/tls/config.go:425-427`). REALITY-клиент ALPN из конфига не берёт вовсе: ClientHello строится по отпечатку (`transport/internet/reality/reality.go:122-137`).

### 3.2 Клиент на каждой версии

**HTTP/1.1** (`dialer.go:262-275`): `net/http.Transport` с `DisableKeepAlives: true` и `IdleConnTimeout: 300s`; каждый download-GET и каждый запрос с потоковым телом — отдельное TCP(+TLS)-соединение с `Connection: close`. Upload-POST'ы `packet-up` идут мимо `http.Transport`: запрос сериализуется `req.Write` и пишется в сырое соединение из `sync.Pool` (`client.go:122-176`, `h1_conn.go`); ответы не читаются (`UnreadedResponsesCount` нигде не увеличивается, ветка `client.go:149-160` мёртвая). TLS — тот же dial, что у HTTP/2: uTLS по `fingerprint` (`dialer.go:135-143`).

**HTTP/2** (`dialer.go:248-261`): `http2.Transport`, `IdleConnTimeout: 300s`, `ReadIdleTimeout` = `xmux.hKeepAlivePeriod`; `0` → 45 с (`ChromeH2KeepAlivePeriod`, `common/net/net.go:20`), отрицательное → выключено.

**HTTP/3** (`dialer.go:156-247`): `http3.Transport` на форке `apernet/quic-go` (`go.mod:6`).

- `quic.Config` (`dialer.go:164-190`) при отсутствии `finalmask.quicParams`: `MaxIdleTimeout` = 300 с (`net.ConnIdleTimeout`); `KeepAlivePeriod` = `xmux.hKeepAlivePeriod`, при `0` — 10 с (`QuicgoH3KeepAlivePeriod`, `common/net/net.go:17`), при отрицательном — `0` (выключено); `MaxIncomingStreams: -1` (сервер не открывает двунаправленные потоки); `DisablePathMTUDiscovery` на всех ОС, кроме linux/windows/darwin (на Android PMTUD выключен); `ChromeParrot: true`.
- `TLSClientConfig` — стандартный `crypto/tls`-конфиг из `tlsSettings` (`dialer.go:111-115`, `193-194`). `fingerprint` в HTTP/3-ветке не участвует: uTLS применяется только в TCP-dial (`dialer.go:135-143`). Вид ClientHello задаёт `ChromeParrot` — QUIC-рукопожатие и транспортные параметры как у Chrome.
- Dial (`dialer.go:195-246`): UDP-сокет, `quic.Transport` с `ZeroLengthConnectionIDGenerator` при `ChromeParrot` (`dialer.go:224-227`), `tr.DialEarly` (`dialer.go:229`), закрытие сокета по `conn.Context()` (`dialer.go:233`). Контроль перегрузки по умолчанию — BBR (`dialer.go:235-243`, пакет `transport/internet/hysteria/congestion`).
- `DefaultDialerClient.Close` закрывает только `http3.Transport` (`client.go:181-188`).

### 3.3 Режимы и `xmux` на разных версиях

- Режим от версии не зависит: `auto` → `stream-one` при REALITY, иначе `packet-up` (`dialer.go:333-342`); `stream-up` при REALITY с `downloadSettings`. Все три режима допустимы на HTTP/1.1, HTTP/2 и HTTP/3; сервер версию при разборе режима не проверяет (`hub.go:154-200`, `ProtoMajor` читается только для адреса клиента в логе, `hub.go:169-174`).
- Заголовки от версии не зависят: `Content-Type: application/grpc` на запросах с телом (`config.go:326-328`), паддинг и размещения одинаковы.
- `xmux` одинаков для всех версий: ресурс пула — `DefaultDialerClient` целиком (`dialer.go:73-75`, `mux.go`). На HTTP/1.1 это логический клиент: TCP-соединение на каждый запрос всё равно новое, а лимиты `max_concurrency`/`h_max_request_times`/`c_max_reuse_times`/`h_max_reusable_secs` считаются по клиенту. `IsClosed()` у клиента — флаг, который ставится при ошибке download-запроса или upload-POST (`client.go:73-83`, `109-114`); контекст QUIC-соединения Xray для этого не использует.
- Xray пишет версию в лог dial: `XHTTP is dialing to …, mode …, HTTP version …` (`dialer.go:349`).

### 3.4 mihomo (второй источник, @ `8d57a8c5`)

`transport/xhttp/client.go:158-229`: `alpn` = `[h3]` → `http3.Transport` (`KeepAlivePeriod` 10 с по умолчанию, `MaxIdleTimeout` 300 с, `MaxIncomingStreams: -1`); `alpn` = `[http/1.1]` → `http.Transport` только HTTP/1.1 (с keep-alive); иначе HTTP/2 — в том числе без TLS (h2c, расходится с Xray). HTTP/3-dial (`adapter/outbound/vless.go:676-713`): `DialEarly` через dialer прокси, TLS-конфиг через `ToStdConfig` (client-fingerprint не применяется); без TLS — ошибка `xhttp HTTP/3 requires TLS`, с REALITY — `xhttp HTTP/3 does not support reality` (Xray в этом случае молча берёт HTTP/2). `auto` — как у Xray (`transport/xhttp/config.go:77-89`).

## 4. Решения

1. **Правила выбора версии — `decideHTTPVersion` Xray (§3.1); отступления — §10.** Входы — наличие TLS, REALITY, `tls.alpn`. Для REALITY `tls.alpn` на выбор версии не влияет.
2. **HTTP/3 — отдельный файл под `//go:build with_xhttp && with_quic`.** quic-go в апстриме за `with_quic`. Без `with_quic` конфиг, для которого правило даёт HTTP/3, отвергается при загрузке ошибкой (§6). Все поставляемые сборки (`LX_TAGS` в `Makefile.lx`, `sharedTags` AAR) `with_quic` содержат.
3. **uTLS и HTTP/3 — паритет с Xray.** Отпечаток `utls.fingerprint` на HTTP/3 не применяется, конфиг не падает, при загрузке — одно предупреждение (§6). QUIC-рукопожатие идёт с `quic.Config.ChromeParrot: true` (есть в `github.com/sagernet/quic-go` v0.61.0-sing-box-mod.7, апстрим включает его в hysteria2: `protocol/hysteria2/outbound.go:157`), то есть выглядит как у Chrome, как у Xray. Правило фичи: одна кривая нода подписки не роняет весь конфиг.
4. **Откуда std-конфиг.** `UTLSClientConfig.STDConfig()` отвечает `unsupported usage for uTLS` (`common/tls/utls_client.go:81-83`). Правка этого апстримного файла не нужна: в пакет `common/tls` добавляется lx-файл под `with_utls` с методом, собирающим `*crypto/tls.Config` из внутреннего `*utls.Config` (поля, которые `newUTLSClient` заполняет: `Time`, `RootCAs`, `InsecureSkipVerify`, `VerifyPeerCertificate`, `NextProtos`, `MinVersion`/`MaxVersion`, `CipherSuites`, `Certificates`, `ServerName`; `disable_sni` через `InsecureServerNameToVerify` — в `VerifyConnection` тем же `verifyConnection`, что у `STDClientConfig`), и lx-файл без тега с экспортируемым помощником, который сначала зовёт `STDConfig()`, при отказе снимает обёртку kTLS и берёт метод uTLS-конфига. Конфиг с ECH через этот путь не конвертируется (§6). Апстримные файлы не трогаются.
5. **Подгонка std-конфига под `ChromeParrot`.** quic-go при `ChromeParrot` отвергает `VerifyConnection`, `Certificates`, `GetCertificate` (`internal/handshake/tls_conn_utls.go:56-62`). На клоне конфига в пакете XHTTP: `Certificates` (клиентский сертификат) переносятся в `GetClientCertificate`; при `VerifyConnection` (`disable_sni`) `ChromeParrot` для этого узла выключается с предупреждением — проверка сертификата важнее вида рукопожатия. `NextProtos` = `["h3"]`.
6. **Пул: новые реализации `xmuxConn`** (`transport/v2rayxhttp/xmux.go:31`) рядом с `http2XmuxConn`:
   - `http3XmuxConn` — один `http3.Transport`; `IsClosed()` = вызван наш `Close()` или завершился `Context()` последнего `*quic.Conn`, отданного из `Dial` этого транспорта; `Close()` = `http3.Transport.Close()`.
   - `http1XmuxConn` — один `net/http.Transport` (только HTTP/1.1: `ForceAttemptHTTP2: false`, `TLSNextProto` пустой, `IdleConnTimeout` 300 с). Download-GET и запросы с потоковым телом (`stream-one`, `stream-up`) идут с `request.Close = true` — на проводе `Connection: close` и отдельное соединение, как у Xray с `DisableKeepAlives`. Upload-POST'ы `packet-up` идут по keep-alive-соединениям того же транспорта и **читают ответ** (отличие от Xray §3.2: сырые записи без чтения ответов не воспроизводятся — ответ нужен брейкеру SPEC 076, а сервер Xray отвечает на каждый POST). `IsClosed()` — как у `http2XmuxConn`: только наш `Close()`.
   - `xmux` применяется ко всем версиям по семантике Xray (§3.3); счёт `h_max_request_times` — по запросам, как сейчас.
7. **`h_keep_alive_period` на HTTP/3** → `quic.Config.KeepAlivePeriod`: `0` → 10 с, `> 0` → значение, `< 0` → `0` (место заложено в SPEC 059 §6). На HTTP/1.1 не действует (у Xray тоже).
8. **`quic.Config` HTTP/3 — как у Xray при пустом `quicParams`:** `MaxIdleTimeout` 300 с, `KeepAlivePeriod` по п. 7, `MaxIncomingStreams: -1`, `DisablePathMTUDiscovery` вне linux/windows/darwin, `ChromeParrot: true`. `ChromeParrot` в quic-go фиксирует idle timeout (30 с), окна, лимит потоков и размер первого пакета (`config.go:109-124` quic-go) — у Xray так же; значения выставляются для паритета записи, действуют закреплённые. `HandshakeIdleTimeout` — дефолт quic-go (Xray его не задаёт).
9. **Dial HTTP/3 — через тот же `N.Dialer`** (detour): `dialer.DialContext(ctx, "udp", serverAddr)` + `quic.DialEarlyConn` — форма апстрима `common/httpclient/http3_transport.go:85-98` (с `ChromeParrot` `DialEarlyConn` сам ставит нулевую длину connection ID, quic-go `client.go:131-146`); закрытие UDP-conn по `quicConn.Context()`, как `transport/v2rayquic/client.go:69-82`. Адрес из аргумента `Dial` не используется: dial идёт на `serverAddr` (у Xray — `dest`). Контекст QUIC-соединения отвязан от отмены dial-контекста (quic-go `transport.go:312`, `context.WithoutCancel`); dial-контекст ограничивает только рукопожатие.
10. **HTTP/1.1 и HTTP/2 dial не меняется:** TCP через `N.Dialer`, TLS через `tls.NewDialer` (uTLS, фрагментация, REALITY работают как сейчас). Без TLS — `http1XmuxConn` с `DialContext` на `N.Dialer` вместо h2c (паритет с Xray, §10 п. 4).
11. **ALPN в ClientHello:** HTTP/1.1 и HTTP/3 — как в конфиге (правило их и выбрало); HTTP/2 при пустом `tls.alpn` — `["h2"]`, как сейчас (отличие от Xray `["h2","http/1.1"]` — §10 п. 2); REALITY — как сейчас, кроме случая, когда `tls.alpn` задан и не содержит `h2`: тогда ALPN заменяется на `["h2"]` с предупреждением (иначе REALITY-узел с `alpn: ["h3"]` по TCP не поднимется, у Xray ALPN из конфига к REALITY не доходит вовсе).
12. **Классификация ошибок и сокрытие типов — общими для всех версий механизмами** (§7): брейкер SPEC 076/094, `outboundErr` SPEC 094, сокрытие типов SPEC 082 расширяется на типы quic-go/http3.
13. **`downloadSettings` вне объёма** (§8).

## 5. Контракт поведения

### 5.1 Версия HTTP

| TLS | REALITY | `tls.alpn` | Версия | Транспорт | ALPN в ClientHello |
|-----|---------|-----------|--------|-----------|--------------------|
| нет | — | — | HTTP/1.1 | TCP, `http1XmuxConn` | — |
| да | да | любой | HTTP/2 | TCP+REALITY, `http2XmuxConn` | как сейчас; без `h2` в списке → `["h2"]` + предупреждение |
| да | нет | пусто | HTTP/2 | TCP+TLS, `http2XmuxConn` | `["h2"]` |
| да | нет | 2+ элемента | HTTP/2 | TCP+TLS, `http2XmuxConn` | как в конфиге |
| да | нет | `["http/1.1"]` | HTTP/1.1 | TCP+TLS, `http1XmuxConn` | `["http/1.1"]` |
| да | нет | `["h3"]` | HTTP/3 | UDP+QUIC, `http3XmuxConn` | `["h3"]` |
| да | нет | любой другой один элемент (`["h2"]`, …) | HTTP/2 | TCP+TLS, `http2XmuxConn` | как в конфиге |

Сравнение элемента — точное, как у Xray (`h3`, `http/1.1`).

### 5.2 Режимы

| Режим | HTTP/1.1 | HTTP/2 | HTTP/3 |
|-------|----------|--------|--------|
| `packet-up` | download-GET — своё соединение; upload-POST'ы — keep-alive | как сейчас | потоки одного QUIC-соединения |
| `stream-up` | GET и потоковый POST — по своему соединению каждый | как сейчас | два потока одного QUIC-соединения |
| `stream-one` | потоковый POST на своём соединении | как сейчас | один поток |
| `auto` | `packet-up` | REALITY → `stream-one`, иначе `packet-up` | `packet-up` |

Заголовки, пути, паддинг, размещения, `Content-Type: application/grpc` — одинаковы на всех версиях.

### 5.3 Контракты dial (сохраняются на всех версиях)

- **SPEC 077:** `stream-one`/`stream-up` отдают conn после первого вызова `Read` тела запроса HTTP-слоем. HTTP/3 читает тело после рукопожатия (`ClientConn.roundTrip` ждёт `HandshakeComplete` для не-0-RTT методов, `http3/client.go`; `sendRequestBody` — после заголовков); HTTP/1.1 — в `writeLoop` после установки соединения и записи заголовков. `probeRequestBody` `net/http` читает тело до dial только у методов без тела (`GET`, `HEAD`, …), а потоковые режимы шлют `POST` (замена `GET` → `POST` вне `packet-up` уже есть). 0-RTT не случается: при `ChromeParrot` возобновление сессий выключено (`tls_conn_utls.go:26-30`).
- **SPEC 061:** `packet-up` отдаёт conn сразу, download-ответ не ждётся — от версии не зависит.
- **SPEC 050:** дедлайны рвут пайп тела с читающей половины; HTTP/3 (`sendRequestBody` → `cancelingReader`) и HTTP/1.1 получают ошибку чтения тела и прерывают запрос.
- **Detour без UDP.** Ошибка `DialContext(…, "udp", …)` возвращается из `RoundTrip` сразу; `stream-one`/`stream-up` отдают её из dial, `packet-up` — первым `Read`. Текст дополняется причиной: `v2ray-xhttp: HTTP/3 needs UDP to the server: …`. Если UDP до сервера режется по пути, dial завершается по `HandshakeIdleTimeout` quic-go, а не висит. В части сетей UDP/QUIC режется или ограничивается — тогда HTTP/2 остаётся рабочим вариантом, если сервер его слушает.

## 6. Ошибки и предупреждения

Предупреждения пишутся один раз при создании транспорта, логгер `xhttp` (как события `xmux`).

| Ситуация | Где | Поведение и текст |
|----------|-----|-------------------|
| HTTP/3 по правилу, сборка без `with_quic` | загрузка | ошибка `v2ray-xhttp: HTTP/3 (tls.alpn ["h3"]) requires the with_quic build tag` |
| HTTP/3 + uTLS | загрузка | предупреждение `xhttp: utls fingerprint is not applied over HTTP/3, the QUIC handshake uses the Chrome profile` |
| HTTP/3 + `disable_sni` | загрузка | предупреждение `xhttp: disable_sni turns off the Chrome QUIC profile for this server`; `ChromeParrot` выключен |
| HTTP/3 + ECH | загрузка | предупреждение `xhttp: ECH is not supported over HTTP/3`; dial этого узла возвращает ошибку с тем же текстом, конфиг грузится |
| REALITY + `tls.alpn` без `h2` | загрузка | предупреждение `xhttp: REALITY uses HTTP/2, tls.alpn replaced with ["h2"]` |
| HTTP/3, detour без UDP | dial | `v2ray-xhttp: HTTP/3 needs UDP to the server: <причина>` |
| версия выбрана | загрузка | debug: `xhttp: HTTP version 1.1/2/3` (у Xray — info на каждый dial, `dialer.go:349`) |

## 7. Ошибки на границе conn (HTTP/3, HTTP/1.1)

Типы из `github.com/sagernet/quic-go` v0.61.0-sing-box-mod.7:

| Событие | Что отдаёт библиотека | Брейкер (SPEC 076/094) | Наружу из conn |
|---------|------------------------|------------------------|----------------|
| отмена запроса нашим `cancel()` / вызывающим | `RoundTrip` → `context.Canceled` (`ClientConn.RoundTrip` подменяет ошибку на `req.Context().Err()`; `Transport.doRoundTripOpt` при ожидании dial → `context.Cause`, у нас `WithCancel` = `context.Canceled`) | нейтрально (SPEC 094) | — (dial/raise) |
| сервер сбросил поток | `*http3.Error{Remote: true}` (`maybeReplaceError` переводит `*quic.StreamError`/`*quic.ApplicationError`, `http3/error.go:41-63`; `Unwrap` нет) | сбой | скрытый тип, текст сохранён |
| мы закрыли тело (`Close()`, истёкший read-deadline) | `*http3.Error{Remote: false, ErrCode: H3_REQUEST_CANCELLED}` (`body.Close` → `CancelRead`, `http3/body.go:74-77`; отмена контекста — горутина в `ClientConn.roundTrip`) | не сбой (`localClosed`, SPEC 076) | `net.ErrClosed` / `os.ErrDeadlineExceeded` (`outboundErr`, SPEC 094 — гейт по флагу, не по тексту) |
| соединение умерло по простою | `*quic.IdleTimeoutError` (`Timeout() = true`, `Temporary() = false`, `Unwrap` → `net.ErrClosed`) | сбой | скрытый тип, без `Timeout()`, `Unwrap` → `net.ErrClosed` |
| stateless reset | `*quic.StatelessResetError` (`Temporary() = true`, `Unwrap` → `net.ErrClosed`) | сбой | то же |
| ошибка транспорта / рукопожатия | `*quic.TransportError` (`Unwrap` → `net.ErrClosed`, причина), `*quic.HandshakeTimeoutError` (`Timeout() = true`) | сбой | то же |
| чистое завершение | `io.EOF` | — | `io.EOF` |
| HTTP/1.1: сброс, обрыв | `ECONNRESET`, `io.ErrUnexpectedEOF` | сбой | как есть |
| HTTP/1.1: мы закрыли тело | `http: read on closed response body` | не сбой (`localClosed`) | `net.ErrClosed` (SPEC 094) |

**Аналог SPEC 082.** Спин из SPEC 082 требует, чтобы потребитель conn'а (h2-клиент x/net поверх `crypto/tls`) получил значение `http2.StreamError`; типы quic-go/http3 под `err.(http2.StreamError)` не попадают, этого спина нет. Риск другой: `*quic.IdleTimeoutError` и `*quic.HandshakeTimeoutError` отвечают `Timeout() = true`, `*quic.StatelessResetError` — `Temporary() = true`. Conn XHTTP отдаёт сохранённую ошибку чтения на каждый `Read`, и потребитель, который считает `Timeout()` истёкшим дедлайном и читает снова (или повторяет на `Temporary()`), крутится без сисколлов. Решение: эти типы, `*http3.Error` и `*quic.StreamError` не выходят за conn — в тех же шести точках, где `badh2.HideStreamError` (три `Read`, три `setupReader`), ошибка заменяется типом с тем же текстом, без `Timeout()`/`Temporary()`; для ошибок уровня соединения (`Unwrap` → `net.ErrClosed` у оригинала) `Unwrap` → `net.ErrClosed` сохраняется, чтобы релей (`E.IsClosedOrCanceled`) по-прежнему видел закрытие. Настоящий `os.ErrDeadlineExceeded` нашего read-deadline (SPEC 050) не затрагивается: он рождается в `outboundErr`, после сокрытия. Функция сокрытия HTTP/3 живёт в файле под `with_xhttp && with_quic`, заглушка без тега возвращает ошибку как есть.

## 8. Не входит

- **`downloadSettings`** — асимметричный транспорт Xray (download по другому адресу/версии/TLS, `dialer.go:354-397`). Следующий кандидат фичи: с этой задачей решение о версии становится попутным, `decideHTTPVersion` применяется к download-стороне отдельно (`dialer.go:367`).
- **`finalmask.quicParams`** Xray (окна, congestion, `brutal`, `disableChromeParrot`, `disableGSO`) — новых ключей нет; действуют значения §4 п. 8.
- **Контроль перегрузки BBR** на HTTP/3 (у Xray по умолчанию, `dialer.go:235-243`) — §10 п. 1.
- **Сырые pipelined upload-POST'ы HTTP/1.1** Xray без чтения ответов — §4 п. 6.
- **ECH на HTTP/3** — узел грузится, dial отвечает ошибкой (§6).
- **Browser dialer** Xray (`browser_dialer`), серверная сторона.

## 9. За чем следить на мерже

Апстримные файлы задача не правит. Швы, от которых зависит реализация:

- `common/tls/utls_client.go` — поля `UTLSClientConfig` и то, какие поля `*utls.Config` заполняет `newUTLSClient`. Новое поле, влияющее на проверку сертификата, должно попасть в lx-конвертацию; страж — юнит-тест конвертации в пакете XHTTP, сравнивающий проверку сертификата у std- и uTLS-клиента на одном конфиге.
- `common/tls/std_client.go` — `verifyConnection` (lx-конвертация `disable_sni` его вызывает) и форма `STDConfig()`.
- `common/tls/ech.go`, `common/tls/ktls.go` — обёртки, которые помощник снимает или отвергает.
- `github.com/sagernet/quic-go`: `ChromeParrot`, `ZeroLengthConnectionIDGenerator`, `DialEarlyConn`, `utlsConfigFromStd` (набор отвергаемых полей), `http3.maybeReplaceError`, `ClientConn.RoundTrip` (подмена на `ctx.Err()`), типы `qerr` (`Timeout`/`Temporary`/`Unwrap`). Бамп quic-go — прогон тестов пакета XHTTP.
- `common/httpclient/http3_transport.go` — образец dial; если апстрим вынесет общий HTTP/3-dial в экспортируемую функцию, перейти на неё.
- Xray `decideHTTPVersion` (`dialer.go:83-100`) и `hub.go:463` — при изменении правил на стороне Xray правится таблица §5.1.

## 10. Решения по расхождениям с Xray

1. **Контроль перегрузки на HTTP/3 — Cubic (дефолт quic-go), не BBR.** На совместимость провода не влияет; BBR — отдельная задача при запросе.
2. **ALPN HTTP/2 при пустом `tls.alpn` — `["h2"]`, как сейчас.** Сервер выбирает h2 в обоих случаях; REALITY-ветка не трогается.
3. **Keep-alive HTTP/2 по умолчанию не меняется.** Расхождение с Xray (45 с) и с формулировкой SPEC 059 §3 — вне объёма задачи.
4. **Без TLS — HTTP/1.1, как у Xray (не отступление).** Сервер Xray на TCP принимает и HTTP/1.1, и h2c (`hub.go:556-559`), поэтому прямые конфиги без TLS, ходившие по h2c, продолжают работать. Обратные прокси и CDN на незашифрованном порту обычно принимают только HTTP/1.1 — такие узлы начинают работать. Цена — нет мультиплексирования: каждый поток даунлинка и каждый потоковый запрос идут отдельным TCP-соединением, как у Xray. mihomo без TLS шлёт h2c (`transport/xhttp/client.go:203`) — расходится с Xray; контракт задаёт Xray.

## 11. Критерии приёмки

1. Юнит на выбор версии: таблица §5.1 построчно, включая REALITY с `alpn: ["h3"]` и `["http/1.1"]`, `alpn` из двух элементов, пустой, неизвестный единственный.
2. Юнит на lx-конвертацию uTLS → std: у конфига с `utls.fingerprint`, `certificate_public_key_sha256`, `server_name`, `alpn: ["h3"]` получается std-конфиг с `VerifyPeerCertificate`, `ServerName`, `NextProtos` = `["h3"]`; проверка сертификата отвергает чужой ключ и принимает свой; `disable_sni` даёт `VerifyConnection`; ECH — отказ.
3. Юнит на `http3XmuxConn`: `IsClosed()` после `Close()` и после завершения контекста QUIC-соединения; `h_keep_alive_period` `0`/`>0`/`<0` → `KeepAlivePeriod` 10 с / значение / `0`.
4. Юнит на классификацию и сокрытие (§7): `context.Canceled` из `roundTrip` нейтрален; `*http3.Error{Remote: true}` считается сбоем; наружу из `Read` не выходят `*http3.Error`, `*quic.StreamError`, `*quic.IdleTimeoutError`, `*quic.StatelessResetError`; у скрытой ошибки нет `Timeout()`/`Temporary()` = `true`; `errors.Is(…, net.ErrClosed)` сохраняется у ошибок уровня соединения; локальное закрытие даёт `net.ErrClosed`.
5. Юнит на HTTP/1.1: download-GET и потоковые запросы уходят с `Connection: close`, upload-POST'ы `packet-up` переиспользуют соединение; сервер — `httptest` HTTP/1.1.
6. Тесты пакета `transport/v2rayxhttp` зелёные под тегами `with_xhttp,with_quic,with_utls` (с `-ldflags "-checklinkname=0"`).
7. **Живой стенд:** Xray-core (релиз не старше `60e2a0c5`) с VLESS-inbound `xhttp`, `security: tls`, `alpn: ["h3"]`, самоподписанный сертификат, UDP на loopback; клиент — наше ядро с конфигом репортёра #25 (`utls.fingerprint: "chrome"`, `certificate_public_key_sha256`, `alpn: ["h3"]`), по одному прогону на режим `packet-up`, `stream-up`, `stream-one`: HTTP-запрос через туннель успешен, в логе ядра версия `3`, у Xray — соединение по QUIC.
8. Прогон на Android-устройстве через LxBox с тем же узлом — за владельцем, отдельно от приёмки задачи.
