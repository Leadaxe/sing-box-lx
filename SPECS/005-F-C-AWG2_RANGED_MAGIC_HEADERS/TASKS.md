# TASKS — 005-F-C-AWG2_RANGED_MAGIC_HEADERS

## Опции
- [x] `option/wireguard_awg.go`: тип `MagicHeader` (string-based, comparable) — UnmarshalJSON (number + `"N"`/`"N-M"`), MarshalJSON (number ↔ string), валидация uint32 / start ≤ end
- [x] `H1..H4` → `MagicHeader`; `IsSet()` работает как раньше
- [x] `option/wireguard_awg_test.go`: number / string-число / диапазон / ошибки (start > end, > uint32, мусор, отрицательное); marshal round-trip; `0` → unset

## Девайс
- [x] `transport/wireguard/device_awg.go`: `h1..h4` → spec-строка через writeStr-путь; валидация с именем ключа; unset → не эмитить
- [x] `device_awg_test.go` (`with_awg`): `\nh1=43613244-384550127`; одиночные значения как раньше; plain WG → `""`

## Проверки
- [x] `lx-test/config/awg2_ranged.json` (фейк-ключи) + `sing-box check` обеих AWG-фикстур
- [x] Сборка с тегами и без; `go vet`; `go test` затронутых пакетов
- [x] Существующие AWG-фикстуры (`awg2_basic.json`) не ломаются

## Приёмка
- [x] Лайв/handshake-тест против awg2-сервера с ranged-конфигом (uapi принял строки без IpcError, handshake + трафик прошли); секреты — только в temp-файлах, затёрты

## Документация и закрытие
- [x] `docs/lx-config.md`: `h1`–`h4` `int | "min-max"`, no-overlap, пример с диапазоном, маппинг awg.conf
- [x] `SPECS/README.md`: строка 005 в roadmap
- [x] IMPLEMENTATION_REPORT.md, DoD-чеклист, папка → `C`
- [x] Тег `v1.13.13-lx.6` → `lx-release.yml` (desktop + AAR)
