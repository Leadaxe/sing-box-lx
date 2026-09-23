# SPEC: 087 — UTLS_SAFARI_26_3

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · хотфикс в [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — остаток границы [086](../086-UTLS_FORK_FIREFOX148/SPEC.md): REALITY с `fp=safari` не проходит на Xray ≥ v26.9.8, потому что `HelloSafari_Auto` в `metacubex/utls` v1.8.7 = `HelloSafari_16_0` без гибридного key share |
| Статус | C (complete) — критерии закрыты стендом на Mac 2026-09-16 (Xray v26.9.9: `fp=safari` 204 ×3, ядро lx.2 на том же стенде — `reality verification failed` ×3; v26.7.28/v26.7.11 и `chrome`/`firefox` без регрессии). Реализовано 2026-09-16: третий `cherry-pick -x` в форк [Leadaxe/utls-lx](https://github.com/Leadaxe/utls-lx) (`aa6edf4` «feat: add safari 26.3»), страж в `common/tls` расширен. Выпущено в `v1.14.1-lx.3`. LxBox v2.24.1 на пине lx.3 снял предупреждение и для `safari` (подробности — в 086). Остаток — как в 086: прогон AAR на AVD/устройстве — за владельцем |
| Ветка | `lx` |
| Связанные | предшествующая [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) (форк-сабмодуль и метод), [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (контракт `AuthKey`); issue [#22](https://github.com/Leadaxe/sing-box-lx/issues/22) (закрыт); полевой отчёт [singbox-launcher#124](https://github.com/Leadaxe/singbox-launcher/issues/124) |

**Touches:** `submodules/utls` (гитлинк → третий коммит форка), `go.mod` (комментарий блока `lx:begin utls-firefox148` — упомянут третий коммит; `replace` не менялся), `common/tls/utls_firefox148_lx_test.go` (страж расширен), `common/tls/utls_client.go` (маппинг `"safari"` не менялся — проверен тестом), реестр HOTFIXES, `SPECS/README.md`, `README.md`/`README.ru.md`, `docs-lx/lx-release-runbook.{ru.,}md` §1.1/§3, `SPECS/CONSTITUTION.md`, `docs-lx/lx-changelog.md`.

## Why

[086](../086-UTLS_FORK_FIREFOX148/SPEC.md) закрыла `fp=firefox`, но её граница («только Firefox 148») оставила в поле те же симптомы у `fp=safari`: сервер `XTLS/REALITY@8cdf7bf` требует key share `X25519MLKEM768` перед `X25519`, а в `metacubex/utls` v1.8.7 `HelloSafari_Auto = HelloSafari_16_0` — гибрида нет. Клиент получает `reality verification failed`, соединение молча уходит на камуфляжный сайт. Отпечаток приходит из подписки, пользователь его не меняет.

Приём уже отработан 086: форк-сабмодуль есть, метод переноса и сверки есть, цена — один cherry-pick.

## Что переносим — один коммит `refraction-networking/utls`

| Коммит | Что делает |
|---|---|
| `aa6edf4` feat: add safari 26.3 (2026-02-28, Mingye Chen) | `u_common.go`: `HelloSafari_Auto = HelloSafari_26_3`, новый `HelloSafari_26_3 = ClientHelloID{helloSafari, "26.3", nil, nil}`; `u_parrots.go`: +110 строк — сам пресет |

В ленте refraction `aa6edf4` идёт **сразу за** `ddebe39` (вторым коммитом 086) — промежуточных коммитов нет, переносить ленту целиком = перенести один этот коммит. Reuse ключа (механика `fc716b2`/`ddebe39`) пресет Safari 26.3 не использует: гибридная и классическая записи key share несут разные ключи, как у chrome.

## Требования

- **R1. Перенос.** `cherry-pick -x aa6edf4` в ветку `lx` форка поверх `6b7f051`. Конфликты и их разрешение — в этот SPEC. Собственных правок библиотеки по-прежнему не заводить.
- **R2. Маппинг.** Наш `"safari"` смотрит в `HelloSafari_Auto` (`common/tls/utls_client.go`). После переноса проверить, что алиас переключился на 26.3, иначе — явно.
- **R3. Страж.** Расширить `common/tls/utls_firefox148_lx_test.go`: `"safari"` = Safari 26.3, и `safari` — в проверке «гибрид перед X25519 по разу». Тест должен падать, если `replace` съедет на голый metacubex.
- **R4. CI.** Правок workflow нет (как в 086: все `lx-*.yml` чекаутят `submodules: recursive`).

## Критерии приёмки

1. Конфликты cherry-pick (или их отсутствие) записаны в SPEC. ✅ (Реализация → форк)
2. `make -f Makefile.lx lx-build` полным `LX_TAGS`; `go test ./...` с `-ldflags "-checklinkname=0"`; оба AAR зелёные. ✅ сборка и тесты локально; кросс-сборка android/arm64 и AAR — CI `lx-release` (linkname не затронуты: пресет декларативный, как в 086)
3. ClientHello `fp=safari`: `X25519MLKEM768` стоит перед `X25519`, по одному разу в key_share и в supported_groups. Структура приветствия совпадает с тем же пресетом из refraction. ✅ (Реализация → сравнение с refraction + страж)
4. REALITY `fp=safari` против Xray ≥ v26.9.8 → 204; против Xray < v26.9.8 → 204. ✅ стенд 2026-09-16
5. `fp=chrome` и `fp=firefox` без регрессии ([083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md), [086](../086-UTLS_FORK_FIREFOX148/SPEC.md)). ✅ тот же стенд

## Границы

- Только `safari`. **Решение владельца 2026-09-16:** `edge`, `ios`, `android`, `360`, `qq` остаются как есть — подмену отпечатка делают приложения (LxBox §281, лаунчер), ядро их не трогает. Основание: пресетов с гибридным шаром для этих имён нет **ни у metacubex, ни у refraction** — новейшие там Edge 106, iOS 14, 360 11.0, QQ 11.1, Android 11 OkHttp, все без гибрида. Xray-core сидит на тех же пресетах refraction, поэтому по этим четырём именам мы с Xray в равном положении: у него они тоже не проходят. Гибридный шар в refraction есть только у Chrome 131/133 (+115_PQ_PSK), Firefox 148 и Safari 26.3 — все три у нас закрыты (083, 086, 087).
- В форке — только три коммита refraction, собственных lx-правок библиотеки нет.
- Серверная сторона REALITY (`RealityServer` в форке) не трогается.
- Ядро по-прежнему не подменяет отпечаток ([083 §5](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#5-границы)).

## Реализация — 2026-09-16

### Форк `Leadaxe/utls-lx`

- Третий `cherry-pick -x` поверх `6b7f051`: `aa6edf4b11af82e110eea845bb2983d30138d651` → `59e89bb121d8cc7a0a1c87a930864122b5685d00`.
- Ветка `lx` теперь = тег `v1.8.7` + ровно три коммита: `9fd088e` (`fc716b2`), `6b7f051` (`ddebe39`), `59e89bb` (`aa6edf4`).
- Проверка форка: `go build ./...`, `go vet .`, `go test ./...` — зелёные под go1.26.8.
- В ядре: гитлинк `submodules/utls` → `59e89bb`; `replace` и `.gitmodules` не менялись, комментарий блока `lx:begin utls-firefox148` в `go.mod` дополнен третьим коммитом.

### Критерий 1 — конфликты cherry-pick

**Конфликтов не было.** `aa6edf4` трогает `u_common.go` (алиас + `ClientHelloID`) и `u_parrots.go` (тело пресета) — в обоих случаях добавление, а не правка общих регионов; ожидавшаяся зона 086 (генерация key share в `ApplyPreset`) этим коммитом не затрагивается, потому что пресет Safari 26.3 механику reuse не использует.

### Критерий 3 — пресет и сравнение с refraction

Метод — тот же, что в [086](../086-UTLS_FORK_FIREFOX148/SPEC.md#критерий-3--clienthello-firefox-148-против-refraction): scratch-модуль вне ядра (в `go.mod` ядра refraction не добавлялся) с двумя импортами — `github.com/metacubex/utls` (→ форк через `replace`) и `github.com/refraction-networking/utls`; у обоих `UClient(…, HelloSafari_26_3)` + `BuildHandshakeState()` с детерминированным `Config.Rand`, затем разбор **сырого** ClientHello по байтам.

Результат: `HelloSafari_Auto` = 26.3 у обоих (R2 закрыт — `"safari"` в `utls_client.go` менять не пришлось); 15 расширений одного типа в одном порядке; структурная форма идентична. Заодно повторно подтверждено, что Firefox 148 не изменился.

| Поле | Значение |
|---|---|
| cipher_suites | GREASE, `1302 1303 1301 c02c c02b cca9 c030 c02f cca8 c00a c009 c014 c013 009d 009c 0035 002f c008 c012 000a` |
| расширения по порядку | GREASE, sni, extended_master_secret, renegotiation_info, supported_groups, ec_point_formats, alpn (`h2`, `http/1.1`), status_request, signature_algorithms, sct, key_share, psk_key_exchange_modes, supported_versions (GREASE, `1.3`, `1.2`), compress_certificate (zlib), GREASE |
| supported_groups | GREASE, `X25519MLKEM768`, `X25519`, P-256, P-384, P-521 |
| key_share | GREASE: 1 байт, `X25519MLKEM768`: 1216, `X25519`: 32 — гибрид **перед** X25519, каждый по разу |

ECH GREASE в этом пресете нет (в отличие от Firefox 148) — различий по сырым байтам между форком и refraction не осталось вовсе.

### Страж в ядре — `common/tls/utls_firefox148_lx_test.go` (`with_utls`)

- Новый `TestLxSafariFingerprintIsSafari26_3` — `"safari"` → `HelloSafari_Auto` == `HelloSafari_26_3`, версия `"26.3"` (на голом metacubex упадёт: там `Auto = 16_0`).
- `TestLxRealityFingerprintsCarryHybridShareFirst` — в список добавлен `safari`: `X25519MLKEM768` перед `X25519` и ровно по одному разу в key_share и в supported_groups, длины записей 1216/32.
- Все 5 тестов файла зелёные (`go test -tags with_utls -ldflags "-checklinkname=0" ./common/tls/`).

### Критерий 2 — сборка и тесты ядра

- `make -f Makefile.lx lx-build` полным `LX_TAGS` — ✅.
- `go test ./...` под `LX_TAGS` с `-ldflags "-checklinkname=0"` — ✅.
- Кросс-сборка android/arm64 и оба AAR — прогон CI `lx-release` на теге. linkname `badtls`/`ktls` этим коммитом не затронуты: как и в 086, перенос декларативный (пресет + алиас), экспортов библиотека не меняет.

### Критерии 4 и 5 — стенд (Mac, 2026-09-16)

Как в [083 §2](../083-REALITY_MLKEM_KEYSHARE/SPEC.md#2-доказательство) и [086](../086-UTLS_FORK_FIREFOX148/SPEC.md#критерии-4-и-5--стенд-mac-2026-09-16): Xray-сервер на loopback (`127.0.0.1:14443`, VLESS + REALITY, `xtls-rprx-vision`), `dest`/`serverNames` = `www.apple.com`, `mixed`-inbound, `curl` ×3 на `http://www.gstatic.com/generate_204` на отпечаток. Ядро «до» = `v1.14.1-lx.2` (форк на `6b7f051`, без Safari 26.3).

| Ядро | Xray | chrome | firefox | safari |
|---|---|---|---|---|
| **lx.3** (utls-lx `59e89bb`) | **v26.9.9** | 204 ×3 | 204 ×3 | **204 ×3** |
| lx.2 (utls-lx `6b7f051`) | v26.9.9 | — | — | ✗ ×3 `reality verification failed` |
| lx.3 | v26.7.28 | 204 ×3 | 204 ×3 | 204 ×3 |
| lx.3 | v26.7.11 | — | — | 204 ×3 |

- Лог Xray v26.9.9 «после» для `safari`, каждое соединение: `hs.c.ClientVer: [26 3 27]`, `hs.c.conn == conn: true` — сервер посчитал `AuthKey` по тому же ключу, что и мы (контракт 083 держится, критерий 4 ✅).
- «До»: `forwarded SNI: www.apple.com`, затем `processed invalid connection … authentication failed or validation criteria not met` — та же отсечка, что в 083 §2 и 086.
- `chrome` и `firefox` — 204 на всех проверенных версиях Xray, регрессии нет (критерий 5 ✅).

### CI

Правок workflow нет (R4) — все `actions/checkout` в `lx-*.yml` уже с `submodules: recursive`, сверено в 086; сабмодуль тот же, новых гитлинков не появилось.

## Цена сопровождения

- В `submodules/utls` теперь ожидается **три** коммита поверх тега `v1.8.7` (`fc716b2`, `ddebe39`, `aa6edf4`), а не два — сверка в [раннбуке §1.1](../../../docs-lx/lx-release-runbook.ru.md).
- Бамп `metacubex/utls` в апстриме sing-box → ветка `lx` форка переезжает на новый тег с сохранением всех трёх коммитов.
- metacubex внешние PR не принимает — синк только своими силами.

## Условие снятия

metacubex выпустит тег с Firefox 148 + reuse **и** Safari 26.3, либо апстрим sing-box переедет на библиотеку, где они есть → убрать `replace` и сабмодуль; `"safari"` и `"firefox"` уже смотрят в соответствующие `*_Auto`.

⚠️ Связь с приложениями — [086, «Условие снятия»](../086-UTLS_FORK_FIREFOX148/SPEC.md#условие-снятия): набор без предупреждения в LxBox v2.24.1 включает `safari`, поэтому потеря гибрида у этого имени в ядре требует сузить набор в LxBox и контракте лаунчера в той же поставке.
