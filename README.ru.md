[English](README.md) · **Русский**

# sing-box-lx

> **Тонкий downstream-форк [SagerNet/sing-box](https://github.com/SagerNet/sing-box).**
> Небольшой набор клиентских фич поверх upstream — транспорт **XHTTP**, **AmneziaWG**, **MASQUE** (CONNECT-IP / Cloudflare WARP), пост-квантовый слой **VLESS `encryption`**, **DNS-группа серверов**, расширения **наблюдаемости** (CommandClient), балансировка нагрузки **round_robin**, **энергосбережение idle-suspend**, headless-демон **`lxd`** и outbound **`chain`** (виртуальная цепочка хопов из групп и узлов) — изолированы в lx-файлах, большинство за своим build-tag.
> Набор может расти, философия — нет: жить ребейзом на каждый upstream-тег, а не отдельной жизнью.

> 📄 README самого upstream sing-box — **[на GitHub](https://github.com/SagerNet/sing-box/blob/main/README.md)** (всегда актуальный).

Это не отдельный проект и не «улучшенный sing-box». Это upstream sing-box **плюс несколько фич**, реализованных так, чтобы их можно было переносить на новые версии sing-box годами, почти без конфликтов. Со временем фич может становиться больше — другие протоколы, новые возможности, — но каждая обязана жить по тем же правилам тонкого форка ([CONSTITUTION](SPECS/CONSTITUTION.md)).

---

## Уникальное позиционирование

В экосистеме sing-box форки, добавляющие XHTTP/AmneziaWG, делятся на два лагеря — и `sing-box-lx` не входит ни в один:

| Форк | Фичи | Подход | Синк с upstream |
|------|------|--------|-----------------|
| **SagerNet/sing-box** (upstream) | базовый | — | — |
| **shtorm-7/sing-box-extended** | десятки (WARP, MASQUE, MTProxy, XHTTP, AWG2, …) | «комбайн», правки повсюду | отдельная ветка, без ребейза на теги |
| **amnezia-vpn/amnezia-box**, **hoaxisr/amnezia-box** | только AWG | толстый форк, правки in-place | синк по веткам (`dev-next`/`stable-next`) |
| **➡ sing-box-lx** (этот репозиторий) | **малый набор (XHTTP, AWG2, MASQUE, VLESS PQ-шифрование, DNS-группа, наблюдаемость, балансировка, энергосбережение, `chain`)** | **тонкий: новые файлы за build-tag, минимум касаний upstream** | **ребейз атомарных `// lx`-коммитов на upstream-теги** |

**Чем мы отличаемся:**

- **Минимальная дивергенция.** Новый код живёт в новых файлах. Существующие upstream-файлы трогаются только в крошечных помеченных швах `// lx:begin … // lx:end`. → дешёвые ребейзы.
- **Изоляция за build-tag.** Фичи включаются тегами `with_xhttp` / `with_awg`. Сборка **без** них байт-в-байт повторяет поведение upstream — фичи ничего не ломают по умолчанию.
- **Идентичность сохранена.** Go-модуль остаётся `github.com/sagernet/sing-box`, бинарь называется `sing-box`. Суффикс `-lx` есть только в строке версии (`1.14.0-lx.N`).
- **Build-tag — родная конвенция sing-box**, а не наше изобретение (`with_quic`, `with_wireguard`, …). Мы просто применяем её с максимальной дисциплиной.

> Готовые форки-комбайны мы **не тянем как зависимость**, а используем только как референс wire-протокола.

---

## Фичи и статус

| # | Фича | Что это | Статус |
|---|------|---------|--------|
| **XHTTP** | клиентский транспорт | Xray-совместимый «splithttp» (режимы `auto`/`packet-up`/`stream-up`/`stream-one`) поверх Reality/TLS/h2c, с переиспользованием соединений `xmux` (SPEC 059) | ✅ **проверен живыми Xray-серверами**: packet-up/auto (handshake + DNS + HTTPS + скачивание), а `stream-one` (путь `auto`+REALITY) **девайс-верифицирован** с `v1.14.0-lx.17` — SPECs [042](SPECS/TASKS/042-XHTTP_STREAM_GRPC_CONTENT_TYPE/SPEC.md)/[043](SPECS/TASKS/043-XHTTP_STREAM_ONE_PATH_PREFIX/SPEC.md) закрыли паритет gRPC Content-Type и 404 из-за срезанного слэша, вешавший режим. `xmux` возвращает совместимость с Xray-серверами, чьи конфиги несут секцию `xmux` (раньше молча игнорировалась), и экономит полный хендшейк TCP+TLS на каждый поток |
| **AmneziaWG** | клиентский endpoint | полный набор обфускации: мусорные пакеты `Jc/Jmin/Jmax`, мусорные заголовки `S1–S4`, магические заголовки `H1–H4` (числом или диапазоном), управляемые последовательности пакетов `I1–I5`, плюс WireSock-стиль `Id/Ip/Ib` — декларативный сахар над `I1` | ✅ собирается, проходит `check`; зависимость **активирована** ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet-база + обфускация); **проверено живым AWG-сервером**: handshake + keepalive + трафик наружу. Паритет сверен с `amneziawg-tools` и netlink-контрактом ядерного модуля — реализованы все параметры обфускации, которые принимают официальные реализации ([SPEC 031](SPECS/TASKS/031-AWG_PARITY_AUDIT_ADVANCED_SECURITY/SPEC.md)) |
| **Маскировка `id/ip/ib`** | сахар над AWG | WireSock-стиль: декларативная маскировка поверх `I1` — домен (`id`) + протокол (`ip`: `quic`/`dns`/`stun`/`sip`) + браузер (`ib`), ядро строит клиент-инициированную `I1`-приманку: `quic` = out-of-order фрагментированный Initial (i1+i2), `dns`/`stun`/`sip` = query/Binding-Request/INVITE | ✅ **`ip=quic` device-проверен на реальном LTE/WARP DPI** (~330 мс, упрощает Cloudflare WARP); `dns`/`stun`/`sip` собираются и проходят `check`, но режутся как класс протокола к WARP-edge — для других провайдеров |
| **Наблюдаемость** (расширения CommandClient) | live-стрим для UI | нативные расширения libbox gRPC за `with_lx_command` (SPEC 014–018, 035, 037): `URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `GetPool`, `GetDNSGroups`, `GetRunningConfig` (канонический JSON, из которого реально построен работающий box), `GetChains` (состояние outbound'ов `chain`), плюс `Connection.detourList` (хвост detour'а отдельным полем, SPEC 017) и `SubscribeDNSQueries` — структурный live-поток DNS (домен, qtype, rcode `-1`=ошибка, CNAME-цепочка, привязка к процессу, `dnsServer`/`dnsServerType`/`outbound`, SPEC 018) | ✅ в стабильных тегах, потребляется Android-клиентом **LxBox**. Фича — [OBSERVABILITY](SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md) |
| **round_robin** (балансировка нагрузки) | режим `urltest` | пул-балансировка на `urltest` за `with_lx_command` (для `GetPool`): `mode` `least_test` (дефолт) \| `round_robin`; `balancer{pool (дефолт 3), pool_tolerance (0=держать живые / >0=топ по задержке), sticky_hash}`. Sticky-ключ: пропущен/`[]` → дефолт `["process","domain"]`, `["none"]` → выкл; компоненты `process`/`domain`/`source_ip`/`dest_ip`/`dest_port`. Фиксированные слоты `slot[hash(key)%pool]` (FNV-64a), замена в слоте; `GetPool` отдаёт слоты | ✅ локально равномерно (10/10/10, sticky off); **device-verified end-to-end** на реальном мульти-нодовом пуле; rc.15 починил схлопывание `domain`-ключа (теперь читается `metadata.Domain`, переживающий resolve домен→IP, а не пустой `destination.Fqdn`) — на устройстве равномерность 0.27 → 0.95+. Фича — [URLTEST_BALANCE](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md), конфиг — [docs/.../urltest.md](docs/configuration/outbound/urltest.md) |
| **MASQUE** (`type: masque`) | клиентский outbound | CONNECT-IP (RFC 9484) поверх HTTP/3 **или** HTTP/2 для **Cloudflare WARP** (SPEC 021): туннелирует целые IP-пакеты через userspace gVisor-стек; `profile` (`cloudflare`/`standard`), `vhttp` (`h3`/`h2`), стандартный блок `tls`, pinning ECDSA public key, idle-suspend + самовосстановление. h2 — ручной фреймер поверх `x/net/http2` (без доп. зависимостей), TLS через общий `common/tls`; `connect-ip-go` вкопан | ✅ **device-verified на Wi-Fi и LTE** (`warp=on`, реальный трафик на `h3` и `h2`); на сетях, режущих входящий UDP:443, `h3`-handshake виснет — там `vhttp: "h2"` (TCP:443). ⚠️ Форма конфига сменилась в SPEC 062: `network`→`vhttp`, плоские `sni`/`skip_cert_verify`/`fragment*` → блок `tls` (старая форма живёт с deprecation до `v1.14.0-lx.30`) |
| **VLESS `encryption`** | поле outbound | пост-квантовый слой `mlkem768x25519plus` ВНУТРИ VLESS (SPEC 032) — под транспортом, независим от TLS/REALITY; spec-строка `mlkem768x25519plus.<native\|xorpub\|random>.<0rtt\|1rtt>….<key>`, `""`/`"none"` = выкл; только клиентская половина (порт из `starifly/sing-box` — тот же GPL-3.0 и та же upstream-база) | ✅ отгружено в `v1.14.0-lx.18`, **девайс-верифицировано**: +10 прежде мёртвых нод подписки (6/8 WS, 4/4 gRPC), остальные группы транспортов не сдвинулись. Фича — [VLESS_ENCRYPTION](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) |
| **DNS-группа серверов** | тип DNS-сервера | DNS-сервер `type: group` (SPEC 033/035): режимы `stable`/`fastest`/`parallel` как грани одной TTL-модели (раздельные TTL ошибки/победы, веер с гарантированным бюджетом, `survival`-видимость деградации); live-состояние через `GetDNSGroups` (за `with_lx_command`) | ✅ код + тесты + DoD, отгружено; адверсариальная ревизия (24 агента) чистая. Полевая проверка на устройстве — впереди. Фича — [DNS_GROUP](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md) |
| **Idle-suspend (энергия)** | route-опции | три уровня сна простаивающих WG/AWG-узлов — `route.lx_idle_suspend` / `lx_idle_suspend_reachable` / `lx_idle_teardown` (SPEC 020) плюс `urltest.passive_check` (SPEC 019): батарея, нагрев и RAM на мульти-нодовых мобильных профилях; тег `with_lx_idle_suspend` (зашит в Android AAR) | ✅ **девайс-верифицировано** (SPEC 020): recv-воркеры 16→0, RSS −31 %; гайд — [docs-lx/lx-energy.ru.md](docs-lx/lx-energy.ru.md). Фича — [ENERGY](SPECS/FEATURES/008-ENERGY/FEATURE.md) |
| **Демон `lxd`** | headless-подкоманда | `sing-box lxd` держит ядро **внутри процесса, за управляющим каналом, который переживает любую смену конфига** (SPEC 055–057): gRPC + admin-REST на одном порту, `apply` с валидацией в сабпроцессе и автооткатом на last-good, mTLS (демон сам себе CA, клиенты регистрируются одноразовым кодом), установка службой, хранилище файловых ресурсов (`.srs`, geo), телеметрия хоста (CPU по ядрам, память, температура, диски, интерфейсы) и справочник «IP → устройство»; build-tag `with_lxd` | ✅ device-verified на macOS (enrollment, обе роли службы, откат). Руководство — [docs-lx/lxd-daemon.ru.md](docs-lx/lxd-daemon.ru.md); gRPC-справочник — [lxd-grpc-api.md](docs-lx/lxd-grpc-api.md). Фича — [LXD_DAEMON](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md) |
| **Outbound `chain`** (`type: chain`) | клиентский outbound | виртуальная цепочка хопов, собираемая в рантайме из групп и узлов (SPEC 073): позиции в порядке пакета, любая позиция — узел/endpoint/группа; группы не копируются — выбранные узлы обслуживают рантайм-звенья с дозвоном через предыдущую позицию (ленивые, с прогревом, эвикшн по `idle_timeout`); `direct` на позиции ≥ 1 — прозрачный выключатель хопа; туннельным звеньям MTU понижается автоматически; `strip` (односторонние DPI-приёмы по умолчанию сняты) / `rewrite` (merge-patch по типу); путь в `detourList`, RPC `GetChains`, задержка по слоям через URLTest по тегам хопов; build-tag `with_lx_chain` | ✅ код + юниты + живой стенд на реальных shadowsocks-хопах (`lx-test/chain`); полевая проверка WireGuard-звеньев на устройстве — впереди. Фича — [CHAIN](SPECS/FEATURES/015-CHAIN/FEATURE.md) |
| **Реакция на отказы дайлов** | поведение `urltest` | группа `least_test` теперь реагирует на **боевые** отказы дайлов, а не только на результаты проб (SPEC 054): ошибка «путь мёртв» даёт узлу штраф и один fallback-дайл через лучшего кандидата; в аварийном режиме ранжирование идёт штрафы → задержка; штраф снимается только доказательством жизни | ✅ отгружено, потребитель 15-секундного netstack-дедлайна (SPEC 052). Фича — [URLTEST_BALANCE](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) |

Подробные отчёты — в [`SPECS/TASKS/002-…`](SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT/IMPLEMENTATION_REPORT.md), [`SPECS/TASKS/003-…`](SPECS/TASKS/003-AWG2_CLIENT_ENDPOINT/IMPLEMENTATION_REPORT.md) и [`SPECS/TASKS/009-…`](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/IMPLEMENTATION_REPORT.md). Обзор конфига — **[docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md)**; полный справочник параметров — **[docs-lx/lx-protocols-transports.ru.md](docs-lx/lx-protocols-transports.ru.md)**.

> **Не поддерживается (слой Reality, отложено):** post-quantum Reality (`pqv` / ML-DSA-65) и `spiderX` из Xray. Это Xray-специфичные фичи Reality, которых нет в sing-box, а Reality — upstream-слой TLS, который мы держим нетронутым (это не одна из наших фич). Классический X25519 Reality работает; сервер, который **требует** post-quantum Reality, не подключится. Это ограничение sing-box — правильнее решать в upstream (получим на ребейзе).

---

## Сборка

Сборка идёт через отдельный **`Makefile.lx`** (upstream `Makefile` не трогаем):

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build
# → бинарь ./sing-box с версией вида 1.14.0-lx.18
```

> `--recurse-submodules` обязателен для `with_awg`: рантайм AmneziaWG подключён submodule'ом `submodules/wireguard-go` → [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx).

Под капотом — стандартный `go build` с набором тегов (единственный источник истины — `make -f Makefile.lx lx-print-tags`):

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg,with_lx_command,with_lxd,with_openvpn,with_openconnect,with_lx_chain
```

Это клиентский feature-set upstream **минус** серверные/нерелевантные теги — `with_acme` (серверный выпуск сертов), `with_tailscale`, `with_ccm`/`with_ocm` (AI-прокси) — **плюс** `with_purego` (CGO-free кросс-сборка, чтобы `with_naive_outbound`/cronet собирался при `CGO=0` на любом desktop-таргете, кроме Windows 7 / 32-бит legacy-сборки, где naive выкинут — у `cronet-go` нет windows/386), апстримные `with_openvpn` / `with_openconnect` и наши фичи `with_xhttp` / `with_awg` / `with_lx_command` / `with_lxd` / `with_lx_chain`. Всё остальное — ровно как upstream.

Наши два тега независимы по замыслу (SPEC 067): **`with_lx_command`** несёт расширения командного протокола libbox (`URLTestOutbound`, `GetRules`, `GetGroups`, … — ими живёт LxBox), **`with_lxd`** — пакет `lxd/` и подкоманду демона. Legacy-сборка Windows 7 идёт **без** `with_lxd` (там нет ни службы Windows, ни ротации лога, так что подкоманда существовала бы без того, что делает её демоном), сохраняя RPC. В Android-AAR демона не было и раньше: gomobile собирает `experimental/libbox`, который `lxd/` не импортирует.

Версия Go-тулчейна пиновится файлом **`go.version`** (SPEC 049) — его читает каждый шаг `setup-go` во всех `lx-*.yml`. Это намеренно **не** `go-version-file: go.mod`: тот даёт 1.24.x, а он убивает все quic-go-аутбаунды в Android-AAR (SPEC 044). Апстрим-версия, на которой стоит форк, — в **`upstream.version`**, бампается руками при re-graft.

Проверка конфигов:

```bash
./sing-box check -c lx-test/config/xhttp_reality.json
./sing-box check -c lx-test/config/awg2_basic.json
```

> `lx-test/config/` — наши примеры (upstream `test/` — отдельный Go-модуль, его не используем).

**Android (`libbox.aar`).** `make lib_install && make lib_android` собирает gomobile-AAR — `libbox.aar` (SDK 23) + `libbox-legacy.aar` (SDK 21) — с зашитыми `with_xhttp`/`with_awg`/`with_lx_command`/`with_lx_idle_suspend`/`with_lx_chain` (и без `tailscale`/`clash_api` — внешние Clash-дашборды остаются заботой десктопа), для встраивания в Android-приложение-потребитель (нужны NDK r28 + OpenJDK 17). `Libbox.version()` отдаёт `…-lx.N`.

---

## Конфигурация фич

> Полные таблицы полей, дефолты и `awg-quick`→JSON маппинг — **[docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md)**. Здесь — кратко.

### XHTTP (outbound transport)

```jsonc
"transport": {
  "type": "xhttp",
  "host": "example.com",
  "path": "/xhttp",
  "mode": "auto"          // auto | packet-up | stream-up | stream-one
}
```

### AmneziaWG (endpoint)

Поля AWG промотированы прямо в `WireGuardEndpointOptions`:

```jsonc
{
  "type": "wireguard",
  // … стандартные поля wireguard (private_key, address, peers, …) …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": "1000-2000", "h4": 4,   // число или диапазон "N-M"
  "i1": "<b 0x...><r 12>", "i2": "", "i3": "", "i4": "", "i5": ""   // CPS
}
```

> `I1–I5` — это конфиг (не согласуется по сети), значения должны **совпадать на клиенте и сервере**, регистрозависимы.

**Сахар-маскировка (`id`/`ip`/`ib`).** Вместо ручного `i1` задаёшь домен, протокол и
браузер — ядро само собирает `I1`-приманку (стиль WireSock). Удобно для упрощения
коннекта к **Cloudflare WARP**:

```jsonc
{
  "type": "wireguard",
  // … стандартные поля wireguard …
  "id": "www.google.com", "ip": "quic", "ib": "chrome"   // quic: id идёт как SNI в ClientHello
  // или: "ip": "dns",  "id": "www.google.com"   // dns/sip: id идёт как QNAME/host
}
```

`ip` ∈ `quic|dns|stun|sip`; `id` обязателен только для `quic` (идёт как SNI в ClientHello);
для `dns`/`sip` опционален (без него генерится псевдо-имя; где задан — идёт на провод как
QNAME / host), `stun` его игнорирует. `ib` ∈ `chrome|firefox|curl` (только quic, эффект
минимальный — без JA3-fingerprint). Взаимоисключается с явным `i1`.

Для **`quic`** ядро генерит out-of-order фрагментированный QUIC Initial (RFC 9001) — реальный
ClientHello, нарезанный на CRYPTO-фреймы в перемешанном порядке, так что line-rate DPI парсит
мусор и пропускает. Раскладка рандомизируется на каждый вызов (нет межюзерной сигнатуры), и
`ip=quic` теперь шлёт **два** независимых Initial (i1+i2) — поток читается как развивающаяся
QUIC-сессия. Это **единственный профиль, device-проверенный на реальном LTE/WARP DPI** (~330 мс).
`dns`/`stun`/`sip` реализованы как корректные клиент-инициированные запросы, но режутся как класс
протокола к WARP-edge (raw DNS/STUN/SIP к дата-центровому IP сам по себе аномален) — сохранены
для других провайдеров, чей DPI проверяет лишь корректность пакета. См.
[docs-lx/lx-protocols-transports.ru.md §2](docs-lx/lx-protocols-transports.ru.md#2-amneziawg-20-awg2) и [фича AWG2](SPECS/FEATURES/003-AWG2/FEATURE.md) · [примеры](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md).

### MASQUE (outbound — Cloudflare WARP)

Outbound `masque` туннелирует целые IP-пакеты через **CONNECT-IP (RFC 9484)**, HTTP/3 или HTTP/2,
к **Cloudflare WARP**. Не путать с AWG-сахаром *masquerade* `id/ip/ib` выше — разные фичи, одно слово.

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "profile": "cloudflare",       // cloudflare (WARP) | standard (RFC 9484)
  "vhttp": "h3",                 // версия HTTP: h3 (QUIC) | h2 (HTTP/2). tcp/udp — это network_list
  "tls": {
    "server_name": "www.microsoft.com"   // domain-fronting; аутентификация — пиннинг public key, не SNI
  },
  "private_key": "<base64 DER EC>",
  "public_key":  "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Ключевой материал (`private_key`/`public_key`/`ip`/`ipv6`) берётся готовым из конфига — регистрацию
устройства в WARP делает клиент. На сетях, режущих входящий UDP:443, `h3`-handshake виснет —
переключите узел на `vhttp: "h2"` (TCP:443).

> **Форма конфига сменилась в SPEC 062.** Версия HTTP — это `vhttp` (раньше было `network`,
> которое везде означает обратное), а настройки TLS живут в стандартном блоке `tls`
> (`sni` → `tls.server_name`, `skip_cert_verify` → `tls.insecure`, …). Старые поля ещё работают
> и печатают deprecation до **v1.14.0-lx.30** — таблица миграции в
> [docs-lx/lx-protocols-transports.ru.md §3.10](docs-lx/lx-protocols-transports.ru.md#310-миграция-со-схемы-до-spec-062). SNI по умолчанию — `www.cloudflare.com`,
> а не имя эндпоинта: именно имя MASQUE-эндпоинта в ClientHello и режет DPI.

Полный справочник —
[docs-lx/lx-protocols-transports.ru.md §3](docs-lx/lx-protocols-transports.ru.md#3-masque-outbound-connect-ip--warp) и [фича MASQUE_WARP](SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md).

### VLESS `encryption` (пост-квантовый слой)

Плоское поле `encryption` на `vless`-outbound включает `mlkem768x25519plus`-рукопожатие
*внутри* VLESS — под транспортом и независимо от TLS/REALITY:

```jsonc
{
  "type": "vless",
  "uuid": "…",
  "encryption": "mlkem768x25519plus.native.0rtt.<ключ ML-KEM-768>"
}
```

Отсутствует или `"none"` — слой выключен, поведение идентично upstream. Только
клиентская половина (`decryption` — серверная, намеренно не портирована). См.
[фичу VLESS_ENCRYPTION](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md).

### DNS-группа серверов

DNS-сервер `type: group` оборачивает несколько upstream-DNS в один с режимами
`stable` / `fastest` / `parallel` на TTL-модели (раздельные TTL ошибки и победы,
веер с гарантированным бюджетом). Состояние отдаётся в UI через `GetDNSGroups`
(за `with_lx_command`). См. [docs-lx/lx-config.ru.md §5](docs-lx/lx-config.ru.md)
и [фичу DNS_GROUP](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md).

---

### `chain` (outbound — виртуальная цепочка хопов)

Многохоповый путь, собираемый в рантайме из того, что группы выбрали прямо сейчас; позиции
перечисляются **в порядке пакета** (вход первым, выход последним), любая позиция — узел,
endpoint или группа любой вложенности. Группы не копируются — выбранный на позиции ≥ 1 узел
обслуживает рантайм-**звено**, которое дозванивается через предыдущую позицию; звенья ленивые,
прогреваются для детерминированных позиций, удаляются по `idle_timeout` при нуле живых
соединений. `direct` на позиции ≥ 1 прозрачен (выключатель хопа на лету).

```jsonc
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],   // вход → выход
  "idle_timeout": "5m",
  "strip": { "multiplex.padding": false },        // односторонние DPI-приёмы снимаются у звеньев по умолчанию
  "rewrite": { "wireguard": { "mtu": 1200 } }      // merge-patch по типу узла, только звенья
}
```

Туннельным звеньям (WireGuard, MASQUE) MTU понижается автоматически на точные накладные
IP-туннелей под ними. Наблюдаемость: путь в `detourList`, RPC `GetChains` / поле `chain` в
Clash API, задержка по слоям — URLTest по внутренним тегам хопов `<tag>#0`, `<tag>#1`, ….
См. [docs-lx/lx-config.ru.md §9](docs-lx/lx-config.ru.md) и
[фичу CHAIN](SPECS/FEATURES/015-CHAIN/FEATURE.md).

