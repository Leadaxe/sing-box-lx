# SPEC: 118 — UTLS_CHROME_155

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — отпечаток Chrome 155 по явному имени `fp=chrome_155`; `chrome` (и пустой `fingerprint`, и алиасы `chrome_*`) остаётся `HelloChrome_133` |
| Статус | I (implemented) — 2026-10-08: в форке [Leadaxe/utls-lx](https://github.com/Leadaxe/utls-lx) четвёртый `cherry-pick -x` из refraction (`6ebdceb`) и три наших коммита, локально; в ядре имя `chrome_155` и страж в `common/tls`; `make -f Makefile.lx lx-build` и тесты зелёные. Форк не запушен, гитлинк в ядре не закоммичен. Полевая проверка (REALITY на Xray ≥ v26.9.8 со стенда 086 и обычный TLS через Cloudflare) не прогонялась. Открыт пункт по приложениям — см. «Открытые вопросы» |
| Ветка | `lx` |
| Связанные | [086](../086-UTLS_FORK_FIREFOX148/SPEC.md) (форк-сабмодуль и метод), [087](../087-UTLS_SAFARI_26_3/SPEC.md) (третий cherry-pick), [083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md) (контракт `AuthKey`, гибрид перед X25519), [089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md) (сети, теряющие длинный ClientHello) |

**Touches:** `submodules/utls` (гитлинк → седьмой коммит форка), `common/tls/utls_client.go` (`// lx:`-ветка `case "chrome_155"` в `uTLSClientHelloID`), `common/tls/utls_firefox148_lx_test.go` (стражи `chrome` = 133, `chrome_155` = 155, гибрид у `chrome_155`), `go.mod` (комментарий блока `lx:begin utls-firefox148`; `replace` не менялся), `docs-lx/lx-release-runbook.{ru.,}md` §1.1, `docs-lx/protocols-transports.{ru.,}md` §5.2, `docs-lx/xray-protocols-explained.{ru.,}md`, `SPECS/README.md`, `SPECS/FEATURES/017-REALITY/FEATURE.md`.

## Why

`HelloChrome_Auto` в `metacubex/utls` v1.8.7 и в форке после 087 = `HelloChrome_133` — Chrome начала 2025 года. Текущий стабильный Chrome шлёт другой ClientHello: ML-DSA в `signature_algorithms` с отдельным GREASE и расширение `trust_anchors` (`0xca34`, draft-ietf-tls-trust-anchor-ids) со списком ID Chrome Root Store. По этим признакам отпечаток 133 отличим от живого браузера.

refraction-networking/utls 2026-10-06 добавил `HelloChrome_155` (коммит `6ebdceb`): ClientHello Chrome 155.0.8059.40 (macOS arm64) снят с живого браузера на чистом профиле, байты лежат в `testdata/chrome155_clienthello.hex`, тест сравнивает с ними сгенерированный пресет. Форк-сабмодуль и метод переноса уже есть (086/087).

## Решение владельца 2026-10-08: по умолчанию остаётся 133

refraction переключает на 155 `HelloChrome_Auto`. Мы так не делаем: `chrome` остаётся Chrome 133, Chrome 155 доступен только по имени `chrome_155`. Причины — два свойства пресета 155, которых нет у 133:

1. **ML-DSA в `signature_algorithms`, но без проверки.** Форк объявляет `MLDSA44/65/87`, а проверить такую подпись в CertificateVerify не может (нужен `crypto/mldsa`, Go 1.27). Сервер, который выберет ML-DSA, получит обрыв рукопожатия.
2. **`trust_anchors` меняет выбор цепочки на сервере.** Сервер с поддержкой расширения подбирает цепочку под перечисленные ID Chrome Root Store. Клиент же проверяет сертификат своими корнями (системными или `Config.RootCAs`), и если в них нет корня, под который сервер подобрал цепочку, проверка упадёт.

Обоих рисков нет у тех, кто `chrome_155` не выставил: дефолт не меняется ни для одного существующего конфига.

## Что переносим

| Коммит | Что делает |
|---|---|
| `6ebdceb` Add Chrome 155 fingerprint verified against a fresh browser capture (2026-10-06, Mingye Chen) | пресет `HelloChrome_155` в `u_parrots.go` (в refraction ещё и `HelloChrome_Auto = HelloChrome_155` — у нас откачено); `u_chrome155.go` — 28 ID Chrome Root Store; `u_trust_anchors.go` — `TrustAnchorsExtension` (+ JSON, Fingerprinter); `dicttls` — `mldsa44/65/87` и `trust_anchors`; GREASE: `ssl_grease_seed_count`, отдельный слот BoringSSL под GREASE в `signature_algorithms`; `testdata/chrome155_clienthello.{hex,md}` |

Чем `chrome_155` отличается от `chrome` на проводе:

- `signature_algorithms`: впереди GREASE (слот 7 сида BoringSSL), затем `MLDSA44`/`MLDSA65`/`MLDSA87` (`0x0904…0x0906`), затем прежний набор.
- `trust_anchors` (`0xca34`): 28 ID Chrome Root Store в лексикографическом порядке — снимок этой сборки браузера.
- key_share и supported_groups: `X25519MLKEM768` перед `X25519`, как у 133 — условие приёма REALITY на Xray ≥ v26.9.8 ([083](../083-REALITY_MLKEM_KEYSHARE/SPEC.md)) выполнено.

## Наши три коммита в форке

`6ebdceb` применяется без конфликтов, но не собирается на базе форка, а его `HelloChrome_Auto` противоречит решению выше.

- **Перед cherry-pick: кодпоинты ML-DSA.** Константы `MLDSA44/65/87` в refraction пришли синком Go 1.27.1 (`f08c33d` «crypto/tls: add ML-DSA support»). Этот синк взять нельзя: форк стоит на metacubex (go 1.20 в `go.mod`, свой `internal/mlkem`), тулчейн ядра go1.26.8, `crypto/mldsa` в нём нет. Коммит переносит только кодпоинты в `common.go` и записи стрингера в `common_string.go` — один в один с refraction. Пресету этого достаточно: он только объявляет схемы.
- **После: тесты.** `TestChrome155MLDSAHandshake` импортирует `crypto/mldsa` и сертификаты-фикстуры из синка Go 1.27.1; `TestChrome155PresetOwnership` опирается на `ClientHelloSpec.clone` и клонирующий `ApplyPreset` из refraction `98f1bbc`, которого в форке нет; два новых тестовых файла импортируют путь модуля `github.com/refraction-networking/utls`. Коммит убирает два теста (с `// lx:`-пояснением в начале файла) и меняет путь импорта на `github.com/metacubex/utls`. Сверка с живым захватом (`TestChrome155CapturedClientHello`) и остальные тесты остаются. `TestChrome155PresetOwnership` ядру не нужен: `UClient(…, id)` строит спек заново на каждое соединение, `chrome155TrustAnchorIDs()` возвращает свежие срезы.
- **После: `HelloChrome_Auto` = 133.** `u_common.go` возвращает `HelloChrome_Auto = HelloChrome_133`, абзац README, добавленный cherry-pick'ом, говорит, что 155 включается явно; проверка `HelloChrome_Auto == HelloChrome_155` в `TestChrome155CapturedClientHello` перенацелена на сам `HelloChrome_155`.

## Требования

- **R1. Перенос.** В ветку `lx` форка поверх `59e89bb`: кодпоинты ML-DSA, `cherry-pick -x 6ebdceb`, тесты, `HelloChrome_Auto` = 133. Конфликты — в этот SPEC. Верификацию ML-DSA на стороне рукопожатия не переносить.
- **R2. Маппинг.** В `uTLSClientHelloID` (`common/tls/utls_client.go`) — `// lx:`-ветка `case "chrome_155": return utls.HelloChrome_155, nil`. `"chrome"`, `""` и `chrome_*` по-прежнему → `HelloChrome_Auto` = 133.
- **R3. Страж.** `common/tls/utls_firefox148_lx_test.go`: `"chrome"` и `""` = `HelloChrome_Auto` = `HelloChrome_133`; `"chrome_155"` = `HelloChrome_155`; `chrome_155` в проверке «гибрид перед X25519 по разу».
- **R4. CI.** Правок workflow нет (сабмодуль тот же, `submodules: recursive`).

## Критерии приёмки

1. Конфликты cherry-pick (или их отсутствие) записаны в SPEC. ✅
2. Форк: `go build ./...`, `go vet .`, `go test .` зелёные под go1.26.8; сгенерированный `HelloChrome_155` совпадает с захватом Chrome 155 (`TestChrome155CapturedClientHello`). ✅
3. Ядро: `make -f Makefile.lx lx-build` полным `LX_TAGS`, `go test -tags with_utls -ldflags=-checklinkname=0 ./common/tls/` зелёные; `sing-box check` принимает `fingerprint: "chrome_155"`. ✅
4. REALITY `fp=chrome_155` против Xray ≥ v26.9.8 и < v26.9.8 → 204 (стенд 086/087). ⏳ не прогонялось
5. Обычный TLS `fp=chrome_155` через Cloudflare → рукопожатие проходит. ⏳ не прогонялось
6. `chrome`, `firefox`, `safari` без изменений на проводе. ✅ страж

## Риски (только для `chrome_155`)

- **Сервер, выбравший ML-DSA для CertificateVerify, получит отказ** — прежней ошибкой неподдерживаемой подписи. Публичных ML-DSA-сертификатов сейчас нет; REALITY-сервер Xray подписывает Ed25519 — не затронут.
- **Цепочка под Root Store Chrome.** Неизвестное расширение сервер обязан игнорировать (RFC 8446 §4.2), но сервер, который `trust_anchors` знает, может отдать цепочку под корень из списка Chrome, которого нет в корнях клиента. REALITY не затронут: клиент проверяет подпись REALITY, а не цепочку сайта-прикрытия.
- **ClientHello длиннее на ~200 байт.** Замер в форке на `www.example.com` (50 сборок, разброс от длины GREASE ECH): Chrome 133 — 1720…1816 байт, Chrome 155 — 1918…2014; 190 байт даёт `trust_anchors`, 8 — четыре новые подписи. Для сетей, теряющих длинный первый пакет ([089](../089-REALITY_KEY_SHARE_OPTION/SPEC.md), LxBox #142), это дополнительный вес; ручки прежние — `key_share: classical`, фрагментация ([088](../088-REALITY_FRAGMENT_BYPASS/SPEC.md)).
- **Список ID — снимок.** Root Store Chrome обновляется компонентом без смены версии браузера; пресет со временем разойдётся с живым Chrome по этому списку.

## Открытые вопросы

- **Приложения.** Имени `chrome_155` нет в наборе отпечатков LxBox (`kRealityHybridFingerprints`, §281) и в контракте лаунчера (D-119). Пока его туда не добавят, приложения не могут предложить `chrome_155`, а на узле из подписки с этим именем LxBox покажет предупреждение о негибридном отпечатке, хотя гибрид у пресета есть. **Решение владельца 2026-10-08: пока не делаем** — имя остаётся только для ручных конфигов ядра.
- **JSON-схема.** `chrome_155` добавлен в тег `enum` поля `Fingerprint` в `option/tls.go` (апстримный файл, одна строка под `// lx:`), `sing-box schema` выдаёт имя в перечислении. `docs/schema.json` не перегенерирован: это апстримный артефакт, его обновляет апстрим своим набором тегов.

## Реализация — 2026-10-08

### Форк `Leadaxe/utls-lx`

Ветка `lx` = тег `v1.8.7` + семь коммитов:

| Коммит форка | Источник | Что |
|---|---|---|
| `9fd088e` | refraction `fc716b2` | Firefox 148 + reuse ключа (086) |
| `6b7f051` | refraction `ddebe39` | reuse через байт-маркеры (086) |
| `59e89bb` | refraction `aa6edf4` | Safari 26.3 (087) |
| `7ff1e9e` | наш | `crypto/tls: add ML-DSA signature scheme codepoints` |
| `c3e6746` | refraction `6ebdceb` | Chrome 155 |
| `e21488d` | наш | `test: drop ML-DSA handshake test (no crypto/mldsa before Go 1.27)` |
| `47b8493` | наш | `keep HelloChrome_Auto at Chrome 133` |

### Критерий 1 — конфликты cherry-pick

**Конфликтов не было** (автослияние `README.md`, `u_common.go`, `u_conn.go`, `u_parrots.go`, `u_tls_extensions.go`). Без коммита с кодпоинтами не собирается библиотека (`undefined: MLDSA44/MLDSA65/MLDSA87` в `u_parrots.go`), без коммита с тестами — тестовый бинарь (`crypto/mldsa is not in std`, `spec.clone undefined`, путь модуля refraction).

### Критерий 2 — проверка форка (go1.26.8)

- `go build ./...`, `go vet .` — ✅; `gofmt -l` по затронутым файлам пуст.
- `go test -run 'Chrome155|MLDSA|TrustAnchor|GREASE|Grease' .` — ✅, среди них `TestChrome155CapturedClientHello` (сверка с захватом: игнорируются только перестановка расширений, значения GREASE, эфемерные ключи и случайное содержимое GREASE ECH), `TestChrome155DefaultFingerprintRoundTrip`, `TestChrome155SignatureGREASESeed`, `TestMLDSASignatureSchemeJSON`, `TestTrustAnchorsExtension*`, `TestBoringGREASEPublicAPI`; `TestChrome133CapabilitiesPreserved` — ✅.
- `go test .` целиком — ✅.

### Ядро

- `common/tls/utls_client.go`: одна `// lx: SPEC 118`-ветка `case "chrome_155"`.
- Стражи (`with_utls`): `TestLxChromeFingerprintStaysChrome133`, `TestLxChrome155FingerprintIsOptIn`; `TestLxRealityFingerprintsCarryHybridShareFirst` — `chrome`, `chrome_155`, `firefox`, `safari`.
- `go test -ldflags=-checklinkname=0 -tags with_utls ./common/tls/` — ✅.
- `make -f Makefile.lx lx-build` — ✅ (`sing-box version 1.14.2-lx.1`, локальная сборка без `LX_VERSION`, go1.26.8 darwin/arm64); `sing-box check` на VLESS+REALITY с `fingerprint: "chrome_155"` — ✅, с `chrome_999` — `unknown uTLS fingerprint: chrome_999`.
- linkname `badtls`/`ktls` не затронуты: экспортируемые символы, на которых они стоят, коммиты не меняют.

## Состояние у соседей (2026-10-08)

- **metacubex/utls:** ветка `v1.9.0-mod-meta` (без тега, 2026-09-30) уже несёт Firefox 148 и Safari 26.3, но `HelloChrome_Auto = HelloChrome_133` и ML-DSA нет.
- **sing-box апстрим:** и `stable`, и `testing` держат `metacubex/utls v1.8.7`.

## Цена сопровождения

- В `submodules/utls` ожидается **семь** коммитов поверх тега `v1.8.7` — сверка в [раннбуке §1.1](../../../docs-lx/lx-release-runbook.md).
- Бамп `metacubex/utls` в апстриме → ветка `lx` форка переезжает на новый тег со всеми семью коммитами. Если в новой базе появятся константы ML-DSA, коммит с кодпоинтами отпадает; если появится `crypto/mldsa`-верификация — и коммит с тестами. Если новая база сама переключит `HelloChrome_Auto`, коммит «Auto = 133» остаётся и решение пересматривается владельцем.

## Условие снятия

metacubex выпустит тег с `HelloChrome_155` (вместе с Firefox 148 + reuse и Safari 26.3, см. [087](../087-UTLS_SAFARI_26_3/SPEC.md#условие-снятия)), либо апстрим sing-box переедет на библиотеку, где они есть → убрать `replace` и сабмодуль; ветка `case "chrome_155"` останется рабочей, пока в библиотеке есть `HelloChrome_155`. Снятие форка до этого потребует убрать и эту ветку, и конфиги с `chrome_155` перестанут загружаться (`unknown uTLS fingerprint`).
