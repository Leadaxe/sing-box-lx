# PLAN — 041-WG_HANDSHAKE_GIVEUP_REBIND

## Подход

Весь механизм — в форке `wireguard-go` (submodule, local-path replace):
одно действие `selfHealRebind(trigger, peers...)` и три триггера над общим
состоянием `giveUpRebind{enabled,freshPort,last}` девайса. Ядро конфигурирует
его одной строкой и пробрасывает нудж через libbox.

1. **Общее действие.** Отдельная горутина (не держим ни таймерный колбэк, ни
   вызывающего нуджа): при `freshPort` обнулить `device.net.port`, затем
   штатный `device.BindUpdate()` — он закрывает старый bind (recv-воркеры
   выходят) и на **up**-девайсе открывает новый; на down/закрытом девайсе
   реоткрытия нет by design (гарантия совместимости со сном SPEC 020 —
   бесплатно, из state machine девайса). После успешного rebind — немедленный
   `SendHandshakeInitiation(false)` каждому переданному пиру. Дебаунс — CAS по
   `last`, окно `RekeyAttemptTime` (90 с), **общий на все триггеры**: досрочный
   rebind гасит штатный give-up-rebind той же серии.
2. **Триггер give-up (v1).** `device/timers.go`, ветка
   «attempts > MaxTimerHandshakes» в `expiredRetransmitHandshake` →
   `handleHandshakeGiveUp(peer)` → `selfHealRebind("giveup", peer)`.
3. **Триггер early (v2).** Retry-ветка того же таймера →
   `maybeEarlyGiveUpRebind(peer)`: неотвеченных инициаций ≥ 3
   (`earlyGiveUpMinAttempts`) **и** `sessionProvablyDead(peer)` →
   `selfHealRebind("early", peer)`. Цикл ретраев продолжается штатно.
4. **Стале-предикат.** `sessionProvablyDead`: нет текущего keypair **либо**
   `lastHandshakeNano` старше `RejectAfterTime` (180 с — ключи невалидны,
   терять нечего). Живая сессия с временной потерей пакетов предикат не
   проходит — retry-ветка для неё байт-в-байт апстримная.
5. **Триггер nudge (v2).** Публичный `Device.RebindIfSessionStale() bool`:
   гейты enabled+isUp, обход пиров под RLock, стухшим —
   `selfHealRebind("nudge", stale...)`. Проброс в ядре:
   `transport/wireguard.Endpoint.RebindIfSessionStale()` (nil-safe при
   teardown) → `protocol/wireguard.Endpoint.RebindStale()` (гейты сна под
   `resumeMu`: `closing`/`!started` отсекают idle-asleep, torn-down,
   остановленный и закрытый endpoint) → `adapter.StaleRebindable` →
   `libbox CommandServer.RebindStaleEndpoints()` (зеркало `ResetNetwork`,
   прямой gomobile-метод без `.proto`; обход endpoint'ов в одной горутине на
   вызов — gomobile-поток освобождается сразу).
6. **Ядро-конфигурация.** `transport/wireguard/endpoint.go`, после
   `device.NewDevice`: `wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)`
   — свежий порт только когда пользователь не пинил `listen_port`.

## Решения

- **Активный watchdog (тикер живости)** — отвергнут владельцем: фоновая цена
  (таймеры/горутины/трафик в здоровом состоянии). Give-up/retry дают сигнал
  бесплатно и только под спросом трафика; нудж событийный и оплачивается
  вызывающим.
- **App-side лестница (LxBox §088) вместо ядрового нуджа** — отклонена
  повторно: kernel-вариант покрывает не-Android сборки, работает при убитом
  UI-процессе и не требует gate-проб; §088 остаётся замороженным, нудж
  реализует только его entry-событие.
- **Хук-колбэк из ядра (`onHandshakeGiveUp func()`)** — отвергнут: ядру
  нечего решать в момент события, а колбэк тащит через границу submodule
  замыкание с контекстом жизненного цикла. Флаги проще и переживают бамп
  submodule дешевле.
