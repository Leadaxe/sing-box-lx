# SPEC: 079 — VPN_VOIP_PACKET_SNIFFERS

**Фича:** [SNIFF](../../FEATURES/016-SNIFF/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — четыре новых сниффера: `openvpn`, `ike`, `tailscale`, `sip` |
| Статус | I (implemented) — решение владельца 2026-09-05 («OpenVPN, IKEv2/IPsec, Tailscale, SIP — делаем; остальное нет»); код, юниты, `lx-build`, `check`, прогон всех четырёх через бинарь; полевого прогона на роутере не было |
| Ветка | `lx` |
| Base | `3f1e8d710` (v1.14.0-lx.31) + незакоммиченный 078 |
| Связанные | [078](../078-WIREGUARD_PACKET_SNIFFER/SPEC.md) (образец и порядок), [009](../009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) (decoy `ip=sip` получит честное имя) |

**Touches:** `constant/protocol.go`, `route/route.go` (дефолтные packet- и
stream-списки), `route/rule/rule_action.go` (имена) — под теми же маркерами
`lx:begin sniff-*`; новые `common/sniff/{openvpn,ike,tailscale,sip}_lx.go` + тесты;
строки в `docs/configuration/route/sniff{,.zh}.md`; общая страница
`docs-lx/lx-sniff.md` / `.ru.md`. Промисы других фич не затронуты.

## Why

Апстримный набор снифферов закрывает web и торренты. Роутер на нашем ядре
видит чужие туннели и звонки из LAN только как байты, а маршрутизировать их
хочется по имени, а не по портам: VPN-клиенты сидят на произвольных портах,
а bittorrent-правило стоит выше любого порта. 078 закрыл WireGuard; здесь —
остальные протоколы, у которых **первый пакет клиента** имеет однозначную форму.

Отобрано по критерию «форма детерминирована протоколом, а не эвристикой»:

| Имя | Сеть | Первый пакет клиента | Почему однозначно |
|-----|------|----------------------|-------------------|
| `openvpn` | UDP, TCP | `P_CONTROL_HARD_RESET_CLIENT_V2/V3` | opcode в старших 5 битах (`0x38`/`0x50`), session id 8 б; без tls-auth ровно 14 б с нулевым ack-массивом и packet-id 0; с tls-crypt — replay packet-id `1` и net-time; с tls-auth — HMAC известных длин, за ним packet-id `1`; TCP — то же за 2-байтной длиной |
| `ike` | UDP | ISAKMP `IKE_SA_INIT` (v2) / `Identity Protection`, `Aggressive` (v1) | SPIr = 0, version `0x20`/`0x10`, exchange type 34 / 2 / 4, флаг Initiator, message id 0, поле length == размер пакета; на 4500 впереди 4-байтный non-ESP маркер |
| `tailscale` | UDP | disco ping | магия `TS💬` (6 б), 32 б ключ, 24 б nonce, ≥16 б box → ≥ 78 б; сам туннель Tailscale = WireGuard, его ловит 078 |
| `sip` | UDP, TCP | request-line | `METHOD sip:…​ SIP/2.0\r\n`, метод из RFC 3261/3262/3265/3428/3515/3903; host из Request-URI → `Domain` |

Отвергнуто (решение владельца): RTP/RTCP (слабая форма), L2TP/PPTP (устарели),
обфусцированные транспорты (это работа DPI против нас), KCP, VNC (первым говорит
сервер).

## Требования

- R1. Каждый сниффер — отдельный файл `*_lx.go`, апстримные файлы снифферов не
  правятся (P2 фичи).
- R2. Порядок в дефолтных списках: packet — после `WireGuard`, до `UTP`:
  `OpenVPN, IKE, TailscaleDisco, SIP`; stream — после `RDP`: `SIP, OpenVPN`.
  Ни один новый сниффер не должен перехватывать вектор из апстримных тестов
  (`quic`, `stun`, `dns`, `utp`, `dtls`, `ntp`, `http`, `tls`, `ssh`, `rdp`) и
  наоборот — проверяется тестами перекрёстно.
- R3. `sip` ставит `Domain` = host из Request-URI (без `user@`, порта и
  параметров) — так decoy `ip=sip` (009) и настоящий звонок маршрутизируются
  доменными правилами одинаково.
- R4. OpenVPN TCP-вариант — stream-сниффер: 2 байта длины, затем та же проверка;
  недобор данных → `ErrNeedMoreData`.
- R5. Все четыре имени доступны в `sniffer: [...]` и `protocol:` правил.
- R6. Общая документация: страница `docs-lx/lx-sniff.md` + `.ru.md` — таблица всех
  снифферов форка (078 + 079), что даёт каждый (`protocol`/`domain`/`client`),
  дефолтный порядок, ограничение «первый пакет потока», пример правил для роутера.

## Критерии приёмки

- [x] Юниты: положительные векторы всех форм из таблицы (OpenVPN plain/tls-auth/
  tls-crypt/TCP; IKEv2 на 500 и 4500, IKEv1 main/aggressive; disco; SIP INVITE/
  REGISTER с доменом), отрицательные: чужие векторы + сдвиги размеров/полей.
- [x] Перекрёстный тест: дефолтные списки на всех векторах дают ожидаемое имя.
- [x] `go test ./common/sniff/ ./route/...` + `-race`; `go vet`; `lx-build`;
  `sing-box check` с четырьмя именами в `sniffer` и `protocol`.
- [x] Прогон через `direct`-inbound бинаря `lx-build`: синтетические, но валидные по форме пакеты всех четырёх протоколов → `sniffed packet protocol: openvpn|ike|tailscale|sip, domain: apteka.ru`, каждый попал в своё правило. Против реального OpenVPN/IKE-сервера не гонялось (нет стенда).
- [x] Строки в апстримной доке sniff.md/.zh.md; страница `docs-lx/lx-sniff{,.ru}.md`;
  ссылка из `docs-lx/lx-config.md`.
