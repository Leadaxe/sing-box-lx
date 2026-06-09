# PLAN: 002 — XHTTP_CLIENT_TRANSPORT

## 1. Архитектура

**Было (upstream):** `transport/v2ray/transport.go::NewClientTransport` — `switch options.Type { case … }`.

**Станет:** реестр конструкторов.
```go
// transport/v2ray/registry.go  (new)
var clientRegistry = map[string]ClientConstructor{}
func RegisterClient(typ string, ctor ClientConstructor) { clientRegistry[typ] = ctor }
```
- Встроенные типы регистрируются в `init()` (в `transport.go` или соседнем файле) — поведение для http/ws/quic/grpc/httpupgrade без изменений.
- `NewClientTransport` → `ctor, ok := clientRegistry[options.Type]`; нет — прежняя ошибка.
- XHTTP-конструктор регистрируется из пакета `v2rayxhttp` через `init()` **только** под `//go:build with_xhttp` (через проводящий файл, чтобы импорт пакета подтягивался лишь с тегом).

Конструктор XHTTP должен соответствовать сигнатуре `ClientConstructor` (см. upstream `transport.go`): `(ctx, dialer, serverAddr, options, tlsConfig) → (adapter.V2RayClientTransport, error)`. Опции достаются из `options.XHTTPOptions`.

## 2. Изменяемые / новые файлы

| Файл | Тип | Изменения |
|------|-----|-----------|
| `transport/v2ray/registry.go` | **new** | `clientRegistry`, `RegisterClient`, `init()` встроенных типов |
| `transport/v2ray/transport.go` | `// lx:` | `NewClientTransport`: `switch` → lookup в реестре (минимальная правка) |
| `transport/v2rayxhttp/*.go` | **new** | Клиент XHTTP: `client.go`, `conn.go`, `dialer.go`, `http.go`, `mux.go`, `upload_queue.go`, `writer.go` |
| `transport/v2rayxhttp/register.go` | **new** | `//go:build with_xhttp` — `init(){ v2ray.RegisterClient(C.V2RayTransportTypeXHTTP, New) }` |
| `constant/v2ray.go` | `// lx:` | `V2RayTransportTypeXHTTP = "xhttp"` |
| `option/v2ray_transport.go` | `// lx:` | одна строка: поле `XHTTPOptions` в `_V2RayTransportOptions` |
| `option/v2ray_xhttp.go` | **new** | тип `XHTTPOptions` (mode, path, host, headers, padding…) |
| `include/v2rayxhttp_stub.go` | **new** | `//go:build !with_xhttp` — понятная ошибка/нет регистрации |
| `test/config/xhttp_*.json` | **new** | Конфиги для `sing-box check` |

## 3. Зона касания upstream (для ребейза)

Только: `transport/v2ray/transport.go`, `constant/v2ray.go`, `option/v2ray_transport.go`. Все — с `// lx:` маркерами, атомарными коммитами. Реестр (`registry.go`) — новый файл, конфликтов не даёт.

## 4. Порядок работ

1. `registry.go` + рефактор `NewClientTransport` (поведение идентично — прогнать существующие тесты транспорта).
2. Константа + опции (`v2ray_xhttp.go` + `// lx:` поле).
3. Пакет `v2rayxhttp` (портировать клиент, сверить с Xray).
4. `register.go` под тегом + `_stub.go` без тега.
5. Конфиги, `sing-box check`, ручной коннект.

## 5. Риски

- **XHTTP — движущаяся цель** в Xray; нужна периодическая сверка параметров (`mode`, padding).
- `mode=auto` в sing-box-портах исторически падает в `packet-up`, что ломало, напр., аплоад в Telegram ([hiddify#2082](https://github.com/hiddify/hiddify-app/issues/2082)) — задокументировать фактический выбор режима.
- Рефактор `switch`→registry должен **точно** сохранить семантику ошибок и nil-обработку (`options.Type == ""` → `nil, nil`).
