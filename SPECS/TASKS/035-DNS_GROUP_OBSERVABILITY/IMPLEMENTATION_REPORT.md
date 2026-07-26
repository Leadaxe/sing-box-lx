# IMPLEMENTATION_REPORT 035 — фактический сервер в DNS-потоке

## Файлы

| Зона | Файлы |
|------|-------|
| Новый lx-код | `common/dnstrack/effective_server.go` (ctx-холдер), правки `dns/client_log.go` (чтение холдера в `emitQueryEvent`/`emitFailedQuery`), `dns/transport/group/{group,race}.go` (`recordEffective` на успехе), тесты `dns/client_log_effective_lx_test.go`, `dns/transport/group/observability_test.go` |
| `// lx:` upstream | `dns/client.go` — 3 строки в `beginExchange`: холдер кладётся в operation-ctx только для транспортов типа `group` (ноль затрат на остальных) |

`.proto`/`.pb.go` не тронуты — семантика полей `dnsServer`/`dnsServerType`
та же, честнее источник значения; LxBox совместим без изменений.

## Семантика (сверено со SPEC)

- Обмен через группу → тег/тип фактически ответившего участника; его
  `outbound` подставляется в событие (у группы своего нет).
- Кеш-попадания и полный сбой группы → тег группы (холдер пуст — валидное
  состояние).
- Прямые транспорты — поведение прежнее (холдер не создаётся).

## DoD

- Юниты: failover-участник, race-победитель (и межгоночные запросы),
  полный сбой → unset, no-op без холдера; emit-уровень: подмена значения,
  сохранение тега группы для кеша/сбоя, прямой транспорт.
- `go test -race ./dns/... ./common/dnstrack/`, vet, gofmt — чисто;
  `git status` пуст по `.proto`/`.pb.go`.

## Грабля, пойманная в ходе работы

`observable.Observer` при `UnSubscribe` закрывает done-канал, а не канал
подписки — `for range subscription` не завершается никогда. В тестовой
обвязке нужен select по обоим каналам (см. `newEmitTestContext`).

## Вне скоупа (отложено)

RPC состояния группы (победитель/рейтинг/down-список) — до конкретного
запроса LxBox (§3.1(а1)); при появлении — отдельная задача по §3.6.
