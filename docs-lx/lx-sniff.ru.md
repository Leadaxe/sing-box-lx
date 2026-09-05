# sing-box-lx — снифферы протоколов для трафика из LAN (SPEC 078 / 079)

> EN: [lx-sniff.md](lx-sniff.md) · Фича: [SNIFF](../SPECS/FEATURES/016-SNIFF/FEATURE.md)

Ядро сниффит первый пакет каждого соединения, вошедшего через `tun` (или любой
inbound с действием `sniff` в правилах), и вешает на него имя протокола, по
которому работают правила маршрутизации. Апстримный sing-box узнаёт web и торренты
(`http`, `tls`, `quic`, `stun`, `dns`, `bittorrent`, `dtls`, `ssh`, `rdp`, `ntp`).
Форк добавляет то, что домашний роутер реально видит от других устройств в LAN:
VPN-туннели и звонки.

## Что узнаёт форк

| Имя | Сеть | Ставит `domain` | Первый пакет клиента, который совпадает | Задача |
|-----|------|-----------------|------------------------------------------|--------|
| `wireguard` | UDP | — | handshake initiation / response / cookie по точному размеру (148 / 92 / 64 б); transport `>= 32 б` и `(len-32) % 16 == 0`. Резервные байты не проверяются, поэтому Cloudflare WARP тоже совпадает | [078](../SPECS/TASKS/078-WIREGUARD_PACKET_SNIFFER/SPEC.md) |
| `openvpn` | UDP, TCP | — | `P_CONTROL_HARD_RESET_CLIENT_V2/V3`, key id 0: plain (14 б), tls-auth (HMAC известной длины, replay id 1, правдоподобное net-time), tls-crypt / tls-crypt-v2 (replay id 1, net-time, 32-байтный HMAC). TCP: тот же кадр за 2-байтной длиной | [079](../SPECS/TASKS/079-VPN_VOIP_PACKET_SNIFFERS/SPEC.md) |
| `ike` | UDP | — | заголовок ISAKMP: SPI инициатора задан, SPI ответчика нулевой, message id 0, поле length равно датаграмме; IKEv2 `IKE_SA_INIT` с флагом Initiator либо IKEv1 Main / Aggressive. На UDP 4500 пропускается non-ESP маркер | 079 |
| `tailscale` | UDP | — | disco-пинг: магия `TS💬`, 32-байтный disco-ключ, 24-байтный nonce, box. Сам туннель — WireGuard и виден как `wireguard` | 079 |
| `sip` | UDP, TCP | host из Request-URI | request-line `METHOD sip:… SIP/2.0` (INVITE, REGISTER, OPTIONS, …). `sip:user@host` → `domain: host`; IP-литералы и `tel:` домен не ставят | 079 |

Все имена работают в обоих местах: в списке `sniffer` действия `sniff` и в
матчере `protocol` правила маршрутизации.

## Порядок по умолчанию

Пакетные (UDP) снифферы по порядку: `dns`, `quic`, `stun`, **`wireguard`, `openvpn`,
`ike`, `tailscale`, `sip`**, `bittorrent` (uTP, tracker), `dtls`, `ntp`.
Потоковые (TCP): `tls`, `http`, `dns`, `bittorrent`, `ssh`, `rdp`, **`sip`, `openvpn`**.

Снифферы форка стоят **перед** uTP намеренно. Апстримная проверка uTP принимает любой
пакет от 20 байт с первыми двумя байтами `01 00` — а именно так начинается
handshake initiation WireGuard, — поэтому plain WireGuard с устройства в LAN раньше
помечался `bittorrent` и уходил в торрент-правило. Ни один апстримный файл снифферов
не правится: каждый сниффер форка — отдельный `*_lx.go` с более строгой формой.

## Главное, что нужно знать: считается только первый пакет

Роутер смотрит **одну** датаграмму на поток (дальше читает только чтобы собрать
фрагментированный QUIC ClientHello). Следствия:

- **AmneziaWG с junk (`jc > 0`)** — первая датаграмма это случайный мусор, ничего не
  совпадает, поток остаётся без имени. Идущий следом хендшейк никто не смотрит.
- **AmneziaWG с decoy (`ip=quic` / `dns` / `stun` / `sip`)** — первый пакет и есть
  валидный QUIC Initial / DNS-запрос / STUN-запрос / SIP INVITE, поэтому поток
  получает имя `quic` (с SNI из `id`), `dns`, `stun` или `sip`. В этом смысл decoy,
  и он прячет туннель и от собственного роутера: `.ru`-SNI попадёт под правило
  «российские домены напрямую».
- **Не-дефолтные `h1`–`h4` или `s1`–`s4`** — формы больше нет, так и задумано; не
  распознаётся.
- Поток, начатый **до рестарта ядра**, виден с середины: transport-пакеты WireGuard
  всё равно узнаются (правило для типа 4), остальные нет.

Обфусцированные туннели маршрутизируйте по адресу назначения (`ip_cidr` своих
VPN-серверов).

## Пример для роутера

Все туннели из LAN — через WARP-outbound, звонки — напрямую, и торрент-правило больше
не глотает VPN. Порядок важен: правило про VPN идёт первым.

```jsonc
{
  "route": {
    "rules": [
      { "inbound": ["tun-in"], "action": "sniff", "timeout": "1s" },
      { "protocol": ["wireguard", "openvpn", "ike", "tailscale"], "outbound": "🔥🎭 WARP (MASQUE)" },
      { "protocol": "sip", "outbound": "direct-out" },
      { "protocol": "bittorrent", "outbound": "direct-out" }
    ]
  }
}
```

Узкое действие `sniff`, когда интересны только туннели:

```jsonc
{ "action": "sniff", "sniffer": ["wireguard", "openvpn", "ike", "tailscale"] }
```

Поскольку `sip` ставит `domain`, доменные правила применяются к звонкам так же, как к
TLS: `{ "protocol": "sip", "domain_suffix": "sip.example.com", "outbound": "…" }`.

## Намеренно не распознаётся

RTP/RTCP (форма слишком слабая, ложные срабатывания), L2TP / PPTP (устарели), KCP
(общий заголовок), VNC (первым говорит сервер) и все обфусцированные транспорты —
Shadowsocks, VMess, MTProto, Hysteria/salamander, AmneziaWG с изменёнными
заголовками. Узнавать их — работа DPI, и всё, что нашёл бы форк, найдёт и провайдер.

## Диагностика

При `log.level: debug` каждый распознанный поток пишет
`router: sniffed packet protocol: <имя>` (для `sip` — ещё `domain: …`), а следом —
правило, в которое он попал. Поток без строки `sniffed` не узнал ни один сниффер —
см. «считается только первый пакет».
