# SPEC: 025 — AWG_TRANSPORT_PADDING_OVERRUN

**Фича:** [AWG2](../../FEATURES/003-AWG2/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) |
| Статус | C (complete) |

Класс рантайм-крашей в **нашей** AWG-дельте (merged-форк `wireguard-go` +
AmneziaWG 2.0 обфускация), вскрытый при разборе основного бага: transport
padding (`s4`) писал за границу исходящего буфера и ронял процесс `SIGABRT` на
первом же пакете данных. Разбор вскрыл ещё четыре дефекта того же вида —
значение из конфига / подписки роняет send- или receive-горутину, — все
исправлены одним заходом.

Баг в фиче [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT) (magic-header
— [005](../005-AWG2_RANGED_MAGIC_HEADERS), junk — [008](../008-AWG_JUNK_PARAM_VALIDATION)).
Основной overrun найден на устройстве (CPH2411, AmneziaWG-профиль с `s4=60`):
приложение исчезало через 20–90 с после подключения. Дефекты **нашей** дельты
(форк + AWG-проводка) → в скоупе (CONSTITUTION §3.1). Правки — в
`submodules/wireguard-go` (осознанный отдельный шаг по сабмодулю, CONSTITUTION
§4): базовый WireGuard (`s4=0`) сломанные ветки не исполняет, поэтому чинить
надо там, где живёт обфускация.

---

## 1. Проблема / контекст

Пять дефектов, все под тегом `with_awg`, все в `submodules/wireguard-go/device`.

### 1.1 Transport padding overrun (основной, `SIGABRT`)

`RoutineSequentialSender` (`send.go`) при `paddings.transport (s4) > 0`
сдвигает содержимое пакета вправо на `s4` байт **внутри** `elem.buffer`, чтобы
освободить место под случайный префикс:

```go
for i := len(elem.packet) - 1; i >= 0; i-- {
    elem.buffer[i+padding] = elem.buffer[i]   // ← index out of range
}
```

Но injection-пути `InputPacket` / `InputPackets` (`send.go`) выделяют
`elem.buffer` **впритык** — `allocLength` включает заголовки, данные,
`PaddingMultiple` (выравнивание WG до 16) и overhead шифрования, но **не `s4`**.
После шифрования `elem.packet` начинается с `elem.buffer[0]`, так что сдвиг на
`s4` вылезает за конец. На устройстве: payload 28 → буфер 76, sealed 64,
`s4=60` → запись в `elem.buffer[63+60] = [123]` при длине 76 →
`panic: runtime error: index out of range [123] with length 76` → `SIGABRT`
всего процесса. Детерминированно, на первом пакете данных. Базовый путь
(`NewOutboundElement`, `send.go:69`) берёт `MaxMessageSize` — там места хватает;
уязвимы только injection-пути.

### 1.2 Двойной учёт rx (коррупция статистики)

`RoutineSequentialReceiver` (`receive.go`) содержал блок
`peer.rxBytes.Add(...)` + timers (`keepKeyFreshReceiving`,
`timersAnyAuthenticatedPacket*`, `timersDataReceived`) **дважды подряд** —
re-graft AmneziaWG вставил его повторно (апстрим `1adc4c7^` имеет его один раз).
Следствие: каждый принятый батч удваивал `peer.rxBytes` (download в статистике
завышался вдвое) и лишний раз дёргал `keepKeyFreshReceiving`.

### 1.3 `jmax < jmin` → паника handshake

`SendHandshakeInitiation` (`send.go`) генерит размер junk через
`rand.Int(..., big.NewInt(int64(jmax-jmin+1)))`. При `jmax < jmin` аргумент ≤ 0
→ `rand.Int` паникует. UAPI (`uapi.go`) валидирует `jc`/`jmin`/`jmax` только по
отдельности (`> 0`), связь — нет; сырой UAPI-путь мимо `awgIpcLines` (см.
[008](../008-AWG_JUNK_PARAM_VALIDATION)) остаётся открыт.

### 1.4 Длины obf-шаблонов `i1`–`i5` без границ

Билдеры `newRandObf` / `newRandCharObf` / `newRandDigitsObf` /
`newDataSizeObf` (`obf_*.go`) парсили длину через `strconv.Atoi` без проверки.
Отрицательная длина → `dst[written:written+obfLen]` с `obfLen < 0` → slice
bounds паника; огромная → `make([]byte, ObfuscatedLen(0))` в
`SendHandshakeInitiation` = многогигабайтный аллок → OOM. Битая подписка ронял
туннель на первом handshake.

### 1.5 magic-header на полный диапазон → нулевой bound

`magicHeader.Generate` (`magic-header.go`) считал `high := int64(h.end - h.start
+ 1)` — арифметика в `uint32` **до** расширения. Для диапазона
`0-4294967295` (`newMagicHeader` его принимает, `end >= start`) `uint32`
переполняется в 0 → `rand.Int(0)` паникует. Сейчас замаскировано тем, что
`mergeWithDevice` отвергает пересекающиеся headers, но защита случайна.

## 2. Цель

