# PLAN: 133 — TRUSTTUNNEL_OUTBOUND

**Фича:** [018-TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.md) · контракт — [SPEC.md](SPEC.md) · серверная половина — [134](../134-TRUSTTUNNEL_INBOUND/PLAN.md)

> Всё за тегом `with_trusttunnel`; тег в `LX_TAGS` (`Makefile.lx`) с 2026-10-16 (решение
> владельца, R9): desktop- и роутерные бинари несут тип `trusttunnel`, AAR — нет. Открытые
> вопросы §7 (В7–В11) этим не закрыты.

## Этапы

1. **Ядро протокола** `transport/trusttunnel/` — клиентская половина этой задачи и серверная
   половина задачи 134 в одном пакете: тесты ядра гоняют клиент против своего сервера. Пакет не
   импортирует ничего из sing-box (проверка: `go list -deps ./transport/trusttunnel/ | grep sing-box/`
   печатает только сам пакет); quic-go и sing-quic — только в файлах под `with_quic`.
2. **Адаптеры sing-box**: опции, `protocol/trusttunnel/outbound.go`, регистрация, помеченные строки
   в двух апстримных файлах.
3. **Стенд и документация.**

## Файлы

| Файл | Этап | Что меняется |
|------|------|--------------|
| `transport/trusttunnel/wire.go` | 1 | Константы (`_udp2`, `_check`, `_icmp`, имена заголовков); кодек кадров `_udp2` обеих сторон без аллокаций на запись (`buf.Buffer.ExtendHeader`), чтение со scratch-буфером; границы: `len` ≤ заголовок + 255 + 65535, кадр короче заголовка или усечённый — ошибка, поток закрывается |
| `transport/trusttunnel/auth.go` | 1 | `BasicAuthorization` — стандартный base64 с паддингом; таблица пользователей сервера (134) |
| `transport/trusttunnel/errors.go` | 1 | `StatusError` (код, `X-Warning` код + текст, `X-Adguard-Vpn-Error`), `ErrAuthenticationRequired` (407, GOAWAY / H3 `0x1F`), `IsAuthenticationRequired`; для 134 — ошибка dial → `X-Warning` 300/301/302 |
| `transport/trusttunnel/useragent.go` | 1 | Платформа из `GOOS` (`Mac`/`Windows`/`Linux`/`Android`/`iOS`), `<platform> <app>`, `<platform> _udp2`, `<platform> unknown` (R10) |
| `transport/trusttunnel/client.go` | 1 | `Client`: одна ленивая сессия под мьютексом; `DialContext` возвращает conn только после 200; `ListenPacket`; `Check`; `Reset`; `Close`. Idle-teardown (тикер idle/4, только без открытых потоков); stale-сессия (тишина дольше idle-окна, без `idle_timeout` — 5 мин) получает бюджет 3 с, затем одна пересборка и повтор в `ctx` вызывающего; health check по событию (тишина > 30 с перед dial, дедлайн 7 с, провал рвёт сессию); 403/404/405/407 рвут сессию |
| `transport/trusttunnel/client_http2.go` | 1 | Сессия на одном `http2.ClientConn` (`Transport.NewClientConn`): проверка ALPN `h2`, `StrictMaxConcurrentStreams: true`, без PING, без сжатия; CONNECT с телом-пайпом |
| `transport/trusttunnel/client_udp.go` | 1 | `PacketConn` (`N.NetPacketConn`, `FrontHeadroom`): кадр на датаграмму, нулевой источник, домен в назначении — ошибка; битый кадр закрывает поток |
| `transport/trusttunnel/client_quic.go`, `client_quic_stub.go` | 1 | Под `with_quic`: UDP-сокет через `Dialer.DialContext("udp")` + `qtls.Dial`, ALPN `h3`, congestion control (`bbr` / `cubic` / `new_reno`, алиас `reno`), без keepalive; `http3.ClientConn` + `OpenRequestStream`. Заглушка: `ErrQUICNotIncluded` |
| `transport/trusttunnel/service*.go` | 1 | Серверная половина — [PLAN 134](../134-TRUSTTUNNEL_INBOUND/PLAN.md) |
| `transport/trusttunnel/*_test.go` | 1 | Кодек, Basic, `StatusError`, user-agent; end-to-end H2 и H3 (`service_quic_test.go` под `with_quic`) |
| `option/trusttunnel.go` | 2 | `TrustTunnelOutboundOptions` (§3: `ServerOptions`, `username`, `password`, `network`, `quic`, `quic_congestion_control`, `health_check`, `idle_timeout` как `*badoption.Duration` — «не задан» ≠ `0`, `OutboundTLSOptionsContainer`, `DialerOptions`) и `TrustTunnelInboundOptions` (134) |
| `option/lx.go` | 2 | Глобальный дефолт `idle_timeout` рядом с `lx.masque.idle_timeout`, по умолчанию 5 мин — только если владелец примет предложение В7 |
| `protocol/trusttunnel/outbound.go` | 2 | Валидация §3 (IP в `server` без `tls.server_name`, пустой `password`, `icmp` в `network`, `quic` + `utls`, ALPN без `h2`/`h3`, неизвестный congestion control); `tls.NewClient`; dialer из dial fields; `trusttunnel.NewClient` с `AppName: "sing-box-lx"` (В10); TCP → `DialContext`, UDP → резолв домена через dial fields / DNS-роутер (R2) и `ListenPacket`; `Start(stage, scope)` / `Close` (R4, SPEC 117); `InterfaceUpdated` → `Reset` (R5) |
| `protocol/trusttunnel/inbound.go`, `inbound_quic.go` | 2 | Адаптер сервера — [PLAN 134](../134-TRUSTTUNNEL_INBOUND/PLAN.md) |
| `include/trusttunnel.go` (`with_trusttunnel`), `include/trusttunnel_stub.go` | 2 | `registerTrustTunnelInbound` / `registerTrustTunnelOutbound`; заглушка регистрирует оба типа с ошибкой `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel` (R8) |
| `include/registry.go` | 2 | Два вызова в блоках `// lx:begin trusttunnel … // lx:end trusttunnel` (6 строк), отдельный коммит |
| `constant/proxy.go` | 2 | `TypeTrustTunnel` и `case` в `ProxyDisplayName` → «TrustTunnel» (4 строки), отдельный коммит |
| `docs-lx/protocols-transports.md`, `.ru.md` | 3 | Раздел outbound + inbound: конфиг, таблица маппинга экспорта TrustTunnel (FEATURE 018), асимметрия DNS (TCP-домены резолвит сервер, UDP — узел), предел IPv6 `::/96` в кадрах |
| `lx-test/trusttunnel/` | 3 | Стенд с официальными endpoint и клиентом уже есть (`stand.sh`); добавить сценарии §6 для нашего outbound |
| `docs-lx/lx-changelog.md`, `docs-lx/releases/*.md` | 3 | Релиз-нота lx.21 — подраздел TrustTunnel |

