# SPEC 133 — Outbound `trusttunnel`: TCP + UDP поверх HTTP/2 и HTTP/3

> 🌐 English version: **[SPEC.md](SPEC.md)**.
>
> **Язык.** Фича пришла снаружи (PR [#45](https://github.com/Leadaxe/sing-box-lx/pull/45), обсуждение в [#46](https://github.com/Leadaxe/sing-box-lx/issues/46)), поэтому основной язык её документов — английский. Русская копия (`*.ru.md`) лежит рядом с каждым файлом, чтобы фичу было проще сопровождать.

**Фича:** [TRUSTTUNNEL](../../FEATURES/018-TRUSTTUNNEL/FEATURE.ru.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — клиентский outbound |
| Статус | N (new) — черновик на обсуждение; решены В1 (своя реализация), В4 (desktop + роутеры), В5 (подпись «TrustTunnel»), остальное в §7 открыто |
| Ветка | `lx` |
| Build-tag | `with_trusttunnel` |
| Обсуждение | [Leadaxe/sing-box-lx#46](https://github.com/Leadaxe/sing-box-lx/issues/46) |
| Источник | PR [Leadaxe/sing-box-lx#45](https://github.com/Leadaxe/sing-box-lx/pull/45) (закрыт: три протокола без задачи, сборка ломалась); владелец 2026-10-15: начать с TrustTunnel |
| Связанные | [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (своя H2/H3-машинерия `CONNECT`) · [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md) (health check в простое) · [CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) (узел как звено) · [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md) (условие снятия) |

## 0. TL;DR

Новый outbound `trusttunnel` за тегом `with_trusttunnel`: TCP как `CONNECT host:port`,
UDP как `CONNECT _udp2` с кадрами протокола, транспорт HTTP/2 поверх TLS или HTTP/3
поверх QUIC (`quic: true`). Схема ключей — та, что уже ходит в husi и его производных.
Inbound, ICMP, `client_random`-правила и `auto`-выбор H2/H3 — вне задачи. Реализация
своя, поверх H2/H3-кода MASQUE, без сторонней библиотеки (§7, В1).

## 1. Проблема и критерии §3.1

- **(а1) Нужна.** Запрос пришёл от downstream-клиента (NekoBox/NyameBox строит
  следующую версию на sing-box-lx и уже собирает протокол в своём форке — PR #45).
  В апстриме открыт запрос SagerNet/sing-box#3723 (январь 2026, ~30 реакций, полевые
  отзывы: работает в РФ, в Иране — нестабильно). Запроса от приоритетных потребителей —
  лаунчера, LxBox, роутеров — **пока нет**; это решение владельца.
- **(а2) Апстрим не даёт.** TrustTunnel нет ни в `upstream/stable`, ни в `testing`,
  ни в `unstable` (проверено 2026-10-15); на #3723 ответа мейнтейнеров нет.
- **(а3) Дешевле, чем альтернатива.** Альтернатива для пользователя — mihomo или husi.
  Цена у нас — новые файлы плюс маркированные блоки в `include/registry.go` (3 строки)
  и в `constant/proxy.go` (2 строки); `go.mod` не трогается (§5).
- **(б) Изоляция** — §5.
- **(в) Spec Kit** — фича [018](../../FEATURES/018-TRUSTTUNNEL/FEATURE.ru.md) написана первой.

## 2. Протокол (провод, по `TrustTunnel/PROTOCOL.md` v1.0 и официальному клиенту)

### 2.1 Транспорт

| Режим | Как | ALPN |
|-------|-----|------|
| HTTP/2 | TLS поверх TCP; начальное окно потока 131072 | `h2` (клиент предлагает `h2,http/1.1`, но всё кроме `h2` рвёт) |
| HTTP/3 | QUIC v1, без 0-RTT, без QUIC datagrams | `h3` |

Сервер маршрутизирует по SNI и рвёт неизвестный SNI на TLS-уровне. HTTP/1.1 сервер
принимает только для браузеров; туннельный клиент его не использует.

### 2.2 Авторизация

`proxy-authorization: Basic base64(user:pass)` на **каждом** `CONNECT`, включая
`_udp2` и `_check`. Сервер сравнивает строку base64 побайтно со стандартной кодировкой
**с паддингом**. Отказ — 407 (настраивается на 403/404/405) с `proxy-authenticate`.

### 2.3 TCP

Обычный `CONNECT` (не extended, без `:protocol`), `:authority` = `host:port`, IPv6 как
`[addr]:port`. Заголовки: `user-agent: <платформа> <приложение>`, `proxy-authorization`.
200 — поток становится сырым каналом; иначе — обычно 502 с `X-Warning: <код> - <текст>`:
300 connect failed, 301 host unreachable, 302 timeout, 310/311 DNS non-routable / loopback.

### 2.4 UDP

Один поток на сессию: `CONNECT` с `:authority: _udp2` (литерально, без порта),
кадры на потоке, big-endian:

```
клиент → сервер:  u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | u8 L | app_name[L] | payload
сервер → клиент:  u32 len | src_ip[16] | src_port u16 | dst_ip[16] | dst_port u16 | payload
```

`len` не включает себя; IPv4 — 12 нулевых байт + 4 байта адреса. В кадре только IP:
домен резолвится клиентом. Сервер ведёт потоки по 4-кортежу, idle 120 с по спецификации.
Нулевой `src` допустим (так делает mihomo).

### 2.5 Health check

`CONNECT _check`, сервер сразу отвечает 200. Официальный клиент шлёт его по событиям
туннеля, не чаще `timeout`, дедлайн 7 с; любые данные от сервера отменяют ожидающую
проверку; провал запускает переподключение с backoff.

## 3. Контракт конфига

```json
{
  "type": "trusttunnel",
  "tag": "tt-node",
  "server": "vpn.example.com",
  "server_port": 443,
  "username": "user",
  "password": "secret",
  "network": "",
  "quic": false,
  "quic_congestion_control": "bbr",
  "health_check": false,
  "tls": { "enabled": true, "server_name": "vpn.example.com" }
}
```

| Ключ | Тип | Дефолт | Валидация |
|------|-----|--------|-----------|
| `server`, `server_port` | `ServerOptions` | обязательны | как у всех |
| `username`, `password` | `string` | обязательны | пусто — ошибка старта |
| `network` | `NetworkList` (`tcp`/`udp`) | оба | как у всех |
| `quic` | `bool` | `false` | `true` + `tls.utls.enabled` — ошибка старта |
| `quic_congestion_control` | `string` enum `bbr,cubic,reno` | `bbr` | неизвестное — ошибка старта (не тихий откат на BBR) |
| `health_check` | `bool` | `false` | — |
| `tls` | `OutboundTLSOptions` | обязателен | `enabled: false` — `C.ErrTLSRequired`; ALPN, если задан, обязан содержать `h2` (H2) или `h3` (H3) |
| Dial Fields | `DialerOptions` | — | как у всех |

Схема совпадает с husi `TrustTunnelOutboundOptions` (кроме `icmp` в `network` — у нас
вне задачи; значение `icmp` — ошибка старта с понятным текстом).

## 4. Требования

- **R1. TCP.** `DialContext("tcp", dst)` открывает `CONNECT dst` в общей сессии узла и
  **возвращается после ответа сервера**: 200 — `net.Conn`; иначе — ошибка с кодом и
  `X-Warning`. `ctx` вызова ограничивает и ожидание ответа.
- **R2. UDP.** `ListenPacket` и `DialContext("udp")` открывают `CONNECT _udp2`; домен
  назначения резолвится через dial-поля узла (`domain_resolver` → DNS-роутер), пустой
  ответ резолва — ошибка, не паника; кадры по §2.4; `WriteTo` на домен без резолва —
  ошибка.
- **R3. Транспорт.** Одна сессия на узел, поднимается лениво по первому запросу, переживает
  отдельные потоки; при обрыве — пересоздаётся следующим запросом. H2 проверяет, что
  согласован `h2`. H3 — QUIC v1 поверх UDP-сокета из dial-полей (`detour` работает).
- **R4. Жизненный цикл.** Конструктор не открывает сеть; `Start(stage, scope)` /
  `Close()` по модели scoped lifecycle (SPEC 117). `Close` закрывает сессию и все потоки.
- **R5. Смена сети.** `InterfaceUpdated()` сбрасывает сессию.
- **R6. Health check.** При `health_check: true` — `CONNECT _check` не чаще раз в 7 с,
  только пока у узла есть живые потоки или был запрос в последние 60 с; в простое
  таймер стоит. Провал проверки сбрасывает сессию.
- **R7. Авторизация.** Basic по §2.2, стандартный base64 с паддингом.
- **R8. Без тега.** Без `with_trusttunnel` тип регистрируется заглушкой с ошибкой
  `trusttunnel is not included in this build, rebuild with -tags with_trusttunnel`;
  остальное ядро — апстрим байт в байт.
- **R9. Сборки.** Тег зашит в `LX_TAGS` — desktop-бинари и статические сборки для роутеров
  (решение владельца 2026-10-15, §7 В4). В AAR (`sharedTags`) тега нет. Сборка без тега и
  `go vet` под обоими наборами тегов зелёные.

## 5. Изоляция и цена мержа

По шаблону CHAIN (SPEC 073), не MASQUE:

| Что | Где | Апстримный файл? |
|-----|-----|------------------|
| Константа `TypeTrustTunnel` | `constant/trusttunnel_lx.go` | нет |
| Опции | `option/trusttunnel_lx.go` | нет |
| Регистрация / заглушка | `include/lx_trusttunnel.go` + `include/lx_trusttunnel_stub.go` | нет |
| Вызов регистрации | `include/registry.go` — блок `// lx:begin trusttunnel … // lx:end trusttunnel` | **да, 3 строки** |
| Протокол | `protocol/trusttunnel/` (H3-часть под `with_quic` внутри пакета) | нет |
| Имя в UI | `constant/proxy.go` `ProxyDisplayName` — `case` за `// lx:begin trusttunnel` | **да, 2 строки** (§7, В5) |
| Теги | `Makefile.lx` `LX_TAGS` | нет (lx-файл) |

`include/quic.go` и `include/quic_stub.go` не трогаются.

## 6. Критерии приёмки

1. Сборка без `with_trusttunnel`, с ним, и AAR (без тега) — зелёные; `go vet` под обоими наборами.
2. Юниты пакета: кодек кадров `_udp2` (IPv4/IPv6, граничные длины, обрезанный кадр),
   разбор ошибок `X-Warning`, Basic-auth с паддингом, валидация конфига (§3).
3. Стенд в `lx-test/`: официальный `trusttunnel_endpoint` (Apache-2.0, релиз-бинарь) в
   контейнере или локально; через узел — HTTP(S) по TCP, DNS по UDP, большой UDP-кадр;
   H2 и H3; неверный пароль — ошибка из `DialContext`, а не из первого `Read`.
4. Дайл через `detour` и как звено `chain` работает.
5. В простое при `health_check: true` нет исходящих пакетов (наблюдается по счётчикам или
   pcap на стенде).
6. Живой сервер (свой VPS или публичный тестовый) — `curl` через узел на desktop.

## 7. Открытые вопросы (на обсуждение)

- **В1. Реализация — решено владельцем 2026-10-15: своя**, в `protocol/trusttunnel/`
  (~700–900 строк, оценка) поверх H2/H3 `CONNECT`-кода MASQUE. Поведение и версии под
  контролем, R1/R6 закрываются сразу, новых модулей в `go.mod` нет. Библиотека
  `xchacha20-poly1305/sing-trusttunnel` v0.3.2 не берётся: `Dial`/`ListenPacket` не ждут
  ответа `CONNECT` (нарушает R1), health check тикает в простое (R6), нет проверки ALPN;
  один мейнтейнер на sing-box 1.15-alpha. Её код и mihomo-копию не копировать — сверка
  провода по `PROTOCOL.md` и официальному клиенту.
- **В2. Схема ключей.** Совпадение с husi (`quic` bool, как у апстримного `naive`) против
  отдельного `transport: h2|h3`. Предложение — схема husi: конфиги NekoBox/husi
  переносятся без правок.
- **В3. `client_random`-правила.** Нужны, только если встретятся серверы с такими
  правилами (по умолчанию их нет). Предложение — отдельная задача по живому запросу,
  через uTLS-форк.
- **В4. Поверхности — решено владельцем 2026-10-15:** сразу desktop и роутеры (`LX_TAGS`).
  AAR (LxBox) — не в этой задаче.
- **В5. `ProxyDisplayName` — решено владельцем 2026-10-15:** маркированный `case` в
  `constant/proxy.go` (2 строки), UI показывает «TrustTunnel». Тем же решением подпись
  «Chain» получил `chain` (SPEC 073 §8).
- **В6. Inbound.** Вне задачи; заводить ли вторую задачу сразу или по запросу.

## 8. Условие снятия

Апстрим sing-box добавляет outbound `trusttunnel` в той поверхности, которую мы
собираем, — наша реализация снимается в пользу апстримной (§3.4), с миграцией ключей,
если схема разойдётся.

## 9. Источники

- Спецификация: `TrustTunnel/TrustTunnel` `PROTOCOL.md`, `CONFIGURATION.md`, `DEEP_LINK.md`
  (коммит `48f424c`, 2026-10-09).
- Официальный клиент: `TrustTunnel/TrustTunnelClient` (коммит `64e2e5b`, 2026-10-15).
- Сторонние: mihomo `transport/trusttunnel` (копия sing-trusttunnel v0.1.1),
  husi `libcore/plugin/trusttunnel`, `xchacha20-poly1305/sing-trusttunnel` v0.3.2.
