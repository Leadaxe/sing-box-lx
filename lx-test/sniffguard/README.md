# Стенд `lx.sniff_guard` (FEATURE 019, SPEC 060 §6)

Loopback-стенд против настоящего сниффера Xray (`sniffing.destOverride: ["http","tls","quic"]`,
`routeOnly: false`). Всё слушает только `127.0.0.1`; TUN, маршрутов, системного прокси и
смены DNS нет. Маскировочный SNI узлов — `decoy.invalid`: имя не резолвится, поэтому сниффер,
подменивший назначение, ломает соединение, а не уводит его на чужой хост.

```bash
export SG_SINGBOX=/path/to/sing-box   # сборка из рабочего дерева под LX_TAGS (по умолчанию bin/sing-box)
export SG_XRAY=/path/to/xray          # по умолчанию bin/xray
export SG_WORK=/path/to/workdir       # конфиги, ключи, логи (по умолчанию .stand/, gitignored)
lx-test/sniffguard/stand.sh gen       # самоподписанный сертификат srv.test + server.json, xray.json
lx-test/sniffguard/stand.sh up        # HTTP-origin, серверы sing-box, Xray
lx-test/sniffguard/stand.sh list      # сценарии
lx-test/sniffguard/stand.sh run a-tls-clean 3   # клиент, по 3 curl на узел, строки sniff guard и Xray
lx-test/sniffguard/stand.sh check bad-persist-no-cache
lx-test/sniffguard/stand.sh down
```

`client NAME` / `curl PORT N [URL]` / `client-down` — ручной режим, когда клиент должен жить между
шагами (переход метки hysteria2/tuic, `persist` с перезапуском). `capture NAME [N]` — первые N
датаграмм клиента NAME на UDP-приёмник `127.0.0.1:28540` в `capture.hex` (источник `i1`).

## Роли и порты

| Порт | Что |
|---|---|
| 28580/tcp | HTTP-origin (`python3 -m http.server`), конечная цель `curl` |
| 28501/tcp | trojan + TLS (sing-box) |
| 28502/udp, 28503/udp | hysteria2, tuic (sing-box) |
| 28510/tcp | Xray vless-инбаунд со сниффингом — стенд A «релей», узел идёт через `detour` |
| 28521/tcp, 28522/udp, 28523/udp | Xray `dokodemo-door` с фиксированным назначением на 28501/28502/28503 и тем же сниффингом — стенд B «прямой путь»: узел без `detour`, сокет ядра |
| 28530/udp | Xray `wireguard`-инбаунд без сниффинга — обычный WireGuard-сервер для сценариев AWG |
| 28540/udp | `stand.sh capture`: приёмник первых датаграмм клиента |
| `SG_LAN_IP`:28581/tcp | второй HTTP-origin, цель `curl` через туннель WireGuard |
| 28600+ | `mixed`-инбаунды клиента, по одному на узел |

WireGuard-сервер — Xray, а не sing-box: endpoint sing-box слушает `*:port`, `inet4_bind_address`
его bind не учитывает. gVisor-стек WireGuard-сервера дропает внутренние пакеты с назначением
`127.0.0.1` как martian (симптом — ложный таймаут дайла), поэтому цель за туннелем — второй
origin на первом не-loopback IPv4 хоста (`SG_LAN_IP`, по умолчанию `ipconfig getifaddr en0`).
Внешний UDP WireGuard и инбаунды клиента остаются на `127.0.0.1`.

Без pf прямой путь эмулируется `dokodemo-door`: для клиента это обычный сокет ядра к
`127.0.0.1`, сниффер стоит на нём, как прозрачный прокси на роутере. Freedom в Xray нужен
`finalRules` с разрешением `127.0.0.0/8` и `::1/128`: по умолчанию он блокирует приватные
адреса (ключ есть и в снимке Xray 26.7.28).

Сценарии — словарь `S` в `gen.py`: префикс `a-` — через релей, `b-` — прямой путь, `p2-` — без
сниффера (обещание P2), `bad-` — конфиги, которые `check` должен отвергнуть, `awg-` — контроль
WireGuard без сниффера, `cap-` — источники для `capture`. Узел AWG — `jc 4/40/70` и `ip: quic`,
`id: decoy.invalid`, `ib: chrome` (как в HISTORY 060 §6.2) или статические `i1`/`i2` из
`capture.hex`; лог клиента со сценарием AWG — на уровне trace (строка приманки AWG — trace).

## Что снято (2026-11-13, Xray 26.7.28, ядро `6504471ea` под `LX_TAGS`)

