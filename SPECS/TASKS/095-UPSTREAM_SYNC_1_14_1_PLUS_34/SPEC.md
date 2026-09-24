# SPEC: 095 — UPSTREAM_SYNC_1_14_1_PLUS_34

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` в `lx` по раннбуку, с re-graft двух форк-сабмодулей |
| Статус | I (implemented) — мерж в дереве, конфликты разобраны, сборка в обоих наборах тегов, vet и тесты как в CI; прогон на эмуляторе по раннбуку §1.4 выполнен 2026-09-24 стороной приложений, регрессий нет (§4) |
| Ветка | `lx` |
| База | до: `a7aec0ab3` (v1.14.1-lx.9, merge-base `1ac1a339c` = v1.14.1); после: `upstream/stable` = `fe401b3f2` (v1.14.1 + 34) |
| Связано | [051](../051-UPSTREAM_MERGE_235/SPEC.md) (прошлый большой синк и его уроки), [082](../082-H2_STREAM_ERROR_TYPE_LEAK/SPEC.md), [047](../047-EARLY_RPC_NIL_ROUTER_CRASH/SPEC.md), [040](../040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md), [094](../094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md) |

Решение владельца 2026-09-24: дрейф устраняется по протоколу до следующего релиза. На момент среза lx.9 `upstream/stable` был впереди на 34 коммита с бампами `sing` v0.9.6, `sing-mux` v0.3.8, `bbolt`, `asc-go` и двух форк-сабмодулей: `wireguard-go` v0.0.7 и `sing-tun` v0.9.6-0.20260923054523-3e03774a6a62.

---

## 1. Форк-сабмодули (раннбук §1, до мержа ядра)

### 1.1 wireguard-go → v0.0.7 (re-graft, ветка `lx-awg2-v007`)

До v0.0.7 апстримной ленте от нашей базы (`c6c8a83`) не хватало трёх коммитов; два из них мы уже несли черри-пиками (`8bd032a` I/O activity callbacks — patch-id совпал; `abd9348` darwin reopen — тот же патч в другом контексте), третий — `7aa7121` «Register the endpoint resolver on the device instead of the peer», смена API, которую требует апстримный фикс ядра `96b004bab` («Fix initial WireGuard handshake for domain peers»). Десять lx-коммитов (AWG2/AWG3 обфускация, SPEC 026, 041, 069, 080, 081) перенесены на v0.0.7 целиком, черри-пики отброшены как уже содержащиеся.

Конфликты: `conn/msgx_darwin.go` (v0.0.7 снял `receiveSingle`, на который ложился гейт SPEC 026 — взята сторона апстрима) и `device/peer.go` (снят `Peer.SetEndpointResolver`, наши `sameDestination`/`noteUDPWindow` оставлены). При первом разрешении гейт `hasReserved()` на darwin-батч-пути потерялся, старое дерево его имело — восстановлен отдельным коммитом `d3a0e26`. `go vet` и тесты `conn`/`device` зелёные.

### 1.2 sing-tun → 3e03774a6a62 (обычный merge)

Пин — осиротевший коммит: апстрим переписал ветку `dev` после того, как sing-box его запинил, в ветках его нет, GitHub отдаёт только по полному SHA. Пять коммитов после v0.9.3, три правят `stack_system.go` (укрепление циклов чтения TUN: `ReadRetry`, выход по неустранимой ошибке, отказ от purge TCP NAT на сбросе сети). Слились без конфликтов, наша дельта к пину — по-прежнему только `stack_system.go` + тест SPEC 040; self-heal-тест зелёный. **Условие снятия SPEC 040 не выполнено**: апстрим укрепил чтение из TUN, а не acceptLoop TCP-форвардера.

gvisor и utls не менялись.

## 2. Мерж ядра: конфликты и как разрешены

| Файл | Апстрим | Наш шов | Решение |
|---|---|---|---|
| `go.mod` / `go.sum` | бампы `asc-go`, `bbolt`, `sing`, `sing-mux`, `sing-tun`, `wireguard-go` | четыре `replace` на форк-сабмодули | `require` апстрима, `replace` наши; `go mod tidy`; `go list -m` по всем четырём → `./submodules/*` |
| `common/interrupt/conn.go` | поле `NetPacketConn`, `Close` по-прежнему под замком (`defer Unlock`) | SPEC 084: снять запись под замком, закрыть после | оставлен SPEC 084, взято переименование поля; регистр HOTFIXES без изменений |
| `dns/client.go` | новая дедупликация: ключ `(cacheKey, timeout)`, `chan`, цикл ожидания, `ExchangeAsync` в режиме ожидания уходит в горутину («Respect DNS query timeouts during deduplication») | наш `exchangePending`/`waitAsync` (асинхронное ожидание) + SPEC 018/035/022 | взят апстримный файл целиком, поверх наложены только аддитивные lx-куски: `transport` в лог-вызовах, `emitFailedQuery` (loopback, rejected, rejected cached, ошибка обмена — в `Exchange` и в `finish` у `ExchangeAsync`), хук группы SPEC 035, fresh-hit в `questionCache` (SPEC 022 #3). Наш `exchangePending` снят: апстримная схема тоже не блокирует вызывающего |
| `experimental/libbox/command_client.go` | обработчики стримов получают `(client, ctx)` («Fix command client cancellation during connection») | `case CommandDNS` (SPEC 018) | `handleDNSStream(client, ctx)` в той же форме |
| `route/network.go` | `startedCtx`/`startedCancel`, колбэк интерфейса регистрируется только в `Start`, `resetRunAccess` («Bind network reset dispatch to manager lifecycle») | `started atomic.Bool` (гонка колбэка до старта) + nil-гейт SPEC 047 в `ResetNetwork` | флаг `started` снят — апстрим убрал причину (колбэка до `Start` больше нет); nil-гейт SPEC 047 и блок SPEC 073 оставлены |

Автослитые файлы со швами проверены счётчиком lx-строк до/после мержа: расхождения только осознанные (см. выше). `transport/wireguard/endpoint.go` получил апстримный `SetEndpointResolverFunc` поверх наших SPEC 020/041/069/070/071 без конфликтов.

## 3. Что апстрим закрыл из нашего

- **SPEC 082, частично.** `sing` v0.9.6 ввёл `baderror.WrapH2`: «stream error:» → непрозрачный `protocolError`, «response body closed» → `net.ErrClosed`; апстрим применяет его в `v2rayhttp/conn.go` и `v2raygrpclite/conn.go` (`18b031701`). Наши `HideStreamError`-обёртки в этих двух апстримных файлах сняты, файлы снова равны апстриму. `protocolError` намеренно оставляет `Unwrap`, но x/net ловит `StreamError` прямым утверждением типа (`err.(StreamError)` в обеих точках `readLoop`), поэтому спина нет; стражи `stream_error_test.go` в обоих пакетах переведены с `errors.As` на проверку конкретного типа и остаются на случай отката апстрима к голой ошибке. В форк-нативном `v2rayxhttp` `HideStreamError` и гейт SPEC 094 остаются: апстрим xhttp не трогает, а наш гейт точнее (таймаут остаётся таймаутом).
- **SPEC 047, часть про гонку.** Флаг `started` больше не нужен; nil-гейт в `ResetNetwork` остаётся (RPC до `Start` по-прежнему возможен).
- **Апстримный аналог SPEC 077** (`9be5fcf4e`, dial-контексты при живых соединениях) касается `httpclient`, `cloudflare`, gRPC-клиентов и `urltest.go`; наш контракт в `v2rayxhttp` он не затрагивает.
- **`96b004bab` Fix initial WireGuard handshake for domain peers** — резолвер теперь регистрируется на устройстве до `IpcSet`; это и есть причина re-graft wireguard-go.

## 4. Проверка

- `go build ./...` без тегов и с полным `LX_TAGS`; `go vet` lx-пакетов и `go test -race` для `lxd`, `cmd/sing-box`, `dns`, `common/interrupt`, `experimental/libbox`, `route`, `transport/v2rayxhttp`, `transport/v2rayhttp`, `transport/v2raygrpclite`, `common/tls` (страж utls), `transport/wireguard`, `protocol/wireguard`, `protocol/group`, `common/dnstrack`; стенды `lx-test`; `lx-build` + `check`.
- Раннбук §1.4: после смены сабмодулей обязателен живой прогон (туннель, DNS, URL-тест, WG/AWG-узлы, несколько раз подряд) — до тега, на эмуляторе стороны приложений. **Выполнено 2026-09-24** (AVD Android 14 arm64, AAR из dry-run `35963586885`, версия в бинаре `lx.10-dryrun`): три цикла по 8 узлам (AWG3.1 и AWG2 с доменными пирами, xhttp REALITY ×2, vless tcp ×2, IP-пир AWG3), `tun0` снимается чисто. Апстримный фикс хендшейка доменных пиров подтверждён: `handshake did not complete after 5 seconds, retrying` у целевых узлов 0 во всех циклах, переключение на доменный пир 1,14 с и urltest 1307 мс против 5,4–5,7 с на lx.8/lx.9. Счётчики SPEC 094 (`response body closed`, `xmux: evicted`, `cause=failing`) — 0 по всем 21 логам. ERROR только стендовые (UDP GSO виртуальной NIC, таймаут Google-push, IPv6 выключен). javap AAR lx.9↔lx.10: одна аддитивная строка `Libbox.hasTunInbound(String)`, удалений нет.

## 5. За чем следить

- `submodules/wireguard-go`: ветка `lx-awg2-v007`; следующий бамп — снова re-graft, не черри-пик (§1.2 раннбука). Гейт `hasReserved()` в `conn/msgx_darwin.go` проверять глазами при каждом переносе.
- `submodules/sing-tun`: пин ядра может снова оказаться осиротевшим коммитом — забирать `git fetch sagernet <полный SHA>`.
- `dns/client.go`: наши вставки только аддитивные; при следующей апстримной перестройке дедупликации переносить их тем же списком из §2.
