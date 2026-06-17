# EXAMPLES — WireSock-style маскировка `id` / `ip` / `ib`

Практический how-to по полям маскировки фичи 009. Это сахар над AmneziaWG `i1`:
вместо ручной CPS-строки `i1=<b 0x...>` пишешь домен/протокол/браузер, а движок
сам собирает пакет-приманку нужного протокола и шлёт его как `i1` перед handshake.

> Требуется сборка с `with_awg`. Без тега любой `id`/`ip`/`ib` отвергается:
> `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg`.

---

## 1. Три поля

> **§146 (2026-06-17):** `ip=quic` теперь эмитит **out-of-order фрагментированный
> QUIC Initial** (RFC 9001) с реалистичным ClientHello, где `id` идёт как **SNI** —
> это сменило прежний 1-RTT short header. Поэтому `id` стал **обязателен** для
> `quic` (как и для `dns`/`sip`), и домен теперь **виден** цензору в ClientHello.
> Детали ниже уже отражают это поведение. См. квик-секцию §2.1 и LxBox-таск §146.

| Поле | Имя | Значения | Обязательно |
|------|-----|----------|-------------|
| `id` | домен | LDH-хост (`www.google.com`, `ozon.ru`, `_dmarc.example.com`) | **для `quic`/`dns`/`sip`** (там он идёт в пакет: SNI / QNAME / SIP-host); опционален **только для `stun`** |
| `ip` | протокол | `quic` \| `dns` \| `stun` \| `sip` | да |
| `ib` | браузер | `chrome` \| `firefox` \| `curl` | нет (только при `ip=quic`) |

Минимум: `ip` всегда; плюс `id` — для `quic`/`dns`/`sip`. Только для `stun` хватает
одного `ip` (`id`/`ib` опциональны). Если `id` задан для `stun` — он валидируется
(LDH), но в пакет не идёт.

### Куда `id` реально попадает на провод

| `ip` | пакет-приманка | `id` виден цензору? |
|------|----------------|---------------------|
| `dns` | EDNS OPT response, `id` = **QNAME** | **да**, открытым текстом |
| `sip` | SIP `200 OK`, `id` = **host** в Via/From/To/Call-ID | **да**, открытым текстом |
| `quic` | фрагментированный QUIC Initial, `id` = **SNI** в ClientHello | **да** — цензор выводит ключи из DCID и читает SNI (если соберёт фрагменты по порядку) |
| `stun` | STUN Binding Success Response | **нет** — в STUN нет поля под домен |

**Вывод:** `ip=dns`/`ip=sip`/`ip=quic` несут домен на провод (`id` как QNAME /
SIP-host / SNI соответственно). `stun` даёт приманку без домена (маскировка по
*форме* протокола, а не по имени хоста).

---

## 2. Примеры по профилям

Во всех примерах опущены `log`/`outbounds`/`route` — оставлены только поля
endpoint'а. Подставь свои `private_key` / `peers` / `address`.

### 2.1 QUIC (под Cloudflare WARP)

```jsonc
{
  "type": "wireguard",
  "tag": "warp",
  "mtu": 1280,
  "address": ["172.16.0.2/32", "2606:4700:110:8000::2/128"],
  "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "www.google.com",
  "ip": "quic",
  "ib": "chrome",
  "peers": [
    {
      "address": "engage.cloudflareclient.com",
      "port": 2408,
      "public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
      "allowed_ips": ["0.0.0.0/0", "::/0"],
      "persistent_keepalive_interval": 25
    }
  ]
}
```

Генерирует **out-of-order фрагментированный QUIC Initial** (RFC 9001): реалистичный
ClientHello, где `id` (`www.google.com`) — это **SNI**, разбитый на CRYPTO-фреймы,
которые идут на провод **не по порядку** — первый CRYPTO-фрейм имеет offset≠0, а
offset-0 фрейм лежит ближе к концу, с интерливом PING/PADDING. Line-rate DPI хватает
первый фрейм, считает его offset 0, парсит мусор и пропускает (fail-open). `ib`
выбирает профиль ClientHello (порядок расширений / GREASE) под соответствующий
браузер. Подробная спецификация — LxBox-таск §146
(`146-warp-quic-initial-fragmented-i1.md`), генератор — `quic_initial_awg.go`,
`quic_clienthello_awg.go`, `quic_crypto_awg.go`.

