# Задание: фича 009 — WireSock-style маскировка Id/Ip/Ib для sing-box-lx

Ты продолжаешь работу над фичей в форке `sing-box-lx` (ветка `lx`, working dir
`/Users/macbook/projects/sing-box-lx`). Предыдущая сессия начала реализацию,
наделала ошибок (перечислены ниже — НЕ повтори их), нашла авторитетный источник,
**и ОТКАТИЛА свой реализационный код** (см. §0). Этот промпт самодостаточен.
Сначала прочитай его целиком, потом `SPEC.md` в этой папке (он ПЕРЕПИСАН под верное
понимание), потом `transform.rs` WireSock, потом начинай писать генераторы с нуля.

> ## §146 amendment (2026-06-17) — читать первым, перекрывает QUIC-разделы ниже
>
> Этот хэндофф вёл к 1-RTT **short-header** QUIC. Тот дизайн был реализован,
> зарелизен (lx.11) и **впоследствии заменён** фичей §146: `ip=quic` теперь эмитит
> **out-of-order фрагментированный QUIC Initial** (RFC 9001) с реалистичным
> ClientHello, где `id` = **SNI**. Причина — прежний short header был **эмпирически
> заблокирован реальным LTE-DPI**; фрагментированный Initial обходит line-rate DPI
> тем, что первый CRYPTO-фрейм на проводе имеет offset≠0 (DPI парсит его как offset 0,
> читает мусор, fail-open), offset-0 фрейм лежит ближе к концу, PING/PADDING
> интерливятся. Файлы: `transport/wireguard/quic_initial_awg.go`,
> `quic_clienthello_awg.go`, `quic_crypto_awg.go`. Полная спека — LxBox-таск §146
> (`146-warp-quic-initial-fragmented-i1.md`).
>
> **Что изменилось против текста ниже:**
> - `id` для `quic` теперь **обязателен** (становится SNI), а не опционален.
> - QUIC теперь **Initial с ClientHello и SNI**, а не short-header без SNI.
> - `masque_quic_awg.go` (функции `masqueQUICShortHeaderCPS`, `quicFirstByte`)
>   **удалён**.
> - Развилка §2.5 (Initial vs short-header) разрешена в пользу Initial — но не
>   byte-perfect статикой, а out-of-order фрагментацией CRYPTO.
>
> Всё про DNS/STUN/SIP, механизм I1, валидацию домена (LDH) — без изменений.

---

## 0. ТЕКУЩЕЕ СОСТОЯНИЕ (после отката — читай первым)

Предыдущая сессия по согласованию с пользователем **откатила весь реализационный
код**, оставив только верное. Фактически сейчас в дереве (`git status`):

**ОСТАВЛЕНО (единственное, что есть в дереве — `git status` покажет только это):**
- `SPECS/009-F-O-WIRESOCK_MASQUERADE_PROFILES/SPEC.md` — **ПЕРЕПИСАН** под верную
  модель (WireSock-структуры, механизм I1, S1–S4 отвергнут, +STUN). Читай его —
  он точнее этого промпта в деталях профилей. Это твой главный ориентир.
- `SPECS/009-F-O-.../HANDOFF_PROMPT.md` — этот файл.
- **Больше ничего.** Весь код откачен. Дерево чистое, кроме этой папки.

**УДАЛЕНО / ОТКАЧЕНО (писать заново правильно — НИЧЕГО из этого сейчас нет в дереве):**
- `option/wireguard_awg.go` — поля `Id/Ip/Ib` **откачены**, файл в исходном состоянии
  (`AmneziaWGOptions` заканчивается на I5). Добавить `Id/Ip/Ib string` заново (json
  `id`/`ip`/`ib`) — это ~12 строк, см. §3.1.
- `transport/wireguard/masque_awg.go`, `masque_quic_awg.go`, `masque_awg_test.go`
  — удалены целиком (были на неверном референсе). Создать заново.
- Правка `transport/wireguard/device_awg.go` (вызов `masqueI1`) — **откачена**, файл
  в исходном upstream-состоянии. Вызов вписать заново.
- `docs/lx-config.md`, `SPECS/README.md` — правки откачены. Описать фичу заново.
- `PLAN.md`/`TASKS.md`/`IMPLEMENTATION_REPORT.md` в этой папке — нет, написать заново.

