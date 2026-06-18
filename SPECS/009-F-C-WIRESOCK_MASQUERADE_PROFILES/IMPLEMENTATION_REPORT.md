# IMPLEMENTATION_REPORT — 009 WIRESOCK_MASQUERADE_PROFILES

**Статус:** Closed. Код + тесты + DoD; device-smoke на активном DPI пройден (туннель + трафик).
**Коммиты:** `51d5cff1` (id/ip/ib + dns/stun/sip + QUIC), `64ce4a47` (QUIC → out-of-order
фрагментированный Initial).

---

## Принятые решения

### Р1. Механизм — I1 CPS, не S1–S4
`Id/Ip/Ib` разворачиваются в `i1` CPS-строку в option/transport-слое; сабмодуль
`submodules/wireguard-go` не трогается. S1–S4 padding отвергнут: init/response должны
оставаться бит-в-бит как plain WG, иначе Cloudflare WARP отвергает handshake.
Код: `masqueI1` — `transport/wireguard/masque_awg.go:64`; проводка в `awgIpcLines`
(`device_awg.go`); decoy шлётся `Obfuscate(buf, nil)` (`submodules/wireguard-go/device/send.go:135`).

### Р2. QUIC — out-of-order фрагментированный Initial
`ip=quic` эмитит полный QUIC Initial (RFC 9001) с реалистичным ClientHello (SNI=`Id`),
нарезанным на 6 CRYPTO-фреймов в перемешанном порядке: первый wire-фрейм `offset≠0`,
`offset=0` почти в конце, PING/PADDING между ними (инварианты I1–I4). Line-rate DPI берёт
первый фрейм как `offset 0`, парсит середину ClientHello как начало → мусор → fail-open;
настоящий сервер реассемблирует по offset. Причина выбора: 1-RTT short header был
эмпирически заблокирован реальным LTE-DPI; фрагментированный Initial device-proven против
того DPI.
Код: диспетч `masque_awg.go:97`; `masqueQUICInitialCPS` — `quic_initial_awg.go:384`;
`buildInitialPacket` — `quic_initial_awg.go:319`; frame-план `etalonWirePlan` —
`quic_initial_awg.go:139`; cutpoints `etalonCutpoints` — `quic_initial_awg.go:128`.

### Р3. Крипта — копия RFC 9001-примитивов из qtls, не своя реализация
HKDF-Expand-Label / QUIC v1 salt / AES-128-GCM-XOR-nonce AEAD скопированы байт-в-байт из
`common/sniff/internal/qtls/qtls.go` (этот `internal/` не импортируется из `transport/`,
поэтому копия, а не импорт) → выводимые ключи совпадают с боевым снифером. Единственный
недостающий примитив — varint-энкодер `appendQUICVarint` (RFC 9000 §16), написан вручную.
Код: `transport/wireguard/quic_crypto_awg.go` (`quicHKDFExpandLabel`, `quicAEADAESGCMTLS13`,
`quicSaltV1`); `deriveInitialKeys` — `quic_initial_awg.go:269`; `encryptInitial` (шифрование +
header protection) — `quic_initial_awg.go:287`.

### Р4. `Id` обязателен только для quic
`Id` идёт на провод как SNI (quic) → обязателен только для `quic`; пустой при quic
отвергается на `sing-box check`. Опционален для `sip` (пуст → псевдо-host) и `stun`
(hostname-less). Заданный `Id` всегда LDH-валидируется.
dns/sip при пустом `Id` генерируют псевдо-имя (PseudoGen), stun его игнорирует. Код: guard `masque_awg.go` (quic ветка); LDH-валидатор `validateMasqueDomain`
(security-граница, зеркало `is_valid_sni_hostname`).

