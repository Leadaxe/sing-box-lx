# TASKS: 133 — TRUSTTUNNEL_OUTBOUND

**Фича:** [018-TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) · план — [PLAN.md](PLAN.md)

Реализация экспериментальная: тег `with_trusttunnel` в `LX_TAGS` с 2026-10-16 — тип в desktop- и роутерных бинарях, интерфейс ещё может измениться; в AAR тега нет.

- [x] 1. Ядро, общая часть: `wire.go` (кодек кадров `_udp2` обеих сторон, границы, усечённый кадр → `io.ErrUnexpectedEOF`), `auth.go`, `errors.go`, `useragent.go`
- [x] 2. Клиент H2: одна сессия на `http2.ClientConn`, проверка ALPN `h2`, `StrictMaxConcurrentStreams`, без PING; `DialContext` возвращает conn после 200, иначе `*StatusError`
- [x] 3. Клиент H3 под `with_quic` (`qtls.Dial`, ALPN `h3`, congestion control, без keepalive) и заглушка `ErrQUICNotIncluded`
- [x] 4. Сессия: ленивое открытие, idle-teardown без открытых потоков, stale-бюджет 3 с + одна пересборка, health check по событию (30 с / 7 с), 403/404/405/407 и GOAWAY `0x1F` рвут сессию, ожидание слота вместо второй сессии, `Reset`
- [x] 5. `PacketConn` поверх `_udp2`: нулевой источник, домен — ошибка, битый кадр закрывает поток
- [x] 6. Тесты ядра: кодек (IPv4/IPv6, максимум, усечённый, слишком длинный, предел `::/96`), Basic с паддингом, `StatusError`, user-agent; end-to-end H2 и H3 против `Service`: TCP-эхо, UDP два потока, 407 → новая сессия, `_check`, отложенный 502/300, 400 и 404, предел потоков без второй сессии, stale-сессия, idle-teardown, health check (H2 — ещё и провал проверки)
- [x] 7. `option/trusttunnel.go`: `TrustTunnelOutboundOptions`; глобальный дефолт `idle_timeout` — по решению В7
- [x] 8. `protocol/trusttunnel/outbound.go`: валидация §3, TLS, dial fields, резолв UDP (R2), `Start`/`Close` (R4), `InterfaceUpdated` → `Reset` (R5), `AppName` (В10)
- [ ] 9. `include/trusttunnel{,_stub}.go`; помеченные строки в `include/registry.go` и `constant/proxy.go` отдельными коммитами
- [ ] 10. Сборки и `go vet`: без тега, с тегом, с тегом без `with_quic`, AAR — 2026-10-16 зелёные: без тега, `with_trusttunnel` без `with_quic`, полный `LX_TAGS` (darwin/arm64, linux/arm64, windows/amd64, `CGO_ENABLED=0`), `go vet` под тремя наборами; `sharedTags` AAR тега не несёт (grep), сама сборка AAR не прогонялась
- [x] 11. Стенд `lx-test/trusttunnel/` против официального endpoint: HTTP(S) по TCP, DNS по UDP, большой UDP-кадр, H2 и H3, неверный пароль, idle/stale (`client_listener_timeout_secs`, DROP), `detour` и `chain`, нет пакетов в простое — снято 2026-10-15/16 (endpoint 1.1.0, клиент 1.1.11, loopback; протокол — README стенда):
  - большой UDP-кадр: эхо 9100 байт туда-обратно (потолок отправителя — macOS `net.inet.udp.maxdgram` 9216, не туннель) по H2 и H3, к endpoint и к нашему inbound;
  - stale: endpoint с `client_listener_timeout_secs = 5`, пауза 10 с — первый dial 3 мс, сессия пересобрана до CONNECT (закрытие замечено), ошибок нет (H2 и H3); имитация NAT-обрыва заморозкой потока в `relay.py` — CONNECT упирается в бюджет 3,0 с, пересборка, 200 (H2 и H3); `urltest` через узел при сессии, закрытой сервером, — `available`, ложного провала нет;
  - `detour` через `socks` к соседнему sing-box — H2 (TCP) и H3 (UDP ASSOCIATE); `chain` обоих порядков: `["socks-hop", "tt-out"]` (`clone created: tt-out@1`) и `["tt-out", "socks-hop"]`;
  - простой 2 мин при живой сессии: релей-счётчик не видел ни байта в обе стороны, H2 и H3 × `health_check` on/off; idle-teardown на 5-й минуте — по H2 один TLS-alert в каждую сторону, по H3 беззвучно;
  - дефект, найденный и исправленный: QUIC `MaxIdleTimeout` был равен `idle_timeout`, и молчащий открытый поток по H3 умирал через окно (по H2 — нет); теперь 600 с (цифра сервера), окно применяет только `idleWatcher`; тест `IdleTeardown` дополнен эхо после паузы.
  - не снято: §6.7 живой сервер.
- [x] 12. `docs-lx/protocols-transports.md` и `.ru.md`: раздел, таблица маппинга, асимметрия DNS, предел IPv6 `::/96`
- [x] 13. Отпечаток H2 (R13): преамбула как у Chrome в пределах `x/net/http2` — `newHTTP2Transport` (`http.HTTP2Config` окна 6291456 / 15663105 + `http2.ConfigureTransports`, `MaxHeaderListSize` 262144, `MaxDecoderHeaderTableSize` 65536); `TestHTTP2Preface` читает с провода SETTINGS `2:0;4:6291456;5:16384;6:262144;1:65536` и `WINDOW_UPDATE 15663105`; стенд 2026-10-16: `check-out` по H2 к официальному endpoint зелёный, также с `tls.utls` `chrome`
- [ ] 14. Решения владельца по В7–В11
- [x] 15. Тег в `LX_TAGS` (R9) — 2026-10-16, решение владельца: выходит в следующем релизе как экспериментальная; релиз-нота — подраздел в черновике `docs-lx/releases/v1.14.3-lx.21.md`
- [ ] 16. IMPLEMENTATION_REPORT, статус задачи и Roadmap
