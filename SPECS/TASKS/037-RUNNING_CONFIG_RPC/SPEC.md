# SPEC 037 — GetRunningConfig: канонический снапшот работающего конфига

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — расширение libbox command-протокола (CONSTITUTION §3.6) |
| Статус | C (complete) — код + тесты + сборки; в релизный тег ещё не входит |
| Ветка | `lx-1.14` |
| Связанные | [[SPECS/TASKS/015-COMMAND_PROTOCOL_RPC_EXTENSIONS]] (инфраструктура §3.6, образец handler'ов) · [[SPECS/TASKS/014-CLASH_API_TO_COMMANDCLIENT_MIGRATION]] |

## 1. Проблема

После правки профиля без рестарта ядра клиент (LxBox) живёт в окне
рассинхрона: его текст профиля ≠ то, что реально крутится. `GetOutbounds()`
отдаёт только `tag/type/urlTestTime/urlTestDelay` — этого хватает отличить
«узел исчез» от «конфиг разошёлся», но не хватает для «View details» /
«Copy JSON» (нужен сам outbound целиком). Ядро при этом **нигде не хранит**
конфиг после старта: `newInstance` парсит `profileContent`, мутирует options
и выбрасывает обе формы после `box.New`.

## 2. Контракт

```proto
// lx:begin lx_command
rpc GetRunningConfig(google.protobuf.Empty) returns (RunningConfig) {}

message RunningConfig {
  string content = 1; // канонический JSON запущенных options
}
// lx:end lx_command
```

Семантика `content` — **единственный источник правды о том, что запущено**:

- Это **post-override** снапшот: включены мутации `newInstance` — tun
  `AutoRedirect`/`IncludePackage`/`ExcludePackage` из `OverrideOptions` и
  подмешанный OOM-killer-сервис. То есть именно тот `option.Options`,
  который ушёл в `box.New`, а не присланный клиентом текст.
- Это **re-marshal распарсенной структуры**, НЕ байт-в-байт исходник:
  порядок полей, omitempty, нормализация `[] → null`. Сравнение с профилем
  на клиенте — только семантическое, не текстовый diff.
- **Per-node JSON выводится на клиенте** извлечением тега из этого
  документа. Отдельного per-tag RPC нет намеренно: один примитив-источник
  вместо двух, которые могут разойтись; конфиги — десятки КБ, не проблема.

Ошибки (модель `GetGroups`, не Variant-B):

| Состояние | Ответ |
|-----------|-------|
| Сервис не STARTED | `FailedPrecondition` |
| STARTED, но снапшот не захвачен (attached-путь `service/api`, либо сбой marshal) | `Unavailable` |
| Без `with_lx_command` | `Unimplemented` (stub-двойник) |

## 3. Механизм

- **Захват один раз на старте.** `daemon.Instance` несёт поле
  `runningConfig string` (lx-шов в upstream-файле `instance.go`);
  `newInstance` заполняет его `captureRunningConfig(options)` сразу после
  мутаций options. Сериализация — тот же энкодер, что в `FormatConfig`
  (`json.NewEncoder` + indent «два пробела», badjson-aware), поэтому форма
  совпадает с тем, что клиент уже знает по format-выводу. Отдача по RPC —
  копия готовой строки, ноль работы на запрос.
- **Захват за тегом.** `captureRunningConfig` — build-tag-пара
  `instance_command_lx.go` / `instance_command_lx_stub.go`: без
  `with_lx_command` возвращает `""` — безтеговая сборка эквивалентна
  upstream и по поведению, и по памяти (нет копии конфига на инстанс).
- **Fail-soft.** Ошибка marshal (практически невозможна для распарсенных
  options) даёт `""`, а не срыв старта сервиса: наблюдаемость не имеет права
  ронять запуск. Handler на `""` отвечает `Unavailable`.
- **Marshal не требует registry** — registry нужен только unmarshal'у
  (`Outbound.UnmarshalJSONContext`); это то же свойство, на котором стоит
  upstream `FormatConfig`.

## 4. Секреты

В документе — приватные ключи/пароли as-is. Новой поверхности утечки нет:
конфиг изначально пришёл от клиента этим же каналом (`StartOrReloadService`),
канал локальный (unix socket / in-process). Клиент сам решает, что из
этого показывать.

## 5. Критерии приёмки

- [x] Round-trip: снапшот — валидный JSON, узлы сохраняют `tag`/`type`
  (юнит `TestCaptureRunningConfig_LX`).
- [x] Машина состояний handler'а: не-STARTED → `FailedPrecondition`;
  STARTED без снапшота → `Unavailable`; со снапшотом → строка без искажений
  (юнит `TestGetRunningConfigHandler_LX`).
- [x] Stub-эквивалентность: без тега захват = `""`, RPC = `Unimplemented`
  (юнит `TestRunningConfigStub_LX`).
- [x] Обе сборки компилируются (`daemon`+`libbox` с тегом и без); полный
  бинарь с LX-тегами (минус `badlinkname`/naive на go1.25-хосте) собирается,
  `check -c lx-test/config/minimal.json` зелёный.
- [x] Регенерация proto пиненым тулчейном идемпотентна, non-SPEC-шум
  отревёрчен.
- [ ] Полевая проверка из LxBox (после попадания в AAR-релиз).
