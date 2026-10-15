# Стенд TrustTunnel (FEATURE 018, SPEC 133 §6 / SPEC 134 §6)

Loopback-стенд с **официальными** бинарями TrustTunnel: endpoint (Rust, `TrustTunnel/TrustTunnel`)
и CLI-клиент (`TrustTunnel/TrustTunnelClient`). Релизы подписаны ключом AdGuard
`28645AC9776EC4C00BCE2AFC0FE641E7235E2EC6`, `stand.sh install` проверяет подписи.
Всё слушает только `127.0.0.1`; клиент работает как SOCKS5-прокси, без TUN, маршрутов,
kill switch и смены системного DNS, поэтому стенд не мешает остальной сети машины.

```bash
lx-test/trusttunnel/stand.sh install     # скачать и проверить бинари в bin/ (gitignored)
lx-test/trusttunnel/stand.sh gen         # сертификат tt.test + конфиги в .stand/ (gitignored)
lx-test/trusttunnel/stand.sh up          # endpoint 127.0.0.1:24441 (TCP = h2, UDP = h3) + HTTP-origin 24443
lx-test/trusttunnel/stand.sh client-up   # официальный клиент, SOCKS5 на 127.0.0.1:24442
lx-test/trusttunnel/stand.sh check       # curl через клиента: приватный origin и example.com
lx-test/trusttunnel/stand.sh down
```

Наш sing-box (тег `with_trusttunnel` в `LX_TAGS` с 2026-10-16, фича экспериментальная; стенд собирает свой бинарь из рабочего дерева):

```bash
lx-test/trusttunnel/stand.sh build       # LX_TAGS (с with_trusttunnel) -> bin/sing-box (nice 15, -p 4)
lx-test/trusttunnel/stand.sh sb-out      # наш trusttunnel outbound, mixed на 127.0.0.1:24444
lx-test/trusttunnel/stand.sh check-out   # curl через него: приватный origin и example.com
lx-test/trusttunnel/stand.sh sb-in       # наш trusttunnel inbound 127.0.0.1:24445 (h2 + h3)
                                         # + официальный клиент (client-sb.toml), SOCKS5 на 24446
lx-test/trusttunnel/stand.sh check-in    # curl через официальный клиент в наш inbound
lx-test/trusttunnel/stand.sh sb-down     # остановить sb-out, sb-in и client-sb
```

Параметры (env): `TT_PORT`, `TT_SOCKS_PORT`, `TT_HTTP_PORT`, `TT_SNI` (по умолчанию `tt.test`),
`TT_USER`/`TT_PASS`, `TT_CLIENT_PROTO=http2|http3` (перед `gen`; для `sb-in` — при запуске),
`ENDPOINT_VER`, `CLIENT_VER`; для нашего sing-box — `TT_SB_OUT_PORT` (24444), `TT_SB_IN_PORT`
(24445), `TT_SB_SOCKS_PORT` (24446), `TT_OUT_TARGET=endpoint|inbound` (куда смотрит наш
outbound), `TT_OUT_QUIC=1` (HTTP/3), `TT_OUT_PASS` / `TT_CLIENT_PASS` (неверный пароль для
негативных проверок). Конфиги `sb-out.json`, `sb-in.json`, `client-sb.toml` и логи
`sb-out.log`, `sb-in.log`, `client-sb.log` генерируются в `.stand/` при каждом запуске.

## Роли

- **Наш outbound (SPEC 133)** — `sb-out`: `server: 127.0.0.1`, `server_port: 24441`
  (или 24445 при `TT_OUT_TARGET=inbound`), `tls.server_name: tt.test`,
  `tls.certificate_path: .stand/cert.pem`, `username: lx`, `password: lx-stand-secret`.
  Перед ним `mixed` на 24444; в маршруте действие `resolve` (`ipv4_only`), а DNS-сервер —
  UDP `1.1.1.1` с `detour` через сам outbound, так что `curl --socks5-hostname` гоняет и
  UDP-путь (`_udp2`). Endpoint запущен с `allow_private_network_connections = true`, иначе
  loopback-origin получит 502 + `X-Warning: 301`.
- **Наш inbound (SPEC 134)** — `sb-in`: `trusttunnel` на 127.0.0.1:24445 (`network` оба:
  TCP = h2, UDP = h3), тот же сертификат, пользователь `lx` / `lx-stand-secret`, выход —
  `direct`. Официальный клиент к нему — `client-sb.toml` (копия `client.toml` с адресом
  24445 и SOCKS5 на 24446).
- Официальный клиент в режиме SOCKS5 **не туннелирует loopback-назначения** — `curl` к
  `127.0.0.1:24443` через него идёт мимо туннеля. Проверка приватного origin через клиента —
  `curl --socks5-hostname 127.0.0.1:24446 http://localhost:24443/` (домен уходит в `CONNECT`), так и делает `check-in`.
  `_udp2` клиент открывает только на UDP ASSOCIATE (DNS-запрос через SOCKS5 UDP), не на
  `curl --socks5-hostname`.

## Что снято на стенде (2026-10-15, endpoint 1.1.0, клиент 1.1.11)

