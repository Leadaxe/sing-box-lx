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

### ⚠️ h2 (CONNECT-IP over HTTP/2) — НЕ РАБОТАЕТ (WARP отвечает 400)

```
ERROR connection: open connection ... using outbound/masque[warp]:
  masque: dial connect-ip over HTTP/2: connect-ip: server responded with 400
```

**Причина (диагноз):** stdlib `net/http` для `Method=CONNECT` использует классическую
tunnel-семантику (CONNECT к `req.Host` как к target-хосту), а WARP h2 CONNECT-IP ждёт
extended-CONNECT-подобный запрос с особым `:authority` (mihomo шлёт `:0` через
`metacubex/http` fork + `NewClientConn`/`Reserve`) и заголовком `cf-connect-proto`.
Наш обычный `client.Do(CONNECT)` формирует authority = `cloudflareaccess.com:443` и
origin-form target — WARP это отвергает 400.

Это ровно **риск №1 из SPEC**, который проявился только на живом сервере: stdlib h2
семантически несовместим с WARP CONNECT-IP. Первичная гипотеза «h2 без http-форка»
ОПРОВЕРГНУТА живым тестом.

**Варианты фикса (фаза 2):**
- (а) вкопать нужную часть http-форка (`NewClientConn`/`Reserve` + h2c с masked tls.Conn),
- (б) ручной h2-framing поверх tls.Conn (SETTINGS/HEADERS/DATA сами),
- (в) оставить h2 как not-yet-supported, отдавать понятную ошибку при `network: h2`.

До реализации фикса `network: h2` фактически нерабочий на WARP — не заявлять как готовый.

---

## Итог

Главная цель — **подключение к Cloudflare WARP по MASQUE — достигнута на h3.** h2 требует
доработки транспорта (фаза 2). Все юнит-тесты зелёные; риск №3 (формат ключей) снят —
реальные WARP-ключи парсятся без изменений.
