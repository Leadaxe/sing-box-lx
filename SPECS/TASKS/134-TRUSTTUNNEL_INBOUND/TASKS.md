# TASKS: 134 — TRUSTTUNNEL_INBOUND

**Фича:** [018-TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) · план — [PLAN.md](PLAN.md)

Тег `with_trusttunnel` в `LX_TAGS` с 2026-10-16 — inbound в desktop- и роутерных бинарях; в AAR тега нет.

- [x] 1. `Service`: accept-петля H2 (TLS → ALPN `h2` → `http2.Server.ServeConn`, 1000 стримов, idle 600 с), HTTP/1.1 не обслуживается, `UpdateUsers`, `Close`
- [x] 2. `ServeHTTP`: 404 на не-CONNECT без `proxy-authenticate`; 407 + `proxy-authenticate`; `_check` → 200; `_icmp` → 404; authority без порта → 400
- [x] 3. `ServerConn`: отложенный 200 на первом `Read`/`Write`; `Fail` → 502 + `X-Warning` 300/301/302; `Close` до ответа → 502/300; нет записи после выхода хендлера
- [x] 4. `service_udp.go`: потоки по 4-кортежу, нулевой источник → адрес сессии, ответные кадры под мьютексом, конец стрима закрывает потоки, битый кадр закрывает стрим
- [x] 5. `ServeQUIC` под `with_quic` (`qtls.ListenEarly`, 4096 стримов, без 0-RTT, idle 600 с, congestion control) и заглушка
- [x] 6. Тесты ядра (общие с 133, H2 и H3): TCP, UDP два потока на одном стриме, 407 как ошибка dial, `_check`, отложенный 502, 400/404, 404 без `proxy-authenticate` на GET, нет HTTP/1.1, 1000 (H2) / 4096 (H3) одновременных стримов
- [x] 7. `option/trusttunnel.go`: `TrustTunnelInboundOptions`
- [x] 8. `protocol/trusttunnel/inbound.go`: валидация §3, слушатели по `network`, `RouteConnectionEx` с `onClose` → `Fail`, `RoutePacketConnectionEx`, metadata; `inbound_quic.go` — если понадобится
- [x] 9. Сборки и `go vet`: без тега, с тегом без `with_quic`, с обоими, AAR — 2026-10-16 зелёные: без тега, `with_trusttunnel` без `with_quic`, полный `LX_TAGS` (darwin/arm64, linux/arm64, windows/amd64, `CGO_ENABLED=0`), `go vet` под тремя наборами; `sharedTags` AAR тега не несёт (grep), AAR собран lx-release на теге lx.21
- [x] 10. Стенд: официальный клиент и наш outbound через inbound (H2, H3, `auto` против H2-only, ICMP у клиента), R4 при `sniff`, R7 `curl`, idle 600 с, route rules — снято 2026-10-15/16 (клиент 1.1.11, loopback):
  - `auto` против `network: tcp`: клиент пингует только `tcp://`, выбирает H2, оба `curl` проходят; большой UDP-кадр 9100 байт от клиента и от нашего outbound (H3);
  - R4 при `{"action": "sniff"}`: CONNECT на закрытый порт — 200 уже ушёл, dial упал, поток закрыт, `curl` получает ошибку за миллисекунды (наш outbound H2/H3 и официальный клиент), ничего не виснет, клиент продолжает работать;
  - idle 600 с (H2): outbound с `idle_timeout: "0"`, пауза 640 с — inbound закрыл сессию, следующий dial 7 мс с прозрачной пересборкой, ошибок нет; H3 не снимался;
  - route: `inbound: tt-in` + `ip_is_private: true` → `reject` — приватная цель отбита (`502` / `X-Warning: 300` у нашего outbound), интернет через тот же inbound жив;
  - не снято: ICMP у клиента — у CLI-клиента 1.1.11 ручки ICMP нет.
- [x] 11. `docs-lx/protocols-transports.md` и `.ru.md`: раздел inbound
- [x] 12. Решения владельца по §7 — В1–В5 реализованы по предложениям, приняты вместе с релизом lx.21; статус I (2026-10-29, проверено владельцем на живой связке); отдельный IMPLEMENTATION_REPORT не писался
- [x] 13. Тег в `LX_TAGS` вместе с 133 — 2026-10-16 (решение владельца)
