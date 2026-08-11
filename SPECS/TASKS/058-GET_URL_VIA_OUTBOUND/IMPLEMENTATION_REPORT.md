# IMPLEMENTATION REPORT 058 — GetURLViaOutbound

**Статус:** C (complete) — код, тесты и сборки зелёные; отгружается в
`v1.14.0-lx.25-rc.1`. Открыт один пункт приёмки: полевая проверка с устройства
после попадания в AAR.

## Что сделано

| Слой | Файл | Содержание |
|------|------|------------|
| Провод | `daemon/started_service.proto` | `rpc GetURLViaOutbound` + `GetURLViaOutboundRequest`/`Response` + `HttpHeaderPair` внутри `lx:begin/end lx_command` |
| Провод | `daemon/started_service*.pb.go` | Регенерация пиненым тулчейном |
| Ядро | `daemon/started_service_command_lx.go` | Handler + константы клампа |
| Ядро | `daemon/started_service_command_lx_stub.go` | `Unimplemented` без тега |
| Клиент | `experimental/libbox/command_client_command_lx.go` | `HTTPHeaders`, `GetURLResult`, `GetURLViaOutbound` |
| Тесты | `daemon/started_service_geturl_lx_test.go` | 8 юнитов контракта |
| Тесты | `daemon/started_service_geturl_lx_stub_test.go` | Stub-эквивалентность |

Контракт реализован ровно как в SPEC: unary, Variant B, не-2xx = результат,
кламп 256 KiB / потолок 1 MiB, `Truncated`, `RemoteAddr`, `ElapsedMs`,
резолв тега в обоих менеджерах, отмена per-call ctx.

## Что выяснилось по ходу

**Встраивание `adapter.Outbound` + `*probeDialer` в тестовый двойник даёт
неоднозначный `DialContext`.** Оба интерфейса несут метод на одной глубине
встраивания, и Go отказывается выбирать. Дайлер в двойниках держится полем,
методы написаны явно — на продовый код не влияет, но следующий, кто будет
писать фейк узла, наступит туда же.

**`go run ./cmd/internal/protogen` без последующего `gofumpt` даёт ложный шум.**
Генератор перестраивает импорты во ВСЕХ `.pb.go` репозитория (включая
`v2rayapi`, `v2raygrpc`, `boxdd`, `managed_service`) и в сабмодуле `gvisor`;
`gofumpt` возвращает их к исходному виду. Полный `make -f Makefile.lx lx-proto`
делает оба шага, но его `gofumpt -w .` лезет в сабмодули — известные грабли.
Рабочий порядок: `protogen`, затем `gofumpt -w` точечно по сгенерированным
`.pb.go`, затем `git checkout` в затронутых сабмодулях. После этого дифф
содержит только целевые файлы.

**`lx-proto-install` требует сети даже при уже установленных плагинах** —
`go install` ходит в sumdb за проверкой. При флапающей сети шаг падает, хотя
`protoc-gen-go v1.36.11` / `protoc-gen-go-grpc 1.5.1` уже стоят и совпадают
с пином Makefile. Обход — запустить `protogen` напрямую, предварительно сверив
версии плагинов с `LX_PROTOC_GEN_*_VERSION`.

## Проверки

- `go test -tags with_lx_command ./daemon/` — зелёный, в том числе под `-race`.
- `go test ./daemon/` (без тега) — зелёный, stub отвечает `Unimplemented`.
- `go build` обеих сборок для `daemon` и `libbox` — зелёный.
- `make -f Makefile.lx lx-build` — полный бинарь с LX-тегами собран;
  `./sing-box check -c lx-test/config/minimal.json` — код 0.
- `gofmt -l` / `gofumpt -l` по lx-owned файлам — пусто.
- Дрейф upstream по merge-base: единственный расходящийся subject
  `Fix oomkiller service stub build` (`9fa673d10`) уже поглощён более поздней
  лентой — наш `service/oomkiller/service_stub.go` несёт и `s.network`,
  и более новую сигнатуру `newAdaptiveTimer(…, s.writeOOMReport)`. Не берётся.
- Сабмодули на своих пинах, рабочие деревья чисты.

## Границы реализации

- gomobile-биндинг проверен по правилам SPEC 038 (возврат — объект, строка
  только геттером), но сам AAR локально не собирался: нужен Android SDK,
  это делает CI.
- Полевая проверка (`cdn-cgi/trace` через WG-endpoint и vless-outbound,
  HTTPS на Android без кастомных корней) остаётся за устройством.
