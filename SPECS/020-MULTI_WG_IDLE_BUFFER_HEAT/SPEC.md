# 020 — Множество WG/AWG-узлов греют телефон: eager-устройства держат bufsArrs → scan-bound GC

| Поле | Значение |
|------|----------|
| Тип | B (bug) — корень ДОКАЗАН на воспроизведении (heap до/после), фикс выбирается |
| Статус | **O (open)** — держатель доказан профилем на стенде (**confidence high**): 180 МБ = `bufsArrs` recv-воркеров живых WG-устройств. Воркэраунд («убрать лишние WG») воспроизведён: 11 ep → 1 ep даёт 269 МБ → 0. Код-фикс НЕ реализован. |
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

### Кандидаты в рычаги (выбрать после замера throughput)

1. **Динамический batch: активный=128, неактивный=малый.** `maxBatchSize` приходит из
   `bind.BatchSize()` (`device.go:558`), перечитывается в `BindUpdate` при Down→Up.
   Нужен: (а) механизм определения «неактивен» (переключение узла само НЕ усыпляет —
   доказано); (б) согласованно уменьшить `StdNetBind.getMessages()`-размер для idle.
   Главный кандидат, но сложнее — требует прокинуть batch в bind.
2. **Глобально меньший batch на android-клиенте** (`StdNetBind.BatchSize()` 128→напр.
   16). Просто (одна точка), но режет GRO-throughput и на АКТИВНОМ узле. **Требует
   замера: насколько падает download при batch=16.** Если падение приемлемо — самый
   дешёвый фикс.
3. **Down неактивных устройств** (BindClose завершает `RoutineReceiveIncoming`, defer
   `receive.go:104-110` освобождает `bufsArrs`). Реально режет, но: переключение узла
   НЕ усыпляет (доказано), а selector держит членов up для urltest health-check →
   нужен явный lazy-start / отложенный старт endpoints (не eager).

### Открытый замер (нужен ДО выбора рычага)

**Влияние batch на download-скорость** — на стенде: PUT конфиг → урезать batch →
замерить throughput активного узла. Решает выбор между рычагом 2 (если GRO терпит малый
batch) и рычагом 1 (если активному нужен 128).

---

## Верификация (стенд доступен — Debug API)

- **Уже сделано (доказательство корня):** heap `/diag/pprof?profile=heap` до/после
  смены числа endpoints; `RoutineReceiveIncoming` cum 180 МБ → 0.
- **Для фикса:** собрать билд с урезанным batch idle-устройств, снять heap — `bufsArrs`
  держатель должен упасть с ~16 МБ до ~1 МБ на неактивное устройство при сохранении
  download-скорости активного (замер throughput).
- Команды: `adb forward tcp:9269`; `GET /diag/pprof?profile=heap&query=gc=1`;
  `POST /action/switch-node` / `set-group`; `PUT /config` + `POST /action/reload-vpn`.

## Остаточные риски
1. Уменьшение batch активного узла может срезать GRO download-throughput — обязателен
   замер до релиза. Если активному нужен 128 — только динамический рычаг (1), не
   глобальный (2).
2. «Неактивность» устройства не тривиальна: переключение узла НЕ усыпляет; selector
   держит всех членов up для health-check. Рычагу 1/3 нужен корректный критерий и
   обратимость (re-Up на реальный dial / health-probe).
3. 2 recv-горутины на устройство (v4+v6) — каждая держит свой `bufsArrs`. Фикс должен
   бить обе.
