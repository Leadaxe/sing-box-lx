# TASKS — 002-F-N-XHTTP_CLIENT_TRANSPORT

## Registry-рефактор (касание upstream)
- [ ] `transport/v2ray/registry.go`: `clientRegistry`, `RegisterClient`, `init()` встроенных типов (http/ws/quic/grpc/httpupgrade)
- [ ] `// lx:` правка `NewClientTransport` — lookup вместо `switch`; сохранить `Type==""` → `nil,nil` и текст ошибки
- [ ] Прогнать существующие тесты транспорта — поведение без изменений

## Опции / константа
- [ ] `// lx:` `V2RayTransportTypeXHTTP="xhttp"` в `constant/v2ray.go`
- [ ] `option/v2ray_xhttp.go`: тип `XHTTPOptions` (mode/path/host/headers/padding)
- [ ] `// lx:` поле `XHTTPOptions` в `_V2RayTransportOptions`

## Пакет v2rayxhttp (client)
- [ ] Портировать клиент (референс hiddify, сверка с Xray): `client.go`, `conn.go`, `dialer.go`, `http.go`, `mux.go`, `upload_queue.go`, `writer.go`
- [ ] Режимы `auto`/`packet-up`/`stream-up`/`stream-one`; padding-параметры
- [ ] `transport/v2rayxhttp/register.go` (`//go:build with_xhttp`) — регистрация конструктора
- [ ] `include/v2rayxhttp_stub.go` (`//go:build !with_xhttp`)

## Проверки
- [ ] `test/config/xhttp_reality.json` + `sing-box check`
- [ ] Ручной коннект к Xray XHTTP-серверу (`stream-one`, `packet-up`)
- [ ] Сборка без тега: `xhttp` → `unknown transport type`
- [ ] `go vet ./...`, `go test ./transport/...`

## Закрытие
- [ ] DoD-чеклист
- [ ] IMPLEMENTATION_REPORT.md (+ зафиксировать выбор `mode=auto`)
- [ ] Папка → `C`