### Р5. flex-PADDING — payload пинится к length-полю при любой длине SNI
Один PADDING-run в frame-плане помечен `padFlex` и вычисляется как остаток до payload-таргета
→ payload всегда ровно 1232 (length-поле) для любой длины ClientHello (длинный домен удлиняет
CH и укорачивает flex-run). Без этого валидный домен >77 символов переполнял фиксированную
раскладку и ронял генерацию (находка адверсариального ревью).
Код: `planEntry.padFlex` — `quic_initial_awg.go:115`; `buildInitialPayload` — `quic_initial_awg.go:212`;
ClientHello добивается до ≥294б padding-расширением (`quicCHTargetLen=294`, `quicCHMinLen=291`) —
`quic_clienthello_awg.go:31,35`.

### Р6. ClientHello — реалистичный, без JA3-имитации
TLS 1.3 ClientHello: непустой cipher_suites, key_share (реальный ephemeral x25519,
`ecdh.X25519().GenerateKey` — `quic_initial_awg.go:330`), ALPN "h3", quic_transport_params,
supported_versions, GREASE `0x0a0a` (RFC 8701 — `quic_clienthello_awg.go:117`). Конкретный
браузерный JA3/JA4 не имитируется: bypass держится на фрагментации, не на fingerprint. `Ib`
валидируется (`normalizeMasqueBrowser` — `masque_awg.go:183`), но на байты пакета не влияет.
Код: `buildClientHello` — `quic_clienthello_awg.go:67`.

### Р7. sip — начало звонка: INVITE (i1) + 100 Trying (i2), один диалог
`ip=sip` эмитит **начало SIP-звонка из двух самостоятельных пакетов** одного диалога (RFC 3261
§17 call-setup), `sip_invite_awg.go`: i1 = полный INVITE **request** (`masqueSIPInviteCPS`):
request-line `INVITE sip:<user>@<host> SIP/2.0`, Via(branch=z9hG4bK)/To(без tag)/From(tag)/Call-ID/
CSeq:N INVITE/Max-Forwards:70/Contact/Content-Type: application/sdp/`Content-Length: 0`, пустая
строка — **без SDP-тела**; i2 = полный `SIP/2.0 100 Trying` provisional response
(`masqueSIPTryingCPS`): статус-строка + те же Via/To/From/Call-ID/CSeq + `Content-Length: 0`.
Каждый пакет — валидное самодостаточное SIP-сообщение: UDP не реассемблируется, пакетный DPI
смотрит каждую датаграмму отдельно. **Один диалог**: Via branch / From tag / Call-ID / CSeq
идентичны в i1 и i2 — общий диалог строит `newSIPDialog(domain)` одним проходом, оба слота
наполняет диспетчер `masqueI1I2`. Значения запечены в `<b>` на сборке (НЕ per-packet `<rc>`/`<rd>`),
уникальны между юзерами. Имена пользователей (display+local) и host (при пустом `Id`) —
произносимые псевдо-строки через `PseudoGen` (`pseudo_gen_awg.go`, портирован из LxBox §127),
свежие на генерацию; **не** хардкод RFC-примера `alice@atlanta.com`/`bob@biloxi.com` (публичный
DPI-маяк). Профиль **требует junk** (`jc/jmin/jmax > 0`): декои уходят вместе с junk-пакетами в
одном пред-handshake-залпе. `Id` опционален для sip (пуст → `pgHost()`). Прежняя одиночная форма
(один INVITE с SDP, затем короткая версия «фрагментация INVITE на head→i1 + SDP→i2») — заменена.
(DNS см. Р10.)

### Р8. Рандомизация QUIC-раскладки + robustness-ручки
Раскладка фрейм-плана генерится на каждый вызов: случайные точки разреза
(`planFragmentsN`) и случайный out-of-order порядок (`randomizedWirePlan`), при этом I1–I4
держатся по построению (перестановка фрагментов + ремонт «offset-0 не первый» + flex-PADDING).
Убирает фиксированную межюзерную сигнатуру (раньше был зашит `etalonWirePlan`, удалён).
`quicGenParams` (`defaultQUICGenParams` — 6 фрагментов, 2 PING, 1250б) — ручки эскалации без
правки кода: число фрагментов/PING и диапазон размера датаграммы; length-поле/payload
пересчитываются от размера. Код: `quic_initial_awg.go` (`quicGenParams`, `planFragmentsN`,
`randomizedWirePlan`, `pickTotalLen`). Стресс-тест: 300 случайных пакетов держат I1–I4.

