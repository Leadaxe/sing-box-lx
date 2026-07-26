# IMPLEMENTATION_REPORT 035 — наблюдаемость DNS-группы (v3)

## Файлы

| Зона | Файлы |
|------|-------|
| lx-код | `common/dnstrack/query_trace.go` (`fanned`/`survival` вместо `racer`; API холдера прежний), `manager.go` (поля `QueryEvent`), `dns/client_log.go`, `dns/transport/group/*` (разметка трассы в новом потоке), `daemon/started_service_command_lx.go` (маппинг v3 + кламп rtt), `experimental/libbox/command_client_command_lx.go` |
| Upstream-шов | `daemon/started_service.proto` (lx-зона): `fanned=15`, `survival=16`; `DnsGroupMember`/`DnsGroupState` v3 — ЗАМЕНА v2-полей (v2 не было в релизном теге, потребителей нет); `dns/client.go` — шов 035 без изменений |
| Регенерация | pb через pinned `lx-proto`, шум откачен |
| Тесты | `observability_test.go` (таблица сценариев §3 целиком + state v3), `dns/client_log_effective_lx_test.go`, `daemon/started_service_dnsgroup_stub_lx_test.go`, живой `test/dns_group_trace_lx_test.go` |

## Контракт для LxBox

- Поток: `dnsGroupPath` (изнутри наружу), `attempts`
  (`answered|timeout|network_error|servfail` + rttMs), `fanned`, `survival`;
  атрибуция — фактический лист, кеш/полный сбой — тег группы.
- `CommandClient.GetDNSGroups() DnsGroupIterator` → `DnsGroup{Tag, Mode,
  Current, Members()}`, `DnsGroupMember{Tag, ServerType, Clean, LiveErrors,
  LastErrorAgeMs(-1 = нет), LiveWins, Current, LastRTTMs(0 = не мерялся,
  живое < 1мс клампится в 1)}`.
- `DnsQuery.Fanned/Survival` + `GroupPath()/Attempts()`.

## Ключевые решения ревизии

- Заброшенные пробы веера (сбой при завершённом ctx) — не исход: ни в
  здоровье, ни в трассу (иначе отменённые клиентом гонщики мусорили
  событие ложными network_error — поймано живым тестом).
- `survival` в событии обязателен: без него деградация «отвечаем через
  наименее грязного» неотличима от здоровья (находка панели дизайна).
- Путь при веере через группы-сиблинги — множество затронутых групп;
  ветка ответа определяется по write-once листу.

## DoD

Юниты по таблице §3 (все строки), state v3, stub `Unimplemented`;
живой тест: опоздавший ответ не в снимке; прото аддитивно к 1–12,
сборки ± теги, vet, gofmt — чисто.

## Вне скоупа

Push-подписка состояния; выдача TTL через RPC; полевой device-verify.
