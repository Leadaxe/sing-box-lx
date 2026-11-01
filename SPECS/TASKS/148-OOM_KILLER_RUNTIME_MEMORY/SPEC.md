# SPEC: 148 — OOM_KILLER_RUNTIME_MEMORY

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — лимит памяти oom-killer по памяти Go-рантайма вместо RSS процесса (режим для ядра, встроенного в чужой процесс) + память рантайма в `Status` |
| Статус | D (done, device-verified) — выпущена в `v1.14.4-lx.29` 2026-11-01; LxBox develop (пин d579253a) на arm64 AVD, release-APK с lx.29: без `memory threshold reached` на старте с лимитом 200, `Memory` 331 МБ против `MemoryRuntime` 50,3 МБ, переключение на Auto без рестарта. Срабатывание порога по реальной утечке — только юнит |
| Ветка | `lx` |
| Build-tag | — (сервис `oom-killer`, `daemon`, `libbox` собираются всегда) |
| Источник | [#62](https://github.com/Leadaxe/sing-box-lx/issues/62) (LxBox, Android): RSS 461 МБ при лимите 200 МБ, живая Go-куча по allocs-профилю 42 МБ |
| Связано | [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) (нагрев от оборота аллокаций WG-пулов — отдельная тема, не лимит), [114](../114-WG_PEER_STATUS/SPEC.md) (контракт «унарный статус по типу», здесь не затрагивается) |

Контракт согласован с сессией LxBox 2026-11-01 и записан комментарием в #62.

## 1. Что не так

LxBox встраивает libbox в основной процесс приложения: Go-ядро, Flutter и Dalvik — один PID.
Приложение передаёт пользовательский «лимит памяти» (200/384/512/768 МБ) как
`SetupOptions.OomMemoryLimit`. С лимитом сервис `oom-killer` работает в режиме `memory_limit`
и сравнивает его с RSS **всего процесса** (`/proc/self/statm`), пороги `limit − 5 MiB`
(сброс) и `limit − 10 MiB` (armed). В общем процессе RSS состоит из того, чем ядро не владеет:
код `libbox.so`/Flutter/oat, графика, Dalvik, Dart-куча.

Следствия на устройстве репортёра (RSS 461 МБ, лимит 200):

- первый опрос через 100 мс после старта пробивает порог → `ResetNetwork`, `FreeOSMemory`, отчёт;
- гистерезис держит таймер в `triggered` навсегда (выход требует RSS < 190 МБ), опрос
  `/proc/self/statm` десять раз в секунду;
- любой лимит > 0 выключает слежение за свободной памятью системы (`Available` читается
  только в режимах `available` и `network_extension`) — явный лимит снимает анти-LMK-защиту.

Тезис issue о том, что GC soft limit (`limit*4/5` = 160 MiB) «сидит ниже живой кучи с пулами
WG», allocs-профилем **не подтверждён**: живая куча 42 МБ, soft limit не связывающий. Нагрев,
если он от GC, — оборот аллокаций (WG-пулы 108 МБ за жизнь при 16 МБ живых, приём датаграмм
`x/net socket.recvMsg` 43 МБ); это тема [020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md), не этой
задачи.

## 2. Решение

### 2.1 Величина «память ядра»

Память, которую Go-рантайм держит у ОС:

```
/memory/classes/total:bytes − /memory/classes/heap/released:bytes   (runtime/metrics)
```

Это буквально та величина, которой управляет `debug.SetMemoryLimit`: куча с idle-спанами,
стеки горутин, метаданные GC. Читается без stop-the-world (в отличие от `runtime.ReadMemStats`,
на котором построен `memory.Inuse()` из sing). Единый источник и для порога, и для `Status`.
Код `libbox.so` и cgo сюда не входят; на Android у ядра нет заметной нативной памяти.

### 2.2 Режим порога по памяти рантайма

Явный флаг, а не автоматика по платформе: путь лимита общий для libbox, boxdd и lxd, и в двух
последних процесс отдельный, RSS ≈ ядро. Автоматика по GOOS была бы невидимой развилкой
семантики одного и того же лимита.

| Где | Имя | Семантика |
|-----|-----|-----------|
| `SetupOptions` (libbox, gomobile) | `OomMemoryLimitHeap bool` | дефолт `false` — поведение апстрима; LxBox ставит `true` на Android |
| Опции сервиса `oom-killer` | внутреннее поле `MemoryLimitHeap`, в JSON не сериализуется (как `MemoryLimitOverride`) | наполняется из `SetupOptions` тем же путём, что лимит |

Ключа конфига **нет**: у фичи OBSERVABILITY конфиг-ключей нет, схему апстрима не трогаем;
режим доступен только приложению через `SetupOptions`. Если понадобится из конфига —
отдельное решение.

Поведение при флаге и лимите > 0 (новый режим политики `runtime_limit`):

- `usage` выборки = величина §2.1; пороги `limit − 5 MiB` / `limit − 10 MiB` и GC soft limit
  `limit*4/5` **не меняются**. «200 МБ» значит: цель GC 160 MiB, сброс при 195 MiB рантайма.
  Поскольку GC не ужимает ниже живых данных, порог достижим только настоящей утечкой или
  группой, которой лимита действительно мало; пилу между сборками режет сам soft limit.
- Слежение за свободной памятью системы **остаётся включённым**, как при лимите 0:
  выборка несёт и `Available`, состояние — по любому из двух условий (`usage ≥ порог`
  **или** `available ≤ порог available`). Пороги `available` — как в режиме `available`
  без явного `safety_margin` (32…128 MiB от `usage/4`); явный `safety_margin` из конфига
  применяется к обоим условиям, как сегодня.
- Нулевое показание `Available` считается неизвестным (условие по `available` не применяется):
  на macOS с cgo `os_proc_available_memory` отдаёт 0 вне jetsam-лимита, и без этого режим
  застрял бы в `triggered` со сбросом каждые 100 мс. Апстримные режимы не трогаются.
- Rate-trigger (сброс по скорости роста) не меняется; на Android он не армируется
  (baseline ставится только darwin-коллбэком давления), пилообразный рост кучи его не разбудит.
- Без флага, при лимите 0, на iOS NE, в boxdd и lxd — ничего не меняется.

### 2.3 Память рантайма в `Status`

Поле `MemoryRuntime` (uint64, байты) рядом с `Memory` в потоке `SubscribeStatus`, тот же тик.
`Memory` остаётся RSS процесса. daemon-proto: `memoryRuntime = 10` в `Status`. Через gomobile —
новое поле `StatusMessage.MemoryRuntime`, javap-дельта: один аддитивный геттер. Отдаётся всегда,
независимо от флага и от того, включён ли oom-killer. UI LxBox: строка «Core runtime: X / limit»
рядом с RSS процесса.

## 3. Изоляция

Логика — в новых lx-файлах (`service/oomkiller/*_lx.go`, пакет-помощник чтения метрик);
в апстримных файлах только точки переключения `// lx:` блоками: выбор режима, выборка,
переходы состояния, порог «ниже триггера», детали лога, протаскивание флага
`SetupOptions → StartedService → OOMKillerServiceOptions`, поле proto и строка в `readStatus`.
Пакет `service/oomkiller` активно правится апстримом (три коммита в октябре 2026, в т.ч.
смена сигнатуры `nextPressureState`) — конфликты на мерже ожидаемы, механические.

## 4. Проверка

- Юниты (`service/oomkiller`, пакет-помощник; оба добавлены в lx-ci):
  выбор режима по флагу и лимиту; выборка в новом режиме несёт `Available`; рост `usage`
  до порога даёт `triggered`, низкий `available` при малом `usage` — тоже; гистерезис выхода;
  величина §2.1 растёт при удержании живого среза и ≥ `HeapInuse + StackInuse`.
- Устройство — прогон LxBox 2026-11-01 на arm64 AVD (release-APK, `libbox.so` = 1.14.4-lx.29),
  лимит 200 МБ: в логе ядра после старта только `status=Starting/Started`, строк
  `memory threshold reached` нет; `Status`: `Memory` 331 МБ (RSS), `MemoryRuntime` 50,3 МБ,
  в шторке «Core memory 50.3 MB / 200 MB»; переключение на Auto через `reloadSetupOptions`
  применилось без рестарта. Срабатывание порога по реальной утечке на устройстве не воспроизвести
  (приложению нечем нарастить кучу ядра) — остаётся на юните `runtime_limit_lx_test.go`.
  Телефон владельца — после релиза LxBox rc.2.

## 5. Снятый долг

- Info-строка при старте oom-killer: `memory pressure monitoring started, mode: runtime_limit,
  limit: 200 MB, trigger: 195 MB, armed: 190 MB` — для всех режимов, включая апстримные
  (`available` — только режим), суффикс `, report only` при `killer_disabled`.
  `service/oomkiller/start_log_lx.go`, вызов в `startTimer`; тест `start_log_lx_test.go`.