## Демон `lxd`

`sing-box lxd` (build-tag `with_lxd`) держит ядро **внутри процесса**, за управляющим каналом,
который принадлежит демону, а не box-инстансу, — поэтому канал переживает любую смену конфига
и доступен ровно тогда, когда data-plane лежит:

```bash
sing-box lxd --state-dir ./lxd-state -c config.json
```

- **Reload без потери канала.** `POST /admin/apply` кладёт кандидата на диск, валидирует его
  **сабпроцессом** (крэш не утащит демон), подменяет инстанс и делает его *last-good* только
  после успешного старта. Провал старта откатывается автоматически; прерванный apply
  запоминается и не бутится никогда.
- **Один порт — две плоскости.** `application/grpc` уходит в тот же `daemon.StartedService`,
  которым живёт Android-линия; всё остальное — admin-REST (обычный stdlib-клиент, дружелюбен
  к Windows 7).
- **mTLS с регистрацией.** Демон сам себе CA и печатает приглашение `адрес#отпечаток#код`:
  клиент пинит сервер, регистрируется одноразовым кодом и дальше опознаётся по сертификату.
  Доверием управляют `client add / list / remove`, только с loopback.
- **Наблюдаемость без второго порта** — `/admin/memory`, `/admin/stats`, `/admin/logs`,
  `/admin/pprof/*`, плюс телеметрия **хоста** (`/admin/host`: CPU по ядрам, память считается
  от *available*, а не от *free*, термозоны, диски, дескрипторы; `/admin/host/interfaces`)
  и справочник **«IP → устройство»** для сетевого инспектора (`/admin/clients-info`).
