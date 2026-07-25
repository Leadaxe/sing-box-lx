# PLAN: 005 — AWG2_RANGED_MAGIC_HEADERS

## 1. Архитектура

Диапазон уже понимает нижний слой (`submodules/wireguard-go`: `device/magic-header.go` + `device/uapi.go` case `"h1".."h4"` → `newMagicHeader("N"/"N-M")`). Задача — донести spec-строку от JSON-конфига до IpcSet. Меняются **только lx-собственные файлы** (`option/wireguard_awg.go`, `transport/wireguard/device_awg.go`) — ноль новых касаний upstream, ребейз-стоимость не растёт.

Тип `option.MagicHeader` — `string` с канонизацией при парсе:

- `UnmarshalJSON`: JSON number → канон `"N"`; JSON string → парс `"N"`/`"N-M"` (uint32, start ≤ end), канонизация (`N-N` → `N`, обрезка пробелов); number `0` / строка `""`/`"0"` → unset (`""`) — сохраняет прежнюю omitempty-семантику нуля.
- `MarshalJSON`: одиночное значение → JSON number (type-fidelity со старым `uint32`), диапазон → JSON string.
- База string ⇒ тип comparable, `IsSet()` (`o != AmneziaWGOptions{}`) работает; zero value `""` = unset.
- Повторная валидация в `awgIpcLines` с именем ключа в ошибке (`E.Cause(err, "h1")`) — покрывает и программно собранные опции (libbox/лаунчер мимо JSON).

## 2. Изменяемые / новые файлы

| Файл | Тип | Изменения |
|------|-----|-----------|
| `option/wireguard_awg.go` | lx-own | `H1..H4 uint32` → `MagicHeader`; тип + Unmarshal/Marshal/Validate |
| `option/wireguard_awg_test.go` | **new** | unmarshal number / string / диапазон / ошибки; marshal round-trip; IsSet |
| `transport/wireguard/device_awg.go` | lx-own | `writeUint("h1"…)` → write magic-spec с валидацией; unset → не эмитить |
| `transport/wireguard/device_awg_test.go` | **new** (`//go:build with_awg`) | ipc-строки: одиночные, диапазон, plain WG = `""` |
| `lx-test/config/awg2_ranged.json` | **new** | check-фикстура с ranged H1–H4 (фейк-ключи) |
| `docs-lx/lx-config.md` | docs | `h1`–`h4`: `int \| "min-max"`, пример, маппинг awg.conf |
| `SPECS/README.md` | docs | строка 005 в roadmap |

## 3. Зона касания upstream (для ребейза)

**Ничего нового.** Оба изменяемых Go-файла — lx-собственные (созданы в 003), upstream-файлы не трогаются, `// lx:`-зоны не расширяются. Vendored `submodules/wireguard-go` не меняется.

## 4. Порядок работ

1. `option`: тип `MagicHeader` + замена полей + тесты.
2. `transport/wireguard`: эмит spec-строки + тесты (под `with_awg`).
3. Фикстура `awg2_ranged.json`; `go build` (с тегами и без), `go vet`, `go test`, `sing-box check` обеих AWG-фикстур.
4. Docs + roadmap.
5. Лайв/handshake-приёмка (конфиг с реальными ключами — только во временных файлах, как в 003).
6. REPORT, статус C, тег `v1.13.13-lx.6`.

## 5. Риски

- **Совместимость marshal**: код, который сериализует опции обратно в JSON (`sing-box format`, экспорт из лаунчера), должен получить number для одиночных значений — закрыто MarshalJSON-логикой + тестом round-trip.
- **`"h1": 0` / `"0"`**: прежняя семантика «0 = не задано» (omitempty по zero value uint32) сохраняется канонизацией в `""`.
- **Ошибка без имени поля** при парсе JSON — закрыто дублирующей валидацией в `awgIpcLines` с ключом и тестом текста ошибки.
