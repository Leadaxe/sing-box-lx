# PLAN — 041-WG_HANDSHAKE_GIVEUP_REBIND

## Подход

Весь механизм — в форке `wireguard-go` (submodule, local-path replace),
на существующем событии give-up; ядро только конфигурирует его одной строкой.

1. **Хук в give-up.** `device/timers.go`, ветка «attempts > MaxTimerHandshakes»
   в `expiredRetransmitHandshake` — единственная точка срабатывания. Оттуда
   вызывается `device.handleHandshakeGiveUp(peer)`.
2. **Состояние на девайсе.** Три атомика: `enabled` (дефолт **true**),
   `freshPort` (дефолт false), `last` (unix-секунды последнего rebind —
   дебаунс окном `RekeyAttemptTime`, CAS решает гонку многопир-девайса).
3. **Действие.** Отдельная горутина (не держим таймерный колбэк):
   при `freshPort` обнулить `device.net.port`, затем штатный
   `device.BindUpdate()` — он закрывает старый bind (recv-воркеры выходят)
   и на **up**-девайсе открывает новый; на down/закрытом девайсе реоткрытия
   нет by design (гарантия совместимости со сном SPEC 020 — бесплатно, из
   state machine девайса). После успешного rebind — немедленный
   `peer.SendHandshakeInitiation(false)` (кик нового цикла; guard
   `lastSentHandshake < RekeyTimeout` к этому моменту уже истёк, т.к. give-up
   приходит таймером через RekeyTimeout после последней инициации).
4. **Оба bind-пути одним кодом.** `StdNetBind`: `Open(0)` → новый
   ephemeral-порт. `ClientBind` (detour): `BindUpdate` → `Close` (гасит
   `wireConn`) → `Open` (пересоздаёт `done`/`bindCtx`) → следующий `connect()`
   диалит свежий detour-сокет; `net.port` там всегда 0, `freshPort` no-op.
5. **Ядро.** `transport/wireguard/endpoint.go`, после `device.NewDevice`:
   `wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)` — свежий порт
   только когда пользователь не пинил `listen_port`.

## Решения

- **Активный watchdog (тикер живости)** — отвергнут владельцем: фоновая цена
  (таймеры/горутины/трафик в здоровом состоянии). Событие give-up даёт тот же
  сигнал бесплатно и только под спросом трафика.
- **Хук-колбэк из ядра (`onHandshakeGiveUp func()`)** — отвергнут: ядру
  нечего решать в момент события (политика «свежий порт или нет» известна на
  старте), а колбэк тащит через границу submodule замыкание с контекстом
  жизненного цикла. Флаги проще и переживают бамп submodule дешевле.
- **Rebind в `InterfaceUpdated`/wake вместо give-up** — отвергнут: событие
  смены сети при «проснулись в той же Wi-Fi» не приходит вовсе, а wake-путь
  срабатывает и без провала (цена на каждый выход из сна). Give-up — это
  доказанный провал, ложных срабатываний нет.
- **Смена порта безусловно (игнорировать `listen_port`)** — отвергнут:
  пиновый порт — осознанный выбор пользователя (NAT-проброс и т.п.);
  рушить его ради самолечения нельзя. Ограничение задокументировано.
- **Правка `BindUpdate` на «всегда порт 0»** — отвергнут: сломала бы
  апстрим-семантику стабильного listen-порта для всех остальных вызовов
  (`IpcSet`, `device.Up`).
- **Дефолт `enabled=true` в девайсе** (ядро лишь уточняет `freshPort`) —
  осознанно: red/green-тест поведения компилируется и на базовом коммите
  (не ссылается на новый API), а единственный потребитель форка — мы.

## Зона касания

| Файл | Что меняется |
|---|---|
| `submodules/wireguard-go/device/device.go` | +поле `giveUpRebind{enabled,freshPort,last}`, +`SetGiveUpRebind`, +`handleHandshakeGiveUp` |
| `submodules/wireguard-go/device/timers.go` | +1 вызов в ветке give-up `expiredRetransmitHandshake` |
| `submodules/wireguard-go/device/lx_giveup_rebind_test.go` | новый: red/green самовосстановление + юниты (fresh/pinned порт, дебаунс, disable=апстрим-паритет) |
| `transport/wireguard/endpoint.go` | +1 строка конфигурации после `NewDevice` |

Цена мержа: две точки в чужих файлах submodule (стиль `// lx:` как у AWG-графта),
одна — в уже lx-модифицированном `endpoint.go` ядра. Условие снятия — в SPEC.
