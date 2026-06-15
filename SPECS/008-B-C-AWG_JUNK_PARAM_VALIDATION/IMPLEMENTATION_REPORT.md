# IMPLEMENTATION_REPORT — 008 AWG_JUNK_PARAM_VALIDATION

**Дата:** 2026-06-16 · **Статус:** Complete (код + тесты + DoD) · **База:** `v1.13.13`

## Итог

AmneziaWG junk-диапазон с `jmin > jmax` теперь отвергается **при построении
endpoint'а** (`awgIpcLines` → `wireguard.NewEndpoint` → `check`/старт) понятной
ошибкой, вместо рантайм-**паники** в горутине таймера ретрансмита handshake.

Баг **нашей** дельты (merged-форк wireguard-go + AWG-проводка), найден при разборе
[007](../007-B-C-AWG_OVER_WIREGUARD_DETOUR_GUARD) / [issue #2](https://github.com/Leadaxe/sing-box-lx/issues/2)
→ в скоупе (CONSTITUTION §3.1).

## Диагноз

`submodules/wireguard-go/device/send.go:147-149` перед handshake:
```go
nBig, _ := rand.Int(rand.Reader, big.NewInt(int64(jmax-jmin+1)))
```
При `jmin > jmax` аргумент `jmax-jmin+1 <= 0` → `rand.Int` паникует
(`crypto/rand: argument to Int is <= 0`) в timer-горутине → краш. `device/uapi.go`
проверяет `jc/jmin/jmax > 0` по отдельности, но не их связь.

## Решение (узкое — по согласованию с автором)

Валидируем **только** `jmin <= jmax` — ровно краш-кейс. jc-несогласованность
(`jc>0` без размеров → пустой junk; размеры без `jc` → junk не шлётся) **осознанно
не ловим**: безвредна (туннель встаёт, не паникует), а строгое правило (а) сломало
бы существующий тест `TestAwgIpcLinesUnsetHeadersOmitted` (`jc=4` без размеров) и
(б) рисковало бы отклонить рабочий awg2-экспорт — против приоритета совместимости
с реальными серверами (CONSTITUTION §2.2) и минимального диффа.

**`transport/wireguard/device_awg.go`** (lx, под `with_awg`):
- `validateJunk(o)` — `if o.Jmin > o.Jmax { return error }`;
- вызов в начале `awgIpcLines` (после `IsSet()`), до рендера IpcSet-строк.

**`transport/wireguard/device_awg_test.go`** (lx, под `with_awg`):
- `TestAwgIpcLinesJminGreaterThanJmax` — `jmin=70 jmax=40` → ошибка (`jmin`/`jmax`
  в тексте) + `require.NotPanics`;
- `TestAwgIpcLinesValidJunkRange` — `jc4/jmin40/jmax70` ок, junk-off ок.

## Приёмка (DoD)

- ✅ `go build ./...` без тегов — ок (без `with_awg` AWG отвергается раньше).
- ✅ `go build -tags "...,with_awg" ./cmd/sing-box` — ок.
- ✅ `go test -tags with_awg ./transport/wireguard/...` — зелёный, 7 ipc-тестов
  (5 прежних + 2 новых), `TestAwgIpcLinesUnsetHeadersOmitted` не сломан.
- ✅ `gofmt -l` изменённых файлов — пусто.

## Зона касания upstream (для ребейза)

- `device_awg.go` / `device_awg_test.go` — lx-собственные файлы под `with_awg`,
  в upstream их нет → конфликтов на ребейзе не дают.
- `submodules/wireguard-go` **не трогали** — guard в `awgIpcLines` ловит раньше,
  до того как значения уйдут в устройство.

## Вне скоупа

- Правка панического `rand.Int` в самом `submodules/wireguard-go` (наш guard
  делает её ненужной для конфигов, проходящих через `awgIpcLines`).
- jc/размеры-согласованность (см. «Решение» — осознанно не делаем).
- s1–s4 / h1–h4 (h уже валидируются `MagicHeader.Spec()`) / i1–i5.
