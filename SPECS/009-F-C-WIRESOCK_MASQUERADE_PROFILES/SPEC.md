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

Все профили — собственные клиент-инициированные генераторы: `quic` — фрагментированный QUIC
Initial (§3.1), `stun` — WebRTC Binding **Request** (§3.2), `dns` — клиентский DNS query (§3.3),
`sip` — INVITE **request** с SDP (§3.2). SIP-текст исходно вдохновлён WireSock-референсом
([`amneziawg-install`](https://github.com/wiresock/amneziawg-install), MIT), но переведён из
response в request. LDH-валидатор домена совпадает с их `quic_handshake.rs::is_valid_sni_hostname`.

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
- **sip** — INVITE **request** с SDP-офером. Request-line `INVITE sip:<user>@<host> SIP/2.0`,
  Via(branch=z9hG4bK)/Max-Forwards:70/From(tag)/To(без tag)/Call-ID/CSeq:N INVITE/Contact,
  Content-Type: application/sdp, Content-Length (точная), тело SDP (`v=0`, `m=audio`, rtpmap).
  Request, а не response: клиент первым шлёт INVITE. Имена пользователей и (если `Id` пуст)
  host — произносимые псевдо-строки (`PseudoGen`, запечены в `<b>`, уникальны между юзерами);
  branch/tag/Call-ID/CSeq/SDP-id — per-packet `<rc>`/`<rd>` фикс-ширины (Content-Length
  стабилен). `Id` опционален: задан → host, пуст → `pgHost()`. Генератор — `sip_invite_awg.go`.

---

## 4. Браузер (`Ib`)

В сгенерированном QUIC Initial браузерный JA3/JA4-fingerprint **не имитируется**: DPI-bypass
держится на фрагментации CRYPTO-фреймов, а не на TLS-fingerprint. `Ib` принимается для
синтаксической совместимости с WireSock-конфигами и валидируется (`chrome|firefox|curl`,
только при `ip=quic`), но на байты пакета не влияет — JA3-имитации в этом профиле нет.

---

## 5. Валидация (fail-fast)

- **Взаимоисключение с `I1`** — задан и `i1`, и `id/ip/ib` → ошибка.
- **`Ip ∈ {quic,dns,stun,sip}`** (lower); пусто при заданном `Id`/`Ib` → ошибка.
- **`Id` обязателен только для `quic`** (SNI); **опционален для `dns`** (QNAME или псевдо-домен) и для
  `sip`** (задан → SIP host, пуст → генерируется псевдо-host) и **`stun`** (hostname-less).
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
| `transport/wireguard/masque_awg.go` | lx, `with_awg` | диспетчер `masqueI1` + валидация + DNS query + `cpsBuilder` |
| `transport/wireguard/quic_initial_awg.go` | lx, `with_awg` | QUIC Initial: varint, рандомизированный frame-план (I1–I4) + `quicGenParams`, сборка RFC 9001 |
| `transport/wireguard/quic_clienthello_awg.go` | lx, `with_awg` | реалистичный TLS 1.3 ClientHello (SNI=`Id`) |
| `transport/wireguard/quic_crypto_awg.go` | lx, `with_awg` | HKDF / AES-128-GCM / header protection |
| `transport/wireguard/stun_request_awg.go` | lx, `with_awg` | STUN WebRTC Binding Request (FINGERPRINT + MESSAGE-INTEGRITY) |
| `transport/wireguard/sip_invite_awg.go` | lx, `with_awg` | SIP INVITE request + SDP-офер |
| `transport/wireguard/pseudo_gen_awg.go` | lx, `with_awg` | произносимые псевдо-имена/host/IP (для SIP) |
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
  CRC-32 сходится, USERNAME + MESSAGE-INTEGRITY присутствуют); SIP — валидный INVITE
  **request** (request-line `INVITE ... SIP/2.0`, Via/Max-Forwards/From/To-без-tag/Call-ID/
  CSeq/Contact, SDP-тело, Content-Length точно покрывает тело; имена не захардкожены).
- **Рандомизация QUIC:** раскладка фрейм-плана и точки разреза свежие на каждый вызов;
  инварианты I1–I4 держатся на каждом сэмпле (стресс-тест), две генерации → разные offset'ы
  фрагментов (нет фикс-сигнатуры). Robustness-ручки (`quicGenParams`: 4–12 фрагментов,
  переменный размер) тоже держат I1–I4.
- **Уникальность:** два вызова QUIC с одним SNI → разные DCID/TLS random → разный
  ciphertext; два вызова STUN → разный txn/ufrag/ключ → разный blob.
- **Длинный домен:** валидный LDH-домен любой длины (≤253) генерируется без ошибки (payload
  пинится к length-полю flex-PADDING-run; CH растёт с длиной SNI, инварианты сохраняются).
- **CPS принят реальным движком:** прогон через `newObfChain` из `submodules/wireguard-go`.
- **Валидация:** конфликт с `I1`, неизвестный `Ip`, пустой `Id` для quic,
  control-байт/метасимвол в домене, `Ib` вне набора / не при quic — ошибки; нет паники.
- **Gating:** `Id/Ip/Ib` без `with_awg` → «awg support not built».
- **Регресс:** плоский WG и явный `I1` без masquerade — байт-в-байт.
- `go build` (без тегов и `-tags with_awg`) ок; `go test -tags with_awg ./transport/wireguard/...`
  зелёный; `gofmt -l` lx-файлов пусто.
- **Device-smoke:** узел `ip=quic` с фрагментированным Initial поднимает туннель и проводит
  реальный трафик через активный DPI — проверено вживую.

---

## 8. Active probing — граница односторонней маскировки (гипотезы)

Этот раздел — **гипотезы**, не device-факты. Device-факты у нас ровно те, что в §3: `quic`
проходит (~340 мс), `dns`/`stun`/`sip` — Timeout даже после исправления направления на
клиент-инициированное. Здесь — *почему* (механизм под эмпирическим выводом §3), и почему это
механистически ограничивает односторонний (client-only) decoy. Внутреннюю логику DPI мы не
наблюдаем, поэтому всё ниже — модель, а не измерение.

> **H3 (тезис, высокая уверенность). Односторонний decoy силён ровно настолько, насколько то,
> что ЦЕЛЕВОЙ СЕРВЕР реально отдаёт на этом порту.** Клиент эмитит только клиентскую сторону
> протокола; ответить на проверку он не может. Это структурно неизбежно (см. §2 и `send.go:135`:
> decoy шлётся как `Obfuscate(buf, nil)`, src=nil — самодостаточный датаграм без ciphertext-хвоста;
> единственный серверный ответ в device — `SendHandshakeResponse`, и тот лишь на валидный
> WG-handshake, не на произвольную протокол-проверку). Это «(протокол + назначение)» из §3,
> доведённое до механизма. H3 робастна при любой из трёх причинных моделей ниже.

Конкретная причина, по которой `dns`/`stun`/`sip` к WARP-edge `162.159.x:2408` падают, нами **не
наблюдаема** и совместима как минимум с тремя моделями DPI:

1. **Пассивная репутация назначения (модель, которую сейчас прямо поддерживает §3).** raw
   DNS/STUN/SIP к дата-центровому IP сам по себе аномален (DNS живёт на `:53`-резолвере, STUN — на
   STUN-сервере) и режется как класс протокол-к-назначению, **без всякой проверки и без ответа**.
   При этой модели `quic` проходит не потому, что кто-то ответил, а потому что QUIC/HTTP3-к-CDN —
   ожидаемая по форме трафика картина (responder не нужен вообще).
2. **Пассивный allowlist протоколов на класс назначения** — частный случай (1): на дата-центровый
   IP разрешён ожидаемый набор, QUIC в нём есть, raw DNS/STUN/SIP — нет.
3. **Active probing (сильнейшая, но наименее подтверждённая форма).** Увидев decoy, DPI сам шлёт
   проверочный пакет на тот же 5-tuple (свой QUIC Initial / version-negotiation-триггер, DNS-query,
   STUN Binding Request, SIP OPTIONS) и ждёт протокол-корректного ответа. Тогда:
   > **H1 (гипотеза активной проверки, средняя уверенность).** Если DPI активно проверяет
   > `quic`-decoy и Cloudflare-edge **действительно отвечает QUIC на этом 5-tuple**, проба
   > удовлетворяется и поток классифицируется как легитимный QUIC. Decoy «одалживает» чужой
   > настоящий responder, который сам не держит.
   > **H2 (гипотеза активной проверки, средняя уверенность).** На том же `162.159.x:2408` нет
   > DNS-резолвера, STUN-сервера и SIP-UA. Активная проба по этим протоколам не получит
   > протокол-корректного ответа никогда — как бы хорошо ни был сделан клиентский decoy.

> **Слабое звено H1 — непроверенное условие.** `:2408` — это **WireGuard**-порт WARP. Отвечает ли
> Cloudflare-edge QUIC/HTTP3 **именно на этом UDP-порту** (а не на штатном `:443`) — пакетным
> захватом не подтверждено. «Edge говорит QUIC по всему флоту» не равно «этот порт отвечает на QUIC
> Initial». Поэтому «Cloudflare отвечает QUIC на `:2408`» — условие, а не факт; его проверяет
> тест T3.

**Общий вывод и его условность.** При активной модели для `dns`/`stun`/`sip` нужен
**контролируемый сервер с probe-responder** (двусторонняя модель WireSock `amneziawg-proxy`, см.
§9), и они применимы только к **self-hosted AmneziaWG**, не к WARP. Но если DPI **пассивный**
(модель §3), responder ничего не чинит: проблема не в отсутствии ответа, а в том, что raw
DNS/STUN/SIP к дата-центровому IP аномальны по назначению — помогает только **протокол-уместное
назначение** (DNS на `:53` и т.д.), не co-located responder на том же WG-порту. То есть вывод
«не-QUIC нужен self-hosted responder» **корректен только при активной модели** и остаётся
гипотезой. Поэтому код заполняет только `i1`/`i2` (QUIC для WARP), а `i3..i5` оставлены свободными
под self-hosted/мульти-decoy — это реальный задел.

Репутация назначения подробнее описана в `transport/wireguard/masque_awg.go` (DNS-генератор) и в
§3; этот раздел обобщает их в probe-модель. Серверная сторона / probe-response — вне скоупа (§10).
Источник device-фактов — LxBox-задача 146; статья habr 1047080 описывает двустороннюю модель
WireSock архитектурно (без байт-спек и без измерений) — подтверждает
**механизм** H3, но не служит device-доказательством.

### 8.1 Фальсифицируемые device-тесты

Те же телефон + LTE/WARP-DPI, что дали §3. Все decoy в `i1`, если не сказано иное; `jc`/junk и
реальный WG-handshake — константа.

- **T1 — назначение vs протокол (проверяет H2/H3).** Направить `ip=dns` (и отдельно `stun`,
  `sip`) на **self-hosted AmneziaWG**, хост которого реально держит соответствующий responder на
  том же UDP-порту. *Предсказание:* проходят, тогда как к WARP `:2408` — Timeout → подтверждает
  H2+H3. Если падают даже с co-located responder → H2/H3 (в активной форме) опровергнуты (блокер
  иной — напр. сигнатура raw-протокол-к-любому-дата-центру).
- **T2 — активная проба vs пассивная репутация.** На контролируемом сервере логировать входящий
  UDP на WARP-5-tuple после каждого decoy. *При активной модели:* после QUIC-decoy виден непрошеный
  входящий QUIC-образный проб (не от WG-сервера). Если проба нет, а `dns`/`stun`/`sip` всё равно
  падают → активная проба опровергнута, механизм — пассивная репутация назначения (H3 держится в
  слабой форме).
- **T3 — контроль «бесплатного responder» QUIC (проверяет H1).** Направить `ip=quic`-decoy на
  IP/порт **без** QUIC-responder (plain UDP-echo или `:2408` не-Cloudflare хоста). *Предсказание:*
  `ip=quic` начинает Timeout, как dns/stun/sip — значит успех нёс настоящий Cloudflare-responder, а
  не сами байты QUIC. Если `quic` всё равно проходит → H1 опровергнута. T3 — единственное, что
  снимает слабое звено H1 (`:2408` vs `:443`).

---

## 9. Многопакетная QUIC-последовательность (реализовано)

`ip=quic` эмитит **два** независимых фрагментированных Initial: `i1` (как раньше) и `i2`
(`masqueQUICSecondInitialCPS`, диспетч `masqueI2` → проводка `awgIpcLines`). Поток читается как
**развивающаяся QUIC-сессия** (два старта Initial), а не одиночный opener — снижает сигнатуру
одиночного пакета. Слоты движка `i1..i5` (`device.go` `ipackets [5]*obfChain`, каждый парсится в
`uapi.go`, каждый шлётся своим UDP-датаграмом в `send.go`) делают это возможным без правки
сабмодуля; `i3..i5` остаются пустыми (резерв-эскалация).

**Device-verified.** На том же LTE/WARP DPI: конфиг с явными `i1+i2` поднимает туннель **без
регресса** латентности относительно `i1`-only (~340 мс). Подтверждает §9.1: добавление `i2` не
трогает реальный handshake.

> **Почему НЕ short-header.** `i2` — это **полноценный второй фрагментированный Initial с
> независимым DCID**, НЕ QUIC short-header (1-RTT) continuation. Short-header — тот самый конструкт,
> который коммит `64ce4a47` УДАЛИЛ как device-blocked (все short-header-варианты давали Timeout). И
> DCID-reuse (1-RTT с DCID из `i1` без ответа сервера) — невозможное QUIC-состояние, читаемое как
> аномалия. Два **независимых** Initial с разными DCID выглядят как два старта QUIC-сессии — что
> браузер делает рутинно. Это устойчивая форма, а не повторное использование уже заблокированного
> short header.

### 9.1 Почему это не сломает WARP

Разобрано построчно по `submodules/wireguard-go/device/send.go` `SendHandshakeInitiation`: `i2` (как
и любой decoy `i2..i5`) не может задержать, переупорядочить, испортить или изменить ни одного
валидируемого байта реального `MessageInitiation`.

1. **Handshake строится ДО и НЕЗАВИСИМО от decoy.** `CreateMessageInitiation(peer)` исполняется в
   начале функции, **до** входа в цикл по `ipackets`. Decoy не аргументы `msg` и идут после того,
   как `msg` уже вычислен.
2. **Decoy — самодостаточные UDP-датаграмы без реального ciphertext.** `ObfuscatedLen(0)` +
   `Obfuscate(buf, nil)` (src=nil); каждый decoy добавляется в `sendBuffer` отдельным элементом.
   `i2` — ещё один такой элемент, как `i1`; ни конкатенации, ни XOR с подлинным пакетом.
3. **Подлинный пакет добавляется последним и байт-идентичен стоковому WG.** После decoy- и
   `jc`-junk-циклов: `binary.Write(msg)`, `AddMacs(packet)`, опц. init-padding,
   `append(sendBuffer, packet)`, весь батч уходит одним `SendBuffers`. `i2` меняет только число
   элементов перед `packet`, не байты `packet`.
4. **Cloudflare реагирует только на валидный `MessageInitiation`.** WARP — стоковый WG-responder:
   `i1` (`0xC0..`) и любой `i2` (не WG-type-1, без валидных MAC) отбрасываются как не-WG-шум.
   Handshake завершается на подлинном пакете — ровно как сегодня с `i1`-only (~340 мс).
5. **Эмпирическая непрерывность.** `i1`-only уже сейчас шлёт один QUIC-decoy перед подлинным
   пакетом, и WARP-handshake идёт. `i2` — тот же механизм (+1 не-WG-датаграм), строго в режиме,
   который `jc` уже рутинно эксплуатирует. Нового кода на пути подлинного handshake нет.

### 9.2 Остаточные риски (после реализации)

Дизайн `i2` обходит главные грабли (short-header → второй фрагментированный Initial; DCID-reuse →
независимый DCID). Остаются ограничиваемые риски:

1. **Retry-амплификация.** `SendHandshakeInitiation` гоняет весь decoy-цикл **на каждом retry**. На
   лоссовом LTE дизайн увеличивает head-of-line-байты (`i1`~1250б + `i2`~1250б + `jc`) ровно когда
   линк хуже всего. Поэтому (decoy + jc) — **единый бюджет ведущих пакетов**: `ip=quic` уже тратит 2
   из него, не задирать `jc` сверх ~2–3 junk. `i3..i5` оставлены пустыми именно по этой причине.
2. **Flood/anti-amplification.** 2 decoy + малый `jc` — ниже правдоподобного порога, та же форма
   всплеска, что `jc` уже рутинно даёт.
3. **MTU.** Оба Initial ~1250б (как device-proven одиночный) → нет IP-фрагментации на LTE.
4. **Нет серверного responder.** `i2` помогает только против **пассивной** flow-классификации, НЕ
   против активной пробы (§8) — ответить на QUIC Version Negotiation мы не можем (WARP не наш). На
   текущем DPI `i1`-only и так проходит, поэтому ценность `i2` — задел против будущего DPI, который
   начнёт давить одиночный Initial; на сегодня это «не хуже», а не измеримое «лучше».
5. **Регресс `i1`.** `i2` генерится тем же `buildInitialPacket`, что и `i1`, с независимым DCID;
   байты `i1` не изменились (тесты `quic_initial_awg_test.go` зелёные). Откат к `i1`-only —
   тривиален (вернуть `masqueI2` → "").

### 9.3 Что проверено и что осталось

**Device-verified:** `i1+i2` поднимает туннель без регресса латентности vs `i1`-only (~340 мс) —
многопакетность безопасна для WARP-handshake (§9.1).

**Осталось (не блокирует отгрузку — задел, а не текущая нужда):**
1. **`i3..i5` эскалация:** третий Initial — только если будущий DPI начнёт давить 2-пакетную форму,
   и строго внутри бюджета ведущих датаграмов (п.1 §9.2).
2. **Поведенческая плоскость:** межпакетные задержки / вариативность размеров между подключениями
   (статья отмечает, что DPI палит и по timing) — отдельное направление, если passive-shape станет
   недостаточно.

---

## 10. Вне скоупа

- `dns`/`stun`/`sip` фрагментация (отдельная таска при необходимости).
- Серверная сторона / probe-response (client-only) — но см. §8: non-QUIC профили осмысленны
  только со своим сервером-ответчиком (это и есть серверная сторона, вне скоупа 009).
- Byte-identical имитация конкретного снимка трафика (рандомизация снижает сигнатуру).
- JA3/uTLS браузерный fingerprint (§4).
- `i3..i5` эскалация многопакетного QUIC (§9.3) — задел, реализуется при необходимости.

---

## 9. Ссылки

- RFC 9000 §16 (varint), §14.1 (Initial ≥1200), §17.2.2 (Initial), §19.6 (CRYPTO), §19.7 (PING), §19.1 (PADDING)
- RFC 9001 §5 (Initial secrets), §5.4 (header protection) · RFC 6891 (EDNS OPT) · RFC 5389 (STUN) · RFC 3261 (SIP)
- WireSock open-source (dns/stun/sip структура): <https://github.com/wiresock/amneziawg-install> (`amneziawg-proxy/src/transform.rs`, `quic_handshake.rs`)
- CPS-движок: `submodules/wireguard-go/device/obf.go`, `send.go:135`
- Проводка: `transport/wireguard/device_awg.go`, `option/wireguard_awg.go`
- Память: [[wiresock-id-ip-ib-feasibility]], [[qtls-helpers-reuse-for-quic-initial]]
