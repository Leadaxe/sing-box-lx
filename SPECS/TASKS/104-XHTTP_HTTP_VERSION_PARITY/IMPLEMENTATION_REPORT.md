# IMPLEMENTATION REPORT: 104 — XHTTP_HTTP_VERSION_PARITY

**Фича:** [002-XHTTP](../../FEATURES/002-XHTTP/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `common/tls/utls_client_std_lx.go` (`with_utls`) | `lxSTDConfig()`: `*utls.Config` → `*crypto/tls.Config`, `disable_sni` → `VerifyConnection` |
| `common/tls/std_config_quic_lx.go` | `STDConfigForQUIC`: снимает kTLS, отвергает ECH (`ErrECHOverQUIC`), конвертирует uTLS |
| `transport/v2rayxhttp/http_version.go` | `decideHTTPVersion` (§5.1), `realityALPNNeedsH2`, `hideTransportError` |
| `transport/v2rayxhttp/http1.go` | `http1XmuxConn`, `newHTTP1Transport` (TLS и без TLS) |
| `transport/v2rayxhttp/http3.go` / `http3_stub.go` | `http3XmuxConn`, `newHTTP3Transport`, `hideQUICError`; заглушка без `with_quic` |
| `transport/v2rayxhttp/client.go`, `conn.go` | ветки версий вместо h2/h2c, предупреждения §6, debug `HTTP version N`; `request.Close` на долгих запросах HTTP/1.1; шесть точек сокрытия ошибок → `hideTransportError` |

Апстримных файлов ноль; `go.mod` не менялся.

## Отступления и уточнения

- Предупреждения и debug-строка пишутся логгером с тегом `xhttp`, поэтому в коде текст без префикса `xhttp:` — в выводе строка совпадает с §6.
- Предупреждение про uTLS пишется для любого uTLS-конфига на HTTP/3 (признак — конфиг пришёл из uTLS); отдельной проверки поля `fingerprint` нет: при `utls.enabled` отпечаток есть всегда.
- ECH: кроме `*ECHClientConfig` (динамический ECH) отвергается и статический список ECH-конфигов (`ECHConfigList()` непуст).
- Клиентский сертификат на HTTP/3 переносится в `GetClientCertificate` первым из `Certificates`.

## Проверка

- Тесты пакета: `go test -tags with_xhttp,with_quic,with_utls -ldflags "-checklinkname=0" ./transport/v2rayxhttp/...` — ok. Новые: `http_version_test.go` (§11 п. 1, в т. ч. «без TLS → 1.1»), `http1_test.go` (п. 5), `http3_test.go` (п. 3, 4 и сквозной stream-one по HTTP/3 с `ChromeParrot` против in-process `http3.Server`), `std_config_quic_test.go` (п. 2, страж §9).
- Сборка: `transport/v2rayxhttp` и `common/tls` с тегами и без (заглушка); `go vet` этих пакетов чистый.
- Живой стенд `lx-test/xhttp_h3/run.sh`: Xray 26.9.9 (`60e2a0c`, собран из исходников), VLESS + xhttp + TLS `alpn: ["h3"]`, самоподписанный сертификат, UDP 127.0.0.1; клиент — конфиг репортёра #25 (uTLS chrome, `certificate_public_key_sha256`, `alpn: ["h3"]`). `packet-up`, `stream-up`, `stream-one` — HTTP через туннель OK, в логе ядра `HTTP version 3`, Xray слушает только QUIC. Бинарь стенда собран до коммита `4c8967dc0` (правка текста лога), поведение не отличается.

## Открыто

- Android через LxBox — за владельцем (TASKS 4.5).
- HTTP/1.1 и cleartext против живого Xray не прогонялись (стенд — только h3); проверены юнитами против `httptest`.
