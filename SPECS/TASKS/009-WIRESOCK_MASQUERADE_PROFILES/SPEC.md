# SPEC: 009 — WIRESOCK_MASQUERADE_PROFILES

**Фича:** [AWG](../../FEATURES/003-AWG/FEATURE.md)

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
| `Ib` | **Browser** | `chrome` \| `chrome-full` \| `firefox` \| `curl`. Валидируется; только при `ip=quic`. Задаёт и TLS-отпечаток ClientHello (uTLS, тег `with_utls`), и раскладку фреймов Initial: `chrome` — Chrome 155 без PQ key_share, один Initial 1250б; `chrome-full` — Chrome 155 с X25519MLKEM768, один Initial ~2КБ (IP-фрагментация); `firefox` — Firefox 148 по захвату 149, заголовок neqo; `""`/`curl` и сборка без `with_utls` — generic CH. См. §3.1, §4 |

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
`sip` — первый пакет звонка: полный INVITE (i1), §3.2. Структура — стандартный SIP call-setup
(RFC 3261 §17). LDH-валидатор домена совпадает с
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
> назначению), но это не доказано. `sip` одно время был парой i1 = INVITE, i2 = `100 Trying`;
> 09.10.2026 вторая половина убрана: `100 Trying` — ответ сервера, клиент его не шлёт, а
> серверная сторона WireSock (`amneziawg-proxy`) отвечает на INVITE своим `100 Trying` сама.
> Профиль рассчитан на работу с `junk`. Заработает ли это против WARP —
> **ожидает device-проверки**; `quic` остаётся подтверждённо рабочим механизмом, `dns`/`stun`
> реализованы в правильной клиент-инициированной форме и сохранены для проверки/других провайдеров.

### 3.1 QUIC — один Initial с целым ClientHello

`ip=quic` эмитит полный **QUIC Initial (RFC 9001)** с ClientHello, где `Id` идёт как **SNI**.
Весь ClientHello лежит в **одном** Initial; заголовок и раскладка CRYPTO-фреймов повторяют стек
того браузера, чей отпечаток несёт ClientHello (`Ib`, §4). Эталоны — захваты в
`transport/wireguard/testdata/` (Chrome 147, Firefox 149; README с происхождением) и захват
Chrome 133 из LxBox §618:

| `Ib` | ClientHello | Раскладка фреймов | Заголовок | Датаграмма |
|---|---|---|---|---|
| `chrome` | Chrome 155 без PQ key_share (~470б) | **chaos** — как `QuicChaosProtector` в quiche | SCID 0, pn_len 1, pn=1 | 1250б |
| `chrome-full` | Chrome 155 с X25519MLKEM768 (~1.75КБ) | chaos | то же | ~1.9–2.1КБ, один QUIC-пакет, IP-слой режет на 2 фрагмента |
| `firefox` | Firefox 148/149 без PQ (~665б) | **плоская** — один CRYPTO, без PADDING внутри | SCID 3 байта, pn_len 2, pn случайный, QUIC-пакет по размеру фреймов + **нули после него** до 1252 (neqo) | 1252б |
| `""` / `curl` | generic (~294б) | плоская + PADDING внутри (ngtcp2 / quic-go) | SCID 0, pn_len 1, pn=0 | 1250б |

**Decoy генерируется на каждый хендшейк.** Для `ip=quic` `awgIpcLines` не пишет статичный `i1`:
движок получает генератор (`SetDecoyPacketsFunc`, lx-хук в форке wireguard-go) и перед каждым
handshake initiation — старт, rekey каждые 120 с, пробуждение, реконнект — зовёт его и шлёт свежий
Initial с новыми DCID/SCID, TLS random, key_share, раскладкой и шифртекстом. Статичный `<b>`-блоб
(так было до 09.10.2026) повторял одну и ту же датаграмму байт-в-байт каждые две минуты: хеш-сигнатура
для DPI, повтор Initial уже открытого соединения для QUIC-aware узла и постоянный DCID как
идентификатор устройства. Профили `dns`/`stun`/`sip` остаются статичным CPS с `<r>`-энтропией: их
байты не связаны AEAD, движок рандомизирует их сам. Тем самым `id`/`ip`/`ib` для QUIC — не сахар,
разворачиваемый один раз в `i1`, а параметры живого генератора.

