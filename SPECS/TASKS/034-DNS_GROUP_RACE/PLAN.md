# PLAN 034 — режим `race`

## Файлы (все новые/lx-owned, upstream не трогаем)

| Файл | Содержимое |
|------|------------|
| `dns/transport/group/race.go` | Гоночное состояние, фан-аут, выбор между гонками |
| `dns/transport/group/race_test.go` | Юниты §3 SPEC |
| `dns/transport/group/group.go` | Снять временную ошибку `mode: race`; диспетчер Exchange по mode |

## Структура

```go
// в Transport (group.go) добавляются:
raceMu      sync.Mutex     // может быть общий access-мьютекс 033
winner      string         // тег победителя, "" до первой гонки
ranking     []string       // порядок прихода успешных ответов последней гонки
lastRace    time.Time      // нулевое значение = гонки не было
raceRunning bool           // сериализация кандидатов
```

- `exchangeRace(ctx, msg)`:
  1. под мьютексом: решить — гонщик ли этот запрос
     (`lastRace.IsZero() || since >= interval`, и `!raceRunning`);
  2. гонщик: снапшот живых; на каждого — горутина
     `member.Exchange(detachedCtx, msg.Copy())`,
     `detachedCtx = context.WithTimeout(context.WithoutCancel(ctx), C.DNSTimeout)`;
     результаты — в канал коллектора;
  3. коллектор (отдельная горутина, живёт до последнего участника):
     первый успех → отдать гонщику + `winner`; каждый успех →
     `ranking = append`; каждый сбой → `markFailure`; финал →
     `raceRunning = false`;
  4. не-гонщик: `current()` → победитель или фолбэк по рейтингу/033;
     до первой гонки — ожидание её первого успеха (канал) с дедлайном ctx.
- `msg.Copy()` на каждого участника обязателен — участники мутируют Id.

## Порядок коммитов

1. `lx(dns-group): race mode — lazy fan-out, winner, ranking` (race.go + правка group.go + тесты).

## Грабли

- Отвязанный контекст обязан наследовать values (`WithoutCancel` сохраняет) —
  участникам нужен ctx с сервисами (dialer/manager).
- Тесты времени — на управляемых каналах-фейках, не на `time.Sleep`
  по возможности; допустимые sleep — миллисекундные с запасом.
- `-race` прогон обязателен: коллектор пишет состояние из горутины.
