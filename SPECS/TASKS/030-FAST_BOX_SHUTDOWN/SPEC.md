# SPEC: 030 — FAST_BOX_SHUTDOWN

**Фича:** [HOTFIXES](../../FEATURES/HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug/perf) — `box.Close()` виснет 10+ секунд на Android при остановке множества (≈30) WG/AWG-endpoint'ов, особенно только что пропинганных |
| Статус | C — фикс реализован, юниты механизма red/green зелёные, e2e-smoke + регрессы зелёные; ждёт field-подтверждения |
| Владелец | Порядок и параллелизм закрытия в `box.Close()` / `endpoint.Manager.Close()` + гейт `closing` в `wireguard`-endpoint. `device.Close`-drain НЕ трогаем |

## 0. TL;DR

Остановка инстанса с ~30 WG/AWG-endpoint'ами зависала на 10+ секунд, потому что
`box.Close()` рвал endpoints, **пока idle/urltest-тик ещё слал wake-пинги**.
Каждый `Endpoint.Close()` блокировался на `resumeMu`, ожидая, пока
пинг-разбуженный `resumeOnDial` доделает **полный rebuild устройства +
handshake** (~0.5–5с), и эти ожидания **суммировались последовательно** по всем
endpoint'ам.

**Фикс (4 шага, drain воркеров НЕ трогается — иначе use-after-free gVisor):**
1. **Квиз тика + закрытие сокетов вперёд** (`box.Close` → `Router.QuiesceForShutdown`): стоп idle-тика (нет новых wake) + `pause.DevicePause()` broadcast → каждый endpoint `device.Down()` закрывает свой UDP-сокет.
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
| `route/reachability_common_lx.go` | 1 | `Router.QuiesceForShutdown()` — `stopIdleSuspend()` (no-op без тега idle-suspend) + `pauseManager.DevicePause()`. В always-compiled файле → есть в любой сборке. |
| `box.go` (`Close`) | 1 | Вызов `s.router.QuiesceForShutdown()` **в самом начале** `Close`, до менеджеров. |
| `protocol/wireguard/endpoint.go` | 2 | Поле `closing atomic.Bool`; ставится в `Close()` **до** `resumeMu.Lock()`; `resumeOnDial` проверяет его на fast-path и под lock, возвращает `false` (не будить). |
| `adapter/endpoint/manager.go` (`Close`) | 4 | Последовательный цикл → `task.Group` с `Concurrency(8)`, `Run` (без FastFail — **все** closes джойнятся, ничего не бросается). |

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
- **e2e-smoke** (`test/wireguard_fast_close_lx_test.go`): 20 живых WG-endpoint'ов
  (мёртвые peer'ы → busy receive-воркеры), `box.Close()` за **~5мс**, без паники
  и дедлока параллельного закрытия. Это smoke (не воспроизводит именно
  resumeMu-гонку — она в юнитах), гейтит hang/deadlock параллели.
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
  `stopIdleSuspend` + `DevicePause` (уже существующие), `closing`-abort
  возвращает `false` = «не будить» — тот же контракт, что «намеренно
  остановлен».
- Глобально безопасно (не только Android): порядок и параллелизм — корректность
  и скорость, семантику не меняют.
- Чистый рестарт на desktop работает (флаг `closing` — per-Close, endpoint
  после Close выбрасывается).

## 8. Ссылки

- [SPEC 020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) — idle-suspend (источник wake-пингов и `resumeMu`)
- [SPEC 029](../029-ENDPOINT_DETOUR_START_ORDER/SPEC.md) — detour-резолв в Start (соседняя правка Close/Start-пути)
- [SPEC 028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) — вложенные туннели (тот же multi-WG-контекст)