**Chaos-раскладка** — пошаговая копия `QuicChaosProtector::BuildDataPacket`
(`quiche/quic/core/quic_chaos_protector.cc`), константы quiche: исходный CRYPTO режется
2–10 раз в случайной точке случайного фрейма (итого до 11 CRYPTO; первый разрез всегда удаётся, следующие могут попасть на однобайтовый кусок и пропасть, как у Chrome), добавляется 2–10 PING,
бюджет PADDING размазывается случайными кусками перед фреймами, затем весь список фреймов
перемешивается Fisher–Yates. Порядок не закреплён ничем: фрейм с `offset=0` бывает и первым, и
нет, как у живого Chrome. Единственный параметр генератора — диапазон размера датаграммы
(`quicGenParams`); число фреймов и PING не настраивается, оно хромовское.

**Почему один пакет.** Полевые прогоны LxBox §617/§618 (Мак, LTE): порядок CRYPTO-фреймов
нагрузки не несёт — in-order и перемешанный Initial проходят одинаково; давится инициация, в
которой ClientHello размазан по **нескольким** Initial (включая побайтовую копию трёх пакетов
живого Chrome с ML-KEM). Поэтому `chrome-full` не повторяет трёхпакетный первый бросок Chrome,
а кладёт тот же ClientHello в один Initial больше MTU: на уровне QUIC это один пакет, на
проводе — два IP-фрагмента. Цена — зависимость от прохождения IP-фрагментов на пути (§618: F
проходил частично). Прежнее объяснение «DPI парсит первый фрейм как начало и fail-open'ится»
опровергнуто §617 и из кода убрано.

`i1` — decoy (src=nil, шлётся перед WG-handshake); реальный TLS-handshake он не завершает,
его задача — валидный первый пакет QUIC-сессии к CDN перед потоком.

Инварианты (проверяются обратным разбором в тестах):
- **chaos** (`chrome`, `chrome-full`): 2 ≤ CRYPTO ≤ 11, 2 ≤ PING ≤ 10, PADDING есть, pn=1, SCID
  пуст; на выборке из 200 пакетов `offset=0` встречается и первым, и не первым;
- **плоская** (`firefox`, `""`, `curl`): ровно один CRYPTO с `offset=0`, он первый, PING нет;
  `firefox` — SCID 3, pn_len 2, нули после пакета, PADDING внутри нет; generic — PADDING-хвост
  внутри, pn=0;
- **паритет с захватами** (`TestQUICInitialCaptureParity`): оба `.bin` расшифровываются нашим
  крипто (known-answer), набор расширений и набор transport parameters сгенерированного ClientHello
  совпадают с захватом (Firefox — и порядок расширений); у `chrome-full` и захватов первый key_share
  — гибрид;
- **динамика** (`TestAwgIpcLinesQUICDynamicDecoy`): для `ip=quic` `i1`/`i2` пусты, генератор даёт
  по одному Initial на вызов с разными DCID и шифртекстом; для `dns` `i1` статичный;
- **I4.** объединение CRYPTO-фреймов по offset = непрерывный валидный ClientHello `[0..N)`,
  без дыр/перекрытий, SNI на месте.

Крипта — RFC 9001 §5 (HKDF-Extract по DCID → `client in` → `quic key/iv/hp`,
AES-128-GCM, header protection; pn_len 1 или 2 по профилю). Свежие DCID/SCID + TLS random +
ephemeral x25519 на каждый вызов → разный ciphertext. Пакет 1250б (length-поле 1232) у chrome и
generic, 1252б у firefox; `chrome-full` растёт под ClientHello (+128б запаса на chaos).

### 3.2 DNS / STUN / SIP

