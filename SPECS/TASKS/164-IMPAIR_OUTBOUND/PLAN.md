# PLAN 164 — тестовый outbound `impair`

**Фича:** [IMPAIR](../../FEATURES/020-IMPAIR/FEATURE.md) · [SPEC](SPEC.md)

## 1. Файлы

| Файл | Что в нём |
|---|---|
| `constant/impair_lx.go` | `TypeImpair = "impair"` |
| `option/impair_lx.go` | схема JSON: `ImpairOutboundOptions` (встраивает `DialerOptions` и `ImpairLinkOptions`), `ImpairLinkOptions`, `ImpairVaryOptions`, `ImpairStallOptions`, `ImpairScheduleEntry`. Типы лежат в `option`, чтобы заглушка без тега регистрировала тип, не импортируя пакет outbound |
| `protocol/impair/outbound.go` | адаптер: `RegisterOutbound`, `New`, `DialContext`, `ListenPacket`, `Close` |
| `protocol/impair/option.go` | короткие псевдонимы типов из `option`; сведение слоёв (умолчания, профиль, верхний уровень, `up`/`down`) и расписания в `config`; профили |
| `protocol/impair/units.go` | разбор скорости, длительности, процента, размера очереди с диапазонами |
| `protocol/impair/model.go` | модель линии: очередь, время передачи, задержка с разбросом, потери с сериями, скачки скорости, остановки, расписание, счётчики. Чистая логика на подставляемых часах |
| `protocol/impair/pump.go` | доставка датаграмм по времени: куча по сроку, один исполнитель на направление, один переиспользуемый таймер, пачки по 250 мкс, пул буферов |
| `protocol/impair/packet.go` | обёртки `net.PacketConn` и UDP-`net.Conn` |
| `protocol/impair/stream.go` | обёртка потока: очередь кусков до 4 КБ, обратное давление на `Write`, остановка чтения, остановки потока по потерям |
| `protocol/impair/shaper.go` | общее узкое место экземпляра: модель, исполнители, реестр соединений для `reset`, ожидание обрыва перед дозвоном |
| `include/lx_impair.go` | `//go:build with_lx_impair`: `registerImpairOutbound` вызывает `impair.RegisterOutbound` |
| `include/lx_impair_stub.go` | `//go:build !with_lx_impair`: регистрирует тип с ошибкой «rebuild with -tags with_lx_impair» |
| `include/registry.go` | вставка `registerImpairOutbound(registry)` за `// lx:begin impair` |

## 2. Тесты

- `model_test.go`, `bench_test.go` — внутренний пакет, без тега: модель на подставных часах
  (очередь, задержка, разброс и порядок, потери и серии, скачки, расписание, `first_use`, остановки,
  остановки потоков, обрыв, профили, ошибки проверки) и бенчмарки горячего пути.
- `integration_test.go` — `//go:build with_lx_impair`, внешний пакет `impair_test`: поднимает полные
  экземпляры sing-box через `include.Context` на loopback с настоящими сокетами и часами. Внешний
  пакет нужен потому, что `include` под тегом импортирует `protocol/impair`, а внутренний тест,
  импортирующий `include`, дал бы цикл импорта.
- `export_test.go` — `DownStats` отдаёт интеграционному тесту счётчики приёма.

## 3. Коммиты

Каждый коммит собирается сам по себе.

1. Новые файлы: `constant/impair_lx.go`, `option/impair_lx.go`, пакет `protocol/impair/`.
2. Проводка тега: `include/lx_impair.go`, `include/lx_impair_stub.go` (новые файлы; функция
   регистрации пока не вызывается).
3. Апстримный файл: вставка в `include/registry.go`.
4. Документация: `docs-lx/lx-impair{,.ru}.md`, FEATURE 020, задача 164, индексы, changelog.