- **Установка службой** на macOS (`--service=install` / `install-user`); на Linux демон печатает
  рецепт, а диск не трогает — это принцип, а не пробел (systemd и OpenWrt/procd).

📖 Руководство оператора — **[docs-lx/lxd-daemon.ru.md](docs-lx/lxd-daemon.ru.md)**
([EN](docs-lx/lxd-daemon.md)); плоскость наблюдения для клиентов (один контракт поверх gRPC-демона **и** Android-AAR) —
[docs-lx/lxd-grpc-api.ru.md](docs-lx/lxd-grpc-api.ru.md) ([EN](docs-lx/lxd-grpc-api.md)); разбор под OpenWrt (VPN на отдельном SSID) —
[docs-lx/openwrt-vpn-ssid.ru.md](docs-lx/openwrt-vpn-ssid.ru.md).

---

## Модель сопровождения

```
upstream tag (vX.Y.Z)
        │
        └─►  ветка lx = upstream + N атомарных // lx-коммитов
                 ├─ FORK_BOOTSTRAP (Makefile.lx, CI, версия)
                 ├─ XHTTP client transport
                 ├─ AWG2 client endpoint
                 └─ … (новые фичи — такими же атомарными // lx-коммитами)
```

- **Только ребейз, никогда merge.** На новый upstream-тег ветка `lx` ребейзится поверх него.
- Каждая фича — атомарный коммит(ы), помеченный `// lx`. Новые файлы конфликтов не дают; швы в upstream-файлах малы и переносятся вручную.
- Разработка ведётся по **Spec Kit** (`SPECS/NNN-T-S-NAME/`: SPEC → PLAN → TASKS → IMPLEMENTATION_REPORT).

