# SPEC: 078 — WIREGUARD_PACKET_SNIFFER

**Фича:** [SNIFF](../../FEATURES/016-SNIFF/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — новый сниффер; попутно закрывает ложное срабатывание апстримного uTP |
| Статус | I (implemented) — код, юниты, сборка `lx-build`, живой прогон через бинарь; полевой прогон на роутере не делался |
| Ветка | `lx` |
| Base | `3f1e8d710` (v1.14.0-lx.31) |
| Связанные | [009](../009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) (найдено при проверке `ip=quic` через роутер 2026-09-05) |

**Touches:** `constant/protocol.go`, `route/route.go` (дефолтный порядок),
`route/rule/rule_action.go` (ветка по имени) — три маркера `lx:begin sniff-wireguard`;
новые `common/sniff/wireguard_lx.go` + тест; строка в `docs/configuration/route/sniff{,.zh}.md`.
Промисы других фич не затронуты.

## Why

Роутер на нашем ядре (RouteRich, lx.29, `sniff(1s)` на `tun-in`) получил с Mac
plain-WireGuard handshake initiation к Proton и записал в лог
`sniffed packet protocol: bittorrent`, после чего поток ушёл в правило
`protocol=bittorrent => direct-out`. Причина — апстримный `sniff.UTP`
(`common/sniff/bittorrent.go`): он принимает **любой** пакет от 20 байт, у
которого первый байт `0x01` (uTP v1, ST_DATA) и второй `0x00` (без
расширений). Handshake initiation начинается с `01 00 00 00` и длиной 148
байт проходит проверку всегда. Ответ (`02`) и cookie (`03`) не проходят по
версии, transport (`04`) тоже; ошибка только на первом пакете потока, то есть
ровно там, где сниффер решает маршрут.

Второе, важнее первого: у пользователя нет имени, которым можно
замаршрутизировать WireGuard чужого устройства. Правило по порту 51820
покрывает только стандартный порт и проигрывает bittorrent-правилу, стоящему
выше.

## Что сделано

`sniff.WireGuard` — packet-sniffer по типу и размеру датаграммы:

| тип | сообщение | условие |
|-----|-----------|---------|
| 1 | handshake initiation | `len == 148` |
| 2 | handshake response | `len == 92` |
| 3 | cookie reply | `len == 64` |
| 4 | transport data | `len >= 32 && (len-32) % 16 == 0` |

Правило для типа 4 следует из раскладки: 16-байтный заголовок + payload,
дополненный до кратного 16, + 16-байтный тег Poly1305; keepalive — 32 байта.
Тип 4 нужен, чтобы поток, начатый до рестарта ядра на роутере, всё равно
получил имя. Резервные байты после типа **не** проверяются: Cloudflare WARP
пишет в них client id, форма однозначна и без них.

- Константа `C.ProtocolWireGuard = "wireguard"`.
- Дефолтный порядок: `DomainNameQuery, QUICClientHello, STUNMessage,
  WireGuard, UTP, …` — раньше uTP, позже QUIC/STUN (у тех сильнее проверки).
- `sniffer: ["wireguard"]` в `sniff`-действии; `protocol: wireguard` в правилах.
- AmneziaWG с не-дефолтными H1–H4 или S1–S4 не распознаётся намеренно.
  С junk/decoy сниффер увидит их первыми: junk не совпадёт ни с чем,
  `ip=quic` совпадёт как `quic` с SNI из `id` — так и должно быть.

## Критерии приёмки

- [x] Юниты: 8 положительных форм (вкл. WARP-резерв), 10 отрицательных
  (uTP SYN/STATE, STUN, сдвиги размеров, чужой тип); тест, документирующий
  ложное срабатывание uTP и его снятие порядком.
- [x] `go test ./common/sniff/ ./route/...` зелёный; `go vet` чистый.
- [x] `make -f Makefile.lx lx-build` собирается; `sing-box check` принимает
  `sniffer: ["wireguard"]` и `protocol: wireguard`.
- [x] Живой прогон: inbound `direct` (udp) → `sniff` → реальная initiation
  к живому WG-серверу: лог `sniffed packet protocol: wireguard`,
  `match[1] protocol=wireguard`, handshake response 92 байта вернулся;
  uTP SYN тем же путём — `bittorrent`.
- [ ] Полевой прогон на роутере: поток WG с LAN-устройства попадает в
  правило `protocol: wireguard` (после релиза ядра на RouteRich).

## Фактура (2026-09-05)

Лог роутера при трёх потоках с Mac к `169.150.218.80:51820`:

```
sniffed packet protocol: quic, domain: telemost.yandex.ru → match[19] ru-domains → direct-out
sniffed packet protocol: quic, domain: apteka.ru          → match[19] ru-domains → direct-out
sniffed packet protocol: bittorrent                        → match[6] protocol=bittorrent → direct-out
```

Третья строка — plain-WG initiation. Маршрут в тот день совпал, но только
потому, что оба правила вели в `direct-out`.
