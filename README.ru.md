[English](README.md) · **Русский**

# sing-box-lx

**Клиентское ядро на базе sing-box для лаунчера, LxBox и роутеров.** Совместимость с
актуальными серверами Xray и AmneziaWG, демон и наблюдаемость для приложений.

- **Совместимость с сегодняшними серверами и сетями.** REALITY с гибридным постквантовым
  обменом ключами ML-KEM (X25519MLKEM768), как требует актуальный Xray, XHTTP,
  VLESS `encryption`, AmneziaWG 3.x, фрагментация ClientHello, WARP через MASQUE.
- **Ядро, сделанное под эксплуатацию** в сложных потребителях — десктопном лаунчере,
  Android-приложении LxBox, на роутерах: режим демона, наблюдаемость по gRPC, энергосбережение
  и сон простаивающих туннелей, роутерные сборки.
- **Дополнительные возможности.** Многохоповые цепочки `chain`, балансировка, группы
  DNS-серверов, снифферы протоколов.
- **Закрытие багов там, где они найдены, — в реальной практике**, каждый с тестом и условием
  снятия патча.

Подробности по каждому пункту — [Фичи](#фичи).

## Оглавление

- [Почему появился форк](#почему-появился-форк)
- [О sing-box](#о-sing-box)
- [Фичи](#фичи)
- [Сборка](#сборка)
- [Конфигурация — короткий тур](#конфигурация--короткий-тур)
- [Демон `lxd`](#демон-lxd)
- [Как сопровождается форк](#как-сопровождается-форк)
- [Карта репозитория](#карта-репозитория)
- [Ссылки](#ссылки)
- [Лицензия](#лицензия)

---

## Почему появился форк

Серверы, к которым подключаются люди, часто работают на Xray и AmneziaWG и движутся быстрее,
чем sing-box; примеры отставания: REALITY, постквантовый обмен ключами, XHTTP,
VLESS `encryption`, AmneziaWG 3.x. `sing-box-lx` закрывает этот зазор на своей стороне, не
расходясь с апстримом: каждый релиз `upstream/stable` вливается в течение дней, наш код живёт
в своих файлах за build-тегами, а сборка без них — апстрим байт в байт; см.
[Как сопровождается форк](#как-сопровождается-форк).

Go-модуль и бинарь сохраняют имя апстрима; суффикс `-lx` живёт только в строке версии.
Правила, по которым здесь живёт каждая фича, — [CONSTITUTION](SPECS/CONSTITUTION.md).

## О sing-box

[sing-box](https://github.com/SagerNet/sing-box) от SagerNet — универсальная прокси-платформа,
на которой построено это ядро: протоколы, движок маршрутизации, TUN-стек и биндинг `libbox`
для мобильных — всё оттуда. Документация — [sing-box.sagernet.org](https://sing-box.sagernet.org/),
README — [на GitHub](https://github.com/SagerNet/sing-box/blob/main/README.md).

---

## Фичи

### Протоколы и транспорты

| Фича | Поверхность конфига | Что даёт | Build-тег | Статус |
|---|---|---|---|---|
| **XHTTP** — [002](SPECS/FEATURES/002-XHTTP/FEATURE.md) | `transport.type: xhttp` | Xray-совместимый «splithttp»: режимы `auto` / `packet-up` / `stream-up` / `stream-one` поверх TLS, REALITY или h2c; переиспользование соединений `xmux`; опции обфускации | `with_xhttp` | проверен вживую на Xray-серверах; `stream-one` (путь `auto`+REALITY) девайс-верифицирован |
| **AmneziaWG 2.0 / 3.x** — [003](SPECS/FEATURES/003-AWG/FEATURE.md) | поля `wireguard`-endpoint `jc/jmin/jmax`, `s1–s4`, `h1–h4`, `i1–i5`, AWG 3.x `header_protection_key`, паддинг, хвосты, диапазонные тайминги | Полный набор обфускации amneziawg-go v3.1 плюс сахар **маскировки** в духе WireSock — `id`/`ip`/`ib`, который сам собирает декой `I1` | `with_awg` | проверено на живых серверах AWG 2.0 и 3.1; декой `ip=quic` девайс-верифицирован против DPI на LTE/WARP |
| **MASQUE / Cloudflare WARP** — [009](SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md) | outbound `type: masque` | CONNECT-IP (RFC 9484) поверх HTTP/3 или HTTP/2 через userspace-стек; `profile: cloudflare` для WARP; стандартный блок `tls`; idle-suspend и самовосстанавливающийся реконнект | — | девайс-верифицирован на Wi-Fi и LTE, `h3` и `h2` |
| **REALITY против актуального Xray** — [017](SPECS/FEATURES/017-REALITY/FEATURE.md) | `tls.reality` + `tls.utls.fingerprint`, `tls.reality.key_share`, `tls.fragment` / `record_fragment` | Гибридный постквантовый key share `X25519MLKEM768`, которого требует Xray ≥ v26.9.8, на `chrome`, `firefox`, `safari` (последние два — через форк-сабмодуль utls); по-узловой `key_share: classical \| hybrid`; фрагментация ClientHello теперь работает и на REALITY | — (внутри `with_utls`) | стенд-верифицировано против Xray v26.9.9 и более старых; `firefox`/`safari` подтверждены в поле; `key_share` и фрагментация ждут полевого прогона |
| **VLESS `encryption`** — [012](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) | поле `encryption` на `vless`-outbound | Постквантовый слой `mlkem768x25519plus` *внутри* VLESS, под транспортом и независимо от TLS/REALITY | — | девайс-верифицирован: ранее мёртвые узлы подписки ожили |

### Маршрутизация и DNS

| Фича | Поверхность конфига | Что даёт | Build-тег | Статус |
|---|---|---|---|---|
| **Outbound `chain`** — [015](SPECS/FEATURES/015-CHAIN/FEATURE.md) | `type: chain` | Виртуальный многохоповый путь, собираемый в рантайме из групп и узлов; группы не копируются, хопы — рантайм-ссылки; прозрачный `direct`, автоматический MTU для туннельных звеньев, `strip` / `rewrite` | `with_lx_chain` | живой стенд на реальных хопах; WireGuard-звенья на устройстве впереди |
| **Группа DNS-серверов** — [013](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md) | `dns.servers[].type: group` | Один DNS-сервер поверх нескольких: `stable` / `fastest` / `parallel` на TTL-модели, веерный запрос с бюджетом, видимость `survival` | — | выпущено; полевой прогон впереди |
| **Балансировка и отказоустойчивость** — [007](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) | `urltest` с `mode: round_robin`, `balancer{…}`, `mode: failover`; `least_test` реагирует на живые ошибки дайла | Round-robin-пул с ленивыми health-проверками и sticky-слотами; failover держится за рабочий узел до его отказа; ошибки мёртвого пути штрафуют узел и повторяют попытку через лучшего кандидата | `with_lx_command` (только `GetPool`) | девайс-верифицирован на реальном многоузловом пуле |
| **Снифферы протоколов** — [016](SPECS/FEATURES/016-SNIFF/FEATURE.md) | имена действий `sniff`: `wireguard`, `openvpn`, `ike`, `tailscale`, `sip` | Распознают VPN-туннели и звонки чужих устройств за роутером по форме первого пакета; стоят перед апстримным uTP-сниффером, который помечал WireGuard как bittorrent | — | выпущено; прогон на роутере впереди |

### Платформа и эксплуатация

| Фича | Поверхность конфига | Что даёт | Build-тег | Статус |
|---|---|---|---|---|
| **Наблюдаемость** — [006](SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md) | расширения `CommandClient` в libbox | `URLTestOutbound`, `GetRules`, `GetGroups`, `GetOutbounds`, `GetPool`, `GetDNSGroups`, `GetRunningConfig`, `GetChains`, `SubscribeDNSQueries`, `Connection.detourList` — то, на чём живёт Android-клиент | `with_lx_command` | выпущено, используется LxBox |
| **Idle-suspend (энергия)** — [008](SPECS/FEATURES/008-ENERGY/FEATURE.md) | `route.lx_idle_suspend` / `lx_idle_suspend_reachable` / `lx_idle_teardown`, `urltest` `mode: failover` | Три уровня сна для простаивающих WireGuard/AWG-эндпоинтов: батарея, нагрев и RAM на многоузловых мобильных профилях | `with_lx_idle_suspend` (вшит в AAR) | девайс-верифицирован: RSS −31 % |
| **Демон `lxd`** — [014](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md) | подкоманда `sing-box lxd` | Ядро in-process за управляющим каналом, который переживает любую смену конфига: gRPC + admin-REST на одном порту, `apply` с автоматическим откатом, mTLS с энролментом, установка службы, телеметрия хоста | `with_lxd` | девайс-верифицирован на macOS; OpenWrt-скрипты установки проверены в поле |

> **Не поддерживается by design:** серверные половины перечисленного; постквантовые **подписи** REALITY у Xray (`pqv` / ML-DSA-65) и `spiderX` — это другой механизм, не обмен ключами, и в sing-box его нет; отпечатки `edge`, `ios`, `android`, `360`, `qq` против Xray ≥ v26.9.8 (ни один апстримный пресет не несёт гибридного шара, у Xray та же граница; подменять отпечаток — работа приложений).

---

## Сборка

Сборки идут через **`Makefile.lx`**; апстримный `Makefile` не тронут.

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build        # → ./sing-box, версия вида vX.Y.Z-lx.N
make -f Makefile.lx lx-check        # проверка примеров конфигов в lx-test/config/
```

- **`--recurse-submodules` обязателен для любой сборки.** Четыре зависимости заменены форк-сабмодулями через `replace` в `go.mod`: `sing-tun`, `gvisor` и `utls` — безусловно, `wireguard-go` (рантайм AmneziaWG) — за `with_awg`. Клон без них не собирается.
- **Набор тегов** (`make -f Makefile.lx lx-print-tags` — единственный источник истины):

  ```
  with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,with_naive_outbound,with_purego,badlinkname,tfogo_checklinkname0,with_xhttp,with_awg,with_lx_command,with_lxd,with_openvpn,with_openconnect,with_lx_chain,with_tailscale
  ```

  Это клиентский набор апстрима минус серверные теги (`with_acme`, `with_ccm`/`with_ocm`), плюс `with_purego` (кросс-компиляция без CGO, чтобы `with_naive_outbound` собирался при `CGO=0`) и наши собственные теги. `with_lx_command` (расширения командного протокола libbox) и `with_lxd` (демон) независимы by design.
- **Тулчейн.** Версия Go пинуется в **`go.version`** и читается каждым CI-шагом `setup-go`; версия апстрима, на которой стоит форк, — в **`upstream.version`**. Не `go-version-file: go.mod` — это разрешилось бы в языковой минимум, а не в тулчейн, а AAR на Go 1.24 убивает любой quic-go-outbound на Android.
- **Android (`libbox.aar`).** `make lib_install && make lib_android` собирает `libbox.aar` (SDK 23) и `libbox-legacy.aar` (SDK 21) с вшитыми `with_xhttp` / `with_awg` / `with_lx_command` / `with_lx_idle_suspend` / `with_lx_chain` / `with_tailscale`. `clash_api` выброшен только из AAR — Android-клиент управляет ядром через нативный `CommandClient`.
- **Релизная матрица** (`lx-release.yml` на теге `v*-lx.*`): десктопные бинари для linux / darwin / windows, включая legacy-сборку под **Windows 7 (32-бит)** (без naive и `lxd`), linux-musl и mips/mipsle softfloat для роутеров, оба AAR, `SHA256SUMS`. Релиз-ноты берутся из `docs-lx/releases/`.

---

## Конфигурация — короткий тур

По одному сниппету на фичу. Таблицы полей, дефолты и все опции — **[docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md)** ([EN](docs-lx/lx-config.md)); детали уровня провода для XHTTP, AmneziaWG и MASQUE — **[docs-lx/protocols-transports.ru.md](docs-lx/protocols-transports.ru.md)** ([EN](docs-lx/protocols-transports.md)).

### Транспорт XHTTP

```jsonc
"transport": { "type": "xhttp", "host": "example.com", "path": "/xhttp", "mode": "auto" }   // auto | packet-up | stream-up | stream-one
```

### Endpoint AmneziaWG

```jsonc
{
  "type": "wireguard",                       // … стандартные поля wireguard …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": "1000-2000", "h4": 4,   // одно значение или диапазон "N-M"
  "i1": "<b 0x...><r 12>",                          // I1–I5: обязаны совпадать с сервером, регистр значим
  "id": "www.google.com", "ip": "quic", "ib": "chrome"   // либо: сахар маскировки вместо написанного руками i1
}
```

`id`/`ip`/`ib` и явный `i1` взаимоисключающи. `ip=quic` шлёт два фрагментированных QUIC Initial вне порядка — это профиль, доказанный против живого DPI; `dns`/`stun`/`sip` — корректные запросы, оставленные для провайдеров, чей DPI проверяет только правильность формы. Справка — [protocols-transports.ru.md §2](docs-lx/protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3) ([EN](docs-lx/protocols-transports.md#2-amneziawg-203x-awg2-awg3)) · [примеры маскировки](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md).

### Outbound MASQUE (Cloudflare WARP)

```jsonc
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "profile": "cloudflare",                        // cloudflare (WARP) | standard (RFC 9484)
  "vhttp": "h3",                                  // h3 (QUIC) | h2 (HTTP/2, для сетей, фильтрующих UDP:443)
  "tls": { "server_name": "www.microsoft.com" },  // фронтящий SNI; аутентификация — пиннинг публичного ключа
  "private_key": "<base64 DER EC>", "public_key": "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Ключевой материал берётся из регистрации WARP-устройства, которую делает клиент. Не путать с сахаром *маскировки* AWG выше — слово то же, фича другая. Справка — [protocols-transports.ru.md §3](docs-lx/protocols-transports.ru.md#3-masque-outbound-connect-ip--warp) ([EN](docs-lx/protocols-transports.md#3-masque-outbound-connect-ip--warp)).

### REALITY: отпечаток и `key_share`

```jsonc
"tls": {
  "enabled": true, "server_name": "www.apple.com",
  "utls": { "enabled": true, "fingerprint": "chrome" },      // chrome | firefox | safari несут гибридный шар
  "reality": {
    "enabled": true, "public_key": "<base64url>", "short_id": "0123abcd",
    "key_share": ""    // "" = как несёт отпечаток · "classical" = снять X25519MLKEM768 (только Xray < v26.9.8, один TCP-сегмент) · "hybrid" = требовать его
  }
}
```

`classical` существует для сетей, которые дропают двухсегментный гибридный ClientHello; на более новом сервере остаются только рычаги `record_fragment` (теперь действует на REALITY) и `detour`. Справка — [lx-config.ru.md §7](docs-lx/lx-config.ru.md#7-reality-key_share--гибридный-или-классический-clienthello-spec-089) ([EN](docs-lx/lx-config.md#7-reality-key_share--hybrid-or-classical-clienthello-spec-089)).

### VLESS `encryption`

```jsonc
{ "type": "vless", "uuid": "…", "encryption": "mlkem768x25519plus.native.0rtt.<ML-KEM-768 key>" }   // нет поля или "none" = выключено
```

Только клиентская половина; `decryption` — серверная сторона и намеренно не портируется. Справка — [lx-config.ru.md §6](docs-lx/lx-config.ru.md#6-vless-encryption--пост-квантовый-слой-spec-032) ([EN](docs-lx/lx-config.md#6-vless-encryption--post-quantum-layer-spec-032)).

### Группа DNS-серверов

```jsonc
{ "type": "group", "tag": "dns-public", "mode": "stable", "servers": ["dns-cf", "dns-google", "dns-quad9"] }   // stable | fastest | parallel
```

Справка — [lx-config.ru.md §5](docs-lx/lx-config.ru.md#5-группа-dns-серверов-spec-033035) ([EN](docs-lx/lx-config.md#5-dns-server-group-spec-033035)).

### Outbound `chain`

```jsonc
{
  "type": "chain", "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],   // вход → выход, в порядке движения пакета
  "idle_timeout": "5m",
  "strip": { "multiplex.padding": false },                          // односторонние DPI-трюки по умолчанию снимаются со звеньев
  "rewrite": { "wireguard": { "mtu": 1200 } }                        // merge-patch по типу узла, только для звеньев
}
```

Туннельным звеньям MTU понижается автоматически; путь виден в `detourList` и `GetChains`, послойная задержка — через URLTest по хоп-тегам `<tag>#0`, `<tag>#1`, …. Справка — [lx-config.ru.md §10](docs-lx/lx-config.ru.md#10-outbound-chain--виртуальная-цепочка-хопов-из-групп-и-узлов-spec-073) ([EN](docs-lx/lx-config.md#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes-spec-073)).

### Балансировка, энергия, снифферы

Новых типов нет — несколько полей на существующих: `urltest` `mode: round_robin` + `balancer{…}` и `mode: failover` ([lx-config.ru.md §3](docs-lx/lx-config.ru.md#3-режимы-выбора-узла-в-urltest-spec-019--116), [EN](docs-lx/lx-config.md#3-urltest-node-selection-modes-spec-019--116)); уровни сна `route.lx_idle_*` ([lx-energy.ru.md](docs-lx/lx-energy.ru.md), [EN](docs-lx/lx-energy.md)); имена протоколов в действии `sniff` и правилах `protocol` ([lx-sniff.ru.md](docs-lx/lx-sniff.ru.md), [EN](docs-lx/lx-sniff.md)).

---

## Демон `lxd`

`sing-box lxd` (build-тег `with_lxd`) держит ядро **in-process** за управляющим каналом, который принадлежит демону, а не инстансу box, — поэтому канал переживает любую смену конфига и доступен ровно тогда, когда плоскость данных лежит.

```bash
sing-box lxd --state-dir ./lxd-state -c config.json
```

- **Перезагрузка без потери канала** — `POST /admin/apply` валидирует кандидата в подпроцессе, подменяет инстанс и повышает его до *last-good* только после успешного старта; неудачный старт откатывается автоматически.
- **Один порт, две плоскости** — gRPC (тот же контракт `CommandClient`, на котором говорит Android-клиент) и admin-REST (обычный stdlib-клиент, дружелюбный к Windows 7).
- **mTLS с энролментом** — демон сам себе CA, печатает приглашение `address#fingerprint#code`, дальше знает клиентов по сертификату.
- **Наблюдаемость без второго порта** — память, статистика, логи, pprof, телеметрия хоста (CPU по ядрам, память, термалка, диски, интерфейсы) и справочник IP → устройство.
- **Установка службы** на macOS; на Linux (systemd, OpenWrt/procd) демон печатает рецепт, а не трогает диск.

📖 Руководство оператора — **[docs-lx/lxd-daemon.ru.md](docs-lx/lxd-daemon.ru.md)** ([EN](docs-lx/lxd-daemon.md)); клиентский контракт наблюдаемости — [docs-lx/lxd-grpc-api.ru.md](docs-lx/lxd-grpc-api.ru.md) ([EN](docs-lx/lxd-grpc-api.md)); разбор OpenWrt (VPN на отдельном SSID) — [docs-lx/openwrt-vpn-ssid.ru.md](docs-lx/openwrt-vpn-ssid.ru.md) ([EN](docs-lx/openwrt-vpn-ssid.md)) со скриптами установки в [`scripts-lx/openwrt/`](scripts-lx/openwrt/README.md).

---

## Как сопровождается форк

```
upstream/stable  ──merge──►  lx  =  upstream  +  швы // lx  +  lx-файлы  +  4 форк-сабмодуля
                                     │
                                     └─►  тег vX.Y.Z-lx.N  ──►  lx-release.yml  ──►  GitHub Release
```

- **Ручной мерж `upstream/stable`, никогда не ребейз.** `lx` — одновременно рабочая и релизная ветка, force-push по ней не делается. Дрейф меряется только по merge-base против `upstream/stable`; баннер GitHub «N commits behind testing» дрейфом не является.
- **Форк-сабмодули — часть дельты**: [wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) (рантайм AmneziaWG), [sing-tun-lx](https://github.com/Leadaxe/sing-tun-lx) (self-heal accept-loop), [gvisor-lx](https://github.com/Leadaxe/gvisor-lx) (nil-guard в хендшейке), [utls-lx](https://github.com/Leadaxe/utls-lx) (пресеты Firefox 148 и Safari 26.3). Каждый — свой апстрим плюс несколько коммитов; дрейф сабмодулей закрывается **до** мержа ядра.
- **У хотфиксов апстримных багов есть срок годности**: каждая заплатка в [реестре HOTFIXES](SPECS/FEATURES/004-HOTFIXES/FEATURE.md) называет условие, при котором её снимают.
- **Релизы**: теги `vX.Y.Z-lx.N` — стабильные, `-rc.N` / `-alpha.N` / `-beta.N` — пререлизы; процедура — [раннбук релиза](docs-lx/lx-release-runbook.ru.md) ([EN](docs-lx/lx-release-runbook.md)); инженерный лог — [lx-changelog.md](docs-lx/lx-changelog.md), пользовательские ноты — в [`docs-lx/releases/`](docs-lx/releases/).
- **Spec Kit**: [`SPECS/FEATURES`](SPECS/FEATURES/README.md) описывает текущее состояние каждой фичи как чёрный ящик; [`SPECS/TASKS`](SPECS/README.md) держит по папке на единицу работы (`SPEC → PLAN → TASKS → отчёт`), с роадмапом и кодами статуса; правила — в [CONSTITUTION](SPECS/CONSTITUTION.md).
- **Remotes**: `origin` = `Leadaxe/sing-box-lx` (ветка по умолчанию `lx`), `upstream` = `SagerNet/sing-box`.

### Потребители

| Потребитель | Платформа | Что берёт отсюда |
|---|---|---|
| [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) | десктоп | бинарь `sing-box` (кладётся как `bin/sing-box`), опционально демон `lxd` |
| [LxBox](https://github.com/Leadaxe/LxBox) | Android | `libbox.aar` и расширения `CommandClient` |
| Роутеры OpenWrt | сборки mips / musl | бинарь как `lxd` вместе со скриптами установки |

Маппинг ссылок подписок на поля конфига — работа потребителя; сам JSON конфига везде одинаков.

---

## Карта репозитория

Всё downstream — это либо новый файл, либо шов, помеченный `// lx`; `grep -rn "lx:begin"` находит каждый шов в апстрим-файле.

| Путь | Назначение |
|------|------------|
| `Makefile.lx` | сборка с lx-набором тегов и версией `-lx`; `lx-build`, `lx-check`, `lx-print-tags`, `lx-proto` |
| `go.version` / `upstream.version` | пин Go-тулчейна / версия апстрима, на которой стоит форк |
| `.github/workflows/lx-ci.yml`, `lx-release.yml`, `lx-build.yml` | матрица CI, релиз по тегам `v*-lx.*`, сборщик по запросу для любой ветки |
| `SPECS/` | Spec Kit: `FEATURES/` (состояние), `TASKS/` (работа), `CONSTITUTION.md` |
| `docs-lx/` | документация форка (EN + RU), changelog, релиз-ноты |
| `lx-test/` | примеры конфигов для `sing-box check` и живые стенды (`zombie`, `chain`, …) |
| `scripts-lx/openwrt/` | роутерный установщик `lxd` |
| `transport/v2rayxhttp/` | клиентский транспорт XHTTP |
| `transport/wireguard/device_awg.go`, `submodules/wireguard-go` | параметры и рантайм AmneziaWG |
| `protocol/masque/` | outbound MASQUE / CONNECT-IP |
| `protocol/chain/` | outbound `chain` |
| `common/tls/` (`*_lx*`), `submodules/utls` | key share REALITY, фрагментация, пресеты отпечатков |
| `common/sniff/*_lx.go` | снифферы протоколов |
| `dns/transport/group/`, `common/dnstrack/` | группа DNS-серверов и трассировка DNS-запросов за `SubscribeDNSQueries` |
| `experimental/libbox/`, `daemon/` (швы `lx:`) | расширения `CommandClient` |
| `lxd/` | демон `lxd` |
| `option/v2ray_xhttp.go`, `option/wireguard_awg.go`, `option/masque_lx.go`, `option/chain_lx.go` | опции фич |
| `submodules/sing-tun`, `submodules/gvisor` | форк-сабмодули для TUN-стека |

---

## Ссылки

| | |
|---|---|
| Апстрим | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [документация](https://sing-box.sagernet.org/) |
| Обзор конфигурации | [docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md) ([EN](docs-lx/lx-config.md)) — каждое поле каждой фичи, с примерами |
| Протоколы и транспорты | [docs-lx/protocols-transports.ru.md](docs-lx/protocols-transports.ru.md) ([EN](docs-lx/protocols-transports.md)) — XHTTP, AmneziaWG, MASQUE в деталях |
| Как устроены протоколы Xray | [docs-lx/xray-protocols-explained.ru.md](docs-lx/xray-protocols-explained.ru.md) ([EN](docs-lx/xray-protocols-explained.md)) — REALITY, Vision, XHTTP, VLESS `encryption`: логика, реализация в форке, отличия от ванили, примеры Xray ↔ sing-box-lx |
| Как устроен AmneziaWG | [docs-lx/amneziawg-explained.ru.md](docs-lx/amneziawg-explained.ru.md) ([EN](docs-lx/amneziawg-explained.md)) — три слоя обфускации, бюджет MTU, графт в форке wireguard-go, пример `awg.conf` ↔ endpoint |
| Руководство по энергии | [docs-lx/lx-energy.ru.md](docs-lx/lx-energy.ru.md) ([EN](docs-lx/lx-energy.md)) — уровни idle-suspend, `mode: failover`, тюнинг |
| Снифферы | [docs-lx/lx-sniff.ru.md](docs-lx/lx-sniff.ru.md) ([EN](docs-lx/lx-sniff.md)) |
| Руководство оператора `lxd` | [docs-lx/lxd-daemon.ru.md](docs-lx/lxd-daemon.ru.md) ([EN](docs-lx/lxd-daemon.md)) |
| API наблюдаемости | [docs-lx/lxd-grpc-api.ru.md](docs-lx/lxd-grpc-api.ru.md) ([EN](docs-lx/lxd-grpc-api.md)) — контракт, на котором говорят клиенты, и gRPC-демон, и Android-AAR |
| Разбор OpenWrt | [docs-lx/openwrt-vpn-ssid.ru.md](docs-lx/openwrt-vpn-ssid.ru.md) ([EN](docs-lx/openwrt-vpn-ssid.md)) |
| Раннбук релиза | [docs-lx/lx-release-runbook.ru.md](docs-lx/lx-release-runbook.ru.md) ([EN](docs-lx/lx-release-runbook.md)) |
| Changelog и релиз-ноты | [docs-lx/lx-changelog.md](docs-lx/lx-changelog.md) · [docs-lx/releases/](docs-lx/releases/) |
| Референсные ядра | [docs-lx/lx-reference-cores.ru.md](docs-lx/lx-reference-cores.ru.md) ([EN](docs-lx/lx-reference-cores.md)) — где искать ответы по wire-протоколу |
| Spec Kit | [SPECS/FEATURES](SPECS/FEATURES/README.md) · [SPECS/TASKS](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) |
| Происхождение протоколов | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) (XHTTP, REALITY, VLESS encryption) · [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [Cloudflare WARP / MASQUE](https://developers.cloudflare.com/warp-client/) |

---

## Лицензия

Наследует лицензию апстримного sing-box (**GPL-3.0**). Все правки помечены `// lx` и распространяются под той же лицензией. Это неофициальный форк, не аффилированный с SagerNet.
