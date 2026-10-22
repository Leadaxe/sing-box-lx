# SPEC: 030 — FAST_BOX_SHUTDOWN

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug/perf) — `box.Close()` виснет 10+ секунд на Android при остановке множества (≈30) WG/AWG-endpoint'ов, особенно только что пропинганных |
| Статус | C — фикс реализован; пре-пасс (шаг 1) восстановлен 2026-10-23 после регресса lx.37 … lx.22, юниты всех четырёх шагов red/green зелёные, стенд зелёный; в поле без жалоб с `v1.14.0-lx.14` |
| Владелец | Порядок и параллелизм закрытия в `box.Close()` / `endpoint.Manager` + гейт `closing` и `QuiesceForShutdown` в `wireguard`-endpoint. `device.Close`-drain НЕ трогаем |

## Состояние на 2026-10-23

Пре-пасс закрытия сокетов (шаг 1) — **явный обход endpoint'ов**, не broadcast
паузы:

- `box.Close` → `Router.QuiesceForShutdown()` → `stopIdleSuspend()` +
  `pauseManager.DevicePause()` (теперь только ради остановки pause-тикеров —
  urltest health-check, idle-тик) + **обход `EndpointManager.Endpoints()`**:
  у каждого endpoint'а, реализующего `adapter.ShutdownQuiescer`
  (`adapter/shutdown_quiesce_lx.go`), вызывается `QuiesceForShutdown()`.
- Протокольный WG/AWG-endpoint (`protocol/wireguard/endpoint_quiesce_lx.go`):
  ставит `closing` (все wake-пути — `resumeOnDial`, `Start`, ожидание
  бюджета сборки — отказываются с этого момента), будит бюджет (SPEC 097) и
  зовёт транспорт.
- Транспортный endpoint (`transport/wireguard/endpoint_quiesce_lx.go`):
  `device.Down()` **асинхронно под `pauseOpAccess` со штампом `pauseSeq`** —
  тот же контракт, что у pause-событий SPEC 071: обход не блокируется на
  Down, повисшем за мёртвым detour; очередное pause-событие не поднимет
  устройство обратно; `Close`, обогнавший квиз, инвалидирует его штампом и
  сделает Down сам. Устройство не закрывается — teardown остаётся за `Close`.

**Почему так.** Исходный пре-пасс опирался на `DevicePause()`: апстримный
`onPauseUpdated` транспортного endpoint'а отвечал на `EventDevicePaused`
`device.Down()`. Апстримный коммит `6364d9a5f` «Fix WireGuard endpoint
stopping on device sleep» (2026-09-08, у нас с `v1.14.0-lx.37`) убрал эту
реакцию намеренно: WG теперь живёт через сон устройства, SPEC 020 на это
рассчитывает, возвращать нельзя. Наш `applyPauseEvent` обрабатывает только
`EventNetworkPause`/`EventNetworkWake`. С lx.37 по `v1.14.3-lx.22` пре-пасс
не закрывал ни одного сокета; каждый `device.Close()` сам ждал
`stopping.Wait()` на своём `ReadFrom`. Ни один тест этого не поймал: стенд
`test/wireguard_fast_close_lx_test.go` лежал в модуле `test/`, который не
собирается (replace главного модуля там не действует), а
`endpoint_pause_dispatch_lx_test.go` проверяет диспетчеризацию через хук,
не Down. Найдено при перечитывании путей закрытия по issue #50.

**Рассмотренный вариант дешевле по мержу — отвергнут.** Вернуть
`EventDevicePaused → Down()` в `applyPauseEvent` (три строки в lx-блоке
upstream-файла) восстановило бы пре-пасс без новых файлов, но вернуло бы и
апстримное поведение «WG гаснет на сне устройства», которое апстрим убрал
осознанно и от отсутствия которого зависит SPEC 020 (гейт `suspended` на
wake). Явный путь стоит четыре новых lx-файла и ноль правок upstream-файлов
сверх уже существующих lx-блоков: мерж-стоимость такая же, семантика
паузы — апстримная.

