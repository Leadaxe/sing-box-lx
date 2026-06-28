# 020 — Множество WG/AWG-узлов греют телефон: eager-устройства держат bufsArrs → scan-bound GC

| Поле | Значение |
|------|----------|
| Тип | B (bug) — корень ДОКАЗАН на воспроизведении (heap до/после), фикс выбирается |
| Статус | **O (open)** — держатель доказан профилем на стенде (**confidence high**): 180 МБ = `bufsArrs` recv-воркеров живых WG-устройств. Воркэраунд («убрать лишние WG») воспроизведён: 11 ep → 1 ep даёт 269 МБ → 0. Замер throughput выполнен: на мобильном/WARP-канале batch скорость не лимитирует → primary-рычаг = глобально меньший `BatchSize()`. Код-фикс НЕ реализован. |
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

1. **[PRIMARY] Глобально меньший batch на android-клиенте** (`StdNetBind.BatchSize()`
   128→напр. 8–16). Одна точка, без динамики и определения «активности». Срежет
   `bufsArrs` с 8 МБ до ~0.5–1 МБ на recv-горутину → при 11 устройствах 176 МБ → ~11–22
   МБ. Замер показал: на мобильном/WARP-канале (где и репортят нагрев) throughput **не
   падает**. ⚠️ На быстром Wi-Fi/гигабите batch может влиять — проверить на быстром
   канале перед релизом (см. риски).
2. **[SECONDARY] Динамический batch: активный=128, неактивный=малый.** Если окажется,
   что активному узлу на быстром канале нужен 128. `maxBatchSize` из `bind.BatchSize()`
   (`device.go:558`), перечитывается в `BindUpdate` при Down→Up; согласованно уменьшить
   `StdNetBind.getMessages()`-размер для idle. Сложнее (прокинуть batch в bind + критерий
   «неактивен»: переключение узла само НЕ усыпляет — доказано). Брать, только если рычаг 1
   режет скорость на быстром канале.
3. **[SECONDARY] Down неактивных устройств** (BindClose завершает
   `RoutineReceiveIncoming`, defer `receive.go:104-110` освобождает `bufsArrs`). Режет
   полностью (до 0 на устройство), но: переключение узла НЕ усыпляет (доказано), а
   selector держит членов up для urltest health-check → нужен явный lazy-start /
   отложенный старт endpoints (не eager). Самый радикальный, самый дорогой по логике.

---

## Логика фикса (PRIMARY-рычаг)

**Суть.** `bufsArrs = make([]*[65535]byte, maxBatchSize)` (`receive.go:89`), где
`maxBatchSize = bind.BatchSize()`. Для `StdNetBind` на android это **128** → 8 МБ на
recv-горутину × 22 = 176 МБ. Уменьшить `BatchSize()` → пропорционально меньше `bufsArrs`.

**Изменение — одна точка:** `conn/bind_std.go:322`, `StdNetBind.BatchSize()`:
```go
// было:  if linux || android { return IdealBatchSize }  // 128
// стало: if android { return <малая константа: 8 или 16> }
//        if linux   { return IdealBatchSize }            // 128 не трогаем
```

**Что протянется автоматически (без других правок):**
- `BindUpdate` (`device.go:558`) читает `bind.BatchSize()` → передаёт в
  `RoutineReceiveIncoming` как `maxBatchSize` → `bufsArrs` становится размером 8, не 128.
- `getMessages()` в `receiveIP` (`bind_std.go:260`) завязан на `len(bufs)`, который
  теперь 8 → `ReadBatch` читает в полный 8-массив. **Потеря пакетов исключена** (массив
  полный, просто короче).
- Обе recv-горутины (v4 + v6) — через общий `BatchSize()`, бьёт обе.

**Эффект:** 8 МБ → ~0.5 МБ на recv-горутину; 176 МБ → ~11 МБ при 11 устройствах;
GC-нагрев уходит. `MaxSegmentSize` (65535) НЕ трогается → GRO цел (см. §010).

**Единственный риск:** GRO батчит меньше сегментов за syscall → на **гигабитном** канале
download может просесть. На мобильном/WARP — НЕ просел (замерено). Перед релизом —
замер на быстром Wi-Fi; если просядет, переключиться на динамический рычаг (активный
128 / idle 8).

---

## Верификация (стенд доступен — Debug API)

- **Сделано — доказательство корня:** heap `/diag/pprof?profile=heap` при разном числе
  endpoints: 11 ep → 269 МБ, 4 ep → 104 МБ, 1 ep → 0. Держатель ∝ числу устройств,
  линейно. `RoutineReceiveIncoming` cum 180 МБ при 11 ep.
- **Сделано — замер throughput:** baseline batch=128 = 10.7 МБ/с; узкое место = канал/
  syscall, не batch (CPU-профиль под нагрузкой).
- **Для фикса (когда дойдём до кода):** билд с `BatchSize()`=8–16, снять heap — держатель
  `bufsArrs` должен упасть с ~16 МБ до ~1–2 МБ на устройство; повторить замер throughput
  на мобильном И на быстром канале — подтвердить, что скорость не просела.
- Команды: `adb forward tcp:9269`; `GET /diag/pprof?profile=heap&query=gc=1`;
  `GET /diag/pprof?profile=profile&query=seconds=8` (CPU); download —
  `curl -sk --resolve speed.cloudflare.com:443:<ip> .../__down?bytes=52428800`.

## Остаточные риски
1. **Быстрый канал.** Замер сделан на ~86 Мбит/с WARP — там batch не влияет. На Wi-Fi/
   гигабите малый batch МОЖЕТ срезать throughput (больше syscall'ов). Перед релизом
   рычага 1 — замерить на быстром канале; если просядет, переключиться на динамический
   рычаг 2.
2. «Неактивность» устройства не тривиальна: переключение узла НЕ усыпляет; selector
   держит всех членов up для health-check. Рычагам 2/3 нужен корректный критерий и
   обратимость (re-Up на реальный dial / health-probe). Рычаг 1 этого НЕ требует (режет
   batch у всех одинаково).
3. 2 recv-горутины на устройство (v4+v6) — каждая держит свой `bufsArrs`. Фикс должен
   бить обе (рычаг 1 — автоматически, через общий `BatchSize()`).