> **§146 amendment (2026-06-17):** прежний дизайн эмитил 1-RTT short header
> (`<b 0x60><r 40>` и т.п. для chrome/firefox/curl, файл `masque_quic_awg.go`,
> функции `masqueQUICShortHeaderCPS` / `quicFirstByte`). Тот short header был
> **эмпирически заблокирован реальным LTE-DPI**, поэтому заменён на
> фрагментированный Initial выше; `masque_quic_awg.go` удалён. Описание
> short-header ниже более не отражает текущее поведение — оставлено как
> исторический контекст. **`id` для `quic` теперь обязателен** (становится SNI).

`id` для `quic` теперь **обязателен** (он становится SNI в ClientHello). Минимальный
вариант — `"id": "<домен>", "ip": "quic"` (плюс опциональный `"ib"`):

```jsonc
{ /* ...endpoint... */ "jc": 4, "jmin": 40, "jmax": 70, "id": "www.google.com", "ip": "quic", "ib": "chrome" }
```

### 2.2 DNS (домен виден на проводе)

```jsonc
{
  "type": "wireguard",
  "tag": "awg-dns",
  "mtu": 1280,
  "address": ["10.0.0.2/32"],
  "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "www.google.com",
  "ip": "dns",
  "peers": [
    { "address": "192.0.2.1", "port": 51820,
      "public_key": "<server-public-key-base64>",
      "allowed_ips": ["0.0.0.0/0"], "persistent_keepalive_interval": 25 }
  ]
}
```

Генерирует EDNS OPT *response* (87 байт): DNS-заголовок (QR=1, RD=1, RA=1, NOERROR),
вопрос с QNAME = `www.google.com`, OPT RR (TYPE 41, UDP-size 1232), и случайные
cover-байты как opaque-данные неизвестной EDNS-опции `0xFDE9`. Парсится как один
валидный DNS-ответ. **`id` тут — QNAME, виден цензору.**

### 2.3 STUN

```jsonc
{
  "type": "wireguard", "tag": "awg-stun", "mtu": 1280,
  "address": ["10.0.0.2/32"], "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "stun.l.google.com",
  "ip": "stun",
  "peers": [
    { "address": "192.0.2.1", "port": 51820,
      "public_key": "<server-public-key-base64>",
      "allowed_ips": ["0.0.0.0/0"], "persistent_keepalive_interval": 25 }
  ]
}
```

Генерирует STUN Binding Success Response (52 байта): type `0x0101`, magic cookie
`0x2112A442`, случайный transaction ID, XOR-MAPPED-ADDRESS + SOFTWARE. **`id`
валидируется, но в STUN-пакет не попадает** (поля под домен нет).

### 2.4 SIP (домен виден на проводе)

```jsonc
{
  "type": "wireguard", "tag": "awg-sip", "mtu": 1280,
  "address": ["10.0.0.2/32"], "private_key": "<client-private-key-base64>",
  "jc": 4, "jmin": 40, "jmax": 70,
  "id": "pbx.example.com",
  "ip": "sip",
  "peers": [
    { "address": "192.0.2.1", "port": 51820,
      "public_key": "<server-public-key-base64>",
      "allowed_ips": ["0.0.0.0/0"], "persistent_keepalive_interval": 25 }
  ]
}
```

Генерирует SIP `200 OK` response (280 байт): status-line + Via/From/To/Call-ID/CSeq
(хост = `pbx.example.com`) + `Content-Length: 0`. **`id` — host в Via/From/To,
виден цензору.**

---

## 3. Что выбрать

- **Коннект к WARP под реальным DPI** → `ip=quic`, `id=<популярный домен>`,
  `ib=chrome`. Фрагментированный QUIC Initial с `id` как SNI (§146); device-proven
  против реального LTE-DPI, где прежний short-header был заблокирован.
- **Нужно, чтобы DPI увидел «разрешённый» домен** → `ip=quic`/`ip=dns`/`ip=sip` с
  региональным популярным `id` (SNI / QNAME / SIP-host).