- **dns** — клиентский DNS **query**. Flags `0x0100` (QR=0, RD=1; byte2 ноль), QDCOUNT=1,
  ARCOUNT=1; QNAME из `Id`, QTYPE **HTTPS** (`0x0041`, RR-type 65 — самый частый запрос
  современного браузера), QCLASS IN; OPT RR (TYPE `0x0029`, CLASS=1232, TTL=0, DO=0,
  **RDLENGTH 0 — без опций**, как у stub-резолвера) → весь датаграм парсится как один DNS query
  без хвоста. TXID `<r 2>` свежий на пакет. Прежняя неизвестная EDNS-опция `0xFDE9` с 40
  случайными байтами (наследие серверного S1-хвоста WireSock) убрана 09.10.2026: такого не шлёт ни
  один резолвер, диссектор это помечает. Query, а не response: клиент первым шлёт запрос.
  Генератор — `masqueDNSQueryCPS`.
- **stun** — WebRTC Binding **Request**. type `0x0001`, magic cookie `0x2112A442`, свежий
  txn; атрибуты USERNAME (`0x0006`), ICE-CONTROLLING (`0x802a`), PRIORITY (`0x0024`),
  SOFTWARE (`0x8022` = `libwebrtc`), MESSAGE-INTEGRITY (`0x0008`, HMAC-SHA1), FINGERPRINT
  (`0x8028`, CRC-32). Request, а не response: клиент первым шлёт именно запрос.
  MESSAGE-INTEGRITY структурно валиден, но по произвольному ICE-ключу (реального пароля у
  decoy нет — on-path DPI HMAC всё равно не проверит). Свежая энтропия на вызов; hostname не
  несёт. Генератор — `stun_request_awg.go`.
- **sip** — первый пакет SIP-звонка (call setup, RFC 3261 §17): **i1 = полный INVITE**
  (request-line + Via(branch=z9hG4bK)/To(без tag)/From(tag)/Call-ID/CSeq:N INVITE/Max-Forwards:70/
  Contact/Content-Type/`Content-Length: 0`, **без SDP-тела**), одна самостоятельная валидная
  UDP-датаграмма; `i2` пуст. История форм: фрагментация одного INVITE (head→i1, SDP→i2) оставляла
  каждую датаграмму битой (UDP не реассемблируется); пара INVITE (i1) + `100 Trying` (i2) клала
  **серверный ответ в клиентский слот** — UAC никогда не шлёт `100 Trying`, а серверная сторона
  WireSock отвечает на INVITE своим `100 Trying` (убрано 09.10.2026). Идентификаторы диалога
  (branch/tag/Call-ID/CSeq) запекаются в `<b>` (`newSIPDialog` → `masqueSIPInviteCPS`), чтобы
  заголовки одного сообщения согласовались. Имена пользователей (display + local) и (если `Id`
  пуст) host — произносимые `PseudoGen`-строки, свежие на генерацию; это **не** хардкод
  RFC-примера `alice@atlanta.com`/`bob@biloxi.com` (он — публичный DPI-маяк). `Id` опционален:
  задан → host, пуст → `pgHost()`. Явный пользовательский `i2` рядом с `id/ip/ib` проходит как
  есть. Генератор — `sip_invite_awg.go`.
  **Требует junk** (`jc/jmin/jmax > 0`): профиль рассчитан на отправку вместе с junk-пакетами в
  том же пред-handshake-залпе.

---

## 4. Браузер (`Ib`)

`Ib` валидируется (`chrome|chrome-full|firefox|curl`, только при `ip=quic`) и управляет
**JA3/JA4 ClientHello** (build с `with_utls`) и **раскладкой фреймов** Initial (§3.1):

- **`ib=""` / `ib=curl`** → собственный generic ClientHello (~294б). uTLS не имеет
  curl-QUIC-fingerprint, поэтому curl деградирует на generic. Плоская раскладка.
