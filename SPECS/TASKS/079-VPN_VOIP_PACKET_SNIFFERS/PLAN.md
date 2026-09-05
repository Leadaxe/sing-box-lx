# PLAN: 079 — VPN_VOIP_PACKET_SNIFFERS

## Файлы

| Файл | Содержимое |
|------|-----------|
| `common/sniff/openvpn_lx.go` | `OpenVPN` (packet) + `OpenVPNStream` (TCP, 2-байтная длина → та же проверка) |
| `common/sniff/ike_lx.go` | `IKE` (packet): ISAKMP-заголовок, non-ESP маркер на 4500 |
| `common/sniff/tailscale_lx.go` | `TailscaleDisco` (packet): магия + минимальная длина |
| `common/sniff/sip_lx.go` | `SIP` (packet) + `SIPStream` (TCP): request-line, `Domain` из Request-URI |
| `common/sniff/*_lx_test.go` | векторы + перекрёстный тест с апстримными |
| `constant/protocol.go` | `ProtocolOpenVPN`, `ProtocolIKE`, `ProtocolTailscale`, `ProtocolSIP` (в блок `sniff-wireguard` → переименовать маркер в `sniff-lx`) |
| `route/route.go` | packet-дефолты после `WireGuard`; stream-дефолты после `RDP` |
| `route/rule/rule_action.go` | четыре `case` |
| `docs/configuration/route/sniff{,.zh}.md` | строки таблицы |
| `docs-lx/lx-sniff.md`, `.ru.md` | общая страница (R6) |

## Проверки формы

**OpenVPN (UDP).** `b0 & 0x07 == 0` (key_id 0) и `b0 >> 3 ∈ {7, 10}`:
- plain: `len == 14`, `b[9] == 0` (ack count), `b[10:14] == 0` (packet-id);
- tls-crypt / tls-crypt-v2: `len >= 49`, `be32(b[9:13]) == 1`, `be32(b[13:17])`
  в окне [2010-01-01, now+1d];
- tls-auth: для H ∈ {16, 20, 28, 32, 48, 64}: `len == 22+H`, `be32(b[9+H:]) == 1`,
  `b[17+H] == 0`, `b[18+H:22+H] == 0`.
TCP: `be16(b[0:2]) == len-2`, дальше то же по `b[2:]`; для stream — `Peek(2)`,
затем `Peek(2+n)`; недобор → `ErrNeedMoreData`.

**IKE (UDP).** Если `b[0:4] == 0` и `len >= 32` → сдвиг 4 (non-ESP на 4500).
Заголовок 28 б: SPIi ≠ 0, SPIr == 0, `version == 0x20 && exch == 34 && flags == 0x08`
либо `version == 0x10 && exch ∈ {2, 4} && flags == 0`, `msgid == 0`,
`be32(length) == len(остатка)`, next payload ≠ 0.

**Tailscale disco.** `b[0:6] == "TS\xf0\x9f\x92\xac"`, `len >= 78`.

**SIP.** Первая строка до `\r\n` (лимит 1 КБ): три токена, третий `SIP/2.0`,
первый ∈ методов, второй начинается с `sip:`/`sips:`/`tel:`. `Domain` = host
из `sip[s]:[user@]host[:port][;params]`, только для `sip`/`sips`, LDH-фильтр.
Stream — тот же разбор через `bufio.Peek`, недобор → `ErrNeedMoreData`.

## Порядок и коллизии

- `OpenVPN` `0x38/0x50`: uTP отвергает версии 8/0 → пересечений нет; DTLS `0x16`, QUIC `≥0x80`.
- `IKE` начинается со случайного SPI: единственная защита — SPIr == 0 + version + length; этого достаточно.
- `SIP` текст: `DomainNameQuery` должен отвергнуть (в тесте); stream — после `HTTPHost`
  (`http.ReadRequest` не принимает `SIP/2.0`, проверяется тестом).
- `TailscaleDisco` — магия, коллизий нет.

## Риски

- tls-auth с нестандартной длиной HMAC (напр. `auth none` или whirlpool) не распознается — принимаем.
- OpenVPN за `tls-crypt-v2` при `--tls-crypt-v2-verify` не меняет первый пакет — ок.
- SIP over TLS (5061) — это `tls`, не наш случай.
