# SPEC: 009 — WIRESOCK_MASQUERADE_PROFILES

Декларативные поля маскировки **`Id` / `Ip` / `Ib`** (домен / протокол / браузер) —
взятые из [WireSock Secure Connect](https://www.wiresock.net/), — которые
**на уровне конфига разворачиваются в AmneziaWG `I1` CPS-строку**. Вместо ручного
`i1=<b 0x...>` пользователь пишет осмысленные строки, а движок собирает
правдоподобный пакет-приманку нужного протокола.

> **Статус: SPEC переписан после реверта первой реализации.** Первая попытка
> (uncommitted) строилась на НЕВЕРНОМ референсе (`mini_quic_generator` — модель
> «расшифровываемый QUIC Initial с SNI») и была откачена. Этот SPEC опирается на
> **авторитетный open-source код самого WireSock** (см. §1.2) — их реальная модель
> мимикрии иная. Код генераторов пишется с нуля по этому SPEC.

Тип: **Feature**, расширение [003 AWG2_CLIENT_ENDPOINT](../003-F-C-AWG2_CLIENT_ENDPOINT)
и [005 AWG2_RANGED_MAGIC_HEADERS](../005-F-C-AWG2_RANGED_MAGIC_HEADERS).
Укладывается в CONSTITUTION §3 (новые файлы, тег `with_awg`; правка сабмодуля —
по выбору механизма, см. §3.1, держать минимальной/изолированной если нужна).

---

## 1. Проблема / контекст

### 1.1 Что такое Id/Ip/Ib (источник — WireSock)

| Поле | Имя | Значение |
|------|-----|----------|
| `Id` | **Domain** | домен для маскировки (популярный в регионе: `ozon.ru`, `google.com`…) |
| `Ip` | **Protocol** | протокол маскировки: **quic** \| **dns** \| **stun** \| **sip** |
| `Ib` | **Browser** | у WireSock — подпись браузера; **в их реальном QUIC-коде НЕ выражается JA3-fingerprint'ом** (см. §3.3). Поле принимается, но влияние ограничено — задокументировать честно. |

WireSock называет это «более понятной заменой переусложнённой для обычного
пользователя модели AmneziaWG I1–I5». То есть `Id/Ip/Ib` — **декларативная обёртка**,
порождающая CPS-пакет (`<b 0x..>`/`<r N>`/`<rc N>`), отправляемый как приманка.

### 1.2 Авторитетный референс (open source, MIT)

Сам WireSock Secure Connect — **проприетарный**. Но его команда публикует
open-source реализацию той же мимикрии:
**[`github.com/wiresock/amneziawg-install`](https://github.com/wiresock/amneziawg-install)**
(Rust, MIT), пакет `amneziawg-proxy/src/`:
- **`transform.rs`** — эталон построения протокол-пакетов: `apply_quic_padding_short`,
  `apply_dns_padding` (EDNS OPT), `apply_stun_padding`, `apply_sip_padding`.
- `quic_handshake.rs` — `is_valid_sni_hostname` (строгий LDH-чек; мы уже совпали).

**ВАЖНО:** WireSock — это **серверный UDP-прокси**, который (а) отвечает на DPI-пробы
валидным пакетом протокола и (б) переписывает **S1–S4 padding** под отпечаток
протокола. Их DNS/STUN/SIP — это **responses** (серверная сторона). Мы берём
**структуру протокола** из их кода; механизм у нас — **развилка I1 CPS vs S1–S4**
(см. §3.1), рекомендация — I1, но это решение реализатора.

### 1.3 Как WireSock реально строит каждый протокол (из `transform.rs`)

> **§146 amendment (2026-06-17):** `ip=quic` теперь эмитит **out-of-order
> фрагментированный QUIC Initial** (RFC 9001) с реалистичным ClientHello, где `id`
> идёт как **SNI** — см. `quic_initial_awg.go` / `quic_clienthello_awg.go` /
> `quic_crypto_awg.go` и LxBox-таск §146. Это **сменило** 1-RTT short-header дизайн,
> описанный ниже в этом пункте. `id` для `quic` теперь **обязателен** (становится
> SNI). Прежняя аргументация «short header не ломается на размере, поэтому лучше
> Initial» **развёрнута полевыми данными**: short header был эмпирически заблокирован
> реальным LTE-DPI; фрагментированный Initial обходит line-rate DPI тем, что первый
> CRYPTO-фрейм на проводе имеет offset≠0 (DPI парсит его как offset 0 → мусор →
> fail-open). Файл `masque_quic_awg.go` (`masqueQUICShortHeaderCPS` / `quicFirstByte`)
> **удалён**. Описание short-header ниже — исторический контекст, не текущее поведение.

- **QUIC = 1-RTT SHORT header, НЕ Initial/ClientHello.** Первый байт
  `0x40 | (spin<<5) | (key_phase<<2) | pn_len_bits` (form=0, fixed=1), далее
  псевдослучайные байты = «зашифрованный 1-RTT ciphertext». Нет version, нет
  length, **нет SNI**. Их причина: QUIC Initial требует датаграм ≥1200 байт
  (RFC 9000 §14.1), а 1-RTT short header не имеет минимума размера и доминирует в
  реальном QUIC, и его нельзя «сломать». → CPS: `<b 0x4X><r N>`.
- **DNS = EDNS OPT *response*, НЕ query.** Flags `0x8180` (QR=1,RD=1,RA=1,NOERROR),
  QDCOUNT=1, ARCOUNT=1; root-label question; OPT RR (TYPE `0x0029`, CLASS=1232,
  TTL=0), RDATA = одна неизвестная EDNS-опция код `0xFDE9` (IANA local-use),
  OPTION-LENGTH покрывает остаток → весь датаграм парсится как один DNS-месседж.
  TXID из первых 2 байт payload.
- **STUN = Binding Success Response.** type `0x0101`, length=байты атрибутов, magic
  cookie `0x2112A442`, 12-байт txn, затем XOR-MAPPED-ADDRESS (`0x0020`) +
  SOFTWARE (`0x8022`, printable ASCII, ≤124, 4-align).
- **SIP = *response*.** `SIP/2.0 <status>` + Via(branch)/From(tag)/To/Call-ID/CSeq,
  CRLF.

### 1.4 Семантика CPS-движка (что можно эмитить)

Из `submodules/wireguard-go/device/obf.go` (`newObfChain` + `obfBuilders`):
`<b 0xHEX>` статичные байты · `<r N>` N криптослучайных байт · `<rc N>` ASCII-буквы
· `<rd N>` цифры · `<t>` таймстамп · `<d>/<ds>/<dz>` data-теги (не нужны: src=nil).
I1-пакеты шлются как приманки перед handshake с `Obfuscate(buf, nil)` (src=nil,
реальных данных нет — `send.go:135`).

---

## 2. Цель

Пользователь задаёт на корне endpoint'а:

```jsonc
{ "type": "wireguard", /* ... */ "id": "www.google.com", "ip": "quic", "ib": "chrome" }
```

и получает сгенерированную `i1`-строку, как если бы вписал её руками.
`Id/Ip/Ib` — сахар над `I1`, **без нового рантайма** в device.

Профили (структура — по WireSock §1.3, оформление — самодостаточный I1 CPS):
- **quic** — _§146: out-of-order фрагментированный QUIC Initial с SNI=`Id` (см. §1.3);
  ранее — 1-RTT short header + энтропия._
- **dns** — EDNS OPT response, QNAME из `Id`.
- **stun** — Binding Success Response.
- **sip** — SIP response с доменом `Id`.

---

## 3. Требования

### 3.1 Механизм — I1 CPS vs S1–S4 padding (развилка, рекомендация — I1)

Это **развилка для реализатора**, не догма. Реши осознанно и зафиксируй выбор здесь.

**Рекомендация: I1 CPS** — генерировать CPS-строку в option/transport-слое, device-стек
не меняется. Аргументы за:
- транспорт под I1 уже готов (option → `awgIpcLines` → vendored `obf.go`), сабмодуль не
  трогается → дешевле ребейз (CONSTITUTION §2 ценит минимальный дифф);
- WireSock'овский 1-RTT-short трюк опирается на реальный WG-ciphertext **за** padding в
  том же датаграме; standalone I1 (src=nil) такого «хвоста» не имеет — для I1 структуру
  придётся делать самодостаточной (`<b>`-скелет + `<r>/<rc>`).

**Альтернатива: S1–S4 padding (как реально делает WireSock).** Править сабмодуль **не
запрещено** — это tradeoff, а не инвариант. За S1–S4:
- 1:1 с подходом WireSock (`transform.rs` переписывает именно S1–S4 padding);
- мимикрия на **каждом** пакете, а не только приманкой перед handshake.

Против: правки `submodules/wireguard-go/device/send.go` (дороже ребейз, надо аккуратно
изолировать и пометить происхождение). Если выберешь S1–S4 — допустимо, обоснуй выбор и
держи правку сабмодуля минимальной/изолированной.

В любом варианте **структуру протокола** берём из `transform.rs`.

### 3.2 Опции (option-слой) — добавить (откачено вместе с остальным кодом)

Три поля в `option.AmneziaWGOptions` (`option/wireguard_awg.go`, lx-файл; ~12 строк):
```go
Id string `json:"id,omitempty"` // masquerade domain
Ip string `json:"ip,omitempty"` // quic | dns | stun | sip
Ib string `json:"ib,omitempty"` // browser (limited effect — see §3.3)
```
`IsSet()` (сравнение со `AmneziaWGOptions{}`) учтёт их автоматически → без `with_awg`
отвергаются stub'ом; пустые → конфиг байт-в-байт upstream.

### 3.3 Браузер (`Ib`) — честно

В реальном QUIC-коде WireSock **нет ClientHello и нет JA3-fingerprint'а** (QUIC =
1-RTT short header). Поэтому **не выдумывать** браузерный fingerprint. Варианты
(решить в PLAN): принять `Ib` для совместимости синтаксиса и валидировать
(`chrome|firefox|curl`), но честно задокументировать, что влияние минимально/нет;
либо использовать как seed для незначимых псевдослучайных битов (spin/key_phase).
**Запрещено** заявлять JA3-имитацию, которой нет.

### 3.4 Правила валидации (fail-fast)

- **Взаимоисключение с `I1`** — конфликт → ошибка.
- **`Ip ∈ {quic,dns,stun,sip}`** (lower); пусто при заданном `Id`/`Ib` → ошибка.
- **`Id` обязателен для `quic`/`dns`/`sip`** (идёт на провод как SNI / QNAME /
  SIP-host); опционален **только для `stun`** (STUN не несёт домен). _(§146 amendment
  2026-06-17: было «обязателен только для dns/sip» — после перехода `quic` на
  фрагментированный Initial с SNI `id` стал обязателен и для `quic`; см. §1.3.)_
  **Строгий LDH-hostname-чек** применяется **всегда, когда `Id` задан**
  (метки alnum+hyphen+`_`, без edge-hyphen, ≤63, всего ≤253, трейлинг-дот ок). Это
  **security-граница**: домен идёт в SIP-текст и DNS QNAME — control-байты
  (`\r\n\0\t`) и SIP/URI-метасимволы (`> ; @ "`) → инъекция. Совпадает с
  `is_valid_sni_hostname` WireSock.
- **`Ib`** валидировать (`chrome|firefox|curl`), эффект — см. §3.3.

### 3.5 Изоляция (CONSTITUTION §3.2–3.3)

| Файл | Зона | Что |
|------|------|-----|
| `option/wireguard_awg.go` | lx | +`Id/Ip/Ib` |
| `transport/wireguard/masque_awg.go` | lx, `with_awg` | диспетчер `masqueI1` + валидация + DNS/STUN/SIP + CPS-хелперы |
| `transport/wireguard/quic_initial_awg.go` (+ `quic_clienthello_awg.go`, `quic_crypto_awg.go`) | lx, `with_awg` | QUIC out-of-order фрагментированный Initial с SNI (§146; ранее `masque_quic_awg.go` — 1-RTT short header, удалён) |
| `transport/wireguard/device_awg.go` | lx, `with_awg` | вызов `masqueI1` в `awgIpcLines` |
| `transport/wireguard/masque_awg_test.go` | lx, `with_awg` | тесты |

Таблица — для механизма I1 (рекомендация §3.1): новых upstream-швов нет, сабмодуль не
трогаем. При выборе S1–S4 добавится изолированная правка `submodules/wireguard-go`.

---

## 4. Критерии приёмки

- **Структурная валидность каждого профиля** (НЕ тавтология): сгенерированный
  decoy парсится обратно как заявленный протокол — QUIC как валидный
  фрагментированный Initial с ClientHello (SNI=Id; §146 — ранее как 1-RTT short
  header, первый байт `0x40|...`); DNS как валидный EDNS-OPT месседж (QNAME=Id,
  OPT RR, опция `0xFDE9`, без хвостов); STUN как Binding Response (cookie, длина
  покрывает атрибуты); SIP как валидный response (status-line + обязательные
  заголовки, CRLF).
- **CPS принят реальным движком:** прогон через настоящий `newObfChain` из
  `submodules/wireguard-go/device` (GOFLAGS=-mod=mod, затем `git checkout` go.mod).
- **Валидация:** конфликт с `I1`, неизвестный `Ip`, пустой `Id`,
  control-байт/метасимвол в домене, `Ib` вне набора — ошибки; нет паники.
- **Инъекция домена** (`a.com\nx`, `a.com>;q=1`, …) — отвергается.
- **Gating:** `Id/Ip/Ib` без `with_awg` → «awg support not built».
- **Регресс:** старые `device_awg_test.go` зелёные; плоский WG и явный `I1` без
  masquerade — байт-в-байт. `lx-test/config/awg2_*.json` валидны.
- `go build ./...` (без тегов) ок; `go build -tags with_awg` ок;
  `go test -tags with_awg ./transport/wireguard/...` зелёный; `gofmt -l` пусто.
- **Тесты НЕ тавтологичны:** не «два случайных вывода различны». Проверять
  структуру/инварианты.

---

## 5. Вне скоупа

- Серверная сторона (client-only) — probe-response не делаем.
- Byte-identical имитация именно WireSock-трафика.
- JA3/uTLS браузерный fingerprint (его нет даже в WireSock QUIC, §3.3).

(Механизм S1–S4 и связанная правка `submodules/wireguard-go` — НЕ вне скоупа; это
открытая развилка в §3.1 с рекомендацией в пользу I1, решает реализатор.)

---

## 6. Ссылки

- WireSock open-source: <https://github.com/wiresock/amneziawg-install> (`amneziawg-proxy/src/transform.rs`, `quic_handshake.rs`)
- WireSock advanced params: <https://wiresock.net/documentation/wiresock-secure-connect/advanced-parameters.html>
- RFC 9000 §17.3.1 (QUIC short header), §14.1 (Initial ≥1200) · RFC 6891 (EDNS OPT) · RFC 5389 (STUN) · RFC 3261 (SIP)
- CPS-движок: `submodules/wireguard-go/device/obf.go`, `send.go:135`
- Проводка: `transport/wireguard/device_awg.go`, `option/wireguard_awg.go`
- Память: [[wiresock-id-ip-ib-feasibility]]