- **`ib=chrome`** → ClientHello через **uTLS** (форк-сабмодуль `submodules/utls`, тот же, что у
  REALITY): пресет `HelloChrome_155` описывает **TCP**-ClientHello Chrome, а QUIC-ClientHello у
  браузера другой, поэтому спека перестраивается по захватам Chrome 133 (LxBox §618) и Chrome 147
  (`testdata/chrome_147_initial.bin`): шифры — только три TLS 1.3 (без GREASE и TLS 1.2); группы
  `X25519, P-256, P-384` без GREASE; расширения ровно те 11, что шлёт Chrome в QUIC:
  `server_name`, `supported_groups`, ALPN `h3`, `signature_algorithms` (9 алгоритмов из захвата),
  `key_share`, `psk_key_exchange_modes`, `supported_versions`, `compress_certificate`, ALPS `h3`,
  GREASE ECH и **`quic_transport_parameters`** (57) с параметрами Chrome: `initial_rtt` (0x3127,
  шлётся всегда, значение из правдоподобного диапазона), `max_datagram_frame_size` 65536,
  `version_information` v1+GREASE, `max_udp_payload_size` 1472, лимиты 6/15 МБ, 100/103 потоков,
  idle 30 с, пустой `initial_source_connection_id`, GREASE-параметр со случайной длиной 0–15;
  **порядок параметров перемешивается** на каждый вызов (quiche тасует: у 133 и 147 он разный).
  Не шлётся, как и у Chrome в QUIC: GREASE-расширения, `trust_anchors` (в 147 его нет),
  `ec_point_formats`, `status_request`, SCT, `session_ticket`, `extended_master_secret`,
  `renegotiation_info`; `pre_shared_key`/`early_data`/token — атрибуты повторного визита. Гибрид
  `X25519MLKEM768` (~1.2КБ) удалён из supported_groups и key_share — ClientHello (~470б)
  помещается в один Initial 1250б; это отпечаток Chrome 155 с
  `PostQuantumKeyAgreementEnabled=false`. Chaos-раскладка, SCID пуст, pn_len 1, pn=1.
- **`ib=chrome-full`** → та же QUIC-спека, гибридный key_share **сохранён** первым в key_share
  (ClientHello ~1.75КБ), один Initial ~1.9–2.1КБ больше MTU. Отпечаток текущего Chrome;
  датаграмма не хромовская (Chrome шлёт три по 1250), выбор обоснован в §3.1.
- **`ib=firefox`** → пресет `HelloFirefox_148`, перестроенный под QUIC по захвату Firefox 149
  (`testdata/firefox_149_initial.bin`): три шифра TLS 1.3 в порядке NSS; 15 расширений **в
  порядке захвата** (NSS в QUIC упорядочивает иначе, чем в TCP): `extended_master_secret`,
  `delegated_credentials`, `record_size_limit`, ALPN `h3`, `status_request`,
  `signature_algorithms` (11), `renegotiation_info`, `compress_certificate`, `server_name`,
  `key_share` (X25519, P-256), `supported_versions`, `supported_groups` (X25519, P-256, P-384,
  P-521 — без FFDHE), `psk_key_exchange_modes`, `quic_transport_parameters`, GREASE ECH; нет
  `ec_point_formats`, SCT, `session_ticket`. Transport parameters — 13 по захвату в порядке id:
  idle 30 с, `initial_max_data` 24 МБ, bidi_local 12 МБ, bidi_remote/uni 1 МБ, 100/100 потоков,
  `max_ack_delay` 20, `active_connection_id_limit` 8, `initial_source_connection_id` = SCID (3
  байта), `version_information`, `min_ack_delay` (draft, 1000), `max_datagram_frame_size` 65535.
  PQ-гибрид срезан (один пакет). Заголовок neqo: SCID 3 байта, pn_len 2, pn случайный, нули
  после пакета до 1252. Плоская раскладка.
- **GREASE-версия** в `version_information` считается у нас: `(rand & 0xf0f0f0f0) | 0x0a0a0a0a`
  (хелпер форка utls ставит `| 0x0a0a0a0a` без маски и даёт форму `?a?a?a?a` в 1 случае из 256).

