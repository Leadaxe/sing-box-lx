# PLAN: 078 — WIREGUARD_PACKET_SNIFFER

## Решение

Не трогать апстримный `bittorrent.go` (P2 фичи SNIFF): поставить впереди
uTP свой сниффер с более сильной проверкой. Форма plain-WG фиксирована
протоколом, эвристика не нужна.

```go
// common/sniff/wireguard_lx.go
func WireGuard(_ context.Context, metadata *adapter.InboundContext, packet []byte) error {
	switch packet[0] {
	case 1: len == 148
	case 2: len == 92
	case 3: len == 64
	case 4: len >= 32 && (len-32)%16 == 0
	}
	metadata.Protocol = C.ProtocolWireGuard
}
```

## Точки касания upstream (все под `lx:begin sniff-wireguard`)

| Файл | Что | Почему нельзя иначе |
|------|-----|---------------------|
| `constant/protocol.go` | `ProtocolWireGuard` | список констант |
| `route/route.go` | `sniff.WireGuard` перед `sniff.UTP` в `defaultPacketSniffers` | диспетчер порядка |
| `route/rule/rule_action.go` | `case C.ProtocolWireGuard` | диспетчер по имени |
| `docs/configuration/route/sniff{,.zh}.md` | строка таблицы | пользовательская дока апстрима |

## Порядок

QUIC и STUN стоят раньше: у них криптографическая/magic-cookie проверка,
и decoy `ip=quic` (SPEC 009) должен выигрывать у любого размера. uTP — сразу
после нас: единственное пересечение форм — `01 00` длиной 148, и его забираем мы.

## Риски

- Тип 4 без проверки резервных байт: любой пакет `04 …` длиной `32+16k`
  получит имя `wireguard`. Другие снифферы дефолтного набора на `0x04` не
  претендуют (uTP отвергает версию 4), поэтому раньше такой пакет оставался
  без имени; регресса нет.
- Мерж апстрима может переставить `defaultPacketSniffers` — маркер и
  комментарий в `route.go` держат порядок; см. «Следить» в FEATURE 016.
