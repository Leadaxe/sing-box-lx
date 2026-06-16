# IMPLEMENTATION_REPORT — 009 WIRESOCK_MASQUERADE_PROFILES

**Дата:** 2026-06-17 · **Статус:** Closed — код+тесты+DoD; **механизм проверен
вживую (туннель + трафик на 009)**; релиз `v1.13.13-lx.11` · **База:** `v1.13.13`

## Итог

Добавлены декларативные поля маскировки **`id` / `ip` / `ib`** (домен / протокол /
браузер) в стиле [WireSock Secure Connect](https://www.wiresock.net/) на
`wireguard`-endpoint. Это **сахар над AmneziaWG `I1`**: на уровне option/device они
разворачиваются в `I1` CPS-строку, которую существующий vendored-стек уже умеет
потреблять. Профили: **quic / dns / stun / sip**. Рантайм device **не меняется**.

## Что портировано и откуда

Структуры протокол-пакетов портированы из open-source реализации самого WireSock —
[`wiresock/amneziawg-install`](https://github.com/wiresock/amneziawg-install)
(`amneziawg-proxy/src/transform.rs`, MIT, Copyright (c) WireSock):

- **QUIC** ← `apply_quic_padding_short` — 1-RTT short header (`0x40|spin<<5|key_phase<<2|pn_len` + энтропия).
- **DNS** ← `apply_dns_padding` / `write_dns_opt_response` — EDNS **OPT response** (flags `0x8180`, QDCOUNT=1/ARCOUNT=1, OPT TYPE 41, CLASS 1232, опция `0xFDE9`).
- **STUN** ← `apply_stun_padding` — Binding Success Response (`0x0101`, cookie `0x2112A442`, XOR-MAPPED-ADDRESS + SOFTWARE).
- **SIP** ← `apply_sip_padding` — `200 OK` response (Via/From/To/Call-ID/CSeq + Content-Length).
- **LDH-валидатор** ← `quic_handshake.rs::is_valid_sni_hostname`.

## Развилки — как решены (и почему)

- **Механизм: I1 CPS, НЕ S1–S4 padding.** Решено пользователем: S1–S4 **невозможен
  против Cloudflare WARP**, ради упрощения коннекта к которому фича существует. I1
  standalone-приманка — единственный жизнеспособный вариант. Сабмодуль
  `submodules/wireguard-go` **не трогаем** (и ребейз дешевле — CONSTITUTION §2).
- **QUIC = 1-RTT short header, НЕ Initial.** Как реально делает WireSock: нет SNI,
  нет ClientHello, нет JA3. Честно, без «byte-perfect». (Подход прошлой сессии с
  `mini_quic_generator`-Initial отвергнут — он был на неверном референсе.)
- **`ib` — без fingerprint.** В short-header QUIC нет ClientHello, имитировать JA3
  нечем. Поле валидируется (`chrome|firefox|curl`, только при `ip=quic`); его
  **единственный** эффект — выбор фикс-битов spin/key_phase первого байта (косметика,
  эти биты per-packet random в реальном QUIC). Задокументировано честно.

## Ключевое отличие модели от WireSock

WireSock — **серверный** прокси, переписывает leading S1–S4 padding датаграммы с
реальным WG-ciphertext за padding (сидит PRNG от него, длины покрывают хвост). У нас
— **standalone I1-приманка** (`send.go:135`, `Obfuscate(buf, nil)`, src=nil): хвоста
нет. Поэтому весь датаграм = CPS-вывод, длины (DNS RDLENGTH/OPTION-LENGTH, STUN
message-length, SIP Content-Length=0) покрывают **только реально записанные байты**,
а энтропия — криптослучайная (`<r N>`), не payload-seeded LCG. Это честный decoy,
не байт-в-байт replay WireSock-трафика.

## Изменённые / новые файлы

| Файл | Зона | Что |
|------|------|-----|
| `option/wireguard_awg.go` | lx | +`Id/Ip/Ib string` (json `id`/`ip`/`ib`); `IsSet()` учёл автоматически |
| `transport/wireguard/masque_awg.go` | lx, `with_awg` | `masqueI1` диспетчер + валидация (LDH/Ip/Ib/конфликт-с-I1) + `cpsBuilder` + DNS/STUN/SIP генераторы |
| `transport/wireguard/masque_quic_awg.go` | lx, `with_awg` | QUIC 1-RTT short header |
| `transport/wireguard/device_awg.go` | lx, `with_awg` | вызов `masqueI1` в `awgIpcLines`, подстановка как `i1` |
| `transport/wireguard/masque_awg_test.go` | lx, `with_awg` | структурные тесты (обратный парсинг), валидация, инъекция, browser |
| `transport/wireguard/masque_cps_test.go` | lx, `with_awg` | test-only верный реплей CPS-парсера (зеркало `newObfChain`) |
| `docs/lx-config.md` | lx | секция «Masquerade id/ip/ib» |
| `SPECS/README.md` | lx | roadmap-строка 009 |

Сабмодуль — без изменений (`git -C submodules/wireguard-go status` пуст).

## Валидация (fail-fast в `masqueI1`)

Конфликт с явным `I1`; пустой `Ip`/неизвестный `Ip`; **`Id` обязателен только для
`dns`/`sip`** (там он идёт на провод как QNAME / SIP-host), для `quic`/`stun` —
опционален (decoy без домена); **строгий LDH** домена применяется **всегда, когда
`Id` задан** (зеркало `is_valid_sni_hostname`: метки alnum+`-`+`_`, без edge-дефиса,
≤63, всего ≤253, трейлинг-дот ок) — это **security-граница** (домен идёт в SIP-текст
и DNS QNAME, инъекция через control-байты/метасимволы); неизвестный `Ib`; `Ib` без
`ip=quic`. Все → понятная ошибка на уровне `check`/старта, без паники.

## Приёмка (DoD)

- ✅ `go build ./...` без тегов — ок.
- ✅ `go build -tags "with_wireguard with_gvisor with_awg" ./cmd/sing-box` — ок.
- ✅ `go test -tags with_awg ./transport/wireguard/...` — зелёный (структурные
  тесты + валидация + инъекция, не тавтологии: обратный парсинг каждого профиля).
- ✅ **Каждый профиль принят реальным `newObfChain`** — transient-тест в
  `submodules/wireguard-go/device` (`GOFLAGS=-mod=mod`), все 7 спеков (4 quic + dns
  + stun + sip) парсятся и обфусцируются с `src=nil`; затем удалён, go.mod/go.sum
  откачены, сабмодуль чист.
- ✅ `sing-box check` (бинарь со всеми тегами): quic/dns/stun/sip — ок; конфликт с
  `i1`, CRLF-инъекция в домен, неизвестный `ip` — отвергнуты понятной ошибкой.
- ✅ Gating: id/ip/ib без `with_awg` → «AmneziaWG (awg) support is not included…».
- ✅ Регресс: `lx-test/config/awg2_basic.json` / `awg2_ranged.json` — валидны.
- ✅ `gofmt -l` всех lx-файлов — пусто.
- ✅ **Адверсариальный ревью** (workflow, 6 независимых агентов: QUIC/DNS/STUN/SIP/
  валидация/честность-заявлений, с верификацией находок) — **0 находок**.

## Зона касания upstream (для ребейза)

Все новые файлы под `with_awg` (в upstream их нет) → конфликтов на ребейзе не дают.
`option/wireguard_awg.go` — lx-файл целиком. `device_awg.go` — lx-файл под тегом.
Сабмодуль **не трогали**.

## Вне скоупа / ограничения (честно)

- **Механизм проверен вживую: туннель встаёт и трафик идёт на сборке с 009.**
  Локально также: `check` + структурный парсинг + реальный `newObfChain`.
  Систематическая полевая проверка каждого профиля против конкретного DPI —
  отдельный шаг (подтверждён рабочий коннект, не A/B по профилям).
- Серверная сторона (probe-response) — вне скоупа (client-only, CONSTITUTION).
- Byte-identical replay именно WireSock-трафика и JA3/uTLS fingerprint — вне скоупа
  (последнего нет даже в WireSock QUIC).
