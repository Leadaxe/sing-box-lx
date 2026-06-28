# 020 — Множество WG/AWG-узлов греют телефон: idle-устройства держат буферы → scan-bound GC

| Поле | Значение |
|------|----------|
| Тип | B (bug) — расследование завершено, фикс спроектирован (диагноз держателя пересмотрен) |
| Статус | **O (open)** — нагрев доказан heap/CPU-профилем на устройстве. Воркэраунд («убрать лишние WG из конфига») подтверждён Ильёй. Код-фикс НЕ реализован. Точная атрибуция держателя (sync.Pool victim-cache vs device-каналы) — по коду, **confidence medium**: on-device A/B НЕВОЗМОЖЕН, два держателя эмпирически неразличимы → фикс покрывает оба. |
| Зона | submodule `wireguard-go` (`device/` — drain device-каналов) + ядро `sing-box-lx` (`transport/wireguard/device_stack.go` — suspended-seam, `transport/wireguard/endpoint.go` — Suspend/Resume, `protocol/group/awg_selector_guard.go` — один GC на переход) |
| Связь | [010-WG_ENDPOINT_GRO_SPLIT_BRAIN](../010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (GRO нужен 65535-буфер — НЕ трогать размер), [007-AWG_OVER_WIREGUARD_DETOUR_GUARD](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) (Suspend() = точка приглушения), memory `android-cpu-heat-multi-wg-gc` |
| Репортер | Iliya, 2026-06-28 (CPH2411 / OnePlus, Adreno 642L, Android) |

---

## Симптом

На Android новое 1.14-ядро (LxBox 2.5.0+) держит все ядра CPU на максимуме и греет
телефон. VPN внешне работает нормально. Морда закрыта — всё равно греет. Сначала
казалось типонезависимым (VLESS/Trojan/WG). «Не у всех устройств». Освобождение
RAM **не помогает**.

---

## Корень (доказан heap + CPU pprof, §207-механизм)

Греет **фоновый сборщик мусора Go**, сканирующий большую pointer-плотную живую кучу.

- **CPU-профиль (горячий, 10с):** `runtime.scanobject` 52.65%, `gcBgMarkWorker`
  доминирует, 350% CPU (~3.5 ядра), ~1516 GC-циклов / 10с. GC **scan-bound**
  (mallocgc лишь 9%). `markroot`/stack-scan минимален — дело НЕ в горутинных стеках.
- **heap inuse_space:** **224 МБ (92.75%) из 242 МБ** атрибутированы профилем на
  `wireguard-go device.(*Device).PopulatePools` — буферы `[65535]byte` × ~10
  WG-устройств. ⚠️ **Атрибуция = место аллокации, НЕ держатель** (см. ниже).
- **A/B на устройстве:** селектор с ~10 WG/WARP → 479 горутин + нагрев; убрать WG
  (только VLESS/Trojan) → 70 горутин, 0 WG Device, норма. **Убрать лишние WG из
  конфига → нагрев уходит** (подтверждено Ильёй).

### Конфиг, на котором воспроизводится

`endpoints`: **10 WG/AWG** (WireGuard-1, WARP, QUIC-google, WireGuard-kiberportal,
WireGuardStatic, WireGuardStun, WARP-AWG-STUN/SIP/QUICK/DNS). Все перечислены в 4
группах: `vpn-1` (selector), `vpn-1-auto` (urltest), `vpn-2`, `vpn-2-auto`. Активен
1, остальные приглушены через awg-detour-guard.

### Механизм удержания (диагноз ПЕРЕСМОТРЕН — что держит 224 МБ)

> ⚠️ **Прежняя версия SPEC указывала на `bufsArrs = make([]*[65535]byte, maxBatchSize)`
> с `maxBatchSize=128` (= 8 МБ/устройство). Это НЕВЕРНО для нашего форка.**
> На клиентском пути `BatchSize()=1` во всех трёх точках:
> `ClientBind.BatchSize()` (`client_bind.go:201`), `stackDevice.BatchSize()`
> (`device_stack.go:267`, режим Ильи — gVisor netstack), `systemDevice.BatchSize()`
> (`device_system.go:191`). `device.BatchSize()=max(1,1)=1`, значит
> `bufsArrs` (`receive.go:89`) = **1 элемент ≈ 64 КБ/устройство, не 8 МБ**.
> Рычаг «SLIM batch 128→4» сэкономил бы ~ноль.

1. `endpoints` стартуют **eager** все на старте инстанса (upstream-механизм
   `adapter/endpoint/manager.go:55`, одинаков в 1.13/1.14).
2. `PopulatePools` (`device/pools.go:49`) создаёт 5 `WaitPool`. `WaitPool` оборачивает
   `sync.Pool` с ленивой `New` — **буферы НЕ префиллятся**, создаются на `Get()`
   (`pools.go:20-36`). `max=PreallocatedBuffersPerPool=4096` — это **семафор
   in-flight** (потолок одновременных Get без Put), НЕ размер удержания.
3. **pprof соврал про держателя.** Метка `PopulatePools.func*` = это
   `sync.Pool.New` alloc-SITE (`pools.go:58-60`), а не тот, кто держит. 64 КБ-буферы
   `new([65535]byte)` — **noscan**, GC их нутро не сканирует.
4. **Настоящие держатели 64 КБ-буферов (оба кандидата, A/B недоступен):**
   - **(A) `device.pool.messageBuffers` sync.Pool** — local + victim cache держат
     буферы с пика нагрузки, пока `*Device` жив. `Suspend()` device НЕ закрывает →
     victim cache переживает обычный GC.
   - **(B) буферизованные device-каналы** `device.queue.decryption.c` /
     `encryption.c` / `handshake.c` (cap `QueueInbound/Outbound/HandshakeSize=1024`,
     `channels.go:28/46/64`, `device.go:339-341`) + per-peer
     `peer.queue.inbound`(1024)/`staged`(128).
5. **Почему scanobject 52%, хотя `[65535]byte` это noscan:** жгут pointer-плотные
   спутники — `QueueInbound/OutboundElement` (поля `buffer`/`packet`/`keypair`/
   `endpoint`, `receive.go:28-34`) и `*ElementsContainer`-слайсы. **4 из 5 пулов —
   scan-типа**, только `messageBuffers` noscan.
6. **Пробел в Suspend.** Текущий `Suspend()=Down()=downLocked()` (`device.go:223-235`)
   делает ТОЛЬКО `BindClose()` + `peer.Stop()`. `peer.Stop` (`peer.go:262-280`)
   дренирует только per-peer `staged` (`FlushStagedPackets`) + шлёт nil в
   `peer.queue.inbound/outbound`. **НИЧЕГО не дренирует 3 device-level канала и НЕ
   трогает `device.pool.*`.**

### Почему совпало со всем

1.14 (re-graft WG submodule на v0.0.3 + `MaxSegmentSize` 2200→65535 ради GRO) ·
память не лечит (это scan живой Go-кучи) · не у всех (только конфиги с многими WG +
устройства, где трафик активен) · типонезависимо (gVisor netstack/WG-воркеры общие).

### Ложные следы (НЕ трогать)

- **`MaxSegmentSize` 65535 → 2200** — буфер нужен для UDP_GRO (см. §010,
  upstream-родной фикс в 1.14). Откат задушит download-скорость.
- **`bufsArrs` / `maxBatchSize`** — на клиенте уже `1` (≈64 КБ/устройство). Резать
  нечего, это no-op. См. пересмотр выше.
- **`runtime.debug.FreeOSMemory` как АВТО-источник нагрева** — исключён профилем
  (0.085%, oomkiller не активен на Android). НО: фикс **сознательно** зовёт
  `FreeOSMemory()` ОДИН раз на переход селектора, чтобы форсировать возврат RSS после
  дренажа. Это редкий, debounced вызов — НЕ hot-path. (Авто-GC на этом Android-билде
  страницы victim-cache сам не возвращает.)
- **Swap `device.pool.*` (пересоздать пул через `PopulatePools()`)** — ОТВЕРГНУТ:
  даёт underflow `WaitPool.count` (`pools.go:29`) → вечный `cond.Wait` deadlock, если
  хоть один писатель пула жив (а он жив — см. «Гонки»). Дренаж+GC даёт тот же возврат
  RSS без этой мины.

---

## Решение: drain-on-Suspend (дренаж device-каналов + один GC), без swap пула

Раз on-device A/B невозможен и держатель неразличим эмпирически — фикс **безопасно
покрывает ОБА** держателя: дренирует device-каналы (B) И форсирует GC, чтобы
sync.Pool victim cache (A) освободился. Устройство **НЕ закрывается** (`Close()`) —
keypair/handshake/index/bind/endpoints живы, пинги и общие узлы работают; resume =
`Up()` как сейчас. Никакого «slim»-состояния нет: после resume устройство
функционально идентично, отличается только idle-RSS.

### Процедура (упорядоченная, race-safe)

0. **PREREQUISITE — припарковать единственного выжившего писателя пула.**
   `RoutineReadFromTUN` (`send.go:307`) переживает `Down()`: gVisor-стек продолжает
   доставлять пакеты (DialContext общего узла, urltest health-check) в `w.outbound`,
   `Read` их возвращает, воркер делает `GetMessageBuffer` → цикл буфера через пул.
   Это **lockless-писатель пула**. Без его парковки дренаж бесполезен и небезопасен.
   → В `Endpoint.Suspend`, ПЕРЕД `Down()`: `stackDevice.suspended=true`; в
   `stackDevice.Read` добавить ветку, паркующую читателя на resume-сигнале.
1. `e.device.Down()` как сейчас. `downLocked` → `BindClose()`+`net.stopping.Wait()`
   (`device.go:470`) → каждый `RoutineReceiveIncoming` вышел → у `decryption.c` /
   `handshake.c` **ноль писателей**. После шага 0 у `encryption.c` (писатель
   `send.go:477`, gated `isRunning`+`isUp`) тоже ноль писателей.
2. Новый метод `device.SuspendDrain()` под `device.state.Lock()` (тот же мьютекс, что
   держит `changeState`). Re-check `isClosed()`/`isUp()` в начале. **НЕ** брать
   `ipcMutex` (deadlock vs `IpcSet`). Держание `state.Lock` блокирует конкурентный
   `Up()`.
3. Дренаж 3 device-каналов через `select/default` (**НЕ закрывать** — close убьёт
   device-lifetime воркеров навсегда):
   - `decryption.c` (`*QueueInboundElementsContainer`) → тело `flushInboundQueue`
     (`channels.go:90-104`): `PutMessageBuffer`+`PutInboundElement` per elem,
     `PutInboundElementsContainer`.
   - `encryption.c` (`*QueueOutboundElementsContainer`) → тело `flushOutboundQueue`
     (`channels.go:123-137`).
   - `handshake.c` (`QueueHandshakeElement` BY VALUE) → только `PutMessageBuffer(e.buffer)`.
4. **НЕ свопить пул** (см. «Ложные следы»). После шага 3 каналы пусты, остаётся
   только sync.Pool local+victim cache.
5. **ОДИН** `runtime.GC()` + `debug.FreeOSMemory()` — **на уровне guard**, ОДИН раз
   на переход селектора (после того как `suspendAmneziaWGConsumers` обошёл весь граф
   и приглушил N endpoints), НЕ per-device и НЕ периодически.
6. Отпустить `state.Lock()`. Флаг `drained`, чтобы второй Suspend был дешёвым no-op.
7. **RESUME** (текущий путь + одна строка): перед `Up()` —
   `stackDevice.SetSuspended(false)` + сигнал resume-канала. `Up()` → `upLocked`
   ре-биндит и `peer.Start()`; `RoutineReadFromTUN` распарковывается. Первый пакет
   лениво `New`-ает буфер из ТОГО ЖЕ пула (`pools.go:20-24`). Без префилла, без
   thundering herd, без `count`-skew (пул не свопался).

### gVisor netstack — отдельная забота, НЕ дренировать через пул

`*stack.Stack` (`device_stack.go`, один на `stackDevice`) держится из `*Device`
только как `device.tun.device`, общих пулов с `device.pool.*` нет. `stack.Close()`
идёт ТОЛЬКО через `Endpoint.Close()`, **никогда** через `Suspend()` — закрытие убьёт
общие узлы/пинги и необратимо. У приглушённого (deselected) узла трафика нет
(`peer.timersStop` гасит keepalive, `SendStagedPackets` early-return на `!isUp`),
поэтому netstack-остаток мал (KB–low-MB: структуры + два 256-канала); крупные 1–8 МБ
буферы — **per-connection** на живых endpoint'ах, у deselected-узла их нет. Chunk/view
пулы gVisor **package-global** (общие, GC-reclaimable) — `FreeOSMemory` из шага 5 их
заодно чистит. **Не добавлять** меру закрытия endpoint'ов — out of scope, ломает
общие узлы, необратимо.

### refcount по графу route — НЕ нужен для нагрева (выкинут из обязательного)

Нагрев = буферы, удержанные пока узел приглушён; дренаж освобождает их при Suspend
**независимо** от used-ности. Общий узел просто `Up()`-ается при повторном выборе.
Машинерия `used(node)` по `route.rules[].outbound ∪ route.final ∪ Now()` —
**возможная будущая оптимизация**, только если когда-нибудь появится «тёплый
приглушённый» узел (который пингуется в Down). Сейчас НЕ реализовывать.

---

## Гонки и почему drain race-safe

- Единственный писатель, переживающий `Down()` — `RoutineReadFromTUN` через
  `stackDevice.Read` (lockless-писатель пула). Suspended-seam (шаг 0) его паркует.
- Писатели `decryption.c`/`handshake.c` умирают с `BindClose` (`net.stopping.Wait`,
  `device.go:470`).
- Писатель `encryption.c` gated `isUp`+`isRunning` (`send.go:430/475`).
- Каждый элемент в канале доставляется ровно раз: кто выиграл receive (дренажёр или
  припаркованный воркер) — тот владеет, без double-Put.
- **Swap `PopulatePools()` ОТВЕРГНУТ** (underflow `count` → deadlock, `pools.go:29`) в
  пользу drain+GC.
- TOCTOU `send.go:475/477`: читатель может увидеть `isRunning==true`, затем
  конкурентный `peer.Stop` его сбросит, затем читатель пишет в `encryption.c` — вот
  почему шаг 0 (парковка TUN-reader) обязателен ДО `Down`.

---

## План реализации

### Submodule `wireguard-go`
- `device/device.go`: метод `func (device *Device) SuspendDrain()` рядом с
  `downLocked` (после `device.go:235`). Тело: `state.Lock()`; re-check
  `isClosed()||isUp()`; 3 цикла-дренажа (шаг 3); **без** `PopulatePools`, **без**
  close каналов; GC/FreeOSMemory НЕ здесь (решает вызывающий).
- `device/channels.go`: вынести тела `flushInboundQueue`/`flushOutboundQueue` в
  `drainInbound(c)`/`drainOutbound(c)`, чтобы `SuspendDrain` не дублировал Put-логику;
  для `handshake.c` — inline-цикл `PutMessageBuffer(e.buffer)`.
- НЕ трогать `MaxSegmentSize` (GRO), НЕ трогать lifecycle (`Close`/очереди).

### Ядро `sing-box-lx`
- `transport/wireguard/device_stack.go`: добавить `suspended atomic.Bool` +
  resume-сигнал в `stackDevice` (struct). В `Read` — ветка парковки при
  `suspended.Load()`. Метод `SetSuspended(bool)` (на clear — сигналит resume).
- `transport/wireguard/endpoint.go`: переписать `Suspend()` — type-assert tunDevice к
  stackDevice, `SetSuspended(true)` → `Down()` → `SuspendDrain()`. Добавить
  `Resume()`: `SetSuspended(false)` → `Up()`. `onPauseUpdated` (EventDeviceWake) тоже
  чистит `suspended` перед `Up()`. Для System-пути (нет stackDevice) — gate
  type-assertion'ом, `Suspend` остаётся `Down()+SuspendDrain()` без Read-парковки.
- `protocol/group/awg_selector_guard.go`: после `suspendAmneziaWGConsumers` — ОДИН
  (debounced) `runtime.GC()+debug.FreeOSMemory()` на переход селектора, не per-device.

---

## Верификация (раз on-device A/B невозможен)

### В репо (замена устройству, ловит в CI)
- `device/suspenddrain_test.go`: Device с фейковым tun+bind; набить 3 канала +
  per-peer очереди синтетическими элементами; `Down()+SuspendDrain()`; ассерты:
  (a) все 3 канала `len==0`; (b) `WaitPool.count` вернулся к pre-load (нет
  утечки/underflow); (c) `Up()`+пакет снова аллоцирует и round-trip-ит.
- Бенч с `runtime.ReadMemStats` (`HeapInuse`) до/после `SuspendDrain` — количественно
  фиксирует реклейм в CI.

### На устройстве (ОДНА прошивка, финальное подтверждение — не отладка)
- Re-снять heap-профиль (§207) на конфиге Ильи с 10 WG: атрибуция на
  `PopulatePools`/`messageBuffers` должна резко упасть.
- CPU-профиль: `scanobject` доля резко падает, нагрев уходит.
- A/B: активный узел сохраняет download-скорость (GRO цел); переключение селектора
  между WG-узлами не теряет трафик у общих узлов.

## Остаточные риски
1. **Эффективность целиком зависит от suspended-seam в `stackDevice.Read`.** Если
   `RoutineReadFromTUN` не припаркован — он перезаполняет sync.Pool после каждого
   `FreeOSMemory`, реклейм сводится на нет именно на health-checked/общих узлах §208.
   Самая рискованная правка, труднее всего верифицируется без устройства → go-тест
   обязателен.
2. **Lazy-refill** не проблема при текущей семантике (приглушённый узел шлёт 0
   пакетов), но станет ей, если будущее изменение оставит deselected-узел пингующимся
   или с открытым соединением в Down. Латентно, не активно сейчас.
3. **`FreeOSMemory` = полный STW GC.** Даже debounced до 1/переход, на
   heat-sensitive устройстве переключение с веерным suspend может дать всплеск
   latency. Допустимо т.к. Suspend редкий/user-initiated; НИКОГДА не в per-packet/
   периодический путь.
4. **gVisor per-connection буферы** (1–8 МБ) на endpoint'ах, оставшихся открытыми
   после Suspend, фикс НЕ реклеймит. RSS падает до netstack-floor, не до нуля.
   Считается пренебрежимым для traffic-free узла, не проверено на устройстве.
5. **Величина реклейма (224 МБ → floor) — проекция** из атрибуции профиля + чтения
   кода, не измерена post-fix. In-repo `HeapInuse`-бенч закрывает пул-держатель, но не
   воспроизводит multi-device gVisor + реальный пик трафика, давший 224 МБ.
6. **System=true путь** (не-gVisor) seam'а не имеет; дренаж там без парковки
   TUN-reader. Считается безопасным (System TUN read управляется fd-close иначе), но
   это отдельный, менее обкатанный путь без покрытия здесь.
