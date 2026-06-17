# EXAMPLES — WireSock-style маскировка `id` / `ip` / `ib`

Практический how-to по полям маскировки фичи 009. Это сахар над AmneziaWG `i1`:
вместо ручной CPS-строки `i1=<b 0x...>` пишешь домен/протокол/браузер, а движок
сам собирает пакет-приманку нужного протокола и шлёт его как `i1` перед handshake.

> Требуется сборка с `with_awg`. Без тега любой `id`/`ip`/`ib` отвергается:
> `AmneziaWG (awg) support is not included in this build, rebuild with -tags with_awg`.

---

## 1. Три поля

| Поле | Имя | Значения | Обязательно |
|------|-----|----------|-------------|
| `id` | домен | LDH-хост (`www.google.com`, `ozon.ru`, `_dmarc.example.com`) | **для `quic`/`dns`** (идёт в пакет: SNI / QNAME); опционален для `sip` (host или псевдо-host) и `stun` (игнорируется) |
| `ip` | протокол | `quic` \| `dns` \| `stun` \| `sip` | да |
| `ib` | браузер | `chrome` \| `firefox` \| `curl` | нет (только при `ip=quic`) |

Минимум: `ip` всегда; плюс `id` — для `quic`/`dns`. Для `sip`/`stun` хватает одного `ip`
(`id` опционален: для `sip` без него генерируется псевдо-host, для `stun` он не идёт в пакет).
Если `id` задан — он всегда валидируется (LDH).

### Куда `id` реально попадает на провод

| `ip` | пакет-приманка | `id` виден цензору? |
|------|----------------|---------------------|
| `dns` | EDNS OPT query (QR=0), `id` = **QNAME** | **да**, открытым текстом |
| `sip` | SIP INVITE request, `id` = **host** в URI (или псевдо-host) | **да** (если задан), открытым текстом |
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
валидируется (`chrome|firefox|curl`), но на байты пакета не влияет — bypass держится
на фрагментации, а не на TLS-fingerprint (JA3 не имитируется). Генератор —
`quic_initial_awg.go`, `quic_clienthello_awg.go`, `quic_crypto_awg.go`.

`id` для `quic` **обязателен** (он становится SNI в ClientHello). Минимальный
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

Генерирует клиентский DNS **query** (~87 байт): DNS-заголовок (QR=0, RD=1), вопрос с
QNAME = `www.google.com` и QTYPE HTTPS (тип 65), OPT RR (TYPE 41, UDP-size 1232) и
случайные cover-байты как opaque-данные неизвестной EDNS-опции `0xFDE9`. Парсится как один
валидный DNS-запрос. **`id` тут — QNAME, виден цензору.**

> **Не подтверждён на WARP-DPI.** На тестовом LTE/WARP DPI `ip=dns` — Timeout (как `stun`):
> DPI режет DNS/STUN к WARP-edge `:2408` как класс протокола (raw DNS живёт на :53, а не на
> дата-центровом IP). Для WARP используй `ip=quic`. `ip=dns` оставлен для других провайдеров,
> чей DPI проверяет только корректность пакета.

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

Генерирует SIP **INVITE** request (~580 байт): request-line `INVITE sip:<user>@pbx.example.com
SIP/2.0`, Via(branch)/Max-Forwards:70/From(tag)/To/Call-ID/CSeq:N INVITE/Contact, и SDP-офер
(`m=audio`, rtpmap). Имена пользователей — произносимые псевдо-строки (не захардкожены).
**`id` — host в URI, виден цензору.** `id` для sip **опционален**: без него генерируется
правдоподобный псевдо-host.

> **Не подтверждён на WARP-DPI.** Ожидаемо Timeout (как `dns`/`stun`): SIP к WARP-edge `:2408` —
> аномалия назначения (SIP живёт на SIP-сервере/`:5060`). INVITE-форма исправляет аномалию
> направления старого `200 OK`, но назначение не лечит. Для WARP используй `ip=quic`; `ip=sip` —
> для других провайдеров, чей DPI проверяет только корректность пакета.

---

## 3. Что выбрать

- **Коннект к WARP под реальным DPI** → `ip=quic`, `id=<популярный домен>`,
  `ib=chrome`. Фрагментированный QUIC Initial с `id` как SNI; device-proven против
  реального LTE-DPI.
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
| `ip=sip` без `id` | **не ошибка** — `id` опционален, генерируется псевдо-host |
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
- **QUIC раскрывает SNI** (фрагментированный Initial): `id` идёт на провод как
  SNI в ClientHello. `stun` домен не несёт (см. §1). DPI-обход — за счёт out-of-order
  фрагментации CRYPTO-фреймов (line-rate DPI парсит первый фрейм как offset 0, видит
  мусор и пропускает), не за счёт сокрытия SNI.
- **`ib` валидируется, но на байты пакета не влияет.** JA3/JA4-fingerprint не
  имитируется: DPI-обход держится на out-of-order фрагментации CRYPTO-фреймов, а не на
  TLS-fingerprint. `ib` принимается для совместимости синтаксиса с WireSock-конфигами.
- Байт-в-байт replay именно WireSock-трафика **не** делается: энтропия наша
  (криптослучайная `<r>`, для QUIC — свежие DCID/random/x25519 на вызов), а не
  payload-seeded PRNG (это standalone I1, а не серверный S1–S4 padding).
- Полевая проверка: `ip=quic` (фрагментированный Initial) **device-proven против
  реального LTE-DPI**. Для `dns`/`stun`/`sip` систематическая полевая A/B-проверка
  против конкретного DPI не проводилась — подтверждены приём движком, структурная
  валидность и `sing-box check`.

См. также: [SPEC.md](SPEC.md),
[IMPLEMENTATION_REPORT.md](IMPLEMENTATION_REPORT.md),
краткая версия — `docs/lx-config.md` (секция «Masquerade id/ip/ib»).
