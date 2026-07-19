# IMPLEMENTATION REPORT: 030 — FAST_BOX_SHUTDOWN

## Итог

`box.Close()` виснул 10с+ на Android при остановке ~30 пропинганных
WG/AWG-endpoint'ов. Доминирующая причина (аудит 3 агентов + синтез): endpoints
закрывались **последовательно, пока idle/urltest-тик ещё слал wake-пинги** —
каждый `Endpoint.Close()` блокировался на `resumeMu`, ожидая, пока
пинг-разбуженный `resumeOnDial` доделает полный device-rebuild + handshake
(~0.5–5с), суммируясь по всем нодам. Фикс ускоряет **расписание** закрытия, не
пропуская ни одного шага teardown (drain воркеров дропать нельзя —
use-after-free gVisor netstack).

## Изменения кода

| Файл | Что |
|---|---|
| `route/reachability_common_lx.go` | `Router.QuiesceForShutdown()` — стоп idle-тика + `pauseManager.DevicePause()` broadcast (закрывает все UDP-сокеты через `onPauseUpdated`→`device.Down()`). Always-compiled → в любой сборке. |
| `box.go` (`Close`) | `s.router.QuiesceForShutdown()` в самом начале `Close`, до менеджеров. |
| `protocol/wireguard/endpoint.go` | `closing atomic.Bool`; ставится в `Close` до `resumeMu`; `resumeOnDial` возвращает `false` при `closing` (fast-path + под lock) — in-flight wake не начинает rebuild. |
| `adapter/endpoint/manager.go` (`Close`) | последовательный цикл → `task.Group` Concurrency(8), `Run` без FastFail (все closes джойнятся). Убраны неиспользуемые импорты taskmonitor/constant. |
| `protocol/wireguard/endpoint_fast_close_lx_test.go` | юниты гейта `closing` (red/green). |
| `test/wireguard_fast_close_lx_test.go` | e2e-smoke: 20 нод, замер `box.Close`. |

## Верификация

- **Юниты механизма:** `resumeOnDial` → false при `closing` (обе ветки), `Close`
  ставит `closing`. Red/green доказан: без правок endpoint.go тест не
  компилируется (нет поля `closing`), с фиксом — 3/3 PASS.
- **e2e-smoke:** 20 живых WG-нод, `box.Close` за ~5мс, без паники/дедлона
  параллельного закрытия.
- **Регресс:** SPEC 020 idle-suspend юниты PASS; SPEC 028 AWG-over-AWG + SPEC 029
  detour-order стенды PASS (Close/manager тронуты — detour цел).
- Обе сборки (`with_lx_idle_suspend` on/off) exit 0; `gofmt -l`/`go vet` чисто.

## Метод (почему безопасно)

Воркфлоу из 3 read-only аудитов + синтеза установил на исходниках:
- worker-drain (`stopping.Wait`) уже быстрый (сокет закрыт → `ReadFrom` будится
  мгновенно) → доминанта именно `resumeMu`×wake-сумма;
- дроп drain = use-after-free (воркер трогает netstack через unsafe.Pointer) →
  отвергнут;
- `pause.DevicePause` уже гоняет `device.Down()` на всех нодах (механизм
  `onPauseUpdated`), box держит pause-manager → шаг 1 реюзит готовое;
- `task.Group` (`sing/common/task`) — in-repo примитив, `Run` джойнит всё.

## Инварианты (соблюдены)

- Ни один шаг teardown не пропущен (netstack, ключи, fd). ✔
- Параллель джойнит все closes перед возвратом (нет abandon). ✔
- SPEC 020 suspend/resume цел (реюз stopIdleSuspend/DevicePause; closing-abort =
  «не будить»). ✔
- Глобально безопасно, не только Android; чистый рестарт на desktop работает. ✔

## Остаток (owed)

- **Field-тест на устройстве:** ~30 WG/AWG-нод, пинг → stop → замер (было 10с+,
  ожидается доли секунды); goroutine-dump подтверждает отсутствие стеков в
  `resumeMu.Lock` при close. Дополняет клиентский таймаут LxBox вокруг
  `closeService()`.
