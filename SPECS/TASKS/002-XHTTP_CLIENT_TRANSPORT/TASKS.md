# TASKS — 002-XHTTP_CLIENT_TRANSPORT

## v2 — полная клиентская поддержка (ветка `lx-1.14-xhttp-full`)

### Аудит (основание спеки) — ✅ сделано
- [x] Аудит Xray-core `transport/internet/splithttp` + sing-box-extended `transport/v2rayxhttp`/`option`
- [x] Adversarial-верификация каждого из 16 параметров против исходника
- [x] Карта параметров → [PARAM_MAP.md](PARAM_MAP.md) (клиент/сервер, дефолты, on-wire, impl-заметки)
- [x] Классификация: 12 клиентских + 2 tuning-бонуса реализуем; 4 server-only — accept-but-ignore

### Спека (ветка `lx-1.14`) — ✅ сделано
- [x] `SPEC.md` (минимальная) → `SPEC_v1.md` (история сохранена)
- [x] Новая полная `SPEC.md` + `PARAM_MAP.md` — коммит `cafbe546`

### Опции — ✅ сделано
- [x] `option/v2ray_xhttp.go`: +20 полей в `V2RayXHTTPOptions` (placement/key/obfs/tuning + server-only)
- [x] Range-поля строкой `"min-max"` (нет `badoption.Range` в `sing`; решение в SPEC §4.1)
- [x] `option/v2ray_transport.go` уже прокидывает весь `XHTTPOptions` (без новых правок)

### Транспорт — ✅ сделано
- [x] `meta.go`: placement-движок `applyMeta`, `normalizeMeta` (mode-gate'ы, дефолты), uplink-data сборка
- [x] `xpadding.go`: obfs-движок `applyXPadding`, `repeat-x` + `tokenish` (HPACK-Huffman-тюнинг)
- [x] `client.go`: `meta`/`paddingRange`, `NewClient`→`normalizeMeta`, `baseURL`+`newRequest(...)`
- [x] `conn.go`: dial-функции под новую сигнатуру; `packetConn.Write` (разбиение + троттлинг + placement)

### Проверки — ✅ сделано
- [x] `xhttp_test.go` + `url_test.go`: 16 тест-функций (placement/uplink/obfs/tokenish/валидация/регрессия)
- [x] `go test -tags with_xhttp ./transport/v2rayxhttp/` → 16/16 PASS
- [x] `lx-test/config/xhttp_obfs_full.json` + `./sing-box check` → PASS (все 3 xhttp-конфига)
- [x] `make -f Makefile.lx lx-build` → ок; `go vet`/`gofmt` → чисто; tagged/untagged build → ок
- [x] Негатив: без `with_xhttp` → `unknown transport type: xhttp`

### Лайв-верификация
- [x] **Дефолтный путь лайв-подтверждён** на реальных нодах (igareck/vpn-configs): 4 живых XHTTP-сервера,
  packet-up + stream-one(reality), скачивание 1 МБ через IP сервера — закрывает stream-one-TODO из §011
- [ ] **Лайв obfs/placement** против сервера, *настроенного* на obfs (публичные ноды на дефолте — не закрывают)

### Закрытие
- [x] IMPLEMENTATION_REPORT.md (секция v2)
- [ ] Merge ветки `lx-1.14-xhttp-full` → `lx-1.14` (по решению пользователя)

---

## v1 (история) — Complete

### Registry-рефактор (касание upstream) — ✅
- [x] `transport/v2ray/registry.go`: реестр + `RegisterClient` — коммит `e111f800`
- [x] `// lx:` правка `NewClientTransport` — lookup вместо `switch` — коммит `2d97ff56`

### Опции / константа / клиент — ✅
- [x] `V2RayTransportTypeXHTTP="xhttp"` — `2d97ff56`
- [x] lean-native клиент (`client.go`/`conn.go`/`register.go`) — `d1b434fc`
- [x] padding в Referer, sessionId path-layout, stream-one bare-path fix (задача 011) — `5a398a5e`
- [x] Лайв packet-up/auto против Xray/3x-ui (см. IMPLEMENTATION_REPORT v1)
