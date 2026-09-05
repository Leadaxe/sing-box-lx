---
icon: material/alert-decagram
---

# lx changelog

Changes in the `sing-box-lx` fork (the `lx` features layered on top of upstream
sing-box). Upstream's own changelog is in [changelog.md](../docs/changelog.md); this file
tracks only the fork. Versions are tagged `vX.Y.Z-lx.N`; releases are built by
`lx-release.yml`. Tags carrying an `-rc.N` / `-alpha.N` / `-beta.N` suffix publish
as GitHub **pre-releases** and never become "Latest".

This file is the **engineering log** (SPEC links, implementation detail, one section
per tag). The user-facing release page body comes from `docs-lx/releases/v<version>.md`
when that file exists (bilingual, LxBox format — see `docs-lx/releases/TEMPLATE.md`;
required for stable tags); this changelog section is the fallback used for pre-releases.

> **Language.** Unlike the rest of `docs-lx/`, this file has no `.ru.md` twin, and its
> entries are written in Russian. Two reasons: `lx-release.yml` extracts a section from
> **this exact path** as the pre-release body, so a split would need the CI contract
> changed, and translating years of history would rewrite the record of what was said at
> the time. User-facing notes are bilingual where it counts — in
> [`releases/`](releases/), which is what a reader of a release page actually sees.
>
> **Язык.** В отличие от остального `docs-lx/`, у этого файла нет пары `.ru.md`,
> а записи ведутся по-русски: `lx-release.yml` берёт секцию **именно отсюда** в тело
> пререлиза, а перевод многолетней истории переписал бы запись о том, что говорилось
> тогда. Пользовательские ноты билингвальны там, где это важно, — в
> [`releases/`](releases/).

#### v1.14.0-lx.33