### Remotes

```bash
origin    git@github.com:Leadaxe/sing-box-lx.git   # ветка по умолчанию: lx
upstream  https://github.com/SagerNet/sing-box.git
```

---

## Структура lx-специфики

| Путь | Назначение |
|------|------------|
| `Makefile.lx` | сборка с lx-тегами и версией `-lx` |
| `.github/workflows/lx-ci.yml` | CI: матрица фич (baseline/xhttp/awg/full) + negative-check + кросс-платформа + android AAR |
| `.github/workflows/lx-release.yml` | релиз на `v*-lx.*`: desktop ×6 + `libbox.aar` → GitHub Release |
| `SPECS/` | Spec Kit (конституция, задачи, отчёты) |
| `lx-test/config/` | примеры конфигов для `sing-box check` |
| `transport/v2rayxhttp/` | XHTTP-клиент (новый пакет) |
| `transport/wireguard/device_awg.go` | AWG IpcSet-параметры (за `with_awg`) |
| `submodules/wireguard-go` | submodule: merged-форк AmneziaWG-рантайма ([Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx)) |
| `option/v2ray_xhttp.go`, `option/wireguard_awg.go`, `option/masque.go` | опции фич |
| `include/v2rayxhttp.go` | регистрация транспорта за build-tag |
| `submodules/gvisor` | submodule: пин-снапшот gVisor с нашим nil-guard'ом хендшейка ([Leadaxe/gvisor-lx](https://github.com/Leadaxe/gvisor-lx)) |
| `submodules/sing-tun` | submodule: форк sing-tun с самолечением acceptLoop ([Leadaxe/sing-tun-lx](https://github.com/Leadaxe/sing-tun-lx)) |
| `protocol/chain/` | outbound `chain`: хопы, рантайм-звенья, strip/rewrite/MTU (за `with_lx_chain`) |
| `lxd/` | демон `lxd`: admin-REST, mTLS, установка службой, телеметрия хоста (за `with_lxd`) |
| `go.version` / `upstream.version` | пин Go-тулчейна (его читает каждый `setup-go` в CI) / апстрим-версия, на которой стоит форк |

Поиск всех правок upstream-файлов: `grep -rn "// lx"`.

---

## Потребитель

Ядро собирается для десктоп-лаунчера **singbox-launcher** (бандлит `bin/sing-box`). На Android потребитель встраивает **`libbox.aar`** (gomobile) вместо бинаря — конфиг-JSON тот же. Маппинг `type=xhttp` и AWG-полей в визарде — задачи на стороне потребителя, не здесь.

---

## Ссылки

| | |
|---|---|
| Upstream | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [документация](https://sing-box.sagernet.org/) |
| Этот форк | [Leadaxe/sing-box-lx](https://github.com/Leadaxe/sing-box-lx) |
| AmneziaWG-рантайм | [Leadaxe/wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) — sagernet-база + обфускация (3-way merge) |
| AmneziaWG upstream | [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [docs.amnezia.org](https://docs.amnezia.org/documentation/amnezia-wg/) |
| XHTTP (исток) | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — `transport/internet/splithttp` |
| Обзор конфига | [docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md) ([EN](docs-lx/lx-config.md)) — все downstream-фичи, build-tags, короткие примеры |
| Протоколы и транспорты | [docs-lx/lx-protocols-transports.ru.md](docs-lx/lx-protocols-transports.ru.md) ([EN](docs-lx/lx-protocols-transports.md)) — полный справочник параметров XHTTP, AmneziaWG, MASQUE |
| Changelog форка | [docs-lx/lx-changelog.md](docs-lx/lx-changelog.md) — источник, из которого `lx-release.yml` берёт релиз-ноты |
| Гайд по энергии | [docs-lx/lx-energy.ru.md](docs-lx/lx-energy.ru.md) — уровни idle-suspend, passive_check, настройка |
| Руководство по `lxd` | [docs-lx/lxd-daemon.ru.md](docs-lx/lxd-daemon.ru.md) ([EN](docs-lx/lxd-daemon.md)) — установка, daemon.json, mTLS, admin REST |
| API наблюдаемости | [docs-lx/lxd-grpc-api.ru.md](docs-lx/lxd-grpc-api.ru.md) ([EN](docs-lx/lxd-grpc-api.md)) — контракт наблюдаемости, которым говорят клиенты (gRPC-демон + Android-AAR) |
| Разбор под OpenWrt | [docs-lx/openwrt-vpn-ssid.ru.md](docs-lx/openwrt-vpn-ssid.ru.md) ([EN](docs-lx/openwrt-vpn-ssid.md)) — VPN на отдельном SSID |
| Референс-ядра | [docs-lx/lx-reference-cores.md](docs-lx/lx-reference-cores.md) — куда смотреть за ответом по wire-протоколам |
| Релизный runbook | [docs-lx/lx-release-runbook.md](docs-lx/lx-release-runbook.md) — ритуал merge upstream + тегирования |
| Spec Kit | [SPECS/](SPECS/) — [README](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [IMPLEMENTATION_PROMPT](SPECS/IMPLEMENTATION_PROMPT.md) |

---

## Лицензия

Наследует лицензию upstream sing-box (**GPL-3.0**). Все правки помечены `// lx` и распространяются под той же лицензией. Это неофициальный форк, не аффилирован с SagerNet.
