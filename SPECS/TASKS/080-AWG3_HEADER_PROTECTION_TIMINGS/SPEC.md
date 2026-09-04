# SPEC: 080 — AWG3_HEADER_PROTECTION_TIMINGS

**Фича:** [AWG2](../../FEATURES/003-AWG2/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | **C** (complete) — device-verified против живого AWG 3.1-сервера 2026-09-05 |
| Подмодуль | `submodules/wireguard-go` (ветка `lx-awg2-v005`; порт amneziawg-go `v0.2.19 → v3.1.20260828`, HEAD `b5928ef`) |
| Связанные | [[SPECS/TASKS/003-AWG2_CLIENT_ENDPOINT]] · [[SPECS/TASKS/005-AWG2_RANGED_MAGIC_HEADERS]] · [[SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION]] · [[SPECS/TASKS/025-AWG_TRANSPORT_PADDING_OVERRUN]] · [[SPECS/TASKS/026-AWG_MAGIC_VS_RESERVED_CLEAR]] · [[SPECS/TASKS/031-AWG_PARITY_AUDIT_ADVANCED_SECURITY]] · [[SPECS/TASKS/041-WG_HANDSHAKE_GIVEUP_REBIND]] |

Поддержать **AmneziaWG 3.0/3.1** — защиту заголовка (`HeaderProtectionKey`),
паддинг содержимого (`ContentPaddingAddition`), случайные хвосты (`RandomTrailers`),
отключение cookie (`DisableCookies`), диапазонные тайминги (`RekeyAfterTime`,
`RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout`, `MaxHandshakeAttempts`) и
диапазонный `PersistentKeepalive` пира — по всей цепочке конфиг → UAPI → устройство,
байт-совместимо с amneziawg-go v3.1.

---

## 1. Проблема / контекст

Владельцу дали тестовый сервер Amnezia: экспорт `vpn://` с контейнером `amnezia-awg2`,
`protocol_version: "3.1"`. В его `.conf`, помимо AWG2-набора (`Jc/Jmin/Jmax`, `S1–S4`,
`H1–H4 = 1..4`, пустые `I1–I5`), стоят:

```ini
HeaderProtectionKey = pBNb…Xgc=
ContentPaddingAddition = 10-100
RekeyAfterTime = 100-120
RekeyTimeout = 3-7
RejectAfterTime = 150-180
KeepaliveTimeout = 5-15
MaxHandshakeAttempts = 15-20
RandomTrailers = on
DisableCookies = on
[Peer] PersistentKeepalive = 25-35
```

**Наша прививка — AWG 2.0** (`lx-awg2-v005`, база amneziawg-go v0.2.19). Она не знает
ни одного из этих ключей: UAPI отвергает `header_protection_key` как «invalid UAPI
device key», а если бы и приняла — сервер шифрует заголовок каждого пакета, и без
ключа клиент не отличит handshake response от мусора. Итог — «нода не встаёт».

[SPEC 031](../031-AWG_PARITY_AUDIT_ADVANCED_SECURITY/SPEC.md) §3/§3.1 в 2026-07
списала эти ключи как «неизданную разработку master-ветки, не часть протокола». С тех
пор Amnezia выпустила их официально: amneziawg-go `v3.0.0` («feat: amneziawg 3.0»,
2026-07) … `v3.1.20260828`; amneziawg-tools `v3.0.20260730` (парсер `.conf` +
netlink-атрибуты для ядерного модуля) и `v3.1.20260812` («feat: add awg 3.1 params»:
`RandomTrailers`, `DisableCookies`). Вывод SPEC 031 устарел — там поставлен баннер.

## 2. Цель

Конфиг, перенесённый 1:1 из экспорта AWG 3.1 (`.conf` или `vpn://`), проходит
`sing-box check`, а endpoint делает хендшейк и держит трафик с сервером на
amneziawg-go v3.1. Конфиг без AWG3-полей ведёт себя как раньше (AWG2 / plain WG —
байт-в-байт).

## 3. Что сделано

### 3.1 Протокол AWG 3.x (по исходникам amneziawg-go v3.1)

Сверено по диффу `v0.2.19..HEAD` (`device/{device,peer,noise-types,noise-protocol,
send,receive,timers,uapi}.go`), README и amneziawg-tools `src/config.c`, `src/ipc-uapi.h`.

**Header protection.** Каждая датаграмма: `[S_k случайных байт][сообщение]`.
Keystream = ChaCha20(`header_protection_key`, nonce = первые 12 байт датаграммы, т.е.
паддинга). Отправитель XOR-ит keystream'ом с байта 0: у handshake-сообщений
(init/response/cookie) — всё сообщение целиком (148/92/64 байт), у transport — только
16-байтный заголовок (тип, receiver index, счётчик); AEAD-payload не трогается.
Приёмник снимает keystream[0:4] с типа на каждом кандидатном смещении `S1..S4`
(nonce один — первые 12 байт датаграммы, независимо от вида сообщения), классифицирует
по `H1..H4`, затем XOR-ит остальное. Отсюда требование `S1–S4 ≥ 12`, которое UAPI
проверяет при каждом `IpcSet` (merge стадии с учётом уже стоящего ключа).

**Content padding addition** — вместо выравнивания plaintext по 16: к plaintext
каждого transport-пакета добавляется `PickOne(min-max)` нулей, но не больше, чем
`udpWindow − размер датаграммы`, где `udpWindow` — самая большая датаграмма, виденная
на этом пире в любую сторону (старт `DefaultUdpWindow = 500`). Нули внутри AEAD, на
проводе случайны.

**Random trailers** — у handshake-сообщений: случайный хвост длиной
`fastrandn(udpWindow − size)` (cookie — по `DefaultUdpWindow`), приёмник принимает
`size > expected` и обрезает. У transport — если `content_padding_addition` не задан,
тот же расчёт даёт добавку внутрь AEAD.

**Disable cookies** — не слать cookie reply и не входить в under-load-ветку
(`!disableCookies && IsUnderLoad()`, фикс `b5928ef`).

**Тайминги** — `UintRange` (uint64: hi<<32|lo) на каждую константу; вычисляются
хелперами: `retransmitHandshakeTimeout`/`sendKeepaliveTimeout` = PickOne;
`newHandshakeTimeout` = keepalive.Hi + rekey.PickOne; `keyRefreshTimeoutReceiving` =
reject.PickOne − keepalive.Lo − rekey.Lo; `keychainExpireTime` = reject.Hi;
`rekeyMinTimeout` = rekey.Lo; `maxHandshakeAttempts` = PickOne на цикл.
`persistent_keepalive_interval` пира — тоже `UintRange`, PickOne при каждом взводе.

**UAPI-ключи** (device): `header_protection_key=<hex>`, `content_padding_addition`,
`rekey_after_time`, `rekey_timeout`, `reject_after_time`, `keepalive_timeout`,
`max_handshake_attempts` (`N` | `N-M`), `random_trailers`, `disable_cookies`
(`strconv.ParseBool`: `1`/`true`); peer: `persistent_keepalive_interval=N-M`.

### 3.2 Порт в прививку (`submodules/wireguard-go/device`)

Перенесён весь дифф, с оговорками ниже. Все правки под маркерами `lx:` / `lx:begin awg3`.

| Файл | Что |
|------|-----|
| `noise-types.go` | `HeaderCipherKey`, `UintRange`, `AtomicUintRange` (замена `magicHeader`; `magic-header.go` удалён) |
| `constants.go` | `DefaultUdpWindow = 500` |
| `device.go` | `junk`/`paddings` → atomics, `headers` → `AtomicUintRange`, `headerProtection.key`, `contentPaddingAddition`, `timings`, `randomTrailers`, `disableCookies`; `keychainExpireTime()` в `SendKeepalivesToPeersWithCurrentKeypair` |
| `peer.go` | `timers.maxHandshakeAttempts`, `persistentKeepaliveInterval AtomicUintRange`, `udpWindow`; `rekeyMinTimeout()` в Start/Expire; сброс окна при роуминге |
| `noise-protocol.go` | `PickOne()` для типов, `JunkPackets()`, `HeaderProtectionCipher()` |
| `send.go` | раскладка датаграммы строится in-place: `[headroom][s4][header][sealed]`; init/response/cookie с паддингом+шифром+хвостом; `randomPaddingAddition`/`randomTrailer`; `isKeepalive`; тайминги |
| `receive.go` | keystream/typeHash, новая `DeterminePacketTypeAndPadding` (size,type,padding), снятие шифра по видам, `elem.padding` (паддинг остаётся в буфере, TUN-write со смещением), `udpWindow`, keepalive по `packet[0]==0`, `disable_cookies` |
| `timers.go` | хелперы таймингов; все константы через них; `maxHandshakeAttempts` |
| `uapi.go` | новые ключи; `ipcSetDevice` с `fromDevice`/`mergeWithDevice` (S1–S4 ≥ 12 при ключе); get-путь |
| `lx_giveup_rebind.go` | `sessionProvablyDead` по `keychainExpireTime()` |

**Осознанные отличия от референса** (на провод не влияют):

1. **Первый батч после старта.** TUN-ридер захватывает `s4` до блокирующего `Read`,
   а `IpcSet` приходит позже (порядок sing-box: `NewDevice → IpcSet → Up`) — в
   референсе первый батч уезжает в старой раскладке (padding 0) и сервером
   отбрасывается. У нас `RoutineEncryption` считает актуальный `s4` авторитетным и
   переносит plaintext под него (`memmove` в том же буфере); tight-буфер инжекции,
   которому не хватает места, дропается с verbose-логом. Поймано тестом
   `TestAWG3EndToEnd` («Header protection failed … at least 12 bytes» в логе).
2. **Сброс `udpWindow` при роуминге** — референс сравнивает `conn.Endpoint` как
   интерфейс по идентичности; `StdNetBind` отдаёт новый указатель на каждую датаграмму,
   так что окно сбрасывается на каждый батч и фактически ≈500. У нас сравнение по
   `DstToString()` — окно растёт до реально виденного максимума. Добавка всё равно
   ограничена диапазоном (`10-100`), разница проявляется только у пакетов в пределах
   100 байт от окна.
3. **`headerProtection.key` — `atomic.Pointer`**, а не `RWMutex` (RLock на каждый пакет).
4. **`JunkPackets`**: сохранён guard `jmin>jmax` (SPEC 008; в референсе беззнаковое
   `max-min` переполняется в многогигабайтный `make`) и включительный `jmax`.
5. **`UintRange.PickOne`** для полного диапазона `0..2^32-1` (`hi-lo+1` = 0 в uint32;
   референс отдаёт константу `lo`) — `rand.Uint32()`, guard SPEC 025 сохранён.
6. **`HeaderProtectionCipher`** проверяет длину nonce (референс режет `crypt[:12]`
   за `len`), ошибка → пакет дропается, а не паникует.
7. **Инжекция (`InputPacket/InputPackets`)** аллоцируется по размеру (SPEC 020/025):
   `outboundLayout` + `outboundTailroom` (запас под добавку), добавка клампится по
   `cap` буфера — никогда не выходит за буфер.
8. **Дропнутые в шифровании элементы** (`packet=nil`) пропускаются отправителем;
   референс шлёт пустую датаграмму.
9. **`Timer`** оставлен наш (isPending + `pauseManager.WaitActive`, SPEC 020); в
   референсе `duration` ради текста лога — у нас длительности в логах считаются
   хелперами.
10. **IpcGet**: `random_trailers`/`disable_cookies` эмитятся только когда включены;
    `persistent_keepalive_interval` — всегда (паритет с WireGuard xplatform), `h1–h4` —
    всегда (как и в AWG2-прививке: дефолты 1–4 хранятся на устройстве).
11. **`maxHandshakeAttempts`** = 0 (пир не стартовал через `timersStart`) → дефолт
    устройства, а не «сдаться на первом же повторе».

### 3.3 Ядро (`option/`, `transport/wireguard/`)

- `option.AWGRange` (string: `""` | `"N"` | `"N-M"`, uint32, N ≤ M; JSON number или
  string; single → number при marshal) — общий тип для всех «число-или-диапазон»
  полей; `option.MagicHeader = AWGRange` (алиас, старые вызовы компилируются).
- `AmneziaWGOptions`: `header_protection_key` (base64), `content_padding_addition`,
  `rekey_after_time`, `rekey_timeout`, `reject_after_time`, `keepalive_timeout`,
  `max_handshake_attempts` (AWGRange), `random_trailers`, `disable_cookies` (bool).
  `IsSet()` учитывает их.
- `option.WireGuardPeer.PersistentKeepaliveInterval`: `uint16 → AWGRange` (lx-маркер в
  upstream-структуре; JSON-число как раньше). Прокинуто через
  `transport/wireguard.PeerOptions` → `peerConfig.keepalive string` → IPC.
- `device_awg.go`: эмиссия ключей; ключ base64 → hex (32 байта, не нули); `s1–s4 ≥ 12`
  при ключе — ошибка `sing-box check` с именем поля; `awgKeepaliveSpec`.
  `device_stub_awg.go`: без `with_awg` диапазонный keepalive отвергается как
  AWG-поле, число проходит.
- `lx-test/config/awg3_full.json` + строки в `lx-ci.yml` (позитивная и негативная
  проверки).
- Доки: `docs-lx/lx-config{,.ru}.md` (пример, счёт полей 30), `lx-protocols-transports{,.ru}.md`
  (§2.1, §2.7, §2.8, §2.9, новый §2.10), FEATURE 003, баннер в SPEC 031.

## 4. Критерии приёмки — выполнено

**Юнит/e2e (сабмодуль, `go test ./device ./conn`, плюс `-race` по AWG-тестам):**

| Тест | Что пинит |
|------|-----------|
| `TestAWG3EndToEnd` | пара устройств с полным набором live-экспорта: TUN-путь A→B/B→A, инжекция; wire-tap: тип init/transport замаскирован на проводе и снимается ChaCha20(key, dg[:12]); data-датаграмма выросла на 10..100 |
| `TestAWG3HeaderKeyMismatch` | разные ключи → нет хендшейка, ничего не доставлено (защита реальна) |
| `TestAWG3UapiRejectsShortPaddingWithHeaderKey` | `s4=8`+ключ → ошибка; частичный `s2=4` при стоящем ключе → ошибка, значение не изменилось; нулевой ключ снимает ограничение |
| `TestUintRangeFromString`, `TestMagicHeaderGenerateFullRange` | парсинг, полный диапазон не константа |
| `TestAWG3PersistentKeepaliveRange`, `TestIpcGetReportsAWGParams`, `TestIpcGetPlainWireGuardOmitsAWGKeys` | IPC round-trip всех 9 ключей + диапазонный keepalive; plain WG без AWG-ключей |
| прежние AWG-тесты (SPEC 025/026/041, `transport_padding_test`) | зелёные без изменений семантики |

**Ядро** (`go test -tags with_awg,… ./option/... ./transport/wireguard/...` и без тега):
`TestAWG3OptionsUnmarshal`, `TestPeerKeepaliveNumberCompat`, `TestAWG3RangeMarshalFidelity`,
`TestAWG3FieldsCountAsSet`, `TestAwgIpcLinesAWG3*`, `TestAwgIpcLinesHeaderKey*`,
`TestAwgKeepaliveSpec`, `TestAwgKeepaliveSpecStub`.

**Live, 2026-09-05, сервер 77.239.123.44:30565 (`amnezia-awg2`, `protocol_version 3.1`),
бинарь `make -f Makefile.lx lx-build`, конфиг = экспорт 1:1, `mtu 1376`:**

| Проверка | Результат |
|----------|-----------|
| `sing-box check` (live-конфиг, `awg3_full.json`, `awg2_*.json`) | OK |
| хендшейк | `sending handshake initiation` → `received handshake response` в ту же секунду, с первой попытки |
| `curl -x socks5h://… https://api.ipify.org` | `77.239.123.44` (IP сервера) |
| `http://example.com`, `https://example.com`, `https://1.1.1.1/cdn-cgi/trace` | 200 |
| `https://speed.cloudflare.com/__down?bytes=1000000` | 200, 1 000 000 B, ~217 KB/s — full-MTU пакеты с `s4`+паддингом проходят |
| rekey | повторный хендшейк через 107 с (`rekey_after_time 100-120`; WG-константа дала бы 120), ответ сразу |
| `www.google.com`, `www.gstatic.com` | таймаут через этот сервер при рабочих остальных HTTPS и 1 МБ-загрузке — недоступность на стороне сервера/хостинга, не ядра |

## 5. Вне скоупа

- **Серверная сторона** (`AdvancedSecurity`, ответ cookie reply как сервер) —
  форк client-focused ([[fork-is-client-focused]]); `SendHandshakeCookie` портирован
  для симметрии, но клиенту не нужен.
- **Импорт `vpn://` в лаунчере/LxBox** — новые ключи надо маппить при разборе
  экспорта (см. §8). Задача клиентской стороны.
- **Релиз** — тег не резался; для пререлиза нужна changelog-секция, для stable —
  билингвальный файл в `docs-lx/releases/` ([[lx-changelog-before-release-tag]]).
- **AAR/девайс-прогон** — не делался; протокольная часть в чистом Go, платформенных
  правок нет.

## 6. Риски / заметки

- **`random_trailers` × широкие диапазоны `h1–h4`** — приёмник пробует любую датаграмму
  длиннее `S_k + size_k` как handshake по слову типа; при широком диапазоне доля
  ложных совпадений на data-пакетах = ширина/2³² (пакет затем не проходит MAC и
  дропается). Свойство референса, сохранено 1:1; в доках предупреждение. AWG3-экспорты
  держат `h1–h4 = 1..4`.
- **Аллокация `chacha20.Cipher` на каждую датаграмму** в обе стороны при включённом
  ключе — как в референсе; кандидат на оптимизацию, если всплывёт в профиле.
- **Тайминги не валидируются на смысл** (`rekey_after_time` > `reject_after_time`
  примет и будет дёргать туннель) — копировать экспорт сервера.
- **`MessageEncapsulatingTransportSize = 0`** (lx) — раскладка in-place опирается на
  это; при ре-графте на новую sagernet-базу проверить, что константа по-прежнему 0.

## 7. Открытые вопросы

Нет. Формат `vpn://` и маппинг ключей для лаунчера — §8.

## 8. Источники и формат экспорта

- amneziawg-go: `git diff v0.2.19 HEAD` (HEAD `b5928ef`, 2026-08-28): коммиты
  `457d920` (3.0), `1f50ad7` (3.1: trailers/cookies), фиксы `08d68cd` (keepalive с
  паддингом = `packet[0]==0`), `1b86b2a` (cap/len у cookie), `da11c9f` (udpWindow в
  `randomPaddingAddition`), `b5928ef` (under-load при `DisableCookies`).
- amneziawg-tools: `src/config.c` (`key_match("HeaderProtectionKey")` …
  `("DisableCookies")`), `src/ipc-uapi.h` (`header_protection_key=%s` hex,
  `random_trailers=%u`), `src/uapi/linux/linux/wireguard.h` (netlink).
- Экспорт Amnezia `vpn://`: `base64url(qCompress(json))`, где `qCompress` = 4 байта
  big-endian длины + zlib; в JSON `containers[].awg.last_config` — строка с JSON, чей
  ключ `config` — текст `.conf` (`[Interface]` со всеми ключами выше, `[Peer]` с
  `PersistentKeepalive = 25-35`); `protocol_version: "3.1"`; `mtu` там же (`1376`).
  Маппинг в JSON ядра — `docs-lx/lx-protocols-transports.md` §2.7.