- **STUN** — нишево (выглядит как ответ STUN-сервера); домен не несёт.

---

## 4. Проверка

```sh
# сборка со всеми нужными тегами
go build -tags "with_wireguard with_gvisor with_awg" -o ./sing-box ./cmd/sing-box

# проверка конфига
./sing-box check -c config.json      # пусто = ок
```

Соответствие `awg.conf` (WireSock/awg-quick): поля `id`/`ip`/`ib` — это аналог
`I1` из `[Interface]`, только в декларативной форме. Нельзя задавать `i1` и
`id`/`ip`/`ib` одновременно.

---

## 5. Частые ошибки (дословные сообщения)

| Конфиг | Ошибка |
|--------|--------|
| `i1` **и** `id`/`ip`/`ib` вместе | `amneziawg: id/ip/ib masquerade conflicts with an explicit i1; use one or the other` |
| `id` есть, `ip` нет | `amneziawg: ip (masquerade protocol) is required when id/ib is set; one of quic\|dns\|stun\|sip` |
| `ip` не из набора | `amneziawg: unknown masquerade protocol "ftp"; one of quic\|dns\|stun\|sip` |
| `ip=quic` без `id` | `amneziawg: id (masquerade domain) is required for ip=quic (it becomes the ClientHello SNI)` |
| `ip=dns` без `id` | `amneziawg: id (masquerade domain) is required for ip=dns (it becomes the DNS QNAME)` |
| `ip=sip` без `id` | `amneziawg: id (masquerade domain) is required for ip=sip (it becomes the SIP host)` |
| `ip=stun` без `id` | **не ошибка** — `id` опционален, decoy без домена |
| домен с `\r\n`/`;`/`@`/пробелом | `amneziawg: invalid masquerade domain "...": illegal character (only a-z A-Z 0-9 - _ allowed)` |
| `ib` не из набора | `amneziawg: unknown masquerade browser "safari"; one of chrome\|firefox\|curl` |
| `ib` с `ip≠quic` | `amneziawg: ib (browser) is only meaningful with ip=quic, got ip="dns"` |
| без `with_awg` | `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg` |

Домен проходит **строгую LDH-валидацию** (как SNI у WireSock): метки из
`a-z A-Z 0-9 - _`, ≤63 байта, без дефиса по краям; всё имя ≤253, без точки в начале;
одна точка в конце допускается. Это security-граница — домен идёт в текст SIP и в
DNS QNAME, поэтому control-байты и метасимволы отвергаются (защита от инъекции).

---

## 6. Ограничения

- Это **decoy перед handshake**, не полноценная протокол-сессия. Один пакет нужной
  формы, дальше — обычный AWG-трафик.
- **QUIC раскрывает SNI** (фрагментированный Initial, §146): `id` идёт на провод как
  SNI в ClientHello. `stun` домен не несёт (см. §1). DPI-обход — за счёт out-of-order
  фрагментации CRYPTO-фреймов (line-rate DPI парсит первый фрейм как offset 0, видит
  мусор и пропускает), не за счёт сокрытия SNI.
- **`ib` выбирает профиль ClientHello** (порядок расширений / GREASE под браузер),
  но **не претендует на полную JA3/JA4-эквивалентность** конкретной сборке браузера.
  В прежнем short-header дизайне `ib` не делал ничего значимого (косметические биты
  первого байта) — §146 дал ему реальный, хоть и приближённый, эффект.
- Байт-в-байт replay именно WireSock-трафика **не** делается: энтропия наша
  (криптослучайная `<r>`), а не payload-seeded PRNG (у нас нет ciphertext-хвоста,
  т.к. это standalone I1, а не серверный S1–S4 padding).
- Полевая проверка: `ip=quic` (фрагментированный Initial, §146) **device-proven
  против реального LTE-DPI** — прежний short-header там был эмпирически заблокирован.
  Для `dns`/`stun`/`sip` систематическая полевая A/B-проверка против конкретного DPI
  не проводилась — подтверждены приём движком, структурная валидность и `sing-box check`.

См. также: [SPEC.md](SPEC.md), [PLAN.md](PLAN.md),
[IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md),
краткая версия — `docs/lx-config.md` (секция «Masquerade id/ip/ib»).
