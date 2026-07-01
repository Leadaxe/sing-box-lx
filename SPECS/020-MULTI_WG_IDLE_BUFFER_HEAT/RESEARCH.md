# 020 — Множество WG/AWG-узлов греют телефон: eager-устройства держат bufsArrs → scan-bound GC

| Поле | Значение |
|------|----------|
| Тип | B (bug) — корень ДОКАЗАН на воспроизведении (heap до/после), фикс выбирается |
| Статус | **O (open)** — держатель доказан профилем на стенде (**confidence high**): 180 МБ = `bufsArrs` recv-воркеров живых WG-устройств. Воркэраунд («убрать лишние WG») воспроизведён: 11 ep → 1 ep даёт 269 МБ → 0. ⚠️ **Изначальный primary-рычаг (меньший `BatchSize()`) ОТВЕРГНУТ разведкой кода — ломает GRO-приём** (массив GRO-разворота захардкожен на `IdealBatchSize`=128; `bind_std.go:269/565`; GRO на android включён). **Реальный рычаг = Down неактивных устройств** (рычаг 3) — освобождает `bufsArrs` целиком, GRO активной ноды цел. Реализован на ветке `lx-spec020-idle-suspend` (device-verification — [TEST_PLAN](TEST_PLAN_idle_suspend.md), pending). |
| Зона | submodule `wireguard-go` (`device/receive.go`, `conn/bind_std.go` — размер batch/bufsArrs) + ядро `sing-box-lx` (`transport/wireguard/endpoint.go` — выбор bind / lifecycle неактивных) |
| Связь | [010-WG_ENDPOINT_GRO_SPLIT_BRAIN](../010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) (GRO нужен 65535-буфер — НЕ трогать размер), [007-AWG_OVER_WIREGUARD_DETOUR_GUARD](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) (Suspend = точка приглушения, но bufsArrs НЕ освобождает), memory `android-cpu-heat-multi-wg-gc` |
| Репортер | Iliya, 2026-06-28 (CPH2411 / OnePlus, Adreno 642L, Android) |

---

## Симптом

На Android новое 1.14-ядро (LxBox 2.5.0+) держит все ядра CPU на максимуме и греет
телефон. VPN внешне работает нормально. Морда закрыта — всё равно греет. Сначала
казалось типонезависимым (VLESS/Trojan/WG). «Не у всех устройств». Освобождение
RAM **не помогает**.

---

## Корень (ДОКАЗАН экспериментом на воспроизведении, high confidence)

Греет **фоновый сборщик мусора Go**, сканирующий большую pointer-плотную живую кучу.
Куча — это **`bufsArrs` recv-воркеров живых WG-устройств**.

### CPU (горячий профиль)

`runtime.gcDrain` 65.69%, `runtime.scanobject` 52.65%, `greyobject` 15%, `findObject`
11%. GC **scan-bound**. **Crypto в профиле НЕТ** (нет chacha/poly/AEAD) — устройство в
момент снятия НЕ обрабатывало трафик, но GC всё равно жёг ~3.5 ядра, сканируя кучу.

### Держатель — измерен heap inuse_space + goroutines на стенде

`PopulatePools.func3` = **269 МБ (91.7%)** по `inuse_space` (это `messageBuffers`,
`new([65535]byte)`). НО держит их **`RoutineReceiveIncoming` = 180.79 МБ (61%) cum** —
буферы висят **на стеках recv-воркеров** (`bufsArrs`), `peek` подтверждает 100% выдачи
через `sync.Pool.Get` (в руках воркеров, не в пуле, не в каналах).

**Арифметика (сходится точно):**
- 11 живых wireguard endpoints (`RoutineTUNEventReader`×11).
- **22 `RoutineReceiveIncoming`, ВСЕ на `StdNetBind` (batch=128)**, 0 на `ClientBind`.
  (2 на устройство: `makeReceiveIPv4` + `makeReceiveIPv6`.)
- Каждый держит `bufsArrs = make([]*[65535]byte, maxBatchSize)`, `maxBatchSize =
  bind.BatchSize() = 128` → **128 × 65535 ≈ 8 МБ**, заполняется СРАЗУ на старте
  горутины (`receive.go:99-102`), держится весь lifetime сильной ссылкой.
- **22 × 8 МБ = 176 МБ ≈ 180.79 МБ.** ✅

### A/B на стенде (Debug API `/diag/pprof`) — что доказано