- Обе проверки проходят по H2 и по H3 (`TT_CLIENT_PROTO=http3`).
- Заголовки официального клиента на macOS: `user-agent: Mac trusttunnel_client` на
  `CONNECT host:port`, `user-agent: Mac unknown` на `CONNECT _check`. Health check уходит
  сразу вслед за первым `CONNECT` соединения (событие «новое соединение», SPEC 133 R6).
- Endpoint с `--logfile` при `-l debug` файл не наполняет; смотреть `client.log`.
- Endpoint и origin запущены под `nice -n 10`, чтобы не отбирать CPU у остального.

## Что снято на стенде (2026-10-15, наш sing-box `1.14.3-lx.1` + `with_trusttunnel`)

- Наш outbound → официальный endpoint, H2 и H3: оба `curl` проходят, DNS-запрос
  `example.com` уходит через `_udp2` (в логе `outbound packet connection to 1.1.1.1:53` у
  `tt-out`), SOCKS5 UDP ASSOCIATE через `mixed` тоже.
- Наш outbound → наш inbound, H2 и H3: то же.
- Официальный клиент → наш inbound, `http2` и `http3`: `CONNECT _check` 200, `CONNECT host:port`
  200, `CONNECT _udp2` 200 на SOCKS5 UDP ASSOCIATE (клиент шлёт реальный источник в кадре).
- Неверный пароль: наш outbound → `trusttunnel: server responded 407 Proxy Authentication
  Required` как ошибка dial (H2 и H3, endpoint и наш inbound); официальный клиент получает
  407 + `proxy-authenticate` и завершается, наш inbound пишет
  `process connection from 127.0.0.1:…: authentication failed` без учётных данных.
- `CONNECT` на закрытый порт: и endpoint, и наш inbound — `502 Bad Gateway: 300 - Connection
  failed for some reason` из dial.
- `curl -k https://127.0.0.1:24445/` — `HTTP/2 404`, пустое тело, без `proxy-authenticate`;
  `--http1.1` — соединение закрыто.


## Вспомогательные скрипты

- `relay.py <listen> <target> [log]` — счётный релей TCP+UDP на 127.0.0.1: каждый переданный
  кусок пишется в лог с временем (замена tcpdump, BPF на macOS требует root). `SIGUSR1`
  замораживает существующие потоки (данные дропаются, сокеты живы — так выглядит истёкший
  NAT-маппинг), новые соединения и новые UDP-порты ходят; `SIGUSR2` снимает заморозку.
- `udp_assoc.py <socks> <dest> <size>` — SOCKS5 UDP ASSOCIATE: одна датаграмма заданного
  размера и сверка эха; `udp_assoc.py --udp-echo <port>` — UDP-эхо-сервер.

## Что снято на стенде (2026-10-16, SPEC 133 §6 п. 4–6, SPEC 134 §6 п. 3–7)

- **Большой UDP-кадр**: эхо 9100 байт туда-обратно по H2 и H3, к endpoint и к нашему
  inbound, от нашего outbound и от официального клиента. Потолок — отправитель
  (`net.inet.udp.maxdgram` 9216 на macOS), не туннель.
- **Stale-сессия**: второй endpoint с `client_listener_timeout_secs = 5`, пауза 10 с —
  первый dial 3 мс, сессия пересобрана до CONNECT, ошибок нет (H2 и H3). NAT-обрыв через
  заморозку в `relay.py`: CONNECT на замороженной сессии упирается в бюджет 3,0 с, затем
  пересборка и 200 (H2 и H3). `urltest` через узел при сессии, закрытой сервером, —
  `available`.
- **idle 600 с у нашего inbound**: outbound с `idle_timeout: "0"`, пауза 640 с — inbound закрыл
  сессию, следующий dial 7 мс, прозрачно (H2).
- **`detour`** через `socks` к соседнему sing-box (H2 — TCP, H3 — UDP ASSOCIATE) и **`chain`**
  обоих порядков: `["socks-hop", "tt-out"]` и `["tt-out", "socks-hop"]`.
- **Простой 2 мин** при живой сессии: релей не видел ни байта в обе стороны — H2 и H3 ×
  `health_check` on/off. Idle-teardown на 5-й минуте: H2 — один TLS-alert в каждую сторону,
  H3 — беззвучно (сокет закрывается без CONNECTION_CLOSE).
- **Дефект H3, исправлен**: QUIC `MaxIdleTimeout` был равен `idle_timeout`, и молчащий
  открытый поток (idle TCP-соединение) по H3 умирал через окно, по H2 — нет. Теперь 600 с,
  окно применяет только `idleWatcher`; тест `IdleTeardown` ядра дополнен эхом после паузы.
- **`sniff` на inbound**: CONNECT на закрытый порт — 200 уже отправлен, dial упал, поток
  закрыт, `curl` получает ошибку за миллисекунды, ничего не виснет (наш outbound H2/H3 и
  официальный клиент, который после этого продолжает работать).
- **Официальный клиент `upstream_protocol = "auto"`** против inbound с `network: tcp`:
  клиент пингует только `tcp://`, выбирает H2, работает.
- **Route**: `inbound: tt-in` + `ip_is_private: true` → `reject`: приватная цель отбита
  (наш outbound видит `502` / `X-Warning: 300`), интернет через тот же inbound жив.
- Не снято: §6.7 живой сервер; ICMP у клиента (у CLI 1.1.11 ручки нет); idle 600 с по H3.
