# SPEC 021 — MASQUE outbound: справочник конфигурации

Полный шаблон и таблица всех параметров MASQUE-outbound (`type: masque`).
Сверено с кодом на ветке `lx-spec021-masque` (`option/masque.go`, `protocol/masque/outbound.go`,
`transport/masque/profile.go`). Формат — sing-box JSON (НЕ Clash YAML).

---

## Полный шаблон (все параметры)

```jsonc
{
  "type": "masque",
  "tag": "warp",

  // ── сервер (ServerOptions) ──────────────────────────────────────────────
  "server": "162.159.198.2",      // IP или хост WARP endpoint (обязательно)
  "server_port": 443,             // порт (обязательно)

  // ── профиль и транспорт ─────────────────────────────────────────────────
  "profile": "cloudflare",        // "cloudflare" (деф) | "standard"
  "network": "h3",                // ТРАНСПОРТ: "h3" (деф) | "h2"  ⚠️ НЕ tcp/udp

  // ── ключевой материал (обязателен для profile=cloudflare) ───────────────
  "private_key": "<base64 DER>",  // x509 EC private key
  "public_key":  "<base64 DER>",  // PKIX public key endpoint'а
  "ip":   "172.16.0.2",           // локальный IPv4 внутри туннеля
  "ipv6": "2606:4700:110:...:c7a6", // локальный IPv6 (нужен хотя бы один из ip/ipv6)

  // ── TLS / маскировка ────────────────────────────────────────────────────
  "sni": "4pda.to",               // TLS ServerName; деф — по профилю
  "uri": "https://cloudflareaccess.com", // CONNECT-IP URI; деф — по профилю
  "skip_cert_verify": false,      // отключить pubkey-pinning (ТОЛЬКО debug)

  // ── туннель / ресурсы ───────────────────────────────────────────────────
  "mtu": 1280,                    // MTU userspace-стека; деф 1280 (h2: ≤ 16000)
  "idle_timeout": "5m",           // suspend после простоя; деф 5m; отриц. = выкл
  "keep_alive_period": "30s",     // QUIC keepalive (h3); деф 30s; отриц. = выкл
  "network_list": ["tcp", "udp"], // L4-протоколы через туннель; деф — оба

  // ── общие DialerOptions (как у любого outbound; все опциональны) ─────────
  "detour": "",                   // цепочка через другой outbound
  "bind_interface": "",           // привязка к интерфейсу
  "connect_timeout": "",          // таймаут установки соединения
  "domain_resolver": "",          // резолвер для server, если он домен
  "domain_strategy": ""           // prefer_ipv4 / prefer_ipv6 / ipv4_only / ipv6_only
  // (полный список DialerOptions — общий для всех outbound sing-box)
}
```

Минимальный рабочий конфиг (только обязательное, остальное — дефолты):

```jsonc
{
  "type": "masque",
  "tag": "warp",
  "server": "162.159.198.2",
  "server_port": 443,
  "private_key": "<base64 DER>",
  "public_key":  "<base64 DER>",
  "ip":   "172.16.0.2",
  "ipv6": "2606:4700:110:...:c7a6"
}
```
(profile=cloudflare, network=h3, sni=`consumer-masque.cloudflareclient.com`, uri=`https://cloudflareaccess.com`,
mtu=1280, idle_timeout=5m, keep_alive_period=30s, network_list=tcp+udp — всё по умолчанию.)

> ⚠️ Нужен блок верхнего уровня `"dns"` (например `{"servers":[{"type":"udp","server":"1.1.1.1"}]}`):
> userspace-стек работает на L3 и сам домены не резолвит — outbound резолвит их через DNS перед dial.

---

## Таблица параметров

### Специфичные для MASQUE

| Поле | Тип | Обязат. | Дефолт | Значения / смысл |
|---|---|---|---|---|
| `type` | string | ✅ | — | всегда `"masque"` |
| `tag` | string | ✅ | — | имя outbound (для route/групп) |
| `server` | string | ✅ | — | IP/хост WARP endpoint |
| `server_port` | uint16 | ✅ | — | порт (обычно 443) |
| `profile` | string | — | `cloudflare` | `cloudflare` \| `standard`. Набор квирков (см. ниже) |
| `network` | string | — | `h3` | **транспорт**: `h3` (QUIC) \| `h2` (HTTP/2). ⚠️ НЕ tcp/udp |
| `private_key` | string (base64) | ✅¹ | — | EC private key, DER → `x509.ParseECPrivateKey` |
| `public_key` | string (base64) | ✅¹ | — | endpoint PKIX pubkey, DER → `x509.ParsePKIXPublicKey` (ECDSA) |
| `ip` | string | ✅² | — | локальный IPv4 туннеля; без маски → `/32` |
| `ipv6` | string | ✅² | — | локальный IPv6 туннеля; без маски → `/128` |
| `sni` | string | — | по профилю³ | TLS ServerName (для WARP ≠ endpoint host — маскировка) |
| `uri` | string | — | по профилю³ | CONNECT-IP URI-шаблон |
| `skip_cert_verify` | bool | — | `false` | `true` = отключить pubkey-pinning (небезопасно, только отладка) |
| `mtu` | uint32 | — | `1280` | MTU userspace-стека; на `h2` максимум `16000` |
| `idle_timeout` | duration | — | `5m` | suspend туннеля после простоя; `""`=5m; **отрицательное = выкл** |
| `keep_alive_period` | duration | — | `30s` | QUIC keepalive (только h3); `""`=30s; **отрицательное = выкл** |
| `network_list` | list | — | `["tcp","udp"]` | L4-протоколы, проходящие через туннель |

