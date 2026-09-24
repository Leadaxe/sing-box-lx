# SPEC: 099 — GETURL_NAIVE_NIL_REMOTEADDR_PANIC

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — наш дефект в lx-обработчике `GetURLViaOutbound` (SPEC 058) |
| Статус | I (implemented) — страж red/green в `daemon/started_service_geturl_lx_test.go`, пакет `daemon` под `-race` с `with_lx_command` и без; выпущено в `v1.14.1-lx.10`; подтверждение репортёра (§3 п.3) впереди |
| Ветка | `lx` |
| База | `2ca066c8a` |
| Связано | [058](../058-GET_URL_VIA_OUTBOUND/SPEC.md) (пробник; контракт поля `remoteAddr`), [096](../096-NAIVE_ENGINE_POOL/SPEC.md) (naive; §5 поправлена о darwin) |

Заявка: 4PDA, Sm00keR, Huawei P50 Pro, LxBox 2.25.2 на ядре lx.9; передана стороной приложений 2026-09-24. Соединение через naive-узел живёт сутками, запуск Диагностики этого узла роняет приложение мгновенно, лимит памяти на результат не влияет.

---

## 1. Дефект

При живом туннеле Диагностика идёт не в отдельную пробную сессию, а в RPC `GetURLViaOutbound` живого ядра (разведка стороны приложений). Обработчик снимает адрес установленного соединения в трассе `httptrace.GotConn`:

```go
remoteAddr = info.Conn.RemoteAddr().String()
```

Conn naive-узла — `naiveConn` из `cronet-go`, встраивающий `BidirectionalConn`; у него и `LocalAddr()`, и `RemoteAddr()` возвращают `nil`: Cronet адрес сокета стрима наружу не отдаёт. `String()` на nil-интерфейсе — паника `nil pointer dereference`. gRPC-сервер `daemon` паники не перехватывает, непойманная паника в Go завершает процесс целиком. Это объясняет все признаки заявки: мгновенно, без Java-исключения, независимо от памяти и только у naive — у остальных outbound'ов conn несёт адрес (TCP-сокет, `Socksaddr` обёрток, адрес netstack у WG и MASQUE).

`URLTestOutbound`, донор обработчика, адрес не снимает — поэтому urltest того же узла проходит, а трафик через него живёт.

Платформа роли не играет: тот же код исполняется в `lxd` и на desktop; юнит воспроизводит падение без Cronet, conn'ом с nil-адресами.

**Отклонённая гипотеза** (разведка стороны приложений): `certificate.NewStore` на каждый запрос и переполнение таблицы локальных JNI-ссылок в `common/certificate/system_android.c`. По коду: ссылки цикла (`alias`, `certificate`, `encoded`) удаляются на каждой итерации; в ветках `continue` ссылка уже NULL (JNI возвращает NULL, когда бросает исключение); шесть внешних ссылок освобождает `DetachCurrentThread` в конце вызова; апстримный `libbox.NewHTTPClient` строит такой же store на каждый клиент, и приложения им пользуются без падений. JNI-путь не трогается.

## 2. Решение

Одна проверка в `GotConn`: `info.Conn == nil` → выход; `addr := info.Conn.RemoteAddr()`, `String()` только при `addr != nil`. Nil-адрес даёт пустое поле `remoteAddr` — состояние, которое контракт SPEC 058 §2.2 уже допускает для пробы без соединения. Провод, proto и опции не меняются; статус, тело и `elapsedMs` для naive теперь доезжают.

Не делается: подмена адреса в `protocol/naive` (апстримный файл; адрес назначения не является «адресом соединения», который обещает поле) и обёртка над `cronet-go`.

## 3. Критерии приёмки

1. `TestGetURLViaOutbound_NilRemoteAddrIsNotPanic_LX`: conn с nil `RemoteAddr`/`LocalAddr` → HTTP 200 с телом, `RemoteAddr == ""`, ровно один дайл через узел; паника внутри обработчика = провал теста. Red-check: до правки `runtime error: invalid memory address or nil pointer dereference`. Выполнено.
2. `go test -race ./daemon/` с `with_lx_command` и без; `go vet`; gofmt. Выполнено.
3. Репортёр на ядре lx.10 (через LxBox после бампа пина): Диагностика naive-узла при живом туннеле отдаёт результат; пустое поле адреса — штатно. После этого статус D.

## 4. За чем следить

- `cronet-go`: если `BidirectionalConn` начнёт отдавать адрес, поле заполнится само, проверка остаётся.
- Любой новый потребитель `RemoteAddr()`/`LocalAddr()` conn'а outbound'а в lx-коде обязан допускать nil; naive — известный случай, апстримный `net.Conn` такого не запрещает.
