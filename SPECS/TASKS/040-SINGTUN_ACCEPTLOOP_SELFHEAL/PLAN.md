# PLAN — 040-SINGTUN_ACCEPTLOOP_SELFHEAL

## Approach

Минимальный форк sing-tun: сабмодуль `submodules/sing-tun` на upstream
`2d9b8aed5fe2` (ревизия из go.mod ядра) + local-path `replace` — точная копия
схемы wireguard-go. Патч локализован в `stack_system.go` (+ тест-файл), чтобы
дифф против апстрима оставался однофайловым и дёшево переживал будущие синки.

Дизайн патча:

1. `System` получает:
   - `closing atomic.Bool` — выставляется в начале `Close()` до закрытия
     листенеров; отличает штатный останов от убийства;
   - `acceptRecoveries atomic.Uint32` — счётчик восстановлений (телеметрия);
   - `tcpPort`/`tcpPort6` — атомарное чтение/запись (`atomic.Uint32`):
     пишутся из `acceptLoop` (relisten), читаются конкурентно из
     `tunLoop`-пути (`dispatchIPv4/6`, NAT rewrite).
2. `acceptLoop(listener, isIPv6)`:
   - `Accept` err при `closing` → тихий return (текущее поведение);
   - иначе: warn-лог (полный err — errno называет путь убийства + порт),
     relisten тем же кодом, что в `start()` (3 попытки,
     `retryableListenError`, 1s пауза);
   - успех → записать новый listener/port, warn-лог
     «recreated (old→new port)», продолжить петлю;
   - провал → error-лог, return (health-остаток).

## Decisions

- health-бит/RPC вместо самолечения — отклонено: сообщает «TCP мёртв», но
  юзеру всё равно нужен рестарт VPN; самолечение убирает класс поломки, а
  warn-лог даёт ту же диагностику (решение владельца, 2026-07-31).
- Детектор через pprof goroutine-дамп из LxBox — отклонено: парсинг текстовых
  дампов в проде («это лажа»), плюс ловит только пост-фактум.
- Патчить vendor/копию вместо сабмодуля — отклонено: прецедент wireguard-go
  показал, что сабмодуль + replace дешевле на апстрим-синках и прозрачнее
  для аудита диффа.
- Восстанавливать листенер на СТАРОМ порту (bind :port вместо :0) —
  отклонено: порт может быть занят (в т.ч. тем самым чужим сокетом,
  унаследовавшим номер), а обновление `tcpPort` всё равно требуется для
  корректности — значит просто `:0`.

## Touch area

| File | What changes |
|---|---|
| `go.mod` / `go.sum` | `replace github.com/sagernet/sing-tun => ./submodules/sing-tun` |
| `.gitmodules` | новый сабмодуль `submodules/sing-tun` |
| `submodules/sing-tun/stack_system.go` | флаг closing, атомарные порты, самолечение acceptLoop (форк, дифф против `2d9b8aed5fe2`) |
| `submodules/sing-tun/stack_system_selfheal_test.go` | новый тест red/green + регрессия штатного останова |
| `SPECS/FEATURES/004-HOTFIXES/FEATURE.md` | строка реестра (на закрытии) |
| `docs/lx-changelog.md` | запись под будущий тег (на закрытии; релиз не в этой сессии) |
