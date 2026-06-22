# SPEC: 005 — AWG2_RANGED_MAGIC_HEADERS

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) |

Поддержать **диапазонные magic headers** AmneziaWG 2.0 (`H1`–`H4` вида `N-M`) в конфиге sing-box-lx. Только прослойка option → IpcSet; протокольный слой (vendored `Leadaxe/wireguard-go`) уже умеет диапазоны.

---

## 1. Проблема / контекст

- Лаунчер (LxBox) начал импортировать реальные awg2-экспорты. Живые конфиги содержат H-поля в новом формате AWG 2.0 — **диапазон** вместо числа:
  ```ini
  H1 = 43613244-384550127
  H2 = 826869626-2105069164
  ```
- Vendored merged-форк `submodules/wireguard-go` **уже умеет** диапазоны: `device/magic-header.go` — `newMagicHeader(spec)` принимает `"N"` и `"N-M"` (uint32, start ≤ end); `device/uapi.go` case `"h1".."h4"` парсит value через `newMagicHeader`.
- Но прослойка ядра не пропускает диапазон:
  - `option/wireguard_awg.go`: `H1..H4 uint32` — диапазон не выразить, JSON-строка `"h1": "N-M"` не анмаршалится;
  - `transport/wireguard/device_awg.go`: `writeUint("h1", o.H1)` — в IpcSet уходит только одиночное число.

## 2. Цель

Конфиг wireguard-endpoint с `"h1": "43613244-384550127"` (и одиночными `"h1": 1234567890` как раньше) парсится, проходит `sing-box check`, и диапазонная spec-строка доезжает до uapi девайса без IpcError.

## 3. Требования

### 3.1 Опции (`option/wireguard_awg.go`)
- `H1..H4` → тип «число-или-диапазон» (`MagicHeader` на базе string):
  - `UnmarshalJSON`: принимает **JSON number** (обратная совместимость — существующие конфиги с `"h1": 1234567890` читаются без изменений) и **JSON string** `"N"` / `"N-M"`;
  - валидация: обе части — uint32, start ≤ end; мусор → явная ошибка с именем поля;
  - `MarshalJSON`: одиночное значение → number (type-fidelity как раньше), диапазон → string;
  - тип comparable — `IsSet()` (`o != AmneziaWGOptions{}`) продолжает работать.

### 3.2 Девайс (`transport/wireguard/device_awg.go`)
- `h1..h4` эмитятся как spec-строка (`writeStr`-путь), unset → не эмитить.
- Гарантия «plain WG даёт byte-identical конфиг» сохраняется.

### 3.3 Документация (`docs/lx-config.md`)
- `h1`–`h4`: `int | "min-max"` + пример с диапазоном; обновить таблицу и маппинг awg.conf.

## 4. Критерии приёмки

- Тесты: unmarshal number / string-число / диапазон / ошибки (start > end, > uint32, мусор); ipc-строки с диапазоном (`\nh1=43613244-384550127`); существующие AWG-фикстуры не ломаются.
- `sing-box check` принимает конфиг с ranged H1–H4.
- Лайв-тест против awg2-сервера с ranged-конфигом (инфраструктура lx-test из 003), либо хотя бы handshake-проверка, что uapi принимает выставленные строки без IpcError.
- Сборка без `with_awg`: поведение upstream, AWG-поля → явная ошибка (как раньше).
- `go vet`, тесты затронутых пакетов — зелёные.

## 5. Вне скоупа

- Vendored `submodules/wireguard-go` — **не трогать**, он уже умеет диапазоны.
- Перепин `app/android/libbox.version` в лаунчере — задача на стороне LxBox.
- Диапазоны для S1–S4/Jc — в формате AWG 2.0 их нет (только H1–H4).

## 6. Релиз

Тег `v1.13.13-lx.6`, AAR через `lx-release.yml`.
