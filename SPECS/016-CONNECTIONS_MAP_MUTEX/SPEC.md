# SPEC 016 — Connections: гонка map (нет мьютекса в ApplyEvents/Filter/Iterator)

**Тип:** долг ядра (bug, data race → fatal)
**Статус:** Open — обойдено клиент-стороной в LxBox §170, в ядре НЕ исправлено
**Приоритет:** High (приводит к `fatal error` → abort всего процесса при ≥2
подписчиках `CommandConnections`)
**Файл:** `experimental/libbox/command_types.go`
**Связано:** SPEC 014 (Clash→CommandClient), SPEC 015 (RPC extensions)

## Симптом

При двух одновременных подписчиках на `CommandConnections` (например клиент с
открытым Stats-экраном + клиент-профайлер, оба слушают connections-стрим)
процесс падает:

```
fatal error: concurrent map iteration and map write
goroutine ... [running]:
libbox.(*Connections).ApplyEvents(...)
    experimental/libbox/command_types.go:170
... WriteConnectionEvents → handleConnectionsStream
```

→ `SIGABRT`, весь процесс умирает (на хост-приложении — Android-процесс целиком).

## Корень

`Connections` (command_types.go:115) держит разделяемое изменяемое состояние
**без какой-либо синхронизации**:

```go
type Connections struct {
    connectionMap map[string]*Connection  // ← map, читается+пишется
    input         []Connection
    filtered      []Connection
    filterState   int32
    filterApplied bool
}
```

Методы, читающие/пишущие это состояние, вызываются из РАЗНЫХ горутин (по одной
на gRPC-подписчика `handleConnectionsStream`), но не защищены:

| Метод | Строки | Доступ к connectionMap/input/filtered |
|---|---|---|
| `ApplyEvents` | 129-179 | пишет map (142/157), пересоздаёт (134), удаляет, **итерирует map** (170) |
| `evictClosedConnections` | 181-190 | итерирует + delete map (182/187) |
| `FilterState` | 192-212 | читает input, пишет filtered |
| `SortByDate/Traffic/TrafficTotal` | 214-252 | сортирует filtered in-place |
| `Iterator` | 254+ | читает filtered |

Когда подписчик A в `ApplyEvents` итерирует `connectionMap` (range на :170),
а подписчик B одновременно пишет в ту же map (:142) — Go-runtime детектит
`concurrent map iteration and map write` и делает `fatal error` (неперехватываемо,
не `recover()`-able).

Каждый CommandClient-подписчик ядра получает свой `WriteConnectionEvents`-колбэк
→ если фасад/хост шарит один `*Connections` между подписчиками, гонка
неизбежна при ≥2 активных потребителях.

## Почему всплыло сейчас

До CommandClient-миграции connections-стрим в реальных хостах слушал максимум
один потребитель. С появлением второго (LxBox §168 поднял profilerClient
параллельно screenClient) гонка стала воспроизводимой за ~20с под трафиком
(device CPH2411/Android15).

## Клиентский обход (уже сделан, НЕ заменяет фикс ядра)

LxBox §170 развёл аккумулятор по-клиентно: отдельный `*Connections` на каждого
подписчика (`screenAccumulator` + `profilerAccumulator` в `BoxCommandClient.kt`)
→ две независимые map, горутины не пересекаются. Это убирает краш для ДВУХ
известных потребителей, но:
- не чинит сам класс гонки в ядре;
- третий потребитель `CommandConnections` (или любой хост, шарящий один
  `*Connections`) вернёт `fatal error`;
- дублирует состояние (каждый клиент держит свою копию connectionMap).

## Предлагаемый фикс ядра

Добавить `sync.Mutex` (или `RWMutex`) в `Connections` и обернуть им все методы,
трогающие `connectionMap`/`input`/`filtered`/`filterState`:

```go
type Connections struct {
    access        sync.Mutex
    connectionMap map[string]*Connection
    input         []Connection
    filtered      []Connection
    filterState   int32
    filterApplied bool
}

func (c *Connections) ApplyEvents(events *ConnectionEvents) {
    c.access.Lock()
    defer c.access.Unlock()
    // ... существующее тело ...
}
// аналогично: evictClosedConnections (или звать под уже взятым локом из
// ApplyEvents — оно приватное), FilterState, SortBy*, Iterator-snapshot.
```

Тонкость: `Iterator()` отдаёт наружу срез `filtered` — под локом сделать
**копию** для итератора, чтобы потребитель не держал лок во время обхода и не
читал срез, мутируемый параллельным `ApplyEvents`.

После фикса ядра клиентский обход §170 можно упростить обратно к одному
shared-аккумулятору (необязательно — текущий per-client тоже корректен).

## Проверка

- Race-detector: `go test -race` с двумя горутинами, гоняющими `ApplyEvents`
  и `Iterator()` на одном `*Connections`.
- Интеграция: два CommandClient-подписчика `CommandConnections` под трафиком
  >60с — нет `fatal error: concurrent map`.

## Референс клиентской стороны

LxBox: `docs/spec/tasks/170-connections-accumulator-per-client-race.md`,
`BoxCommandClient.kt` (screenAccumulator/profilerAccumulator).