### Р9. STUN — Binding Request вместо Response
`ip=stun` теперь эмитит WebRTC Binding **Request** (`0x0001`): USERNAME, ICE-CONTROLLING,
PRIORITY, SOFTWARE=`libwebrtc`, MESSAGE-INTEGRITY (HMAC-SHA1 по случайному ICE-ключу),
FINGERPRINT (CRC-32). Причина: Success Response (`0x0101`), посланный клиентом первым и без
запроса — аномалия направления; Request — то, что ICE-клиент шлёт первым. Свежий
txn/ufrag/ключ на вызов. Код: `stun_request_awg.go` (`buildSTUNBindingRequest`,
`masqueSTUNRequestCPS`); диспетч `masque_awg.go:103`. Старый `masqueSTUNResponseCPS` удалён.

**Device-результат:** ни Binding Request, ни полный WebRTC-вариант с MESSAGE-INTEGRITY **не
прошли** тестовый LTE/WARP DPI (Timeout, тогда как `quic` ✅ ~340 мс). См. общий вывод после Р10.

### Р10. DNS — query вместо response
`ip=dns` теперь эмитит клиентский DNS **query** (`masqueDNSQueryCPS`, `masque_awg.go`): flags
`0x0100` (QR=0, RD=1), QNAME=`Id`, QTYPE **HTTPS** (`0x0041`), OPT RR с cover-байтами в опции
`0xFDE9`. Причина: прежний response (`0x8180`, QR=1) — аномалия направления (ответ без запроса в
слоте клиента), как у STUN. Правка от прежнего кода — только flags и QTYPE; `encodeDNSName`/OPT/
cover переиспользованы. TXID/cover свежие на пакет (`<r 2>`/`<r 40>`).

**Device-результат:** DNS query — **Timeout** (как STUN). SIP (Р7, двухпакетный INVITE+100 Trying)
**ожидает проверки на устройстве** — причина прошлых таймаутов точно не установлена, сейчас
проверяем гипотезу с multi-packet-формой и junk.

**Общий вывод (Р9+Р10).** Качество пакета и направление (request vs response) вторичны; решает
триплет **(протокол + назначение)**. DPI режет STUN/DNS/SIP к WARP-edge `162.159.x:2408` как
класс протокола — raw STUN/DNS/SIP к дата-центровому IP сами по себе аномальны (DNS живёт на
:53, STUN — на STUN-сервере). `quic` обходит проверку назначения: QUIC/HTTP3 легитимно идёт
куда угодно, поэтому QUIC к Cloudflare-IP — ожидаемый трафик. `quic` — единственный проверенный
рабочий механизм здесь; `dns`/`stun`/`sip` реализованы в правильной клиент-инициированной форме
и сохранены для других провайдеров (DPI без проверки протокол-к-назначению).

### Р11. Многопакетный QUIC (i1+i2) — рассмотрен и ОТКЛОНЁН
Был реализован вариант «два независимых Initial» (i1+i2) и device-проверен как безопасный для
WARP-handshake (туннель без регресса латентности). Затем **отклонён** как концептуально неверный:
каждый DCID — отдельное QUIC-соединение, поэтому два Initial с разными DCID читаются как два
*брошенных* соединения, что для DPI с DCID-tracking более аномально, не менее. Настоящее
«продолжение» невозможно (short-header device-blocked; 1-RTT до ответа сервера — невозможное
состояние). Итог: `ip=quic` = **один** фрагментированный Initial с браузер-точным ClientHello (§4);
`masqueI1I2` для quic возвращает i2="", `masqueQUICSecondInitialCPS` удалён. Безопасность slots-механизма
(send.go: decoy перед независимым `MessageInitiation`) остаётся актуальной для sip i1+i2 (Р7).

