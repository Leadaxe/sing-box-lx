# SPEC 156 — явный `"record_fragment": false` под `detour`

**Фича:** [019-SNIFF_GUARD](../../FEATURES/019-SNIFF_GUARD/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — долг [SPEC 060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) §3 п.3: отказаться от авто-фрагментации под `detour` было нельзя |
| Статус | I (implemented) — код и юниты 2026-11-12; полевая проверка ждёт ответа в LxBox#215 |
| Источник | [LxBox#215](https://github.com/Leadaxe/LxBox/issues/215): MASQUE `h2` через `detour` → группа с одним `direct` висит `context deadline exceeded`; без detour и на `h3` работает. Решение владельца 2026-11-12: `*bool` на самом `record_fragment` («более нативно пользователям»), а не отдельное lx-поле |
| Build-tag | — (общий TLS-слой) |

## 1. Проблема

SPEC 060 включает `record_fragment` по умолчанию, когда TLS-outbound диалит через `detour`.
Поле было `bool`, поэтому явный `"record_fragment": false` не отличался от отсутствия ключа:
дефолт включался всё равно. Выключить фрагментацию под detour было нечем.

Это нужно, когда разрезанный на несколько TLS-записей ClientHello дропает DPI, а PMTU-дыры,
ради которой существует дефолт, на пути нет. В LxBox#215 detour ведёт в `direct`: без
detour ClientHello уходит одной записью и проходит, с detour режется и, по гипотезе,
дропается (подтверждение запрошено у репортёра).

## 2. Решение

- `OutboundTLSOptions.RecordFragment` — `*bool` (`option/tls.go`, `// lx: SPEC 156`).
  Ключ отсутствует → `nil` → дефолт SPEC 060. Явный `false` → выбор пользователя, дефолт не
  трогает. Явный `true` — как раньше.
- `applyDetourFragmentDefault` (`common/tls/client.go`) считает выбором `fragment: true` или
  `RecordFragment != nil`.
- Читателям нужно только итоговое вкл/выкл — они идут через
  `OutboundTLSOptions.RecordFragmentEnabled()` (`option/tls_lx.go`).
- JSON туда-обратно сохраняет явный `false` (`omitempty` на указателе не выкидывает
  `false`) — экспорт running-config и lxd опцию не теряют.

Отвергнуто: отдельное lx-поле (`detour_fragment`) — дешевле на мержах (одна строка в
апстрим-файле), но пользователь ожидает выразить это тем же ключом.

## 3. Тронутые файлы (зона мержа)

| Файл | Что |
|---|---|
| `option/tls.go` | тип поля `bool` → `*bool`, хвостовой `// lx: SPEC 156` (без сдвига выравнивания) |
| `common/tls/std_client.go`, `utls_client.go`, `system_client.go` | чтение через `RecordFragmentEnabled()` |
| `protocol/naive/outbound.go` | то же |
| `common/tls/windows_client_test.go` | `RecordFragment: lxBoolPtr(true)` |
| `common/tls/client.go` | условие дефолта (зона SPEC 060) |
| lx-файлы | `option/tls_lx.go`, `protocol/masque/legacy_options_lx.go`, тесты |

На мерже: апстрим, добавивший нового читателя `options.RecordFragment` как `bool`, сломает
сборку явно (`*bool` в булевом контексте не компилируется) — тихой ошибки нет.

## 4. Проверка

- `common/tls/detour_fragment_lx_test.go`: кейс «явный `false` под detour остаётся `false`»,
  `TestRecordFragmentJSON` — `{}` / `false` / `true` декодируются различимо и переживают
  round-trip.
- `go test` `./common/tls ./protocol/masque ./option` под `LX_TAGS` ✅; `go build ./...` под
  `LX_TAGS` ✅; `GOOS=windows go vet ./common/tls` ✅.
- Поле: LxBox#215 — `"record_fragment": false` при detour на `direct` после подтверждения
  гипотезы репортёром.

## 5. Условие снятия

Вместе с SPEC 060, либо если апстрим сам сделает поле трёхзначным.

## 6. Не в scope

- Линия `lx-1.15` — переносится отдельно при следующем синке этой линии.
- UI LxBox (переключатель «без фрагментации под detour») — на стороне приложения.
- Ретрай «целый ↔ разрезанный» — не делаем: отказ неотличим от мёртвого узла, ретрай возможен
  только после таймаута 12–17 с (обсуждение с владельцем 2026-11-12).