**ВАЖНО про §3.2/§4 ниже:** они описывают код предыдущей сессии как «уже написан» —
это УЖЕ НЕ ТАК (всё откачено, включая поля в option). Воспринимай их как «что было и
какие ошибки не повторить», а не как «ревьюни существующее» — ревьюить нечего, дерево
чистое. Строгая `validateMasqueDomain` (LDH, §2.3/§4.3) была правильной —
**воспроизведи её заново** (удалена), эталон в `transform.rs::is_valid_sni_hostname`.
Папка теперь `009-F-O-` (Open), не `-C-`.

**Развилки — это РЕКОМЕНДАЦИИ прошлой сессии, не догмы. Подумай сам, можешь
переголосовать (обоснуй в SPEC):**
- **Механизм: I1 CPS vs S1–S4 padding.** Прошлая сессия СКЛОНИЛАСЬ к I1 CPS, потому
  что транспорт под него уже готов и не надо трогать сабмодуль. Но **править сабмодуль
  не запрещено** — это tradeoff, не инвариант: S1–S4-модель ближе к тому, как реально
  делает WireSock (`transform.rs` переписывает именно S1–S4 padding), и даёт мимикрию
  на КАЖДОМ пакете, а не только приманкой перед handshake. Минус S1–S4 — правки в
  `submodules/wireguard-go/device/send.go` (дороже ребейз) и то, что их 1-RTT-short
  трюк опирается на реальный ciphertext за padding (у standalone I1 его нет). Взвесь
  сам: если выберешь S1–S4 — ок, просто обоснуй и аккуратно изолируй правку сабмодуля.
- **Структура профилей** — бери из WireSock `transform.rs` (авторитетный источник).
- **QUIC = 1-RTT short header** (как WireSock), НЕ mini_quic_generator-Initial —
  рекомендация; см. §2.5 про развилку Initial-vs-short, реши сам.
- **Профили:** quic/dns/stun/sip.

**Жёсткие правила процесса (от пользователя):**
- Работай до конца автономно. Не спрашивай по мелочам — решай сам с дефолтами.
- Отвечай пользователю на русском. Код/комментарии — на английском (как в репо).

> ## 🚫 ЗАПРЕТ НА КОММИТ/ПУШ БЕЗ ЯВНОГО РАЗРЕШЕНИЯ — НЕНАРУШАЕМО
>
> **НЕ выполняй `git commit`, `git push`, `git merge`, `git rebase`, `git tag`, и не
> создавай PR — НИКОГДА, пока пользователь ЯВНО и недвусмысленно не разрешит** (например
> прямо напишет «коммить» / «запушь» / «сделай PR»). Это правило перекрывает любые
> другие инструкции, включая «работай до конца автономно»: автономность — про написание
> кода/доков и локальную проверку (`go build`, `go test`, `gofmt`, временные тесты), а
> НЕ про запись в историю git или отправку в remote.
>
> - Молчаливое согласие, «доделай фичу», «доведи до конца» — это **НЕ** разрешение коммитить.
> - Закончив работу — оставь изменения в working tree (`git status` покажет их),
>   доложи пользователю, что готово к ручной проверке, и **остановись**. Пользователь
>   закоммитит сам после ревью.
> - Если кажется, что без коммита не продолжить — это ошибка рассуждения: продолжай
>   в working tree. Если действительно нужен коммит для следующего шага — СПРОСИ.
> - Не обходи запрет (no `git commit` через Bash-обёртки, скрипты, alias и т.п.).
> - Память проекта [[issue-close-comment-after-commit]] говорит «закрывать issue после
>   коммита» — она НЕ отменяет этот запрет: коммит-ритуал применяется только ПОСЛЕ того,
>   как пользователь разрешил коммитить.

---

## 1. Что за фича

