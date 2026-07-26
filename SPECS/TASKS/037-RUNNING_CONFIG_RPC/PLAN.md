# PLAN 037 — GetRunningConfig

Класс §3.6 «мост» по образцу SPEC 015: шов в `.proto` под `// lx:`-маркером,
handler за `with_lx_command`, stub-двойник, обёртка в CommandClient.

## Файлы

| Файл | Изменение | Зона |
|------|-----------|------|
| `daemon/started_service.proto` | `rpc GetRunningConfig` + `message RunningConfig` внутри `lx:begin/end lx_command` | lx-шов в upstream-файле |
| `daemon/started_service.pb.go`, `daemon/started_service_grpc.pb.go` | регенерация (`go run ./cmd/internal/protogen` + `gofumpt`) | машинный вывод |
| `daemon/instance.go` | поле `Instance.runningConfig` + вызов `captureRunningConfig(options)` в `newInstance` (две строки с `// lx: SPEC 037`) | lx-шов в upstream-файле |
| `daemon/instance_command_lx.go` | **новый** — `captureRunningConfig`: канонический marshal post-override options (энкодер `FormatConfig`) | lx-only |
| `daemon/instance_command_lx_stub.go` | **новый** — `captureRunningConfig` → `""` без тега | lx-only |
| `daemon/started_service_command_lx.go` | handler `GetRunningConfig` (RLock, FailedPrecondition/Unavailable) | lx-only |
| `daemon/started_service_command_lx_stub.go` | stub `Unimplemented` | lx-only |
| `experimental/libbox/command_client_command_lx.go` | `CommandClient.GetRunningConfig() (string, error)` — строка через gomobile без итератора | lx-only |
| `daemon/instance_command_lx_test.go`, `daemon/instance_command_lx_stub_test.go` | **новые** — юниты обоих вариантов сборки | lx-only |

## Решения

- Захват на старте, не на запросе: ноль работы и ноль вопросов конкурентности
  в handler'е; память — одна строка размера конфига на инстанс, только под тегом.
- Точка захвата — после мутаций options (`OverrideOptions`, OOM-killer),
  до `box.New`: снапшот = ровно то, что запущено.
- Attached-путь (`service/api` → `NewAttachedService`) options не имеет —
  снапшот пуст, RPC отвечает `Unavailable`. Прокидывать options через
  box-контекст ради этого пути — правка upstream `box.go`, дифф не
  оправдан (потребитель — daemon-путь LxBox).
- Per-tag RPC (`GetOutboundConfig(tag)`) отвергнут: выводится на клиенте из
  полного документа; добавим только если полный конфиг окажется неудобен.
- `json` полем в `GroupItem` отвергнуто: сообщение общее со стримами
  `SubscribeGroups`/`SubscribeOutbounds` — либо пустое поле с грязной
  семантикой, либо раздувание каждого push.