| Состояние | PopulatePools | RoutineReceiveIncoming | устройств | recv |
|---|---|---|---|---|
| baseline (11 ep) | 269 МБ | 180 МБ | 11 | 22 |
| переключить активный узел WARP→home | 269 МБ | 180 МБ | 11 | 22 |
| **конфиг с 1 endpoint + reload** | **0 МБ** | **0 МБ** | **1** | **2** |

1. **Буферы НЕ засыпают.** Переключение активного узла/группы НЕ освобождает память —
   неактивные устройства держат `bufsArrs` так же, как активное.
2. **Память ∝ числу endpoints, не активности.** 11 ep → 1 ep: 269 МБ → 0. Это и есть
   воркэраунд Ильи, воспроизведённый профилем.

### Почему batch=128 (а НЕ 1)

`endpoint.go:200-202`: bind = `StdNetBind` **тогда и только тогда**, когда
`e.options.Dialer` реализует `dialer.WireGuardListener`; иначе `ClientBind` (batch=1).
WARP/AWG-endpoints используют `WireGuardListener`-диалер → `StdNetBind` →
`BatchSize()=128` на android (`bind_std.go:322`, `linux||android → IdealBatchSize=128`).
> Прежние версии этого SPEC ошибались в обе стороны: первая — `bufsArrs`=8 МБ, но без
> доказательства; вторая — «batch=1, держат каналы/sync.Pool» (неверно: на стенде bind
> = `StdNetBind`, batch=128, держат `bufsArrs`, а НЕ каналы/victim-cache).

### Почему scanobject 52%, хотя `[65535]byte` это noscan

GC не сканирует нутро байтовых буферов. Жгут **указатели**: `*[65535]byte` в `bufsArrs`
(массив указателей) + pointer-плотные `QueueInbound/OutboundElement`
(`buffer`/`packet`/`keypair`/`endpoint`/`peer`, `receive.go:28-34`), которые рождаются
из этих буферов под нагрузкой и живут в пулах/каналах. Число указателей ∝ числу
устройств, **не** размеру буфера.

### Роль MaxSegmentSize 2200 → 65535 (1.13 → 1.14)

