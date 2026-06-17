# SPEC: 009 — WIRESOCK_MASQUERADE_PROFILES

Декларативные поля маскировки **`Id` / `Ip` / `Ib`** (домен / протокол / браузер) —
из [WireSock Secure Connect](https://www.wiresock.net/) — которые **на уровне конфига
разворачиваются в AmneziaWG `I1` CPS-строку**. Вместо ручного `i1=<b 0x...>` пользователь
пишет осмысленные поля, а движок собирает пакет-приманку нужного протокола.

Тип: **Feature**, расширение [003 AWG2_CLIENT_ENDPOINT](../003-F-C-AWG2_CLIENT_ENDPOINT)
и [005 AWG2_RANGED_MAGIC_HEADERS](../005-F-C-AWG2_RANGED_MAGIC_HEADERS).
Тег `with_awg`; новые файлы в зонах lx; сабмодуль не трогается.

---

## 1. Что это

`Id/Ip/Ib` — декларативная обёртка над `I1`. На корне endpoint'а пользователь задаёт:

```jsonc
{ "type": "wireguard", /* ... */ "id": "www.google.com", "ip": "quic", "ib": "chrome" }
```

и получает сгенерированную `i1`-строку, как если бы вписал её руками. Новый рантайм в
device не добавляется — генерация целиком в option/transport-слое.

| Поле | Имя | Значение |
|------|-----|----------|
| `Id` | **Domain** | домен для маскировки (массовый легитимный: `www.google.com`, `ozon.ru`…). Идёт на провод как SNI / QNAME / SIP-host |
| `Ip` | **Protocol** | протокол маскировки: **quic** \| **dns** \| **stun** \| **sip** |
| `Ib` | **Browser** | `chrome` \| `firefox` \| `curl`. Валидируется; на сгенерированный пакет не влияет (нет JA3-имитации — см. §4) |

> Нейминг проприетарный WireSock (`i`nterface **d**omain/**p**rotocol/**b**rowser); `ip` —
> это «protocol», НЕ IP-адрес. Эти ключи понимают только WireSock и это ядро; меняться
> не могут (контракт на входе). Результат же — стандартный AmneziaWG `i1` CPS-тег.

---

## 2. Механизм — I1 CPS

Генерируется CPS-строка в option/transport-слое; device-стек не меняется, сабмодуль
`submodules/wireguard-go` не трогается. Путь: option → `masqueI1` → `awgIpcLines` →
vendored `obf.go` (`newObfChain`). `I1`-пакет шлётся приманкой перед handshake с
`Obfuscate(buf, nil)` (src=nil, реальных данных нет — `send.go:135`).

Семантика CPS-движка (`submodules/wireguard-go/device/obf.go`): `<b 0xHEX>` статичные
байты · `<r N>` N криптослучайных байт · `<rc N>` ASCII-буквы · `<rd N>` цифры. Decoy
самодостаточен — это `<b>`-скелет плюс, где нужно, `<r>/<rc>/<rd>`-энтропия.

S1–S4 padding не используется: он невозможен против Cloudflare WARP (init/response
должны оставаться бит-в-бит как plain WG, иначе сервер отвергает handshake), ради
упрощения коннекта к которому фича и существует.

---

## 3. Профили

`sip` портирован из open-source WireSock-референса
([`github.com/wiresock/amneziawg-install`](https://github.com/wiresock/amneziawg-install),
Rust/MIT, `amneziawg-proxy/src/transform.rs`). `quic` — собственный фрагментированный QUIC
Initial (DPI-bypass, §3.1). `stun` — WebRTC Binding **Request** (§3.2). `dns` — клиентский
DNS query (§3.3). LDH-валидатор домена совпадает с их `quic_handshake.rs::is_valid_sni_hostname`.

> **Device-результат (тест-телефон, LTE с активным DPI):** проходит **только `quic`**
> (~340 мс). `stun` (Binding Request и полный WebRTC-вариант с MESSAGE-INTEGRITY) и `dns`
> (query QR=0, QTYPE HTTPS) — **Timeout**. `sip` по аналогии ожидаемо так же.
>
> **Фундаментальный вывод.** Качество пакета и направление (request vs response) —
> вторичны. Решает триплет **(протокол + назначение)**: DPI режет STUN/DNS/SIP к
> WARP-edge `162.159.x:2408` **как класс протокола**, потому что raw STUN/DNS/SIP к
> дата-центровому IP сами по себе аномальны (DNS живёт на :53-резолвере, STUN — на
> STUN-сервере). Углубление пакета (request вместо response, MESSAGE-INTEGRITY, реальный
> QTYPE) аномалию назначения не убирает. **`quic` обходит проверку назначения**, потому
> что QUIC/HTTP3 легитимно идёт куда угодно (весь HTTP/3-веб к CDN), так что QUIC к
> Cloudflare-IP — ожидаемый трафик. `quic` — единственный проверенный рабочий механизм
> здесь; `dns`/`stun`/`sip` реализованы в правильной (клиент-инициированной) форме и
> сохранены на случай других провайдеров, чей DPI проверяет только корректность пакета,
> а не «протокол-к-назначению».

### 3.1 QUIC — out-of-order фрагментированный Initial

`ip=quic` эмитит полный **QUIC Initial (RFC 9001)** с реалистичным браузерным
ClientHello, где `Id` идёт как **SNI**. ClientHello нарезан на CRYPTO-фреймы, выложенные в
payload в **перемешанном (out-of-order) порядке**: первый CRYPTO-фрейм на проводе имеет
`offset≠0`, фрейм с `offset=0` — не первый, между CRYPTO-фреймами вставлены PING и PADDING.

**Раскладка рандомизируется на каждый вызов** (случайные точки разреза + случайный
out-of-order порядок), при сохранении инвариантов I1–I4 — так нет фиксированной
межюзерной сигнатуры. Параметры robustness (`quicGenParams`: число фрагментов, число PING,
диапазон размера датаграммы) — «ручки» для эскалации обфускации без правки кода, если DPI
поумнеет (напр. начнёт держать reassembly-буфер → больше фрагментов). По умолчанию —
device-проверенная база: 6 фрагментов, 2 PING, 1250б.

**Почему так.** Настоящий QUIC-сервер реассемблирует CRYPTO-фреймы по offset до TLS-парсинга;
line-rate DPI reassembly-буфер не держит — берёт первый CRYPTO-фрейм, считает, что он с
offset 0, и парсит TLS оттуда. При первом фрейме `offset≠0` DPI парсит середину ClientHello
как начало → длины TLS-записи не сходятся → парс прерывается → DPI пропускает (fail-open:
настоящий Chrome тоже легитимно фрагментирует большие ClientHello). Сервер фреймы
переупорядочит, DPI — нет.

`i1` — decoy (src=nil, шлётся перед WG-handshake); реальный TLS-handshake он не завершает,
его задача — чтобы первый пакет потока выглядел как легитимный старт QUIC-сессии к CDN.

Инварианты (проверяются обратным разбором в тестах):
- **I1.** первый CRYPTO-фрейм в wire-порядке имеет `offset≠0`;
- **I2.** фрейм с `offset=0` выложен не первым;
- **I3.** между CRYPTO-фреймами есть PADDING-runs и ≥1 PING;
- **I4.** объединение CRYPTO-фреймов по offset = непрерывный валидный ClientHello `[0..N)`,
  без дыр/перекрытий, SNI на месте.

Крипта — RFC 9001 §5 (HKDF-Extract по DCID → `client in` → `quic key/iv/hp`,
AES-128-GCM, header protection). Свежие DCID + TLS random + ephemeral x25519 на каждый
вызов → разный ciphertext (нет общей сигнатуры между юзерами). Пакет ≈1250б, length-поле
1232 (padded ≥1200, RFC 9000 §14.1).

### 3.2 DNS / STUN / SIP

- **dns** — клиентский DNS **query**. Flags `0x0100` (QR=0, RD=1; byte2 ноль), QDCOUNT=1,
  ARCOUNT=1; QNAME из `Id`, QTYPE **HTTPS** (`0x0041`, RR-type 65 — самый частый запрос
  современного браузера), QCLASS IN; OPT RR (TYPE `0x0029`, CLASS=1232, TTL=0, DO=0) с одной
  неизвестной EDNS-опцией код `0xFDE9` (IANA local-use), OPTION-LENGTH покрывает cover-байты →
  весь датаграм парсится как один DNS query. TXID `<r 2>` и cover `<r 40>` свежие на пакет.
  Query, а не response: клиент первым шлёт запрос. Генератор — `masqueDNSQueryCPS`.
- **stun** — WebRTC Binding **Request**. type `0x0001`, magic cookie `0x2112A442`, свежий
  txn; атрибуты USERNAME (`0x0006`), ICE-CONTROLLING (`0x802a`), PRIORITY (`0x0024`),
  SOFTWARE (`0x8022` = `libwebrtc`), MESSAGE-INTEGRITY (`0x0008`, HMAC-SHA1), FINGERPRINT
  (`0x8028`, CRC-32). Request, а не response: клиент первым шлёт именно запрос.
  MESSAGE-INTEGRITY структурно валиден, но по произвольному ICE-ключу (реального пароля у
  decoy нет — on-path DPI HMAC всё равно не проверит). Свежая энтропия на вызов; hostname не
  несёт. Генератор — `stun_request_awg.go`.
- **sip** — `SIP/2.0 200 OK` response + Via(branch)/From(tag)/To/Call-ID/CSeq, CRLF; `Id`
  как host в URI. (Остаётся response-формой; на тестовом DPI отдельно не проверялся.)

---

## 4. Браузер (`Ib`)

В сгенерированном QUIC Initial браузерный JA3/JA4-fingerprint **не имитируется**: DPI-bypass
держится на фрагментации CRYPTO-фреймов, а не на TLS-fingerprint. `Ib` принимается для
синтаксической совместимости с WireSock-конфигами и валидируется (`chrome|firefox|curl`,
только при `ip=quic`), но на байты пакета не влияет. Заявлять JA3-имитацию запрещено.

---

## 5. Валидация (fail-fast)

- **Взаимоисключение с `I1`** — задан и `i1`, и `id/ip/ib` → ошибка.
- **`Ip ∈ {quic,dns,stun,sip}`** (lower); пусто при заданном `Id`/`Ib` → ошибка.
- **`Id` обязателен для `quic`/`dns`/`sip`** (идёт на провод как SNI / QNAME / SIP-host);
  опционален **только для `stun`**.
- **Строгий LDH-чек** применяется **всегда, когда `Id` задан** (метки alnum+hyphen+`_`,
  без edge-hyphen, ≤63, всего ≤253, трейлинг-дот ок). Это security-граница: домен идёт в
  SIP-текст / DNS QNAME / TLS SNI — control-байты (`\r\n\0\t`) и SIP/URI-метасимволы
  (`> ; @ "`) дали бы инъекцию. Совпадает с `is_valid_sni_hostname`.
- **`Ib` ∈ {chrome,firefox,curl}** и только при `ip=quic`; иначе ошибка.

---

## 6. Файлы (зоны)

| Файл | Зона | Что |
|------|------|-----|
| `option/wireguard_awg.go` | lx | поля `Id/Ip/Ib` |
| `transport/wireguard/masque_awg.go` | lx, `with_awg` | диспетчер `masqueI1` + валидация + DNS/SIP + `cpsBuilder` |
| `transport/wireguard/quic_initial_awg.go` | lx, `with_awg` | QUIC Initial: varint, рандомизированный frame-план (I1–I4) + `quicGenParams`, сборка RFC 9001 |
| `transport/wireguard/quic_clienthello_awg.go` | lx, `with_awg` | реалистичный TLS 1.3 ClientHello (SNI=`Id`) |
| `transport/wireguard/quic_crypto_awg.go` | lx, `with_awg` | HKDF / AES-128-GCM / header protection |
| `transport/wireguard/stun_request_awg.go` | lx, `with_awg` | STUN WebRTC Binding Request (FINGERPRINT + MESSAGE-INTEGRITY) |
| `transport/wireguard/device_awg.go` | lx, `with_awg` | вызов `masqueI1` в `awgIpcLines` |
| `transport/wireguard/masque_awg_test.go`, `quic_initial_awg_test.go` | lx, `with_awg` | тесты |

Сабмодуль `submodules/wireguard-go` не трогается.

---

## 7. Критерии приёмки

- **Структурная валидность каждого профиля** (обратным разбором, не тавтология): QUIC —
  собственный вывод AEAD-расшифровывается (тег сходится), frame-walk даёт ≥6 CRYPTO + ≥1
  PING + PADDING, первый CRYPTO `offset≠0` (I1), CRYPTO реассемблируются в валидный
  ClientHello с SNI=`Id` (I4); DNS — валидный EDNS-OPT **query** (QR=0, QNAME=`Id`, QTYPE
  HTTPS, опция `0xFDE9`, без хвостов); STUN — Binding **Request** (cookie, атрибуты тайлят сообщение, FINGERPRINT
  CRC-32 сходится, USERNAME + MESSAGE-INTEGRITY присутствуют); SIP — валидный response
  (status-line + обязательные заголовки + CRLF).
- **Рандомизация QUIC:** раскладка фрейм-плана и точки разреза свежие на каждый вызов;
  инварианты I1–I4 держатся на каждом сэмпле (стресс-тест), две генерации → разные offset'ы
  фрагментов (нет фикс-сигнатуры). Robustness-ручки (`quicGenParams`: 4–12 фрагментов,
  переменный размер) тоже держат I1–I4.
- **Уникальность:** два вызова QUIC с одним SNI → разные DCID/TLS random → разный
  ciphertext; два вызова STUN → разный txn/ufrag/ключ → разный blob.
- **Длинный домен:** валидный LDH-домен любой длины (≤253) генерируется без ошибки (payload
  пинится к length-полю flex-PADDING-run; CH растёт с длиной SNI, инварианты сохраняются).
- **CPS принят реальным движком:** прогон через `newObfChain` из `submodules/wireguard-go`.
- **Валидация:** конфликт с `I1`, неизвестный `Ip`, пустой `Id` для quic/dns/sip,
  control-байт/метасимвол в домене, `Ib` вне набора / не при quic — ошибки; нет паники.
- **Gating:** `Id/Ip/Ib` без `with_awg` → «awg support not built».
- **Регресс:** плоский WG и явный `I1` без masquerade — байт-в-байт.
- `go build` (без тегов и `-tags with_awg`) ок; `go test -tags with_awg ./transport/wireguard/...`
  зелёный; `gofmt -l` lx-файлов пусто.
- **Device-smoke:** узел `ip=quic` с фрагментированным Initial поднимает туннель и проводит
  реальный трафик через активный DPI — проверено вживую.

---

## 8. Вне скоупа

- `dns`/`stun`/`sip` фрагментация (отдельная таска при необходимости).
- Серверная сторона / probe-response (client-only).
- Byte-identical имитация конкретного снимка трафика (рандомизация снижает сигнатуру).
- JA3/uTLS браузерный fingerprint (§4).

---

## 9. Ссылки

- RFC 9000 §16 (varint), §14.1 (Initial ≥1200), §17.2.2 (Initial), §19.6 (CRYPTO), §19.7 (PING), §19.1 (PADDING)
- RFC 9001 §5 (Initial secrets), §5.4 (header protection) · RFC 6891 (EDNS OPT) · RFC 5389 (STUN) · RFC 3261 (SIP)
- WireSock open-source (dns/stun/sip структура): <https://github.com/wiresock/amneziawg-install> (`amneziawg-proxy/src/transform.rs`, `quic_handshake.rs`)
- CPS-движок: `submodules/wireguard-go/device/obf.go`, `send.go:135`
- Проводка: `transport/wireguard/device_awg.go`, `option/wireguard_awg.go`
- Память: [[wiresock-id-ip-ib-feasibility]], [[qtls-helpers-reuse-for-quic-initial]]