AmneziaWG-профиль с обфускацией не роняет процесс:
- `s4 > 0` держит трафик (overrun устранён, буфер с запасом);
- rx-статистика точна (дубликат убран);
- значения из конфига/подписки (`jmin`/`jmax`, `i1`–`i5`, magic-range),
  способные уронить горутину, обезврежены на месте — второй слой к
  fail-fast-валидации [008](../008-AWG_JUNK_PARAM_VALIDATION) (которая ловит
  `jmin>jmax` в билдере, но не покрывает сырой UAPI-путь).

## 3. Требования

### 3.1 Transport padding (`send.go`)
- `InputPacket` / `InputPackets`: `allocLength += device.paddings.transport` —
  буфер выделяется с запасом под сдвиг.
- `RoutineSequentialSender`: ручной побайтовый цикл заменён на overlap-безопасный
  `copy` (memmove). Если буфер всё же мал (`need > len(elem.buffer)`) —
  defensive-grow через пул; если `need > MaxMessageSize` (одиночным WG-сообщением
  не отправить; sing-аллокатор к тому же отдаёт `nil` при `size > 65536`) —
  пакет дропается с `Errorf`, не переполняя память.

### 3.2 rx-дубликат (`receive.go`)
- Повторный блок `rxBytes`/timers удалён; остаётся один (как в апстриме).

### 3.3 Guard'ы значений
- `send.go` `SendHandshakeInitiation`: при `jmax < jmin` — swap локальных
  границ (не паниковать; отвергать конфиг — задача [008](../008-AWG_JUNK_PARAM_VALIDATION),
  здесь только «не падать» на сыром UAPI-пути).
- `obf.go` `parseObfLen`: длина `i1`–`i5` бортуется в `[0, MaxMessageSize]`;
  четыре билдера (`obf_rand`/`obf_randchars`/`obf_randdigits`/`obf_datasize`)
  зовут его вместо голого `strconv.Atoi`.
- `magic-header.go` `Generate`: `high := int64(h.end) - int64(h.start) + 1` —
  расширение до арифметики, чтобы полный диапазон не давал нулевой bound.

### 3.4 Изоляция
- Все правки — в `submodules/wireguard-go/device` (форк целиком под AWG-графтом).
  Upstream-швов вне сабмодуля не добавляем. Тесты — рядом, в `device/` (пакет
  `device`, internal): `transport_padding_test.go` (пара устройств через
  in-memory bind/tun, `s4=60`) и `obf_guards_test.go`.

## 4. Критерии приёмки

- `transport_padding_test.go` красный на `1adc4c7` (та же паника `[123] length
  76`), зелёный с фиксом; покрыты оба injection-пути и tun-путь (in-place-ветка).
- `obf_guards_test.go`: `parseObfLen` бортует знак/величину; полнодиапазонный
  magic-header не паникует; `jmin>jmax`-триада поднимает туннель без паники.
- `go test ./device/` зелёный; `go build ./device/` и `go vet ./device/` чисто;
  ядро собирается с полным `LX_TAGS` (`with_awg` включён).
- Device-verified: AmneziaWG-профиль, падавший под нагрузкой, держит трафик;
  download в статистике не удваивается.

## 5. Вне скоупа

- Расширение fail-fast-валидации [008](../008-AWG_JUNK_PARAM_VALIDATION) на
  `i1`–`i5` / `s4` в `awgIpcLines` — здесь только рантайм-guard'ы в форке (второй
  слой). Config-level отклонение битых значений — отдельный шаг, если понадобится.
- Верхний предел `s4` в UAPI/option-слое: overrun устранён, огромный `s4`
  теперь даёт управляемый drop+`Errorf`, а не краш; жёсткий лимит — за скоупом.
- Синхронизация `paddings.*`/`headers.*` под mutex на data-path: формальная
  гонка (чтение lock-free, запись под `ipcMutex`), но в проде не срабатывает —
  единственный `IpcSet` вызывается **до** `Up` (`transport/wireguard/endpoint.go`),
  live-реконфига нет. Если появится — сделать `atomic` или задокументировать
  контракт IpcSet-before-Up.

## 6. Ссылки

- `submodules/wireguard-go/device/send.go` — `InputPacket`/`InputPackets`
  (alloc), `RoutineSequentialSender` (сдвиг), `SendHandshakeInitiation` (junk)
- `submodules/wireguard-go/device/receive.go` — `RoutineSequentialReceiver`
  (rx-дубликат)
- `submodules/wireguard-go/device/obf.go` `parseObfLen` + `obf_*.go` (билдеры)
- `submodules/wireguard-go/device/magic-header.go` `Generate`
- `submodules/wireguard-go/device/transport_padding_test.go`,
  `obf_guards_test.go`
- [003](../003-AWG2_CLIENT_ENDPOINT) / [005](../005-AWG2_RANGED_MAGIC_HEADERS) /
  [008](../008-AWG_JUNK_PARAM_VALIDATION) — фичи, к которым относятся дефекты
- Релиз: `v1.14.0-lx.8-rc.1` (`docs-lx/lx-changelog.md`)