¹ Обязательны при `profile=cloudflare`. При `profile=standard` — опциональны.
² Обязателен **хотя бы один** из `ip`/`ipv6`.
³ Дефолты по профилю — см. таблицу профилей ниже.

### Общие DialerOptions (наследуются, все опциональны)

Стандартный набор sing-box, применяется к исходящему dial к WARP endpoint:

| Поле | Тип | Смысл |
|---|---|---|
| `detour` | string | пустить соединение к endpoint через другой outbound |
| `bind_interface` | string | привязать к сетевому интерфейсу |
| `inet4_bind_address` / `inet6_bind_address` | addr | исходящий bind-адрес |
| `routing_mark` | int | fwmark (Linux) |
| `reuse_addr` | bool | SO_REUSEADDR |
| `connect_timeout` | duration | таймаут установки соединения |
| `tcp_fast_open` | bool | TFO (актуально для h2) |
| `domain_resolver` | object | какой DNS резолвит `server`, если он домен |
| `domain_strategy` | string | `prefer_ipv4` \| `prefer_ipv6` \| `ipv4_only` \| `ipv6_only` |
| `fallback_delay` | duration | задержка happy-eyeballs |
| `network_strategy`, `network_type`, `fallback_network_type` | | мульти-сетевые стратегии |

(Полный перечень — общий `DialerOptions` в `option/outbound.go`.)

---

## Профили — что переключает `profile`

Профиль трогает 4 точки; всё остальное (QUIC/HTTP2, capsule, стек, насосы) — общее.

| Аспект | `cloudflare` (деф) | `standard` (RFC 9484) |
|---|---|---|
| `:protocol` (h3) / cf-connect-proto (h2) | `cf-connect-ip` | `connect-ip` |
| Extended CONNECT settings | игнорировать отсутствие (WARP их не шлёт) | требовать |
| дефолт `sni` | `consumer-masque.cloudflareclient.com` | = `server` |
| дефолт `uri` | `https://cloudflareaccess.com` | нет (обязателен) |
| TLS-проверка | pinning на `public_key` (ECDSA) | обычная цепочка по `sni` |
| `private_key`/`public_key` | обязательны | опциональны |

`standard` — задел под собственный RFC-совместимый CONNECT-IP сервер; к Cloudflare WARP **не подключится**.
Для WARP всегда `cloudflare` (это и дефолт).

---

## Форматы значений

**Duration** (`idle_timeout`, `keep_alive_period`, `connect_timeout`, …):
строка Go-duration — `"30s"`, `"5m"`, `"1h30m"`. Пустая строка = дефолт. Отрицательная (`"-1s"`) = «выключить».

**Ключи** (`private_key` / `public_key`):
base64 от DER. `private_key` = `x509.MarshalECPrivateKey` (SEC1 EC), `public_key` = `x509.MarshalPKIXPublicKey`
от `*ecdsa.PublicKey` (P-256). Ровно тот формат, что отдаёт WARP-регистрация на Dart-стороне —
парсится ядром без преобразований.

**`ip` / `ipv6`**: адрес или CIDR. `"172.16.0.2"` → `172.16.0.2/32`, `"2606:...::"` → `/128`.
Это локальные адреса **внутри** туннеля (твой адрес в сети WARP), НЕ выходной IP.

---

## Валидация при старте (fail-fast)

Конфиг отклоняется при загрузке, если:
- `profile=cloudflare`, но нет/не парсится `private_key` или `public_key`;
- не задано ни `ip`, ни `ipv6` (или не парсится в CIDR);
- `network` ∉ {`h3`, `h2`} (например привычный `"tcp"` → ошибка «invalid network»);
- `network=h2` и `mtu > 16000`;
- `network=h2` и `profile=standard` (h2 для standard не реализован).

---

## Частые грабли

1. **`network` ≠ tcp/udp.** У masque `network` — это транспорт (h3/h2). Список tcp/udp — это `network_list`.
   `"network": "tcp"` даст fail-fast. У всех прочих outbound наоборот — здесь инверсия by design.
2. **Нужен блок `dns`** на верхнем уровне — иначе доменный трафик через туннель не резолвится.
3. **Выходной IP меняется** после idle-suspend/reconnect — WARP anycast раздаёт разные edge-адреса
   на каждое новое подключение. Внутренний `ip`/`ipv6` при этом стабилен.
4. **`skip_cert_verify: true`** снимает pubkey-pinning целиком — единственную защиту при WARP-маскировке
   SNI. Только для отладки.
5. **`keep_alive_period` vs `idle_timeout`**: при коротком `idle_timeout` туннель обычно сносится раньше,
   чем keepalive становится нужен. Не выключай keepalive (`-1s`) при большом `idle_timeout` — иначе сервер
   может разорвать туннель по своему idle раньше, чем сработает наш suspend.
