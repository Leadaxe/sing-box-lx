# SPEC: 067 — LXD_BUILD_TAG_SPLIT

**Фича:** [LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (refactor) — расщепление `with_lx_command` на два тега: демон получает собственный `with_lxd`, командные RPC остаются за прежним тегом |
| Статус | C (complete) — в дереве, юниты зелёные, все четыре комбинации тегов собраны, Win7-набор проверен кросс-сборкой |
| Ветка | `lx` |
| Base | `35074cd71` |
| Связанные | [SPEC 015](../015-COMMAND_PROTOCOL_RPC_EXTENSIONS/SPEC.md) (RPC, за которыми остаётся `with_lx_command`), [SPEC 055](../055-LXD_DAEMON_SKELETON/SPEC.md) (демон, переезжающий на `with_lxd`), [FEATURE 014](../../FEATURES/014-LXD_DAEMON/FEATURE.md) |

**Touches:** строки `//go:build` в 46 файлах `lxd/` и двух файлах
`cmd/sing-box/cmd_lxd_lx{,_test}.go`; `LX_TAGS` в
[Makefile.lx](../../../Makefile.lx); наборы тегов в
[lx-ci.yml](../../../.github/workflows/lx-ci.yml) и
[lx-release.yml](../../../.github/workflows/lx-release.yml). **Логика не
меняется ни в одной строке** — правятся только теги сборки и комментарии.

## Why

Один тег `with_lx_command` гейтил две несвязанные вещи:

1. **Расширения командного протокола libbox** (SPEC 015) — `URLTestOutbound`,
   `GetRules`, `GetGroups` в `daemon/` и `experimental/libbox/`. Ими живёт
   LxBox: тест задержки по узлам и экран правил.
2. **Демон `sing-box lxd`** — весь пакет `lxd/` и подкоманда.

Пока они делили тег, собрать одно без другого было нельзя. Владельцу
потребовалась сборка **без демона, но с RPC** — прежде всего для Win7, где
служба Windows не реализована и ротация лога тоже, так что подкоманда
существовала бы без того, что делает её демоном.

Наивное решение — убрать `with_lx_command` из Win7 — **ломает LxBox**: RPC
уходят в стаб `started_service_command_lx_stub.go` и начинают отвечать
`codes.Unimplemented`. Экономия при этом 0.3 МБ из 46 — то есть цена
неверного шага высокая, а выигрыш нулевой.

## Design

### Два тега вместо одного

| Тег | Что гейтит | Где нужен |
|---|---|---|
| `with_lx_command` | RPC SPEC 015 в `daemon/`, `experimental/libbox/` | десктоп, Win7, **AAR** |
| `with_lxd` | пакет `lxd/` + подкоманда `sing-box lxd` | десктоп, серверные сборки |

Теги независимы: `with_lxd` не подразумевает `with_lx_command` и наоборот.
Демон пользуется gRPC-сервисом `daemon.StartedService`, но не его
lx-расширениями — те отвечают на запросы клиента, а не вызываются демоном.

### AAR ничего не теряет

`gomobile` собирает `experimental/libbox`, который `lxd/` не импортирует —
демона в AAR не было и раньше. Проверено:
`go list -deps ./experimental/libbox | grep sing-box/lxd` даёт ноль. Тег
`with_lx_command` в `sharedTags`
([cmd/internal/build_libbox/main.go:88](../../../cmd/internal/build_libbox/main.go))
стоит ради RPC и остаётся нетронутым.

### Win7: минус демон, плюс прежние RPC

В `lx-release.yml` ветка `legacy_win7` теперь снимает **два** тега:
`with_naive_outbound` (у cronet-go нет сборки под windows/386) и `with_lxd`.
`with_lx_command` остаётся — это записано в комментарии рядом, потому что
соблазн снять «всё лишнее» одним движением как раз и есть та ошибка, ради
предотвращения которой заведён отдельный тег.

### Стаб пакета

`lxd/stub.go` за `//go:build !with_lxd` держит пакет собираемым без тега —
`go build ./...` не должен падать на пакете, у которого все файлы под тегом.
Комментарий в нём объясняет, почему тега два, чтобы следующий читатель не
«починил» расхождение обратно.

## Out of scope

- **Переименование `with_lx_command`.** Тег назван по своему содержимому
  (command protocol) и упомянут в CONSTITUTION §3.6 как образец гейтинга
  RPC; переименовывать его — трогать спеки 015/035/037/058 ради
  косметики.
- **Отдельный тег на плоскость наблюдаемости** (SPEC 065) или справочник
  клиентов (SPEC 066). Это части демона, а не самостоятельные поверхности;
  дробить дальше — плодить комбинации, которые никто не собирает.
- **Windows-служба для демона.** Причина исключения Win7 — именно её
  отсутствие, но реализация службы это отдельная задача (см. дорожную карту
  FEATURE 014).

## Acceptance

- [x] С `with_lxd` подкоманда есть: `sing-box lxd --help` печатает описание.
- [x] Без `with_lxd` подкоманда отсутствует: `unknown command "lxd"`.
- [x] Без `with_lxd` пакет `lxd/` **не попадает в зависимости** бинаря
  (`go list -deps ./cmd/sing-box | grep -c sing-box/lxd` = 0; с тегом = 1).
- [x] Без `with_lxd`, но с `with_lx_command` RPC SPEC 015 **живые**: маркер
  стаба `rebuild with -tags with_lx_command` в бинаре отсутствует.
- [x] Win7-набор (`LX_TAGS` минус `with_naive_outbound` минус `with_lxd`)
  собирается под `GOOS=windows GOARCH=386`, `lxd/` в зависимостях нет,
  `with_lx_command` на месте.
- [x] Пакет `lxd/` собирается **без единого тега** (стаб `lxd/stub.go`).
- [x] Юниты зелёные под `-race` с новым набором тегов; `go vet` чист;
  `gofmt` чист.
- [x] AAR: `experimental/libbox` не тянет `lxd/` (было и осталось 0), набор
  тегов гомобильной сборки не изменён.
