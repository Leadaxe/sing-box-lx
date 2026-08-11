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

---

## RESULTS — аудит-проход (idle/reconnect/оптимизации), 2026-07-02

Многоагентный аудит (24 подтверждённых находки) → исправления по волнам. Стенд: `idle_timeout: "10s"`.

**Lifecycle device-verified на живом WARP (h3 и h2):**

| Проверка | h3 | h2 |
|---|---|---|
| dial (warp=on) | ✅ edge IP, warp=on | ✅ warp=on, http/2 |
| idle-suspend | ✅ `idle-suspend tunnel after 12.24s` | ✅ `after 12.27s` |
| reconnect после suspend | ✅ warp=on (новый edge) | ✅ warp=on |
| no-flap (повторный suspend) | ✅ 2 цикла подряд | ✅ |
| ошибки в логе (PROTOCOL_ERROR / masque:) | нет | нет |

**Исправленные баги корректности:**
- **C1 reconnect** (был HIGH): упавший/suspended туннель не пересоздавался (`running` залипал).
  Переписан на `*session` + generation-guard; `o.sess=nil` при teardown → следующий dial поднимает заново.
- **A1 blackhole**: битый/пустой inbound datagram (unparseable varint) рвал весь туннель — теперь drop-and-continue.
- **C2 leak**: `ipConn.Close()` в teardown разблокирует парный насос, залипший в блокирующем read.
- **A3 ICMP**: снимок заголовка ДО мутации TTL/checksum.
- **A4/A5/B5 h2**: `maxCapsulePayload` (65535) против peer-controlled `payloadLen` (overflow/OOM);
  fail-fast `mtu ≤ 16000` на h2; recvCh 64→8; окно 1 GiB → 8 MiB (реальный backpressure).

**Оптимизации idle/ресурсов:**
- **B1 idle-suspend**: после `idle_timeout` (деф. 5m) сносится netstack + 4-6 горутин + keepalive; поднимается по требованию.
- **B3/B2 аллокации**: reusable scratch для исходящего datagram (h3 + h2) вместо make-на-пакет
  (безопасно — quic-go/http2 копируют срез до возврата, писатель — единственная tx-горутина).
- **B4**: `idle_timeout` / `keep_alive_period` вынесены в конфиг.

**Документация (группа D):** убран фантомный `cwnd` (ломал strict-decode), мёртвый `congestion_control`;
предупреждение про `network`=транспорт vs `network_list`=L4; актуализированы секции «Файлы»/«Поток данных»/
«Риски»; добавлен package-doc.

**Верификация:** `go build` (with_quic+with_gvisor+with_wireguard и stub), `go vet`, `gofmt -l`, `go test -race`
— всё зелёно. Юнит-тесты жизненного цикла (generation-guard, идемпотентный teardown, close-guard) под -race.

---

## Ф4 — TLS-слой h2 на общий `common/tls` (план + baseline)

> Baseline ниже снят до реализации и служит также точкой отсчёта для
> [SPEC 060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) (авто-фрагментация при detour).

### Baseline (снят 2026-08-12, до реализации)

Стенд: локальный `sing-box run` с раздельными mixed-инбаундами (по одному на плечо),
нижние плечи с `bind_interface: en0` в обход боевого TUN. Endpoint — реальный WARP
`162.159.199.118:8443`, `network: h2`, `sni: www.google.com` (конфиг из LxBox-генератора).

| плечо | результат |
|---|---|
| VLESS сам по себе | ✅ `ip=66.234.150.226` |
| masque h2 напрямую (en0) | ✅ `warp=on` |
| masque h2 **через detour(VLESS-FI)** | ❌ `tls handshake: EOF` за 17 с |
| masque h3 через detour | ✅ `warp=on`, 4/4 |

Порог по размеру ClientHello через сломанное плечо: 1488 B — OK, 1502 B и выше — виснет.
На узлах GB/FR те же размеры проходят. Голым `curl`/Go-клиентом воспроизводится без ядра
⇒ причина вне sing-box (PMTU black hole за detour-сервером).

Фрагментация штатным `tf.NewConn` через то же сломанное плечо:

| режим | результат |
|---|---|
| без фрагментации | ❌ FAIL, 12 с |
| `fragment` (packet-split) | ✅ OK, 0.6 с |
| `record_fragment` | ✅ **OK, 0.1 с** |
| оба | ✅ OK, 0.6 с |

Post-handshake: сквозная загрузка 5 МБ через `masque + detour` (здоровое GB-плечо) —
`http=200`, 1.0 с. Записи туннеля ≈1300 B (по одному IP-пакету на `WriteData`),
порога не достигают ⇒ фрагментация нужна только на хендшейке.

### Что проверить ПОСЛЕ реализации Ф4

1. **Регресс прямого пути**: masque h2 и h3 БЕЗ detour — `warp=on`, как в baseline.
2. **Фрагментация доступна и работает**: masque h2 + `detour` на заведомо сломанный узел
   (FI/SE) с `record_fragment: true` — туннель поднимается, `warp=on`, хендшейк < 1 с.
   Без флага — по-прежнему падает (это ожидаемо до SPEC 060).
3. **Pinning не сломан**: подмена `public_key` → внятная ошибка pinning, а не успешный
   коннект; `skip_cert_verify: true` по-прежнему отключает pinning.
4. **Наследство общего слоя**: `sni` пустой/непустой, `skip_cert_verify`;
   h3-путь не задет (он на своём `quic-go` TLS).
5. **Сквозной трафик**: 5 МБ через detour-туннель, сравнить со строкой baseline.
6. **`go test -race`** по `protocol/masque` + `transport/masque`, `gofmt -l`, `go vet`.

> Замеры на живых узлах воспроизводимы только при наличии сломанного плеча: PMTU-дыра —
> свойство конкретного хостера. На момент baseline ломались FI/SE, работали GB/FR.

### RESULTS — Ф4 (2026-08-12)

Живой Cloudflare WARP `162.159.199.118:8443`, `network: h2`, `sni: www.google.com`.
Нижнее плечо — VLESS-узел FI с подтверждённой PMTU-дырой (порог ≈1490 B).

| сценарий | было (baseline) | стало |
|---|---|---|
| h2 напрямую (регресс) | ✅ `warp=on` | ✅ `warp=on`, 0.4 с |
| h2 + detour, без флагов | ❌ EOF 17 с | ❌ FAIL 15.2 с — **ожидаемо**: дефолт не меняли (SPEC 060) |
| h2 + detour + `record_fragment` | — (поля не было) | ✅ **`warp=on`, 0.3 с** |
| 5 МБ через detour + `record_fragment` | — | ✅ `http=200`, 0.97 с (= здоровое плечо) |
| pinning на подменённый `public_key` | — | ✅ `masque: remote endpoint has a different public key than pinned` |

Ключевое: перевод на общий TLS-слой **не ослабил pinning** — проверено и юнит-тестом
(`h2_tls_client_lx_test.go`), и живым коннектом с валидным, но чужим P-256 ключом.

Сборка/статика: `make -f Makefile.lx lx-build`, сборка без `with_quic` (stub-путь),
`go vet`, `gofmt -l` — чисто. `go test -race` по `protocol/masque`, `transport/masque`,
`transport/masque/connectip`, `option` — зелено.