- **Rebind в `InterfaceUpdated`/wake безусловно** — отвергнут: событие смены
  сети при «проснулись в той же Wi-Fi» не приходит вовсе, а безусловный
  wake-rebind срабатывал бы и без провала (цена на каждый выход из сна).
  Нудж лечит только по доказательству (стале-предикат), give-up — по
  доказанному провалу; ложных срабатываний нет.
- **Смена порта безусловно (игнорировать `listen_port`)** — отвергнут:
  пиновый порт — осознанный выбор пользователя (NAT-проброс и т.п.).
  Ограничение задокументировано.
- **Правка `BindUpdate` на «всегда порт 0»** — отвергнут: сломала бы
  апстрим-семантику стабильного listen-порта для остальных вызовов.
- **Порог early = 3 неотвеченных инициации** — компромисс: LTE-яма в 1-2
  ретрая не тревожит сокет (и всё равно отсечена предикатом), а окно ERR
  сжимается до ~15-20 с.
- **Нудж через RPC (`.proto`)** — не нужен: прямой gomobile-метод на
  `CommandServer`, как upstream `ResetNetwork()`; ни аргументов, ни
  возвращаемых значений (см. [[gomobile-string-return-packed-frame-kill]] —
  без string-возвратов).
- **Дефолт `enabled=true` в девайсе** (ядро лишь уточняет `freshPort`) —
  осознанно: red/green-тесты поведения компилируются и на базовом коммите,
  а единственный потребитель форка — мы.

## Зона касания

| Файл | Что меняется |
|---|---|
| `submodules/wireguard-go/device/lx_giveup_rebind.go` | **новый lx-файл — весь механизм**: `SetGiveUpRebind`, `handleHandshakeGiveUp`, `maybeEarlyGiveUpRebind`, `sessionProvablyDead`, `RebindIfSessionStale`, `selfHealRebind` |
| `submodules/wireguard-go/device/device.go` | поле `giveUpRebind{enabled,freshPort,last}` + дефолт `enabled=true` в `NewDevice` (v1; v2 вынес методы в lx-файл) |
| `submodules/wireguard-go/device/timers.go` | give-up ветка: вызов `handleHandshakeGiveUp` (v1); retry-ветка: lx-блок `maybeEarlyGiveUpRebind` (v2) |
| `submodules/wireguard-go/device/lx_giveup_selfheal_test.go` | v1 red/green harness (`gateBind`) + e2e give-up |
| `submodules/wireguard-go/device/lx_giveup_rebind_test.go` | v1 юниты: fresh/pinned порт, дебаунс, disabled=апстрим-паритет |
| `submodules/wireguard-go/device/lx_early_rebind_test.go` | v2 red/green e2e досрочного триггера (без нового API — компилируется на v1-базе) |
| `submodules/wireguard-go/device/lx_stale_rebind_test.go` | v2 юниты: нудж (stale/healthy/expired/down/pinned), общий дебаунс, порог попыток, fresh-сессия = апстрим, гонки нуджа с Close/suspend |
| `transport/wireguard/endpoint.go` | v1: строка `SetGiveUpRebind` после `NewDevice`; v2: метод-проброс `RebindIfSessionStale` (nil-safe) |
| `adapter/endpoint_rebind_lx.go` | **новый**: интерфейс `StaleRebindable` |
| `protocol/wireguard/endpoint_rebind_lx.go` | **новый**: `RebindStale()` с гейтами сна |
| `protocol/wireguard/endpoint_rebind_lx_test.go` | **новый**: гейты сна нуджа на харнесе SPEC 020 |
| `experimental/libbox/command_server_rebind_lx.go` | **новый**: `CommandServer.RebindStaleEndpoints()` |

Цена мержа: две точки в чужих файлах submodule (стиль `// lx:` как у
AWG-графта), одна строка + один метод в уже lx-модифицированном
`transport/wireguard/endpoint.go`; остальное — отдельные lx-файлы (0 строк в
общих файлах). Условие снятия — в SPEC.