**Зачем валидный QUIC-ClientHello, если decoy никто не отвечает.** До 09.10.2026 uTLS-путь (с
18.06.2026) слал TCP-ClientHello внутри Initial: без расширения 57, с TLS 1.2-шифрами и
TCP-расширениями. На WARP это не проявлялось (Cloudflare decoy молча дропает, §9), но
серверная сторона WireSock — `amneziawg-proxy/src/quic_handshake.rs` — держит настоящий
QUIC-сервер на `quinn-proto`+`rustls`: расшифровывает Initial, разбирает ClientHello, подбирает
сертификат под SNI и **отвечает серверным flight'ом** (Initial + Handshake с сертификатом),
чтобы наблюдатель видел и ответ сервера. ClientHello без `quic_transport_parameters` `rustls`
отвергает — вместо сертификата ушёл бы `CONNECTION_CLOSE` с TLS-alert, то есть QUIC-сессия,
оборванная сервером на первом пакете. Generic ClientHello (`ib=""`) этого дефекта не имел.
- Без тега `with_utls` `ib=chrome/chrome-full/firefox` деградируют на generic CH (stub-файл);
  раскладка при этом остаётся по `Ib`.

Код: `quic_clienthello_utls_awg.go` (+ stub `…_utls_stub_awg.go`), раскладка —
`quic_initial_awg.go`.

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
- **`Ib` ∈ {chrome,chrome-full,firefox,curl}** (lower) и только при `ip=quic`; иначе ошибка.

---

## 6. Файлы (зоны)

| Файл | Зона | Что |
|------|------|-----|
| `option/wireguard_awg.go` | lx | поля `Id/Ip/Ib` |
| `transport/wireguard/masque_awg.go` | lx, `with_awg` | диспетчер `masqueI1` + валидация + DNS query + `cpsBuilder` |
| `transport/wireguard/quic_initial_awg.go` | lx, `with_awg` | QUIC Initial: varint, рандомизированный frame-план (I1–I4) + `quicGenParams`, сборка RFC 9001 |
| `transport/wireguard/quic_clienthello_awg.go` | lx, `with_awg` | generic TLS 1.3 ClientHello (SNI=`Id`) + диспетч по `Ib` |
| `transport/wireguard/quic_clienthello_utls_awg.go` | lx, `with_awg && with_utls` | uTLS браузерный ClientHello (chrome/chrome-full/firefox JA3, §4) |
| `transport/wireguard/testdata/` | lx | захваты Chrome 147 / Firefox 149 (BSD-3, из wiresock-boringtun), README |
| `submodules/wireguard-go/device/{device,send}.go` | форк, `// lx:` | `SetDecoyPacketsFunc` — генератор decoy на каждый handshake initiation |
| `transport/wireguard/quic_clienthello_utls_stub_awg.go` | lx, `with_awg && !with_utls` | fallback на generic, когда uTLS не собран |
| `transport/wireguard/quic_crypto_awg.go` | lx, `with_awg` | HKDF / AES-128-GCM / header protection |
| `transport/wireguard/stun_request_awg.go` | lx, `with_awg` | STUN WebRTC Binding Request (FINGERPRINT + MESSAGE-INTEGRITY) |
| `transport/wireguard/sip_invite_awg.go` | lx, `with_awg` | первый пакет SIP-звонка: полный INVITE (i1), без SDP |
| `transport/wireguard/pseudo_gen_awg.go` | lx, `with_awg` | произносимые псевдо-имена/host/IP (для SIP) |
| `transport/wireguard/device_awg.go` | lx, `with_awg` | вызов `masqueI1` в `awgIpcLines` |
| `transport/wireguard/masque_awg_test.go`, `quic_initial_awg_test.go` | lx, `with_awg` | тесты |

Сабмодуль `submodules/wireguard-go` не трогается.

---

## 7. Критерии приёмки

- **Структурная валидность каждого профиля** (обратным разбором, не тавтология): QUIC —
  собственный вывод AEAD-расшифровывается (тег сходится), frame-walk даёт 2–11 CRYPTO + ≥2
  PING + PADDING у chaos-профилей, CRYPTO реассемблируются в валидный
  ClientHello с SNI=`Id` (I4); DNS — валидный EDNS **query** (QR=0, QNAME=`Id`, QTYPE HTTPS,
  OPT с RDLENGTH 0, без хвостов); STUN — Binding **Request** (cookie, атрибуты тайлят сообщение,
  FINGERPRINT CRC-32 сходится, USERNAME + MESSAGE-INTEGRITY присутствуют); SIP — i1 — валидный
  INVITE **request** (request-line `INVITE ... SIP/2.0`, Via/Max-Forwards/From/To-без-tag/Call-ID/
  CSeq/Contact, `Content-Length: 0`, без SDP-тела), i2 пуст, имена не захардкожены.
