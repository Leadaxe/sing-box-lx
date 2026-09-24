# PLAN: 104 — XHTTP_HTTP_VERSION_PARITY

Контракт — [SPEC.md](SPEC.md). Порядок работ задан владельцем: **спека → доки → вся реализация → тесты и стенд**. Тесты с реализацией не перемежаются.

## Этап 1. Спека

`SPEC.md`, этот план, `TASKS.md`; строка 104 в таблице «Задачи фичи» FEATURE 002.

## Этап 2. Доки (до кода)

| Файл | Что меняется |
|------|--------------|
| `SPECS/FEATURES/002-XHTTP/FEATURE.md` | Новый подраздел «Версия HTTP» в «Контролируемых параметрах»: таблица §5.1 SPEC (входы — `tls.alpn`, REALITY, наличие TLS; новых ключей нет). В «Режимах»: `auto` на HTTP/1.1 и HTTP/3 → `packet-up`. В «Сборке»: HTTP/3 требует `with_quic`, без него конфиг с `alpn: ["h3"]` отвергается. В «Правилах и гарантиях»: отпечаток uTLS на HTTP/3 не применяется (предупреждение), REALITY всегда HTTP/2, без TLS — h2c как раньше, HTTP/3 ходит по UDP через тот же detour. В «Границах»: `downloadSettings`, `quicParams`, ECH на HTTP/3. `h_keep_alive_period` в таблице `xmux`: действует на HTTP/2 и HTTP/3, дефолты по версиям |
| `docs-lx/lx-protocols-transports.md`, `.ru.md` | §1: новый подраздел «HTTP version» с той же таблицей и предупреждениями; строка в «Troubleshooting» (узел с `alpn: ["h3"]` не поднимается → detour без UDP / UDP режется по пути); пример VLESS + XHTTP + TLS `alpn: ["h3"]`; `h_keep_alive_period` в §1.7 |
| `SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/URL_PARSING.md` | §6: пункт «HTTP/3» переписывается — `alpn=h3` маппится как есть и работает (сборка с `with_quic`); `alpn=http/1.1` даёт HTTP/1.1 |
| `SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/PARAM_MAP.md` | строка `alpn` (из `tlsSettings`/`extra`) → `tls.alpn`, с правилом выбора версии и ссылкой на SPEC 104 |
| `SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md` | «Остаточные пробелы» п. 2 → ссылка «закрыто SPEC 104» |

## Этап 3. Реализация

### Файлы

| Файл | Тег | Содержимое |
|------|-----|-----------|
| `transport/v2rayxhttp/http_version.go` | — | `httpVersion` (`1.1`/`2`/`3`); `decideHTTPVersion(tlsConfig tls.Config, reality bool) httpVersion` по §3.1 SPEC (REALITY — существующий `tlsConfigIsReality`); проверка ALPN REALITY-узла (§4 п. 11) |
| `transport/v2rayxhttp/client.go` | — | в `NewClient`: выбор версии; ветки `newTransport` для 1.1/2/3 вместо единственной h2-ветки; при REALITY/HTTP/2 — прежняя логика `SetNextProtos(h2)` при пустом ALPN; debug-строка версии; предупреждения §6 SPEC. Схема `http`/`https` в URL — как сейчас (HTTP/3 всегда `https`) |
| `transport/v2rayxhttp/http1.go` | — | `http1XmuxConn`: `*http.Transport` (`ForceAttemptHTTP2: false`, `TLSNextProto` пустая карта, `IdleConnTimeout` 300 с; `DialContext` → `N.Dialer` TCP без TLS, `DialTLSContext` → `tls.NewDialer`); `Close()` → `CloseIdleConnections` + флаг; `IsClosed()` — флаг |
| `transport/v2rayxhttp/conn.go` | — | `request.Close = true` на download-GET и запросах с потоковым телом, когда транспорт HTTP/1.1 (признак — поле в `Client`); замена шести вызовов `badh2.HideStreamError` на пакетную `hideTransportError` (сначала `badh2`, затем HTTP/3-хук) |
| `transport/v2rayxhttp/http3.go` | `with_xhttp && with_quic` | `http3XmuxConn` (`*http3.Transport`, последний `*quic.Conn` под `atomic.Pointer`; `IsClosed()` = закрыт нами или `Context().Done()`); `newHTTP3Transport(ctx, dialer, serverAddr, tlsConfig, keepAlive, logger) (func() xmuxConn, error)`: std-конфиг через помощник `common/tls` (§4 п. 4), подгонка под `ChromeParrot` (§4 п. 5), `quic.Config` по §4 п. 7–8, `Dial` = `dialer.DialContext(ctx, "udp", serverAddr)` + `quic.DialEarlyConn` + закрытие UDP по `Context()`; ошибка UDP-dial оборачивается `HTTP/3 needs UDP to the server`; ECH → ошибка dial; `hideQUICError(err)` по §7 SPEC |
| `transport/v2rayxhttp/http3_stub.go` | `!with_xhttp \|\| !with_quic` | `newHTTP3Transport` → ошибка `requires the with_quic build tag`; `hideQUICError` = identity |
| `common/tls/utls_client_std_lx.go` | `with_utls` | `func (c *UTLSClientConfig) lxSTDConfig() (*STDConfig, error)` — перенос полей `*utls.Config` → `*tls.Config` (список в §4 п. 4 SPEC), `disable_sni` → `VerifyConnection` через `verifyConnection` |
| `common/tls/std_config_quic_lx.go` | — | `func STDConfigForQUIC(config Config) (std *STDConfig, fromUTLS bool, err error)`: `STDConfig()`; при ошибке — снять `*KTLSClientConfig`, `*ECHClientConfig` → ошибка `ECH is not supported over HTTP/3`, тип с `lxSTDConfig()` → конвертация и `fromUTLS = true`; иначе исходная ошибка. Вход клонируется, исходный конфиг не меняется |

