# SPEC: 038 — GOMOBILE_STRING_RETURN_FRAME_KILL

**Фича:** [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) |
| Статус | C (complete) |
| Ветка | `lx` |
| Связанные | [[SPECS/TASKS/037-RUNNING_CONFIG_RPC]] (внёс дефект) · [[SPECS/TASKS/015-COMMAND_PROTOCOL_RPC_EXTENSIONS]] (образец возвратов) |

Экспортированный метод `CommandClient`, возвращающий **голый `string`**, убивает
процесс ядра на android/arm64 при каждом вызове: `fatal error: bulkBarrierPreWrite:
unaligned arguments`. Это не паника — это `throw`, туннель падает без шанса
восстановиться. Единственным таким методом был `GetRunningConfig` (задача 037),
поэтому фича «снапшот работающего конфига» была неработоспособна на Android
в `v1.14.0-lx.16-rc.3` и в stable `v1.14.0-lx.16`.

Фикс: метод возвращает объект `*RunningConfig` с геттером `Content()` вместо строки.
Отгружен в `v1.14.0-lx.17-rc.1` — rc, а не stable, потому что меняет libbox API
и ждёт полевой проверки со стороны клиента до промоута.

## 1. Механизм

Разбор верифицирован генерацией биндинга (`go tool cgo`), а не рассуждением.

1. Go-строку нельзя отдать в Java как есть. gomobile кодирует её в C-структуру
   `nstring{void *chars; jsize len}` (`bind/java/seq_android.h`) — **структура несёт
   указатель**.
2. cgo строит для callback'а совмещённый фрейм «аргументы + результаты» и помечает его
   `__attribute__((__packed__))`:

   ```c
   typedef struct {
       int32_t p0;
       char __pad0[4];
       nstring r0;
       int32_t r1;
   } __attribute__((__packed__)) _cgo_argtype;
   ```

   `packed` сбрасывает требование выравнивания структуры до 1 байта, поэтому C-локаль
   `_cgo_a` перестаёт быть 8-выровненной и на arm64 ложится по 4.
3. Go-сторона обёртки пишет результат в этот фрейм:

   ```go
   func _cgoexp_..._GetRunningConfig(a *struct{ p0 _Ctype_int32_t; r0 _Ctype_nstring; r1 _Ctype_int32_t }) {
       a.r0, a.r1 = proxylibbox_CommandClient_GetRunningConfig(a.p0)
       _cgoCheckResult(a.r0)
   }
   ```

   Так как `r0` несёт указатель, присваивание компилируется в `runtime.wbMove` →
   `runtime.bulkBarrierPreWrite`, а тот требует 8-выравнивания:
   `if (dst|src|size)&(goarch.PtrSize-1) != 0 { throw(...) }`.
4. `dst = a + 8`, а `a ≡ 4 (mod 8)` → `throw`. Процесс умирает.

## 2. Почему падал ровно этот метод

Тип возврата определяет, есть ли во фрейме указатель:

| Возврат Go | Слот результата в C-фрейме | Указатель во фрейме | Write barrier | Итог |
|---|---|---|---|---|
| `(string, error)` | `nstring` (+`__pad0[4]`) | да | `wbMove` | **kill** |
| `[]byte` | `nbyteslice` (+`__pad0[4]`) | да | `wbMove` | **kill** |
| `(*T, error)`, итератор | `int32` refnum | нет | нет | ок |
| `(int64, error)` | `int64` (+`__pad0[4]`) | нет | нет | ок |
| `(int32, error)` | `int32` | нет | нет | ок |

`GetRunningConfig` был единственным экспортированным методом `CommandClient` с голым
`string`; остальные отдают refnum'ы, итераторы или скаляры — отсюда «падает только он».

⚠️ Строки в **полях** структур (`Rule.Type/Payload/Action`, `PoolSlot.Tag`) безопасны:
gomobile отдаёт объект по refnum, а строку — отдельным геттером. Ломается именно
**возврат метода**, а не наличие строки в контракте.

