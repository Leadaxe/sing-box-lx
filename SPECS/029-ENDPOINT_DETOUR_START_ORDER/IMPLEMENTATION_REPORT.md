# IMPLEMENTATION REPORT: 029 — ENDPOINT_DETOUR_START_ORDER

## Итог

WG/AWG-endpoint с `detour: X` был навсегда мёртв, если провайдер `X` объявлен в
`endpoints[]` позже потребителя. Первопричина (доказана инструментированным
stack-trace repro): detour резолвился **в конструкторе `NewEndpoint`** через
egress-anchor-каст (`common.Cast[dialer.UDPListener]` идёт по `Upstream()`, а
`DetourDialer.Upstream()` жадно резолвит), т.е. в фазе `Create` — до сборки
графа. `sync.Once` замораживал `outbound detour not found` навсегда. Смена
порядка в конфиге чинила случайно. Ядро умеет упорядочивать старт по
зависимостям (топосорт `startOutbounds`, detour = `Dependencies()`), но резолв
утекал из-под этого барьера в Create.

## Изменения кода

Один файл — `protocol/wireguard/endpoint.go`:

| Часть | Что |
|---|---|
| A | Каст egress-anchor обёрнут в `if options.Detour == ""` — убирает преждевременный резолв. На detour-пути каст и так всегда давал `false` (egress-pool неприменим); единственный эффект был вредным. |
| B | В `Start`, ветка `StartStateStart`, после `endpoint.Start(false)`: `return dialer.InitializeDetour(w.outboundDialer)` — резолв за топосорт-барьером, где провайдер гарантированно поднят. `not found` = fail-fast, без ретраев. |
| — | Обновлён комментарий поля `outboundDialer`. |

Тест — `test/wireguard_detour_order_lx_test.go` (новый, `with_gvisor &&
with_wireguard`): потребитель объявлен раньше провайдера.

## Верификация

- **Red/green:** тест FAIL без фикса (~92с таймаут, `outbound detour not found`),
  PASS с фиксом (~2.8с). Красно-зелёное доказано откатом фикса через git stash.
- **Регресс:** `go test ./protocol/wireguard/` (SPEC 020 idle-suspend) PASS;
  SPEC 028 `TestAWGOverAWGDetour_LX` PASS вместе с новым тестом (14.4с).
- `gofmt -l` / `go vet` / `go build` с lx-тегами — чисто.

## Метод (почему фикс минимален и безопасен)

Два параллельных read-only аудита по исходникам подтвердили:
- egress-pool структурно неприменим к detour (единственный `UDPListener` —
  `DefaultDialer`, в detour-цепочке не появляется; bind пере-выводится в
  транспорте) → часть A ничего не теряет;
- топосорт `startOutbounds` провабельно стартует провайдера раньше потребителя
  на `StartStateStart` (endpoints влиты, detour = зависимость) → часть B
  резолвит против полного графа;
- `StartStateStarted` был бы поздно (device/receive-воркер поднимаются в
  Start/PostStart) → выбран `StartStateStart`;
- `DetourDialer` один на процесс, `sync.Once` durable через SPEC 020
  suspend/resume.

## Инварианты (соблюдены)

- No-detour endpoint не изменён (каст выполняется при `Detour == ""`). ✔
- Прочие протоколы не затронуты. ✔
- Механизм топосорта не изменён — резолв возвращён под него. ✔
- Реально несуществующий detour — fail-fast в Start (лучше немого кэша). ✔
- SPEC 020 suspend/resume durable. ✔

## Остаток (owed)

- **Field-тест на устройстве:** воспроизвести сломанный порядок (`awg2-home`
  раньше `wg-parnas`) + AWG-over-AWG через `awg2-home`; убедиться, что
  `outbound detour not found` не появляется при любом порядке.