Единственное, что изменилось между 1.13 и 1.14 в этой зоне — `MaxSegmentSize`
2200→65535 (submodule commit `2e0774f`, ради GRO; `downLocked`/пулы/каналы/batch
**идентичны** 1.13↔1.14, проверено diff'ом). Это раздуло **байты** держателя ×30
(`bufsArrs` 22×128×2200 ≈ 6 МБ → ×65535 ≈ 176 МБ). Резидентная куча ×30 → чаще
GC-циклы → те же указатели сканируются чаще → `scanobject` доминирует. **MaxSegmentSize
= триггер «появилось в 1.14» (объём), но НЕ корневой держатель** — держатель (eager
recv-воркеры с полным batch) был и в 1.13, просто дешевле.

---

## Ложные следы (исключены)

- **`MaxSegmentSize` 65535 → 2200** — буфер нужен для UDP_GRO (см. §010,
  upstream-родной фикс в 1.14). Откат задушит download-скорость. Это триггер объёма,
  не держатель — откатывать неправильно.
- **Suspend-дренаж device-каналов / sync.Pool victim-cache** — ОТВЕРГНУТ профилем:
  держит `bufsArrs` recv-воркеров (61% cum), а НЕ каналы (`decryption.c` и пр.) и НЕ
  victim-cache. Доказано: переключение узла (которое триггерит Suspend) НЕ освобождает
  память.
- **lazy `sync.Pool` / меньше `PreallocatedBuffersPerPool`** — не при чём: `bufsArrs`
  держит буферы СИЛЬНОЙ ссылкой, не через пул/семафор.
- **oomkiller / FreeOSMemory** — исключён (не активен на Android по умолчанию).

---

## Решение: уменьшить размер `bufsArrs` неактивных устройств

Держатель = `maxBatchSize × 65535 × 2 recv × N_устройств`. Активный узел нуждается в
batch=128 ради GRO-throughput; неактивные (принимают ~0 пакетов) — нет. Цель: дать
неактивным устройствам **малый batch** (напр. 8) → 8×65535×2 ≈ 1 МБ/устройство вместо
16 МБ.

> ⚠️ **«lazy bufsArrs» в наивном виде НЕВОЗМОЖЕН.** `StdNetBind.receiveIP`
> (`bind_std.go:260-279`) на каждый `recv` берёт `getMessages()` — фиксированный массив
> `IdealBatchSize=128` `ipv6.Message` — и мапит **каждый** `bufs[i] → msgs[i].Buffers[0]`,
> затем `ReadBatch(*msgs, 0)` читает в весь массив за один syscall. Нельзя дать `bufs`
> короче/с дырами. Уменьшать надо **сам batch** (и `bufsArrs`, и `getMessages`
> согласованно), а не лениво заполнять фиксированный массив.

### Замер throughput ВЫПОЛНЕН на стенде (2026-06-29)

Метод: статический `curl` arm64 в `/data/local/tmp` на устройстве, download через
тоннель (активный WARP), `--resolve` в обход DNS (DNS от shell не идёт через тоннель),
`-k` (glibc-curl не находит CA-store на Android). 50 МБ × 5 прогонов.

- **Baseline (batch=128): 10.7 МБ/с медиана (≈86 Мбит/с)**, стабильно 9.1–11.2.
- **CPU под нагрузкой (download активен, 8с):** `Syscall6` **36%**, `scanobject` 6%,
  crypto ~3% (`aes.encryptBlock` 1.5%). CPU idle (без загрузки): `Syscall6` 33.6%,
  `scanobject` 3.85%.

> **Вывод: A/B «batch 128 vs 16» на этом классе канала НЕинформативен — и это
> результат.** Throughput упирается в **WARP-канал + syscall-overhead (36% CPU)**, НЕ в
> batch-обработку. GRO/batch экономит syscall'ы и влияет на скорость только на сотнях
> Мбит/гигабитах; на ~86 Мбит/с канал — узкое место задолго до этого. Значит **уменьшение
> batch на типичном мобильном/WARP-канале скорость НЕ роняет.**

### Кандидаты в рычаги (рекомендация после замера)

> ⚠️ **ОБНОВЛЕНО (разведка кода submodule): рычаг 1 ОТВЕРГНУТ — ломает GRO-приём.**
> Изначально этот раздел ставил «глобально меньший `BatchSize()`» как PRIMARY. Чтение
> кода `wireguard-go` это **опровергло**: уменьшить `BatchSize()` **нельзя** без слома
> GRO. Механика (см. подробный разбор в «Логика фикса» ниже):
> - GRO на приём на android **ВКЛЮЧЁН** — `UDP_GRO` ставится без android-гейта
>   (`conn/controlfns_linux.go:90-104`; SPEC 010 гейтил под `!android` только GSO на
>   ПЕРЕДАЧУ, а не GRO-приём), так что `rxOffload=true`.
> - На GRO-пути `receiveIP` (`bind_std.go:267-275`) читает coalesced-пакеты и зовёт
>   `splitCoalescedMessages`, который разворачивает **один** GRO-пакет в **до 64**
>   датаграмм (`udpSegmentMaxDatagrams=64`). `readAt = len(*msgs) − IdealBatchSize/64`
>   (`bind_std.go:269`) **хардкодит `IdealBatchSize`=128**, а массив сообщений должен
>   вмещать развёртку.
> - Урезать `bufsArrs`/batch до 8 → массив не вмещает 64-датаграммный разворот →
>   `"splitting coalesced packet resulted in overflow"` (`bind_std.go:565`) **или**
>   паника из рассинхрона `bufs`(8) vs `getMessages`(128). GRO задушить нельзя (§010,
>   иначе падает download).
> **Вывод:** «одна точка `BatchSize()`» технически несостоятельна — конфликтует с GRO,
> который сам этот SPEC защищает в §010. **Реализуемый рычаг — №3 (Down неактивных):**
> `BindClose` завершает recv-воркеры и освобождает `bufsArrs` ЦЕЛИКОМ, **не трогая GRO
> активной ноды** (у неё batch остаётся 128). Это не «самый дорогой запасной», а
> **единственный жизнеспособный**. Реализован на ветке `lx-spec020-idle-suspend`
> (см. [SPEC.md](SPEC.md) §13).

1. ~~**[PRIMARY] Глобально меньший batch на android-клиенте** (`StdNetBind.BatchSize()`
   128→напр. 8–16).~~ **ОТВЕРГНУТ — ломает GRO** (см. врезку выше). Срезал бы `bufsArrs`
   с 8 МБ до ~0.5–1 МБ на recv-горутину, и на мобильном/WARP-канале throughput не падает
   — но массив сообщений GRO-приёма обязан вмещать `IdealBatchSize`=128 слотов под
   разворот coalesced-пакета, поэтому урезать его нельзя без overflow/паники.
2. **[SECONDARY] Динамический batch: активный=128, неактивный=малый.** ⚠️ Наследует ту же
   GRO-проблему рычага 1 для idle-ноды: пока на idle-сокете `rxOffload=true`, малый batch
   её recv-путь сломает. Жизнеспособен ТОЛЬКО если на idle-устройстве ещё и **выключить
   GRO-приём** (тогда обычный `ReadBatch` без split, любой размер массива ок). Сложнее
   рычага 3 и без его выигрыша (буферы всё равно держатся), поэтому не выбран.
3. **[РЕАЛИЗУЕМЫЙ / PRIMARY] Down неактивных устройств** (BindClose завершает
   `RoutineReceiveIncoming`, defer `receive.go:104-110` освобождает `bufsArrs`). Режет
   полностью (до 0 на устройство) и **сохраняет GRO активной ноды**. Цена: переключение
   узла НЕ усыпляет (доказано), а selector держит членов up для urltest health-check →
   нужен критерий «неактивно И недостижимо из активного дерева роутинга» + обратимость
   (wake-on-dial). Реализован через reachability-walk на ветке `lx-spec020-idle-suspend`.
   Раз рычаги 1/2 отпали из-за GRO — это **единственный рабочий путь**, а не дорогой запас.

---

## Почему рычаг 1 (меньший `BatchSize()`) НЕ работает — разбор кода

> Этот раздел раньше назывался «Логика фикса (PRIMARY-рычаг)» и расписывал урезание
> `BatchSize()` как реализацию. **Разведка кода submodule его опровергла** — оставляю
> разбор как обоснование, почему рычаг отвергнут (и почему PRIMARY стал рычаг 3, Down).

**Что предлагалось.** `bufsArrs = make([]*[65535]byte, maxBatchSize)` (`receive.go:89`),
где `maxBatchSize = bind.BatchSize()`. Для `StdNetBind` на android это **128** → 8 МБ на
recv-горутину × 22 = 176 МБ. Урезать `BatchSize()` 128→8 в `conn/bind_std.go:322` →
пропорционально меньше `bufsArrs`. Замер throughput показал, что на мобильном/WARP-канале
скорость от этого не падает (см. выше). На первый взгляд — «одна строка».

**Почему ломается (GRO-приём).** Урезать `bufsArrs` нельзя в отрыве от массива сообщений
GRO-разворота, а тот завязан на `IdealBatchSize`=128 жёстко:

- **GRO на приём на android ВКЛЮЧЁН.** `UDP_GRO` ставится для всех linux/android-ядер
  ≥5.12 без android-гейта (`conn/controlfns_linux.go:90-104`); android-гейт стоит только
  на `IP_PKTINFO`/sticky. `features_linux.go:22` → `rxOffload = (getsockopt UDP_GRO == 1)`
  → на современном android `rxOffload=true`. (SPEC 010 гейтил под `!android` **GSO на
  передачу**, не GRO-приём — это разные вещи.)
- **GRO-путь требует полный массив под разворот.** При `rxOffload=true` `receiveIP`
  (`bind_std.go:267-275`) читает coalesced-пакеты в хвост массива и зовёт
  `splitCoalescedMessages`, который разворачивает **один** GRO-пакет в **до 64** датаграмм
  (`udpSegmentMaxDatagrams=64`). Стартовая позиция: `readAt = len(*msgs) −
  IdealBatchSize/udpSegmentMaxDatagrams` (`bind_std.go:269`) — **`IdealBatchSize`=128
  захардкожен**, не `s.BatchSize()`. И `getMessages()` (`bind_std.go:72`) аллоцирует
  `make([]ipv6.Message, IdealBatchSize)` — тоже 128.
- **Следствие.** Если урезать только `BatchSize()` (`bufsArrs`→8), а `getMessages`/`readAt`
  остаются на 128 — **рассинхрон** `bufs`(8) vs массив(128) → out-of-bounds паника. Если
  урезать всё согласованно до 8 — один GRO-пакет на 64 датаграммы **не помещается** в
  8-слотовый массив → `errors.New("splitting coalesced packet resulted in overflow")`
  (`bind_std.go:565`). Так что прежнее утверждение «потеря пакетов исключена, массив просто
  короче» — **неверно для GRO-пути**: массив там не «просто короче», он обязан вмещать
  `IdealBatchSize` слотов под разворот.
- GRO задушить, чтобы обойти это, **нельзя** — он нужен для download-throughput (§010,
  upstream-родной фикс 1.14; без GRO скорость падает).

**Вывод.** «Одна точка `BatchSize()`» технически несостоятельна: конфликтует с GRO-приёмом,
который сам этот SPEC защищает в §010. Рычаг 1 (и наследующий ту же проблему рычаг 2)
отвергнуты.

## Логика фикса (PRIMARY = рычаг 3, Down неактивных)

**Суть.** `device.Down()` неактивного устройства закрывает bind → завершает обе
recv-горутины → их defer (`receive.go:104-110`) **освобождает `bufsArrs` целиком** (до 0
на устройство). Активная нода остаётся `Up` с полным batch=128 → **GRO цел там, где он
нужен**. Это единственный путь, срезающий держатель без слома GRO.

**Что нужно (и почему дороже одной строки):** критерий «какие устройства гасить» нетривиален
— переключение активного узла НЕ усыпляет (доказано A/B), а selector держит членов up для
urltest health-check. Поэтому гасить можно только устройство, которое **idle И недостижимо
из активного дерева роутинга** (final / цель правила / активный выбор селектора / член
активного пула urltest / detour). Плюс обратимость: wake-on-dial (на Up — новый handshake,
Down зануляет крипто-сессию). Реализация — reachability-walk + event-driven кэш + idle-tick
на ветке `lx-spec020-idle-suspend`; разбор в [SPEC.md](SPEC.md)
§13 и план живой проверки в [TEST_PLAN_idle_suspend.md](TEST_PLAN_idle_suspend.md).

**Эффект:** N неактивных устройств × ~16 МБ (2 recv × 8 МБ) → 0; GC-нагрев уходит
пропорционально числу погашенных. `MaxSegmentSize` (65535) и batch активной ноды НЕ
трогаются → GRO цел.

---

## Верификация (стенд доступен — Debug API)

- **Сделано — доказательство корня:** heap `/diag/pprof?profile=heap` при разном числе
  endpoints: 11 ep → 269 МБ, 4 ep → 104 МБ, 1 ep → 0. Держатель ∝ числу устройств,
  линейно. `RoutineReceiveIncoming` cum 180 МБ при 11 ep.
- **Сделано — замер throughput:** baseline batch=128 = 10.7 МБ/с; узкое место = канал/
  syscall, не batch (CPU-профиль под нагрузкой).
- **Для фикса (рычаг 3, Down):** прогнать [TEST_PLAN_idle_suspend.md](TEST_PLAN_idle_suspend.md)
  — снять heap до/после idle-окна: `RoutineReceiveIncoming`/`PopulatePools` inuse_space
  должен упасть пропорционально числу погашенных устройств (~16 МБ на устройство → 0).
  Throughput активной ноды не меняется (её batch=128 не трогается). ~~Билд с `BatchSize()`=8~~
  — отвергнут (ломает GRO, см. «Почему рычаг 1 не работает»).
- Команды: `adb forward tcp:9269`; `GET /diag/pprof?profile=heap&query=gc=1`;
  `GET /diag/pprof?profile=profile&query=seconds=8` (CPU); download —
  `curl -sk --resolve speed.cloudflare.com:443:<ip> .../__down?bytes=52428800`.

## Остаточные риски
1. ~~**Быстрый канал** (был риск рычага 1).~~ Снят: рычаг 1 (меньший batch) отвергнут —
   ломает GRO. Рычаг 3 (Down) НЕ трогает batch активной ноды, так что throughput-риска на
   быстром канале у него нет. Зато появляется свой риск — **handshake на wake**: Down
   зануляет крипто-сессию, первый пакет после пробуждения ждёт нового handshake (латентный
   всплеск, не потеря; см. TEST_PLAN «No flapping» + min-dwell).
2. «Неактивность» устройства не тривиальна: переключение узла НЕ усыпляет; selector
   держит всех членов up для health-check. Рычагу 3 нужен корректный критерий
   (idle И недостижимо из активного дерева) и обратимость (re-Up на реальный dial). Это и
   есть основная сложность реализации (reachability-walk) — но раз рычаг 1 отпал по GRO,
   альтернативы нет.
3. 2 recv-горутины на устройство (v4+v6) — каждая держит свой `bufsArrs`. `Down` закрывает
   bind → завершаются обе → освобождаются оба `bufsArrs` (рычаг 3 бьёт обе автоматически).
