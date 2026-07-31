# IMPLEMENTATION_REPORT — 041-WG_HANDSHAKE_GIVEUP_REBIND

## Что сделано

Пассивный self-heal WG/AWG-endpoint'а по событию give-up цикла рукопожатий.
Весь механизм — в форке `wireguard-go` (submodule, local-path replace);
ядро конфигурирует его одной строкой.

| Файл | Изменение |
|---|---|
| `submodules/wireguard-go/device/device.go` | поле `giveUpRebind{enabled,freshPort,last}` (enabled=true по умолчанию, ставится в `NewDevice`); `SetGiveUpRebind(enabled, freshPort)`; `handleHandshakeGiveUp(peer)` — дебаунс CAS-ом по `last` (окно `RekeyAttemptTime`), в горутине: сброс `net.port=0` при `freshPort`, `BindUpdate()`, лог, немедленный `SendHandshakeInitiation(false)` |
| `submodules/wireguard-go/device/timers.go` | +1 вызов `peer.device.handleHandshakeGiveUp(peer)` в give-up ветке `expiredRetransmitHandshake` |
| `transport/wireguard/endpoint.go` | `wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)` после `NewDevice` |
| `submodules/wireguard-go/device/lx_giveup_selfheal_test.go` | новый: harness `gateBind` (blackhole первой генерации сокета, учёт Open/портов) + red/green e2e `TestHandshakeGiveUpSelfHeal`; **не использует новый API** — компилируется на базе |
| `submodules/wireguard-go/device/lx_giveup_rebind_test.go` | новый: юниты fresh port / pinned port / debounce / disabled=апстрим-паритет |

Гарантии SPEC закрыты так:

- **Пассивность** — новых таймеров/горутин в здоровом состоянии нет; вся
  логика висит на существующем таймерном событии give-up (только под спросом
  трафика). Горутина создаётся лишь в момент срабатывания.
- **Сон (SPEC 020)** — у спящего девайса таймеры остановлены, событие
  недостижимо; на down/closed девайсе `BindUpdate` не реоткрывает сокет
  (state machine девайса), rebind вырождается в no-op. Отдельного гейта
  не потребовалось.
- **Оба bind-пути** — общий `device.BindUpdate()`: `StdNetBind` реоткрывает
  сокет (`Open(0)` → новый ephemeral-порт), `ClientBind` закрывает `wireConn`,
  и следующий `connect()` диалит свежий detour-сокет.
- **Пиновый `listen_port`** — ядро передаёт `freshPort=false`, порт сохраняется.

## Приёмка (результаты)

| Критерий | Результат |
|---|---|
| red/green e2e | **RED** на базе `d892107` submodule (fix отстэшен): `TestHandshakeGiveUpSelfHeal` — timeout 20 с. **GREEN** с фиксом: PASS 0.00 с |
| fresh port | PASS: второй `Open` с портом 0 |
| pinned port | PASS: второй `Open` с прежним портом |
| немедленное рукопожатие | закрыто e2e (recovery без ожидания следующего спроса; кик после rebind) |
| дебаунс | PASS: второй give-up в окне — реоткрытий не прибавилось |
| соседи | `SetGiveUpRebind(false)` = апстрим-поведение (PASS); полный `go test ./device/` submodule зелёный; `go test -tags with_gvisor,with_awg ./transport/wireguard/ ./protocol/wireguard/` зелёные |
| гигиена | `gofmt -l` чист (device/, transport/wireguard/), `go vet` зелёный, полная сборка `-tags with_gvisor,with_awg,with_lx_command,with_lx_idle_suspend ./...` зелёная (go1.25.5 darwin) |
| verification grade | **synthetic**: loopback-пара девайсов, blackhole первой генерации сокета. **Остаток — field**: стенд жалобы (Android, WARP AWG, сон устройства) |

## Остаток

- Field-подтверждение на устройстве из жалобы (дождаться следующего цикла
  сна: узлы должны выйти из ERR сами в пределах ~90 с + рукопожатие после
  появления спроса; в логе ядра — `rebound socket after handshake give-up`).
- В релизный тег не входит (указание владельца: релиз не резать). Changelog
  заполняется при подготовке релиза по [[lx-changelog-before-release-tag]].
