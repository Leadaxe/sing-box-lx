# TASKS — 002-F-N-XHTTP_CLIENT_TRANSPORT

## Registry-рефактор (касание upstream) — ✅ сделано, запушено
- [x] `transport/v2ray/registry.go`: `clientTransportRegistry`, `RegisterClient`, `lookupClientTransport`, `init()` встроенных типов (http/ws/quic/grpc/httpupgrade) — коммит `e111f800`
- [x] `// lx:` правка `NewClientTransport` — lookup вместо `switch`; сохранены `Type==""`→`nil,nil` и текст ошибки — коммит `2d97ff56`
- [x] Валидация «поведение не изменилось»: build (с тегами/без) + `go vet` зелёные; QUIC TLS-check сохранён. (Юнит-тестов в пакете нет; реальные транспортные тесты — в отдельном `test/` Go-модуле под docker, не прогонялись.)

## Опции / константа
- [x] `// lx:` `V2RayTransportTypeXHTTP="xhttp"` в `constant/v2ray.go` — коммит `2d97ff56`
- [ ] `option/v2ray_xhttp.go`: тип опций XHTTP — **отложено, связано с портом** (схему берём из референса, чтобы не было рассинхрона с транспортом)
- [ ] `// lx:` поле `XHTTPOptions` + 2 case в Marshal/Unmarshal `option/v2ray_transport.go`

## Пакет v2rayxhttp (client) — БОЛЬШОЙ БЛОК, нужен выбор подхода (см. SPEC § 7)
- [ ] Решить подход: (A) вендорить hiddify `common/xray/*` + xhttp-пакет, (B) лёгкий нативный клиент
- [ ] Портировать клиент: `client.go`, `conn.go`, `dialer.go`, `http.go`, `mux.go`, `upload_queue.go`, `writer.go` (+ зависимости `common/xray/{buf,net,pipe,signal,uuid}`)
- [ ] Режимы `auto`/`packet-up`/`stream-up`/`stream-one`; padding-параметры
- [ ] `transport/v2rayxhttp/register.go` (`//go:build with_xhttp`) — `v2ray.RegisterClient(C.V2RayTransportTypeXHTTP, New)`
- [ ] `include/v2rayxhttp_stub.go` (`//go:build !with_xhttp`)

## Проверки
- [ ] `lx-test/config/xhttp_reality.json` + `sing-box check`
- [ ] Ручной коннект к Xray XHTTP-серверу (`stream-one`, `packet-up`)
- [ ] Сборка без тега: `xhttp` → `unknown transport type` (✅ уже так — транспорт не зарегистрирован)
- [ ] `go vet ./...`, `go test ./transport/...`

## Закрытие
- [ ] DoD-чеклист
- [ ] IMPLEMENTATION_REPORT.md (+ зафиксировать выбор `mode=auto` и подход A/B)
- [ ] Папка → `C`
