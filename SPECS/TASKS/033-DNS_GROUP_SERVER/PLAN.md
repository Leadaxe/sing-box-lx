# PLAN 033 — TTL-модель, режимы (v2)

## Файлы

| Файл | Правка |
|------|--------|
| `dns/transport/group/group.go` | Переписать: `memberRecord{errors,wins,lastRTT}` вместо `memberState`; выбор цели по режимам; единый поток; бюджет цели (½ остатка); выживание; лог решений |
| `dns/transport/group/fan.go` (заменяет `race.go`) | Веер под ctx запроса: коллектор, первый успех, guard ошибок, победы, опоздавшие; CAS-флаг выборов |
| `dns/transport/group/group_test.go`, `fan_test.go` (заменяет `race_test.go`), `observability_test.go` | Полная переработка юнитов под §4 SPEC |
| `option/dns.go` | Внутри существующего lx-блока: `Mode`, `ErrorTTL`, `WinTTL` (поля `Interval`/`DownTime` удаляются) |
| `test/dns_group_lx_test.go`, `test/dns_group_trace_lx_test.go` | Конфиги на v2-режимы; негативные кейсы `mode: failover` / `down_time` |
| `constant/dns.go`, `include/registry.go` | Без изменений |

## Решения

- Ленивое отсечение TTL при чтении; кап длины срезов (например 64) —
  счёт выше не различим.
- Под-дедлайн цели: `remaining := time.Until(deadline)`; цель =
  `context.WithTimeout(ctx, remaining/2)`; без дедлайна у ctx — фолбэк
  `C.DNSTimeout/2`. Веер — под родительским ctx, без детача.
- Guard: сбой участника веера записывается только при живом родительском
  ctx (`ctx.Err() == nil` в момент фиксации исхода).
- Случайность: `math/rand/v2` (Intn по срезу кандидатов).
- Липкость: поле `current` (тег) под мьютексом; смена — info-лог.
- Выборы: `electionRunning bool` + gen против Reset (образец v1 race).
- Трасса (стык 035): цель+веер пишутся как attempts; survival-путь
  помечается в холдере.

## Порядок коммитов

1. `lx(dns-group): TTL model core — records, mode selection, unified flow (SPEC 033 v2)` — group.go+fan.go+юниты, option-поля.
2. Наблюдаемость/прото — по PLAN 035.
3. Live-тесты и доки.

## Грабли

- `-race` обязателен: веер пишет записи из горутин.
- Тесты времени — короткие TTL (десятки мс) и фейки с управляемыми
  задержками; не полагаться на wall-clock точность.
- Один мьютекс на всё состояние группы: не звать participant.Exchange
  под ним.