### Порядок внутри этапа

1. `common/tls`: два lx-файла (новые файлы в апстримном пакете, апстримные файлы не трогаются).
2. `http_version.go`, `http1.go`, `http3.go` + `http3_stub.go`.
3. `client.go` — проводка версий, предупреждения; `conn.go` — `request.Close` для HTTP/1.1 и `hideTransportError`.
4. Сборка пакета: `go build -tags with_xhttp,with_quic,with_utls ./transport/v2rayxhttp/ ./common/tls/` и без тегов `go build ./transport/v2rayxhttp/` (заглушка компилируется).

### Места, требующие внимания

- **Один `http3.Transport` на ресурс пула.** `http3.Transport` держит по одному клиенту на хост; хост в URL всегда `serverAddr`, поэтому ресурс = одно QUIC-соединение, как `http2.Transport` = одно TCP-соединение. После смерти соединения `http3.Transport` сам передиалит на следующем запросе; `IsClosed()` по `Context()` выводит такой ресурс из пула раньше.
- **Контекст dial.** `http3.Transport.getClient` передаёт в `Dial` контекст первого запроса — у нас это conn-scoped контекст (SPEC 072/077) без дедлайна. Рукопожатие ограничено `HandshakeIdleTimeout` quic-go; отмена conn'а, открывшего соединение, прерывает только рукопожатие, готовое QUIC-соединение от неё не зависит (`context.WithoutCancel` в quic-go).
- **HTTP/1.1 без правки `conn.go`-логики.** Все режимы уже работают через `http.RoundTripper`; отличие одно — `request.Close` на долгих запросах.
- **Предупреждения один раз** — в `NewClient`, логгер `xhttp` из `log.Factory` контекста (как события `xmux`).

## Зона касания upstream

Апстримные файлы не правятся. Новые lx-файлы в апстримном пакете `common/tls` (`utls_client_std_lx.go`, `std_config_quic_lx.go`) зависят от неэкспортируемых полей `UTLSClientConfig` и функции `verifyConnection` — при их изменении на мерже конфликт проявится ошибкой компиляции, а новое поле проверки сертификата — только тестом §11 п. 2 SPEC (см. SPEC §9). `go.mod` не меняется: `github.com/sagernet/quic-go` и `http3` уже прямые зависимости.

## Этап 4. Тесты и стенд

Только тесты пакета `transport/v2rayxhttp` и один живой стенд. Полные прогоны, матрицы тегов и линтеры не запускаются.

### Юнит-тесты (в `transport/v2rayxhttp/`)

| Файл | Тег | Проверяет (SPEC §11) |
|------|-----|----------------------|
| `http_version_test.go` | — | п. 1: таблица §5.1 |
| `http1_test.go` | — | п. 5: `httptest` HTTP/1.1, `Connection: close` на GET/stream, keep-alive на upload-POST |
| `http3_test.go` | `with_xhttp && with_quic` | п. 3: `IsClosed()`, `KeepAlivePeriod`; п. 4: классификация и сокрытие ошибок quic-go/http3 |
| `std_config_quic_test.go` | `with_xhttp && with_quic && with_utls` | п. 2: конвертация uTLS → std (конфиг собирается `tls.NewClient` с `utls`), проверка сертификата своим/чужим ключом, `disable_sni`, ECH |

Команда:

```sh
go test -tags with_xhttp,with_quic,with_utls -ldflags "-checklinkname=0" ./transport/v2rayxhttp/
```

### Живой стенд (SPEC §11 п. 7)

Каталог `lx-test/xhttp_h3/` (не собирается `go test`, как соседние стенды): конфиг Xray (VLESS-inbound `xhttp`, `security: tls`, `alpn: ["h3"]`, самоподписанный сертификат, `127.0.0.1:<порт>` UDP), конфиг ядра — конфиг репортёра #25 с адресом loopback, `utls.fingerprint: "chrome"`, `certificate_public_key_sha256` от стендового сертификата, `mixed`-inbound. Сертификат и хеш ключа генерируются скриптом стенда. Бинарь ядра — `make -f Makefile.lx lx-build`; Xray — релизный бинарь. Прогон: по одному на `packet-up`, `stream-up`, `stream-one` — `curl` через `mixed` к HTTP-серверу на loopback, в логе ядра `HTTP version 3`, у Xray — соединение по QUIC.

Прогон на Android через LxBox — за владельцем, вне приёмки.

## Риски

- **`ChromeParrot` и проверка сертификата.** quic-go конвертирует std-конфиг в uTLS сам (`utlsConfigFromStd`) и отвергает непереносимые поля. Всё, что проверяет сертификат, должно быть в `VerifyPeerCertificate`/`RootCAs`/`InsecureSkipVerify`; `disable_sni` (`VerifyConnection`) идёт без `ChromeParrot`.
- **Без TLS остаётся h2c** (SPEC §10 п. 4): провод существующих конфигов не меняется.
- **`http3.Transport` повторяет запрос** один раз при `errConnUnusable`/`H3_REQUEST_REJECTED` (`doRoundTripOpt`); тела-пайпы без `GetBody` повтор не проходят и возвращают ошибку — поведение как у h2 для потоковых тел.
- **Лимит сокетов.** `max_concurrency: 1` (дефолт `xmux`) на HTTP/3 = отдельный UDP-сокет и QUIC-соединение на поток, как у Xray; на HTTP/1.1 — TCP-соединение на запрос, тоже как у Xray.