Стабильный релиз. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.33.md`](releases/v1.14.0-lx.33.md).

**Что вошло:**

- ✨ **Снифферы VPN/VoIP для LAN-трафика: `wireguard`, `openvpn`, `ike`, `tailscale`,
  `sip`** ([FEATURE 016-SNIFF](../SPECS/FEATURES/016-SNIFF/FEATURE.md),
  [SPEC 078](../SPECS/TASKS/078-WIREGUARD_PACKET_SNIFFER/SPEC.md),
  [SPEC 079](../SPECS/TASKS/079-VPN_VOIP_PACKET_SNIFFERS/SPEC.md)). Форма первого
  пакета клиента, каждый сниффер — отдельный `common/sniff/*_lx.go`, апстримные
  снифферы не правятся: wireguard — типы 1/2/3 по точным размерам 148/92/64, тип 4
  `len>=32 && (len-32)%16==0`, резервные байты свободны (WARP); openvpn —
  `HARD_RESET_CLIENT_V2/V3` plain/tls-auth/tls-crypt, TCP за 2-байтной длиной; ike —
  ISAKMP `IKE_SA_INIT` (v2) и Main/Aggressive (v1), non-ESP-маркер на 4500; tailscale —
  disco по магии `TS💬`; sip — request-line, `domain` из Request-URI (UDP и TCP).
  Имена работают в `sniffer` sniff-действия и в `protocol` правил. В дефолтные списки
  включены под маркером `lx:begin sniff-lx` (`route/route.go`, `route/rule/rule_action.go`,
  `constant/protocol.go`): packet — после `stun` и **до** uTP (апстримный uTP принимал
  `01 00 …` за ST_DATA, и plain WG с LAN помечался `bittorrent`), stream — после `rdp`.
  Юниты (+race), перекрёстный тест дефолтного порядка, прогон через direct-inbound
  бинаря. Дока: `docs-lx/lx-sniff{,.ru}.md`, строки в `docs/configuration/route/sniff{,.zh}.md`.
- 🐛 **AWG: приём больше не теряет data-пакеты при широких `h1–h4`**
  ([SPEC 081](../SPECS/TASKS/081-AWG_RECEIVE_INDEX_FIRST_CLASSIFICATION/SPEC.md)).
  Референсный порядок классификации (init → response → cookie → transport) присваивал
  data-датаграмму handshake-кандидату по случайному слову типа: ровно `s1+148` байт у
  AWG2 (≈8 % таких пакетов при диапазонах из экспортов), любой крупнее при
  `random_trailers` AWG 3.1. Теперь датаграмма с одним из наших живых receiver index за
  словом типа transport классифицируется как данные первой; промах — прежний порядок,
  провод не меняется. Uplink (приёмник сервера) — как раньше. Red/green e2e с
  детерминированной коллизией размера (`s1=44`, `s4=0`, `h1=5-4294967295`). Сабмодуль
  `wireguard-go` → `6f0f7a2`.
- 📌 **Дрейф апстрима отложен сознательно** (как в lx.30–lx.32): `upstream/stable` ушёл на
  309 коммитов от нашей базы (`b5ebaa1fc`, `v1.14.0`), появились теги `v1.15.0-alpha.1/2`;
  мерж — отдельная задача класса SPEC 051 (сначала сабмодули, потом ядро). Релиз несёт
  снифферы и фикс приёма AWG поверх прежней базы. Сабмодули против `go.mod` сверены
  (wireguard-go `c6c8a83` ⊂ `lx-awg2-v005`, sing-tun `d677342` ⊂ HEAD, gvisor —
  снапшот 20260727).

#### v1.14.0-lx.32

Стабильный релиз. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.32.md`](releases/v1.14.0-lx.32.md).

**Что вошло:**

- ✨ **AmneziaWG 3.0/3.1** ([SPEC 080](../SPECS/TASKS/080-AWG3_HEADER_PROTECTION_TIMINGS/SPEC.md)).
  Порт amneziawg-go `v0.2.19 → v3.1.20260828` в прививку `submodules/wireguard-go`:
  защита заголовка `header_protection_key` (ChaCha20, nonce из паддинга `s1–s4`, поэтому
  каждый ≥ 12), `content_padding_addition`, `random_trailers`, `disable_cookies`,
  диапазонные тайминги `rekey_after_time` / `rekey_timeout` / `reject_after_time` /
  `keepalive_timeout` / `max_handshake_attempts` и диапазонный
  `persistent_keepalive_interval` пира (`"25-35"`; число работает как раньше). Все
  поля на корне `wireguard`-endpoint, как и AWG2; экспорт Amnezia `amnezia-awg2`
  (`protocol_version 3.1`) переносится 1:1. Без `with_awg` новые поля (и диапазонный
  keepalive) отвергаются, число проходит. `sing-box check` ловит ключ с коротким
  паддингом и битый ключ с именем поля. Проверено против живого AWG 3.1-сервера:
  хендшейк с первой попытки, TLS, 1 МБ загрузка, rekey по диапазону. Отличие от
  референса: первый батч после старта переносится под актуальный `s4`, а не уезжает
  в старой раскладке. Новый CI-пример `lx-test/config/awg3_full.json`. Доки:
  `lx-protocols-transports{,.ru}.md` §2.10, `lx-config{,.ru}.md`.
  Внутри `option`: `MagicHeader` стал алиасом общего `AWGRange`; тип
  `WireGuardPeer.PersistentKeepaliveInterval` — `AWGRange` (JSON-совместимо).
  Сабмодуль `wireguard-go` → `ba01446` (ветка `lx-awg2-v005`). Прогон перед тегом:
  юниты сабмодуля (в т.ч. `-race` по AWG), option/transport под обоими тегами,
  loopback-e2e двух экземпляров ядра для plain WG / AWG2 (диапазонные `h1–h4`, `s4=9`) /
  AWG3 через реальные UDP-сокеты, живой AWG 3.1-сервер. Android-AAR на устройстве
  не гонялся: протокольный код платформенно-нейтрален, но AWG2-пользователям на
  Android стоит проверить хендшейк на первом старте.
- 📌 **Дрейф апстрима отложен сознательно** (как в lx.30/lx.31): `upstream/stable` ушёл на
  308 коммитов от нашей базы (`b5ebaa1fc`, `v1.14.0`); затронуты все наши зоны
  (`box.go`, `route/`, `dns/`, wireguard-транспорт, `.pb.go`), мерж — отдельная задача
  класса SPEC 051 с проверкой сабмодулей до ядра. Релиз несёт только AWG3 поверх
  прежней базы.

#### v1.14.0-lx.31

Стабильный релиз. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.31.md`](releases/v1.14.0-lx.31.md).

**Что вошло:**

- ✨ **`with_tailscale` в desktop- и роутерных бинарях** (решение владельца 2026-09-04,
  SPEC 004). Апстримные endpoint `tailscale`, DNS-транспорт `tailscale`, certificate
  provider и сервис `derp` теперь доступны во всех архивах релиза: darwin/windows ×
  amd64/arm64, Win7-386, linux-amd64/arm64/armv7/mipsle-softfloat (musl) и
  linux-mips-softfloat. Чистый Go, собирается `CGO_ENABLED=0` против наших сабмодулей
  wireguard-go/sing-tun/gvisor; проверено сборкой на всех шести целях и живым стартом
  endpoint'а на darwin (tsnet поднимается, уходит на control-plane в login). Цена — только
  размер: ~+13 МБ darwin/arm64 (49.8 → 63.2), ~+16 МБ mips softfloat (56.2 → 72.2).
  Android-AAR тег по-прежнему **не** несёт (`lx:no-tailscale` в `build_libbox`): у LxBox нет
  UI под tailscale, а это самая тяжёлая зависимость APK. Тег добавлен в `LX_TAGS`
  (`Makefile.lx`) и `BASE_TAGS` (`lx-ci.yml`); README/SPEC 004 обновлены.
- 📌 **Дрейф апстрима отложен сознательно:** `upstream/stable` ушёл на 308 коммитов от
  merge-base (замер 2026-09-04; 301 на момент lx.30). Мерж — отдельная задача класса
  SPEC 051 (813 файлов, задеты все наши зоны), в этот тег не берётся.

- 🔧 **MASQUE: idle-suspend туннеля по умолчанию выключен** (SPEC 021 B1, решение
  владельца 2026-09-03). Раньше без ключа `idle_timeout` туннель засыпал через 5 минут
  тишины на любой платформе — роутеры и десктопы платили за это полным QUIC-хендшейком +
  CONNECT-IP + новым gVisor-стеком на первом запросе после каждой паузы, а экономили ~6 МБ
  RSS и один keepalive-пакет в 30 с. Теперь suspend включает **только положительное**
  значение (`"5m"`); отсутствие ключа, `"0s"` и отрицательное равнозначны и держат
  туннель до закрытия outbound'а, его живость обеспечивает `keep_alive_period` (30s).
  Кто хочет прежнее поведение (батарейные хосты) — ставит `"idle_timeout": "5m"` явно.
  Ядро: `protocol/masque/outbound.go` (`idleWindow`), комментарий `option/masque.go`;
  тест `idle_default_lx_test.go`; доки §3.8 EN/RU, lx-config, SPEC 021 SPEC/CONFIG.

#### v1.14.0-lx.27

Стабильный релиз линии `lx.27` — сводит rc.1–rc.6. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.27.md`](releases/v1.14.0-lx.27.md).

**Что вошло:**

- ✨ **Outbound `chain`** (SPEC 073, FEATURE 015) — виртуальная цепочка хопов из групп и
  узлов: порядок пакета, группы не копируются (хук в 5 точках дозвона), рантайм-звенья
  через штатный `detour` на внутренние теги `<tag>#i`, прозрачный `direct`, авто-MTU
  туннельных звеньев, `strip`/`rewrite`, наблюдаемость (`detourList`, RPC `GetChains`,
  послойные пробы). Тег `with_lx_chain`. Живая проверка: трёхслойная цепочка на реальных
  узлах, послойная локализация медленного хопа за секунды.
- ✨ **MASQUE `vhttp: "auto"`** (SPEC 074) — h3 с отвязанным падением на h2 по таймеру
  (не по возврату ноги); победивший режим запоминается. Живой стенд: 3.3 с вместо
  40–90 с зависания.
- 🐛 **Мёртвый detour-узел** больше не морозит сетевую машинерию процесса (SPEC 072).
- 🐛 **Стоп во время старта** не роняет процесс — гонка Start×Close на WG-эндпоинте
  (SPEC 070/071).
- 🐛 **Windows + снятая галка IPv6** — WG/AWG-узлы поднимаются (SPEC 069).
- 🐛 **XHTTP**: провал поднятия стрима рвёт свой upload-пайп, живой стрим не умирает по
  дедлайну дайла; дедлок «колбэк под замком» в read-deadline (SPEC 050) — QUIC-хендшейк
  поверх XHTTP-хопа больше не висит 20–90 с.
- 🧰 Upstream-синк: `v1.14.0-beta.15` + точечный хвост stable; сабмодуль `sing-tun`
  догнал `sagernet/main`; закрыт gofmt-долг.

**Остаток (в SPEC 073):** полевая проверка WG-звеньев цепочки на устройстве.

#### v1.14.0-lx.28

Стабильный релиз линии `lx.28` — сводит rc.1–rc.5. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.28.md`](releases/v1.14.0-lx.28.md).

**Что вошло:**

- ✨ **chain: рантайм вкл/выкл любой позиции** (SPEC 075) — тумблеры по позициям без
  правки конфига: passthrough выключенной середины, direct-fallback входа,
  персистентность по тегам в cache-file, `interrupt_exist_connections`,
  `disabled` в `GetChains`, `GetChainCloneConfig` (эффективный JSON звена).
  RPC `SetChainPositionEnabled`. Приёмка на живом box (три shadowsocks-хопа).
- ✨ **XHTTP `session_table` / `session_length`** (Xray PR 6258, issue #13) — форма
  session id вместо узнаваемого дашед-UUID; наборы Xray байт-в-байт,
  регистрозависимо; половинчатая конфигурация отвергается с ошибкой.
- 🔧 **MASQUE: `vhttp: auto` — дефолт** (SPEC 074 v2) — пустое `vhttp` = auto:
  чистый путь платит 3-секундный бюджет только первым туннелем процесса,
  TCP-only-хоп получает самоспасение в h2; явные `h3`/`h2` — пин, как раньше.
- 🔧 **lxd: `log_file` в daemon.json + дефолт ротации 1 МБ** — настраиваемый путь
  лога (admin-плоскость отдаёт фактический), OpenWrt-инсталлятор уводит лог на
  tmpfs (`/tmp/lxd.log`) — ноль износа overlay.
- 🐛 **MASQUE: причина смерти туннеля в логе** — одна строка
  `masque: tunnel died: <операция>: <причина>` от пампа-первоисточника; штатные
  закрытия молчат.
- 🐛 **Сборка 32-битных целей** — `minSessionIDSpace` в `int64` (регресс rc.2,
  падали 386/armv7/mips/AAR); кросс-сборка 386/arm/mips добавлена в предтеговую
  проверку.
- 📎 **chain: тег-схема хопов `<chain>#<i>` закреплена как публичный контракт**
  (SPEC 073) — клиенты адресуют послойные пробы по этим тегам.

**Остаток (в SPEC 073):** полевая проверка WG-звеньев цепочки на устройстве.

#### v1.14.0-lx.30

Стабильный релиз. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.30.md`](releases/v1.14.0-lx.30.md).

**Что вошло:**

- 🐛 **DNS-серверы `udp`/`tcp`/`tls` с `detour` через XHTTP-узел молча падали
  `write request: context canceled`** (SPEC 077) — красные URL-тесты `masque`- и
  `wireguard`-узлов по доменным URL (они резолвят имя через DNS ядра) и мёртвый
  системный DNS через TUN при живом туннеле; DoH через тот же узел работал.
  Связка двух штатных механизмов: апстримный пул DNS-транспорта
  (`dns/transport/conn_pool.go`, `dialAndInstall`) отменяет dial-контекст сразу
  после возврата `dial` — буквальный контракт `net.Dialer`, — а сторож SPEC
  050/072 в `dialStreamOne`/`dialStreamUp` слушал dial-контекст **до `created`**,
  который в `stream-one` приходит лишь после первой записи, то есть всегда после
  возврата. Пул отменял через микросекунды, сторож трактовал это как «клиент
  передумал», первая же запись DNS-запроса получала `context canceled`.
  **Никогда не работало** (до SPEC 072 запрос ездил прямо на dial-контексте), а
  `auto` + REALITY выбирает именно `stream-one` — дефект бил дефолтный конфиг.

  Фикс — смена контракта, а не правка сторожа: `DialContext` для
  `stream-one`/`stream-up` возвращает conn только после того, как HTTP-слой принял
  тело запроса (первый **вызов** `Read` на pipe; x/net http2 делает его сразу
  после отправки заголовков, до ответа). Download-ответ по-прежнему не ждётся —
  инвариант SPEC 061 цел, `TestDialDoesNotBlockOnDownloadResponse` зелёный без
  правок. После возврата dial-контекст на conn не влияет. Провал подъёма до
  принятия тела и полуживой узел на холодном пуле дают ошибку **из `DialContext`**
  с причиной или по дедлайну вызывающего — случай 072-C закрыт штатно, сторож
  `watchDialContext` снят.
- 🐛 **Двойной возврат слота xmux на ошибочном пути дайла** (SPEC 077, найдено при
  реализации) — `fail()` уже освобождал pooled-соединение, а `DialContext` на
  ошибке `dialMode` делал `addOpenUsage(-1)` ещё раз. До 077 dial не мог упасть
  после создания conn, теперь может: `openUsage` ушёл бы в минус, и снятое с
  ротации соединение не закрылось бы никогда. Теперь один once-хэндл
  `newXmuxRelease` на весь дайл.

**Сознательная цена (SPEC 077 §2/§4):** тёплый h2-conn к молча умершему пиру
принимает тело мгновенно, поэтому такой узел ловится не 15-секундным дедлайном
дайла WG-bind, а `ReadIdleTimeout` (`xmux.h_keep_alive_period`), give-up SPEC 041
или read-deadline вызывающего. Отдельный «raise-таймаут после принятия тела» —
решение владельца, в этот релиз не входит.

**Известный дрейф:** `upstream/stable` ушёл вперёд на 301 коммит (813 файлов,
включая релиз upstream v1.14.0, Go 1.26.7, `sing-tun` v0.8.15 и переработку
`dns/client.go`, `route/route.go`, `daemon/`). Мерж сознательно отложен — это
объём отдельной задачи класса SPEC 051, а не часть багфикс-релиза; лента
`upstream/stable` не трогает `transport/v2rayxhttp`, где живёт этот фикс.

**Остаток (в SPEC 077):** полевой прогон на стенде лаунчера — `dig` через TUN при
`dns.final` с detour на XHTTP-узел, URL-тест `masque` по доменному URL,
три `tcp`-DNS-запроса подряд через пул.

#### v1.14.0-lx.29

Стабильный релиз линии `lx.29`. Пользовательские ноты (EN+RU):
[`docs-lx/releases/v1.14.0-lx.29.md`](releases/v1.14.0-lx.29.md).

**Что вошло:**

- 🐛 **XHTTP: 100% CPU до перезапуска ядра, когда путь до сервера рубит стримы**
  (SPEC 076, issue [#14](https://github.com/Leadaxe/sing-box-lx/issues/14)) — CDN,
  отвечающий `INTERNAL_ERROR` на каждый upload-стрим, превращал клиента в
  незатухающий цикл dial→reset→dial на скорости канала: два ядра ARM64-роутера в
  `http2.clientConnReadLoop` (94% выборок — чтение 9-байтных заголовков служебных
  кадров, тел нет), нагрузка не спадала сама. В xmux-пул добавлен circuit breaker:
  3 подряд отказа стрима (ошибка round-trip, статус ≠ 200, удалённая смерть тела
  download'а) снимают соединение с ротации (`evictCause "failing"`), каждый срыв
  удваивает задержку открытия нового транспорта — 100 мс → потолок 3 с. Успехом
  считаются **данные**, не заголовки (дочитанный 200-POST, первый успешный `Read`
  тела): в полевом случае raise проходил с 200, а тело умирало через секунды.
  Backoff гейтит только пустой пул — живое соединение выдаётся без ожидания;
  `io.EOF` и наш собственный `Close`/дедлайн брекер не трогают.
- 🐛 **XHTTP packet-up: upload больше не падает на graceful GOAWAY** (SPEC 076) —
  payload и так лежал копией в `[]byte`, но `request.GetBody` не выставлялся, и
  http2 не мог прозрачно повторить POST на новом соединении
  (`cannot retry err … after Request.Body was written` в полевых логах).
- 🐛 **XHTTP stream-up: pooled-соединение никогда не возвращалось в пул**
  (баг SPEC 059, найден попутно) — `newSplitConn` принимал `xmux *xmuxRelease` и не
  записывал его в структуру, поэтому `release()` в `Close`/`fail` был nil-no-op:
  `openUsage` рос монотонно, снятое с ротации соединение не сносилось никогда
  (отложенный teardown ждёт `openUsage <= 0`), а на `max_concurrency` пул считал
  занятыми давно мёртвые стримы и вытеснял переиспользование.

**Остаток (в SPEC 076):** полевой прогон репортёром issue #14 на CDN-пути.

#### v1.14.0-lx.28-rc.5

### 🔧 lxd: настраиваемый путь лога (`log_file`) + дефолт ротации 1 МБ

Повод: на OpenWrt без extroot `lxd.log` рядом со state-каталогом пишется в
NAND-overlay — износ флеша. Теперь путь настраивается, а инсталлятор сразу
уводит лог на tmpfs.

- **`log_file` в daemon.json** — переопределение пути ротируемого лога
  (пусто = прежний `<родитель state-dir>/lxd.log`; относительный путь
  резолвится в абсолютный при старте — служба живёт с cwd `/`).
- **Admin-плоскость перестала пере-выводить путь из state-dir**: фактический
  путь проносится в контроллер, `GET /admin/info` (`log_path`) и tail-эндпоинт
  `GET /admin/logs` отдают его; dev-запуск без файла отвечает честным 404
  «лог в терминале», а не фантомным derived-путём.
- **Дефолт `log_max_size_mb` 20 → 1**: страховочный потолок размера; вместе с
  бэкапом максимум ~2 МБ на диске. Ротация по возрасту (24 ч) не тронута.
- **OpenWrt-инсталлятор** пишет `"log_file": "/tmp/lxd.log"` в daemon.json —
  лог на tmpfs, ноль износа overlay (теряется на ребуте — для роутера это
  правильный размен). Старый бинарь неизвестный ключ молча игнорирует.

Юниты: 404 без настроенного файла + прежний 404 на отсутствующий файл;
полный пакет `lxd` зелёный. Доки: таблица daemon.json и раздел OpenWrt в
`docs-lx/lxd-daemon.md` / `.ru.md`.

#### v1.14.0-lx.28-rc.4

### ✨ chain: runtime вкл/выкл любой позиции (SPEC 075)

Запрос владельца: выключать хоп цепочки из UI без правки конфига и перезапуска.
Механика легла на существующую прозрачность SPEC 073: выключенная позиция ≥ 1 —
passthrough в хоп i−1 (группа не опрашивается, звено не создаётся), выключенный
вход — прямой дозвон хопа 0 в реальную сеть дефолтным direct-диалером (привязка к
интерфейсу/марки сохраняются). Любая комбинация валидна: всё выключено = цепочка
вырождается в direct. Клоны верхних позиций не перестраиваются — их `detour`
смотрит на тег хопа, а хоп резолвит на каждом дозвоне.

- **RPC** (`with_lx_command`): `SetChainPositionEnabled(chainTag, position, enabled)` —
  флаг применяется всегда; провал активного прогрева звена при включении едет **данными**
  (`warmupError` в ответе), не статус-ошибкой — тумблер выражает волю пользователя,
  здоровье узла видно в `GetChains`. Прогрев — по правилу preload: только детерминированный
  выбор (узел/selector), urltest остаётся ленивым.
- **Персистентность**: набор выключенных позиций — в cache-file (lx-бакет
  `chain_disabled_lx`), по **тегам** позиций, как selected у селектора: правка конфига
  не сбивает соответствие, ушедший тег молча игнорируется. Загрузка на старте до preload.
- **Живые соединения**: любой тумблер рвёт соединения цепочки по селекторной модели —
  новая опция `interrupt_exist_connections` (default false) гейтит разрыв внешних
  (пользовательских); звено «в отставке» не закрывается принудительно, его забирает
  штатный idle-эвикшн.
- **Диагностика**: `disabled` в `ChainPosition` (`GetChains` / Clash API; `now` у
  выключенной позиции заполнен — видно, ЧТО выключено), `ChainPath` пропускает
  выключенные; новый `GetChainCloneConfig(chainTag, position)` — эффективный JSON звена
  (`{type, tag, …}` после strip → rewrite → MTU → detour), снимок при создании звена
  по модели RunningConfig (SPEC 037).
- Тег-контракт хопов `<chain>#<i>` не тронут; проба выключенного хопа меряет путь без него.

Юниты (`protocol/chain`): выкл середины/входа/всего, обратное включение с прогревом,
провал прогрева, персистентность, interrupt по флагу, пропуск preload, `CloneConfigJSON`.
Приёмка (`lx-test/chain`): живой box на трёх shadowsocks-хопах — трафик во всех
комбинациях тумблера, реальный cache-file через рестарт, реальный direct-fallback входа.
Остаток: полевой прогон на WG-звеньях (вместе с долгом SPEC 073).

#### v1.14.0-lx.28-rc.3

### 🐛 Сборка 32-битных целей (регресс rc.2)

`minSessionIDSpace = 1 << 31` — нетипизированная константа 2147483648, которая не влезает
в 32-битный `int` (потолок 2147483647). На amd64/arm64 `int` 64-битный, поэтому локальная
сборка и тесты проходили; упали ровно 386, armv7, mips, mipsle и AAR. Там же переполнялся
и счётчик `space *= size`.

Константа и накопитель переведены в `int64`. Кросс-сборка под `GOARCH=386/arm/mips`
добавлена в проверку перед тегом — локального `go build` для таких констант мало.

#### v1.14.0-lx.28-rc.2

### ✨ XHTTP: `session_table` / `session_length` — форма session id (Xray PR 6258)

Вопрос из [issue #13](https://github.com/Leadaxe/sing-box-lx/issues/13): при переносе конфига
с Xray не нашлись `sessionIDTable`/`sessionIDLength` — они приехали в
[XTLS/Xray-core#6258](https://github.com/XTLS/Xray-core/pull/6258) (влит 2026-06-09), уже
после того, как наш XHTTP портировался по SPEC 002.

По умолчанию session id — дашед-UUID (`8f14e45f-…-d0b8e5c1a2b7`): 36 символов, hex, дефисы
на фиксированных позициях. Отпечаток узнаётся регуляркой, а в `packet-up` к нему ещё
приклеивается инкрементный `/0`, `/1`, `/2`. Две новые опции заменяют его случайной строкой
заданного вида — `"session_table": "Base62"` + `"session_length": "16-32"` даёт
`/api/v3/k7Qm2XpR9vLdA3wZ/0`.

- Алфавит: имя предопределённого набора (`hex`, `HEX`, `number`, `alphabet`, `Alphabet`,
  `ALPHABET`, `base36`, `BASE36`, `Base62` — байт-в-байт набор Xray, **регистрозависимо**)
  либо свой ASCII-набор. Длина — `"min-max"` или `"n"`.
- **Чисто клиентская ручка**: сервер видит session id как непрозрачный ключ группировки,
  согласование не нужно, Xray-сервер принимает любую форму.
- Дефолт не изменился: без этих полей id остаётся дашед-UUID, как и у ненастроенного Xray.
- Валидация: пол длины > 0 и пространство id (`len(table)^min`) > 2³¹ — иначе два независимых
  клиента вытянут одинаковый id и склеятся в одну сессию на сервере.
- **Отход от Xray, осознанный:** половинчатая конфигурация (одна опция из двух) у нас
  отвергается с ошибкой, а не молча вырождается в UUID. Написавший `session_table` без
  `session_length` хотел не-UUID и молча получил бы ровно то, от чего уходил, — заметно
  только по проводу.
- Имена полей — по нашей конвенции (`session_placement`/`session_key`), не по Xray-camelCase.
  Xray-имена указаны в доках, чтобы следующий переносящий конфиг нашёл их поиском.

Юниты: алфавит, разброс длины в диапазоне, регистрозависимость имён (`hex` ≠ `HEX`), дефолт
= UUID, id едет через тот же placement-движок; 5 negative-кейсов. Живая проверка через
собранное ядро: валидный конфиг принят, четыре невалидные комбинации отклонены внятным
сообщением.

#### v1.14.0-lx.28-rc.1

### ✨ MASQUE: `vhttp: auto` — теперь дефолт (SPEC 074 v2)

Полевой случай LxBox (2026-08-24): цепочка `[WG-WARP, VLESS-tcp-Reality, MASQUE]` — MASQUE
третьим хопом на дефолтном `vhttp: h3` давал **стабильный** deadline: VLESS-звено переносит
только TCP, QUIC-датаграммы уходят в тишину, отказа на проводе нет. Ровно тот класс, ради
которого SPEC 074 делался, — но спасение надо было включать руками.

- Пустое `vhttp` теперь = `auto`: на чистом пути это тот же h3 (3-секундный бюджет платит
  только первый туннель процесса), за TCP-only-хопом — самоспасение в h2.
- `standard`-профиль: дефолт тихо вырождается в h3; предупреждение — только при явном
  `vhttp: "auto"` (дефолт, приземлившийся на standard, — не ошибка пользователя).
- Явные `h3`/`h2` — пин, как раньше, без изменений.
- Резолв поля выделен в `resolveVHTTP` + юниты (дефолт, гейты standard, отказы).

### 🔧 MASQUE: причина смерти туннеля видна в логе

Оба пампа (стек↔туннель) умирали молча: смерть туннеля была видна только следующим
`establishing`, слой-виновник — неразличим (полевой разбор цепочек LxBox, три гипотезы
без единой зацепки в логе). Теперь памп, умирающий сам, оставляет ровно одну строку
`masque: tunnel died: <операция>: <причина>`; штатные закрытия (idle-suspend, `Close`,
эвикшн звена цепочки) молчат — гейт по ctx сессии, Warn пишет только первоисточник.
Юниты: гейт live/cancelled ctx, сквозной путь через памп (Warn + teardown).

### 📎 chain: тег-схема хопов закреплена как контракт

`<chain>#<i>` — публичный контракт (SPEC 073): клиенты (лаунчер, LxBox) адресуют по этим
тегам послойные пробы через CommandClient. Якорь-комментарий у `hopTag()`; смена формата —
только явной правкой SPEC 073 с миграционной заметкой в changelog.

#### v1.14.0-lx.27-rc.6

### ✨ MASQUE `vhttp: "auto"` — h3 с автопадением на h2 (SPEC 074)

Полевой разбор (2026-08-24) вскрыл класс отказов, у которого **нет сообщения об ошибке**:
Cloudflare WARP молча игнорирует QUIC с выходных IP части хостеров. Стенд на живых узлах:
UDP и QUIC через такой узел ходят (`www.google.com` отвечает за 695 мс), а три WARP-эндпоинта
не отвечают вовсе — хендшейк висит до дедлайна вызывающего. TCP:443 при этом принимается
отовсюду (та же цепочка на `h2` — 205 мс).

- Новое значение `vhttp: "auto"`: h3-нога в своей горутине, через **3 с** (или по её
  ранней ошибке) стартует h2 — **не дожидаясь возврата h3**. Застрявшая в чужом
  cleanup-коде нога (см. фикс ниже) не держит дозвон; её поздний успех закрывается в фоне.
- **Победивший режим запоминается** на процесс: таймаут платит только первый туннель,
  дальше дозвон идёт сразу в рабочую ногу.
- **Отмена вызывающим — не вердикт h3:** fallback не делается, память не пишется.
- При падении обеих ног ошибка несёт **обе** причины.
- `standard`-профиль: h2-ноги нет, `auto` вырождается в h3 с предупреждением (не ошибка старта).
- Потолок MTU для h2 применяется и к `auto` — иначе fallback был бы заведомо нерабочим.
- Фиксированные `h3`/`h2` ведут себя ровно как раньше.
- 15 юнитов (`-race`), включая «h3 застряла навсегда → h2 за ~бюджет», «поздний успех
  закрывается», «h2 упал → поздняя h3 спасает»; живой стенд: глухой h3 через xhttp-хоп →
  h2 established за 3.3 с (было 40–90 с зависания).

### 🐛 XHTTP: дедлок «колбэк под замком» в read-deadline

`readDeadline.expireLocked` вызывал закрытие h2-тела под удерживаемым мьютексом; закрытие
блокируется (`x/net/http2` ждёт полного завершения стрима), и на замке вставали остальные
потребители дедлайна — включая quic-go, зовущий `SetReadDeadline` из `Transport.Close` на
своём dial-пути. Наружу: QUIC-хендшейк поверх xhttp-хопа висел 20–90 с. Дамп горутин —
три во взаимном ожидании. Фикс: колбэк выполняется вне замка. Второй слой (блокирующийся
`transportResponseBody.Close` сам по себе) задокументирован в SPEC 050 как открытая
находка — потребители от него отвязаны дизайном `auto`.

#### v1.14.0-lx.27-rc.5

### ✨ Outbound `chain`: виртуальная цепочка хопов из групп и узлов (SPEC 073, FEATURE 015)

Новый тип outbound'а `chain` — многохоповый путь, собираемый в рантайме из того, что
группы выбрали прямо сейчас:

```json
{ "type": "chain", "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"] }
```

- **Порядок = порядок пакета**: `[0]` — вход (касается реальной сети, используется как
  есть, с его dial-полями), последний — выход. Любая позиция — узел, endpoint или группа
  любой вложенности; длина ≥ 2.
- **Группы не копируются.** Цепочка зовёт оригинальную группу; хук
  `adapter.ResolveChainLeaf` в пяти точках дозвона групп (selector ×2, urltest ×2,
  fallback-дайл SPEC 054) подменяет выбранный узел на его **звено** — рантайм-экземпляр
  узла с `detour` на внутренний тег предыдущей позиции `<tag>#i`, созданный штатным реестром
  из копии опций. Звено несёт тег оригинала: история, штрафы, sticky, `interrupt_exist_connections`
  работают без правок.
- **Звенья ленивые**, плюс прогрев детерминированных позиций на старте (узлы, селекторы;
  urltest-позиции — лениво); удаляются по простою `idle_timeout` (дефолт `5m`) только при
  нуле живых соединений; WG-звенья участвуют в idle-suspend (SPEC 020) и смене сети.
- **`direct` на позиции ≥ 1 прозрачен** (выключатель хопа из селектора), `block` терминален.
- **Авто-MTU туннельных звеньев** (WG −60/−80, MASQUE ≈ −90 от ёмкости IP-туннелей ниже,
  худший случай по группе); над потоковыми/датаграммными прокси MTU не меняется.
- **`strip_evasion: true`** (дефолт) снимает у звеньев односторонние DPI-приёмы —
  `tls.fragment` (пакетная; `record_fragment` SPEC 060 не трогается), `multiplex.padding`,
  `xhttp.padding`; карта `strip` правит каталог (`tls.utls` — по запросу, несовместим с
  reality); **`rewrite`** — merge-patch по типу узла. Порядок strip → rewrite → MTU, сухой
  прогон всех патчей на старте.
- Наблюдаемость: путь в `detourList` (SPEC 017), RPC `GetChains` (`with_lx_command`) и
  Clash API `/proxies/<tag>` → `chain`, ошибки дозвона с позициями
  (`chain[tag] #2 (exit) via #1 (mid): …`), URLTest по `<tag>#i` — задержка по слоям,
  pprof-метки `lx.chain/lx.pos/lx.leaf` на горутинах звеньев.
- Build-tag `with_lx_chain` (в `LX_TAGS` и AAR). Тронутые upstream-файлы — за маркером
  `lx:begin chain`: менеджеры outbound/endpoint (память опций, внутренние теги), selector/urltest
  (хук), tracker (путь), route (звенья в idle-тике и смене сети), clashapi, proto (§3.6).
- Тесты: юниты на фейковых узлах (три формы, переключение/эвикшн, direct/block, strip/rewrite,
  MTU-таблица, валидация) + приёмочный стенд `lx-test/chain` на живых shadowsocks-хопах.
  Дока: `docs-lx/lx-config.md` §9 / `.ru.md` §9.


### 🧰 Upstream-синк сабмодулей: sing-tun догнал `sagernet/main`

Ревизия дрейфа по всем трём форк-сабмодулям. `wireguard-go` и `gvisor` чисты
(первый ровно на вершине `sagernet/dev`, второй — снимок пина
`v0.0.0-20260727.0-sing-box-mod.1`, апстрим его не двигал). В `sing-tun`
нашлись три фикса, вышедших **после** нашей базы `7c73233` и отсутствующих в
ленте `sagernet/dev` (она форкнулась от того же коммита раньше, поэтому
`merge-base` с dev показывал ноль и дрейф был виден только против `main`):

- `2ce8e08` — gVisor lazy-conn больше не выставляет `KeepaliveIdle`/
  `KeepaliveInterval` в 15 с; апстрим признал их источником избыточных
  TCP-keepalive-проб. Единственный конфликт синка: наша строка
  `newGTCPConn(...)` (из dev-ленты) против апстримовой `gonet.NewTCPConn` —
  разрешён вручную, наш конструктор сохранён, удалены только два `SetSockOpt`.
- `ac90f98` — `redirect`: починен поиск `su` на Android.
- `a95f1ae` — `tun_linux`: UDP/ICMP-ответы больше не отбрасываются строгим
  `rp_filter` при включённом `auto_redirect`.

Патч [SPEC 040](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md)
(`acceptLoop` self-heal в `stack_system.go`) цел. Автослияния двух остальных
коммитов сверены с апстрим-версиями — содержимое идентично, разошлись только
номера строк. Проверено: `gofmt`, сборка сабмодуля под linux/android/darwin с
`with_gvisor`, `lx-build` ядра, `go vet` и тесты `dns`/`common`/`route`.

### 🧰 gofmt-долг закрыт

`route/rule/rule_item_rule_set_test.go` пришёл неотформатированным с мержем
`4cc0aec9d` и валил бы gofmt-гейт `lx-ci` при следующем касании; по всему
дереву (вне `build/` и `submodules/`) других нарушений нет.

#### v1.14.0-lx.27-rc.4

> Номер rc.3 пропущен: строка `1.14.0-lx.27-rc.3-dev` уже ушла в полевые
> тест-сборки этого фикса (mac-лаунчер), релиз режется следующим номером,
> чтобы версии полевых бинарей не пересекались с тегом.

### 🐛 Detour-фриз добит: провал поднятия XHTTP-стрима рвёт upload-пайп, живой стрим больше не умирает по дедлайну дайла

([SPEC 072](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md))

Полевой дамп (LxBox 2.20.11, ядро lx.27-rc.2 — **с обоими фиксами rc.2 в
бинаре**): 38 минут фриза, весь трафик мёртв включая direct, пинг-тест
приложения проходит (probe — отдельная сессия), лечит только force-stop.
Цепочка: `ClientBind.connect()` → VLESS-заголовок в upload-пайп stream-one →
`io.Pipe.Write` навсегда, под `connAccess` **и** `device.net.RLock`
(`Peer.SendBuffers`) → BindClose → `pauseOpAccess` → `Endpoint.Close` →
`endpoint.Manager.Close` (reload завис, gomobile-поток занят) → 11 горутин в
`Manager.Get` — все `DetourDialer.init` процесса. Дедлайн дайла SPEC 071 был
взведён и не спас — **две дыры**: (1) сторож SPEC 050 снимается по `created`,
а error-ветки raise (`setupReader(nil, err)`) закрывали `created` при провале
RoundTrip **не разбивая пайп** — x/net http2 не закрывает `req.Body` при
отказе до принятия запроса, Write остаётся навечно без сторожа и без
дедлайна; (2) запросы XHTTP ездили на dial-контексте, и 15-секундный дедлайн,
истекая, **рвал живой стрим** (http2 привязывает жизнь стрима к ctx запроса)
— каждый здоровый detour-conn пересоздавался каждые 15 с, заново бросая кости
на дыру (1) против гниющего XMUX-пула (застрявший дайл дампа — ~4-я генерация
через минуту после старта). Фикс в форк-нативном `transport/v2rayxhttp`
(маркер `// lx: SPEC 072`): все error-ветки raise идут через `fail()` —
разбивает upload-пайп с читающей половины (Write выходит с причиной), гасит
conn-scoped контекст и возвращает XMUX-слот (early-дайлы sing-vmess роняют
conn вместе с ошибкой — на `Close` рассчитывать нельзя); `uploadFailed`
stream-up тоже рвёт с читающей половины (писатель видит причину, а не голый
`ErrClosedPipe`); запросы всех трёх режимов ездят на `WithCancel(transport
ctx)` — dial-контекст ограничивает **только** raise через сторож 050
(stream-up сторож получил впервые), `Close`/`fail` гасят подвисшие RoundTrip'ы
(раньше pending download packet-up тёк до конца процесса); каждый upload-POST
packet-up ограничен собственным бюджетом `C.TCPTimeout` (заклинивший пул
стоит один пост, а не вечный Write в WG-send-пути). Red/green: 8 юнитов
`raise_failure_test.go` (red-прогон на базе rc.2 зафиксирован в SPEC); сюиты
`-race` v2rayxhttp / transport/wireguard (with_gvisor) / protocol/wireguard
зелёные. Полевая валидация — на клиенте дампа.

**Слияние задач:** SPEC 070 и SPEC 071 поглощены задачей
[072](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md)
— единый владелец семьи полевых отказов жизненного цикла WG-эндпоинта
(решение владельца, §3.2). Их механизмы живут без изменений под прежними
маркерами `// lx: SPEC 070` / `// lx: SPEC 071`; каталоги 070/071 —
указатели, реестр HOTFIXES (P11/P12) переведён на 072.

### 🧰 Upstream-синк: точечный хвост stable

Дрейф к `upstream/stable` разобран по subject (merge-base уехал после их
force-push). Черри-пиком взят только `Fix version quoting in Apple project
update script`. `Fix DHCP DNS server search domain handling` **уже в дереве**:
stable несёт его в старой до-async-DNS форме, а testing-линия (наша
контент-база beta.15) вобрала его и переоформила рефактором `Async DNS` —
`dns/transport/dhcp/` у нас байт-в-байт равен tip `upstream/testing`
(пробный перенос stable-формы регрессировал `ExchangeAsync` и валил сборку —
откатан). **Осознанно отложен** `Bump version` (beta.17): он двигает пины
сабмодулей `clients/android`/`clients/apple` на коммиты, которые из их
репозиториев ещё не фетчатся (checkout с сабмодулями ломался бы), а нашим
артефактам не даёт ничего; возьмём со следующим полноценным синком.

#### v1.14.0-lx.27-rc.2

### 🐛 Стоп во время старта больше не роняет процесс (гонка Start×Close на WG-эндпоинте)

([SPEC 070](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/070-WG_START_CLOSE_RACE_CRASH/SPEC.md))

Полевой краш-дамп (LxBox, ядро lx.27-rc.1, 1755 аутбаундов, 18 WG-эндпоинтов):
пользователь нажал «стоп», пока тяжёлый профиль ещё стартовал — SIGSEGV в
`transport/wireguard.(*Endpoint).Start`, в дампе виден второй goroutine,
закрывающий **тот же** `Box`. Демон сознательно отпускает `serviceAccess` на
время `instance.Start()` (чтобы стоп мог перебить медленный старт), но ниже
никто не сериализует `Box.Start` против `Box.Close`; наш SPEC 020
`closeTunDevice` nil-assign делал окно детерминированно фатальным. Фикс —
гейт в `protocol/wireguard.Start` + идемпотентность `Box.Close` на
примитивах SPEC 020/030 (`resumeMu`, `closing`); транспортный слой и
сабмодули не тронуты. Red/green юниты + race-smoke (darwin `-race`) зелёные;
стенд `lx-test/startclose`. Полевая валидация — на клиенте жалобы.

### 🐛 Мёртвый detour-узел больше не замораживает сетевую машинерию процесса

([SPEC 071](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/071-WG_BIND_DIAL_PAUSE_DEADLOCK/SPEC.md))

Полевой дамп (LxBox 2.20.7, ядро lx.25-rc.3): 557 goroutine, 12 из них на
мьютексах до **54 минут**, обработка смены сети мертва, финал — force-stop
на RSS 497 МБ. Профиль гонит WG через `detour` (VLESS+XHTTP stream-one);
узел полуживой — TCP принимает, дальше тишина. Кольцо: `ClientBind.connect()`
диалит detour под `connAccess` на **безлимитном** контексте → заголовок VLESS
пишется в upload-pipe stream-one, который никто не читает → `io.Pipe.Write`
навсегда; `Close` не может пробиться (bind-close пути ждут выхода receive-
goroutine под `device.net.Lock`), а pause-manager `sing` гоняет колбэки под
своим локом — замерзает весь процесс. Два фикса: bounded dial в
`client_bind.go` (дедлайн ломает пайп через watchdog SPEC 050) + detached-
применение pause в `transport/wireguard/endpoint.go`. Зависимость `sing`,
wireguard-go и демон не тронуты; юниты red/green + `-race` зелёные. Полевая
валидация — на клиенте жалобы.

### 🔄 Upstream-синк: ветка догнала `v1.14.0-beta.15`

**Upstream-синк 2026-08-17: ветка догнала `v1.14.0-beta.15`.** Формально
«240 коммитов дрейфа» от merge-base, реально новых — 13 (сверка по subject,
runbook §2); взяты cherry-pick'ом, 12 из 13. `077a4a0b5` (DHCP search domain)
пропущен осознанно: его содержимое полностью поглощено более поздним
`14cca98e1` — после синка `dns/transport/dhcp/` совпадает с beta.15
байт-в-байт. Заметное: хвост DNS-партиционирования (`route/network_environment*`
под darwin/linux/windows + stub — первая половина, партиционирование по
сигнатуре интерфейса, была взята в lx.24-rc.2); search-domain-фиксы DNS-транспортов
(`transport.ExchangeNames`); новые DNS-правила `query_client_subnet` /
`query_dnssec` + опция `remove_client_subnet`; `daemon`: полные initial
snapshots в status-подписках и троттлинг push'ей групп/аутбаундов;
Taildrop (+`SubscribeNotifications` RPC); `api` command; Tailscale
`listen_port`; фикс двойного закрытия fd при неудачном bind netlink-сокета.
Конфликты только позиционные: lx-блоки в `started_service.proto` соседствуют
с новым `SubscribeNotifications` — proto смержен, pb.go регенерированы
`lx-proto` (плагины пинованы, SPEC 014 §3.5); в `go.sum` не взяты строки
sing-tun / wireguard-go / gvisor — они у нас local-path replace на
форк-сабмодули, сабмодули дрейфа не имеют (проверено: go.mod-бампы синка их
не трогают). Пины go.mod: netlink `20260814`, tailscale `mod.3`, sing-usbip
`20260813`, + runewidth/term/uax29 (новые depы `api` command).
**`go.version` бампнут go1.26.5 → go1.26.6** вслед за апстримом — патч-бамп,
политика SPEC 044 про минорку не затронута; при следующем AAR — обычный
девайс-прогон. Маркер SPEC 053 (minClientVer 26.3.27 в `reality_client.go`)
сверен — синк tls не трогал. Локально зелёные: полный LX_TAGS-бинарь
(`lx-build`), тесты daemon/dns/route/option/wireguard/tailscale/dialer под
LX_TAGS, gofmt чист. Остаточный дифф с beta.15 — только lx-слой
(в `dns/client.go` дивергенция сжалась 44→6 строк).

#### v1.14.0-lx.27-rc.1

### 🐛 Windows: WG/AWG-узлы мертвы со старта, если у адаптера снята галка IPv6

([SPEC 069](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/069-WG_V6_BIND_FAIL_KILLS_V4/SPEC.md))

Жалоба с поля: **все** WG/AWG-эндпоинты мертвы с первой секунды, хендшейк не
уходит вообще. В логе на каждое сетевое событие пара строк:

```
ERROR endpoint/wireguard[…]: unable to update bind: listen udp6 [::]:55449: An invalid argument was supplied.
ERROR endpoint/wireguard[…]: peer(…) - failed to send handshake initiation: address family not supported by protocol
```

Профиль чисто IPv4, `::/0` в `allowed_ips` ни при чём (проверено снятием).

**Корень.** Ядро привязывает каждый слушающий сокет к default-интерфейсу; для
v6-сокета на Windows это `setsockopt(IPV6_UNICAST_IF, ifIndex)`. Если у
default-адаптера снята галка «IP версии 6» — типовое состояние корпоративных
и «оптимизированных» машин, адаптера просто нет в v6-стеке — вызов
возвращает WSAEINVAL. Апстримный `StdNetBind.Open` считал допустимым провал
одного семейства только при `EAFNOSUPPORT`, поэтому на WSAEINVAL **закрывал
уже открытый живой v4-сокет** и валил `Open` целиком. А `BindUpdate` к этому
моменту успел закрыть старый bind — устройство оставалось вообще без сокетов,
и каждая отправка отвечала «address family not supported by protocol».
Инфо-лог с машины жалобы показал, что первое же route-событие приходит **до**
`sing-box started`: туннель мёртв с нулевой секунды.

**Фикс.** Провал bind одного семейства теперь означает «этого семейства здесь
нет», а не «отменяем весь bind»: соседний сокет остаётся жить. На Windows к
`EAFNOSUPPORT` добавлены `WSAEINVAL` и `WSAEADDRNOTAVAIL`; на остальных
платформах поведение апстрима сохранено байт-в-байт — `EINVAL` на юниксах
по-прежнему валит `Open`. Семейства обрабатываются симметрично, так что
хост без v4 деградирует так же.

Попутно два латентных дефекта в том же блоке: безусловный `v4conn.Close()`
в ветках ошибок (nil-деref, если v4 сам отвалился по деградации) и потеря
порта выжившего сокета — провальный v6-листен возвращает `(nil, 0, err)`, и
множественное присваивание затирало порт нулём.

На машине жалобы туннель поднимается на IPv4 без каких-либо правок настроек
адаптера. Правка только в форке `wireguard-go` (`conn/`), ядро не тронуто.

#### v1.14.0-lx.26

### 🔧 `lxd`: демон отделён от командных RPC — новый build-tag `with_lxd`

([SPEC 067](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/067-LXD_BUILD_TAG_SPLIT/SPEC.md))

Один тег `with_lx_command` гейтил две несвязанные вещи: расширения командного
протокола libbox (`URLTestOutbound`, `GetRules`, `GetGroups` — ими живёт
LxBox) и весь пакет `lxd/` с подкомандой демона. Собрать одно без другого
было нельзя.

Теперь тега два и они независимы:

* **`with_lx_command`** — RPC в `daemon/` и `experimental/libbox/`; десктоп,
  Win7, AAR;
* **`with_lxd`** — пакет `lxd/` и подкоманда `sing-box lxd`.

**Legacy-сборка Windows 7 теперь идёт без демона**: служба Windows там не
реализована и ротация лога тоже, так что подкоманда существовала бы без того,
что делает её демоном. `with_lx_command` при этом остаётся — снять его значило
бы увести RPC в стаб и молча сломать в LxBox тест задержки по узлам и экран
правил. Ради 0.3 МБ из 46.

AAR не затронут: `gomobile` собирает `experimental/libbox`, который `lxd/` не
импортирует — демона там не было и раньше. Правки только в строках
`//go:build`, `LX_TAGS` и наборах тегов CI/релиза; логика не менялась.

### 🆕 `lxd`: телеметрия хоста — CPU, память, температура, диски, интерфейсы

([SPEC 068](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/068-LXD_HOST_TELEMETRY/SPEC.md))

`/admin/memory` показывает память **процесса**. Когда роутер с 256 МБ ОЗУ
начинает тормозить, этого мало: непонятно, упёрлось ли железо и во что.
Оператор шёл по SSH за `top`, `free` и `df` — то есть ровно за тем, ради чего
заводилась плоскость наблюдаемости.

Две новые ручки описывают **машину**, а не процесс:

* **`GET /admin/host`** — `cpu` (общий процент и разрез **по ядрам**: одно ядро
  в полке при трёх свободных среднее прячет, а на четырёхъядерном MT7981 это
  типовой диагноз; плюс `load_1/5/15`), `memory`, `thermal`, `disk`, `fd`.
* **`GET /admin/host/interfaces`** — все интерфейсы с сырыми счётчиками и
  посчитанными скоростями.

Решения, которые видно в ответе:

* **Процент памяти считается от `available`, а не от `free`.** Роутер держит
  почти всю память в page cache; процент от `free` кричал бы «занято» при
  реально свободных 120 МБ.
* **Температура — массивом `zones[]`** плюс `max_celsius` для одного
  индикатора: датчиков на плате обычно несколько (CPU, радиочасть). Нет
  датчиков вовсе → `thermal: null`, а не пустой массив.
* **Диски — массивом `mounts[]`**, у каждого `read_only` и `holds_state_dir`.
  Раздел со state-dir ищется **по device id, а не по префиксу пути**: на
  OpenWrt `/etc` — симлинк в оверлей, и сравнение строк дало бы неверный
  ответ. `max_used_percent` **игнорирует read-only ФС** — корень squashfs
  вечно занят на 100%, а всегда красный индикатор перестают замечать.
* **Дескрипторы в двух уровнях** — свои и системные: упереться в лимит демона
  и в лимит машины это разные баги с одинаковым симптомом «новые соединения не
  открываются».

**Проценты и скорости — дельты, и это видно.** `/proc/stat` и `/proc/net/dev`
отдают монотонные счётчики, поэтому демон хранит предыдущий замер и рядом с
производным числом отдаёт `interval_seconds`: 12.4% за пять секунд и за час
значат разное. Первый запрос после старта честно отдаёт `usage_percent: null`
и `interval_seconds: 0` — считать не от чего, а ноль читался бы как
«простаивает». Сырые счётчики отдаются **вместе** с производными: счётчик
переживает разрывы, скорость на них врёт.

**Вне Linux форма ответа та же, недоступные поля — `null`** (решение
владельца: берём что можем). На macOS работают память, диски, дескрипторы,
load average и счётчики интерфейсов; термодатчики требуют CGO, проценты CPU —
Mach API, поэтому там `null`. Клиент проверяет `null`, а не разбирает поле
`os`.

⚠️ **Отдельная находка про macOS.** `NET_RT_IFLIST2` отдаёт структуру с именем
`if_data64`, но байтовые счётчики в ней фактически **32-битные** и
переполняются каждые 4 ГБ — проверено на живой машине, значения сходятся с
`netstat -ib` по модулю 2³² (пакеты и ошибки при этом честные 64-битные).
Демон доращивает их до 64 бит по дельтам, иначе график обрывался бы в ноль
каждые несколько часов. На Linux этого не нужно — `/proc/net/dev` 64-битный.

Кеш 500 мс: короче, чем у справочника клиентов (данные меняются постоянно),
длиннее, чем у `/admin/memory` (чтение дюжины файлов дороже одного
`ReadMemStats`). Позволяет UI опрашивать на 2 Гц и каждый раз получать свежий
снимок; на загрузку CPU роутера при такой частоте это не влияет (замерено на
MT7981). Проценты при этом остаются дельтами между замерами, и более короткое
окно делает их шумнее, а не точнее. Ручки за тем же mTLS-пином и отвечают независимо от того,
поднято ли ядро. Ядро не правится.

Проверено на живом OpenWrt-роутере (MT7981) — реальные термозоны,
`/proc/stat` на четырёх ядрах и `squashfs`-корень отработали.

#### v1.14.0-lx.25-rc.10

### 🆕 `lxd`: справочник «IP → устройство» — соединения подписываются именем клиента

([SPEC 066](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/066-LXD_CLIENT_IDENTITY/SPEC.md))

Сетевой инспектор показывал `Client: 192.168.20.238:50558`. Для одного
устройства это читается, для домашней сети из пятнадцати — нет: чтобы понять,
кто качает 4.9 МБ с чужого адреса, приходилось держать карту адресов в голове
или лезть в веб-морду роутера. `Connection.processInfo` тут бесполезен по
построению — он ищет процесс в **локальной** таблице сокетов, а соединение
Wi-Fi-клиента открыл вообще другой хост.

Демон крутится на самом роутере, а роутер — та сторона, которая имена и
раздаёт. Новая ручка `GET /admin/clients-info` отдаёт карту `IP → {name, mac,
ssid, iface, port, source}`. Это **справочник, а не поле в соединении**: имена
меняются раз в часы, поэтому клиент тянет карту раз в минуту и сшивает её сам,
а горячий путь и `SubscribeConnections` не трогаются вовсе.

Карту наполняют пять провайдеров, каждый следующий уточняет предыдущего
(`lease` → `arp` → `bridge` → `wireless` → `label`):

* **`lease`** — DHCP-лизы через апстримный `route.ReloadLeaseFiles`: `name` +
  `mac`. Шесть форматов (dnsmasq/odhcpd, ISC, Kea v4/v6, bootpd) уже разобраны
  ядром — писать свой парсер не пришлось. Пути переопределяются в `daemon.json`
  полем `dhcp_lease_files`.
* **`arp`** — `/proc/net/arp`: `mac` + интерфейс. Закрывает клиентов со
  статическим IP, которых в лизах нет вообще: устройство перестаёт быть голым
  адресом, даже если DHCP-имени у него взять неоткуда. Неполные записи
  (`Flags 0x0`, MAC из нулей) отфильтрованы.
* **`bridge`** — `bridge fdb show`: физический порт (`lan2`) проводного
  клиента. `iface` говорит «в каком сегменте», `port` — «в какую розетку».
  Адреса самого моста (`permanent`/`self`) отброшены.
* **`wireless`** — `ubus call hostapd.*` на OpenWrt: `ssid` и точный
  AP-интерфейс. Идёт после `arp` намеренно — ARP даёт мост `br-lan`, а ubus
  уточняет до `phy0-ap1`. Интерфейсы перечисляются через `ubus list`, а не
  хардкодятся: имя UCI-секции (`wireless.vpn_2g`) не равно имени интерфейса.
* **`label`** — метки оператора через `PUT`/`DELETE
  /admin/clients-info/labels/{ключ}`, ключ = IP или MAC. Последнее слово.
  ⚠️ Метка по MAC переезжает с устройством, но телефоны рандомизируют MAC при
  переподключении — для них надёжнее метка по IP при фиксированной резервации.

Поле `source` есть в каждой записи намеренно: когда устройство теряет имя,
вопрос всегда один — какой источник замолчал. Пустое поле — валидное
состояние, а не ошибка.

**Ядро не тронуто ни строкой** — ни `route/`, ни `adapter/`, ни proto: всё на
уровне внешней обвязки, единственное заимствование — вызов
`route.ReloadLeaseFiles` как библиотечной функции. Платформенные провайдеры
разведены build-тегами (тот же приём, что `currentRSS()`/`peakRSS()` в
SPEC 065); вне Linux они молча возвращают «нечего сказать», и ручка продолжает
отвечать лизами и метками. Отсутствие `ubus`/`bridge` в системе — то же самое
состояние, а не ошибка. Карта кешируется на 60 секунд, запись метки видна
сразу. Маршруты — за тем же mTLS-пином, что вся плоскость наблюдаемости, и
намеренно **не** на операторском loopback-пути: смысл в том, чтобы лаунчер
читал их удалённо. Ручка отвечает и когда ядро не поднято — она от инстанса не
зависит.

#### v1.14.0-lx.25-rc.9

### 🆕 `lxd`: демон научился рассказывать о себе — логи, память, трафик, профили

([SPEC 065](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/065-LXD_OBSERVABILITY_PLANE/SPEC.md))

Демон существует ради того, чтобы управление жило, когда лежит data-plane.
Но когда плохо становилось самому демону, посмотреть на него снаружи было
нечем: gRPC-поток `SubscribeLog` несёт лог **ядра**, а строки самого демона
(`lxd: …`, ошибки бутстрапа, паники) уходят в `lxd.log` на удалённом хосте —
и `GET /admin/info` сообщал лишь путь к этому файлу, для лаунчера бесполезный.
Профилировать можно было только через `experimental.debug.listen` ядра, то
есть подняв второй порт вообще без аутентификации.

Девять маршрутов на том же mTLS-порту и за тем же пином клиентского
сертификата:

| Маршрут | Что даёт |
|---|---|
| `GET /admin/memory` | heap/stack/sys, горутины, GC и **два** RSS |
| `GET /admin/stats` | uptime ядра, `uplink_total`/`downlink_total`, соединения |
| `GET /admin/logs?tail=N` | хвост `lxd.log` — лог самого демона |
| `GET /admin/pprof` | список профилей со счётчиками и `enabled` |
| `GET /admin/pprof/{name}` | `heap`, `allocs`, `goroutine`, `threadcreate`, `block`, `mutex` |
| `GET /admin/pprof/profile?seconds=N` | CPU-профиль (пишет N секунд) |
| `GET /admin/pprof/trace?seconds=N` | runtime trace |
| `POST /admin/pprof/block?rate=N` | вкл/выкл block-профилирования |
| `POST /admin/pprof/mutex?fraction=N` | вкл/выкл mutex-профилирования |

**Отдельного debug-порта нет по построению.** Ручки нужны именно удалённо —
лаунчер снимает профиль с сервера, который странно себя ведёт, — поэтому они
идут за клиентским сертификатом. Это честный размен: тот же сертификат уже
разрешает `POST /admin/apply`, то есть исполнение произвольного конфига в
контексте демона.

**RSS отдаётся двумя числами.** Ядро отдаёт `ru_maxrss` под именем `rss` — а
это пик за жизнь процесса, он не убывает, и на графике утечка неотличима от
разового всплеска. Теперь `rss_current_bytes` (на linux — `/proc/self/statm`,
без cgo) и `rss_peak_bytes` раздельно, с единицей в имени поля. На darwin
честный `task_info` требует cgo, поэтому там `-1`.

**`/admin/stats` без ядра отдаёт `null` во всех полях и код 200**, а не 503:
ручка описывает демон и обязана отвечать в `idle` и `fatal` — ровно в тех
состояниях, когда клиент и хочет узнать, что ядра нет.

**Профили-снимки ничего не «начинают».** `heap`, `allocs`, `goroutine`,
`threadcreate` рантайм ведёт непрерывно с самого старта, поэтому GET
возвращается за миллисекунды и уже покрывает всю жизнь процесса; фонового
налога от доступности этих маршрутов нет. Записывают только `profile` и
`trace` (потолок 120 секунд, параллельный запрос — 409). `block` и `mutex`
устроены как снимки, но копятся лишь после явного включения — их учёт
ставит метку времени на каждой операции синхронизации.

Прогон на macOS: `go tool pprof` открыл и heap, и CPU-профиль с настоящей
символизацией; `block` после `rate=1000` набрал 15 событий при нуле до
включения; после `/admin/stop` статистика стала `null`, а `/admin/memory`
продолжил отвечать.

#### v1.14.0-lx.25-rc.8

### 🔧 `selector`: разрыв соединений теперь работает и во вложенных группах

([SPEC 064](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md))

Доработка фикса из rc.7. Тот вариант чинил разрыв только там, где селектор
диалит узел сам, — а если выбранный член группы обрабатывает соединение
целиком (вложенный `selector`/`urltest`, `dns`-аутбаунд), соединение уходило
ему напрямую и в список на разрыв не попадало.

Регистрация переехала на входящее соединение и делается **до** развилки:

```go
conn = s.interruptGroup.NewConn(conn, true)             // TCP
conn = s.interruptGroup.NewSingPacketConn(conn, true)   // UDP
```

Рвётся входящая сторона — копирующий цикл валится и закрывает исходящий
сокет, соединение умирает целиком. Теперь покрыты обе ветки. Заодно ветка
обычных узлов вернулась к апстримной форме — меньше расхождение с upstream
на мержах.

Подход взят из апстримного [PR #4285](https://github.com/SagerNet/sing-box/pull/4285);
в `common/interrupt` портирован `SingPacketConn` для UDP-пути. Сам баг в
апстриме заведён дважды —
[#4281](https://github.com/SagerNet/sing-box/issues/4281) (с полным разбором
корня) и [#2625](https://github.com/SagerNet/sing-box/issues/2625).

Ветка вложенных групп закреплена отдельным тестом: на варианте из rc.7 он
падает, на текущем проходит.

#### v1.14.0-lx.25-rc.7

### 🐞 `selector`: `interrupt_exist_connections` не разрывал соединения

([SPEC 064](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md))

Переключение узла внутри группы `selector` не рвало активные соединения:
трафик продолжал идти через прежний узел до собственного таймаута, и помогал
только Stop/Start ядра. Опция `interrupt_exist_connections: true` при этом была
выставлена — то есть настройка была верной, а эффекта не давала.

Корень — в том, кого `Selector.NewConnection` передавал в `ConnectionManager`
как диалер:

```go
s.connection.NewConnection(ctx, selected, conn, metadata, onClose)  // было
s.connection.NewConnection(ctx, s, conn, metadata, onClose)         // стало
```

`ConnectionManager` вызывает `DialContext` у переданного объекта. С `selected`
диал шёл сразу у конечного узла, **минуя `Selector.DialContext`** — а это
единственное место, где сокет регистрируется в `interruptGroup`. Список
оставался пустым, и `SelectOutbound` дёргал `Interrupt()` по пустоте.

Адресат трафика от правки не меняется: `Selector.DialContext` сам диалит через
выбранный узел, просто по пути кладёт сокет в свою группу. Симметрично
починен UDP-путь.

Дефект **апстримный** и ровесник самой фичи (`c320be75a`, 2023-09-15, вошёл в
v1.10.0); присутствует в актуальном `upstream/stable`. У `urltest` его нет —
там передаётся `s`, поэтому автопереключение узла соединения рвало исправно.
Этим и объясняется, почему баг так долго жил незамеченным: самый ходовой тип
группы вёл себя корректно, а клиенты компенсировали разрыв на своём уровне.

Ветка `ConnectionHandler` намеренно не тронута — в неё попадают только
вложенные группы и `protocol/dns`, чей `DialContext` возвращает `os.ErrInvalid`.

Регресс закреплён тестом на живом `Selector` с настоящим
`route.ConnectionManager`: до фикса переключение оставляло сокет живым, после —
разрывает.

#### v1.14.0-lx.25-rc.6

### 🆕 `lxd`: доставка файловых ресурсов конфига через admin-REST

([SPEC 063](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/063-LXD_RESOURCE_STORE/SPEC.md))

Демон `sing-box lxd` умел принимать только тело конфига (`POST /admin/apply`).
Но конфиг сплошь и рядом ссылается на внешние файлы — скомпилированные rule-set
`.srs` (`type: local`, `format: binary`), geo-базы, — и класть их было некуда:
только мимо демона (scp/rsync прямо в `state_dir`). Теперь у admin-плоскости
есть вторая полезная нагрузка — REST-CRUD над ресурсами:

| Эндпоинт | Семантика |
|---|---|
| `GET /admin/resources` | список `[{name, sha256, size, path}]` |
| `PUT /admin/resources/{name}` | залить/перезаписать; тело = сырые байты |
| `GET /admin/resources/{name}` | метадата по имени (хеш для diff) |
| `GET /admin/resources/{name}/content` | скачать байты |
| `DELETE /admin/resources/{name}` | удалить |

Адресация по имени (стабильный путь для конфига), `sha256` в каждом ответе —
версия, по которой клиент дифает локально и льёт только изменённое. `path`
абсолютный, равен `<info.state_dir>/resources/<name>` (совпадает с
`GET /admin/info` → `state_dir`), — копируется в конфиг как `type: local, path:`
без сборки пути на клиенте.

Клиентский цикл: `GET /admin/resources` → сравнил хеши → `PUT` изменённого →
`POST /admin/apply` с конфигом, где `path` ссылается на ресурс.

**Гуард целостности:** `PUT`/`DELETE` для имени, на которое ссылается активный
или last-good конфиг, отбиваются `409` — иначе перезапись/удаление сделали бы
rollback ядра дырявым (текст конфига откатился бы, а `.srs` под именем уже
другой). Сначала `apply` конфига без ссылки, потом трогать файл. Запись атомарна
(tmp + fsync + rename), имена санитизируются (обход каталога → `400`). Стор,
маршруты и гуард целиком под сборочным тегом `with_lx_command`.

#### v1.14.0-lx.25-rc.5

### ⚠️ `masque`: поле выбора HTTP-версии переименовано — `transport` → `vhttp`

([SPEC 062](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/062-MASQUE_CONFIG_SCHEMA_MIGRATION/SPEC.md))

**Касается только тех, кто успел перейти на `transport` из rc.4.** Поле прожило
один пререлиз и убрано без алиаса:

| в rc.4 | в rc.5 |
|---|---|
| `transport: "h3"` / `"h2"` | `vhttp: "h3"` / `"h2"` |

Причина: у vless/trojan/vmess `transport` — это ключ V2Ray-транспорта, и он
**объект** (`{"type":"ws"}`), а не строка. Одно имя с двумя разными смыслами и
типами — ровно та путаница, которую SPEC 062 и убирает.

Старое `network: "h3"` / `"h2"` работает как прежде — оно deprecated до
`v1.14.0-lx.30`, как и было объявлено в rc.4. Таблица миграции остальных полей
(`sni`, `skip_cert_verify`, `fragment*` → блок `tls`) — в секции rc.4 ниже, она
не изменилась.

### Режим urltest-группы виден клиенту — новое поле `Group.mode`

([SPEC 019](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/019-URLTEST_MODE_STICKY/SPEC.md))

Клиент не мог отличить сбалансированную группу от обычной: `type` у обоих
режимов — `"urltest"`. Единственным признаком был побочный эффект `GetPool`
(пустой список слотов = не `round_robin`), а этот RPC живёт за сборочным тегом
`with_lx_command` — в сборке без него режим не определялся никак.

Теперь `Group` несёт поле `mode`:

| значение | что означает |
|---|---|
| `least_test` | обычный urltest — есть один выбранный узел, его отдаёт `selected` |
| `round_robin` | балансировка по пулу — полное состояние даёт `GetPool` |
| пусто | не urltest-группа (например `selector`) |

Поле приходит и в `GetGroups`, и в `SubscribeGroups`, **в любой сборке** —
в отличие от `GetPool`.

**Уточнена семантика `selected`** (поведение не менялось, только описание): в
режиме `round_robin` это не «текущий узел», а тег последнего фактически
выбранного — след предыдущего соединения, а не состояние группы. Следующий
дозвон уйдёт в другой слот. Источник правды для `round_robin` — `GetPool`.

Изменение аддитивное: старые клиенты нового поля просто не видят.

#### v1.14.0-lx.25-rc.4

### ⚠️ Схема конфига `masque` изменилась — старые поля объявлены устаревшими

([SPEC 062](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/062-MASQUE_CONFIG_SCHEMA_MIGRATION/SPEC.md))

**Действий прямо сейчас не требуется: старые конфиги работают без изменений.**
Каждый аутбаунд со старыми полями выводит одно предупреждение в лог.
**Поддержка старых имён будет снята в `v1.14.0-lx.30`** — до этого момента
конфиги нужно перевести на новую схему.

| устарело | заменить на |
|---|---|
| `network: "h3"` / `"h2"` | `vhttp: "h3"` / `"h2"` |
| `sni: "…"` | `tls.server_name: "…"` |
| `skip_cert_verify: true` | `tls.insecure: true` |
| `fragment: true` | `tls.fragment: true` |
| `record_fragment: true` | `tls.record_fragment: true` |
| `fragment_fallback_delay: "…"` | `tls.fragment_fallback_delay: "…"` |

Остальные поля (`server`, `server_port`, `profile`, `private_key`,
`public_key`, `ip`, `ipv6`, `uri`, `mtu`, `idle_timeout`,
`keep_alive_period`, `network_list`) **не менялись**.

```jsonc
// было
{ "type": "masque", "network": "h2", "sni": "example.com", "record_fragment": true }

// стало
{ "type": "masque", "vhttp": "h2",
  "tls": { "server_name": "example.com", "record_fragment": true } }
```

Одно и то же, заданное старым и новым именем с разными значениями — ошибка
запуска с указанием обоих полей. Одинаковые значения конфликтом не считаются.

**Добавлено:** `tls.disable_sni` — ClientHello без SNI (пустой `sni` этого не
давал, он подменялся дефолтом профиля).

**Предупреждение вместо молчания** на неподдерживаемых полях: `tls.alpn`,
`tls.ech`, `tls.reality`, `tls.kernel_*`, фрагментация при `vhttp: h3`.

### Изменился дефолтный SNI у masque

([SPEC 021](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md))

Было `consumer-masque.cloudflareclient.com`, стало `www.cloudflare.com`.
Затрагивает конфиги **без** явного `sni` / `tls.server_name`; заданное в
конфиге значение по-прежнему приоритетнее.

Причина: с прежним именем h3-туннель к эндпоинту не поднимается на российских
каналах (замерено на двух независимых). Cloudflare это имя не проверяет —
эндпоинт аутентифицируется пиннингом ECDSA-ключа.

### Диагностика

`masque: CONNECT-IP timed out` вместо `dial connect-ip: read response: http3:
parsing frame failed: timeout: no recent network activity`, когда эндпоинт
принял QUIC, но не ответил на CONNECT-IP. Исходная причина сохраняется в
цепочке ошибок.

#### v1.14.0-lx.25-rc.3

**Цепочки «через detour» перестали молча не подниматься.** ([SPEC 060](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md),
[SPEC 021](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md))

Клиенты прячут свои VLESS-серверы за WARP и собирают `MASQUE detour VLESS`.
На части узлов такая связка не работала вообще: висела ~15 секунд и падала с
`tls handshake: EOF`, по которому причину не восстановить.

Причина не в ядре и не в SNI. Нижнее плечо пересылает наш ClientHello **от
своего имени**, и если PMTU за этим плечом меньше размера ClientHello —
пакет теряется молча: ICMP «fragmentation needed» до клиента не доходит.
Замер на живых узлах даёт чистый порог по размеру: 1488 B проходит, 1502 B
исчезает, и порог принадлежит пути за плечом, а не протоколу — на других
узлах те же самые байты идут насквозь. Воспроизводится голым `curl` без
sing-box. Бьёт не только по MASQUE: `VLESS detour VLESS` через тот же узел
падает так же.

Лечится фрагментацией первой TLS-записи — механизм в ядре уже был
(`fragment` / `record_fragment`), не хватало умолчания: пользователь не
обязан знать про чужую дыру и помнить, где ставить флаг. Теперь
`record_fragment` включается сам, когда outbound диалит через `detour`.
Точка одна — `NewClientWithOptions`, до выбора движка, поэтому STD, uTLS и
REALITY получают одинаковый дефолт. Явный выбор пользователя всегда сильнее;
`fragment: true` не апгрейдится добавлением record-split. Цена ограничена
хендшейком: переписывается только первая запись, установившийся поток не
трогается. Прямой путь (без `detour`) не затронут.

Отдельно MASQUE перестал быть единственным outbound'ом мимо `common/tls`:
на h2 он вёл TLS голым `crypto/tls.Client` ради pinning'а по ECDSA-ключу
endpoint'а — и вместе с этим не получал ничего из общего слоя. Теперь h2
ходит через общий клиент, pinning переносится поверх него и проверен как
юнит-тестом, так и живым коннектом с подменённым `public_key`. h3 не тронут:
QUIC не несёт TLS поверх TCP.

Проверено на живых узлах подписки: матрица 19 узлов × 2 направления
(`MASQUE-over-VLESS` и `VLESS-over-MASQUE`) — **38/38** по связности и
**38/38** по сквозным 5 МБ, без единого обрыва. Узлы, на которых связка не
поднималась вообще, работают в обе стороны.

**Демон научился слушать несколько адресов сразу.** ([SPEC 055](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/055-LXD_DAEMON_SKELETON/SPEC.md))

`listen` в `daemon.json` принимал ровно один адрес, и это упиралось в
реальную конфигурацию: демон нужен и с LAN-интерфейса, и с loopback
одновременно. Единственным способом было `0.0.0.0` — а он заодно выставляет
наружу все прочие интерфейсы хоста, включая те, на которых слушать не
планировали.

Теперь `listen` принимает объектную форму с общим портом:

```jsonc
"listen": {"address": ["192.168.10.1", "127.0.0.1"], "port": 19091}
```

Обе плоскости (gRPC и admin REST) одинаково доступны на каждом адресе —
это один и тот же сервер на нескольких листенерах, а не копии. Строковая
форма `"127.0.0.1:19091"` не изменилась: существующие файлы править не
нужно, и одиночный адрес записывается обратно строкой, поэтому переустановка
службы не конвертирует конфиг оператора в другую форму.

Бинд — всё или ничего: если хоть один адрес не поднялся, демон падает с
ошибкой, называющей адрес, а уже открытые сокеты закрываются (иначе порт
остался бы занят до перезапуска процесса). Промежуточное состояние
«поднялся наполовину» — снаружи здоров, но недоступен ровно там, где нужен, —
исключено намеренно: молчаливая деградация управляющего канала стоит дороже
громкого отказа на старте.

Масок в адресах нет намеренно: ядро биндит один адрес, а не диапазон, поэтому
`192.168.10.1/32` отвергается с явным сообщением вместо того, чтобы позволить
файлу обещать несуществующее поведение. Ограничение того, *кому* можно
подключаться, остаётся задачей файрвола. Первый адрес в списке — рекламируемый:
на него указывают инвайты регистрации, локальный клиент и сводка установки.

**XMUX — переиспользование HTTP-соединений в XHTTP.** ([SPEC 059](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/059-XHTTP_XMUX/SPEC.md))

Секция `xmux` приходит в конфигах из подписок и до сих пор молча
отбрасывалась — поля не было в опциях. Теперь клиент держит пул
HTTP-соединений и ротирует их по правилам Xray: делает то, что задумал
владелец сервера, и не платит полный TCP+TLS(+REALITY) хендшейк на каждый
поток. Дефолты применяются и без секции.

#### v1.14.0-lx.25-rc.2

**XHTTP packet-up/stream-up больше не залипает на дайле — узел за CDN
поднимается.** ([SPEC 002](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/SPEC.md))

Xray отдаёт stream-down только после того, как сессия получила первый
uplink-пакет: до этого ему нечего слать вниз. Наш дайл поступал зеркально
наоборот — ждал ответ на download-GET, прежде чем отдать conn наверх и
позволить записать первый uplink. Ни одна сторона не двигалась. Обратный прокси
перед Xray добивал зависший запрос своим upstream-таймаутом, поэтому наружу
это выглядело как **`504 Gateway Timeout`**, а в LxBox — как узел с пингом
`-1`, который «в v2rayN и NekoBox+ работает».

Воспроизведено на проводе по пути VK-CDN → nginx → Xray. Эталонный клиент
([sing-box-extended](https://github.com/shtorm-7/sing-box-extended), ядро
NekoBox+) на том же конфиге живёт: его `OpenStream` возвращается сразу после
установки соединения (`httptrace` `GotConn`), а ответ разбирает асинхронно.
Сверка конфигов узла (наш `/config/running` против бинарного профиля
NekoBox+) показала совпадение всех полей транспорта — расходилось только
поведение дайла, `xmux` в отказе не участвовал.

`dialPacketUp` и `dialStreamUp` теперь отдают conn немедленно, а RoundTrip на
download уходит в горутину и позднее связывает reader (или ошибку). Отсюда —
поздняя привязка reader в `packetConn` и `splitConn`: `Read` синхронизируется
по `created`, `Close` не трогает несвязанный body, а read-дедлайн в
`packetConn` становится настоящим (был `os.ErrInvalid`) — при несвязанном
reader закрывать нечего, и только он способен освободить залипший `Read`.
Регрессионный тест поднимает h2c-сервер с тем же контрактом (stream-down
придерживается до первого uplink) и падает по старому коду в обоих режимах.

Полевая проверка на устройстве обязательна до промоута: узел за VK-CDN
(packet-up, session в header, seq в query) — туннель, URL-тест, реальный
трафик.

**Демон больше не жжёт ядро CPU при открытом лаунчере.**

Лаунчер передавал `Interval:1000`, имея в виду миллисекунды; сервер читал
значение как `time.Duration` (наносекунды) и заводил тикер на 1мкс —
`SubscribeConnections` строил снапшоты соединений на предельной частоте
(`top`: 0.5% ↔ 107%). Оба `Subscribe*`-стрима клампят интервал снизу до 200мс;
положительное значение ниже пола дополнительно оставляет warn в логе демона,
поэтому юнит-баг клиента виден сразу, а не через профилирование.

#### v1.14.0-lx.25-rc.1

**HTTP-пробник узла: `GetURLViaOutbound`** (SPEC 058, фича
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).
Диагностика узла до сих пор упиралась в одно число: `URLTestOutbound` отвечает
«жив, N мс» и выбрасывает тело. Класс жалоб «пинг есть, а сайты не открываются»,
«узел работает, но сервисы считают меня другой страной», «WARP подключён,
а `warp=off`» этим не закрывался — нужны данные ответа, а не факт ответа.

Новый unary-RPC делает GET по произвольному URL **через узел, адресуемый тегом**,
и возвращает тело. Адресация тегом принципиальна: проверить участника группы
можно, не переключая активный selector, то есть не ломая живые соединения
пользователя. Снаружи (обычным запросом с устройства) это невыразимо — там
проверяется маршрут целиком, а не конкретный узел.

Контракт:

- `GetURLViaOutbound(outboundTag, link, timeout, maxBytes, headers)` → объект
  `{Status, Content, Truncated, ContentType, RemoteAddr, ElapsedMs}`.
- Тег резолвится **в обоих менеджерах** — outbound и endpoint, — поэтому WG/AWG-узлы
  проверяются тем же вызовом (механика донора `URLTestOutbound`).
- **Не-2xx — результат, а не ошибка**: 403 от Cloudflare это говорящий ответ, ради
  которого пробник и существует. Ошибка (Variant B, в payload) остаётся за
  несостоявшимся обменом: неизвестный тег, отказ dial/TLS, таймаут.
- Тело клампится в ядре: `maxBytes=0` → 256 KiB, потолок 1 MiB, обрезка помечается
  `Truncated` — молчаливого усечения нет. Клиент не может заказать неограниченное
  чтение в память gomobile-процесса.
- `RemoteAddr` — адрес, достигнутый **изнутри туннеля** (куда резолвилась цель через
  этот узел), не exit-IP узла: exit-IP приносит тело.
- `ElapsedMs` включает чтение тела и **в историю urltest не пишется** — иначе фетч
  по произвольному URL испортил бы показания задержек узла в UI. Осознанное
  отличие от донора.
- Отмена — закрытием вызова (fetch дочерен per-call ctx), как у `URLTestOutbound`.
- HTTPS на Android работает через тот же certificate-store, что и собственный
  HTTP-клиент libbox: системный пул x509 в mobile-процессе пуст.

Границы (SPEC 058 §5): только GET — произвольные методы через все туннели
пользователя это уже не диагностика; ядро не парсит тело, поэтому смена формата
`cdn-cgi/trace` или ip2location не становится багом ядра; проба — реальный трафик
и пробуждение спящих WG-узлов, поэтому вызывается по явному действию пользователя,
без фоновых обходов списка узлов.

Форма libbox: возврат — объект с геттерами, опциональные заголовки — nullable
`*HTTPHeaders` с билдером `Add(key, value)` (мост gomobile не несёт ни map,
ни slice пар, ни overload'ов); `Host` перекладывается в поле запроса. Голых
string-возвратов не добавлено (SPEC 038).

Заглушка без `with_lx_command` отвечает `Unimplemented` — безтеговая сборка
остаётся поведенчески эквивалентна апстриму. Юниты покрывают резолв обоих
менеджеров, не-2xx как результат, кламп/усечение (включая границу «ровно
в лимит»), Variant B (тег/dial/таймаут), не-STARTED, отмену, нетронутую историю
urltest и `Host`; прогон чистый под `-race`.

Дрейф upstream на момент среза проверен по merge-base (runbook §2): единственный
расходящийся subject — `Fix oomkiller service stub build` (`9fa673d10`), уже
поглощённый более поздней апстримовой лентой (наш `service/oomkiller/service_stub.go`
несёт и `s.network`, и более новую сигнатуру `newAdaptiveTimer(…, s.writeOOMReport)`),
cherry-pick откатил бы назад — не берётся. Форк-сабмодули на своих пинах.

#### v1.14.0-lx.24

**Промоут линии lx.23 → lx.24-rc.2 в stable.** Свод: демон `lxd` как
устанавливаемая служба с админ-плоскостью и mTLS, два краш-фикса ядра,
upstream-синк 2026-08-11 и переезд сборки на go1.26.5. Инженерные детали — в
секциях ниже (`v1.14.0-lx.24-rc.1`, `v1.14.0-lx.24-rc.2`, `v1.14.0-lx.23`);
пользовательские ноты —
[docs-lx/releases/v1.14.0-lx.24.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/releases/v1.14.0-lx.24.md).

⚠️ **Тег `v1.14.0-lx.23` не срезался** — его содержимое (SPEC 033, SPEC 055,
FEATURE 006) уехало в поле впервые именно здесь, поэтому нумерация релизов
идёт `lx.22 → lx.24`, а секция `#### v1.14.0-lx.23` осталась в этом логе как
инженерная запись без соответствующего тега.

Состав:

- **Фича 014 целиком** (SPEC 055 + 056 + 057): `sing-box lxd` — headless-демон
  за управляющим каналом, переживающим reload и битый конфиг; `POST /admin/apply`
  с валидацией через собственный `sing-box check` и откатом на last-good;
  `rollback`/`start`/`stop`/`config`/`status`/`info`; mTLS-регистрация клиентов
  одноразовым инвайтом (сертификат = полный мандат, Bearer — операторский
  loopback-only мандат `client add/list/remove`); собственный ротируемый
  `lxd.log`; установка службы на macOS (`install` / `install-user`) и печать
  рецепта на Linux. Настройки — только `<state-dir>/daemon.json`,
  connection-флагов нет.
- **Краш-фиксы ядра:** пустой `members` у `dns_group` больше не паникует на
  `rand.IntN(0)` при раннем дайле WG-bind через detour-цепь с доменным узлом
  (SPEC 033, полевой краш lx.22 на android/arm64); `GetRules` в attached-режиме
  берёт роутер из сервисного контекста вместо nil-`*box.Box` (FEATURE 006).
- **Upstream-синк 2026-08-11** (`4902660f8`, свод 1.14.0) с перебазированными
  форк-сабмодулями и **go1.26.5** как единым сборочным тулчейном (SPEC 044).

Дрейф upstream на момент среза тега проверен по merge-base (runbook §2):
единственный расходящийся subject `Fix oomkiller service stub build` (`9fa673d10`)
уже поглощён более поздней апстримовой лентой — наш `service_stub.go` несёт и
`network adapter.NetworkManager` из этого фикса, и более новую сигнатуру
`newAdaptiveTimer(..., s.writeOOMReport)`; cherry-pick конфликтует откатом назад,
поэтому не берётся. Форк-сабмодули (`wireguard-go` `c6c8a831ef70`, `sing-tun`
`d67734281390`, `gvisor`-снапшот) содержат пины `go.mod`, все три `replace`
резолвятся на `./submodules/*`. rc.2 device-verified владельцем.

#### v1.14.0-lx.24-rc.1

**Демон `lxd` дорос до устанавливаемой службы: apply/rollback, mTLS, свой
ротируемый лог, установка на macOS и рецепт для Linux.**
([SPEC 056](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/056-LXD_APPLY_ROLLBACK/SPEC.md),
[SPEC 057](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/057-LXD_MTLS_SERVICE/SPEC.md),
[FEATURE 014](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md),
руководство оператора — [docs-lx/lxd-daemon.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/docs-lx/lxd-daemon.md))

Продолжение скелета из lx.23. ⚠️ **Синтаксис изменился:** `lxd run` →
голое `lxd`, а connection-флагов (`--listen`/`--tls`/`--secret`/`--secret-file`)
больше нет вовсе — настройки демона живут в `<state-dir>/daemon.json` (0600), и
вопрос «кто побеждает, файл или флаг» невозможен по построению. Без файла
dev-запуск получает фиксированные дефолты (plain h2c на `127.0.0.1:9091`, без
секрета); файл неявно не создаётся.

**Админ-плоскость с гарантиями (056).** `POST /admin/apply` валидирует кандидата
собственным `sing-box check`, при провале старта откатывается на last-good;
`rollback`/`start`/`stop`/`config`/`status`, память run-состояния и прерванного
apply через рестарты. Инфраструктурный сбой валидатора больше не выдаётся за
вердикт по конфигу: отмена родительского запроса — 500, а не 422 («ваш конфиг
битый» нельзя говорить, когда ничего не проверено); свой таймаут в 10 s —
вердикт. Очередь за `applyAccess` после teardown отвечает «daemon is shutting
down» вместо того, чтобы трогать уже снесённый сервис.

**mTLS и роли (057).** Демон сам себе CA; клиент регистрируется одноразовым
инвайтом `адрес#отпечаток#код` и дальше опознаётся по сертификату — **сертификат
это полный мандат**, Bearer поверх не требуется (клиенты секрета не знают, и
требование обоих запирало бы каждый лаунчер). Bearer — операторский мандат
**loopback-only** маршрутов `client add/list/remove`: минт кода это выдача
доверия, из сети он недоступен в принципе. Новый `GET /admin/info` отдаёт
паспорт демона (версия, state_dir, listen, отпечаток, pid, uptime, log_path) —
клиент не хардкодит пути.

**Свой лог вместо вечного append.** Под службой (stdout не терминал) демон
перехватывает stdout/stderr на `<support>/lxd.log` — туда попадает всё, включая
лог ядра и паники рантайма — и ротирует по возрасту/размеру. Дефолты «≈сутки
истории»: 24 ч, 1 бэкап, 20 МБ страховочный потолок; ключи `log_max_size_mb` /
`log_max_backups` / `log_max_age_hours` в daemon.json. Повод конкретный:
launchd `StandardOutPath` не ротируется — наблюдался рост до 39 МБ за ~1.5 суток
на info-логах ядра, а `newsyslog` не умеет пути с пробелами и не покрывает
user-scope. В терминале лог остаётся на экране.

**Служба.** macOS — `--service=install` (системный LaunchDaemon) и
`install-user` (LaunchAgent без sudo) ставят по-настоящему: сами материализуют
daemon.json (скан свободного порта с 19091, генерация секрета, mTLS), пишут
plist, бутстрапят и печатают сводку с одноразовым инвайтом. **Linux — только
печать рецепта** (принцип: всё, что меняет диск, делает оператор): детект init
по `/proc/1/comm` с фолбэками, печать ссылки на нужный раздел руководства,
готовых к вставке `daemon.json`, unit'а systemd или init-скрипта procd и команд
включения; секрет на экран не попадает — в рецепте стоит подстановка
`$(head -c 32 /dev/urandom | xxd -p -c 64)`, значение рождается на хосте.
`uninstall --purge` там тоже печатает `rm -rf`, а не выполняет. Вместо режима
`--service=print` — флаг `--dry-run`, работающий и с install, и с uninstall.
Windows — по-прежнему заглушка.

CI получил отдельный шаг `go test -race` для `./lxd/` и `./cmd/sing-box/`:
пакет существует только под `with_lx_command`, без явного шага его сьюта была
«[no test files]» и молча гнила.

#### v1.14.0-lx.24-rc.2

**Ветка догнала upstream и переехала на go1.26.5.** Кода lx-слоя это не
меняет — rc.2 нужен, чтобы прогнать на устройстве апстрим-хвост и новый
тулчейн до промоута lx.24 в stable.

**Upstream-синк 2026-08-11.** Ветка снова на вершине `upstream/testing`
(`4902660f8`, changelog-свод 1.14.0). `upstream/testing` был force-push'нут
после нашего мержа 2026-08-07: формально «235 коммитов дрейфа», реально новых —
19, взяты cherry-pick'ом (runbook §2). Заметное: DNS-кеши локального транспорта
партиционируются по сигнатуре интерфейса (смена сети больше не отдаёт чужой
кеш); WireGuard-хендшейк резолвит **все** адреса домен-пира и гонит их
наперегонки (`SetEndpointResolver`, fan-out); `daemon`-подписки эндпоинтов
переживают перезапуск сервиса (`followInstance`); hijacked-DNS получил
process info; фиксы reset network, FakeIP async-save, Android process finder,
unbounded-аллокаций на злом SRS (sing v0.9.0-beta.2, typed varbin) и OOM-стаба.
Форк-сабмодули перебазированы ДО ядра (runbook §1): sing-tun → `d67734281390`
(SPEC 040 сверху), wireguard-go → `c6c8a831ef70` (AWG2 + SPEC 041 сверху;
разрешение `SendHandshakeInitiation` = AWG-паддинг/junk + апстримный fan-out);
gvisor без дрейфа. Пины go.mod выровнены с upstream tip (sing v0.9.0-beta.2,
quic-go mod.4, sing-quic beta.2, openconnect 20260810, cronet-go 20260807 —
⚠️ musl-producer запустить вручную при следующем релизе, SPEC 023).
**Сборочный тулчейн переехал на go1.26.5 вслед за апстримом** (решение
владельца 2026-08-11; go1.26.5 = текущий последний стабильный Go): `go.version`
бампнут, win7-скрипт взят апстримовый (патченный go1.26.5) — принцип SPEC 044
«отгружаем тем же тулчейном, что upstream» восстановлен. Локально на 1.26.5
зелёные: полный LX_TAGS-бинарь (badtls-линковка), оба сабмодуля, тесты ядра.
Девайс-прогон этого rc на реальном устройстве обязателен до промоута в stable
(runbook §1.4 — пере-баз сабмодулей; SPEC 044 — смена тулчейна, профиль
hy2/quic): туннель, DNS, URL-тест, WG/AWG — несколько раз.

#### v1.14.0-lx.23

**Ядро больше не падает на старте с доменным узлом в detour-цепи WireGuard.**
([SPEC 033](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/033-DNS_GROUP_SERVER/SPEC.md))

`box.preStart` поднимает endpoints (вместе с outbounds) раньше DNS-транспортов.
Ранний дайл WG-bind через detour-цепь с доменным узлом уходил резолвиться в ещё
не стартовавшую группу `dns_group`: `members` пуст, survival-ветка выбирала
`best[rand.IntN(0)]` — паника на нулевом аргументе, ядро не поднималось вовсе.
Гейт в `selectTarget`: пустой `members` = группа не стартована → типизированная
ошибка «not started», вызывающий ретраится сам, когда транспорты поднимутся.
Полевой краш LxBox на 1.14.0-lx.22 (android/arm64).

Окно не гонка: при таком конфиге оно открыто на каждом старте, а медленный старт
`fakeip` (bbolt-батч) растягивает его до гарантии. Апстримные транспорты
(`udp`/`tls`/`https`) ранний запрос переживают — паниковала только группа,
поэтому правка локальная. ⚠️ Кольца вида «DNS-сервер → `detour` на узел → его
домен резолвится через тот же сервер» ядро по-прежнему не валидирует: детекторы
циклов работают внутри графа DNS-серверов и внутри графа outbounds по
отдельности, а такое кольцо динамическое — селектор переключился на узел с
IP-адресом, и кольца нет. Это зона приложения
([SPEC 024](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/024-CONFIG_LOOP_GUARD/SPEC.md)).

**RPC `GetRules` роняло ядро в attached-режиме.**
([FEATURE 006](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md))

При запуске с `services: [{"type": "api"}]` ядро строит `Instance` через
`attachInstance` — прицепляется к уже поднятому box снаружи, и поля `instance`
(`*box.Box`) у него нет: его заполняет только владеющий `newInstance`. `GetRules`
ходил за таблицей правил через `instance.Router()` и получал nil-деref в
`box.go:697` — первый же вызов RPC убивал процесс. Роутер и так лежит в сервисном
контексте (`box.go` регистрирует `adapter.Router`; тем же приёмом его берёт
`clashapi`), поэтому он резолвится в поле `Instance.router` в обоих конструкторах,
а `GetRules` читает поле. Нулевой роутер деградирует в `codes.Unavailable` — та же
дисциплина, что у `GetRunningConfig`. Остальные lx-RPC этого паттерна не имели:
они берут `ctx` и менеджеры, которые `attachInstance` заполняет.

**Сабкоманда `sing-box lxd run` — скелет headless-демона.**
([SPEC 055](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/055-LXD_DAEMON_SKELETON/SPEC.md),
[FEATURE 014](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md))

Первая задача фичи 014: ядро живёт in-process за долгоживущим управляющим
каналом — модель апстримного десктопного демона (`experimental/boxdd`), но
headless и в форме сабкоманды существующего бинаря (один артефакт, extractor
лаунчера не трогается, version skew невозможен).

`sing-box lxd run -c config.json --listen 127.0.0.1:9091 --secret …` под тегом
`with_lx_command`; без тега сабкоманды нет, пакет — пустой стаб. Порядок внутри:
чтение конфига → `daemon.NewStartedService` (владеющий режим, тот же кодопуть,
что libbox/boxdd) → `daemon.NewServer` (gRPC, Bearer-интерцепторы, health,
reflection) → listener → и только потом `StartOrReloadService`. Несущее свойство
архитектуры: **канал переживает reload и битый конфиг** — провал старта или
reload'а логируется, демон остаётся слушать в FATAL. SIGHUP перечитывает файл и
подменяет инстанс под живым сервером; SIGINT/SIGTERM — `CloseService` →
`grpcServer.Stop` → `Close`. Ни одной правки в апстримных файлах: сабкоманда
самонавешивается через `init()`.

#### v1.14.0-lx.22

**Группа `urltest` больше не сидит на мёртвом узле до следующего прогона.**
([SPEC 054](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/054-URLTEST_PENALTY_FAILOVER/SPEC.md))

Продолжение [SPEC 052](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/052-NETSTACK_CONNECT_DEADLINE/SPEC.md):
тот дал быстрый сигнал об отказе (ошибка за 15 с вместо 127 с тишины), но
потреблять его было некому. Реакции на ошибку боевого дайла у `least_test` не
было вовсе — ни у нас, ни в апстриме: причина отказа неоднозначна (мёртвый узел
против мёртвого сайта против моргнувшей локальной сети), поэтому выбор группы
пересчитывался только в хвосте пробного прогона, то есть до `interval` (по
умолчанию 3 минуты), а с `passive_check` и дольше.

Теперь отказ класса «путь мёртв» — таймаут дайла (включая 15-секундный дедлайн
SPEC 052), `EHOSTUNREACH`/`ENETUNREACH`/`ETIMEDOUT` — даёт узлу **+1 штраф** и
один запасной дайл через лучшего кандидата. Кап — две попытки на пользовательский
дайл; успех запасного переносит на него выбор группы **без** разрыва живых
соединений. `ECONNREFUSED`/`ECONNRESET` штрафа не дают и фолбэк не запускают:
узел донёс пакет, отказало назначение — через другой узел будет тот же отказ.
Отмена вызывающим (`context.Canceled`) тоже не считается.

Когда лучший-по-скорости набирает **3 штрафа**, группа переходит в аварийный
режим: выбор ранжируется двумя уровнями — сначала по штрафам, затем по задержке.
Победители по штрафам (обычно все с нулём) соревнуются по скорости, как обычный
`least_test`. Штраф обнуляется **только доказательством жизни** — успешным
боевым дайлом или ответом на пробу; сбросов по времени нет. Если штрафы набрали
все кандидаты (умер путь целиком, ранжировать нечего) — принудительный прогон
проб, не чаще раза в 2 минуты, отсчёт от **конца** прошлого прогона. И на время
аварийного режима отключается пропуск проб по `passive_check`: иначе рабочий
запасной пассивно подтверждается, циклы пропускаются, и оштрафованный бывший
лидер никогда не получает пробу, которая сбросила бы ему штрафы.

Ни одного нового таймера: все проверки — дельты по метке времени в момент
события, что переживает и сон устройства, и заморозку процесса. `round_robin`
не затронут (у пула своя машинерия здоровья, SPEC 019 v2); UDP штрафов не
копит, но выбором пользуется общим. Дедупа по назначениям сознательно нет:
один заблокированный сайт может уронить группу в аварийный режим, но тот
деградирует мягко — трафик уходит на здоровые узлы и всё работает, а штрафы
смывает первый же ответивший ретест.

⚠️ **Полевого прогона не было** — шесть юнит-тестов и стенд; проверяется этим выпуском.

**Соединения через WireGuard больше не висят по две минуты без ошибки.**
([SPEC 052](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/052-NETSTACK_CONNECT_DEADLINE/SPEC.md),
выпущено в `v1.14.0-lx.21`)

TCP-дайлы через gVisor-стек были единственным классом путей в ядре без
таймаута: `C.TCPConnectTimeout` (5 с) живёт только в системном `net.Dialer`,
netstack его обходит структурно, и ни один слой выше (route, группы, detour,
tun-инбаунд) не ставит дедлайн. Единственной границей оставался SYN-бэкофф
gVisor: шесть ретрансмитов, 1+2+4+8+16+32+64 — **127 секунд** до ошибки (на
стенде: `2m7.065s`). Для доменных назначений хуже: перебор адресов по очереди,
до N×127 с. Полевой дамп горутин показал 16 дайлов, припаркованных в
`DialTCPWithBind` на одном WARP-эндпоинте.

Это унаследованный апстрим-дизайн (файл байт-в-байт совпадает с upstream), не
регрессия форка; QUIC-аутбаунды спаслись случайно — у `quic-go` свой пятисекундный
дефолт рукопожатия. Ручка `TCPSynRetriesOption` в нашем пине gVisor мертва:
хранится и валидируется, но нигде не читается, — так что единственным рычагом
остаётся дедлайн контекста.

Добавлен одноразовый connect-дедлайн `C.TCPTimeout` (15 с) в листовом шве
`DialTCPWithBind` и в трёх родственных точках (openvpn, openconnect, tailscale),
найденных адверсариальной проверкой диффа. Именно 15, а не 5: столько же длится
проба здоровья, и пользовательский бюджет ниже пробного открыл бы вилку, в
которой медленный-но-живой узел проходит пробы, а все боевые дайлы через него
падают. Замеренный потолок честного холодного дайла — 4.8 с при нулевом RTT
(пересборка устройства уровня 3 плюс рукопожатие), в поле с потерянным первым
пакетом инициации 8–10 с; пятисекундный кап резал бы свои же пробуждения.
Дедлайн привязан к фазе установки через `defer cancel()` и физически не может
пережить её в возвращённом соединении — иначе повторился бы класс SPEC 050,
где истёкший дедлайн убивал живой поток XHTTP. Бюджет на каждый адрес отдельный,
поэтому мёртвый первый AAAA не съедает время живого A. UDP не тронут: у
netstack-«дайла» UDP нет сетевой фазы, а любой дедлайн стал бы дедлайном сессии.

Стенд: blackhole-дайл 127 с → 15.05 с; тёплые дайлы и пробуждения не задеты.

**REALITY-узлы на свежих серверах Xray снова ходят.**
([SPEC 053](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/053-REALITY_MIN_CLIENT_VER/SPEC.md))

Xray v26.7.11 коммитом [`af7eb68`](https://github.com/XTLS/Xray-core/commit/af7eb68028732a8ee3c0e5d6ab2b8a657bb2e770)
включил `minClientVer` **по умолчанию** — порог `26.3.27`. Раньше незаданное
поле означало «не проверять вовсе», так что операторы получили отсечку без
единого действия со своей стороны. Апстримный sing-box объявляет в
REALITY-рукопожатии версию `1.8.1`, зашитую константой ещё в 2023-м
(`common/tls/reality_client.go`, первые три байта `SessionId`), — и потому
оказывается ниже порога.

**Симптом обманчив: ошибки нет.** Проверка версии — одно из четырёх AND-условий
в `XTLS/REALITY`, и при провале сервер не рвёт соединение, а прозрачно
проксирует его на камуфляжный `dest` с настоящим сертификатом. Так задумано —
иначе пробер отличал бы REALITY-сервер по форме отказа. Для пользователя это
выглядит как «узел не работает, но в логе чисто», и неотличимо от неверного
`short_id`, чужого публичного ключа или разъехавшихся часов.

**Исправление** — объявлять ровно требуемый минимум `26, 3, 27`. Сравнение там
целочисленное и нестрогое (`>=`), поэтому выше порога подниматься незачем.
Со старыми серверами совместимость сохраняется: они либо не сверяют версию
вовсе, либо сверяют с порогом ниже нашего. Ту же правку сделал mihomo
([`d8231b3`](https://github.com/wiktorbgu/mihomo-mikrotik/commit/d8231b36e7a55656d6eff6028783a0412eeb8274)).

Правка живёт в апстримном файле, который апстрим активно трогает (kTLS, ECH,
TLS spoof), — маркер `// lx: SPEC 053` и запись в реестре
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)
стоят затем, чтобы встречное изменение не сняло её молча.

⚠️ **Полевого прогона не было** — собрано и сверено с исходниками XTLS
построчно, но против живого сервера Xray ≥ v26.7.11 не проверялось (стенда
нет). Отчёты приветствуются.

#### v1.14.0-lx.20-rc.6

**Исправляет rc.5, который падал на старте.** Полевой tombstone с CPH2411:
SIGABRT в `libbox.so`, стек — `endpoint.go:312 → e.tunDevice.Start() →
stackDevice.Start() → udpForwarder.Start() → f.udpNat.Start()`, nil по
смещению `0x78` (поле `classAccess` структуры `UDPNat`). Падение плавающее:
через раз запускалось нормально.

**Корень — не сам баг, а способ, которым я закрывал дрейф в rc.5.** Из 14
недостающих коммитов `wireguard-go` были взяты 3 — по признаку «на эти
символы ругается компилятор» (`undefined: device.PeerLookupFunc`). Пропущены
оказались, в частности:

- `15b912c device: fix TOCTOU race during session state update (#77)`
- `2ad9837 device: refactor container locking for lock-order clarity`
- `010dd5c device: fix some lock ordering violations, add a test for a deadlock we hit`

То есть ровно исправления гонок и порядка блокировок — они не дают ошибок
компиляции, поэтому выборочный cherry-pick их и не подхватил. Получилось
состояние, которого у апстрима никогда не существовало: `go build`, `go test`
и `lx-check` зелёные, а на устройстве — плавающая паника при старте.

**Исправление.** Форк перепривит на ПОЛНУЮ апстримовую ленту, наши патчи
лежат сверху, а не наоборот:

- `wireguard-go` — база `f39689a` (ровно то, что требует `go.mod`), все 14
  апстримовых коммитов на месте; поверх 7 наших: AWG2-обфускация,
  transport padding (SPEC 025), reserved-vs-magic (SPEC 026),
  egress-provider, IpcGet-паритет, rebind по give-up (SPEC 041). Три прежних
  cherry-pick'а выброшены — их API теперь в базе.
- `sing-tun` — наш SPEC 040 (self-heal acceptLoop) лёг поверх апстримового
  `da24aca`, а не наоборот.

**Инспекция кода перепрививки.** Апстрим вынес тела циклов приёма и отправки
в `processInboundContainer`/`processOutboundContainer` и ввёл
`MessageEncapsulatingTransportSize` — резерв под префикс для
`conn.Bind.Send()`. Наш AWG-паддинг занимает ровно это место, поэтому
константа занулена в `noise-protocol.go` (`// lx:` комментарий там же), а
headroom учтён в `allocLength` обеих функций подготовки пакета. Конфликт в
`receive.go` оказался чисто структурным — в нашей стороне блока не было ни
одной AWG-строки, взята апстримовая версия.

**Процессный вывод, записанный в раннбук (раздел 1).** Проверять сабмодуль
надо не «есть ли у апстрима новые коммиты», а машинно: **содержит ли наша
ветка ровно тот коммит, который требует `go.mod`**. И брать ленту целиком —
выборочный cherry-pick по ошибкам компилятора пропускает именно то, что
чинит гонки.

#### v1.14.0-lx.20-rc.5

Синхронизация с апстримом: **235 коммитов** `upstream/testing` влиты
(merge-base `c9e81856e` → tip `d1e283be4`), дрейфа больше нет. Своих изменений
поверх `rc.4` нет — весь релиз про приведённую базу (SPEC 051,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/051-UPSTREAM_MERGE_235/SPEC.md)).

**Что приехало из апстрима.** 97 исправлений, 54 добавления, 21 обновление
зависимостей. Клиентски значимое:

- `Fix TUN dispatcher deadlock` — через бамп `sing-tun`;
- `dns: Fix completed race rule blocked by earlier armed rule` — гонка в
  DNS-роутере (+85 строк регрессионного теста у апстрима);
- `Fix DNS route suffix matching`, `Fix TCP DNS retry`, `dns: Add namespace and
  parallel support for evaluate`;
- `wg: Fix input packets peer lookup`, `wg: Fix InputPackets exceeding device
  batch size`, `Fix wg detour`, `wireguard: Fix system device configures DNS for
  interface`;
- `Fix inconsistent URLTest results` — учёт мультиплекса при замерах
  (`OutboundWithMultiplex`); нашей задачи 050 не касается, `URLTestGroup.Close()`
  у апстрима по-прежнему не отменяет идущий прогон;
- `Add hysteria2 chrome parrot support`, `Add initial_path option to remote
  rule-sets`, `Add multiple tags support to rule-sets`.

Крупные апстримовые новинки в сборку форка **не входят** по клиентскому профилю:
`openvpn`/`openconnect`, `snell`, USB/IP, bridge-outbound, L3-forwarding, DERP,
desktop/Windows-сборки. Их build-теги в AAR не добавляются (`build_libbox`,
блок `lx:begin no-tailscale` и соседние комментарии).

**Разрешение мержа.** 65 конфликтных файлов. `*.pb.go` регенерированы через
`make -f Makefile.lx lx-proto`, а не слиты руками; 27 файлов без наших коммитов
(проверено по авторству, не по маркерам) взяты апстримовыми целиком; остальные
разобраны поштучно с сохранением SPEC 017/018/019/020/028/046/050.

**Три поломки сделало АВТОслияние, а не конфликты** — git молча склеил обе
стороны:

- `adapter/outbound.go` — потерялся импорт `time`, нужный нашему
  `IdleSuspendable` (SPEC 020);
- `dns/client_log.go` — `logRefreshedResponse` задвоился (наша версия с
  `transport` + апстримовая);
- `transport/wireguard/endpoint.go` — после нашего nil-guard остался апстримовый
  `return e.tunDevice.Close()` ⇒ nil-паника в `TestPortAddressesSurviveTeardown`.

Вывод записан в спеку: список конфликтных файлов — **не** список зон риска,
проверять надо сборкой и тестами под всеми тегами.

**Блокер AAR и как он снят.** Мерж поднял `tailscale` 1.92 → 1.102, а тот
требует из `wireguard-go` API, которого не было в базе нашего AWG2-форка:
`PeerLookupFunc`, `NewPeerConfig`, `SetPeerLookupFunc`, `PeerSessionState`,
`AllowedIPs.LookupFromPacket`. Цепочка —
`libbox/native_shell_session.go` → `protocol/tailscale/tailssh` →
`wgengine/wgcfg` → `wireguard-go/device`; тегами не обходится, так как `tailssh`
гейтится по `with_gvisor`. Ядро при этом собиралось, ломался только AAR.

Закрыто **тремя cherry-pick'ами** в форк-сабмодуль
(`Leadaxe/wireguard-go-awg2-lx`, `1255464` → `ce20e73`): `e924a91`, `f69b247`,
`7c3a736`. Полный re-graft не понадобился. Конфликты во всех трёх — одной
природы (невзятый `70b09a6` переименовал `AllowedIPs.mutex` → `mu` и
`IPv4`/`IPv6` → `ipv4`/`ipv6`): взята их логика с нашими именами полей.
В `device/timers.go` сохранены оба вызова — апстримовый
`noteSessionHandshakeStopped()` идёт **перед** нашим `handleHandshakeGiveUp()`
(SPEC 041), чтобы потребитель видел «handshake stopped» до пересоздания сокета.
Семантика нейтральна: `lookupFunc` ставится только tailscale-движком, при
nil-хуке поведение прежнее. Здесь же снят временный shim `lx:begin awg-lookup`.

**Проверено:** все 94 файла с lx-маркерами сохранили свои блоки, 55 lx-only
файлов на месте, патчи HOTFIXES 028/029/030/039/045/046/047/050 найдены
поимённо; AWG-обфускация цела (8 тестов, включая reserved-vs-magic SPEC 026 и
transport-padding SPEC 025); `go build ./...`, `go test ./...` без тегов,
`lx-check`, оба шага `vet` из CI и тесты 050/WireGuard/route под полным
lx-набором — зелёные. Сабмодуль запушен до суперпроекта.

**Дополнительно в rc.5 (после первичного мержа).**

- **OpenVPN / OpenConnect включены** во все сборки — `LX_TAGS` (desktop/CLI),
  `sharedTags` в `build_libbox` (AAR), `BASE_TAGS` в `lx-ci`. В отличие от
  `with_clash_api` эта пара между сборками не расходится. Клиент и сервер
  неразделимы: апстрим держит их за одним build-тегом на пакет, так что
  включение клиента поставляет и серверную часть. Типы на проводе —
  `openvpn-client`, `openvpn-server`, `openconnect`; регистрируются как endpoint
  + DNS transport.
- **gvisor `20250811` → `20260727`** — новый снапшот форк-сабмодуля под пин,
  которого требует `go.mod` после мержа (upstream `d620bbbf2`). Прежний снапшот
  брался 2026-08-04 ровно с версии апстрима на тот момент, разрыв возник с его
  бампом сутки спустя. За год у апстрима ~14 000 строк в 292 файлах; для нас
  значимы `tcp/connect.go` (PMTU-discovery + начальный RTT/RTO: задержка ACK
  внутри стека завышала стартовый таймаут на несколько RTT), `tcp/snd.go`,
  `tcp/rcv.go`, `stack/conntrack.go`, `stack/packet_buffer.go`. **Баг SPEC 048
  апстрим не исправил** — проверено по коду новой версии, guard перенесён
  вместе с тестом, red/green подтверждён.
- **NDK поднят до `r28c`** (= `28.2.13676358`) в наших трёх workflow. Прежнее
  значение `r28` — не «плавающий» тег, а самостоятельный неизменяемый архив от
  05.02.2025 (проверено по `dl.google.com`: `r28`, `r28b`, `r28c` — три разных
  файла, `r28a`/`r28d` не существуют). То есть CI собирал AAR тулчейном на две
  ревизии старше того, что стоит на машине сопровождающего; теперь CI и
  локальная сборка идут на одном NDK. Апстримовый `build.yml` не тронут —
  zero-diff, его пин сверять вручную при мержах.
- **sing-tun** — cherry-pick апстримового `da24aca "Update gvisor to
  20260727.0"`. Понадобился, потому что новый gvisor удалил
  `stack.CapabilityDisconnectOk`, которую использовал `fdbased_darwin`: без
  этого `lx-build` падал. Конфликтов нет, SPEC 040 (self-heal acceptLoop) цел.


#### v1.14.0-lx.20-rc.4

A URL test that reached a half-alive node hung forever and **survived a full
core restart**, piling up one generation of zombie goroutines per Stop → Start
(SPEC 050,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/050-URLTEST_ZOMBIE_RUN_SURVIVES_RESTART/SPEC.md)).

Field dump (Android arm64, `1.14.0-lx.17-rc.3`), taken 40 s after a clean
Stop → Start: two `URLTestGroup.testNodes` runs blocked on `batch.Wait()`, aged
**100 and 43 minutes**, each holding 2806 outbounds from a subscription that was
no longer loaded (592 in the live config). Six goroutines on one stack —
`io.(*pipe).write` ← `v2rayxhttp.(*streamConn).Write` ←
`vless/encryption.(*ClientInstance).Handshake` ← `urltest.URLTest` — plus five
OOM snapshots at 396–428 MB. The group had stopped publishing delays ("the ping
is lost"); only a config rebuild cleared it.

Three defects, lethal only in combination:

- **No deadlines on XHTTP conns.** `streamConn`/`splitConn` returned
  `os.ErrInvalid` from every `Set*Deadline`, while a `Write` goes into an
  `io.Pipe` nobody reads until `RoundTrip` (running in a separate goroutine)
  raises the stream. On a node that accepts TCP but never reads the body, that
  write blocks with nothing able to interrupt it.
- **Cancellation never reached the handshake.** `encryption.Handshake` takes a
  bare `net.Conn` (wire format fixed by SPEC 032) and is invoked *after* the
  conn exists, so the dial context governs only the dial. This is why a single
  `URLTestOutbound` hung despite its per-call context carrying a correct
  timeout (SPEC 015 §3.6).
- **`Close()` did not cancel the run.** It stopped the ticker and closed
  `g.close`, which only `loopCheck` reads; the run inside
  `CheckOutbounds` → `testNodes` knew nothing about it.

Note that level 3 cannot be fixed alone: `batch.Wait()` is a plain `wg.Wait()`
(`sing@v0.8.12.../common/batch/batch.go:76`) and ignores context entirely, so
only the deadlines and the dial watcher can wake a blocked task. That
dependency dictated the whole fix order.

- `transport/v2rayxhttp/conn.go` — real deadlines
  (`// lx:begin 050 deadline-support`) plus a dial-context watcher in
  `dialStreamOne` that exits on `created`, so it can never tear down a live
  stream. The pipe is broken from the **read** half on purpose: `io.Pipe` hands
  a writer only `ErrClosedPipe` for an error set via
  `PipeWriter.CloseWithError`, which would lose `os.ErrDeadlineExceeded`.
  `dialStreamUp` deliberately gets no watcher — its upload `RoundTrip` lives as
  long as the connection does.
- `protocol/vless/lx_encryption.go` — `wrapEncryption(ctx, conn)` bounds the
  handshake with the dial deadline, **write side only**. An XHTTP read deadline
  is one-shot (it closes the late-bound download body and clearing cannot
  reopen it), so arming both directions would hand back a conn whose download
  side is already dead whenever the handshake overran the deadline but still
  succeeded. Two call sites in the upstream-owned `outbound.go`.
- `protocol/group/urltest.go` — owned child context; `Close()` cancels it
  **before** the `ticker == nil` early return, since the `PostStart` run has no
  ticker armed and is exactly the one that used to survive. `testCtx` now
  descends from the batch context instead of `g.ctx`.

`NeedAdditionalReadDeadline` stays `true` on both conns: the read deadline here
is one-shot and does not restore the conn for a later read, which is what
`deadline.NewConn` provides — claiming otherwise would be a lie to consumers.

Verification: `lx-test/zombie` drives a real `box.New`/`Start`/`Close` against a
listener that accepts TCP and stays silent — with the fix reverted it reports
"2 test goroutine(s) survived box.Close", with the fix zero. A/B against 33
live XHTTP nodes taken from a working device config (17 `auto`+REALITY, 16
`packet-up`) showed no regression: 22/33 reachable with the fix versus 20/33
without, medians 151 ms and 154 ms.

#### v1.14.0-lx.20-rc.3

Two nil-pointer crashes that took down the whole process, both reported from
the field on 2026-08-03 and both proven by device crash bundles from
`1.14.0-lx.19-rc.3`.

**A network change during tunnel startup crashed the process (SPEC 047,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/047-EARLY_RPC_NIL_ROUTER_CRASH/SPEC.md)).**
`CommandServer` gated early RPCs on `instance.Box() != nil`, but `Box` stops
being nil the moment it is *created*: `s.instance = instance` is assigned
before `instance.Start()`, while `NetworkManager`'s `router`/`endpoint`/
`inbound`/`outbound` are only set at `StartStateInitialize`. The gate is
therefore passed for the whole duration of `Box.Start()`, with the fields
still nil.

- `ResetNetwork` landing in that window died on `r.router.ResetNetwork()`
  (SIGSEGV `addr=0x50`). The shooter is the routine auto-`resetNetwork()` on
  a WiFi↔LTE switch: LxBox §087 registers the network monitor *before*
  `startOrReloadService`, so a network change coinciding with tunnel start
  hits the window exactly. Both sides of the race are visible in one bundle —
  goroutine 17 held the start inside a bbolt transaction on `cache.db`.
- Two layers of defence. `daemon/started_service_ready_lx.go` exports
  `Ready()` (status `ServiceStatus_STARTED` — the predicate upstream already
  keeps package-private for `URLTest`/`SelectOutbound`), and the four
  weakly-gated methods in `experimental/libbox/command_server.go` now use it.
  `route/network.go` additionally bails out early when the fields are nil,
  before `connectionManager.CloseAll()` — three other callers (windows power
  event, clashapi, oomkiller) have no gate of their own.
- All four methods were checked; exactly one was provably vulnerable
  (`ResetNetwork`). `NeedWIFIState`/`UpdateWIFIState`/`NeedFindProcess` are
  nil-safe and were brought to the same gate as a single invariant, not as a
  fix. No build tag: a crash fix must hold in every build.
- `route/early_rpc_guard_lx_test.go`, `daemon/started_service_ready_lx_test.go`:
  red/green plus a concurrent `Ready()` read under `-race`.

**A TCP connection to an unreachable node crashed the process (SPEC 048,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/048-GVISOR_HANDSHAKE_NIL_CRASH/SPEC.md)).**
A connection that never reached established killed the core instead of
failing with a timeout. In gvisor, `performHandshake`'s failure branch zeroes
`ep.h` and releases `ep.mu` *before* calling `ep.Close()`; the endpoint state
only changes later, inside `closeLocked`. In that window the endpoint is still
`SynSent`/`SynRecv`, so `connecting()` is true.

- A segment arriving there wakes the dispatcher into `handleConnecting`, whose
  gate checks the state but not `h` — so `ep.h.processSegments()` runs on a
  nil handshake and panics with a nil receiver at `connect.go:534`. The two
  conditions used to coincide; they stopped once zeroing `h` moved ahead of
  `Close()`.
- Fix: a nil-guard on `ep.h` at the top of `handleConnecting`, closing all
  five dereferences at once (`processSegments`, `listenEP` in the error
  branch, and both inside `deliverAccepted`, reachable only from here). The
  guard releases the mutex the same way the state gate does — otherwise the
  panic would become a deadlock against the closing side parked on `LockUser`.
- Requires a **third fork submodule**: gvisor is pulled as a plain module.
  `submodules/gvisor` → [Leadaxe/gvisor-lx](https://github.com/Leadaxe/gvisor-lx),
  a **snapshot of the pin without history** — upstream carries 1.45 GB of it
  and every CI job clones this, while the delta is 12 lines of guard plus 45
  of test. Working tree 7.3 MB, `.git` 4.1 MB. Module path is preserved, or
  the `replace` would not apply.
- Reproduced by running, not only by reading the bundle: the test drives an
  endpoint into the exact window (`SynRecv` with `h == nil`) and calls
  `handleConnecting` with no stubbing — the resulting stack matches the device
  bundle line for line. The test lives in the fork next to the guard, so a
  guard lost while rebasing onto a new pin turns the test red instead of going
  silent.
- Trigger in the field: TCP that never reaches established (silent node, RST,
  timeout) while SYN retransmits keep arriving — subscriptions with dead
  nodes, URL tests, reconnects after a network change. Our lazy-mode sing-tun
  widens the window by driving `CreateEndpoint` from the sniffer.

Both bugs are upstream's and both are alive on `upstream/testing` (checked
line by line at `115dbec2c`); the fork only widens the windows. Registry rows
and removal conditions are in
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)
(P8, P9). Field verification of both is still pending — neither class is
reproducible on an emulator.

**The Go toolchain version is now pinned in one readable file (SPEC 049,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/049-GO_TOOLCHAIN_PIN_FILE/SPEC.md)).**
`go.version` in the repository root holds `go1.25.12` — tag-shaped like
`golang/go`, so it serves both a `git checkout` in a toolchain clone and
`actions/setup-go` once the `go` prefix is stripped.

- Packagers building the AAR from source (F-Droid,
  [fdroiddata!44731](https://gitlab.com/fdroid/fdroiddata/-/merge_requests/44731))
  had nothing to read: `go.mod` says `1.24.7`, the language floor, and that is
  exactly the toolchain SPEC 044 device-verified as killing every quic-go
  outbound on some vendor Android kernels.
- 15 `setup-go` steps across the five `lx-*` workflows now read the file. Two
  of them — `lx-rebase` and `lx-musl-toolchain-mirror` — were resolving
  `go-version-file: go.mod`, i.e. building on 1.24.7; for the musl mirror that
  also meant a toolchain different from the release job that restores its
  output. `check-latest` is dropped where an exact patch is pinned.
- `go.mod` is untouched, and so are the upstream workflows and
  `setup_go_for_windows7.sh` — the latter pins the same 1.25.12 upstream-side
  and cannot follow a shared pin anyway, its MetaCubeX patches existing only
  for `release-branch.go1.25`.

#### v1.14.0-lx.20-rc.2

**Hijacked DNS exchanges no longer run on the stack packet loop (SPEC 046,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md)).**
A DNS server with `detour` to an outbound whose selected node is silently
dropped by the network stalled **all** tunnel forwarding — other DNS servers,
ICMP and new connections of every protocol — not just its own queries. Field
incident: CPH2411 / Android 15 / LxBox 2.19.2, forwarding alive 12–60 s after
each reload, then dead in waves of ~4–5 minutes; core logs stayed clean
(incoming `inbound DNS packet from …`, no matching `dns: exchanged`).

- Root: both tun stacks call `Router.HijackDNSPacket` **synchronously** from
  the packet loop (`sing-tun` `ForwardDispatcher` / gvisor dispatch), and
  `Client.ExchangeAsync` — despite the name — blocks the caller inside
  `ConnPool.acquireShared` until the transport dial completes or the DNS
  timeout (10 s) fires. Every *unique* question therefore froze the loop for
  the full timeout; only exact duplicates dedupe via singleflight. Live
  goroutine dump caught `fdbased.dispatchLoop` itself parked in
  `acquireShared`.
- `route/dns.go`: the exchange moves to its own goroutine, gated by
  `dnsHijackSem` (`semaphore.NewWeighted(256)`, new field in `route/router.go`).
  Over the limit the query is dropped with a debug line — UDP clients retry —
  so a hijack storm cannot grow goroutines without bound.
- Fix sits at the shared entry point, so every caller benefits (tun system,
  tun gvisor, wireguard/openvpn/openconnect/tailscale endpoints).
- `route/dns_hijack_async_lx_test.go`: behaviour guards that a hung exchange
  does not block the caller and that over-limit queries drop instead of
  waiting.
- Emulator verification (AVD, gvisor, synthetic dead detour to `192.0.2.1:443`
  plus a stream of unique `.ru` queries): 6 minutes of hammering the dead path
  → ICMP 120/120, DNS over live paths 117/120 (3 misses are netd-side pool,
  never reached the core), core answers in 30–50 ms. The same scenario on the
  previous core kills ICMP and DNS in waves. Device verification on the
  incident config is still pending.

#### v1.14.0-lx.20-rc.1

**Every build job now pins the Go 1.25.x toolchain (SPEC 044,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/044-ANDROID_AAR_GO124_QUIC_DEAD/SPEC.md)).**
No code change on top of `lx.19` — this is a build-environment release.
Desktop, musl and lint jobs used to take the toolchain from `go.mod`
(`go 1.24.7`), which is the language floor upstream keeps for parity, not a
toolchain choice: upstream itself pins **1.25.x** in every release job of its
own `build.yml` (1.25.12; `^1.25.3`/`^1.25.4` for darwin/windows). So the fork
was the only party shipping go1.24 binaries — the same toolchain that made
the AAR's quic-go outbounds hang on vendor Android kernels (`lx.19-rc.2`), and
one where `badtls` compiles as a stub because upstream re-gated it on
`go1.25 && badlinkname`.

- `lx-release.yml`, `lx-build.yml`, `lx-ci.yml`: all `go-version-file: go.mod`
  replaced with `go-version: '1.25.x'` (12 jobs total). `go.mod` keeps
  `go 1.24.x` for upstream parity.
- The AAR pin drops from `1.26.x` to the same `1.25.x` — one toolchain line
  across the whole CI, and the line upstream actually ships. The SPEC 044
  defect threshold is ">= 1.25"; both 1.25.5 and 1.26.5 are device-verified.
- The Windows 7 job is untouched: it builds its own MetaCubeX-patched
  toolchain from `release-branch.go1.25`.
- Desktop binaries therefore gain active `badtls` for the first time.

#### v1.14.0-lx.19

Promotion of the `rc.1`–`rc.3` line with no code changes on top of `rc.3`;
the only commits between `rc.3` and this tag are the release-notes pipeline
rework (`3987cb949`: release body now comes from `docs-lx/releases/v<version>.md`
in the LxBox bilingual format, with the changelog section as pre-release
fallback) and the notes documents themselves. What the line ships:

- **WG/AWG self-heal v2** (SPEC 041 v2, rc.1): early rebind at ~15 s for
  provably dead sessions inside the handshake retry cycle, plus the
  `CommandServer.RebindStaleEndpoints()` wake nudge for consumers — the
  post-wake ERR window collapses from ~90 s to one handshake RTT.
- **Android AAR Go toolchain pin** (SPEC 044, rc.2): AAR jobs pin
  `go-version: '1.26.x'` — a Go 1.24 libbox kills every quic-go outbound
  (hysteria2/tuic/masque-h3) on some vendor Android kernels. Plus static
  libbox exports `SetQuicGSODisabled`/`SetQuicECNDisabled` for field
  diagnostics (LxBox Debug API `/action/quic-knobs`).
- **Trojan/VLESS `tls.enabled: false` nil-dialer crash fix** (SPEC 045,
  rc.3): upstream regression from the ECH-retry commit; the TLS dialer is
  now built only when a TLS config actually exists.

Upstream note: `upstream/testing` tags `v1.14.0-beta.3`/`v1.14.0-beta.4`
(2026-07-29/31) predate our 2026-08-01 merge and their content is already in;
the tags are not ancestors only because upstream force-pushed the branch
afterwards. Subject-level comparison at tag time shows zero genuinely new
commits, so the base stays **v1.14.0-beta.2** (the newest upstream tag in our
ancestry).

#### v1.14.0-lx.19-rc.3

**Trojan/VLESS node with `"tls": {"enabled": false}` crashed the whole core
process with a nil-pointer panic on the first dial, including URL tests
(SPEC 045,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/045-TLS_DISABLED_NIL_DIALER_CRASH/SPEC.md)).**
Field report (4pda, crash bundle from device): a plain-trojan node from a
public subscription (`tls.enabled: false` — a legal config that passes
`check`) took down the VPN the moment a URL test reached it. Upstream
regression from the ECH-retry commit (`1f0308054`): the TLS dialer is built
whenever a `tls` block is present, but the config constructor returns a nil
config for `enabled: false` by contract — so the dialer wraps nil and
SIGSEGVs in `ClientHandshake`. The panic fires only when the TCP connect
succeeds, so such a node can sit dormant in a subscription for months (DPI
drops its TCP) and fire the first time the path becomes passable. Still
unfixed in upstream `testing` at the time of this release.

- `protocol/trojan` and `protocol/vless` now create the TLS dialer only when
  the TLS config was actually built (same guard upstream itself uses in
  vmess); disabled TLS means a plain-TCP dial, as before the regression.
- Red/green regression tests in both packages; registered in the HOTFIXES
  ledger with a removal condition (drop when upstream adds the nil guard).

#### v1.14.0-lx.19-rc.2

**Android AAR: quic-go outbounds (hysteria2 / tuic / masque-h3) dead on
vendor kernels when the AAR is built with Go 1.24 (SPEC 044,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/044-ANDROID_AAR_GO124_QUIC_DEAD/SPEC.md)).**
Field report (4pda): hysteria2 nodes never connect in LxBox while the same
server works in other clients on the same phone and provider. Root cause is
the AAR build toolchain, not fork code: a libbox built with Go 1.24.x (what
`go-version-file: go.mod` gave CI) makes every quic-go dial inside the VPN
process hang to `context deadline exceeded` on some vendor Android kernels
(device-verified on OnePlus Nord CE 2 / MTK / Android 15), while TCP
protocols and wireguard-go UDP keep working and generic-kernel emulators
never reproduce. The same source built with Go 1.25.x works on the same
device. GSO/ECN offload paths were explicitly ruled out on-device via the
new knobs below.

- All three AAR jobs (`lx-release.yml`, `lx-build.yml`, `lx-ci.yml`) now pin
  `go-version: '1.26.x'` explicitly (defect threshold is >= 1.25; both 1.25.5
  and 1.26.5 device-verified); `go.mod` stays at upstream's `go 1.24.x`.
- New static libbox exports `SetQuicGSODisabled(bool)` /
  `SetQuicECNDisabled(bool)` (`quic_env_knobs_lx.go`) — runtime toggles of
  quic-go's own env escape hatches, applied on the next reconnect. Consumed
  by the LxBox Debug API (`POST /action/quic-knobs`, LxBox §341) for field
  diagnostics of offload-class failures.

#### v1.14.0-lx.19-rc.1

**WG/AWG self-heal v2: the post-wake ERR window shrinks from ~90 s to
seconds (SPEC 041 v2,
[SPEC.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/TASKS/041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md)).**
The v1 give-up rebind (lx.17-rc.4) proved itself in the field — sockets do get
recreated — but the cure only arrives ~90 s after the first traffic demand,
while users measure ping within the first half-minute after waking the device
and read every node as dead. Two new triggers of the same rebind, sharing one
debounce window:

- **Early rebind.** Inside the existing handshake retry cycle: once ≥3
  initiations went unanswered *and* the session is provably dead (no live
  keypair, or the last handshake is older than 180 s), the socket is rebound
  at ~15 s instead of ~90 s. A live session with transient packet loss keeps
  byte-for-byte upstream behaviour.
- **Wake nudge.** New libbox method `CommandServer.RebindStaleEndpoints()`
  (a mirror of `ResetNetwork`): the consumer calls it when the device wakes
  (LxBox: `USER_PRESENT`), and every WG/AWG endpoint with a provably dead
  session rebinds and re-initiates immediately — no traffic demand needed,
  the ERR window collapses to one handshake RTT. Healthy, sleeping
  (idle-suspended) and stopped endpoints are strict no-ops; the call never
  blocks the gomobile thread.

No config surface is added; a pinned `listen_port` is still preserved. The
mechanism lives in the `wireguard-go` fork submodule
(`device/lx_giveup_rebind.go`). Verified synthetically (red/green stands,
races under `-race`, adversarial uphold 6/6); the field run of the nudge
waits for the LxBox `USER_PRESENT` receiver.

Upstream note: `upstream/testing` was force-pushed again after our 2026-08-01
merge; the honest subject-level comparison shows zero genuinely new commits,
so this rc carries no upstream delta.

#### v1.14.0-lx.18

Adds the VLESS `encryption` layer on top of `lx.17`, which stays as described
below.

**VLESS nodes using `encryption: mlkem768x25519plus…` now connect (SPEC 032,
feature
[VLESS_ENCRYPTION](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md)).**
This is a post-quantum handshake that lives inside VLESS, beneath the transport
and independent of TLS — not to be confused with REALITY's key exchange. The
field did not exist in our schema at all, so such configs were rejected by the
decoder and the nodes were simply unreachable. Where the layer is absent or set
to `none`, behaviour is unchanged.

The failure mode gave nothing away: the transport came up normally — a
WebSocket upgrade completed with `101`, a gRPC server answered its SETTINGS
frame — and then the peer tore the connection down with not one line in the
core log. What settled it was reading the *other* client's stored
configuration, where the same servers appear twice: once plain, once carrying
`mlkem768x25519plus.native.0rtt` with an ML-KEM-768 key. The dead ones here
were exactly the latter.

Rather than implementing the handshake from scratch, the client half is ported
from the sing-box fork at `starifly/sing-box`, which carries the same GPL-3.0
license and the same upstream base as this tree. Provenance is recorded in the
file headers. The server half (`decryption`) is deliberately not included —
this fork is client-focused. No new external dependencies.

Verified on device against the subscription that prompted it: nodes that had
all been dead came back at **6/8 over WebSocket and 4/4 over gRPC**, with no
other transport group moving — so the gain is attributable to the layer itself
rather than to anything incidental. The nodes still not answering in those
groups are placeholders in the subscription rather than servers: three entries
address `0.0.0.0` and serve as section headings in the node list, and two carry
a truncated 43-character key where a working node carries 1579. Against the
subscription's real nodes the layer works everywhere it applies.

Note for client authors: supporting this end to end takes both halves. The
field arrives inside a subscription as `settings.vnext[0].users[0].encryption`
but belongs on the sing-box outbound as a flat `encryption` field beside
`uuid`; a config builder that drops it leaves the core with nothing to act on.

#### v1.14.0-lx.17

First stable tag of the `lx.17` line — a promotion of `rc.1`–`rc.5` plus the
XHTTP fixes below, which never shipped in an rc. The rc sections stay as
written; this entry describes what is new on top of `rc.5`.

The headline is XHTTP `mode: auto` on REALITY servers — the shape most
subscriptions ship, and until now the one that did not work.

**XHTTP `stream-one` no longer sends a path the server refuses to route (SPEC
043, feature
[XHTTP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/002-XHTTP/FEATURE.md)).**
An XHTTP server normalizes its own path to end in `/` whenever the session id or
sequence number is carried in the path — which is the default — and then serves
only requests whose path starts with that prefix. Our `stream-one` trimmed the
trailing slash, so a node configured as `/api/v1/feed` was dialed at exactly
that, while the server was matching against `/api/v1/feed/`. No prefix match, a
`404`, and the dial hung until the URL-test timeout with nothing logged: no
error, no status, just silence. Every XHTTP node in a subscription looked dead.

`packet-up` was unaffected throughout, because its path continues into
`/<sessionId>` and therefore does carry the slash — which is what made the bug
look like broken mode resolution. It never was: `auto` resolves to `stream-one`
under REALITY exactly as Xray and other sing-box forks do, and that resolution
was correct all along. It simply led into the one broken branch.

The trim dates back to SPEC 011, where dropping the session id out of the
`stream-one` path also dropped the slash. Only the session id belongs gone — an
empty session id is precisely how the server selects the bidirectional branch —
whereas the slash is part of the normalized path. Both properties now hold at
once. Paths whose session id lives in a header, query or cookie are untouched
and still reach the wire exactly as configured, trailing slash included.

Confirmed on the wire against a prefix-checking HTTP/2 server: `REJECT 404:
"/api/v1/feed" lacks prefix "/api/v1/feed/"` before, `ACCEPT: "/api/v1/feed/"`
after — then verified on device against the live subscription that reported the
problem. Two unit tests had been asserting the broken shape and were corrected.

**Streamed-body XHTTP requests carry `Content-Type: application/grpc` (SPEC
042).** Xray sets this header on every request that carries a body — `stream-one`
and `stream-up` — and we never did. The `no_grpc_header` option, previously
accepted as a documented no-op, now genuinely suppresses it, matching Xray's
`NoGRPCHeader`. This restores parity with the Xray wire contract; on its own it
did not resolve the dead-nodes report above, and is shipped as a correctness fix
rather than as that cure.

**Also in this line, from `rc.1`–`rc.5`** (each described in its own section
below): `GetRunningConfig` no longer crashes the core on android/arm64 and
returns the config the running box was actually built from (SPEC 037/038);
report archives are pruned instead of growing without bound — 427 MB had
accumulated on one device (SPEC 039); `Endpoint.Close()` reports tun-device
close failures again; WG/AWG endpoints heal themselves after device sleep
instead of sitting in ERR until a manual reconnect (SPEC 041); system-stack TCP
survives having its listener closed underneath the core (SPEC 040); and roughly
245 upstream commits were merged across the line.

#### v1.14.0-lx.17-rc.5

Upstream sync on top of `rc.4`, which stays as described below. No `lx` changes
of our own — the fork's delta is untouched.

Five upstream fixes were picked up (`SagerNet/sing-box` `testing`, up to
`d2438c2`): a DNS race where a completed rule was blocked by an earlier armed
one, a routing loop to the exact TUN address on darwin, TLS fragment ACK waiting
on Windows without TCP estats, system-device DNS configuration for WireGuard
interfaces, and a naiveproxy bump to `v150.0.7871.63-1`.

The apparent gap was much larger — 215 commits — but `upstream/testing` is
force-pushed, so already-merged work reappears under fresh hashes. Comparing by
commit subject rather than hash showed only six genuinely new commits, five of
which cherry-picked cleanly. The sixth, an `Update sing-tun` bump, was
deliberately **skipped**: it points at a revision older than the one our
`sing-tun` fork is based on, so taking it would have rolled the TCP self-heal
work backwards.

#### v1.14.0-lx.17-rc.4

Adds two fixes on top of `rc.3`, which stays as described below.

**WG/AWG endpoints heal themselves after device sleep instead of staying in ERR
until a manual reconnect (SPEC 041, feature
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)).**
While the phone sleeps, the per-flow state of the tunnel's UDP 5-tuple dies on
the path (the NAT mapping expires and/or a DPI flow entry goes stale). Upstream
wireguard-go then retries handshakes into that same dead socket forever — same
source port, same dead 5-tuple — which is exactly what the field dumps showed:
receive workers alive for half an hour, socket never reopened, zero replies.
Reconnecting "fixed" it purely by opening a new socket with a fresh ephemeral
port. The device now does that by itself: when a peer's handshake retry cycle
exhausts (~90 s of unanswered initiations — the existing give-up event, which
only fires under traffic demand), the bind is reopened once with a fresh
ephemeral port and a new handshake is kicked immediately. For masquerade
profiles the `i1` decoy rides out with the first initiation of the new 5-tuple,
re-opening the flow on the DPI. Debounced to one rebind per give-up cycle; an
explicitly pinned `listen_port` is preserved (self-heal via port change is then
unavailable, by design); both bind paths (plain and `detour`) heal through the
same mechanism. Zero cost while healthy, asleep or closed: no timers, no
goroutines, no traffic — on a down device the rebind degrades to a no-op, so it
never fights idle-suspend (SPEC 020).

**System-stack TCP no longer dies forever when its listener is killed out from
under the core (SPEC 040, feature
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)).**
With `stack: "system"` every new TCP connection from the TUN is NAT-rewritten
onto a local forwarder listener. Its accept loop treated *any* `Accept` error as
terminal and silently returned — so when something else in the shared Android
process closed the listener's fd (a stray close on a reused descriptor number —
the LxBox §047 "browser dead, QUIC alive" failure), the stack kept running and
kept rewriting every new SYN onto a dead port. The OS answered each one with an
instant RST: every app got `ECONNREFUSED` in ~16 ms until the VPN was restarted,
while UDP, QUIC and DNS worked fine. Reproduced on device: ~1 in 8–36 fast VPN
restarts, worse on a "dirty" process — which is why it had floated uncaught for
months.

sing-tun is now a fork submodule (`submodules/sing-tun`, pinned at the exact
upstream revision from go.mod) with a single-file patch: an unexpected `Accept`
error is logged with the errno (which names the killer path), the listener is
recreated on the same address, the forwarder port is republished atomically, and
the loop keeps serving. A deliberate `System.Close()` stays silent as before.
If the rebind itself fails, the loop logs an error and gives up — no worse than
upstream. A recovery counter is kept as telemetry: if it ever ticks, the
fd-closing trigger on the client side is still alive.

#### v1.14.0-lx.17-rc.3

One-line fix on top of `rc.2`, which stays as described below.

**`Endpoint.Close()` reports tun-device close failures again.** Our teardown
support (SPEC 020) wrapped the final `tunDevice.Close()` in a nil check — the
device may already be gone when a torn-down endpoint is closed — but discarded
its error and returned `nil`, where upstream returns it. A device that failed to
close therefore looked like a clean shutdown in the logs. The nil guard stays;
only the error is propagated now, matching upstream. The endpoint manager already
wraps and logs this error, so the sole visible change is that a real failure is
no longer silent.

#### v1.14.0-lx.17-rc.2

Adds one fix on top of `rc.1`, which stays as described below.

**Report archives grew without bound — 427 MB of them on one device (SPEC 039,
feature
[HOTFIXES](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/004-HOTFIXES/FEATURE.md)).**
Every OOM and crash report goes into a fresh directory under `files/oom_reports`
/ `files/crash_reports`, and nothing ever deleted the old ones — upstream leaves
that to the client, which only cleans up what it has exported. A single report
is heavy (two pprof profiles plus a config copy, ~750 KB), so a recurring fault
quietly eats the disk. Found on device: **575 directories / 427 MB accumulated
over 19 days**, peaking at 94 reports in one day, none of them ever removed.

The archive is now trimmed before each new report is written, on both the OOM
and the crash path:

- **32 directories** and **64 MB**, whichever bites first. The count limit is
  what usually holds (32 × ~750 KB ≈ 24 MB); the byte budget covers a handful of
  unusually fat reports.
- Trimming targets `cap-1`, so the archive holds exactly the cap once the
  incoming report lands, rather than overshooting by one every time.
- Oldest go first, ordered by **modification time, not by name** — collision
  suffixes (`-1`…`-1000`) break lexicographic order, since `…-05-2` sorts after
  `…-05-10`, and a name sort would delete the wrong reports.
- Best-effort by design: this runs while the process is already dying, so any
  failure is skipped rather than propagated. Losing one report beats losing the
  report that mattered. Loose files sharing the directory are neither deleted nor
  counted against the budget.

Report format, naming and export are unchanged — only deletion of old reports is
new. Note this does **not** reclaim what has already piled up: rotation runs when
the next report is written, so an archive that grew before this build shrinks on
the next OOM or crash, not at upgrade time.

**240 upstream commits merged** — drift accumulated since the `beta.2` base and
is now closed (upstream has not tagged a release past `beta.2` yet, so the
reported base stays `v1.14.0-beta.2`). Notable for this fork: URLTest now *requires* a history
storage in the context instead of silently creating one, DNS gained
namespace/parallel `evaluate` support and client-subnet-aware caching,
`rule_set` matching semantics were simplified, JSON schema generation landed, and
a TUN dispatcher deadlock plus a local-DNS block on cancelled queries were fixed.
New upstream protocols (snell, usbip, openvpn, openconnect) are present in the
tree but stay out of the AAR tag set, as before.

Most of the 56 merge conflicts were textual, not semantic: our branch already
carried the upstream code from earlier merges, and upstream force-pushes
`testing`, so the same commits reappeared under new hashes against a stale
merge base. Two were real and are fixed here — a dropped `time` import that
broke the whole tree, and a duplicated DNS log function that upstream added
independently of ours. Fork-specific behaviour was re-verified against the merge
rather than assumed: idle-suspend still iterates endpoints, the detour tail still
reaches the client, WireGuard addresses still come from the cache rather than a
possibly-torn-down device, and the AAR tag set is unchanged.

One config-schema addition rides along: the `servers` field of a `group` DNS
server is now declared as a DNS-server reference, so upstream's new JSON schema
cross-links it instead of emitting a plain string list.

#### v1.14.0-lx.17-rc.1

Single fix, and it is a hard one: on Android, calling `GetRunningConfig`
killed the core outright. Cut as a release candidate because the fix changes
the libbox API and wants a device check from the client before promotion.

**`GetRunningConfig` crashed the core on android/arm64 (SPEC 038, feature
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).**
Every call ended in `fatal error: bulkBarrierPreWrite: unaligned arguments`
— a runtime `throw`, not a recoverable panic, so the process died and the
tunnel dropped. The RPC shipped in `rc.3` and in stable `v1.14.0-lx.16`,
which means the feature was unusable on Android in both.

The cause is the **return type**, not the RPC logic. `GetRunningConfig` was
the only exported `CommandClient` method returning a bare `(string, error)`:

- gomobile encodes a Go string as the C struct `nstring{void *chars; jsize
  len}` — a value **carrying a pointer**.
- cgo builds the callback's combined argument/result frame and marks it
  `__attribute__((__packed__))`, which drops the struct's alignment
  requirement to 1, so the C local lands 4-byte aligned on arm64.
- The generated Go wrapper assigns that pointer-bearing result slot, which
  compiles to a GC write barrier — and the barrier requires 8-byte
  alignment. 4 ≠ 8, so the runtime throws.

Every other method returns a refnum, an iterator or a scalar, none of which
puts a pointer in the frame — hence no barrier and no crash. Strings in
*struct fields* (`Rule.Type`, `PoolSlot.Tag`) are equally safe: those cross
as objects with getters.

**API change (breaking for clients):**

```go
// before — killed the process on Android
func (c *CommandClient) GetRunningConfig() (string, error)

// now
func (c *CommandClient) GetRunningConfig() (*RunningConfig, error)
func (c *RunningConfig) Content() string
```

Callers read the document via `.Content()`. The wire protocol, the proto
definition and the whole `daemon/` side are unchanged — the defect lived
purely in the libbox binding.

Note for anyone hitting this elsewhere: returning `[]byte` does **not** fix
it. That binds to `nbyteslice{void *ptr; jsize len}` — the same
pointer-in-a-packed-frame shape, the same crash. Only returning an object
removes the pointer from the frame. A reflection test now guards the entire
`CommandClient` surface against both return shapes, since any future method
with one would reintroduce the same kill.

Upstream `testing` had no new commits at cut time.

#### v1.14.0-lx.16

First stable tag of the `lx.16` line — a promotion of `rc.1`–`rc.3` with no
code changes on top of `rc.3`. Upstream is now on the **v1.14.0 beta** line
(`v1.14.0-beta.2` merged), so the fork resumes cutting non-prerelease tags.
Two features land here.

**DNS server type `group`: DNS resolution no longer dies with one failed
server (SPEC 033/034/035, feature
[DNS_GROUP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)).**
Previously `dns.final` was a *default*, not a fallback, and a rule routing to
a server returned its transport directly — any network error, timeout or
SERVFAIL failed the query outright even with healthy servers in the config.
A `group` puts several servers behind one tag with a selection strategy:

```json
{ "type": "group", "tag": "public",
  "servers": ["google", "cloudflare", "quad9"],
  "mode": "stable", "error_ttl": "2m", "win_ttl": "5m" }
```

Servers carry no states; two expiring record tables drive everything: an
**error** record (`error_ttl`, default 2m — written by any failed exchange,
erases the server's live wins) and a **win** record (`win_ttl`, default 5m —
only the first success of a fan-out; any success erases the server's live
errors). *Clean* = zero live errors; a network change amnesties both tables.

- `mode: stable` (default) — stickiness before randomness: stay on the
  current server while it is clean, re-elect a random clean one only when it
  is not. Server order is NOT meaningful; there is no return-to-primary.
- `mode: fastest` — the clean server with the most live wins; when nobody
  has one, the query becomes an election fan-out to all clean members
  (single-flight — a burst never multiplies fans). Re-election rhythm is
  `win_ttl` expiry. No timers, no synthetic probes.
- `mode: parallel` — every query fans to all clean members (N× traffic by
  design; no wins recorded).
- Unified flow: the single target gets HALF the remaining request budget —
  the rescue fan is guaranteed the rest, so a blackholed server can no
  longer eat the whole deadline. With **no clean member** every mode makes
  exactly one attempt via the least dirty server and never fans (anti-storm
  on a dead network).
- A group is a first-class server: accepted in `final`, in rules and inside
  other groups (cycles are rejected at load, not at runtime). `fakeip` and
  `hosts` members are rejected — local sources cannot fail over.
- Observability: the DNS query stream attributes each answer to the member
  that actually produced it, events carry `fanned` and `survival` flags on
  top of the probe trace, and `GetDNSGroups` returns the live records per
  member (clean, live errors + age, live wins, current, last rtt).
- The implementation survived a 24-agent adversarial review; all six
  confirmed defects (nil fan result, leaky Reset amnesty, election-window
  target trashing, and more) are fixed with regression tests.

Note for anyone who ran the **rc.1** pre-release: the rc.1 config contract
(`mode: failover|race`, `interval`, `down_time`) was replaced during the rc
line and such configs **fail to load** with an explicit error. The group was
redesigned around the TTL record model before any consumer shipped, so no
compatibility bridge is kept. Stable users are unaffected — `group` is new
in this tag.

**New RPC `GetRunningConfig`: the core now answers "what is actually
running" (SPEC 037, feature
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).**
Until now the core kept no config after start — `GetOutbounds` returns only
tag/type/delay, so after a profile edit without restart the client had no
source of truth for node details. The new unary RPC returns the canonical
JSON of the options the running box was actually built from: a **post-override
snapshot** (including what the service layer injected at start), captured
**once at service start**, so serving the RPC is a plain string handoff with
zero per-request work. Per-node JSON is derived client-side by extracting the
tag from this document. The document is a **re-marshal**, not the original
bytes (field order, omitempty, `[] → null`) — compare it with the stored
profile semantically, not as a textual diff. Behind `with_lx_command`; a
tag-less build answers `Unimplemented`, not-started → `FailedPrecondition`,
started without a snapshot → `Unavailable`.

Also in this line: the `lx_idle_teardown` explicit-`"0"` kill switch is now
distinguishable from an absent key (SPEC 020), docs were reconciled with the
code (AWG2 masquerade `id`/`ib`/`sip`, XHTTP GET fallback, DNS stream rcode
semantics), and upstream **v1.14.0-beta.2** is merged — upstream left alpha
(platform options restored, TLS/acme fixes, JSON schema, client-subnet DNS
cache, rule-level race/speculative evaluate actions).

#### v1.14.0-lx.16-rc.3

**New RPC `GetRunningConfig`: the core now answers "what is actually
running" (SPEC 037, feature
[OBSERVABILITY](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)).**
Until now the core kept no config after start — `GetOutbounds` returns only
tag/type/delay, so after a profile edit without restart the client had no
source of truth for node details. The new unary RPC returns the canonical
JSON of the options the running box was actually built from:

- **Post-override snapshot** — includes what the service layer injected at
  start (tun `auto_redirect`/package lists, the OOM-killer service), i.e.
  exactly what went into the box, not the profile text the client sent.
- Captured **once at service start** (same encoder as config formatting);
  serving the RPC is a plain string handoff — zero per-request work, zero
  cost when never called.
- **Per-node JSON is derived client-side** by extracting the tag from this
  document — "View details" / "Copy JSON" need no per-tag RPC.
- The document is a **re-marshal**, not the original bytes (field order,
  omitempty, `[] → null`): compare with the stored profile semantically,
  not as a textual diff.
- Behind `with_lx_command` as usual; a tag-less build captures nothing and
  answers `Unimplemented`. Not-started → `FailedPrecondition`; started but
  no snapshot (the attached-service path) → `Unavailable`.

No other changes vs rc.2; upstream `testing` had no new commits at cut time.

#### v1.14.0-lx.16-rc.2

**⚠️ BREAKING (vs rc.1 only): the DNS group config contract is replaced.**
`mode: failover|race` and the `interval`/`down_time` fields from rc.1 are
GONE and such configs now **fail to load** with an explicit error. The group
was redesigned around a TTL record model before any consumer shipped — no
compatibility bridge is kept.

**DNS group v2 — TTL record model (SPEC 033/035, feature
[DNS_GROUP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)).**
Servers carry no states; two expiring record tables drive everything: an
**error** record (`error_ttl`, default 2m — written by any failed exchange,
erases the server's live wins) and a **win** record (`win_ttl`, default 5m —
only the first success of a fan-out; any success erases the server's live
errors). *Clean* = zero live errors; a network change amnesties both tables.

```json
{ "type": "group", "tag": "public",
  "servers": ["google", "cloudflare", "quad9"],
  "mode": "stable", "error_ttl": "2m", "win_ttl": "5m" }
```

- `mode: stable` (default) — stickiness before randomness: stay on the
  current server while it is clean, re-elect a random clean one only when it
  is not. Server order is NOT meaningful; there is no return-to-primary.
- `mode: fastest` — the clean server with the most live wins; when nobody
  has one, the query becomes an election fan-out to all clean members
  (single-flight — a burst never multiplies fans). Re-election rhythm is
  `win_ttl` expiry. No timers, no synthetic probes.
- `mode: parallel` — every query fans to all clean members (N× traffic by
  design; no wins recorded).
- Unified flow: the single target gets HALF the remaining request budget —
  the rescue fan is guaranteed the rest, so a blackholed server can no
  longer eat the whole deadline. With **no clean member** every mode makes
  exactly one attempt via the least dirty server and never fans (anti-storm
  on a dead network).
- Observability: events now carry `fanned` and `survival` flags on top of
  the probe trace; `GetDNSGroups` returns the live records per member
  (clean, live errors + age, live wins, current, last rtt). The rc.1 trace
  field `racer` and the v2 state fields (winner/ranking/…) are replaced —
  they never shipped to a consumer.
- The implementation survived a 24-agent adversarial review; all six
  confirmed defects (nil fan result, leaky Reset amnesty, election-window
  target trashing, and more) are fixed with regression tests.

Also in this build: the `lx_idle_teardown` explicit-`"0"` kill switch is now
distinguishable from an absent key (SPEC 020), docs were reconciled with the
code (AWG2 masquerade `id`/`ib`/`sip`, XHTTP GET fallback, DNS stream rcode
semantics), and upstream **v1.14.0-beta.2** is merged — upstream left alpha
(platform options restored, TLS/acme fixes, JSON schema, client-subnet DNS
cache, rule-level race/speculative evaluate actions).

#### v1.14.0-lx.16-rc.1

**New DNS server type `group`: DNS resolution no longer dies with one failed
server (SPEC 033/034/035, feature
[DNS_GROUP](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/FEATURES/013-DNS_GROUP/FEATURE.md)).**
Previously `dns.final` was a *default*, not a fallback, and a rule routing to
a server returned its transport directly — any network error, timeout or
SERVFAIL failed the query outright even with healthy servers in the config.
A `group` puts several servers behind one tag with a selection strategy:

```json
{ "type": "group", "tag": "public",
  "servers": ["google", "cloudflare"],
  "mode": "failover", "down_time": "30s" }
```

- `mode: failover` (default) walks `servers` in order; a transport error,
  timeout or SERVFAIL marks the member down for `down_time` (subsequent
  queries skip it), NXDOMAIN and empty answers are valid responses. With
  every member down, each query makes exactly one attempt via the member
  whose failure is the oldest.
- `mode: race` picks the fastest member by racing a **real** query: the
  first query after the previous race aged past `interval` (default 3m)
  fans out to all live members, the first success answers the query and
  becomes the pinned winner; arrival order forms the fallback ranking.
  No timers, no synthetic probe traffic — idle costs nothing (ENERGY
  invariant), and only the winner's answer is cached.
- A group is a first-class server: accepted in `final`, in rules and inside
  other groups (cycles are rejected at load, not at runtime). `fakeip` and
  `hosts` members are rejected — local sources cannot fail over.
- The DNS query stream (`SubscribeDNSQueries`) now attributes each answer to
  the member that actually produced it; cache hits and total-failure events
  keep the group tag. The protocol schema is unchanged — existing clients
  are compatible as-is.

This build also merges upstream `testing` (31 commits), including rule-level
`race`/`speculative` evaluate actions, client-subnet-aware DNS cache, a TCP
DNS retry fix, search-domain expansion fix, JSON schema support and the
openconnect/openvpn DNS server types. Upstream's `race` orchestrates queries
*within one resolution* and holds no state between queries; the lx `group` is
a composite *server* with health memory (`down_time`, pinned winner) — the
mechanisms live on different layers and compose.

#### v1.14.0-lx.15

**XHTTP no longer breaks behind a reverse proxy when the session id is carried
off-path (SPEC 002).** A VLESS + XHTTP config routed through nginx/CDN with
`mode: packet-up`, a trailing-slash `path` (e.g. `/upload/`) and the session id
placed in a header (`session_placement: header`) failed to connect with
`unexpected download status: 301 Moved Permanently`, while the same config
worked in v2rayNG. The client was unconditionally stripping the path's trailing
slash for every mode. That is only needed for stream-one's bare path; when the
session id is not placed in the path, the base path reaches the wire verbatim,
so `/upload/` became `/upload` — and an nginx `location /upload/ {}` answers a
301 redirect to the bare path, which the download request (a raw HTTP/2
round-trip that does not follow redirects) surfaces as a dial error. Default
configs (session id in the path) were unaffected. The fix keeps the configured
path as-is and trims the trailing slash only on stream-one's bare-path request,
so reverse-proxy routing matches for every other mode. Covered by a new
url_test case; details in
[SPEC 002 §9](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/002-XHTTP_CLIENT_TRANSPORT/SPEC.md).

This build also merges upstream `testing` (13 commits: async DNS refactor, a
WireGuard detour fix that converges with our SPEC 029, the OpenConnect
auth-challenge rework, and assorted fixes). The lx observability, detour and
urltest layers were reconciled on top and verified against their test suites.

#### v1.14.0-lx.14

**Stopping the tunnel no longer hangs 10+ seconds with many WireGuard endpoints
(SPEC 030).** On Android, stopping an instance with ~30 WG/AmneziaWG endpoints —
especially just after a health-check ping woke them from idle-suspend — could
hang for ten seconds or more. The cause was an ordering interaction:
`box.Close()` tore endpoints down while the idle/urltest tick was still issuing
wakes, so each endpoint's close blocked waiting for an in-flight ping-wake to
finish a full device rebuild and handshake (up to several seconds each), and
those waits added up serially across every endpoint. The fix quiesces the tick
and closes every WireGuard UDP socket up front (so the per-endpoint teardown no
longer blocks on a socket read), makes an in-flight wake abort the moment its
endpoint starts closing, and closes endpoints concurrently instead of one at a
time. No teardown step is skipped — sessions are still closed cleanly, keys
zeroed, sockets and the userspace netstack released — only the pointless waiting
is removed, so there is no risk of the crash a hard "drop everything" would
cause. Stop now completes in a fraction of a second. Covered by unit tests for
the close-abort gate (red without the fix, green with it) and a smoke stand that
times the concurrent close of many live endpoints; the SPEC 020 idle-suspend and
SPEC 028/029 detour stands still pass.

#### v1.14.0-lx.13

**Fixed a WireGuard/AmneziaWG endpoint dying permanently when its `detour`
provider is declared later in the config (SPEC 029).** An endpoint with
`detour: X` resolved that detour eagerly inside its own constructor — a
UDP-egress-anchor type-assertion (SPEC 020) walks the dialer's `Upstream()`
chain, and the detour dialer's `Upstream()` resolves the detour on the spot.
Endpoints are constructed in config-array order and only registered at the end
of their own construction, so an endpoint whose detour pointed at a provider
declared *after* it resolved that provider before it existed, and the dialer
cached "outbound detour not found" forever — the tunnel then never sent a single
packet (repeating `connect to server: outbound detour not found: X` in the log).
Reordering the config so the provider came first masked it by luck. The core
already starts nodes in dependency order (a node isn't started until every tag
in its `detour` chain has started), but the resolution was leaking out of that
barrier into the construction phase. The fix stops resolving the detour at
construction (the egress-anchor probe is skipped when a detour is set — it never
applies through a detour anyway) and instead resolves it in `Start`, after the
dependency topo-sort has brought the provider up. Config array order no longer
matters; a genuinely missing detour now fails loudly at startup instead of being
silently cached. Covered by an end-to-end stand that declares the consumer
before the provider and asserts traffic flows (red without the fix, green with
it); the SPEC 020 idle-suspend and SPEC 028 nested-tunnel stands still pass.

**Nested tunnels through `detour` now work — stop forcing DF on the outer UDP
socket (SPEC 028).** A `wireguard`/AmneziaWG endpoint or a `masque` outbound
opens its real (bottom-of-chain) UDP socket through `common/dialer`, which by
default disables IP fragmentation (`IP_MTU_DISCOVER=IP_PMTUDISC_DO` on
Linux/Android, `IP_DONTFRAG` on macOS). For a nested tunnel the outer datagram
is routinely oversize — encapsulation adds WireGuard's ~32 bytes plus AWG's
per-packet `s4` junk on top of an already full-size inner packet — so with DF
forced the OS silently drops it (`sendmsg: message too long`) instead of
fragmenting, and the inner tunnel never comes up or comes up and cannot carry
data. This is why `masque`/`wireguard`/AWG chained through `detour` failed on
device while direct nodes over the same detour worked. The endpoint and the
MASQUE outbound now default to `UDPFragmentDefault=true` (the same opt-out
direct/hysteria2/tuic already use), so the OS fragments an oversize outer
datagram and the far end reassembles it. An explicit `"udp_fragment": false` on
the node restores DF; other protocols and listener inbounds are unchanged.
Covered by a socket-flag unit test (both bind paths, both directions of the
explicit override) and an end-to-end AmneziaWG-over-AmneziaWG-through-detour
stand exercising both the fits and the IP-fragmenting (same-MTU) regime with
TCP/UDP ping-pong and large-data in both directions.

#### v1.14.0-lx.11

**Removed the AmneziaWG-over-WireGuard guard (SPEC 007).** The guard refused to
start an AmneziaWG endpoint whose `detour` chain reached a WireGuard-based
endpoint, because that combination used to hang the kernel on Android. That root
cause is gone on the current graft: the re-graft of AmneziaWG 2.0 onto
`sagernet/wireguard-go` v0.0.5 (lx.8), the transport-padding overrun fix (SPEC
025), and the reserved-clear gate that repaired the detour path (SPEC 026, lx.9)
together let AmneziaWG-over-WireGuard come up and carry traffic. Verified
end-to-end on a two-process loopback stand (upper AWG endpoint with `jc/s4` +
ranged `h1..h4` detouring through a plain WireGuard endpoint; handshake,
keepalive and HTTP-over-socks all flow). Both guards are deleted — the static
Start-guard (`protocol/wireguard`) and the runtime selector/urltest guard
(`protocol/group/awg_selector_guard.go`), along with the `OutboundManager.ConsumersOf`
and `AmneziaWGSuspendable` adapter hooks and every guard test. SPEC 020
idle-suspend is untouched: it shares the transport `Suspend`/`Resume` and the
`suspended` flag, and the "a deliberately-stopped endpoint is never resurrected
by a dial" invariant still holds (its tests were reworded from `guardSuspended`
to `stopped`). An Android field-test is still owed — the historical hang was an
Android-only symptom the mac stand cannot fully reproduce. The matching app-side
gate in LxBox (§130) is removed in step.

#### v1.14.0-lx.10

**Merge: upstream `testing` (tun udpnat, endpoint-listen refactor, OpenVPN /
OpenConnect).** Pulled 15 upstream commits. The load-bearing part is an upstream
WireGuard endpoint refactor: the dialer interface was generalised
(`WireGuardListener` → `UDPListener`, now returning an egress flag), a UDP-NAT
config surface was added (`udp_mapping` / `udp_filtering` / `udp_nat_max`), and an
egress-anchoring path (`EgressPool` / `SetEgressProvider`) was introduced. Our AWG
`NewClientBind` path and SPEC 020 idle-teardown guards are preserved; the new
UDP-NAT fields flow through the SPEC 020 rebuild recipe. OpenVPN and OpenConnect
land as new upstream transports but are **not** enabled in the fork's tag set
(endpoint/server-capable, out of the client-focused scope — same stance as
`with_usbip` / `with_clash_api` on the AAR).

**Re-graft: wireguard-go egress-provider API onto the AWG2 base.** Upstream's
`StdNetBind` gained `SetEgressProvider`, so the fork's submodule (one revision
behind) failed to build. Applied the upstream delta and extended SPEC 026: the new
egress receive path re-introduced an unconditional reserved-byte clear, now gated
behind `hasReserved()` like the other five sites — so a small-padding AmneziaWG
magic survives when no WARP reserved value is set, while WARP-over-egress still
clears (its bind sets a reserved value). (SPEC 026 §3.2 registry: 5 → 6 sites.)

#### v1.14.0-lx.9

**Fix: the WARP reserved-byte clear destroyed AmneziaWG magic headers.** Bytes
1-3 of every received datagram (the Cloudflare WARP "reserved" field) were
zeroed unconditionally in every bind. AmneziaWG reads its magic header as
`Uint32(packet[padding:])`, so with a small `s1`/`s2`/`s4` padding (0-3) the
magic sits in bytes 1-3 and the clear collapses it out of the ranged `h1`-`h4`
window — the packet, handshake included, is dropped and the endpoint never comes
up. Plain WireGuard (types 1-4, bytes 1-3 already zero) and AWG with padding ≥ 4
are unaffected, which is why it went unnoticed. All five receive-side clears
(`bind_std` receiveIP, `msgx_darwin` ×2, `bind_windows` ×2) plus the detour
`ClientBind` receive now zero bytes 1-3 only when a WARP reserved value is
actually configured. WARP behaviour is unchanged. Red/green tests bring up a
device pair with zero padding over both bind paths and assert delivery. (SPEC 026.)

Also fixes a data race in `ClientBind.connect()`: the lock-free fast-path read
of the cached connection raced the locked write (the send and receive goroutines
call it concurrently at tunnel start). The field is now an `atomic.Pointer`; the
double-checked-locking logic is unchanged. `go test -race` is clean.

#### v1.14.0-lx.8

Promoted to stable. Same code as `v1.14.0-lx.8-rc.1`, device-verified on
Android (CPH2411): the AmneziaWG endpoint that crashed on every data packet
(`s4=60`) now carries traffic under load, and the AWG download counter no
longer double-counts. No code change from rc.1.

#### v1.14.0-lx.8-rc.1

**Fix: AmneziaWG transport padding (`s4`) crashed the whole process.** Any
AWG endpoint with `s4 > 0` aborted with `SIGABRT` on its first data packet —
device-verified (CPH2411, AWG endpoint with `s4=60`): `panic: runtime error:
index out of range [123] with length 76` in `RoutineSequentialSender`. The
injection paths (`InputPacket`/`InputPackets`) sized the outbound buffer with
no headroom for the random prefix that `s4` prepends in-buffer, so the
right-shift ran off the end. The alloc sites now reserve `paddings.transport`,
the manual byte loop is an overlap-safe `copy`, and a defensive grow drops
packets that cannot fit a single WG message instead of overrunning. A
red/green device-level test reproduces the exact crash and pins the fix.

Four further defects in the AWG graft, same "config value crashes a
send/receive goroutine" class, fixed alongside:

* **RX byte accounting doubled** — the re-graft pasted the `rxBytes` / timers
  block twice on the receive hot path, so every AWG download byte was counted
  twice (and `keepKeyFreshReceiving` fired twice per batch). Deduplicated.
* **Swapped `jmin`/`jmax` panicked the first handshake** — UAPI validates the
  junk fields only individually; an inverted pair fed a non-positive bound to
  `rand.Int`. Bounds are swapped when inverted.
* **Out-of-range `i1`–`i5` obfuscator lengths** — a negative length panicked
  on a slice bound, a huge one OOMed the handshake `make`. Lengths are bounded
  to `[0, MaxMessageSize]`.
* **Full-range magic header wrapped to a zero bound** — `end-start+1` computed
  in `uint32` wrapped to 0 for a `0-4294967295` range, panicking `rand.Int`;
  widened to `int64` before the arithmetic.

Plain WireGuard (`s4=0`) was never affected — the broken branch does not run.

#### v1.14.0-lx.7

**Idle-suspend level 3, promoted to stable.** Same code as `v1.14.0-lx.7-rc.2`,
device-verified on Android (CPH2411, 3 WG/AWG endpoints): the full cycle
`suspend (idle=43s) → teardown (slept=5m19s) → rebuild (by=dial)` ran exactly on
model, including an AmneziaWG endpoint with junk obfuscation. The goroutine
profile confirms each stage — every Device goroutine gone after the teardown,
all back (recv-workers included) after the rebuild — and the node serves traffic
through the fresh netstack (161–252 ms). Details: SPEC 020 `TEST_PLAN §L3
RESULT`; what the levels are and how to tune them: [lx-energy.md](lx-energy.md)
([RU](lx-energy.ru.md)).

* **Expectation check on RAM (measured, worth knowing).** Level 3 frees exactly
  what it owns — netstack, Device objects, goroutines. But the *global*
  `sing/common/buf` pool (`GetOutboundBuffer → buf.Get`) is process-wide and
  survives `Close` by design; on the test config it held 63% of the heap
  (23.4 MB), so tearing down 3 sleeping nodes barely moved the total
  (36.7 → 36.3 MB). The bulk of the RAM win still comes from **level 1**
  (recv-buffers: −134 MB with 8 nodes measured earlier); level 3 reclaims the
  netstack (~5.9 MB/node) and pays off with *many* nodes, not three.

#### v1.14.0-lx.7-rc.2

**Self-review of rc.1's level 3** — a line-by-line re-read of the teardown
implementation found three defects, all in paths a device test would hit:

* **Crash: `PortAddresses()` on a torn-down endpoint** — the L3 layer
  (sing-tun preferred routes) may ask for port addresses at any moment; they
  are now served from a cached copy instead of the released tun device
  (nil-dereference before).
* **Silent L3 downlink break after a rebuild** — the attached sing-tun return
  path lived in the device wrapper that `Rebuild()` recreated; it is now
  carried over (sing-tun knows nothing about our teardown cycle and never
  re-attaches).
* **Dial hang after a failed rebuild** — a half-rebuilt endpoint (fresh tun
  device, `Start` failed, e.g. peer-domain resolution offline) would block the
  retry forever on the device's one-slot event channel, under the wake mutex —
  hanging every dial through the node. A failed rebuild now rolls back to the
  clean torn-down state via an idempotent `Teardown()`, so the next dial
  retries from scratch.

Spec synced to as-built (the teardown gate needs no live-traffic re-check —
`idleAsleep` already guarantees it; documented why), docs gained the level-3
column (lx-energy RU/EN, lx-config). New tests pin all three fixes, including
the partial-rebuild rollback.

#### v1.14.0-lx.7-rc.1

**Idle-suspend level 3: sleeping endpoints are now released completely.**
`Down()` (levels 1–2) frees the recv-workers and silences the timers, but the
gVisor netstack (~5.9 MB per endpoint) stays alive. The new third window tears
that down too — a node that has been *asleep* long enough is closed outright and
rebuilt on the next dial. Model and timelines: [lx-energy.md](lx-energy.md)
([RU](lx-energy.ru.md)); mechanism: SPEC 020 §"Третий уровень".

* **New `route.lx_idle_teardown`** — how long an already-sleeping endpoint may
  stay asleep before it is torn down: device closed, netstack/peers/queues
  freed, only its config left in memory. Counted **from the moment it fell
  asleep** (not from the last dial), so the window does not depend on which
  threshold put it to sleep. Absent → defaults to `lx_idle_suspend_reachable`;
  `0` disables teardown; requires `lx_idle_suspend` (start error otherwise, like
  the reachable window).
* **Wake = rebuild.** The device and its netstack are one-shot objects (their
  Close runs under a `sync.Once` and closes channels), so waking a torn-down
  endpoint recreates them from the stored recipe and re-runs both Start stages —
  roughly 0.5–1 s on the first dial, versus the ~1 RTT a merely-suspended
  endpoint pays. Concurrent dials serialise on one rebuild; a failed rebuild
  (e.g. peer-domain resolution) leaves the state untouched so the next dial
  retries.
* **Invariants preserved.** A guard-suspended AmneziaWG endpoint is never torn
  down and never rebuilt by a dial (the AWG-over-WG guard now clears the
  teardown flag too, SPEC 007); pause/wake over a torn-down endpoint is a no-op
  rather than a nil-deref; `Close` is idempotent over any sleep depth; a dial
  reaching the transport with no device fails cleanly instead of panicking; the
  transport's `Close` now also releases the tun device (a teardown/rebuild cycle
  used to leak it).
* Why it pays: beyond the RAM, fewer live objects mean more headroom under
  `SetMemoryLimit` — i.e. less GC pressure for the endpoints that are actually
  in use.

⚠️ Pre-release: level 3 is unit-tested (including a real teardown→rebuild cycle
over a live gVisor stack) but **not yet device-verified** — live plan in SPEC 020
`TEST_PLAN §L3`. Levels 1–2 are unchanged and were device-verified in lx.5.

#### v1.14.0-lx.6

Small maintenance release: one reported log-noise fix, plus an upstream merge.

* **`urltest`: the legacy-`tolerance` warning no longer fires once
  `balancer.pool_tolerance` is set** ([#7](https://github.com/Leadaxe/sing-box-lx/issues/7)).
  The `round_robin` startup hint — *"tolerance is ignored in round_robin mode;
  use balancer.pool_tolerance"* — was unconditional: it appeared on every start
  even for configs that already carry `pool_tolerance`, i.e. exactly where the
  hint carries no information. It now fires only while `pool_tolerance` is unset
  (there the user plausibly still expects `tolerance` semantics). An explicit
  `pool_tolerance: 0` is indistinguishable from an absent one (numeric field) and
  keeps warning — deliberate: `0` means first-live-fill, where the delay ranking
  `tolerance` would imply genuinely does not happen. Behaviour of `round_robin`
  itself is unchanged; this is log noise only.
* **Upstream `testing` merged** — docs/changelog, tailscale doc touch-ups, a Farsi
  locale fix, and `boxdd` insecure-mode/locale churn (upstream's own desktop
  daemon, not shipped by this fork). No lx seam touched.

#### v1.14.0-lx.5

**Energy revision (stable)** — a multi-agent audit of the idle-suspend ×
urltest combination (26 adversarially verified findings) with every confirmed
defect fixed, plus two new opt-in knobs. The full model is documented in
[docs-lx/lx-energy.md](lx-energy.md) ([RU](lx-energy.ru.md)). Same code as
pre-release `v1.14.0-lx.4-rc.2`, promoted to stable after on-device
verification (CPH2411, LxBox v2.15.4: prior behaviour and the new
suspend/wake/probe semantics confirmed working live).

* **Fixed: screen-off/on (or a network change) permanently resurrected every
  suspended WG/AWG tunnel** — pause-wake now skips devices the idle/guard state
  machine holds down; waking stays dial-only.
* **Fixed: AWG-over-WG guard holes** — a cache-restored WireGuard selection
  slipped past the guard on app restart (Android kernel hang), and urltest
  groups (auto-switch, round_robin pool) had no guard at all. The Start walk
  now resolves groups through their current choice; both urltest paths guard
  before committing.
* **Fixed: idle-suspend could cut live connections** — an endpoint carrying an
  established download but evicted from the active route was downed
  mid-transfer. The tick now consults the device's established-TCP gauge
  (keepalive-immune) and a ≥4 KiB transfer delta before suspending; peers with
  `persistent_keepalive` still fall asleep.
* **Fixed: the 30-minute probe tail** — after a selector switched away, the
  abandoned group kept probing (and waking) its members until `idle_timeout`;
  probe cycles are now skipped while the group is unreachable.
* **New: `route.lx_idle_suspend_reachable`** — a second, longer idle window
  after which even *reachable* endpoints (pool members, the selected node,
  final, DNS detours) suspend; they wake lazily on the next dial (+1 RTT).
* **New: `urltest.passive_check`** — a recent successful TCP dial through a
  node counts as proof of liveness; while fresh, least_test skips whole probe
  cycles and the round_robin first-live path skips confirmed slots.
* Smaller fixes: first auto-selection now invalidates the reachability cache;
  DNS-server detours are seeded reachable (no Down/Up flap around quiet gaps);
  `listen_port` endpoints are never suspended; DNS resolves before wake;
  32-bit rotation-counter overflow (index panic) and uint16 `pool_tolerance`
  overflow; `pool_tolerance` validated ≤ 15000 ms; manual URLTest of a
  balancer group is force again; Touch/Close and tick-shutdown races.
* Specs 007/019/020 synced (incl. a full state-machine model with diagrams in
  SPEC 020); unit-tested with `-race` across tag combinations and
  device-verified via LxBox v2.15.4.
* Merges upstream `testing` (start-lifecycle fix, boxdd insecure mode /
  reworked data protection, tailscale Windows SSH sessions).

#### v1.14.0-lx.4

**NaïveProxy release** — it was broken on **both** desktop platforms and is now
fixed on both: Windows archives were missing `libcronet.dll`, and the darwin
binaries could never load cronet at all. Also merges upstream `testing`
(incl. a naive slow-open fix and a cronet-go bump).

* **darwin now builds with CGO on a macOS runner — NaïveProxy works there for
  the first time.** The darwin binaries were cross-compiled from ubuntu with
  `with_purego`, which loads `libcronet.dylib` at runtime — but cronet-go
  publishes **no macOS dylib at all** (its darwin lib modules carry only a
  static `libcronet.a`), so the tag was on and the outbound was dead. Darwin
  moved to its own `macos-latest` job with `CGO_ENABLED=1` and the tag set
  minus `with_purego` — upstream `build_darwin` parity — so `libcronet.a` is
  linked statically and there is nothing to install next to the binary. The
  job's verify step runs a `naive` config through `sing-box check` on the
  runner (arm64 natively, amd64 under Rosetta).
* **Windows archives now ship `libcronet.dll` next to `sing-box.exe`.** The
  desktop build uses `with_purego`: cronet is not baked into the binary — the
  loader dlopens `libcronet.dll` from the exe's directory at startup. The
  upstream "Extract libcronet.dll" packaging step was lost when the release
  workflow was ported, so **every previously released windows zip lacked the
  dll** and any config with a `naive` outbound failed at startup with
  `cronet: library not found`. The dll is now extracted from the cronet-go lib
  module pinned in `go.mod` (go.sum-verified — not upstream's `extract-lib`,
  which resolves the latest `go`-branch commit and can skew ahead of the purego
  bindings) and packed into the zip. Keep it next to `sing-box.exe`.
  Contract recorded in SPECS/004 (§2.4 per-target table + HISTORY.md).
* **Upstream `testing` merged** (real delta since the last integration; upstream
  force-pushed testing, so most of the 180 incoming commits were re-delivered
  content we already carried): netns/unshare support, windivert driver
  lifecycle hardening, `boxdd` platform daemon, OOM report, **naive slow-open
  fix**, hysteria2 realm `ip_version`/`port_mapping`, cronet-go bump
  (`CRONET_GO_VERSION` 98d539ce → 617d38f4) and dep bumps (sing-tun, sing-quic,
  sing-snell, tailscale, nftables), plus `boxdd` data protection (upstream's own
  desktop daemon — not part of what this fork ships). All lx seams re-anchored
  (SPEC 014/015 command extensions, SPEC 017 detour chain, SPEC 018 DNS stream,
  SPEC 020 idle-suspend, AWG plumbing); upstream's `with_usbip` AAR-tag addition
  not taken (client focus). The AmneziaWG graft needed no re-graft — upstream did
  not move the `wireguard-go` pin.

No runtime change to the AAR payload: this release is about how the desktop
binaries are built and packaged. Both `libbox.aar` and `libbox-legacy.aar` are
rebuilt from the merged base.

#### v1.14.0-lx.3

**Stable release** (published as "Latest", not a pre-release) — a promotion of
`v1.14.0-lx.3-rc.2`, **device-verified**. Functionally identical to that rc; no
runtime change since it.

This release carries two payloads accumulated across the rc line:

* **DNS-query stream re-architected onto the command multiplex** (from `rc.1`) —
  the §180 structured DNS stream (`SubscribeDNSQueries`) moved from a standalone
  client subscription to a first-class member of the CommandClient multiplex,
  laid out identically to `CommandConnections`. It now auto-reconnects with the
  profiler client and dies with it, fixing the field bug where the DNS stream went
  silent after the app was backgrounded (Doze) and never recovered while TCP/UDP
  kept flowing. Requires the matching LxBox client migration (task §261).
* **AmneziaWG re-grafted onto wireguard-go v0.0.5 + upstream merge** (from `rc.2`) —
  merges upstream `testing` (L3-forwarding, snell, bridge outbound) and rebases the
  AWG 2.0 obfuscation graft onto the wireguard-go bump it carried. SPEC 020
  idle-suspend was re-homed onto the new L3-forwarding endpoint API (the
  `resumeOnDial` wake guard moved to `WritePackets`, the single point every
  L3-forwarded packet transits). No config or behaviour change for AWG endpoints.
  The new `PlatformInterface` bridge methods (`usePlatformBridge`/`createBridge`)
  are stubbed off on the client — the Android VPN uses its single VpnService TUN,
  not a platform bridge.

Both `libbox.aar` and `libbox-legacy.aar` are shipped; the LxBox client builds
against this AAR (the bridge-interface stubs land with it).

#### v1.14.0-lx.3-rc.2

**Pre-release** — merges upstream `testing` (14 commits, incl. L3-forwarding,
snell, bridge outbound) and re-grafts AmneziaWG onto the wireguard-go bump that
came with it. Carries the `rc.1` DNS-multiplex payload forward unchanged. Ships a
new `libbox.aar`.

* **AmneziaWG re-grafted onto wireguard-go v0.0.5.** Upstream bumped
  `sagernet/wireguard-go` v0.0.3 → v0.0.5 for L3-forwarding (batched
  `InputPackets`, a size-based outbound buffer pool, Darwin batch UDP I/O). The
  AWG 2.0 obfuscation graft was rebased onto that base (submodule
  `e5feca7` → `1adc4c7`): 15 of 16 grafted files applied clean, only `send.go`
  conflicted on a single backpressure line. The `MessageEncapsulatingTransportSize
  = 0` invariant is preserved, so upstream's rewritten `InputPacket`/`InputPackets`
  and buffer pool compose with the obfuscation hooks without a manual re-weave.
  No config or behaviour change for AWG endpoints.
* **SPEC 020 idle-suspend re-homed onto the new L3-forwarding endpoint API.**
  Upstream replaced the direct-route endpoint interface
  (`PrepareConnection`/`NewDirectRouteConnection`) with a flow API
  (`PreMatchFlow`/`PortAddresses`/`PortMTU`/`AttachReturn`/`DetachReturn`/`JudgeFlow`).
  The idle-suspend wake guard (`resumeOnDial`) moved to `WritePackets` — the single
  point every L3-forwarded packet (including established flows that bypass
  `DialContext`) transits — so a suspended WG/AWG endpoint still wakes lazily on
  first traffic. `Down`/`Up`/`BindUpdate` are unchanged on v0.0.5; the mechanism is
  otherwise untouched.
* **Docs (SPEC 003, SPEC 020) rewritten to current-state methodology.** Both
  SPEC.md files now describe the current architecture top-down; the chronology
  (graft-base evolution, the idle-tick bug, the rejected GRO experiment, the
  v0.0.3→v0.0.5 delta) moved to per-spec `HISTORY.md`.

#### v1.14.0-lx.3-rc.1

**Pre-release** — DNS-query stream (SPEC 018) re-architected onto the command
multiplex. Ships a new `libbox.aar`; the LxBox client must migrate (task §261).

* **DNS stream is now a multiplexed command, uniform with connections** — the §180
  structured DNS-query stream (`SubscribeDNSQueries`) moved from a standalone
  client subscription to a first-class member of the CommandClient multiplex, laid
  out identically to `CommandConnections`: `addCommand(CommandDNS)`, a
  `handleDNSStream` on the shared client context, and `WriteDNSQuery` on
  `CommandClientHandler`. The stream now auto-reconnects with the profiler client
  and dies with it — no per-stream subscription, no bespoke reconnect. This fixes
  the field bug where the DNS stream went silent after the app was backgrounded
  (Doze) and never recovered, while TCP/UDP kept flowing. The core emission layer
  (`common/dnstrack`, `HasSubscribers` gate, event shape) is unchanged; only the
  client transport moved. Removes the standalone `SubscribeDNSQueries` client
  method, `DnsQuerySubscription` and `DnsQueryHandler`. Server/proto untouched.

#### v1.14.0-lx.2

**Stable release** (published as "Latest", not a pre-release) — a promotion of
`v1.14.0-lx.2-rc.1`, **device-verified**. Functionally identical to that rc; no
runtime change since it (only CI/docs commits — the SPEC 023 toolchain mirror).

The `rc.1` payload is a **correctness/stability pass** over the whole lx delta
(SPEC 022 deep-audit remediation) — no new features, no config changes. Two
behavioural fixes headline it:

* **MASQUE (h2): a stalled CONNECT could wedge the outbound forever** — the h2
  CONNECT-IP handshake ignored the dial context, so a peer that completed TCP+TLS
  but never returned the CONNECT HEADERS parked the dial with no timeout (and the
  outbound could then be neither reused nor closed). The handshake is now bounded
  by the dial ctx.
* **AmneziaWG: a guard-suspended endpoint could be resurrected by a dial** — the
  AmneziaWG-over-WireGuard guard (which prevents an Android kernel hang) now clears
  the SPEC 020 idle flag under the shared lock, so a guard-suspended endpoint stays
  down.

Plus the full SPEC 022 batch (IPv4 checksum over header options, an XHTTP reader
data race, DNS query events on fresh cache hits, `GetPool` nested-group delay, the
AmneziaWG `s4`-only MTU budget, robust MASQUE h3 login-failure matching) and doc /
comment / dead-code hygiene. Full register:
[`SPECS/TASKS/022-LX_DEEP_AUDIT`](../SPECS/TASKS/022-LX_DEEP_AUDIT/SPEC.md).

Build infrastructure: the musl router builds now restore the Chromium toolchain
from a durable release-asset mirror before falling back to `snapshot.debian.org`,
so a Debian-snapshot outage no longer blocks releases
([`SPECS/TASKS/023`](../SPECS/TASKS/023-MUSL_TOOLCHAIN_MIRROR/SPEC.md)).

#### v1.14.0-lx.2-rc.1

**Pre-release.** SPEC 022 deep-audit remediation — a correctness/stability pass
over the whole lx delta, no new features and no config changes. A full audit of
the fork's code (10 axes, adversarial verification) surfaced 27 real findings; 24
are fixed here, 3 skipped by design. Upstream base is unchanged (already carries
`v1.14.0-alpha.37`). **Not yet device-verified** — staged as a pre-release before
promotion to a stable `lx.2`; the two behavioural fixes below want a live check.

* **MASQUE (h2): a stalled CONNECT could wedge the outbound forever.** The h2
  CONNECT-IP handshake ignored the dial context, so a peer that completed TCP+TLS
  but never returned the CONNECT `:status` HEADERS parked the dial in `ReadFrame`
  with no timeout — and because establishment is serialized, the outbound could no
  longer be reused *or* closed. The handshake is now bounded by the dial ctx (a
  watcher trips the conn deadline on timeout/cancel and is retired before the
  long-lived read loop starts).
* **AmneziaWG: a guard-suspended endpoint could be resurrected by a dial.** The
  AmneziaWG-over-WireGuard guard (which keeps such an endpoint down because that
  combination hangs the Android kernel) shared state with SPEC 020 idle-suspend;
  if the endpoint was idle-suspended *before* the guard fired, the next dial woke
  it back up. `SuspendAmneziaWG` now clears the idle flag under the shared lock, so
  a guard-suspended endpoint stays down.
* **Other fixes.** IPv4 checksum recomputed over the full header incl. options
  (CONNECT-IP TTL path); a data race in the XHTTP stream reader removed; DNS query
  events now emitted on a fresh cache hit in the Lookup path (SPEC 018 parity);
  `GetPool` reads a nested-group member's delay under the right history key; the
  AmneziaWG MTU budget counts only `s4` (transport padding), not `s3` (cookie
  padding); the MASQUE h3 login-failure hint matches the TLS alert robustly; plus
  documentation corrections (`ip=sip` decoy shape, `s4`/MTU, `no_grpc_header`) and
  a batch of comment/dead-code hygiene. Full register in
  [`SPECS/TASKS/022-LX_DEEP_AUDIT`](../SPECS/TASKS/022-LX_DEEP_AUDIT/SPEC.md).

#### v1.14.0-lx.1

**First full release of the 1.14 lx line** (published as "Latest", not a
pre-release) — a promotion of the `rc.1`–`rc.22` series. Functionally identical
to rc.22 plus the new MIPS asset below; carries the whole rc-line feature set:
MASQUE CONNECT-IP outbound (Cloudflare WARP), AmneziaWG 2.0, XHTTP, native
CommandClient extensions, `urltest` round_robin + sticky, WG idle suspend, and
the static musl router builds. Upstream base is still `v1.14.0-alpha.*` —
pinned at the merge noted in the release notes header.

* **New release asset: `linux-mips-softfloat`** (big-endian MIPS — OpenWrt
  `mips_24kc`, e.g. Atheros AR93xx) — requested in
  [#6](https://github.com/Leadaxe/sing-box-lx/issues/6). Pure-Go static build:
  runs on musl/OpenWrt as-is, but **without NaïveProxy** — Chromium/cronet has
  no big-endian MIPS toolchain, so `with_naive_outbound`/`with_purego` are
  dropped for this target only. Everything else (AWG, XHTTP, MASQUE, Clash API,
  …) matches the desktop tag set.

#### v1.14.0-lx.1-rc.22

**Pre-release.** MASQUE diagnostics + the lx branch is now the release branch. No
behaviour change to the tunnel itself vs rc.21 — MASQUE (CONNECT-IP over h3/h2)
works exactly as before.

* **MASQUE: transport-phase debug logging.** The dial path now logs its phases
  (`establishing <h3|h2> tunnel to <server> (sni=…)` → `udp socket up, starting QUIC
  handshake` → `tunnel established` / `tunnel failed: <err>`), so a stuck dial is
  diagnosable from the core log alone — no goroutine dump needed. This pinpoints
  whether a hang is the UDP socket or the QUIC handshake: on networks that filter
  inbound UDP:443, h3 hangs in the handshake (ClientHello left, no ServerHello back)
  while h2 (CONNECT-IP over TCP:443) works — use `network: "h2"` there.

* **Release branch moved to `lx`.** rc-tags are now cut from the `lx` branch (which
  carries the full 1.14 line + all SPECs); release notes/links reference `lx`.

#### v1.14.0-lx.1-rc.21

**Pre-release.** New outbound: **MASQUE (CONNECT-IP / RFC 9484)** for Cloudflare WARP
(SPEC 021). Tunnels whole IP packets over HTTP/3 or HTTP/2 to a WARP endpoint via a
userspace gVisor stack — device-verified end-to-end (`warp=on`) on both transports.
No change to any existing feature; MASQUE is a new `type: masque` outbound, gated on the
already-shipped `with_quic` + `with_gvisor` tags (both in `LX_TAGS`).

* **`type: masque` outbound — CONNECT-IP over h3 and h2.** One outbound with a `profile`
  field (`cloudflare` default | `standard`) and a `network` transport selector
  (`h3` QUIC default | `h2` HTTP/2). The Cloudflare profile carries WARP's non-RFC
  quirks (`cf-connect-ip`, tolerates the missing Extended-CONNECT settings, ECDSA
  public-key pinning, WARP SNI/URI defaults). Key material (`private_key` / `public_key`
  / `ip` / `ipv6`) is taken ready from config — device registration stays client-side.
  Full config reference in [SPECS/021/CONFIG.md](https://github.com/Leadaxe/sing-box-lx/blob/lx/SPECS/021-MASQUE_CONNECT_IP_OUTBOUND/CONFIG.md).

* **h2 via a manual HTTP/2 framer, no extra dependency.** WARP never advertises
  `SETTINGS_ENABLE_CONNECT_PROTOCOL`, so the high-level h2 clients refuse the request;
  the h2 path is driven directly on `golang.org/x/net/http2`'s public `Framer` + `hpack`
  (already a dependency). `connect-ip-go` is vendored under `transport/masque/connectip`
  (ported onto `sagernet/quic-go`) rather than pulled as an external module.

* **Stateless when idle + self-healing.** The tunnel is an ephemeral session: after
  `idle_timeout` (default 5m) of no traffic it is fully torn down — gVisor netstack,
  pump goroutines and QUIC keepalive all released — and rebuilt lazily on the next dial.
  A dropped tunnel (WARP idle-timeout, GOAWAY, network change) likewise self-heals on
  the next dial. Both `idle_timeout` and `keep_alive_period` are configurable.

* **Hardening (post-implementation audit).** A single malformed inbound datagram no
  longer blackholes the tunnel (drop-and-continue); the paired pump can no longer leak
  on teardown; the ICMP "packet too big" reply quotes the pre-mutation header; the h2
  path bounds peer-declared capsule sizes and uses real flow-control backpressure; and
  the hot path reuses send scratch buffers instead of allocating per packet.

#### v1.14.0-lx.1-rc.20

**Pre-release.** A small XHTTP robustness fix, an upstream sync, and documentation
closing out the SPEC 020 no-GRO experiment. No behaviour change to the shipped
idle-suspend (rc.19) — `route.lx_idle_suspend` works exactly as before.

* **XHTTP: `uplink_http_method: "GET"` outside packet-up no longer aborts.** When a
  config sets `GET` as the uplink method but the stream mode is not packet-up (where
  GET is meaningful), the core now soft-falls-back to POST instead of failing the
  config load. A misconfigured method degrades gracefully rather than taking the
  whole instance down.

* **Docs — SPEC 020 no-GRO experiment recorded and REJECTED.** The idea of a global
  "GRO off + receive batch 8" as a simpler alternative to Down/Up idle-suspend was
  measured on a real device and rejected for three reasons (SPEC.md §14): the main
  Android RAM holder is `messageBuffers` (`PreallocatedBuffersPerPool`, ~100 MB),
  which does **not** depend on the receive batch (the batch-sized `bufsArrs` held
  only ~14 MB); the env switch never reaches Go's `os.Getenv` on Android; and a
  hardcoded batch=8 crashed at start (SIGABRT — `device.BatchSize()=max(bind,tun)`
  clamped back to 128 via the TUN offload). Down/Up (rc.19) stays the only viable
  mechanism. Report + raw pprof/crash artifacts under
  `SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/ANDROID_RESEARCH/nogro-experiment/`; the
  experiment code was NOT merged (kept only on the now-deleted `-nogro-*` branches,
  documented for the record).

* **Upstream sync.** Merged `upstream/testing`: "Fix udpnat2 buffer size" (go.mod/go.sum
  bump) and "release: Fix update apple version script". No lx zones touched.

#### v1.14.0-lx.1-rc.19

**Pre-release.** Gates **idle-suspend (SPEC 020) behind a new build tag
`with_lx_idle_suspend`** and confirms the memory win **on real Android hardware**.
Idle-suspend (rc.18) frees the recv-worker `bufsArrs` of idle+unreachable WG/AmneziaWG
endpoints — but those buffers are only large where `BatchSize=128` (Android/Linux). This
release makes that platform scope explicit in the build.

* **`with_lx_idle_suspend` — mobile-only build tag.** The idle-suspend tick now compiles
  only with this tag, which is baked into the **Android/iOS AAR** (`build_libbox`) but
  **not** the desktop/CLI `LX_TAGS` (`Makefile.lx`). A desktop build has a small
  `BatchSize`, so the feature would save almost nothing there; to prevent a silent
  mismatch, a binary built **without** the tag that is handed a config with
  `route.lx_idle_suspend` now **fails fast at start** — `route.lx_idle_suspend is set but
  this build lacks idle-suspend support; rebuild with -tags with_lx_idle_suspend
  (mobile-only feature)` — instead of a silent no-op. The gate is a single function
  (`startIdleSuspend`), so the dial hot path and the upstream group files are untouched.
  When the option is unset the tag is a clean no-op either way (byte-for-byte upstream
  behaviour).

* **On-device Android verification (closes the RESEARCH.md device gap).** Measured on a
  physical CPH2411 (Android 15, arm64) via the app's pprof passthrough: with 9 WG
  endpoints (1 reachable + 8 idle/unreachable), suspending the 8 dropped
  `PopulatePools.func3` (`bufsArrs`) live heap from **223.9 MB → 89.9 MB (−134 MB,
  −60 %)** and recv-worker goroutines **18 → 2**, matching the `~8.4 MB/worker` model
  exactly — roughly 10× the desktop RSS delta, on the platform the feature was built for.
  Suspend/wake/no-flap/kill-switch all confirmed on-device. Full report + raw pprof
  artifacts in `SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/ANDROID_RESEARCH/`.

* Docs: `SPEC.md` rewritten as the as-built implementation spec; the original
  root-cause/measurement doc renamed `SPEC.md → RESEARCH.md`; `lx-config.md` (+ ru)
  document the new tag and its mobile-only scope. Added unit tests for the no-tag stub
  (option set → error, unset → no-op) plus reachability tests for the production
  nested-group topology.

* CI: pin `gh release create --repo` to this fork — the base-version step adds an
  `upstream` remote for git-describe, which made `gh` target SagerNet/sing-box (403).

* Upstream: synchronised — `upstream/testing` is 0 commits ahead of this base.

#### v1.14.0-lx.1-rc.18

**Pre-release.** Adds **idle-suspend for WireGuard / AmneziaWG endpoints** (SPEC 020) — on
Android, many live WG/AWG endpoints pin the CPU because each holds a recv-worker `bufsArrs`
(`128 × 65535 × 2` per device ≈ 16 MB) that keeps the Go GC scan-bound even with no traffic.
Idle-suspend brings **idle *and* unreachable** endpoints `Down` to free those buffers, and
wakes them on the next dial. Opt-in via one route field; off by default (byte-for-byte
upstream behaviour when unset).

* **`route.lx_idle_suspend` — suspend idle, unreachable WG/AWG endpoints.** Set it to a
  duration (e.g. `"5m"`); `0`/omitted disables the feature (kill-switch). An endpoint is
  suspended only when it is **both** idle past the threshold **and** unreachable from the
  active routing tree — i.e. nothing can currently route to it: not the `final` outbound, not
  a routing-rule target, not a selector's active pick, not a member of a `urltest` group's
  current pool, and not detoured-to by any of those. A suspended endpoint's recv-workers exit
  and its `bufsArrs` are freed; the next dial through it wakes it (`Up`, a fresh handshake —
  expected, the Down model zeroes the crypto session). Edge-triggered INFO logs
  `lx idle: suspend <tag>` / `wake <tag> by=dial`.

* **Reachability is resolved by an event-driven walk, not per-tick.** The reachable set is
  recomputed only when the active routing tree changes — a selector switch, a `urltest`
  auto-switch / pool rebuild, or a config reload each invalidate a cached set; between events
  the idle tick does one comparison per endpoint. The walk descends selectors via `Now()`,
  a `round_robin` pool via its whole active set, and static detours transitively, with a
  cycle guard.

* **Why suspend and not a smaller receive batch.** SPEC.md's first-cut fix was to shrink
  `StdNetBind.BatchSize()` (128→8) to make `bufsArrs` smaller. Code recon of the wireguard-go
  submodule rejected it: GRO receive is enabled on Android and its coalesced-packet split
  hardcodes `IdealBatchSize`=128 (the message array must hold a 64-datagram expansion), so a
  smaller batch panics / overflows and GRO can't be dropped (it's needed for download
  throughput, §010). `Down` is the only lever that frees the buffers whole while leaving the
  active node's batch — and its GRO — intact.

* **Concurrency & correctness.** The idle goroutine is stopped and joined before endpoints are
  torn down (no use-after-close race on the device); `SuspendIfIdle` and the dial-path wake are
  mutually excluded by a per-endpoint mutex (no dial-lands-on-a-suspending-device drop); the
  legacy `least_test` cold-start auto-switch now invalidates reachability (so a freshly-selected
  active node is never wrongly suspended); AWG-over-WG guard-suspended endpoints are never
  idle-woken. Unit tests cover the walk, the idle logic, the event cache, and the
  endpoint-manager iteration seam.

* **Device-verified (2026-07-01, all 8 test nodes):** suspend fires for idle+unreachable
  endpoints and never for reachable ones (final / rule target / selector pick / urltest pool);
  wake-on-dial works; a selector switch dynamically re-suspends the deselected node and wakes
  the new one; the kill-switch (`0`) runs zero ticks. Resource win measured: recv-worker
  goroutines 16→0, RSS −31%. See `SPECS/020-MULTI_WG_IDLE_BUFFER_HEAT/TEST_PLAN_idle_suspend.md`
  §RESULTS.

#### v1.14.0-lx.1-rc.17

**Pre-release.** Fixes a build-tag regression that shipped in every desktop/CLI release
since rc.1: `with_clash_api` was dropped from **all** platforms, not just the Android AAR
it was meant for. No data-path change; desktop binaries only.

* **Desktop/CLI binaries get the Clash API back.** SPEC 014 dropped `with_clash_api`
  because LxBox (Android) manages the core over the native libbox `CommandClient` — so on
  the **AAR** the Clash REST server is dead weight. But the drop landed in the shared
  `Makefile.lx` `LX_TAGS`, which also feeds every desktop/CLI release build
  (mac/windows/linux-musl, via `make -s lx-print-tags`). A CLI binary has **no native
  CommandClient channel** — it is driven by external dashboards (yacd, MetaCubeXD,
  clash-dashboard) over the Clash REST API — so dropping the tag left desktop users with
  no way to manage the core: a config with `experimental.clash_api` failed fast. CI stayed
  green the whole time (`lx-ci.yml` kept `with_clash_api` in its check tags), so the bug
  was invisible outside the release artifact. **Fix:** `with_clash_api` is restored to the
  desktop `LX_TAGS`; the AAR tag set (`build_libbox`) still drops it. The two sets now
  diverge by design — desktop = with Clash API, AAR = without. Verified: the desktop
  binary builds with `with_clash_api`, `check` accepts an `experimental.clash_api` config,
  and the Clash REST server comes up live (all endpoints answer instead of the stub's
  fail-fast).

#### v1.14.0-lx.1-rc.16

**Pre-release.** Full client-side support for the extended Xray/sing-box-extended **XHTTP**
parameters (SPEC 002 v2), on the existing lean-native client — no Xray vendoring. The default
(non-obfs) wire shape is unchanged and stays byte-identical to the live-verified v1, so existing
configs behave exactly as before; the new fields are all opt-in.

* **12 client-relevant XHTTP params + 2 tuning fields.** On a `xhttp` transport you can now set:
  - **session/seq placement** — `session_placement` / `seq_placement` (`path` default, or
    `query`/`header`/`cookie`) with `session_key` / `seq_key`.
  - **uplink-data placement** — `uplink_data_placement` (`body`/`auto` default, or `header`/`cookie`
    with chunked base64) + `uplink_data_key` + `uplink_chunk_size`.
  - **`uplink_http_method`** — upper-cased; `GET` allowed only in packet-up.
  - **X-Padding obfuscation** — `x_padding_obfs_mode` + `x_padding_placement`
    (`cookie`/`header`/`query`/`queryInHeader`) + `x_padding_key` / `x_padding_header` +
    `x_padding_method` (`repeat-x` or `tokenish`, the latter HPACK-Huffman-length-tuned).
  - **packet-up tuning** — `sc_max_each_post_bytes` (POST split threshold) and
    `sc_min_posts_interval_ms` (anti-burst throttle).
  Range fields use the `"min-max"` string form. Four server-only fields
  (`server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts`, `sc_stream_up_server_secs`)
  and the legacy `sc_max_concurrent_posts` are accepted but ignored by the client.

* **Wire-protocol audit: 0 confirmed mismatches.** Every new param was checked byte-for-byte
  against `PARAM_MAP.md` (an audit of Xray-core `splithttp` + sing-box-extended) — base64 variant
  (`RawURLEncoding`), chunk naming (`X-Data-<i>` / `x_data_<i>`), default keys (`X-Session` header
  vs `x_session` query/cookie), path-segment order, and the `["none"]`-style defaults all match
  Xray's normalizers. 16/16 unit tests, `sing-box check` on the full-obfs config, `go vet`/`gofmt`
  clean.

* **Verification status.** The default path (packet-up + `auto`→stream-one on reality) is
  **live-verified** on 4 real public nodes (1 MB download each), which also closes the task-011
  stream-one TODO. The non-default obfs/placement modes are covered by unit tests + `check` + the
  wire audit but are **not yet live-tested** against an Xray server configured for them (no public
  node uses them).

Also merges upstream/testing (version bump, linux ping fix).

#### v1.14.0-lx.1-rc.15

**Pre-release.** Device verification of SPEC 019 v2 `round_robin` on a real pool surfaced
**three** bugs, all fixed here. The headline one: with the default `sticky_hash:
["process","domain"]`, **every connection collapsed onto a single pool node** — the domain
component was always empty at selection time, so the key degenerated to the process alone and one
browser pinned all its traffic to one slot. On a device this measured 28/1/1 across a 3-node pool
(uniformity 0.27); after the fix the same traffic spreads across the pool (0.95+).

* **The sticky key's `domain` component was always empty, collapsing all traffic to one node.**
  The router resolves a domain destination to an IP and overwrites `metadata.Destination` *before*
  a group's `DialContext` runs, so `destination.Fqdn` is empty when the balancer builds the sticky
  key. `stickyComponent("domain")` read `destination.Fqdn` and got `""`, so a single process's key
  was `process + "\0"` for every site — one fixed slot. The original domain survives in
  `metadata.Domain` (set by sniffing / reverse mapping); the key now reads that, falling back to
  `destination.Fqdn` only for a direct dial. `dest_ip` was unaffected (the IP *is* present at dial
  time), so adding it to `sticky_hash` is a viable workaround on an already-built core.

* **Living pool nodes could change slot index during a health-check, moving sticky keys.** The
  SPEC invariant is *replace-in-slot*: a living node always keeps its exact slot index; only an
  evicted slot's occupant changes (sticky binds keys to `slot[hash(key) % pool]`). Two code paths
  broke it:
  - `balancePoolFirstLive` (`pool_tolerance: 0`) rebuilt the pool with a filtering `append`, so a
    transiently-dead slot left no placeholder and every living node *after* it shifted left one
    slot. On a device this produced a single `DE→FI→DE` outlier for one key.
  - `planTolerantPool` (`pool_tolerance > 0`) did `delete(inPool, occupant)` on eviction, letting
    the evicted-but-living node re-enter a *later* slot — relocating it (and cascading across
    slots from one fast newcomer).
  - the manual `URLTest` rebuild ran through the tolerant planner even at `pool_tolerance: 0`,
    re-ranking a stable first-live pool by delay and reshuffling living nodes.

  All three now preserve slot index (fixed-length `copy(current)`, only dead/empty slots rewritten
  in place; manual rebuild at `pool_tolerance: 0` uses a dedicated first-live planner). Regression
  tests pin survivors to their slot indices (they fail against the pre-fix code).

* **Stickiness could not be disabled — `sticky_hash: []` was silently ignored.** The design used a
  bare `[]` to mean "off" (vs. omitted = default), relying on `encoding/json` distinguishing a nil
  slice from an empty one. But the sing-box config decoder (`badjson.UnmarshallExcludedContext`)
  re-marshals each outbound, and an empty JSON array does not survive that round-trip — it arrives
  as nil, indistinguishable from "omitted", so the default `["process", "domain"]` always kicked in
  and a `round_robin` group never actually rotated. A local run pinned every connection to one node
  until this was found. **Fix:** disabling stickiness now uses the explicit sentinel
  `sticky_hash: ["none"]`, which survives any re-marshal. Omitted or `[]` → default; `["none"]` →
  off (pure even rotation); `"none"` mixed with a real component → error. Confirmed locally:
  `["none"]` rotates 10/10/10, `["domain"]` pins each domain to one node.

#### v1.14.0-lx.1-rc.14

**Pre-release.** Desktop smoke-test follow-up to rc.13's SPEC 019 v2. No behaviour change to
the pool/sticky logic — one validation fix found by running the rc.13 binary locally.

* **`balancer.pool: 0` is now the default, not an error.** A Go `int` with `omitempty` cannot
  distinguish `0` from an omitted field, so `pool: 0` reaching `< 1` validation rejected a
  config that should have used the default. Now `pool` `0`/omitted → default `3`; only a
  negative `pool` is rejected. Verified on the rc.13 desktop binary alongside the round_robin
  pool fill (`pool_tolerance: 0` tests only pool-many nodes; `> 0` tests all), config
  fail-fast, and live routing.

#### v1.14.0-lx.1-rc.13

**Pre-release.** Reworks `urltest` `round_robin` (SPEC 019 v2) to scale to large node lists
(hundreds–thousands) and exposes the rotation pool to clients. **Breaking** for the rc.11/12
`round_robin` shape — config moved under a `balancer` object; `least_test` (the default) is
untouched. Not yet device-verified.

* **Fixed-size rotation pool.** `round_robin` no longer rotates over *all* live nodes (which
  meant URL-testing every node each interval — unworkable at 1000 nodes). Instead it keeps a
  fixed pool of `balancer.pool` nodes (default 3) and rotates only within it. The pool is
  lazily health-checked: with `pool_tolerance: 0` (cheap mode) the core tests no more nodes
  than needed to keep the pool full of live nodes and then stops; with `pool_tolerance > 0`
  it tests all nodes and keeps the fastest, evicting a pool member only when an outsider beats
  it by more than the tolerance (ms). A dead pool node keeps its slot until a live replacement
  is found — the pool never empties. A dial error never changes the pool (the cause — dead
  node vs. dead destination vs. local network drop — is unknowable from one failure); only the
  health-check does.
* **New config shape.** Balancing options moved into a `balancer` object on `urltest`:
  `{ "mode": "round_robin", "balancer": { "pool": 3, "pool_tolerance": 0, "sticky_hash": ["process","domain"] } }`.
  `balancer` is only valid with `round_robin` (error otherwise); the upstream `tolerance`
  field is ignored in `round_robin` (warned). `sticky_hash` omitted → defaults to
  `["process","domain"]`; explicit `[]` disables stickiness.
* **Sticky via fixed slots (strict zero reconnects).** Stickiness now binds a flow's key to a
  fixed *slot index* (`slot[hash(key) % pool]`), not a node position. Because slot indices
  never move and a replacement takes the exact slot it evicts, a node that stays in its slot
  keeps **all** its keys when other slots churn — zero needless reconnects, zero per-key state
  (no table to grow or sweep). Replaces both rc.11 sticky mechanisms (`jumphash` over the live
  list — which broke on mid-list eviction — and `ttl_map`), which are removed.
* **`GetPool` RPC.** New `CommandClient.GetPool(groupTag)` returns the current pool — one
  `PoolSlot{slot, tag, delay}` per slot — so the client can show which N nodes are actually in
  rotation, with their delays, instead of the full config list. `delay` is `0` only for a
  dead/unmeasured node (a live sub-ms node is clamped to 1). A non-`round_robin` group returns
  an empty pool, not an error. Additive proto/libbox, behind `with_lx_command`.
* **`least_connection` dropped** from the roadmap: `round_robin` is statistically even, and
  per-node active-connection counting (with decrement-on-close, leak risk) was not worth its
  complexity.

#### v1.14.0-lx.1-rc.12

**Pre-release.** One UI-facing fix on top of rc.11's SPEC 019 load-balancing: the
`urltest` group now reports the node it actually dials during the cold-start window,
instead of showing blank. No data-path change.

* **`urltest` `Now()` cold-start fallback** (SPEC 019). Before the first URL-test fills
  the delay history, `selectedOutbound*` is still nil — but traffic already flows through
  the `Select()` fallback (the first usable outbound). Previously `Now()` returned `""` in
  that window, so the UI showed no server even though connections were live. `Now()` now
  falls through to `Select(tcp)`/`Select(udp)` and reports the same node the next
  `DialContext` will pick — the same source of truth the dial path uses, not a guess. Only
  `least_test` (the default) is affected; `round_robin`/`ttlmap` already reported the
  last-picked tag and are unchanged. (Note: when the process is not unloaded from memory —
  a fast config restart on Android — the in-memory history survives, so `Now()` shows the
  prior pick immediately with no warm-up; that's upstream behaviour.)

#### v1.14.0-lx.1-rc.11

**Pre-release.** Adds load-balancing to the `urltest` group (SPEC 019, live-verified on
5 vless nodes — see `SPECS/019-URLTEST_MODE_STICKY/TEST_REPORT.md`) and lands the SPEC 016
connections-map mutex. No data-path change for existing configs — `urltest` without `mode`
behaves exactly as before.

* **`urltest` gains a `mode`.** `least_test` (default, unchanged: pick the lowest-delay node)
  plus `round_robin` (rotate across the live nodes — those with a fresh URL-test result that
  support the network). Selection runs once per connection, so a UDP/QUIC session stays on one
  node; the existing health ticker is the single source of liveness; when nothing is live the
  first usable outbound is the fallback. `least_connection` is reserved (phase 2) and currently
  rejected at config time. Implemented as a separate branch in `DialContext`/`ListenPacket`
  (`protocol/group/urltest_balance_lx.go`) so the legacy `selectedOutbound*` cache path is
  untouched.
* **`sticky` binds one flow to one node.** Optional object `{mode, timeout, cap, hash}` for the
  balanced modes. `hash` selects key components (`process`, `domain`, `source_ip`, `dest_ip`,
  `dest_port`), concatenated in order; an absent component contributes `""`, and an all-empty
  key maps to a single fixed node so keyless flows never rotate. `mode` is `jumphash` (default,
  stateless consistent hash over the live set — adding/removing a node remaps only ~1/n of keys)
  or `ttlmap` (a `key→node` table with lazy + ticker eviction, a `2000`-entry LRU cap, and a
  `10m` TTL; a dead bound node re-pins to a survivor). `Now()` reports the last-picked tag in
  balanced modes.
* **SPEC 016 — `sync.Mutex` on `Connections`** (libbox `command_types.go`). The client-side
  connections accumulator raced its `connectionMap` across per-subscriber goroutines
  (`ApplyEvents` iterating while another writes → `fatal error: concurrent map iteration and map
  write` → process abort with ≥2 `CommandConnections` subscribers). Guarded all map/slice
  mutators; `FilterState` split into public-locks / private-body for non-reentrancy; `Iterator`
  returns a copy so the gomobile caller walks a snapshot, not a live slice. Verified with a
  writer-∥-3-readers race test.

#### v1.14.0-lx.1-rc.10

**Pre-release — not device-verified.** Adds the DNS server + outbound channel to the
DNS-query stream (SPEC 018, LxBox feedback) and gates event construction on an active
subscriber. Behind `with_lx_command`; no data-path change.

* **`DnsQuery` now carries `dnsServer` / `dnsServerType` / `outbound`.** A DNS rule selects a
  *server* (`dns/router.go` matchDNS by `action.Server`), not an outbound; the channel a query
  goes out through is the server's own detour, fixed at config time. `dnsServer` = the
  resolving transport's `Tag()`, `dnsServerType` its `Type()` (udp/tls/https/quic) — both
  available on every emit path because `transport` is the `Exchange` parameter. `outbound` is
  the server's detour tag, captured once at transport creation (`TransportAdapter.OutboundTag()`
  from `DialerOptions.Detour`); the server expands a selector tag to its live node via `Now()`
  when streaming to a subscriber (consistent with `Connection.Detour`, SPEC 017). Empty on
  cached/optimistic — the query never left the device.
* **Events are built only when a profiler is attached.** `dnstrack.Manager` now tracks live
  subscriptions; the emit sites check `HasSubscribers()` before constructing anything. Without
  an open `SubscribeDNSQueries` stream the DNS hot path does zero work — no event, no answers
  slice, no outbound lookup (previously every resolution built an event that was then dropped
  for lack of a listener). The selector `Now()` resolution thus never touches the hot path.
* Wire: additive `dnsServer`/`dnsServerType`/`outbound` on `DnsQueryEvent`; new `OutboundTag()`
  on the `DNSTransport` interface (satisfied by the embedded `TransportAdapter`, no per-transport
  change). libbox `DnsQuery.DNSServer`/`DNSServerType`/`Outbound()`. No client change needed for
  §180 — fields the client reads as soon as the core fills them.

#### v1.14.0-lx.1-rc.9

**Pre-release — not device-verified.** Fixes DNS-query attribution (LxBox §180-2: 0/119
events had a package) and the answer `rdata` format. SPEC 018; behind `with_lx_command`.

* **DNS queries now carry `ProcessInfo`.** On a TUN VPN, DNS is hijacked on a fast-path
  (`route/route.go` — TUN+DNS protocol returns before `matchRule`), and `searchProcessInfo`
  (which fills `metadata.ProcessInfo`) lives *inside* `matchRule`. So every fast-path DNS
  query reached the resolver — and the `SubscribeDNSQueries` emit — with a nil ProcessInfo,
  i.e. unattributed (the bulk of DNS on a VPN, especially UDP). Now `searchProcessInfo` is
  called before both fast-path hijacks (stream + packet). It's idempotent and cached
  (`findProcessInfoCached`), so the cost is one lookup per flow. This corrects SPEC 018's
  пункт 3, whose earlier "cached attribution is correct" claim was wrong — it checked the
  ctx was consistent inside `Exchange` but not that ProcessInfo was populated before it.
* **`DnsAnswer.rdata` is now the bare value.** It was the full RR string
  (`"google.com. 29 IN A 64.233.165.139"`) from `RR.String()`; now the header prefix is
  stripped so clients get `"64.233.165.139"` / the CNAME target directly, no last-field
  parsing. CNAME chain order unchanged.
* No proto/wire change; rc.7 contract intact. LxBox §180 needs no client change — both fixes
  populate fields the client already reads.

#### v1.14.0-lx.1-rc.8

**Pre-release — not device-verified.** Fixes SPEC 018 `SubscribeDNSQueries` returning
`Unimplemented` on device (LxBox §180) — the DNS stream never delivered a single event in
rc.7. Behind `with_lx_command`; no data-path change.

* **DNS query stream was dead in rc.7 — service-registry key mismatch.** `dnstrack.Manager`
  is registered with `service.MustRegisterPtr` (keys on `*dnstrack.Manager`), but both the
  server (`SubscribeDNSQueries`) and the emit sites (`dns/client_log.go`) read it with
  `service.FromContext[*dnstrack.Manager]`, which keys on `**dnstrack.Manager` — so the
  lookup always returned nil. Server-side that surfaced as `codes.Unimplemented "DNS query
  tracking not available"`; emit-side it silently dropped every event (double failure).
  Fixed all three readers to `service.PtrFromContext[dnstrack.Manager]`, the pair of
  `MustRegisterPtr` — exactly how `trafficManager` is resolved (`daemon/instance.go`).
  Verified: the manager now resolves to the same pointer box.go registered.
* No proto/wire change — rc.7's `DnsQueryEvent`/`SubscribeDNSQueries` contract is intact;
  this only fixes the core wiring so the stream actually starts. LxBox §180 needs no client
  change (subscription already in place, was catching the Unimplemented gracefully).

#### v1.14.0-lx.1-rc.7

**Pre-release — not device-verified.** Adds a structured, process-attributed DNS-query
stream (SPEC 018) so a client can observe DNS resolutions with app attribution, instead of
parsing the text log. New `SubscribeDNSQueries` RPC behind `with_lx_command`; no data-path
change.

* **`SubscribeDNSQueries` — live DNS-query stream with `processInfo`.** Hijacked DNS (the
  norm on an Android VPN) is answered before a connection becomes a traffic tracker, so DNS
  queries never appear in the connections stream — the only egress was the text log, which
  carries no package attribution. New `common/dnstrack` (a `Subscriber[QueryEvent]` mirror
  of `trafficcontrol`) emits one event per resolution from `dns/client.go`, attributed via
  `adapter.ContextFrom(ctx).ProcessInfo` (already populated by the router's process search,
  so cache-hits are attributed too, not just cache-misses).
* **Failures are first-class.** Timeout / loopback / rejected-cached / SERVFAIL-reject emit
  a `failed=true` event with `error` and `rcode=-1` (no response) — without this the stream
  would be blind to DNS failures, the primary "DNS is being throttled" signal. Successful
  resolutions carry `source` (exchanged/cached/optimistic/refreshed), `qtype`, `rcode`,
  `ttl`.
* **CNAME chains preserved.** With `includeAnswers` on the subscription, each event carries
  the full `response.Answer` in wire order — CNAME hops AND final A/AAAA, not filtered to
  IPs — so a client rebuilds the CNAME chain from one event. Off by default (size); the
  field exists in proto from v1 for later DNS↔TCP IP-attribution without a proto bump.
* Wire: `rpc SubscribeDNSQueries(SubscribeDNSQueriesRequest) returns (stream DnsQueryEvent)`
  plus `DnsAnswer` (additive); server stream mirrors `SubscribeConnections` (event-driven,
  no ticker); libbox `SubscribeDNSQueries(includeAnswers, handler) → DnsQuerySubscription`.
  Tag-less core answers `codes.Unimplemented` (graceful fallback). `Detour`/`Chain` and all
  other streams unchanged.
* Docs: `SPECS/018-DNS_QUERY_STREAM`.

#### v1.14.0-lx.1-rc.6

**Pre-release — not device-verified.** Adds the transport detour tail of a connection's
final outbound as a new connection field (SPEC 017), so the client can show the real
physical packet path. Additive proto field; `Chain` / Clash-API unchanged. **Also merges
upstream `v1.14.0-alpha.35`** (114 commits since alpha.33) — the lx layer rebased on top.

* **Merged upstream `testing` → `v1.14.0-alpha.35`.** Conflicts resolved keeping all lx
  patches: `box.go` observable gate (`needObservable`, the rc.3 Android-fatal fix),
  trimmed AAR build tags (no `with_clash_api` / `with_usbip`; keeps `with_lx_command` /
  `with_awg` / `with_xhttp`), the lx command-RPC block, and the new `detourList` field.
  AmneziaWG submodule re-graft (`wireguard-go` → `./submodules`, pinned `e5feca7`) was
  preserved against upstream's "Rebase wireguard-go to official". Dependency versions
  (`sing` 0.8.12, `sing-tun`, `sing-cloudflared`) taken from upstream. Generated `.pb.go`
  regenerated; `go build ./...` green, package tests pass. Notable upstream additions now
  in tree: TLS spoof, optimistic DNS cache, USB/IP service (build-tag-gated off for the
  AAR), hysteria2 realm, certificate CGO JNI bridge, oom-killer improvements.
* **Verify on device:** the trimmed AAR omits `with_usbip` by design — confirm no lx
  config references a usbip endpoint (a missing tag fails fast at runtime, no silent
  fallback). Also exercise the WG/AWG path (submodule + dependency churn) and the
  connection stream (`detourList`).

* **`Connection.Detour` — the detour tail `Chain` omits by design.** Upstream `chain`
  answers "how routing picked the final outbound" (selector groups + the chosen node) and
  stops there (`common/trafficcontrol/tracker.go`): its loop only unwinds `OutboundGroup`
  via `Now()` and breaks on the first non-group, so a node's own `detour` (e.g. a node
  detouring through WARP) never appears. That is by design, not a bug — `detour` is a
  transport detail, not a routing choice. New `Detour []string` on `TrackerMetadata`
  unwinds the final outbound's detour tail via `Dependencies()` (which for a non-group
  outbound is exactly its detour, `adapter/outbound/adapter.go`), descending into groups
  through `Now()` against the **same atomic snapshot** so a detour-into-a-group reflects
  the live selection. A `seen` guard prevents detour cycles.
* **Resolved in the core, not reassembled on the client.** Because a detour can point at a
  group whose active node changes at runtime, building the path client-side would mean
  stitching `chain` + `GetOutbounds` + `GetGroups` snapshots that can drift between calls.
  The core resolves it once at connection-creation time (consistent `Now()` across all
  groups); the per-tick `SubscribeConnections` stream just carries the ready field. Wire
  cost is +1 short tag list per connection (usually 1 element); no extra RPC or channel.
* Wire: `repeated string detourList = 23` added to the `Connection` proto message
  (additive — old clients ignore it); mapped in `connectionToProto`; surfaced on the
  libbox `Connection` as `Detour() StringIterator` next to `Chain()`. Order: final
  outbound → outward; full path from the node = `Chain().first ⊕ Detour()`.
* Docs: `SPECS/017-CONNECTION_DETOUR_CHAIN`.

#### v1.14.0-lx.1-rc.5

**Pre-release — not device-verified.** Fixes in-flight cancellation of the per-node
delay test (SPEC 015 §3.6). Behind `with_lx_command`; client/command-side only — no
data-path change.

* **`URLTestOutbound` now honours cancellation of the gRPC call.** The handler parented
  the delay test to the long-lived `boxService.ctx` instead of the gRPC per-call ctx, so
  a cancelled call could not reach the in-flight dial — the test outlived it and the only
  lever was tearing down the whole connection. Now parented to the call ctx (`testCtx :=
  ctx`): dropping the call aborts that single test at its `DialContext`/`client.Do`,
  before `C.TCPTimeout`, without touching other streams. This restores the granular
  per-node cancel the Clash API had implicitly via `r.Context()` (there was never a
  `cancelDelays` endpoint — the cancel lived in the per-request HTTP context).
* **Mass-cancel of a ping batch** is unblocked on the existing gomobile binding with no
  native-surface change: run the ping worker-pool on a *separate* `CommandClient` instance
  and call `Disconnect()` on it — its `cancel()` + `conn.Close()` reach the test ctx and
  kill the in-flight dials, while the main client's Connections/Groups streams stay up.
  No per-call cancel handle and no server-side batch RPC are added (see SPEC 015 §3.6/§5).
* Docs: SPEC 015 §3.6 (cancellation), SPEC 014 (#4240 was deleted upstream — switch the
  `box.go` seam-removal criterion from issue-status to upstream-code).

#### v1.14.0-lx.1-rc.4

**Pre-release — not device-verified.** Completes the command-protocol work (SPEC 015)
needed for the Clash-API → CommandClient migration. All behind `with_lx_command`.

* **`GetGroups` / `GetOutbounds` — unary pull-snapshots of outbound groups and the
  flat outbound/endpoint list.** The native CommandClient was push-only: groups arrived
  solely via the `SubscribeGroups` stream, whose initial snapshot is lost if the stream
  never opened (service not yet `STARTED` at subscribe) or broke. The client then had no
  cheap way to re-read group state — the main screen could stay empty (tunnel connected,
  traffic flowing, `groups=[]`). These two getters fetch the current snapshot on demand,
  Clash-`GET /proxies`-style, without recreating the whole client / tearing down other
  streams. Both are needed: `SubscribeGroups` covers only in-group nodes, whereas
  standalone outbounds and endpoints (WG/AWG/Tailscale) appear only in the flat list.
* **Single-node / empty groups no longer disappear.** `readGroups()` silently dropped
  any group with fewer than 2 items (upstream commit `5bc0dfa9`), hiding single-node
  selectors — a regression vs Clash, whose `/proxies` returned `group.All()` unfiltered.
  `readGroups()` is the single source feeding both the `SubscribeGroups` startup
  broadcast and the new `GetGroups`, so the fix covers both paths.

#### v1.14.0-lx.1-rc.3

**Pre-release — not device-verified.** Fixes a fatal Android start regression
introduced by the `with_clash_api` drop (`rc.1`).

* **Android start no longer fails with "clash api is not included in this build".**
  Upstream `box.go` forced `needClashAPI` whenever a `PlatformLogWriter` was set
  (always true on Android/libbox), because the Clash server used to be the only
  log/traffic observer. With `with_clash_api` dropped that turned every Android
  start into a fatal — even with no `clash_api` in the config. Split the concern
  (`// lx:` seam): the platform writer now requests *observability* (Observable
  log factory + connection/traffic tracker), served by the native CommandClient
  (`SubscribeLog` / `SubscribeConnections`), **not** the Clash server. Only an
  explicit `experimental.clash_api` block still creates the Clash server (and
  still fails fast without the tag). The daemon is already nil-safe to a missing
  Clash server, so Clash-mode degrades gracefully. Desktop was unaffected.

#### v1.14.0-lx.1-rc.2

**Pre-release — not device-verified.** Adds the SPEC 014 libbox command-protocol
extensions on top of `rc.1`; the §010 Android download-stall path is still pending
on-device re-verification, so this stays a `-rc` tag. Client/command-side only — no
data-path change.

* **Native `CommandClient` per-node delay test + rule-table snapshot** (`SPECS/014`).
  Two new RPCs restore, over the native libbox `CommandClient`, what upstream only
  exposed through the (now-dropped) Clash API:
    * **`URLTestOutbound`** — measures the latency of a single node (an outbound **or**
      a WG/AWG/Tailscale endpoint) with a caller-supplied URL and timeout, returning a
      synchronous `{delay, error}`. Unlike the group-level `URLTest` it never requires
      an `OutboundGroup`; mass-pinging stays a client-side worker pool. Errors travel in
      the response payload (not as a gRPC error), and `delay==0 && error==""` is a
      successful 0 ms test — parity with Clash `/proxies/{name}/delay`.
    * **`GetRules`** — a snapshot of the routing rule table, **route and DNS** rules,
      split by `isDNS`. Route fields match Clash `/rules`; DNS rules go beyond Clash,
      which never exposed them (needs a new `adapter.DNSRouter.Rules()` getter).
  Both handlers are gated by the **`with_lx_command`** build-tag (real handler vs a
  `codes.Unimplemented` stub twin, mirroring `started_service_usbip{,_stub}.go`); the
  tag is baked into the Android AAR (`build_libbox` `sharedTags`) and the desktop
  `LX_TAGS`. A tag-less build is behaviourally equivalent to upstream.
* **Pinned, reproducible proto regeneration** (`Makefile.lx`: `lx-proto` /
  `lx-proto-install`). The codegen plugins are pinned — `protoc-gen-go` v1.36.11
  (= go.mod) and `protoc-gen-go-grpc` v1.5.1 — so `*.pb.go` regenerates idempotently
  across a rebase instead of drifting on `@latest`. The `.proto` seam sits under a
  `// lx:` marker; the generated code carries no markers.

#### v1.14.0-lx.1-rc.1

**Pre-release — not device-verified.** First build on the upstream 1.14 base. The
WireGuard-endpoint GRO fix (§010) now lands at the submodule source on AmneziaWG
`v0.0.3` (no downstream guard), but the Android download-stall path has **not** been
re-verified on real hardware yet — hence the `-rc.1` tag. Promote to a plain
`v1.14.0-lx.1` tag once on-device verification passes.

* **Migrated onto upstream sing-box 1.14** (merge of `v1.14.0-alpha.33` into the lx
  layer + AmneziaWG `wireguard-go` submodule re-grafted on `v0.0.3`). Brings the full
  1.14 feature set: the native sing-box API service / remote control, the DNS rework
  (`evaluate` action + `match_response`, optimistic cache, `store_dns`,
  per-evaluation `ip_version`/`query_type`), native Apple/Windows TLS engines, TLS
  spoof, Hysteria2 BBR/NAT-traversal, and closed-connection history in the
  CommandClient connection tracker (1000 entries / 5 min). See upstream
  [changelog.md](../docs/changelog.md) for the per-alpha breakdown.
* **`with_clash_api` dropped from the Android AAR and the desktop `LX_TAGS`.** LxBox
  is moving to manage the core over the native libbox `CommandClient`
  (group / url-test / select / connections streams), so the Clash REST API is dead
  weight on the client. A config that references `experimental.clash_api` now fails
  fast (`clash api is not included in this build, rebuild with -tags with_clash_api`)
  rather than silently degrading. lx configs do not use it.
* **`lx-release.yml`: `-rc.N` / `-alpha.N` / `-beta.N` tags publish as pre-releases**
  (`gh release create --prerelease`), so an unverified build can never displace the
  stable `lx` release as "Latest".

#### v1.13.13-lx.15

* **`package_name_regex` route/DNS/headless rule item.** Backport of the upstream
  1.14 feature onto the stable 1.13.13 base — matches the Android package name by
  regular expression (e.g. `"package_name_regex": ["^com\\.termux.*"]`), the regex
  counterpart of the existing exact-match `package_name`. Works in route rules, DNS
  rules and rule-set headless rules. See `SPECS/013`.
* The full 1.14 migration is **deferred to v1.14.0 stable** (the feature exists only
  in 1.14-alpha upstream; a feasibility pass put the migration at ~1.5–2 days, the
  main cost being the AmneziaWG `wireguard-go` submodule rebase). `lx-rebase.yml`
  excludes alpha/beta/rc by design, so it will pick up 1.14 only once it is stable.

#### v1.13.13-lx.14

* **WireGuard-endpoint GRO split-brain on Android — fixed.** A WireGuard *endpoint*
  without a `detour` killed download throughput on Android (UDP_GRO was enabled, but
  the GRO receive path is linux-only — packets coalesced on send, never un-coalesced
  on receive). Fix gates `UDP_GRO` behind `!android` in the `wireguard-go` submodule
  (`conn/`). Device-verified on real hardware (download 0.44 → 20.7 Mbps). UDP/WG-only.
  See `SPECS/010`.

#### v1.13.13-lx.13

* `ip=quic`: **`ib` now drives a real browser TLS fingerprint.** `ib=chrome`/`firefox`
  build the ClientHello via uTLS (the lib Reality uses) — genuine browser JA3/JA4
  (Chrome 120 / Firefox 120 shape, ALPN h3, PQ key_share stripped to fit one Initial).
  `ib=""`/`curl` keep the generic device-proven ClientHello. Without the `with_utls`
  build tag, `ib` degrades gracefully to the generic CH.
* `ip=quic`: stays a **single** Initial. The brief two-Initial (i1+i2) experiment was
  reverted — two different DCIDs read as two abandoned connections, more anomalous to a
  DCID-tracking DPI, not less; realism comes from the ClientHello, not packet count.
* `ip=dns`: `id` is now **optional** — when absent, the QNAME is a generated
  pronounceable pseudo-domain (no IP, no `sip.`), removing the hardcoded-default
  signature. `id` is now required only for `quic`.
* `ip=sip`: rebuilt as an INVITE + matching `100 Trying` (one dialog, shared
  branch/tag/Call-ID/CSeq) across i1+i2. Still expected to be blocked on the WARP DPI
  (protocol-to-destination class); kept for other providers.
* Docs: dropped internal/AI-voice phrasing from the 009 spec & comments.

#### v1.13.13-lx.12

* **AmneziaWG masquerade (`id`/`ip`/`ib`) — `ip=quic` reworked for real DPI bypass.**
* `ip=quic`: out-of-order fragmented QUIC Initial (RFC 9001) — a realistic
  ClientHello (`id` = SNI) split across CRYPTO frames in a shuffled wire order
  (first frame offset≠0), so a line-rate DPI parses garbage and fails open.
  Replaces the device-blocked 1-RTT short header. **Device-proven on a real
  LTE/WARP DPI (~330 ms)** — the only profile that passes there.
* `ip=quic`: per-call randomized fragment layout (no fixed cross-user signature)
  plus robustness knobs (fragment/PING count, packet size).
* `ip=quic`: multi-packet — fills **both `i1` and `i2`** with two independent
  fragmented Initials, so the flow reads as a developing QUIC session.
  Device-verified to bring the WARP tunnel up with no latency regression.
* `ip=dns`/`stun`/`sip`: rebuilt into correct client-initiated requests (DNS query,
  WebRTC STUN Binding Request, SIP INVITE+SDP with pseudo names). These time out on
  the WARP DPI (it blocks raw DNS/STUN/SIP to a datacenter IP as a protocol class),
  so they are kept for other providers whose DPI only checks packet well-formedness.
* `id` is required for `quic`/`dns` (SNI / QNAME), optional for `sip` (pseudo-host
  generated when absent) and `stun`.
* No submodule changes. See `SPECS/009-WIRESOCK_MASQUERADE_PROFILES`.

#### v1.13.13-lx.11

* AmneziaWG masquerade `id`/`ip`/`ib` (009): declarative WireSock-style sugar over
  `I1` for quic/dns/stun/sip. Live-verified (tunnel + traffic). First release of the
  feature; `ip=quic` later reworked in lx.12.

---

Older releases of the `1.13.13` line (`v1.13.13-lx.1` … `v1.13.13-lx.10`; the
`-lx.2` tag was never cut) predate this changelog and are not documented here —
see the git history and `SPECS/TASKS/` reports of that period.
