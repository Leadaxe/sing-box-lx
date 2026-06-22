# SPEC: 008 — AWG_JUNK_PARAM_VALIDATION

| Поле | Значение |
|------|----------|
| Тип | B (bug) |
| Статус | C (complete) |

Отклонять невалидную junk-связку AmneziaWG (`jc`/`jmin`/`jmax`) **на уровне
конфига** (при построении endpoint, до handshake), а не падать рантайм-паникой в
горутине таймера. Главный мотив: при `jmin > jmax` вендоренный amneziawg-go
**паникует** (`rand.Int` с аргументом ≤ 0) — это краш ядра, а не управляемая
ошибка.

Баг в фиче [003 AWG2_CLIENT_ENDPOINT](../003-AWG2_CLIENT_ENDPOINT).
Найден при разборе [007](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD) /
[issue #2](https://github.com/Leadaxe/sing-box-lx/issues/2). Баг **нашей** дельты
(merged-форк wireguard-go + наша AWG-проводка) → в скоупе (CONSTITUTION §3.1).

---

## 1. Проблема / контекст

Семантика junk (из `submodules/wireguard-go/device/send.go:143-154`):

```go
jc   := peer.device.junk.count   // сколько junk-пакетов слать перед handshake
jmin := peer.device.junk.min
jmax := peer.device.junk.max
for i := 0; i < jc; i++ {
    nBig, _ := rand.Int(rand.Reader, big.NewInt(int64(jmax-jmin+1)))  // ← паника при jmax<jmin
    n := int(nBig.Int64()) + jmin
    buf := make([]byte, n); rand.Read(buf)
    sendBuffer = append(sendBuffer, buf)
}
```

- **`jmin > jmax`**: `jmax-jmin+1 <= 0` → `rand.Int` паникует
  (`crypto/rand: argument to Int is <= 0`). Происходит в горутине таймера
  ретрансмита handshake → **краш**, не ловится валидацией. uapi (`device/uapi.go`)
  проверяет только `jc/jmin/jmax > 0` по отдельности, связь — нет.
- **`jc > 0`, но `jmin`/`jmax` не заданы**: цикл шлёт `jc` пустых (нулевого
  размера) пакетов — junk фактически не работает, тихая деградация обфускации.
- **`jmin`/`jmax` заданы, `jc == 0`**: цикл не исполняется — размеры заданы
  впустую, junk не шлётся. Вероятная ошибка конфига.

Реальные awg2-экспорты задают триаду **всегда вместе** (тест-конфиги:
`jc=4 jmin=40 jmax=70`; `jc=5 jmin=10 jmax=50`), так что правило согласованности
рабочие конфиги не ломает.

## 2. Цель

Невалидная junk-триада отвергается при построении endpoint'а с **понятной
ошибкой** (`./sing-box check` / старт фейлится управляемо), вместо рантайм-паники
позже. Это **fail-fast на уровне конфига** — в отличие от [007](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD)
(связка узлов, ловится лениво в dialer): здесь невалидно **одно** поле-сочетание
одного endpoint'а, видно сразу.

## 3. Требования

### 3.1 Правило (узкое — только краш-кейс)
- **`jmin <= jmax`** — единственная проверка. При `jmin > jmax` амнезия-форк
  паникует (`rand.Int` arg ≤ 0) → краш; это и чиним.

**Осознанно НЕ валидируем** (решение автора, рекомендация — минимальный дифф):
- `jc > 0` без jmin/jmax — шлёт пустые junk-пакеты: бесполезно, но **не
  паникует**, туннель поднимается. Расширять guard на это — навязывать мнение о
  конфиге и рисковать отклонить рабочий чужой awg2-экспорт (CONSTITUTION §2.2,
  совместимость с реальными серверами).
- размеры без `jc` — junk не шлётся, безвредно.

Существующий тест `TestAwgIpcLinesUnsetHeadersOmitted` (`jc=4` без размеров) при
узком правиле **остаётся валиден** — чужое осознанное решение не ломаем.

### 3.2 Точка проверки
- `transport/wireguard/device_awg.go` `awgIpcLines` — она уже возвращает `error`
  и уже вызывается из `wireguard.NewEndpoint` (→ `NewEndpoint` outbound →
  `check`/старт). Ошибка доходит до пользователя **до** handshake, без паники.
- Под тег `with_awg` (как и весь `awgIpcLines`). Без тега AWG-конфиг и так
  отвергается раньше («awg support not built») — поведение upstream не меняется.

### 3.3 Изоляция
- Правка — в существующем lx-файле `device_awg.go` (он целиком lx, под тегом).
  Новых upstream-швов не добавляем. Тест — в `device_awg_test.go` (уже lx).

## 4. Критерии приёмки

- `jmin > jmax` → endpoint не строится, ошибка с упоминанием `jmin`/`jmax`; **нет
  паники** (`require.NotPanics`).
- Валидная триада (`jc=4 jmin=40 jmax=70`), `jc=4` без размеров, и junk-off —
  проходят. Тест-конфиги `awg2_basic`/`awg2_ranged` валидны; существующий тест
  `TestAwgIpcLinesUnsetHeadersOmitted` не сломан.
- Юнит-тест зелёный; сборка с `with_awg` ок; `go build ./...` без тегов ок;
  `gofmt -l` пусто.

## 5. Вне скоупа

- Правка самого `submodules/wireguard-go` (добавить проверку в uapi/убрать
  панику) — наш guard в `awgIpcLines` ловит раньше, форк не трогаем (его правка —
  отдельный осознанный шаг, CONSTITUTION §4 про сабмодуль).
- Валидация s1–s4 / h1–h4 / i1–i5 (h-поля уже валидируются `MagicHeader.Spec()`).
- MTU/размерные предупреждения — уже есть в `wireguard.NewEndpoint`.

## 6. Ссылки

- `submodules/wireguard-go/device/send.go:143-154` (junk-цикл, паника)
- `submodules/wireguard-go/device/uapi.go:310-341` (uapi: только `>0`, связи нет)
- `transport/wireguard/device_awg.go` `awgIpcLines` (точка проверки)
- [007](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD) / [issue #2](https://github.com/Leadaxe/sing-box-lx/issues/2) — где баг найден
