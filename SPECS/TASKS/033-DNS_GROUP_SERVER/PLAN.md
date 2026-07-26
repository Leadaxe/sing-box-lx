# PLAN 033 — DNS-группа: тип `group`, режим failover

## Файлы

### Новые (нулевой ребейз-конфликт)

| Файл | Содержимое |
|------|------------|
| `dns/transport/group/group.go` | `RegisterTransport(registry)`, `type Transport`, конструктор `NewTransport(ctx, logger, tag, options)`, `Start/Close/Reset`, `Exchange/ExchangeAsync`, down-состояние |
| `dns/transport/group/group_test.go` | Юниты failover/down_time/валидаций + интеграционный цикл-тест через `dns.NewTransportManager` |
| `test/` или `dns/transport/group/group_box_test.go` | Живой `box.New` на семантику `servers: []` (по образцу существующих box-тестов; точное место — по факту) |

### Правки upstream-файлов (маркер `// lx:begin dns-group`, отдельный коммит)

| Файл | Строки |
|------|--------|
| `constant/dns.go` | `DNSTypeGroup = "group"` рядом с остальными `DNSType*` |
| `option/dns.go` | `GroupDNSServerOptions` рядом с остальными `*DNSServerOptions` |
| `include/registry.go` | `group.RegisterTransport(registry)` в `DNSTransportRegistry()` |

## Структура типов

```go
type Transport struct {
    dns.TransportAdapter                    // NewTransportAdapter(C.DNSTypeGroup, tag, options.Servers)
    ctx        context.Context
    logger     log.ContextLogger
    serverTags []string
    mode       string                       // C.DNSGroupModeFailover / ...Race (локальные константы пакета)
    interval   time.Duration                // используется в 034
    downTime   time.Duration                // дефолт 30s
    access     sync.Mutex
    servers    []adapter.DNSTransport       // заполняется в Start
    lastFail   map[string]time.Time         // тег → время последнего сбоя
}
```

- Конструктор: валидация `servers`/`mode`, warning про `interval` вне `race`,
  временная ошибка на `mode: race` (снимается в 034).
- `Start(StartStateStart)`: резолв тегов через
  `service.FromContext[adapter.DNSTransportManager](ctx)` (образец —
  `route/rule/rule_item_preferred_by_dns.go:30`), проверка типов участников
  (`fakeip`/`hosts` → ошибка).
- `Exchange`: снапшот живых под мьютексом → последовательный обход;
  классификатор сбоя `isFailure(response, err)`:
  err != nil (включая `dns.RcodeError(SERVFAIL)`; `RcodeError(NXDOMAIN)` —
  НЕ сбой) или `response.Rcode == dns.RcodeServerFailure`.
- `ExchangeAsync`: горутина поверх `Exchange` (группе нечего выигрывать
  от нативного async в failover; race в 034 сделает свой фан-аут).
- Down-учёт: `markFailure(tag)`, `alive(now) []adapter.DNSTransport`,
  `oldestFailed(now)`.

## Порядок коммитов (IMPLEMENTATION_PROMPT §3.2)

1. `lx(dns-group): add group DNS transport package (failover)` — новый пакет + тесты.
2. `lx(dns-group): wire group transport (constant, option, registry)` — три
   маркированных шва.

## Riски

- Семантика `[]` → nil в badjson — проверяется только живым `box.New`.
- `RcodeError` может прийти и ошибкой, и ответом (`dns/client.go:649`
  конвертирует НАД группой, участники под группой возвращают ошибку) —
  классификатор обязан покрыть оба представления.
- `local` транспорт на некоторых платформах сам гоняет фан-аут — группе
  это не мешает, но в тестах участники должны быть детерминированными
  фейками, не реальными типами.
