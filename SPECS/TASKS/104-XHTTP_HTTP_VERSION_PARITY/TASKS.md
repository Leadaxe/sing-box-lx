# TASKS: 104 — XHTTP_HTTP_VERSION_PARITY

Порядок — по [PLAN.md](PLAN.md): спека → доки → реализация → тесты и стенд.

## 1. Спека

- [x] 1.1 SPEC.md, PLAN.md, TASKS.md; исследование Xray @ `60e2a0c5` и mihomo @ `8d57a8c5`
- [x] 1.2 Строка 104 в «Задачах фичи» FEATURE 002, статус O
- [x] 1.3 Решения по SPEC §10 (BBR, ALPN HTTP/2 по умолчанию, keep-alive HTTP/2, HTTP/1.1 без TLS) внесены в SPEC

## 2. Доки

- [x] 2.1 FEATURE 002: «Версия HTTP», режимы, сборка, правила и гарантии, границы, `h_keep_alive_period`
- [x] 2.2 `docs-lx/lx-protocols-transports.md` и `.ru.md`: подраздел HTTP version, troubleshooting, пример `alpn: ["h3"]`, `h_keep_alive_period`
- [x] 2.3 Задача 002: `URL_PARSING.md` §6, `PARAM_MAP.md` (`alpn`), `IMPLEMENTATION_REPORT.md` п. 2

## 3. Реализация

- [ ] 3.1 `common/tls/utls_client_std_lx.go` (`with_utls`): `lxSTDConfig()`
- [ ] 3.2 `common/tls/std_config_quic_lx.go`: `STDConfigForQUIC` (kTLS, ECH, uTLS)
- [ ] 3.3 `transport/v2rayxhttp/http_version.go`: `decideHTTPVersion`, ALPN REALITY-узла
- [ ] 3.4 `transport/v2rayxhttp/http1.go`: `http1XmuxConn`
- [ ] 3.5 `transport/v2rayxhttp/http3.go` + `http3_stub.go`: `http3XmuxConn`, `newHTTP3Transport`, `hideQUICError`
- [ ] 3.6 `client.go`: ветки версий, предупреждения, debug-строка версии
- [ ] 3.7 `conn.go`: `request.Close` для HTTP/1.1, `hideTransportError` в шести точках
- [ ] 3.8 Сборка пакета с тегами `with_xhttp,with_quic,with_utls` и без тегов

## 4. Тесты и стенд

- [ ] 4.1 Юнит-тесты: `http_version_test.go`, `http1_test.go`, `http3_test.go`, `std_config_quic_test.go`
- [ ] 4.2 `go test -tags with_xhttp,with_quic,with_utls -ldflags "-checklinkname=0" ./transport/v2rayxhttp/`
- [ ] 4.3 Стенд `lx-test/xhttp_h3/`: Xray h3 на loopback, конфиг репортёра #25; `packet-up`, `stream-up`, `stream-one` — по одному прогону
- [ ] 4.4 IMPLEMENTATION_REPORT.md; статус I в SPEC и FEATURE 002
- [ ] 4.5 Прогон на Android через LxBox — за владельцем