⚠️ `[]byte` дефект **не лечит**: `nbyteslice{void *ptr; jsize len}` — та же форма
с указателем и тем же packed-фреймом. Вариант отвергнут (проверен генерацией).

## 3. Фикс

`experimental/libbox/command_client_command_lx.go`:

```go
type RunningConfig struct{ content string }

func (c *RunningConfig) Content() string { return c.content }

func (c *CommandClient) GetRunningConfig() (*RunningConfig, error)
```

Фрейм становится `{int32 p0; int32 r0; int32 r1}` — без паддинга, без указателя;
`_cgoCheckResult` для него не генерируется вовсе, барьера нет.

Форма совпадает с той, что уже используют `Rule`/`PoolSlot`/итераторы, то есть фикс
возвращает метод на проверенный путь, а не изобретает новый.

**Ломающее изменение libbox API.** Клиент читает документ как `.Content()`.
Прото- и серверная часть (`daemon/`) не менялись: дефект чисто в биндинге libbox.

## 4. Диагностика: чему в трейсе нельзя верить

Трейс краша обманчив, и это стоит держать в голове для будущих разборов:

```
runtime.bulkBarrierPreWrite(0x0?, 0x0?, 0xb400007a8e518000?, 0x12162?)
runtime.wbMove(0xb400007a8e518000?, 0x12162?, 0x4000b7be38?)
_cgoexp_..._CommandClient_GetRunningConfig(0x7ac83f7db4)
```

- **Все аргументы напечатаны с `?`** — по `runtime/traceback.go` это значит «слот
  помечен мёртвым, значение недостоверно». `wbMove` — крошечная `nosplit`-функция,
  которая вообще не спиллит регистры, так что печатается содержимое чужих кадров.
  Читать `wbMove(typ, dst, src)` по позициям здесь **нельзя**.
- `0xb400007a8e518000` (одинаков во всех крашах) — не `*_type`, а поле `chars`:
  указатель в scudo-куче от `C.malloc` внутри `encodeString`.
- `0x12162`/`0x118f4` — не `dst`, а поле `len` (`nchars*2`): оба чётные, оба меньше
  `4*len(s)`. Меняются вместе с размером конфига.
- **Настоящая улика — аргумент `_cgoexp_`:** во всех четырёх крашах он `≡ 4 (mod 8)`.
  Систематический сдвиг, а не порча памяти.
- **Вторая улика:** под кадром `_cgoexp_` нет кадра самого Go-метода — упало **до**
  вызова. Для сравнения, в том же логе у `GetGroups` кадры
  `main.proxylibbox_...` → `libbox.(*CommandClient).GetGroups` присутствуют.

Размер конфига (27 КБ) роли не играет: дефект чисто в выравнивании фрейма и
воспроизводится на строке любой длины.

## 5. Критерии приёмки

- [x] Ни один экспортированный метод `CommandClient` не возвращает `string` или `[]byte`.
- [x] `GetRunningConfig` отдаёт документ через объект с геттером.
- [x] Регрессионный тест покрывает **всю поверхность** `CommandClient` (reflection),
      а не один метод: дефект — свойство типа возврата, любой новый метод с такой
      формой = тот же баг.
- [x] Тест доказанно краснеет на старой сигнатуре (проверено откатом).
- [x] `go build ./...`, `go build -tags with_lx_command ./...`, `gofmt -l`, тесты
      `experimental/libbox` и `daemon` — зелёные.
- [ ] Device-verify на android/arm64 после выпуска AAR (обязателен для observability-фич,
      см. runbook §4).

## 6. Затронутые файлы

| Файл | Изменение |
|---|---|
| `experimental/libbox/command_client_command_lx.go` | `RunningConfig` + `Content()`; сигнатура `GetRunningConfig` |
| `experimental/libbox/command_client_cgo_frame_lx_test.go` | новый: whole-surface guard + тест геттера |

`daemon/*` и `.proto` не тронуты.
