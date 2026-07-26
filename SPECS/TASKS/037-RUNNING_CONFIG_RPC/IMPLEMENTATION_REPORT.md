# IMPLEMENTATION_REPORT 037 — GetRunningConfig

**Дата:** 2026-07-26 · **Ветка:** `lx-1.14` (поверх v1.14.0-lx.16-rc.2)

## Что сделано

Unary RPC `GetRunningConfig(Empty) → RunningConfig{content}` — канонический
JSON options, из которых реально построен работающий box (post-override:
tun AutoRedirect/packages, OOM-killer). Снапшот сериализуется один раз в
`newInstance` энкодером `FormatConfig` и хранится строкой в
`Instance.runningConfig`; захват за `with_lx_command`
(`instance_command_lx{,_stub}.go`), безтеговая сборка эквивалентна upstream
по поведению и памяти. Клиентская обёртка —
`CommandClient.GetRunningConfig() (string, error)`.

Решения «per-node JSON в GetOutbounds» и «per-tag RPC» отвергнуты — узловой
JSON клиент извлекает из полного документа (обоснование в PLAN §Решения).

## Верификация

| Проверка | Результат |
|----------|-----------|
| `go test -tags with_lx_command ./daemon/` (round-trip + handler) | ✅ ok |
| `go test ./daemon/` (stub: захват `""`, RPC `Unimplemented`) | ✅ ok |
| `go build ./daemon/... ./experimental/libbox/...` с тегом и без | ✅ |
| Полный бинарь: LX-теги минус `badlinkname`/`with_naive_outbound` (go1.25-хост, известное ограничение линка) | ✅ |
| `check -c lx-test/config/minimal.json` собранным бинарём | ✅ |
| `gofmt -l` по lx-файлам | ✅ пусто |
| `go vet` | только унаследованные upstream-замечания (`unsafe.Pointer` в `managed_service.go`/`debug.go`) |

Регенерация pb: `make lx-proto` из корня **молча не сделал ничего** (root
Makefile не включает `Makefile.lx` — вызывать `make -f Makefile.lx lx-proto`
либо `go run ./cmd/internal/protogen && gofumpt -w .`); после прямого прогона
шум вне SPEC отревёрчен: submodule `wireguard-go` (gofumpt),
`route/rule/rule_item_rule_set_test.go`, косметика соседних generated-файлов
ушла после `gofumpt` (идемпотентность подтверждена).

## Не сделано / хвосты

- Полевая проверка из LxBox — после попадания RPC в AAR-релиз.
- Attached-путь (`service/api`) снапшот не захватывает → `Unavailable`;
  осознанно (см. PLAN §Решения).