**Проверка.** `transport/wireguard/endpoint_quiesce_lx_test.go`
(`with_gvisor`, живое устройство с `listen_port` на loopback):
`TestDevicePauseLeavesSocketOpen` пришпиливает CONSTRAINT (после
`DevicePause()` сокет держится), `TestQuiesceForShutdownClosesSocket` — после
`QuiesceForShutdown()` порт освобождается, устройство не закрыто;
`…ThenCloseIsClean`, `…NilDevice`. Временный тест «`DevicePause()` закрывает
сокет» на дереве до правки — красный (`REGRESSION: DevicePause did not close
the WireGuard socket`), это и есть подтверждение регресса.
`route/quiesce_shutdown_lx_test.go` — обход доходит до каждого
`ShutdownQuiescer` ровно один раз, роутер без менеджеров — no-op.
`protocol/wireguard/endpoint_quiesce_lx_test.go` — гейт `closing`, `Start`
после квиза = `os.ErrClosed`, вызов доходит до транспорта. Стенд
`lx-test/startclose/wireguard_fast_close_lx_test.go` (перенесён из `test/`):
20 живых WG, `box.Close` — до правки 0.56–2.5 мс, после 0.8–0.96 мс: на
loopback-стенде с дешёвым Down разница в шуме, стенд гейтит зависание и
дедлок параллельного закрытия, не выигрыш пре-пасса. Выигрыш пре-пасса —
там, где Down дорог или сокетов много: все N Down стартуют параллельно
(а не по 8 в закрывающем пуле) и все N endpoint'ов отмечены `closing` до
того, как закрывающий пул дошёл до них (urltest-пробы живут до закрытия
outbound'ов, которое идёт **после** endpoint'ов).

