# IMPLEMENTATION_REPORT — 041-WG_HANDSHAKE_GIVEUP_REBIND

## Что сделано

Пассивный self-heal WG/AWG-endpoint'а: пересоздание UDP-сокета с новым
ephemeral-портом и немедленной повторной инициацией, с тремя триггерами
(give-up ~90 с / досрочный ~15 с по стале-предикату / wake-нудж из libbox)
над общим действием и общим дебаунсом. Весь механизм — в форке `wireguard-go`
(submodule, local-path replace); ядро конфигурирует его одной строкой и
пробрасывает нудж через libbox.

| Файл | Изменение |
|---|---|
| `submodules/wireguard-go/device/lx_giveup_rebind.go` | **весь механизм** (v2 вынес из device.go): `SetGiveUpRebind`; `handleHandshakeGiveUp` → `selfHealRebind("giveup")`; `maybeEarlyGiveUpRebind` (порог `earlyGiveUpMinAttempts=3` + предикат) → `selfHealRebind("early")`; `sessionProvablyDead` (нет keypair ∨ handshake старше `RejectAfterTime`); публичный `RebindIfSessionStale()` (гейты enabled+isUp, обход пиров, `selfHealRebind("nudge", stale...)`); `selfHealRebind(trigger, peers...)` — дебаунс CAS-ом по `last` (окно `RekeyAttemptTime`, общее на все триггеры), в горутине: сброс `net.port=0` при `freshPort`, `BindUpdate()`, лог с меткой триггера, немедленный `SendHandshakeInitiation(false)` каждому переданному пиру |
| `submodules/wireguard-go/device/device.go` | поле `giveUpRebind{enabled,freshPort,last}` + дефолт `enabled=true` в `NewDevice` (методы v1 переехали в lx-файл — меньше строк в апстрим-файле) |
| `submodules/wireguard-go/device/timers.go` | give-up ветка: `handleHandshakeGiveUp` (v1); retry-ветка: lx-блок `maybeEarlyGiveUpRebind` (v2) |
| `transport/wireguard/endpoint.go` | v1: `SetGiveUpRebind(true, e.options.ListenPort == 0)` после `NewDevice`; v2: метод-проброс `RebindIfSessionStale()` (nil-safe при teardown) |
| `adapter/endpoint_rebind_lx.go` | **новый**: интерфейс `StaleRebindable` (нудж без импорта protocol/wireguard) |
| `protocol/wireguard/endpoint_rebind_lx.go` | **новый**: `RebindStale()` — гейты сна под `resumeMu` (`closing`/`!started` отсекают idle-asleep, torn-down, остановленный, закрытый) |
| `experimental/libbox/command_server_rebind_lx.go` | **новый**: `CommandServer.RebindStaleEndpoints()` — зеркало `ResetNetwork`, прямой gomobile-метод без `.proto`; обход endpoint'ов в одной горутине на вызов |
| тесты | `lx_giveup_selfheal_test.go` + `lx_giveup_rebind_test.go` (v1); `lx_early_rebind_test.go` (v2 red/green, без нового API — компилируется на v1-базе); `lx_stale_rebind_test.go` (v2 юниты + гонки); `protocol/wireguard/endpoint_rebind_lx_test.go` (гейты сна нуджа) |

Гарантии SPEC закрыты так:

- **Пассивность** — новых таймеров/горутин в здоровом состоянии нет; триггеры
  1–2 висят на существующих таймерных событиях (только под спросом трафика),
  нудж событийный и оплачивается вызывающим; горутина создаётся лишь в момент
  срабатывания.
- **Сон (SPEC 020)** — у спящего девайса таймеры остановлены (триггеры 1–2
  недостижимы); нудж отсекает спящих на protocol-слое (`!started` под
  `resumeMu`) и на девайсе (`isUp`); на down/closed девайсе `BindUpdate` не
  реоткрывает сокет — гонка вырождается в no-op.
- **Оба bind-пути** — общий `device.BindUpdate()`: `StdNetBind` реоткрывает
  сокет (`Open(0)` → новый ephemeral-порт), `ClientBind` закрывает `wireConn`,
  и следующий `connect()` диалит свежий detour-сокет.
- **Пиновый `listen_port`** — ядро передаёт `freshPort=false`, порт сохраняется
  (проверено и для give-up, и для нуджа).
- **Дребезг** — один rebind на девайс за окно 90 с, общий на все триггеры;
  скользящее окно, не латч.
- **Наблюдаемость** — строка лога `rebound socket for self-heal
  (trigger=giveup|early|nudge, fresh port=...)`.
- **Конфиг не расширяется** — единственная новая поверхность — метод libbox;
  невызванный метод поведения не меняет (гейты и дебаунс те же).

## Приёмка (результаты)

| Критерий | Результат |
|---|---|
| red/green досрочного триггера | **RED** на v1-базе (f007282, до правок): `TestEarlyRebindSelfHeal` — timeout 20 с. **GREEN** с фиксом (в общем прогоне ~1.6 с) |
| fresh-сессия = апстрим-паритет | PASS: `TestEarlyRebindFreshSessionNoop` (живой keypair + свежий handshake, порог пройден — rebind не сработал); `TestEarlyRebindNeedsMinAttempts` (мёртвая сессия, попыток < 3 — не сработал) |
| общий дебаунс, скользящее окно | PASS: `TestSharedDebounceAcrossTriggers` — нудж гасит give-up той же серии; состаренное окно лечится снова |
| нудж: стухший → rebind + инициация | PASS: `TestNudgeRebindsStaleSession` (туннель поднимается e2e без спроса трафика), `TestNudgeExpiredHandshakeIsStale` |
| нудж: здоровый / down / pinned | PASS: `TestNudgeHealthySessionNoop`, `TestNudgeDownDeviceNoop`, `TestNudgePinnedPortPreserved` |
| нудж: сон не тронут (protocol-слой) | PASS: `TestRebindStale_asleepNotWoken` / `_stoppedNoop` / `_closingNoop` / `_nilDeviceSafe` |
| гонки | PASS под `-race`: `TestNudgeRacesClose` (25 итераций), `TestNudgeRacesSuspend` (25 итераций Down/Up, девайс консистентен) |
| нудж не блокирует вызывающего | by construction: `RebindStaleEndpoints` уходит в горутину до обхода; тяжёлая часть rebind — в горутине девайса (`selfHealRebind`) |
| соседи | полный `go test ./device/` submodule зелёный; v1-тесты зелёные |
| гигиена | см. TASKS.md (полная сборка с lx-тегами, gofmt, vet) |
| verification grade | **synthetic**; **field-остаток** — нудж с реального BoxService на стенде жалобы (CPH2411) |

## Остаток

- LxBox-таска wake-nudge: `USER_PRESENT`-ресивер → `RebindStaleEndpoints()`
  (репо LxBox, после выката AAR с этим методом).
- Field-подтверждение: сон → разблокировка → пинг в первые секунды зелёный;
  в логе ядра — `rebound socket for self-heal (trigger=nudge ...)`.
- В релизный тег не входит (указание владельца: релиз не резать). Changelog
  заполняется при подготовке релиза по [[lx-changelog-before-release-tag]].