## Решения реализации

- **Ожидание слота — встроенное.** Свой семафор не нужен: `http2.ClientConn` со
  `StrictMaxConcurrentStreams: true` ждёт свободный поток на `cond` до отмены контекста запроса,
  QUIC `OpenStreamSync(ctx)` ждёт `MAX_STREAMS` сервера. Без `Strict` прямой `ClientConn` на пределе
  отвечает `errClientConnUnusable` — так пул решает открыть второе соединение; это нарушило бы R11.
- **H3-сокет — `Dialer.DialContext("udp")` + `qtls.Dial`**, как в `tuic` и `v2rayquic`: работает
  `detour`, QUIC-специфичные TLS-конфиги (ECH) берут свой путь.
- **Idle-teardown без пакетов на H3**: сокет закрывается раньше QUIC-соединения, `CONNECTION_CLOSE`
  не уходит. На H2 закрытие TCP — это FIN, его не избежать.
- **QUIC idle = idle-окно.** Клиентский `MaxIdleTimeout` равен idle-окну (`idle_timeout` узла,
  иначе `lx.trusttunnel.idle_timeout`, иначе 5 мин; при явном `0` — 5 мин), но не больше 600 с;
  keepalive нет. Итоговый idle — минимум клиента и сервера; H3-сессию, молча умершую за это время,
  следующий запрос видит мёртвой и пересобирает сразу, без бюджета 3 с.
- **«Тишина»** для stale-окна — время с последних полученных данных или ответа на запрос; для
  throttle health check — то же или момент последнего отправленного `_check`.
- **Повтор** — на любую транспортную ошибку переиспользованной сессии (одна пересборка); ответы
  сервера (`StatusError`) и отказ в авторизации не повторяются.
- **R6 «на пробуждение»** покрыт правилом тишины: первый dial после сна видит > 30 с и шлёт `_check`.
  Отдельной подписки на пробуждение нет: проверка в момент пробуждения будила бы радио без запроса.
- **Пустой `password`** ядро пропускает (библиотека, husi его допускает); отказ — в адаптере (В2).
- **Адреса conn** — `M.Socksaddr` (домен назначения сохраняется), не `*net.TCPAddr`; никогда не `nil`.

## Что не меняется

- `go.mod`, `go.sum` — новых модулей нет.
- `include/quic.go`, `include/quic_stub.go`, `protocol/naive/*`, `transport/masque/*`.
- `sharedTags` AAR (`cmd/internal/build_libbox/main.go`). `Makefile.lx` (`LX_TAGS`) получил
  `with_trusttunnel` 2026-10-16 (R9).
- Сборка без `with_trusttunnel` и конфиг без `type: trusttunnel` ведут себя как апстрим.

## Проверка

- Этап 1: `gofmt -l` и `gofumpt -l transport/trusttunnel/` пусто;
  `nice -n 15 go vet -p 4 [-tags with_quic] ./transport/trusttunnel/`;
  `nice -n 15 go test -p 4 [-race] [-tags with_quic] -ldflags=-checklinkname=0 ./transport/trusttunnel/`;
  граница пакета — `go list [-tags with_quic] -deps ./transport/trusttunnel/ | grep sing-box/`.
- Этап 2: `go build ./cmd/sing-box` без тегов; с `-tags "$(make -s -f Makefile.lx lx-print-tags)"` (тег уже в наборе);
  с `with_trusttunnel` без `with_quic`; AAR без изменений; `go vet` под всеми наборами;
  `sing-box check` на конфиге §3 и на каждой ошибке валидации.
- Этап 3: стенд по §6 (H2/H3, неверный пароль, idle/stale после `client_listener_timeout_secs` и
  после DROP, `detour` и `chain`, отсутствие пакетов в простое).