**CI.** До 2026-10-23 ни `transport/wireguard`, ни `protocol/wireguard`, ни
`route`, ни `lx-test/startclose` не гонялись в CI: апстримный `test.yml`
(`go test ./...`) срабатывает только на ветках апстрима, `lx-ci` эти пакеты
лишь `go vet`'ил. Добавлен шаг `go test (wireguard endpoint, route,
startclose stands)` в job `lint` (`-race` для пакетов, стенды без `-race`,
теги `BASE_TAGS,with_awg,with_lx_idle_suspend`).

Ограничение-наблюдение для апстрим-синка — CONSTRAINT в
[FEATURE 005](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md).

## 0. TL;DR

Остановка инстанса с ~30 WG/AWG-endpoint'ами зависала на 10+ секунд, потому что
`box.Close()` рвал endpoints, **пока idle/urltest-тик ещё слал wake-пинги**.
Каждый `Endpoint.Close()` блокировался на `resumeMu`, ожидая, пока
пинг-разбуженный `resumeOnDial` доделает **полный rebuild устройства +
handshake** (~0.5–5с), и эти ожидания **суммировались последовательно** по всем
endpoint'ам.

**Фикс (4 шага, drain воркеров НЕ трогается — иначе use-after-free gVisor):**
1. **Квиз тика + закрытие сокетов вперёд** (`box.Close` → `Router.QuiesceForShutdown`): стоп idle-тика (нет новых wake), `pause.DevicePause()` (глушит pause-тикеры urltest) и явный обход endpoint'ов: `QuiesceForShutdown()` каждого WG/AWG-endpoint'а ставит `closing` и асинхронно делает `device.Down()` — закрывает UDP/detour-сокет. (Broadcast паузы сокеты не закрывает с lx.37 — см. «Состояние».)
2. **Гейт `closing`** (`wireguard`-endpoint): ставится в `Close()` до `resumeMu`; `resumeOnDial` видит его и **отказывается** будить (не начинает rebuild).
3. **Сокеты закрыты вперёд** (следствие шага 1) → каждый `stopping.Wait()` в `device.Close()` возвращается мгновенно (receive-воркер выходит из `ReadFrom` сразу), а не блокирует.
4. **Параллельное закрытие** (`endpoint.Manager.Close`): `task.Group` c `Concurrency(8)` вместо последовательного цикла — сумма превращается в максимум.

Результат: 10с+ → доли секунды. **Ничего не бросается недозакрытым**: воркеры
выходят сами, netstack освобождается штатно, ключи зануляются, fd закрываются —
просто убрано бессмысленное ожидание пинг-wake.

## 1. Симптом

Android, ~30 WG/AWG-endpoint'ов, только что пропинганы (urltest health-check
разбудил их из idle-suspend, SPEC 020). `stop` виснет 10+ секунд → ANR-риск,
пользователь ждёт. На desktop реже (idle-suspend агрессивнее на Android).

## 2. Первопричина (доказана аудитом + repro-трассировкой)

Доминирующая стоимость — **`resumeMu`-contention с незавершёнными пинг-wake,
просуммированная последовательно**, а НЕ drain воркеров и НЕ фикс-цена teardown:

- `box.Close()` закрывал `{endpoint}` **раньше** `{router}` (порядок в списке
  закрытия). Idle/urltest-тик, который шлёт wake, принадлежит **router** и
  останавливался только на его закрытии (`Router.Close → stopIdleSuspend`).
- Пока endpoint-менеджер шёл своим **строго последовательным** циклом закрытия
  (по одному, `taskmonitor` лишь **логирует** на 5с, не прерывает), группа
  всё ещё звала `resumeOnDial`, который держит `resumeMu` во время
  level-3 rebuild (`Rebuild()` + `Start(false)` + `Start(true)` = новый
  tun-device + gVisor netstack + resolve peer-домена + свежий WG-handshake).
- Каждый `Endpoint.Close()` блокировался на `resumeMu.Lock()`, пока этот wake
  не завершится — ~0.5–1с на endpoint, или полный RekeyTimeout (~5с), если
  первый handshake-пакет потерян на флаки-мобильном линке. Последовательно →
  десятки секунд.
- **Подчинённые факторы:** worker-drain (`stopping.Wait`) — *быстрый*, т.к.
  `Close` закрывает UDP-сокет и netpoller будит `ReadFrom` мгновенно;
  последовательная сумма 30 `device.Close()` — пол в десятки мс, доминирует
  только при чистом стопе без wake.

## 3. Почему drain воркеров НЕЛЬЗЯ дропать (отвергнутый «жёсткий» вариант)

Receive-воркер трогает gVisor-netstack через `unsafe.Pointer`
(`allowedips.Lookup` по trie; `stackDevice.Write` → `DeliverNetworkPacket`).
Если освободить netstack, **не дождавшись** воркера (скип `stopping.Wait`) —
**use-after-free → segfault** на Android (хуже зависания). И это бессмысленно:
drain и так быстрый после закрытия сокета. Поэтому фикс делает teardown
**быстрее по расписанию**, но НЕ пропускает ни одного шага закрытия.

Единственный по-настоящему «убить не глядя» — `stopSelf()` в LxBox (клиент):
OS убивает процесс, отбирает всё разом, use-after-free невозможен. Это
клиентский backstop поверх `box.Close`, не часть этой спеки.

## 4. Фикс (реализован)

| Файл | Шаг | Что |
|---|---|---|
| `route/reachability_common_lx.go` | 1 | `Router.QuiesceForShutdown()` — `stopIdleSuspend()` (no-op без тега idle-suspend) + `pauseManager.DevicePause()` (pause-тикеры) + обход `endpoint.Endpoints()` с вызовом `adapter.ShutdownQuiescer.QuiesceForShutdown()`. В always-compiled файле → есть в любой сборке. |
| `adapter/shutdown_quiesce_lx.go` | 1 | Интерфейс `ShutdownQuiescer { QuiesceForShutdown() }` — контракт «быстро вернуться, запустить освобождение сокета, не ждать». |
| `protocol/wireguard/endpoint_quiesce_lx.go` | 1+2 | `QuiesceForShutdown()`: `closing=true`, `budget.Notify()`, вызов транспорта. |
| `transport/wireguard/endpoint_quiesce_lx.go` | 1 | `QuiesceForShutdown()`: `device.Down()` асинхронно под `pauseOpAccess` со штампом `pauseSeq` (контракт SPEC 071); устройство не закрывается. |
| `box.go` (`Close`) | 1 | Вызов `s.router.QuiesceForShutdown()` **в самом начале** `Close`, до менеджеров. |
| `protocol/wireguard/endpoint.go` | 2 | Поле `closing atomic.Bool`; ставится в `Close()` **до** `resumeMu.Lock()`; `resumeOnDial` проверяет его на fast-path и под lock, возвращает `false` (не будить). |
| `adapter/endpoint/manager_close_lx.go` (с SPEC 117 — per-endpoint scope) | 4 | Последовательный цикл → `task.Group` с `Concurrency(8)`, `Run` (без FastFail — **все** closes джойнятся, ничего не бросается). |

Шаг 3 — следствие шага 1 (сокеты закрыты вперёд), отдельного кода не требует.

**Инварианты teardown не тронуты:** полный `device.Down()`+`device.Close()`
drain, `stopping.Wait()` джойны, зануление ключей (`peer.Stop → ZeroAndFlushAll`),
освобождение fd (`BindClose`, `EgressPool`, `rate.limiter`) — всё выполняется как
раньше. Меняется только **расписание**, не набор шагов → анализ безопасности
держится по построению.

## 5. Верификация

- **Юниты механизма** (`protocol/wireguard/endpoint_fast_close_lx_test.go`,
  `with_lx_idle_suspend`): `resumeOnDial` возвращает `false` при `closing`
  (fast-path и под lock); `Close` ставит `closing`. **Red/green доказан:** без
  правок endpoint.go не компилируются (нет поля `closing`) → с фиксом PASS.
- **Юниты пре-пасса** (шаг 1): `transport/wireguard/endpoint_quiesce_lx_test.go`
  (живое устройство, сокет освобождается после `QuiesceForShutdown`, не после
  `DevicePause`), `route/quiesce_shutdown_lx_test.go` (обход),
  `protocol/wireguard/endpoint_quiesce_lx_test.go` (гейт `closing`).
- **e2e-smoke** (`lx-test/startclose/wireguard_fast_close_lx_test.go`): 20 живых
  WG-endpoint'ов (мёртвые peer'ы → busy receive-воркеры), `box.Close()` за
  **~1 мс**, без паники и дедлока параллельного закрытия. Это smoke (не
  воспроизводит именно resumeMu-гонку — она в юнитах), гейтит hang/deadlock
  параллели. В CI — шаг lx-ci (см. «Состояние»).
- **Регресс:** SPEC 020 idle-suspend юниты (protocol/wireguard + route) PASS;
  SPEC 028 AWG-over-AWG + SPEC 029 detour-order стенды PASS (Close/manager
  тронуты — detour не сломан).
- Обе сборки (`with_lx_idle_suspend` и без) + `gofmt`/`go vet` — чисто.

## 6. Field-план

Debug-API :9269. На устройстве: ~30 WG/AWG-нод, пропинговать (разбудить), затем
`stop` и замерить. До фикса — 10с+ (goroutine-dump покажет стеки в
`Endpoint.Close → resumeMu.Lock` за `resumeOnDial`-rebuild). После — доли
секунды, стеки в `resumeMu.Lock` при close отсутствуют. Дополняет клиентский
таймаут-обёртку LxBox вокруг `closeService()` (тот разблокирует UI, этот
ускоряет сам Go-Close, чтобы он не жёг CPU/батарею в фоне).

## 7. Инварианты (не сломать)

- Ни один шаг teardown не пропускается (нет use-after-free netstack, ключи
  зануляются, fd закрываются).
- Параллельное закрытие **джойнит все** горутины перед возвратом (Run без
  FastFail); concurrency ограничен (память под gVisor-teardown).
- SPEC 020 suspend/resume цел: `QuiesceForShutdown` переиспользует
  `stopIdleSuspend` + `DevicePause` (уже существующие) и не трогает реакцию
  транспорта на паузу (WG живёт через сон устройства — апстрим `6364d9a5f`);
  `closing`-abort возвращает `false` = «не будить» — тот же контракт, что
  «намеренно остановлен».
- Пре-пасс не блокирует `box.Close`: `QuiesceForShutdown` endpoint'а
  возвращается сразу, Down идёт асинхронно под `pauseOpAccess`; `Close`
  делает Down сам, если квиз не успел.
- Глобально безопасно (не только Android): порядок и параллелизм — корректность
  и скорость, семантику не меняют.
- Чистый рестарт на desktop работает (флаг `closing` — per-Close, endpoint
  после Close выбрасывается).

## 8. Ссылки

- [SPEC 020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) — idle-suspend (источник wake-пингов и `resumeMu`)
- [SPEC 029](../029-ENDPOINT_DETOUR_START_ORDER/SPEC.md) — detour-резолв в Start (соседняя правка Close/Start-пути)
- [SPEC 028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) — вложенные туннели (тот же multi-WG-контекст)
- [SPEC 071](../071-WG_BIND_DIAL_PAUSE_DEADLOCK/SPEC.md) — асинхронная диспетчеризация pause-событий под `pauseOpAccess`/`pauseSeq`, которую переиспользует `QuiesceForShutdown`
- [FEATURE 005](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md) — CONSTRAINT: апстрим `6364d9a5f` снял реакцию WG на `DevicePause`
