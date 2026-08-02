# PLAN: 046 — DNS_HIJACK_PACKET_LOOP_STALL

## Решение

Одна точка — `route/dns.go` `Router.HijackDNSPacket` (общая для tun
system/gvisor и wireguard/openvpn/openconnect/tailscale endpoints):
после `message.Unpack` уводить exchange в горутину под лимитером.

```go
func (r *Router) HijackDNSPacket(ctx context.Context, payload []byte, writer N.PacketWriter, metadata adapter.InboundContext) {
	var message mDNS.Msg
	err := message.Unpack(payload)          // копирует данные из payload в структуру
	if err != nil { ... }                   // как сейчас
	// lx: 046 — не блокировать пакетный цикл стека: постановка запроса
	// (dial DNS-транспорта под capacity-локом пула) может висеть до
	// DNS-таймаута, если detour сервера мёртв. Сверх лимита — дроп (UDP,
	// клиент ретрайнет).
	if !r.dnsHijackSem.TryAcquire(1) {
		r.logger.DebugContext(ctx, "dns hijack overloaded, dropping query")
		return
	}
	go func() {
		defer r.dnsHijackSem.Release(1)
		... существующее тело (ExchangeAsync + writeDNSPacketResponse) ...
	}()
}
```

Обоснования:

- **payload не копируем**: `mDNS.Msg.Unpack` полностью распаковывает пакет в
  собственные аллокации; сырой буфер после Unpack не используется — поэтому
  Unpack остаётся до `go`, в горутину уходит только `message`.
- **writer безопасен из чужой горутины**: callback `ExchangeAsync` уже сегодня
  приходит из горутин транспорта (`queryMultiplexer.recvLoop` /
  singleflight-ветка `exchangeWait`), т.е. `writer.WritePacket` из не-цикловой
  горутины — существующий контракт, не новый (gvisor `UDPBackWriter` держит
  mutex; system `dnsResponseWriter` собирает пакет локально и пишет через
  writeback).
- **Лимитер** — `semaphore.Weighted` (уже в зависимостях, используется
  `ConnPool`), ёмкость 256. `TryAcquire` без блокировки: переполнение = дроп
  запроса, цикл не ждёт. Семафор освобождается в конце горутины постановки —
  ограничивает и горутины, и число одновременно висящих в `acquireShared`
  постановок.
- `Release` в defer горутины, а не в callback exchange: callback может не
  прийти вовсе только при панике постановки — defer покрывает.

Ёмкость 256: постоянный фон инцидента — единицы запросов в секунду при
10-секундном таймауте ⇒ рабочее заполнение ≈ десятки; 256 даёт запас на
шторм и стоит < 1 МБ стеков горутин в пике.

## Что НЕ делаем (альтернативы отвергнуты)

- **Фикс в sing-tun (обе ветки стека)** — два места вместо одного, в system-
  ветке пришлось бы копировать payload (буфер принадлежит циклу до Unpack),
  и не закрывает endpoint-вызывателей (`wireguard/endpoint.go:485` и др.).
- **Неблокирующий рефакторинг `ConnPool.acquireShared` / `Client.ExchangeAsync`
  до конца цепочки** — инвазивная переделка upstream-кода, дорога при каждом
  синке; сама блокировка постановки для остальных вызывателей (per-connection
  горутины) безвредна.
- **`hijackDNSStream` (TCP DNS) не трогаем** — вызывается из
  `NewConnectionEx`, это уже per-connection горутина ConnectionManager,
  пакетный цикл не блокируется.

## Файлы

| Файл | Изменение |
|------|-----------|
| `route/dns.go` | `HijackDNSPacket`: go-wrap + `TryAcquire`/`Release`, `// lx: 046` |
| `route/router.go` | поле `dnsHijackSem *semaphore.Weighted`, инициализация в `NewRouter` |
| `route/dns_hijack_test.go` (новый) | behaviour-тест: зависший exchange не блокирует следующий hijack-вызов |
| `SPECS/FEATURES/004-HOTFIXES/FEATURE.md` | строка реестра |

## Зона касания upstream

`route/dns.go` (+~12 строк, один `// lx:`-блок), `route/router.go` (+2 строки).
Merge-конфликт с upstream вероятен только при переписывании HijackDNSPacket
апстримом — редкая зона.
