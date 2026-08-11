# TASKS 058 — GetURLViaOutbound

## Этап 1 — провод

- [x] `started_service.proto`: `rpc GetURLViaOutbound` + три сообщения внутри `lx:begin/end lx_command`
- [x] `make -f Makefile.lx lx-proto`, проверить идемпотентность и отревёртить non-SPEC-шум
- [x] Проверить, что сабмодуль `wireguard-go` не загрязнён gofumpt (см. память)

## Этап 2 — ядро

- [x] Handler `GetURLViaOutbound` в `started_service_command_lx.go`:
      резолв тега (outbound → endpoint), кламп maxBytes, клиент на вызов,
      certificate-store под `C.IsAndroid`, httptrace для remoteAddr,
      LimitReader+truncated, Variant B
- [x] Stub в `started_service_command_lx_stub.go`

## Этап 3 — libbox

- [x] `HTTPHeaders` (билдер) + `GetURLResult` (геттеры) + `GetURLViaOutbound`
- [x] Проверить, что ни один метод не возвращает голую строку/слайс (SPEC 038)

## Этап 4 — тесты

- [x] Резолв в обоих менеджерах (outbound и endpoint)
- [x] Не-2xx → `error == ""`, статус/тело/Content-Type заполнены
- [x] Кламп maxBytes: 0 → дефолт; > потолка → потолок; тело > лимита → truncated
- [x] Variant B: неизвестный тег / отказ dial / таймаут → payload-ошибка, `(resp, nil)`
- [x] Не-STARTED → transport-ошибка
- [x] Отмена вызова обрывает висящий фетч
- [x] История urltest не изменилась после вызова
- [x] Stub-тест: `Unimplemented` без тега

## Этап 5 — сборки

- [x] `go build ./daemon/... ./experimental/libbox/...` с тегом и без
- [x] `go vet` + `gofmt` по изменённым файлам
- [x] Полный бинарь с LX-тегами, `check -c lx-test/config/minimal.json`

## Этап 6 — релиз

- [x] Секция в changelog
- [x] Билингвальные релиз-ноты `docs-lx/releases/`
- [x] Обновить статус в SPEC/FEATURE/Roadmap
- [x] Дрейф апстрима проверен по merge-base (единственный subject
      `Fix oomkiller service stub build` уже поглощён — не берётся)
- [ ] Push ветки → тег `v1.14.0-lx.25-rc.1` → проверить `gh run list`

## Полевая проверка (после AAR)

- [ ] `https://1.1.1.1/cdn-cgi/trace` через vless-outbound и через WG-endpoint
- [ ] HTTPS на Android без кастомных корней в конфиге