- **Рандомизация QUIC:** chaos-раскладка свежая на каждый вызов; census (2–11 CRYPTO, 2–10
  PING, PADDING) и сборка I4 держатся на каждом сэмпле (стресс-тест), две генерации → разные
  offset'ы фрагментов (нет фикс-сигнатуры); на 200 сэмплах `offset=0` бывает и первым, и нет.
  Переменный размер датаграммы (`quicGenParams`) держит то же.
- **Уникальность:** два вызова QUIC с одним SNI → разные DCID/TLS random → разный
  ciphertext; два вызова STUN → разный txn/ufrag/ключ → разный blob.
- **`Ib` JA3 и раскладка (build с `with_utls`):** `ib=chrome` → три шифра TLS 1.3, 11 расширений
  QUIC-набора Chrome без GREASE-расширений, TP-набор Chrome (без учёта GREASE-id), key_share
  X25519, chaos, pn=1, 1250б; `ib=chrome-full` → гибрид первым в key_share, ClientHello и
  датаграмма > 1250б, chaos; `ib=firefox` → три шифра NSS, 15 расширений в порядке захвата, группы
  без PQ и FFDHE, TP-набор Firefox, SCID 3 байта = `initial_source_connection_id`, pn_len 2, нули
  после пакета, 1252б, плоская; `ib=""`/`curl` → generic ~294б, плоская, pn=0. Паритет с
  захватами `testdata/` (расшифровка нашим крипто, наборы расширений/TP). Без `with_utls`
  chrome/chrome-full/firefox деградируют на generic (stub компилируется и тестируется).
- **Длинный домен:** валидный LDH-домен любой длины (≤253) генерируется без ошибки (бюджет
  PADDING поглощает разницу; CH растёт с длиной SNI, инварианты сохраняются).
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

## 9. QUIC — один Initial (multi-packet рассмотрен и отклонён дважды)

`ip=quic` эмитит **ОДИН** Initial (`i1`; `i2..i5` пусты) с целым ClientHello внутри — при любом
`Ib`, включая `chrome-full`, где ради этого датаграмма растёт за MTU (§3.1).

**Отклонено (1): два независимых Initial с разными DCID (i1+i2).** Реализовано и device-проверено
как безопасное для WARP-handshake, но концептуально неверно: каждый DCID — отдельное
QUIC-соединение, два Initial читаются как два брошенных соединения. Настоящее «продолжение»
(1-RTT short-header с тем же DCID) невозможно: short-header device-blocked (коммит `64ce4a47`),
а 1-RTT до ответа сервера — невозможное QUIC-состояние. (`masqueQUICSecondInitialCPS` удалён.)

**Отклонено (2): ClientHello, размазанный по нескольким Initial одного DCID, как у Chrome с
ML-KEM.** Полевой прогон LxBox §618 (Мак, провод, 08.10.2026): все многопакетные формы — два
Initial по порядку, границы по SNI, ровно 1200б, с token, побайтовая копия трёх пакетов живого
Chrome 133 — давятся на wartune.mail.ru и большинстве доменов (исключение apteka.ru);
проходят только формы, где весь ClientHello уехал одним QUIC-пакетом, в том числе один Initial
больше MTU с IP-фрагментацией. Многопакетная инициация к тому же «отравляет» 5-tuple на
несколько минут. Разделяющий тест «два пакета, первый несёт целый ClientHello» (D) на момент
записи не прогнан; формулировка причины (число пакетов или неполный первый Initial) открыта,
на реализацию не влияет — оба варианта ведут к одному Initial.

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
- Многопакетный QUIC (i1+i2 с разными DCID; ClientHello по нескольким Initial) — рассмотрен и отклонён (§9).
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