| Сценарий | Результат |
|---|---|
| A-TLS без блока | 0 из 3; Xray `sniffed domain: decoy.invalid`, `failed to open connection to tcp:decoy.invalid:28501`, у клиента `EOF` через ~1 с |
| A-TLS `record_fragment` / `delay` / `fragment` | 3 из 3 каждый; 0,03 / 0,36 / 0,56 с; Xray маршрутизирует на `127.0.0.1:28501` |
| A-TLS `{"vless":"clean"}` | 3 из 3; первый curl 1,07 с: `failed without protection (EOF), retrying with protection` и Info `clean -> suspect` в том же дайле |
| A-TLS контрольный узел `localhost` | на `suspect` 3 из 3; на `clean` 2 из 2 без защиты (`sniffed domain: localhost`), метку не двигает — маскировочный узел после него падает первым и учит `clean -> suspect` |
| A-TLS `persist` | прогон 1 учит `clean -> suspect`; после перезапуска первый curl 0,04 с, без неудачной попытки |
| A-TLS `record_fragment: false` | 0 из 3 (сниффится); с `override: true` 3 из 3 |
| A-QUIC без блока | hysteria2 0 из 3, tuic 0 из 3 |
| A-QUIC `decoy_first_byte` | 3 из 3 и 3 из 3 |
| A-QUIC `{"vless":"clean"}` | curl 1 — таймаут 12 с, curl 2 садится на тот же висящий дозвон и падает через 15 с от его начала; curl 3 открывает новое соединение: `failed without protection, next dial goes with protection`, оно же уходит с приманкой и проходит. Метка остаётся `clean`, пока узел не откроет следующее новое соединение: живое соединение пережило 45 с простоя; после перезапуска сервера переподключение дало Info `clean -> suspect`, и tuic на том же релее прошёл с первого раза |
| A-QUIC `delay` | 3 из 3 и 3 из 3; ClientHello занимает два Initial (1250 B у hysteria2, 1280 B у tuic), второй задержан на 300 мс |
| B-TLS без блока | 0 из 3 |
| B-TLS дефолтная карта + `record_fragment` | 3 из 3; первый curl 1,08 с, `tls path direct: clean -> suspect` |
| B-TLS `{"direct":"suspect"}` + `record_fragment` / `delay` / `fragment` | 3 из 3 каждый; 0,03 / 0,35 / 0,57 с |
| B-QUIC без блока | 0 из 2 и 0 из 2 |
| B-QUIC `decoy_first_byte` + `{"direct":"suspect"}` | 2 из 2 и 2 из 2 |
| P2 без сниффера, все методы под `suspect` | trojan `record_fragment`/`fragment`/`delay`, hysteria2 и tuic `decoy_first_byte`/`delay` — все 2 из 2 |
| `check` | все рабочие конфиги проходят; `lx.quic_sniff_decoy`, `outbound_type: []`, `persist` без `cache_file` — ошибка с именем ключа |

`fragment` на прямом пути защищает: ACK на loopback приходит быстрее 20 мс, и апстримный
`writeAndWaitAck` (`common/tlsfragment/wait_darwin.go`, так же `wait_linux.go`) в этом случае сам
спит `fragment_fallback_delay` (500 мс) — «under transparent proxy». Пауза больше бюджета
Xray в 200 мс, отсюда 0,57 с на рукопожатие.

### AmneziaWG (2026-11-13, те же Xray и ядро)

| Сценарий | Результат |
|---|---|
| `awg-plain-none` / `awg-plain-quic` / `awg-relay-none` (контроль) | 3 из 3 каждый: без релея и без маскировки, без релея с `ip: quic`, через релей без маскировки |
| `a-awg-noguard` без блока | 0 из 3; Xray `sniffed domain: decoy.invalid`, `failed to open connection to udp:decoy.invalid:28530` |
| `a-awg-decoy` дефолтная карта | 3 из 3; trace `amneziawg: sniff decoy datagram sent ahead of the masquerade burst`, Xray `default route for udp:127.0.0.1:28530` |
| `a-awg-clean` `{"vless":"clean"}` | 0 из 3, строк `sniff guard` нет: AWG метку не двигает |
| `a-awg-hy2-clean` (общая метка, `persist`) | AWG 0 из 2; hysteria2 учит `clean -> suspect`; AWG на старом сокете 0 из 1; самолечение WireGuard (`rebound socket for self-heal (trigger=early)`) открывает новый сокет, он читает `suspect` и идёт с приманкой — 3 из 3. После перезапуска клиента метка из кеша: первый сокет AWG с приманкой, 3 из 3 |
| `a-awg-delay` `quic.method: delay` | 0 из 3, как без блока: приманки нет, Xray подменяет назначение |
| `awg-plain-i1` / `a-awg-i1-noguard` / `a-awg-i1-decoy` | `i1`+`i2` — два Initial hysteria2 (по 1250 B, один DCID) из `capture cap-hy2`: без релея 3 из 3, через релей без блока 0 из 3 (`udp:decoy.invalid`), с `decoy_first_byte` 3 из 3 |

Новый сокет AWG появляется только при перебинде: самолечение `early` — после трёх
неотвеченных инициаций, когда трафик к узлу перестал идти (каждый новый пакет сбрасывает счётчик
попыток), не чаще раза в 90 с. Под непрерывными curl каждые 12 с перебинда не было.

