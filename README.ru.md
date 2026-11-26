[English](README.md) · **Русский**

# sing-box-lx — форк sing-box для актуальных серверов Xray, AmneziaWG и Cloudflare WARP

[![Последний релиз](https://img.shields.io/github/v/release/Leadaxe/sing-box-lx?label=release)](https://github.com/Leadaxe/sing-box-lx/releases/latest)
[![Загрузки](https://img.shields.io/github/downloads/Leadaxe/sing-box-lx/total)](https://github.com/Leadaxe/sing-box-lx/releases)
[![Go-тулчейн](https://img.shields.io/badge/Go-1.26.10-00ADD8?logo=go&logoColor=white)](go.version)
[![Лицензия: GPL-3.0](https://img.shields.io/github/license/Leadaxe/sing-box-lx)](LICENSE)

**sing-box-lx — доработанное клиентское ядро прокси и VPN на базе [sing-box](https://github.com/SagerNet/sing-box) для десктопа, Android и роутеров, совместимое с актуальными серверами Xray и AmneziaWG.** Форк добавляет то, чего эти серверы ждут от клиента: REALITY с постквантовым key share, транспорт XHTTP и VLESS `encryption` из Xray, AmneziaWG 2.0 / 3.x на WireGuard-endpoint, Cloudflare WARP через MASQUE и TrustTunnel. Вокруг протоколов — то, что нужно приложениям и роутерам на OpenWrt, чтобы эксплуатировать ядро: демон `lxd`, gRPC-API состояния, сон простаивающих туннелей.

Ядро подменяет sing-box без переделок: бинарь по-прежнему называется `sing-box`, конфиг — тот же JSON sing-box, а `-lx` есть только в строке версии. Каждый релиз `upstream/stable` вливается в форк; сборка без тегов форка и конфиг без его полей ведут себя как апстрим, байт в байт.

Проект бесплатный и развивается в свободное время — если он вам полезен, **[поддержите его](docs-lx/donate.ru.md)**.

## Что вы получаете

- **Узлы, которые перестали отвечать после обновления Xray на сервере, снова подключаются.** Xray ≥ v26.9.8 отвергает REALITY-ClientHello без гибридного key share `X25519MLKEM768`; ядро отправляет его на отпечатках `chrome`, `firefox` и `safari`. → [REALITY](#reality-работает-с-актуальными-серверами-xray-включая-постквантовый-x25519mlkem768)
- **WireGuard проходит через сеть, которая режет его по сигнатуре.** AmneziaWG меняет форму пакетов, а ключи маскировки ставят перед рукопожатием QUIC Initial браузера. → [AmneziaWG](#amneziawg-20-и-3x-работают-на-обычном-wireguard-endpoint)
- **Cloudflare WARP подключается там, где фильтруется UDP 443.** Outbound `masque` несёт туннель по HTTP/3 или по HTTP/2 на TCP 443. → [MASQUE](#cloudflare-warp-подключается-через-masque-по-http3-или-http2)
- **Телефон с десятью WireGuard-узлами перестаёт платить за девять простаивающих.** Простаивающие endpoint'ы засыпают: на устройстве восемь спящих endpoint'ов освободили 134 МБ. → [Idle-suspend](#простаивающие-wireguard-туннели-на-android-засыпают)
- **Роутер раздаёт VPN по отдельному SSID и откатывается, если конфиг сломали.** Демон `lxd` проверяет конфиг до подмены и возвращает последний рабочий, если ядро не стартовало; управляющий канал всё это время на связи. → [`lxd`](#демон-lxd-сохраняет-управляющий-канал-при-каждой-смене-конфига)
- **Приложение показывает, что делает ядро, не разбирая логи.** Группы, пулы, цепочки, правила, DNS-запросы и путь каждого соединения приходят по gRPC. → [Наблюдаемость](#наблюдаемость-по-grpc-приложение-видит-что-делает-ядро)
- **Упавший сервер пула не роняет запросы.** Балансировщик `round_robin` повторяет соединение через соседний узел и меняет мёртвый узел живым из запаса, не дожидаясь проверки. → [`urltest`](#urltest-балансирует-по-пулу-или-держит-один-узел-до-отказа)
- **Балансировку, failover и транспорты проверяют на плохой сети без root.** Outbound `impair` управляемо портит канал внутри ядра: скорость, потери, обрывы по расписанию, тот же прогон по `seed`. → [Эмуляция плохой сети](#эмуляция-плохой-сети-без-root-канал-управляемо-портится-внутри-ядра)

## Скачать и установить

Бинари лежат в **[GitHub Releases](https://github.com/Leadaxe/sing-box-lx/releases/latest)**; в каждом релизе есть `SHA256SUMS`. Архивы называются `sing-box-<версия>-<цель>`.

| Где работает | Цель | Примечания |
|---|---|---|
| Windows x64 / ARM64 | `windows-amd64.zip`, `windows-arm64.zip` | `libcronet.dll` из архива держите рядом с `sing-box.exe` — его загружает outbound `naive` |
| Windows 7, 32 бита | `windows-386-legacy-windows-7.zip` | без `naive` и `lxd` |
| macOS, Intel / Apple silicon | `darwin-amd64.tar.gz`, `darwin-arm64.tar.gz` | |
| Linux и роутеры: OpenWrt, Keenetic, AsusWRT Merlin | `linux-amd64`, `linux-arm64`, `linux-armv7`, `linux-mipsle-softfloat`, `linux-386`, `linux-riscv64` | статические musl-сборки, glibc не нужен |
| Роутеры на big-endian MIPS (OpenWrt `mips_24kc`) | `linux-mips-softfloat` | без `naive` |
| Роутеры с маленьким flash-оверлеем | `linux-arm64.upx`, `linux-mipsle-softfloat.upx` | 17 МБ на flash вместо 75–90 МБ; бинарь распаковывается в память при каждом старте |
| Android-приложения | `libbox-<версия>.aar` (SDK 23), `libbox-legacy-<версия>.aar` (SDK 21) | gomobile-библиотека, не приложение |

Нужно приложение, а не ядро: [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) для десктопа и [LxBox](https://github.com/Leadaxe/LxBox) для Android поставляются с этим ядром. На OpenWrt один [скрипт установки](scripts-lx/openwrt/README.ru.md) скачивает бинарь с проверкой checksum и настраивает демон.

### Сборка из исходников

```bash
git clone --recurse-submodules https://github.com/Leadaxe/sing-box-lx
make -f Makefile.lx lx-build        # → ./sing-box, версия вида vX.Y.Z-lx.N
make -f Makefile.lx lx-check        # проверка примеров конфигов в lx-test/config/
```

- **`--recurse-submodules` обязателен.** Четыре зависимости заменены форк-сабмодулями через `replace` в `go.mod`: `sing-tun`, `gvisor` и `utls` всегда, `wireguard-go` (рантайм AmneziaWG) — за `with_awg`. Клон без них не компилируется.
- **Теги.** `make -f Makefile.lx lx-print-tags` печатает набор: клиентские теги апстрима без серверных (`with_acme`, `with_ccm` / `with_ocm`), плюс `with_purego` и теги форка `with_xhttp`, `with_awg`, `with_lx_command`, `with_lxd`, `with_lx_chain`, `with_trusttunnel`, `with_lx_impair`. Апстримный `Makefile` не тронут.
- **Тулчейн.** Версия Go запинена в [`go.version`](go.version), её читает каждый джоб CI; версия апстрима, на которой стоит форк, — в [`upstream.version`](upstream.version).
- **Android.** `make lib_install && make lib_android` собирает оба AAR. В AAR нет `clash_api` и `lxd`: Android-приложение управляет ядром через нативный `CommandClient`.

## Фичи

Один модуль на фичу: что она даёт, как работает, пример, ограничения. Каждое поле и значение по умолчанию — [docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md) ([EN](docs-lx/lx-config.md)); детали на проводе — [docs-lx/protocols-transports.ru.md](docs-lx/protocols-transports.ru.md) ([EN](docs-lx/protocols-transports.md)); индекс фич со спеками — [SPECS/FEATURES](SPECS/FEATURES/README.md).

### REALITY работает с актуальными серверами Xray, включая постквантовый X25519MLKEM768

Узел на сервере, обновлённом до Xray ≥ v26.9.8, подключается, а не падает с `reality verification failed`. Такой сервер принимает только ClientHello с гибридным key share `X25519MLKEM768` перед `X25519`. Ядро отправляет его на `chrome`, на `firefox` и `safari` (пресеты Firefox 148 и Safari 26.3 из форка [utls-lx](https://github.com/Leadaxe/utls-lx)) и на включаемом явно `chrome_155`.

```jsonc
"tls": {
  "enabled": true, "server_name": "www.apple.com",
  "utls": { "enabled": true, "fingerprint": "chrome" },
  "reality": { "enabled": true, "public_key": "<base64url>", "short_id": "0123abcd",
               "key_share": "" }   // "" = как несёт отпечаток · "classical" · "hybrid"
}
```

Ограничения: гибридный ClientHello весит 1,7 КБ и уходит двумя TCP-сегментами; в сети, которая теряет второй, помогает `record_fragment` (здесь он действует и на REALITY) или `detour`, а для сервера старше v26.9.8 — `key_share: classical`. У отпечатков `edge`, `ios`, `android`, `360`, `qq` нет пресета с гибридным key share, актуальный Xray их отвергает — берите один из трёх выше. Постквантовые подписи REALITY из Xray (`pqv` / ML-DSA-65) и `spiderX` не поддерживаются.

Без build-тега (внутри `with_utls`) · проверено на стенде с Xray v26.9.9 и старше; `firefox` / `safari` подтверждены в поле · [фича](SPECS/FEATURES/017-REALITY/FEATURE.md) · [конфиг](docs-lx/lx-config.ru.md#7-reality-key_share--гибридный-или-классический-clienthello) ([EN](docs-lx/lx-config.md#7-reality-key_share--hybrid-or-classical-clienthello))

### XHTTP подключается к серверам Xray поверх TLS, REALITY или h2c

VLESS-узел с транспортом XHTTP («splithttp») из Xray работает из того же конфига, что и остальные узлы sing-box. Транспорт отправляет uplink и downlink обычными HTTP-запросами в режимах `auto`, `packet-up`, `stream-up` и `stream-one` и переиспользует соединения через `xmux`, как браузер.

```jsonc
"transport": { "type": "xhttp", "host": "example.com", "path": "/xhttp", "mode": "auto" }
```

Ограничения: только клиентская сторона. Несовпадение с сервером выглядит как тишина, а не как ошибка — чек-лист в [protocols-transports.ru.md §1.11](docs-lx/protocols-transports.ru.md#111-диагностика) ([EN](docs-lx/protocols-transports.md#111-troubleshooting)).

`with_xhttp` · проверено на живых серверах Xray; `stream-one` под REALITY — на устройстве · [фича](SPECS/FEATURES/002-XHTTP/FEATURE.md)

### VLESS `encryption` добавляет постквантовый слой внутри VLESS

Узлы, у которых на сервере задан `decryption` Xray, подключаются; без этого поля они не отвечают. Слой `mlkem768x25519plus` стоит между VLESS и транспортом и не зависит от TLS и REALITY.

```jsonc
{ "type": "vless", "uuid": "…", "encryption": "mlkem768x25519plus.native.0rtt.<ключ ML-KEM-768>" }   // нет поля или "none" = выключено
```

Ограничения: только клиентская половина; серверный `decryption` не реализован — на сервере нужен Xray.

Без build-тега · проверено на устройстве: узлы подписки, которые не отвечали, ожили · [фича](SPECS/FEATURES/012-VLESS_ENCRYPTION/FEATURE.md) · [конфиг](docs-lx/lx-config.ru.md#6-vless-encryption--пост-квантовый-слой) ([EN](docs-lx/lx-config.md#6-vless-encryption--post-quantum-layer))

### AmneziaWG 2.0 и 3.x работают на обычном WireGuard-endpoint

Туннель работает в сети, которая узнаёт WireGuard по размерам пакетов и заголовкам. Endpoint `wireguard` принимает полный набор параметров amneziawg-go v3.1 — мусорные пакеты, паддинг и подменённые типы заголовков, пакеты-приманки `i1`–`i5`, защиту заголовка AWG 3.x, — поэтому `awg.conf` переносится в него поле в поле. Ключи маскировки `id` / `ip` / `ib` собирают приманку за вас: `ip: quic` перед каждым рукопожатием отправляет QUIC Initial с целым ClientHello браузера.

```jsonc
{
  "type": "wireguard",                              // … обычные поля wireguard …
  "jc": 10, "jmin": 50, "jmax": 100,
  "s1": 20, "s2": 20, "s3": 60, "s4": 60,
  "h1": 1, "h2": 2, "h3": "1000-2000", "h4": 4,     // значение или диапазон "N-M"
  "id": "www.google.com", "ip": "quic", "ib": "chrome"   // или написанный вручную "i1"; одно исключает другое
}
```

Ограничения: `s1`–`s4`, `h1`–`h4` и, в AWG 3.x, `header_protection_key` должны точно совпадать с сервером, иначе рукопожатие молчит; мусор и приманки задаются только на отправляющей стороне. Конфиг без AWG-полей — обычный WireGuard-endpoint.

`with_awg` · проверено на живых серверах AWG 2.0 и 3.1; приманка `ip: quic` подняла Cloudflare WARP в LTE-сети, где чистый WireGuard не подключался · [фича](SPECS/FEATURES/003-AWG/FEATURE.md) · [справочник](docs-lx/protocols-transports.ru.md#2-amneziawg-203x-awg2-awg3) ([EN](docs-lx/protocols-transports.md#2-amneziawg-203x-awg2-awg3)) · [примеры маскировки](SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES/EXAMPLES.md) · [как устроен AmneziaWG](docs-lx/amneziawg-explained.ru.md) ([EN](docs-lx/amneziawg-explained.md))

### Cloudflare WARP подключается через MASQUE по HTTP/3 или HTTP/2

WARP работает там, где не отвечает его WireGuard-endpoint, а с `vhttp: h2` — и там, где фильтруется UDP 443. Outbound `masque` несёт целые IP-пакеты по CONNECT-IP ([RFC 9484](https://www.rfc-editor.org/rfc/rfc9484)) через userspace-стек, спит в простое и сам переподключается.

```jsonc
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "profile": "cloudflare",                        // cloudflare (WARP) | standard (RFC 9484)
  "vhttp": "h3",                                  // auto (по умолчанию) | h3 (QUIC) | h2 (TCP 443)
  "tls": { "server_name": "www.microsoft.com" },
  "private_key": "<base64 DER EC>", "public_key": "<base64 DER PKIX>",
  "ip": "172.16.0.2/32", "ipv6": "2606:4700:110:...::/128"
}
```

Ограничения: ядро не регистрирует устройство в WARP — ключи даёт клиентское приложение или конвертер. HTTP-прокси не может быть `detour` для `h3`, потому что это UDP; там нужен `h2`. MASQUE — не *маскировка* AmneziaWG из раздела выше: слово похожее, фича другая.

Без build-тега (нужны `with_quic`, `with_gvisor`) · проверено на устройстве в Wi-Fi и LTE, `h3` и `h2` · [фича](SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md) · [конфиг](docs-lx/lx-config.ru.md#4-masque-outbound--cloudflare-warp) ([EN](docs-lx/lx-config.md#4-masque-outbound--cloudflare-warp)) · [справочник](docs-lx/protocols-transports.ru.md#3-masque-outbound-connect-ip--warp) ([EN](docs-lx/protocols-transports.md#3-masque-outbound-connect-ip--warp))

### TrustTunnel работает клиентом и сервером

Одно ядро подключается к endpoint'у AdGuard TrustTunnel и само может им быть. `type: trusttunnel` — это прокси-`CONNECT` с Basic-авторизацией поверх HTTP/2 (TLS, uTLS) или HTTP/3 (QUIC): TCP — по стриму на соединение, UDP — кадрами `_udp2`, одна сессия на узел, в простое она молчит. Inbound держит лимиты официального endpoint'а и отвечает на пробы кодом 404.

Ограничения: совместимость с официальными endpoint 1.1.0 и клиентом 1.1.11 проверена на стенде в обе стороны; на живом VPS прогона ещё не было. Это единственный inbound, который добавляет форк, — для серверной стороны остальных протоколов нужен Xray или AmneziaWG.

`with_trusttunnel` · [фича](SPECS/FEATURES/018-TRUSTTUNNEL/FEATURE.ru.md) · [справочник](docs-lx/protocols-transports.ru.md#6-trusttunnel-outbound-и-inbound) ([EN](docs-lx/protocols-transports.md#6-trusttunnel-outbound-and-inbound)) · [как устроен TrustTunnel](docs-lx/trusttunnel-protocols-explained.ru.md) ([EN](docs-lx/trusttunnel-protocols-explained.md))

### `sniff_guard` сохраняет узлы с маскировочным SNI за релеями, которые перенаправляют по SNI

Узел REALITY или WARP за релеем в `detour` перестаёт выглядеть мёртвым. Релей или домашний роутер с inbound'ом Xray и `sniffing.destOverride` читает имя из первого пакета рукопожатия и отправляет соединение на это имя; при маскировочном SNI это чужой хост. `lx.sniff_guard` делает первый пакет рукопожатия TLS или QUIC нечитаемым для сниффера — разрез ClientHello, задержка, байт-приманка перед QUIC Initial — и не меняет того, что получает сервер, а по исходам соединений учится, каким путям защита нужна.

Ограничения: выключено, пока в конфиге нет блока; значения по умолчанию выбирает приложение, которое пишет конфиг. Проверено на loopback-стендах.

Без build-тега · [фича](SPECS/FEATURES/019-SNIFF_GUARD/FEATURE.md) · [конфиг](docs-lx/lx-block.ru.md#lxsniff_guard--защита-от-перенаправления-по-sni) ([EN](docs-lx/lx-block.md#lxsniff_guard--guard-against-rerouting-by-sni))

### `chain` собирает цепочку хопов из групп, не копируя их

Вход, середину и выход выбирает свой selector или `urltest`, и цепочка идёт за этим выбором. При загрузке конфига каждый узел на позиции ≥ 1, включая членов групп, получает `detour` на хоп позиции ниже; у узла один экземпляр и один путь, поэтому группа на позиции проверяет тот путь, которым идёт её трафик. Выравнивание MTU и фильтры strip из `lx.detour` действуют на эти узлы как на любой узел под `detour`.

```jsonc
{
  "type": "chain", "tag": "via-three",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],   // вход → выход
  "rewrite": { "wireguard": { "mtu": 1200 } }                      // merge-patch по типу узла, только позиции ≥ 1
}
```

Путь виден в `detourList` и `GetChainStatus`, каждое изменение узла — в `GetOutboundConfig`; теги хопов `<tag>#0`, `<tag>#1`, … дают задержку каждого слоя через URLTest. Ограничения: проверено на живых стендах с реальными хопами; WireGuard на позиции цепочки на устройстве не проверялся.

`with_lx_chain` · [фича](SPECS/FEATURES/015-CHAIN/FEATURE.md) · [конфиг](docs-lx/lx-chain.ru.md) ([EN](docs-lx/lx-chain.md))

### `urltest` балансирует по пулу или держит один узел до отказа

Группа узлов распределяет соединения по нескольким серверам или держится за один рабочий, вместо того чтобы всегда выбирать узел с наименьшей задержкой. `mode: round_robin` с `balancer{…}` раскладывает соединения по пулу живых узлов и держит каждый сайт на своём узле; `mode: failover` держит узел, пока он не откажет, и проверяет только его; `least_test`, когда подключение через узел не удалось, штрафует узел и открывает соединение через следующий.

Сервер пула, упавший между проверками, не роняет запросы: соединение открывается через соседний слот. Узел, отказывающий в подключении, уходит из пула меньше чем за секунду; узел, который принимает соединения и молчит, новые соединения обходят с 5-й секунды, а вычёркивается он за 41 с. Его слот сразу занимает узел из запаса `max(⌈pool/2⌉, 1)` живых, который держит плановая проверка. Узел вычёркивается, только когда промолчал на собственной пробе, а контрольный узел ответил, поэтому сон телефона, смена Wi-Fi или обрыв у провайдера пул не трогают; пока не отвечает никто, пауза между проверками растёт 15 → 30 → 60 → 120 с и не тратит батарею. Сайт, закрытый для одного узла, открывается через другой узел пула, а остальной трафик остаётся на своих узлах.

```jsonc
{ "type": "urltest", "tag": "auto", "outbounds": ["node-a", "node-b", "node-c", "node-d"],
  "mode": "round_robin", "balancer": { "pool": 3 } }   // привязка по ["process","domain"] по умолчанию
```

Ограничения: восстановление по отказам соединений работает только в `round_robin`; отказы UDP сайты не переносят; смена сети, о которой платформа не сообщила (в том числе Modern Standby на Windows), может один раз вычеркнуть живой узел, и он возвращается сам. Состояние пула для интерфейса (`GetPool`) есть в сборке с `with_lx_command`. Цена на соединение — [замеры](SPECS/TASKS/138-URLTEST_RR_SKIP_DEAD_SLOT/IMPLEMENTATION_REPORT.md).

Без build-тега · проверено на устройстве на реальном пуле из нескольких узлов · [фича](SPECS/FEATURES/007-URLTEST_BALANCE/FEATURE.md) · [руководство](docs-lx/lx-urltest.ru.md#самовосстановление-пула) ([EN](docs-lx/lx-urltest.md#pool-self-heal))

### Группа DNS-серверов отвечает как один сервер

Резолв переживает отказ одного публичного резолвера. Запись `dns.servers[]` с `type: group` опрашивает своих участников в режиме `stable`, `fastest` или `parallel` на TTL-модели, а `hosts` отвечает на локальные имена до обращения к сети.

```jsonc
{ "type": "group", "tag": "dns-public", "mode": "stable", "servers": ["dns-cf", "dns-google", "dns-quad9"] }
```

Без build-тега · в релизах; полевой прогон впереди · [фича](SPECS/FEATURES/013-DNS_GROUP/FEATURE.md) · [руководство](docs-lx/lx-dns-group.ru.md) ([EN](docs-lx/lx-dns-group.md))

### Снифферы узнают WireGuard, OpenVPN, IKE, Tailscale и SIP за роутером

Роутер маршрутизирует VPN-туннели и SIP-звонки чужих устройств по правилам: туннели — через один outbound, звонки — напрямую. Имена `wireguard`, `openvpn`, `ike`, `tailscale`, `sip` работают в действии `sniff` и в правилах `protocol`; они сверяют первый пакет потока и стоят перед сниффером bittorrent, поэтому WireGuard не получает лейбл `bittorrent`.

Ограничения: обфусцированные туннели (AmneziaWG с изменёнными заголовками) и RTP не распознаются — их маршрутизируйте по `ip_cidr` сервера.

Без build-тега · в релизах; прогон на роутере впереди · [фича](SPECS/FEATURES/016-SNIFF/FEATURE.md) · [руководство](docs-lx/lx-sniff.ru.md) ([EN](docs-lx/lx-sniff.md))

### Простаивающие WireGuard-туннели на Android засыпают

Профиль с десятком узлов WireGuard или AmneziaWG тратит батарею и память только на те узлы, которыми пользуются. Апстрим держит каждый endpoint живым вместе с приёмными буферами и таймерами; `lx.wg.idle.suspend`, `suspend_reachable` и `teardown` усыпляют простаивающие endpoint'ы на трёх уровнях; пробуждение с первых двух стоит один RTT, с третьего — пересборку. Замер на устройстве: восемь спящих endpoint'ов освободили 134 МБ, память процесса −31 % ([lx-energy.ru.md](docs-lx/lx-energy.ru.md), [EN](docs-lx/lx-energy.md)).

Ограничения: входит только в AAR (`with_lx_idle_suspend`); десктопный бинарь с этим ключом останавливается на старте с понятной ошибкой. Активная группа `least_test` проверяет каждого участника и будит спящих — где это важно, берите `mode: failover`.

[фича](SPECS/FEATURES/008-ENERGY/FEATURE.md)

### Демон `lxd` сохраняет управляющий канал при каждой смене конфига

Ядром на роутере или сервере управляют удалённо, и оно остаётся на связи, когда новый конфиг сломан. `sing-box lxd` запускает ядро внутри демона, которому принадлежит слушающий порт, поэтому канал поднимается до ядра и переживает его.

```bash
sing-box lxd --state-dir ./lxd-state -c config.json
```

- **Apply с откатом.** `POST /admin/apply` проверяет кандидата; если старт не удался, демон возвращается к последнему рабочему конфигу.
- **Один порт, две плоскости.** gRPC — состояние и логи, REST `/admin/*` — управление.
- **mTLS с регистрацией.** Демон сам себе CA, печатает одноразовое приглашение и дальше узнаёт клиентов по сертификату.
- **Телеметрия хоста.** CPU по ядрам, память, температура, диски, интерфейсы и справочник «IP → устройство» — на том же порту.
- **Наборы правил и гео-базы заливаются по имени.** `PUT /admin/resources/{name}` отвергает набор правил, который ядро не загрузит, и держит прежнюю копию, пока ядро не стартует с новой.
- **Ядро обновляет себя на Linux-роутере.** `sing-box lxd update` скачивает релизный архив под платформу, сверяет его с `SHA256SUMS`, атомарно подменяет бинарь, перезапускает службу и откатывается, если новый бинарь не поднялся; на роутере с маленькой флешкой переходит в режим малого места.
- **Установка службой.** На macOS и Windows — автоматическая. На Linux (systemd, OpenWrt / procd) демон печатает готовый рецепт и диск не трогает; для OpenWrt есть [скрипт установки](scripts-lx/openwrt/README.ru.md).

Ограничения: нет в сборке для Windows 7 и в AAR.

`with_lxd` · проверено на macOS; скрипты для OpenWrt опробованы в поле · [фича](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md) · [руководство оператора](docs-lx/lxd-daemon.ru.md) ([EN](docs-lx/lxd-daemon.md)) · [VPN на отдельном SSID на OpenWrt](docs-lx/openwrt-vpn-ssid.ru.md) ([EN](docs-lx/openwrt-vpn-ssid.md))

## Кто использует

Одно и то же ядро и один и тот же JSON конфига работают в разных продуктах; маппинг ссылок подписок на поля конфига — работа самого продукта.

- **Десктоп** — [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) кладёт бинарь к себе и может запускать его как `lxd`.
- **Android** — [LxBox](https://github.com/Leadaxe/LxBox) встраивает `libbox.aar`; F-Droid собирает LxBox из исходников с sing-box-lx как библиотекой.
- **Роутеры** — панели и скрипты для Keenetic / Netcraze, OpenWrt и Asus Merlin ставят ядро или встраивают его как Go-модуль.
- **Другие клиенты** — независимые клиенты для десктопа, Android и платформ Apple пинят релиз или форкают ядро; документация ядра Hiddify называет sing-box-lx и utls-lx источником своих REALITY `key_share`, режимов DNS-группы и браузерных TLS-отпечатков.
- **Дистрибутивы** — пакет `sing-box` в ALT Linux собирается с `with_xhttp` и `with_awg`.

Полный список со ссылками и тем, что берёт каждый проект, — **[docs-lx/consumers.ru.md](docs-lx/consumers.ru.md)** ([EN](docs-lx/consumers.md)), состояние на 2026-11-23.

## Для разработчиков: свой клиент, лаунчер или прошивка роутера на этом ядре

Команда, которая выпускает VPN-клиент, десктопный лаунчер, прошивку роутера или панель управления, получает от ядра то, что особенно важно при разработке: [наблюдаемость](#наблюдаемость-по-grpc-приложение-видит-что-делает-ядро) — живое состояние ядра по gRPC; [эмуляцию плохой сети](#эмуляция-плохой-сети-без-root-канал-управляемо-портится-внутри-ядра) без root, чтобы проверить продукт на плохой линии раньше пользователей; [API демона для панели роутера и хуки прошивки](#api-демона-для-веб-панели-роутера-и-хуки-прошивки); [сборку любой ветки](#сборка-любой-ветки-и-проверка-конфигов) для проверки на устройстве.

### Наблюдаемость по gRPC: приложение видит, что делает ядро

Клиент или панель показывает живое состояние ядра, не разбирая логи и не открывая порт Clash API. Один контракт ходит по двум каналам: gRPC демона `lxd` на десктопе, сервере или роутере и `CommandClient` libbox в библиотеке для Android и iOS. Поля значат одно и то же в обоих, поэтому один код интерфейса обслуживает и телефон, и удалённый роутер.

- **Соединения.** Поток таблицы соединений с путём каждого через группы и цепочки (`detourList`) и сработавшим правилом (`GetRules`); закрыть соединение — один вызов.
- **DNS.** Каждый запрос с ответами и цепочкой CNAME (`SubscribeDNSQueries`).
- **Группы и узлы.** `GetGroups`, слоты балансировщика с `live` / `dead` / `quarantined` и переведёнными сайтами (`GetPool`), цепочки (`GetChainStatus`), DNS-группы, состояние endpoint'ов WireGuard / AmneziaWG и Tailscale по тегу.
- **Пробы по запросу.** Задержка одного узла (`URLTestOutbound`) и то, какой адрес выхода даёт узел (`GetURLViaOutbound`).
- **Ядро целиком.** Работающий конфиг как написан и как собран (`GetRunningConfig`, `GetEffectiveConfig`, `GetOutboundConfig`), лог ядра, память, горутины, трафик.

```text
SubscribeConnections{interval: 1s}       → таблица соединений с путём каждого
SubscribeDNSQueries{includeAnswers: true} → DNS-запросы с цепочками CNAME
GetPool{tag}                             → слоты балансировщика и их состояние
```

Те же вызовы доступны из терминала: апстримную команду `sing-box api` форк дополняет подкомандами `group pool`, `connection-test` и `impair`.

Ограничения: вызовы форка есть в сборке с `with_lx_command`; без тега они отвечают `Unimplemented`.

`with_lx_command` · этим пользуется LxBox · [фича](SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md) · [API](docs-lx/lxd-grpc-api.ru.md) ([EN](docs-lx/lxd-grpc-api.md))

### Эмуляция плохой сети без root: канал управляемо портится внутри ядра

Балансировщик, failover или новый транспорт проверяются на настоящих серверах под плохой линией без netem, root и стенда: outbound `impair` ухудшает канал прямо в ядре, на десктопе, роутере или телефоне. Что портить, насколько и когда, задаёт конфиг, а `seed` повторяет прогон один в один. Он стоит под прокси-узлом и портит дорогу до его сервера в обе стороны с одного конца:

- узкое место со скоростью и очередью, задержка и джиттер, скачки скорости;
- потери пачками (модель Гилберта) для датаграмм; на TCP-носителе потеря останавливает поток, как повторная передача;
- случайные остановки всей линии и обрывы по расписанию, один раз или повторяющимся кругом, молчаливые или со сбросом соединений;
- подлинии для части трафика: один сайт, порт, каждое N-е соединение, доля UDP-потоков — когда `impair` стоит над узлом, он видит каждый сайт;
- профили `dsl`, `lte-walk`, `wifi-lossy` и свои в `lx.impair_profiles`, одна смена `profile` меняет всю линию;
- `seed`: каждый прогон с теми же потерями, скачками и остановками, поэтому регрессия видна как разница сборок, а не везения.

```jsonc
{ "type": "impair", "tag": "line-a", "seed": 1, "start": "first_use", "schedule_repeat": "2m",
  "link_params": { "profile": "lte-walk" },
  "sublinks": [ { "match": { "domain_suffix": "example.org" }, "link_params": { "loss": "30%" } } ],
  "schedule": [ { "at": "60s", "link_params": { "outage": true, "reset": true } },
                { "at": "70s", "link_params": { "outage": false } } ] }
```

Эксперимент управляется на лету, без перезапуска ядра, который сбросил бы состояние групп. `sing-box api impair set` или `SetImpair` по gRPC меняют линию посреди прогона, `ScheduleImpair` заменяет расписание, а наблюдаемость показывает, как отвечают пул и соединения:

```bash
sing-box api impair set line-a '{"link_params": {"outage": true, "reset": true}}'   # оборвать линию сейчас
sing-box api group pool auto                                                        # что сделал балансировщик
sing-box api impair set line-a '{"link_params": {"profile": "dsl"}}'                 # дальше ровная линия
```

`with_lx_impair` есть в релизных бинарях и в AAR, добавляет 150 КБ и ничего не делает, пока в конфиге нет `"type": "impair"`; при старте пишет в лог предупреждение, что канал ухудшается намеренно. Ограничения: приём ухудшается после настоящей сети, поэтому сервер видит настоящее время туда-обратно.

[руководство с рецептами для `round_robin` и `failover`](docs-lx/lx-impair.ru.md#рецепты-для-испытаний-балансировки) ([EN](docs-lx/lx-impair.md#recipes-for-balancing-tests)) · [фича](SPECS/FEATURES/020-IMPAIR/FEATURE.md)

### API демона для веб-панели роутера и хуки прошивки

Панель роутера строится на готовом API демона `lxd`, без своего агента на устройстве. Тот же порт и тот же mTLS, что у наблюдаемости:

- **Кто в сети.** `GET /admin/clients-info` превращает адреса LAN в устройства: имя, MAC, SSID, интерфейс; `PUT /admin/clients-info/labels/{key}` хранит имя, которое дал оператор.
- **Состояние машины.** `GET /admin/host` — CPU по ядрам, память, датчики температуры, диски, дескрипторы; `GET /admin/host/interfaces` — счётчики и скорости каждого интерфейса; `GET /admin/stats` — трафик и соединения ядра.
- **Кнопки обслуживания.** `daemon.json` задаёт именованные команды (`"update": "/usr/bin/sing-box lxd update"`), а `POST /admin/commands/{name}` запускает их по имени, без входных данных от клиента; вывод и код выхода читаются тем же API.
- **Профили.** `/admin/pprof` отдаёт heap, goroutine, CPU и trace с удалённой машины.

Хуки встраивают ядро в прошивку. Демон запускает скрипты из `<state-dir>/hooks/` на `pre_start`, `post_start`, `post_reload` и `post_stop` и передаёт в окружении событие и причину (`LXD_EVENT`, `LXD_REASON`), а также интерфейс, адрес и MTU TUN (`LXD_TUN_INTERFACE`, `LXD_TUN_ADDRESS`, `LXD_TUN_MTU`). Прошивка дописывает свои правила файрвола и маршруты под тот TUN, который поднялся. Ошибка `pre_start` не даёт ядру стартовать; исход последнего запуска каждого хука виден в `/admin/status`.

```sh
#!/bin/sh
# <state-dir>/hooks/post_start
nft add rule inet fw4 forward iifname "$LXD_TUN_INTERFACE" accept
```

Ограничения: демона нет в AAR и в сборке для Windows 7.

[руководство оператора](docs-lx/lxd-daemon.ru.md) ([EN](docs-lx/lxd-daemon.md)) · [хуки](docs-lx/lxd-daemon.ru.md#6a-хуки-демон-вызывает-скрипты-админа-при-смене-состояния-ядра) · [фича](SPECS/FEATURES/014-LXD_DAEMON/FEATURE.md)

### Сборка любой ветки и проверка конфигов

Workflow `lx-build.yml` собирает из любой ветки или тега этого репозитория бинарь, Linux musl, Android AAR или Apple xcframework, поэтому ветку с правкой можно поставить на устройство до релиза:

```bash
gh workflow run lx-build.yml -f target=android-aar -f branch=<ветка>
make -f Makefile.lx lx-check      # сборка и sing-box check каждого конфига из lx-test/config/
```

## Как форк держится рядом с апстримом

```
upstream/stable  ──merge──►  lx  =  upstream  +  швы // lx  +  lx-файлы  +  4 форк-сабмодуля
                                     │
                                     └─►  тег vX.Y.Z-lx.N  ──►  lx-release.yml  ──►  GitHub Release
```

- **Каждый релиз `upstream/stable` вливается вручную, без ребейза.** По ветке `lx` не делается force-push; дрейф меряется только против `upstream/stable`.
- **Код форка отделим.** Новый код живёт в своих файлах за build-тегами; правка в апстримном файле — блок с пометкой `// lx`, и `grep -rn "lx:begin"` перечисляет их все. Путь Go-модуля остаётся `github.com/sagernet/sing-box`.
- **Четыре форк-сабмодуля, каждый — свой апстрим плюс несколько коммитов:** [wireguard-go-awg2-lx](https://github.com/Leadaxe/wireguard-go-awg2-lx) (рантайм AmneziaWG), [sing-tun-lx](https://github.com/Leadaxe/sing-tun-lx) (self-heal accept-loop), [gvisor-lx](https://github.com/Leadaxe/gvisor-lx) (nil-guard в рукопожатии), [utls-lx](https://github.com/Leadaxe/utls-lx) (пресеты Firefox 148 и Safari 26.3).
- **У исправлений есть срок годности.** Баги, найденные в реальной практике, чинятся здесь с тестом; каждый патч в [реестре HOTFIXES](SPECS/FEATURES/004-HOTFIXES/FEATURE.md) называет условие, при котором его снимают.
- **Релизы.** Теги `vX.Y.Z-lx.N` — стабильные, `-rc.N` / `-alpha.N` / `-beta.N` — пререлизы. Процедура — [раннбук релиза](docs-lx/lx-release-runbook.ru.md) ([EN](docs-lx/lx-release-runbook.md)); заметки — [`docs-lx/releases/`](docs-lx/releases/); инженерный лог — [lx-changelog.md](docs-lx/lx-changelog.md).
- **Правила и процесс.** В [CONSTITUTION](SPECS/CONSTITUTION.md) — правила, по которым живёт каждая фича; [`SPECS/FEATURES`](SPECS/FEATURES/README.md) описывает текущее состояние каждой фичи, [`SPECS/TASKS`](SPECS/README.md) — каждую единицу работы. Путь фичи и требования к PR — [CONTRIBUTING.ru.md](CONTRIBUTING.ru.md).

Скоуп: client-first. Форк добавляет outbound'ы и endpoint'ы; единственный inbound — `trusttunnel`.

## Известные проблемы и частые вопросы

### Xray Mux.Cool — не `multiplex` из sing-box

`mux` в Xray (Mux.Cool) и блок `multiplex` в sing-box — разные протоколы с одной идеей. `multiplex` говорит на sing-mux (`smux` / `yamux` / `h2mux`) и работает, только если сервер — sing-box с включённым multiplex; Xray-сервер его не понимает, и узел перестаёт пропускать трафик. Поэтому `mux` из Xray-ссылки или подписки нельзя переводить в `multiplex` — его надо отбросить. Для UDP ничего не теряется: `vless`-outbound по умолчанию упаковывает UDP в XUDP, см. `packet_encoding` в справочнике [VLESS outbound](https://sing-box.sagernet.org/configuration/outbound/vless/).

### REALITY: `reality verification failed` после обновления Xray на сервере

Нужен отпечаток `chrome`, `firefox` или `safari` и пустой `key_share` — см. [REALITY](#reality-работает-с-актуальными-серверами-xray-включая-постквантовый-x25519mlkem768). Если рукопожатие висит без ошибки, сеть теряет второй сегмент ClientHello: добавьте `record_fragment` или `detour`. Таблица симптомов — [xray-protocols-explained.ru.md §2.7](docs-lx/xray-protocols-explained.ru.md#27-типичные-отказы) ([EN](docs-lx/xray-protocols-explained.md#27-typical-failures)).

## Документация

| | |
|---|---|
| Обзор конфигурации | [docs-lx/lx-config.ru.md](docs-lx/lx-config.ru.md) ([EN](docs-lx/lx-config.md)) — каждое поле каждой фичи, с примерами |
| Протоколы и транспорты | [docs-lx/protocols-transports.ru.md](docs-lx/protocols-transports.ru.md) ([EN](docs-lx/protocols-transports.md)) — XHTTP, AmneziaWG, MASQUE, REALITY, VLESS `encryption`, TrustTunnel в деталях |
| Как устроены протоколы Xray | [docs-lx/xray-protocols-explained.ru.md](docs-lx/xray-protocols-explained.ru.md) ([EN](docs-lx/xray-protocols-explained.md)) — REALITY, Vision, XHTTP, VLESS `encryption`: механика, примеры Xray ↔ sing-box-lx |
| Как устроен AmneziaWG | [docs-lx/amneziawg-explained.ru.md](docs-lx/amneziawg-explained.ru.md) ([EN](docs-lx/amneziawg-explained.md)) — три слоя обфускации, бюджет MTU, `awg.conf` ↔ endpoint |
| Как устроен TrustTunnel | [docs-lx/trusttunnel-protocols-explained.ru.md](docs-lx/trusttunnel-protocols-explained.ru.md) ([EN](docs-lx/trusttunnel-protocols-explained.md)) — формат на проводе, модель сессии, outbound и inbound |
| Руководство по энергии | [docs-lx/lx-energy.ru.md](docs-lx/lx-energy.ru.md) ([EN](docs-lx/lx-energy.md)) — уровни сна, `mode: failover`, конфиг для мобильного клиента |
| Снифферы | [docs-lx/lx-sniff.ru.md](docs-lx/lx-sniff.ru.md) ([EN](docs-lx/lx-sniff.md)) |
| Руководство оператора `lxd` | [docs-lx/lxd-daemon.ru.md](docs-lx/lxd-daemon.ru.md) ([EN](docs-lx/lxd-daemon.md)) |
| API наблюдаемости | [docs-lx/lxd-grpc-api.ru.md](docs-lx/lxd-grpc-api.ru.md) ([EN](docs-lx/lxd-grpc-api.md)) — контракт, на котором говорят клиенты: и демон, и Android-AAR |
| Разбор OpenWrt | [docs-lx/openwrt-vpn-ssid.ru.md](docs-lx/openwrt-vpn-ssid.ru.md) ([EN](docs-lx/openwrt-vpn-ssid.md)) · [скрипты установки](scripts-lx/openwrt/README.ru.md) |
| Кто использует ядро | [docs-lx/consumers.ru.md](docs-lx/consumers.ru.md) ([EN](docs-lx/consumers.md)) |
| Релизы | [заметки к релизам](docs-lx/releases/) · [lx-changelog.md](docs-lx/lx-changelog.md) · [раннбук релиза](docs-lx/lx-release-runbook.ru.md) ([EN](docs-lx/lx-release-runbook.md)) |
| Референсные ядра | [docs-lx/lx-reference-cores.ru.md](docs-lx/lx-reference-cores.ru.md) ([EN](docs-lx/lx-reference-cores.md)) — где искать ответы по wire-протоколу |
| Spec Kit | [SPECS/FEATURES](SPECS/FEATURES/README.md) · [SPECS/TASKS](SPECS/README.md) · [CONSTITUTION](SPECS/CONSTITUTION.md) · [CONTRIBUTING](CONTRIBUTING.ru.md) |
| sing-box | [SagerNet/sing-box](https://github.com/SagerNet/sing-box) · [документация](https://sing-box.sagernet.org/) — платформа, на которой построено ядро: протоколы, маршрутизация, TUN-стек, `libbox` |
| Происхождение протоколов | [XTLS/Xray-core](https://github.com/XTLS/Xray-core) (XHTTP, REALITY, VLESS encryption) · [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [Cloudflare WARP / MASQUE](https://developers.cloudflare.com/warp-client/) |

## Карта репозитория

| Путь | Назначение |
|------|------------|
| `Makefile.lx` | сборка с lx-набором тегов и версией `-lx`: `lx-build`, `lx-check`, `lx-print-tags`, `lx-proto` |
| `.github/workflows/lx-ci.yml`, `lx-release.yml`, `lx-build.yml` | CI, релиз по тегам `v*-lx.*`, сборщик по запросу для каждой ветки |
| `SPECS/`, `docs-lx/` | Spec Kit и документация форка (EN + RU) |
| `lx-test/`, `scripts-lx/openwrt/` | примеры конфигов и живые стенды; роутерный установщик `lxd` |
| `transport/v2rayxhttp/`, `option/v2ray_xhttp.go` | XHTTP |
| `transport/wireguard/device_awg.go`, `option/wireguard_awg.go`, `submodules/wireguard-go` | AmneziaWG |
| `protocol/masque/`, `option/masque_lx.go` | MASQUE / CONNECT-IP |
| `protocol/chain/`, `option/chain_lx.go` | `chain` |
| `common/tls/` (`*_lx*`), `submodules/utls` | key share REALITY, фрагментация, пресеты отпечатков |
| `common/sniff/*_lx.go` | снифферы |
| `dns/transport/group/`, `common/dnstrack/` | группа DNS и трасса запросов за `SubscribeDNSQueries` |
| `experimental/libbox/`, `daemon/` (швы `lx:`), `lxd/` | расширения `CommandClient` и демон |
| `submodules/sing-tun`, `submodules/gvisor` | форк-сабмодули для TUN-стека |

## Поддержать проект

sing-box-lx бесплатный, платных функций в нём нет. Как помочь деньгами и без них — **[docs-lx/donate.ru.md](docs-lx/donate.ru.md)** ([EN](docs-lx/donate.md)).

## Лицензия

**GPL-3.0**, унаследована от sing-box. Правки форка помечены `// lx` и распространяются под той же лицензией. Это неофициальный форк, не аффилированный с SagerNet.