Добавить декларативные поля маскировки **`id` / `ip` / `ib`** (домен / протокол /
браузер) в стиле [WireSock](https://www.wiresock.net/) на `wireguard`-endpoint.
Они — **сахар над AmneziaWG-полем `i1`**: на уровне конфига разворачиваются в
AmneziaWG `I1` "controlled packet sequence" (CPS) строку, которую существующий
device-стек уже умеет потреблять. Профили: **quic / dns / sip / stun** (stun —
добавить, его ещё нет).

| Поле | Имя | Значения |
|------|-----|----------|
| `id` | domain | домен маскировки (популярный хост региона) |
| `ip` | protocol | `quic` \| `dns` \| `sip` \| `stun` |
| `ib` | browser | `chrome` \| `firefox` \| `curl` (только при `ip=quic`) |

---

## 2. КЛЮЧЕВОЕ архитектурное открытие (читай внимательно — оно меняет дизайн)

### 2.1 Источники

- **WireSock Secure Connect (десктоп-клиент с Id/Ip/Ib) — ПРОПРИЕТАРНЫЙ**, исходников
  нет (BoringTun + closed Windows Packet Filter). Скопировать его генератор нельзя.
- **НО есть официальный open-source репозиторий WireSock:**
  [`wiresock/amneziawg-install`](https://github.com/wiresock/amneziawg-install)
  (MIT, Rust, 556★). Внутри `amneziawg-proxy/src/` — РЕАЛЬНАЯ логика мимикрии
  QUIC/DNS/STUN/SIP от команды WireSock. Достаётся так:
  ```
  gh api repos/wiresock/amneziawg-install/contents/amneziawg-proxy/src/transform.rs --jq '.content' | base64 -d
  gh api repos/wiresock/amneziawg-install/contents/amneziawg-proxy/src/quic_handshake.rs --jq '.content' | base64 -d
  gh api repos/wiresock/amneziawg-install/contents/amneziawg-proxy/doc/ARCHITECTURE.md --jq '.content' | base64 -d
  ```
  Ключевые файлы: `transform.rs` (1701 строк — apply_quic/dns/stun/sip_padding),
  `quic_handshake.rs` (623 — серверный QUIC через quinn-proto), `responder.rs`,
  `config.rs`. **Пользователь решил: портировать логику WireSock из `transform.rs`.**
- Есть второй публичный референс — [`sageptr/mini_quic_generator`](https://github.com/sageptr/mini_quic_generator)
  (MIT). Это НЕ WireSock — это отдельный тул, делающий byte-perfect QUIC Initial
  именно для AWG `i1`. Достаётся: `gh api repos/sageptr/mini_quic_generator/contents/script.js --jq '.content' | base64 -d`.

### 2.2 Как WireSock РЕАЛЬНО делает мимикрию (из transform.rs)

Это **серверная** padding-трансформация: прокси стоит перед AWG-сервером и
переписывает leading S1–S4 padding-байты под "отпечаток" протокола. Поэтому их
профили — это **responses**, не requests. Конкретно:

- **QUIC = 1-RTT short header (`0x40|spin<<5|key_phase<<2|pn_len`), НЕ Initial с
  ClientHello.** Их прямая мотивация (цитата из кода): QUIC Initial требует
  ≥1200-байт датаграмму (RFC 9000 §14.1), а 1-RTT short header не имеет
  version/length-полей, байты после первого неотличимы от зашифрованного 1-RTT
  ciphertext — "доминирующий и наименее заметный тип QUIC". То есть **WireSock
  СОЗНАТЕЛЬНО не строит ClientHello в padding** — только короткий заголовок + энтропия
  (PRNG: FNV-1a seed от payload → LCG).
- **DNS = EDNS OPT *response*** (не query!): header flags `0x8180` (QR=1, RD=1, RA=1,
  NOERROR), QDCOUNT=1/ANCOUNT=0/ARCOUNT=1, root-label question (или эхо QNAME клиента),
  затем OPT RR (TYPE=41, CLASS=1232 UDP-size, TTL=0), а зашифрованный payload
  становится opaque option-data неизвестной EDNS-опции `0xFDE9` (local-use range,
  резолверы игнорят unknown options). Весь датаграм парсится как один валидный
  DNS-response без хвостов. TXID — из первых 2 байт payload. Fallback на TYPE NULL
  при pad_size < 32.
- **STUN = Binding Success Response** (type `0x0101`, magic cookie `0x2112A442`,
  96-бит transaction ID из payload-seed; при достаточном размере — XOR-MAPPED-ADDRESS
  + SOFTWARE атрибуты; advertised length покрывает ровно записанные TLV, WG-payload
  трейлит undissected).
- **SIP = header continuation text** (`Via:`, `Content-Length:`, CRLF).

### 2.3 Их SNI-валидация (эталон — мы УЖЕ совпали)

`quic_handshake.rs::is_valid_sni_hostname`: непустой, ≤253, не начинается/кончается
точкой, каждый label непустой ≤63 без edge-дефиса, только `[a-zA-Z0-9-]`. Предыдущая
сессия УЖЕ переписала нашу `validateMasqueDomain` ровно под это (LDH + edge-hyphen +
trailing-dot + underscore для QNAME). **Этот фикс правильный, не трогай его.**

### 2.4 Конфликт двух моделей — это надо решить

Наш механизм — **клиентская I1-приманка** (пакет шлётся ПЕРЕД handshake, см. §3.2),
а WireSock `transform.rs` — **серверная padding-трансформация responses**. Они не
тождественны. Предыдущая сессия сделала QUIC по модели `mini_quic_generator`
(byte-perfect Initial с расшифровываемым SNI) — это валидно для I1, но НЕ то, что
делает WireSock. Тебе нужно:
1. Прочитать `transform.rs` целиком (apply_quic_padding_short, apply_dns_padding,
   apply_stun_padding, apply_sip_padding, fnv1a_seed, lcg_step).
2. Решить честно: портировать их подход в CPS-форму (short-header QUIC + EDNS-response
   DNS + STUN-response + SIP-text), ИЛИ оставить mini_quic_generator-QUIC где он
   уместнее. Скорее всего: **DNS/STUN/SIP — портировать из WireSock** (их структуры
   правдоподобнее моих), **QUIC — обсудить/выбрать** (short-header а-ля WireSock
   проще и честнее, чем мой "byte-perfect Initial", но Initial раскрывает SNI цензору,
   что и есть смысл приманки — см. §2.5). Прими решение и обоснуй в SPEC.

### 2.5 Про QUIC Initial vs short-header (важный нюанс модели угрозы)

Verified (USENIX'25, GFW SNI-QUIC censorship): у QUIC Initial поля DCID+salt
открытые, цензор выводит ключи из DCID и расшифровывает payload, читая SNI. Смысл
QUIC-приманки-Initial — чтобы цензор расшифровал и увидел РАЗРЕШЁННЫЙ SNI (`id`).
Поэтому если делать Initial — он должен быть byte-perfect и эмититься ОДНИМ статичным
`<b 0x..>` (DCID/ciphertext/tag взаимосвязаны, рандомить нельзя — расшифровка сломается).
WireSock же выбрал short-header (не раскрывает SNI, но не ломается на ≥1200 и менее
заметен). **Это легитимная развилка — выбери и задокументируй честно, без вранья
про "byte-perfect".**

---

## 3. Существующая кодовая база (что уже есть)

### 3.1 Архитектура AWG в репо (НЕ ломай)

- `option/wireguard_awg.go` — lx-файл. Структура `AmneziaWGOptions` с полями
  Jc/Jmin/Jmax/S1-S4/H1-H4/I1-I5. Предыдущая сессия УЖЕ добавила туда `Id/Ip/Ib string`
  (json `id`/`ip`/`ib`). `IsSet()` сравнивает со `AmneziaWGOptions{}` — новые поля
  автоматически (а) попадают под stub-гейтинг без тега, (б) при пустых оставляют
  конфиг байт-в-байт upstream.
- `transport/wireguard/device_awg.go` — lx, под `//go:build with_awg`. Функция
  `awgIpcLines(o) (string, error)` рендерит AWG-поля в IpcSet-строки (`\ni1=<...>`).
  Предыдущая сессия УЖЕ вписала туда вызов `masqueI1(o)` → подстановка как `i1`.
- `transport/wireguard/device_stub_awg.go` — `//go:build !with_awg`, отвергает любой
  AWG-конфиг ("AmneziaWG (awg) support is not included…"). Гейтинг работает.
- `submodules/wireguard-go/device/obf.go` — vendored amneziawg-go. `newObfChain(spec)`
  парсит CPS-теги: `<b 0xHEX>` (статика), `<r N>` (N рандом-байт), `<rc N>` (рандом
  ASCII-буквы), `<rd N>` (рандом цифры), `<t>` (timestamp 4б). **НЕ экспортирован,
  другой Go-модуль** — проверять парс можно временным тестом в этом пакете с
  `GOFLAGS=-mod=mod`, потом откатить go.mod/go.sum.
- `submodules/wireguard-go/device/send.go:135` — I1..I5 шлются как приманки перед
  handshake через `Obfuscate(buf, nil)` (src=nil — реальных данных не несут). Любой
  шаблон "статика+рандом" работает. **Submodule НЕ трогать** — он чистый, держи так.

### 3.2 Файлы, которые предыдущая сессия УЖЕ написала (ревьюни и почини)

```
option/wireguard_awg.go                         (M) +Id/Ip/Ib
transport/wireguard/device_awg.go               (M) +вызов masqueI1
transport/wireguard/masque_awg.go               (новый) диспетчер+валидация+dns+sip+CPS-хелперы
transport/wireguard/masque_quic_awg.go          (новый) QUIC (порт mini_quic_generator)
transport/wireguard/masque_awg_test.go          (новый) тесты
SPECS/009-F-C-WIRESOCK_MASQUERADE_PROFILES/     SPEC.md/PLAN.md/TASKS.md/IMPLEMENTATION_REPORT.md
docs/lx-config.md                               (M) секция id/ip/ib
SPECS/README.md                                 (M) roadmap-строка 009
```

---

## 4. ОШИБКИ предыдущей сессии (НЕ повтори; адверсариальный ревью их нашёл)

**P0 — ложные заявления:**
1. `masque_quic_awg.go` заявлен "byte-perfect port of mini_quic_generator", но
   ОТКЛОНЯЕТСЯ от референса: добавлены `browserCipher` (cipher_suites) и
   compression_methods, которых в референсе НЕТ (референс: `0x0303 + 32 random +
   [0,0,0,0] + ext`). +7 байт, каскадный сдвиг. Либо точный порт, либо не называй портом.
2. `ib` (browser fingerprint) — ФИКЦИЯ: перестановка 6 байт cipher-suites внутри
   ЗАШИФРОВАННОГО ClientHello. Ни один JA3/JA4 это не распознает. В референсе
   mini_quic_generator `ib`/level — это пресеты НАРЕЗКИ фрагментов (`<b>`/`<r>`),
   не cipher-байты. А WireSock `ib` вообще не использует так. Реши честно: либо
   реальный fingerprint, либо нарезка-пресеты, либо убери `ib`-влияние и задокументируй.

**P1 — баги (уже починены, ПРОВЕРЬ что не сломал):**
3. Инъекция через домен: старая `validateMasqueDomain` пропускала control-байты
   (`\n\r\0\t`) и SIP/URI-метасимволы (`> ; @ "`) → header injection в SIP, порча
   DNS-label. ПОЧИНЕНО строгой LDH-валидацией. Должно остаться.

**P1 — тавтологичные тесты (почини):**
4. `TestBuildQUICInitialCPSBrowserDistinct` — тавтология: 32 рандом-байта ClientHello
   всегда разные, тест прошёл бы даже если browserCipher возвращал одно. Не изолирует
   cipher-поле. Перепиши на реальную проверку (фикс-random, сравни именно отличающее поле).
5. Round-trip/well-formed тесты проверяют только само-консистентность (AEAD сходится),
   НЕ верность референсу. Нет KAT против реального вывода. Добавь сравнение с эталоном.

**P2 — недоделки:**
6. QUIC Initial <1200 байт (RFC 9000 §14.1 требует ≥1200) — выкинут `padto`. Если
   остаёшься на Initial — верни padding до 1200 (PADDING frames 0x00 внутри payload
   до шифрования). Если уходишь на short-header — неактуально.
7. QUIC ClientHello невалиден как TLS1.3/QUIC: нет supported_versions(0x002b),
   quic_transport_parameters(0x0039, MANDATORY для QUIC), key_share, ALPN. Если
   остаёшься на полноценном Initial — добавь. Если short-header — неактуально.

**Тест-инфра грабли:**
- Тестовый `parseCPS` в `masque_awg_test.go` требует contiguous-теги, а реальный
  `newObfChain` молча скипает junk между тегами. Тесты проверяют свойство, которого
  движок не требует — не полагайся только на свой regex-парсер, гоняй через реальный
  `newObfChain` (см. §3.1).

---

## 5. Конституция проекта (SPECS/CONSTITUTION.md — соблюдай)

- Тонкий downstream sing-box. Приоритет: минимальный дифф / дешёвый ребейз > корректность
  > ребейзопригодность > сама фича.
- Каждая фича за build-tag (`with_awg`). Без тега — байт-в-байт upstream.
- Новый код — в новых файлах. Правки upstream-файлов только где иначе нельзя, обёрнуты
  `// lx:begin awg` / `// lx:end awg`.
- Module path остаётся `github.com/sagernet/sing-box`. Submodule не трогать.
- Портируемый код (mini_quic_generator MIT / wiresock MIT) — в отдельных файлах с
  сохранением исходного copyright + указанием происхождения (§6 конституции). Обе
  лицензии MIT, совместимы с GPLv3-проектом.
- Spec Kit цикл: SPEC.md → PLAN.md → TASKS.md → IMPLEMENTATION_REPORT.md. Папка
  `NNN-T-S-NAME` (T=F/B/Q, S=N/O/W/C). Образец стиля — `SPECS/008-*/`.

## 6. DoD (критерии готовности)

- `go build ./...` без тегов — ок.
- `go build -tags "with_wireguard with_gvisor with_awg" ./cmd/sing-box` — ок (нужны
  ВСЕ три тега, иначе check падает на "WireGuard/gVisor not included").
- `go test -tags with_awg ./transport/wireguard/...` — зелёный.
- CPS-вывод каждого профиля принимается РЕАЛЬНЫМ `newObfChain` (временный тест в
  submodule, `GOFLAGS=-mod=mod`, потом `git -C submodules/wireguard-go checkout go.mod go.sum`).
- End-to-end: собрать бинарь, прогнать `sing-box check` на конфигах с id/ip/ib для
  всех профилей + проверить что конфликт с явным `i1` отвергается. (Шаблон конфига —
  `lx-test/config/awg2_basic.json`, замени i1..i5 на id/ip/ib.)
- Существующие `lx-test/config/awg2_basic.json` / `awg2_ranged.json` — без регресса.
- Gating: id/ip/ib без `with_awg` → "awg support not built".
- `gofmt -l` всех lx-файлов — пусто (lx-CI это линтит; помни про выравнивание комментов).
- Тесты — НЕ тавтологии. Проверяй реальные свойства (валидность протокол-структур
  обратным парсингом, верность референсу, отличие профилей по СУЩНОСТНЫМ байтам).
- Честные комментарии и доки — без "byte-perfect" если не byte-perfect, без "fingerprint"
  если не fingerprint. Задокументируй ограничения профилей (особенно decoy-природу
  и то, что DNS-query/response шлётся на WG-порт, а не резолверу — это ограничение модели).

## 7. План действий (рекомендация)

Развилки уже решены в `SPEC.md` (механизм I1, структура из WireSock, QUIC =
short-header, +STUN) — НЕ передоговаривай их, реализуй. Кода в дереве нет — пишешь
с нуля (см. §0).

1. Прочитай этот промпт, потом `SPEC.md` этой папки, потом `transform.rs` WireSock
   целиком (через gh api — см. §2.1). Файлов `masque_*.go` НЕТ (удалены) — не ищи.
2. Напиши `PLAN.md` + `TASKS.md` (Spec Kit, образец `SPECS/008-*/`) по решениям SPEC.
3. Добавь поля `Id/Ip/Ib` в `option/wireguard_awg.go` (~12 строк, §3.1) — откачено.
4. Напиши генераторы с нуля: DNS → EDNS OPT response (порт WireSock), STUN → Binding
   response (порт), SIP → response-текст (порт), QUIC → 1-RTT short header (порт
   WireSock, НЕ Initial). Без ложных заявлений «byte-perfect»/«fingerprint».
5. `ib`: по SPEC §3.3 — НЕ выдумывать JA3 (его нет в WireSock QUIC); валидировать
   набор и честно задокументировать минимальный/нулевой эффект.
6. Воспроизведи строгую `validateMasqueDomain` (LDH, эталон
   `transform.rs::is_valid_sni_hostname`) — закрывает инъекцию домена в SIP/DNS.
7. Впиши вызов `masqueI1` в `awgIpcLines` (`device_awg.go`, откачено).
8. Напиши тесты с нуля под реальные свойства (НЕ тавтологии — см. §4 п.4-5); гоняй
   через настоящий `newObfChain`; добавь KAT где портируешь.
9. Обнови `docs/lx-config.md` + `SPECS/README.md` + `IMPLEMENTATION_REPORT.md` честно.
10. Прогони весь DoD (§6).
11. **СТОП. НЕ коммить, НЕ пушить** (см. баннер-запрет в начале промта — он ненарушаем).
    Оставь изменения в working tree, доложи пользователю по-русски: что портировал
    откуда, какие развилки как решены, что проверено, что осталось (живой AWG-сервер
    недоступен — это нормально, отметь честно). Жди ручной проверки.

## 8. Полезные команды

```sh
cd /Users/macbook/projects/sing-box-lx
go test -tags with_awg ./transport/wireguard/ 2>&1 | tail
gofmt -l option/wireguard_awg.go transport/wireguard/masque_*.go
go build -tags "with_wireguard with_gvisor with_awg" -o /tmp/sb ./cmd/sing-box
git -C submodules/wireguard-go status --short   # должно быть пусто
git status --short                              # смотри что натрогал
```

Память проекта: см. `~/.claude/.../memory/wiresock-id-ip-ib-feasibility.md` (там
факты про feasibility, но она писалась ДО находки wiresock/amneziawg-install — этот
промпт новее и точнее).
