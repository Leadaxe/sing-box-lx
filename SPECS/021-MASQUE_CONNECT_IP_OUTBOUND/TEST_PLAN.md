# SPEC 021 — TEST PLAN + RESULTS

## Стенд

- Бинарь: `go build -tags "with_quic with_gvisor with_wireguard with_utls with_clash_api"`
- Конфиг: sing-box JSON, mixed-inbound (SOCKS/HTTP) → masque-outbound (final route)
- Ключевой материал: реальная WARP-регистрация (ECDSA P-256 priv/pub, ip/ipv6) из
  ClashWARP-конфига (Dart-сторона). Сервер `162.159.198.2:443`, SNI `4pda.to` (domain-fronting).
- Профиль: `cloudflare`.

Пример h3-конфига outbound:
```json
{
  "type": "masque", "tag": "warp",
  "server": "162.159.198.2", "server_port": 443,
  "profile": "cloudflare", "network": "h3", "sni": "4pda.to",
  "private_key": "<base64 DER EC>", "public_key": "<base64 DER PKIX>",
  "ip": "172.16.0.2", "ipv6": "2606:4700:110:...:c7a6", "mtu": 1280,
  "network_list": ["tcp","udp"]
}
```
Нужен `dns` блок в конфиге (например udp 1.1.1.1) — стек L3 не резолвит домены, outbound
резолвит через DNSRouter перед dial.

## Команды

```bash
# запуск
./sing-box run -c masque-h3.json

# TCP + проверка что вышли через WARP
curl -s -x socks5h://127.0.0.1:12345 https://www.cloudflare.com/cdn-cgi/trace
# ожидаем warp=on, ip=<cloudflare edge>
```

---

## RESULTS

Дата: 2026-07-02. Сборка: ветка `lx-spec021-masque` @ (после фикса резолва доменов).

### ✅ h3 (CONNECT-IP over QUIC) — РАБОТАЕТ

```
$ curl -s -x socks5h://127.0.0.1:12345 https://www.cloudflare.com/cdn-cgi/trace
warp=on
ip=104.28.159.13
colo=FRA
loc=DE
tls=TLSv1.3
http=http/2
```

- `warp=on` — Cloudflare подтверждает вход через WARP MASQUE-туннель.
- Внешний IP = Cloudflare edge (не локальный), выход FRA/DE.
- Подтверждено: QUIC dial → Extended CONNECT `cf-connect-ip` → CONNECT-IP tunnel →
  gVisor stackDevice → трафик приложения.
- DNS-резолв через туннель-конфиг работает; переиспользование туннеля на повторных
  запросах работает; соединения закрываются чисто (`connection upload/download finished`,
  без goroutine-leak в логе).

### ✅ h2 (CONNECT-IP over HTTP/2) — РАБОТАЕТ

```
$ curl -s -x socks5h://127.0.0.1:12346 https://www.cloudflare.com/cdn-cgi/trace
warp=on
ip=104.28.163.86
colo=FRA
http=http/2
```

- `warp=on` через HTTP/2-туннель; переиспользование туннеля на второй хост работает;
  ошибок PROTOCOL_ERROR/reset в сессии нет.

**Путь к рабочему h2 (две итерации отладки на живом сервере):**

1. Первая попытка — stdlib `http.Client.Do(CONNECT)` → **400**: classic tunnel-CONNECT
   семантика, не то, что ждёт WARP.
2. Вторая — extended CONNECT через `x/net/http2.ClientConn.RoundTrip` (с `:protocol`) →
   **`extended connect not supported by peer`**: WARP не шлёт SETTINGS
   `ENABLE_CONNECT_PROTOCOL`, и библиотека блокирует запрос превентивно (тот же RFC-обход,
   что WARP делает на h3). Риск №1 подтверждён на живом сервере.
3. Финал — **ручной h2-фреймер** на публичном `x/net/http2.Framer` + `hpack` (обе уже в
   зависимостях): свой preface + SETTINGS + WINDOW_UPDATE, HEADERS, DATA-фреймы для capsule.
   Это обходит peer-settings gate. И ещё одна поправка после `stream reset: PROTOCOL_ERROR`:
   WARP h2 — это **plain CONNECT** (`:method` + `:authority`) + заголовок `cf-connect-proto`,
   а НЕ extended CONNECT с `:protocol`/`:scheme`/`:path`. С plain CONNECT — `warp=on`.

**Итог по риску №1:** снят без http-форка и без вкапывания x/net/http2 — хватило публичного
Framer/hpack (~250 строк в client_h2.go). Новых внешних зависимостей нет.

---

## Итог

**Главная цель — подключение к Cloudflare WARP по MASQUE — достигнута на обоих транспортах
(h3 и h2), device-verified.** Все юнит-тесты зелёные (включая h2 capsule-reassembly через
границы DATA-фреймов); риск №3 (формат ключей) снят — реальные WARP-ключи парсятся без
изменений; риск №1 (h2) снят ручным фреймером.
