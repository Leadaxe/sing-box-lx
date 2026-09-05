# SPEC: 009 — WIRESOCK_MASQUERADE_PROFILES

**Фича:** [AWG2](../../FEATURES/003-AWG2/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) |

Декларативные поля маскировки **`Id` / `Ip` / `Ib`** (домен / протокол / браузер) —
из [WireSock Secure Connect](https://www.wiresock.net/) — которые **на уровне конфига
разворачиваются в AmneziaWG `I1` CPS-строку**. Вместо ручного `i1=<b 0x...>` пользователь
пишет осмысленные поля, а движок собирает пакет-приманку нужного протокола.

Расширение [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT)
и [005 AWG2_RANGED_MAGIC_HEADERS](../005-AWG2_RANGED_MAGIC_HEADERS).
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
| `Ib` | **Browser** | `chrome` \| `firefox` \| `curl`. Валидируется; только при `ip=quic`. С тегом `with_utls` `chrome`/`firefox` меняют ClientHello на uTLS-профиль браузера (крупнее generic, иная нарезка); `""`/`curl` и сборка без `with_utls` дают device-proven generic CH — см. §4 |

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
`sip` — начало звонка: INVITE (i1) + 100 Trying (i2), два самостоятельных пакета одного диалога
(§3.2). Структура — стандартный SIP call-setup (RFC 3261 §17). LDH-валидатор домена совпадает с
WireSock-референсом ([`amneziawg-install`](https://github.com/wiresock/amneziawg-install), MIT),
`quic_handshake.rs::is_valid_sni_hostname`.

> **Device-результат (тест-телефон, LTE с активным DPI):** на момент прошлых прогонов проходил
> **только `quic`** (~340 мс); `stun` (Binding Request и полный WebRTC-вариант с
> MESSAGE-INTEGRITY) и `dns` (query QR=0, QTYPE HTTPS) — **Timeout**. `sip` тогда был одиночным
> пакетом и не проверялся отдельно.
>
> **Гипотеза, которую мы сейчас проверяем.** Почему `dns`/`stun`/`sip` упирались в Timeout,
> точно **не установлено**. Рабочая гипотеза была «DPI режет STUN/DNS/SIP к WARP-edge
> `162.159.x:2408` как класс протокола» (raw STUN/DNS/SIP к дата-центровому IP аномальны по
> назначению), но это не доказано. По подсказке `sip` переведён на **multi-packet i1+i2**:
> i1 = полный INVITE, i2 = полный `100 Trying` того же диалога (стандартный call-setup), оба —
> самостоятельные валидные SIP-пакеты, и профиль рассчитан на работу с `junk`. Так поток читается
> как начало реального звонка, а не одиночный опенер. Заработает ли это против WARP —
> **ожидает device-проверки**; `quic` остаётся подтверждённо рабочим механизмом, `dns`/`stun`
> реализованы в правильной клиент-инициированной форме и сохранены для проверки/других провайдеров.

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
- **sip** — начало SIP-звонка (call setup, RFC 3261 §17) — **два самостоятельных пакета** одного диалога:
  **i1 = полный INVITE** (request-line + Via(branch=z9hG4bK)/To(без tag)/From(tag)/Call-ID/CSeq:N
  INVITE/Max-Forwards:70/Contact/Content-Type/`Content-Length: 0`, **без SDP-тела**), **i2 = полный
  `SIP/2.0 100 Trying`** (статус-строка + те же Via/To/From/Call-ID/CSeq + `Content-Length: 0`).
  **Почему два целых пакета, а не фрагментация.** i1/i2 уходят как **независимые UDP-датаграммы**
  (amneziawg-go `send.go`, `src=nil`), а у UDP нет потоковой реассемблеризации — пакетный DPI
  смотрит каждую датаграмму отдельно. INVITE и 100 Trying валидны **каждый сам по себе**; вместе —
  каноническое начало вызова (UAC шлёт INVITE → сервер сразу отвечает 100 Trying). Прежняя
  фрагментация одного INVITE (head→i1, SDP→i2) оставляла каждую датаграмму битой и заменена.
  **Один диалог.** Via branch / From tag / Call-ID / CSeq **идентичны** в i1 и i2 — поэтому строятся
  **одним проходом** (`newSIPDialog` → `masqueSIPInviteCPS` + `masqueSIPTryingCPS`) и запекаются в
  `<b>` обеих половин (не per-packet `<rc>`/`<rd>`, иначе токены разошлись бы между слотами).
  Имена пользователей (display + local) и (если `Id` пуст) host — произносимые `PseudoGen`-строки,
  свежие на генерацию; это **не** хардкод RFC-примера `alice@atlanta.com`/`bob@biloxi.com` (он —
  публичный DPI-маяк). `Id` опционален: задан → host, пуст → `pgHost()`. Явный `i2` рядом с
  `id/ip/ib` отвергается как конфликт (зеркало гарда `i1`). Генератор — `sip_invite_awg.go`,
  диспетчер обоих слотов — `masqueI1I2`.
  **Требует junk** (`jc/jmin/jmax > 0`): профиль рассчитан на отправку вместе с junk-пакетами в
  том же пред-handshake-залпе.

---

## 4. Браузер (`Ib`)

`Ib` валидируется (`chrome|firefox|curl`, только при `ip=quic`) и **управляет JA3/JA4
ClientHello** (build с `with_utls`):

- **`ib=""` / `ib=curl`** → собственный generic ClientHello (~294б, device-proven; §3.1). uTLS не
  имеет curl-QUIC-fingerprint, поэтому curl деградирует на generic.
- **`ib=chrome` / `ib=firefox`** → ClientHello строится через **uTLS** (`github.com/metacubex/utls`,
  тот же, что у Reality): `UQUICClient` с fingerprint `HelloChrome_120` / `HelloFirefox_120` →
  настоящий браузерный JA3/JA4 (cipher_suites, supported_groups, порядок extensions, GREASE у
  Chrome). ALPN форсируется в `h3` (это QUIC, не TCP-TLS). PQ-гибрид key_share
  (`X25519MLKEM768`, ~1.2КБ) удаляется — он не влез бы в один Initial; следствие: JA3 как у
  конца-2023 браузера, не у текущего PQ-включённого. CH крупнее (~510–620б), фрагментация
  адаптируется (planFragmentsN режет любую длину, I1–I4 держатся).
- Без тега `with_utls` `ib=chrome/firefox` грациозно деградируют на generic CH (stub-файл).

**Назначение `Ib` — задел против будущего JA3/JA4-классифицирующего DPI.** На текущем целевом DPI
`ip=quic` проходит на фрагментации (fingerprint не проверяется), поэтому дефолт `ib=""` сохраняет
device-proven generic-путь; uTLS-вариант крупнее и сам по себе на устройстве не верифицирован.
Код: `quic_clienthello_utls_awg.go` (+ stub `…_utls_stub_awg.go`).

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
| `transport/wireguard/quic_clienthello_awg.go` | lx, `with_awg` | generic TLS 1.3 ClientHello (SNI=`Id`) + диспетч по `Ib` |
| `transport/wireguard/quic_clienthello_utls_awg.go` | lx, `with_awg && with_utls` | uTLS браузерный ClientHello (chrome/firefox JA3, §4) |
| `transport/wireguard/quic_clienthello_utls_stub_awg.go` | lx, `with_awg && !with_utls` | fallback на generic, когда uTLS не собран |
| `transport/wireguard/quic_crypto_awg.go` | lx, `with_awg` | HKDF / AES-128-GCM / header protection |
| `transport/wireguard/stun_request_awg.go` | lx, `with_awg` | STUN WebRTC Binding Request (FINGERPRINT + MESSAGE-INTEGRITY) |
| `transport/wireguard/sip_invite_awg.go` | lx, `with_awg` | начало SIP-звонка: INVITE (i1) + `100 Trying` (i2), один диалог, без SDP |
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
  CRC-32 сходится, USERNAME + MESSAGE-INTEGRITY присутствуют); SIP — **два самостоятельных
  пакета** одного диалога: i1 — валидный INVITE **request** (request-line `INVITE ... SIP/2.0`,
  Via/Max-Forwards/From/To-без-tag/Call-ID/CSeq/Contact, `Content-Length: 0`, без SDP-тела),
  i2 — валидный `SIP/2.0 100 Trying` (статус-строка + те же Via/To/From/Call-ID/CSeq,
  `Content-Length: 0`); branch/tag/Call-ID/CSeq **идентичны** в i1 и i2 (один диалог), имена
  не захардкожены.
- **Рандомизация QUIC:** раскладка фрейм-плана и точки разреза свежие на каждый вызов;
  инварианты I1–I4 держатся на каждом сэмпле (стресс-тест), две генерации → разные offset'ы
  фрагментов (нет фикс-сигнатуры). Robustness-ручки (`quicGenParams`: 4–12 фрагментов,
  переменный размер) тоже держат I1–I4.
- **Уникальность:** два вызова QUIC с одним SNI → разные DCID/TLS random → разный
  ciphertext; два вызова STUN → разный txn/ufrag/ключ → разный blob.
- **`Ib` JA3 (build с `with_utls`):** `ib=chrome`/`firefox` → uTLS-ClientHello, расшифровывается,
  SNI=`Id`, первый CRYPTO offset≠0 (I1), пакет 1250б; chrome содержит GREASE cipher, firefox нет;
  длины chrome≠firefox≠generic. `ib=""`/`curl` → generic ~294б. Без `with_utls` chrome/firefox
  деградируют на generic (stub компилируется и тестируется).
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

## 9. QUIC — один Initial (multi-packet рассмотрен и отклонён)

`ip=quic` эмитит **ОДИН** фрагментированный Initial (`i1`; `i2..i5` пусты). Один Initial — это
ровно то, что реальный клиент шлёт, открывая одну QUIC-сессию; правдоподобие даёт
браузер-точный ClientHello (`Ib` → uTLS, §4), а не число пакетов.

**Отклонённая альтернатива — два независимых Initial (i1+i2).** Идея «развивающейся сессии» была
реализована и device-проверена как безопасная для WARP-handshake (туннель встаёт без регресса
латентности), но **концептуально неверна**: каждый DCID — отдельное QUIC-соединение, поэтому два
Initial с разными DCID читаются как **два брошенных соединения**, а не одна развивающаяся сессия —
для DPI с отслеживанием по DCID это *более* аномально, не менее. Настоящее «продолжение» (1-RTT
short-header с тем же DCID) невозможно: short-header device-blocked (коммит `64ce4a47`), а 1-RTT до
ответа сервера — невозможное QUIC-состояние. Вывод: один чистый Initial честнее любого
двухпакетного варианта. (`masqueQUICSecondInitialCPS` удалён.)

> **Замечание про send.go (валидно для sip i1+i2, §3.2).** Decoy-слоты `i1..i5` шлются как
> независимые UDP-датаграмы ПЕРЕД подлинным `MessageInitiation`: `CreateMessageInitiation` считается
> до цикла по `ipackets`, каждый decoy — `Obfuscate(buf, nil)` отдельным элементом `sendBuffer`,
> подлинный пакет добавляется последним и байт-идентичен стоковому WG; Cloudflare реагирует только
> на валидный `MessageInitiation`, decoy отбрасывает как не-WG-шум. Поэтому любой decoy (в т.ч.
> sip-i2) не может изменить handshake — это и делало multi-packet безопасным.

---

## 10. Вне скоупа

- `dns`/`stun` фрагментация (отдельная таска при необходимости).
- Серверная сторона / probe-response (client-only) — но см. §8: non-QUIC профили осмысленны
  только со своим сервером-ответчиком (это и есть серверная сторона, вне скоупа 009).
- Byte-identical имитация конкретного снимка трафика (рандомизация снижает сигнатуру).
- Многопакетный QUIC (i1+i2) — рассмотрен и отклонён (§9).
- Поведенческая плоскость (timing / вариативность размеров между подключениями) — отдельное
  направление, если passive-shape станет недостаточно.

---

## 9. Ссылки

- RFC 9000 §16 (varint), §14.1 (Initial ≥1200), §17.2.2 (Initial), §19.6 (CRYPTO), §19.7 (PING), §19.1 (PADDING)
- RFC 9001 §5 (Initial secrets), §5.4 (header protection) · RFC 6891 (EDNS OPT) · RFC 5389 (STUN) · RFC 3261 (SIP)
- WireSock open-source (dns/stun/sip структура): <https://github.com/wiresock/amneziawg-install> (`amneziawg-proxy/src/transform.rs`, `quic_handshake.rs`)
- CPS-движок: `submodules/wireguard-go/device/obf.go`, `send.go:135`
- Проводка: `transport/wireguard/device_awg.go`, `option/wireguard_awg.go`
- Память: [[wiresock-id-ip-ib-feasibility]], [[qtls-helpers-reuse-for-quic-initial]]