### Р12. `Ib` → реальный браузерный JA3 через uTLS
`ib=chrome`/`firefox` теперь строит ClientHello через uTLS (`github.com/metacubex/utls`, тот же,
что у Reality) в QUIC-режиме (`UQUICClient` + `HelloChrome_120`/`HelloFirefox_120`) → настоящий
браузерный JA3/JA4. ALPN форсируется в `h3`, PQ-гибрид key_share (`X25519MLKEM768`) удаляется (не
влез бы в один Initial; паттерн из `reality_client.go`). Решение «`ib` опционален»: `ib=""`/`curl`
→ generic device-proven ~294б CH (дефолт не трогаем); `ib=chrome`/`firefox` → uTLS (~510–620б).
Build-tag split: `quic_clienthello_utls_awg.go` (`with_utls`) / `…_stub_awg.go` (`!with_utls` →
fallback на generic). Фрагментация (`planFragmentsN`) режет CH любой длины — I1–I4 держатся.
Это задел против будущего JA3/JA4-DPI; на текущем DPI `ip=quic` проходит на фрагментации, поэтому
uTLS-вариант сам по себе device не верифицирован, а дефолт `ib=""` остаётся проверенным.

---

## Верификация

- **§5-векторы обратным разбором** (`quic_initial_awg_test.go`): AEAD-тег сходится; ≥6 CRYPTO
  + ≥1 PING + PADDING; первый offset≠0; реассембл в валидный ClientHello, SNI=`Id`; размер
  1250 / length 1232; уникальность DCID+random; длинный домен генерируется.
- **Cross-check боевым снифером:** сгенерированный Initial парсится `common/sniff/quic.go`
  (SNI извлечён, классификация chromium).
- **Рандомизация QUIC** (`quic_initial_awg_test.go`): `TestQUICInitialRandomizedInvariants`
  (80 сэмплов, I1–I4 + offset'ы различаются) и `TestQUICInitialRobustnessKnobs` (4/10/12
  фрагментов, переменный размер).
- **STUN Request** (`masque_awg_test.go`): `TestMasqueSTUNRequestStructure` (тип `0x0001`,
  атрибуты тайлят сообщение, FINGERPRINT CRC-32 сходится, USERNAME+MESSAGE-INTEGRITY есть),
  `TestMasqueSTUNRequestUniqueness`.
- **dns/sip + валидация** (`masque_awg_test.go`, `masque_cps_test.go`): `TestMasqueDNSQueryStructure`
  (QR=0, QNAME round-trips, QTYPE HTTPS, OPT до конца); `TestMasqueSIPInviteStructure` +
  `TestMasqueSIPInviteNoID` проверяют пару i1 INVITE (`Content-Length: 0`, без SDP) + i2
  `100 Trying` и согласованность диалога (`assertSIPInvite`/`assertSIPTrying`/`assertSameSIPDialog`:
  request-line INVITE, To без tag, общий branch/tag/Call-ID/CSeq, имена не захардкожены, пустой
  id → псевдо-host); инъекция домена отвергается; конфликт с `i1` /
  неизвестный ip/ib / пустой id для quic — ошибки.
- **Адверсариальный ревью** (workflow): подтверждённые находки исправлены (flex-PADDING для
  длинного SNI — Р5; GREASE `0x4469`→`0x0a0a` — Р6; response→request для STUN — Р9; SIP missing
  Contact / static caller@ — закрыто request-формой с Contact + PseudoGen-именами в Р7).
- `go build` (с тегами и без) ок; `go test -tags with_awg ./transport/wireguard/...` зелёный;
  `gofmt -l` lx-файлов пусто; `sing-box check` на quic/dns/stun/sip ок (dns/sip/stun без id тоже), пустой id для quic
  отвергнут; gating без `with_awg` → «awg support not built».
- **Device-smoke:** узел `ip=quic` с фрагментированным Initial поднимает туннель и проводит
  реальный трафик через активный DPI.

---

## Зона касания upstream (для ребейза)

Все новые файлы под `with_awg` (в upstream их нет) → конфликтов на ребейзе не дают.
`option/wireguard_awg.go` — lx-файл целиком. `device_awg.go` — lx-файл под тегом. Сабмодуль
`submodules/wireguard-go` не трогали.
