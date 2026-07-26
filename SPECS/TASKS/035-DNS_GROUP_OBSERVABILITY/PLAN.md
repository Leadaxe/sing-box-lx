# PLAN 035 — фактический сервер в DNS-потоке

## Файлы

| Файл | Правка |
|------|--------|
| `common/dnstrack/effective_server.go` (новый) | ctx-холдер: `WithEffectiveServer` / `SetEffectiveServer` / `EffectiveServerFromContext` |
| `dns/client_log.go` (lx) | Вставка холдера в ctx обмена; чтение в `emitQueryEvent`/`emitFailedQuery` |
| `dns/transport/group/group.go`, `race.go` | `SetEffectiveServer` при успешном ответе участника |
| `dns/transport/group/observability_test.go` (новый) | Юниты §3 SPEC |

⚠️ Точку вставки холдера в ctx определить по факту чтения существующих
швов SPEC 018 в `dns/client.go`: холдер должен попасть в ctx ДО вызова
`transport.Exchange` и быть виден в `finishExchange`. Кандидат —
существующий lx-шов у `beginExchange`/`contextWithTransportTag`;
если шва нет — расширить маркированную зону SPEC 018, не создавая новую.

## Порядок коммитов

1. `lx(dns-group): attribute actual answering member in DNS query stream`
   (dnstrack + group + тесты).
2. Отдельно, если понадобится: `lx(dns-group): extend SPEC018 seam for
   effective-server holder` (только если правка легла в upstream-файл).

## Грабли

- Холдер обязан быть per-request (никакого состояния на транспорте) —
  конкурентные запросы через одну группу не должны путать атрибуцию.
- ExchangeAsync-путь (033 оборачивает Exchange горутиной) наследует ctx —
  проверить, что холдер жив и там.
- Не менять `.proto` — семантика полей та же, меняется только источник
  значения.
