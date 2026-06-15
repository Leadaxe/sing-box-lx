# PLAN: 008 — AWG_JUNK_PARAM_VALIDATION

## Архитектура

Одна функция-валидатор `validateJunk(o)` в `transport/wireguard/device_awg.go`,
вызывается в начале `awgIpcLines` (сразу после `IsSet()`-проверки). `awgIpcLines`
уже возвращает `error` и уже на пути `wireguard.NewEndpoint` → endpoint build →
`check`/старт, поэтому ошибка fail-fast доходит до пользователя без паники.

Под тег `with_awg` (файл целиком под ним). Без тега — стаб `device_stub_awg.go`
уже даёт «awg support not built», поведение upstream не меняется.

## Изменённые файлы

| Файл | Зона | Что |
|------|------|-----|
| `transport/wireguard/device_awg.go` | lx (под `with_awg`) | `validateJunk` + вызов в `awgIpcLines` |
| `transport/wireguard/device_awg_test.go` | lx (под `with_awg`) | тесты правил + «не паникует при jmin>jmax» |

## Логика (узкое правило — только краш-кейс)

```go
func validateJunk(o option.AmneziaWGOptions) error {
    if o.Jmin > o.Jmax {
        return E.New("amneziawg: jmin (", F.ToString(o.Jmin), ") must be <= jmax (", F.ToString(o.Jmax), ")")
    }
    return nil
}
```

Одна проверка — ровно тот случай, что паникует в `send.go`. jc-несогласованность
осознанно **не** ловим (см. SPEC §3.1): безвредна, и строгое правило сломало бы
существующий тест `jc=4`-без-размеров и рисковало бы отклонить рабочий awg2-конфиг.
`jmin=0,jmax=0` (junk off) → `0>0` false → ок. `jmin>0,jmax=0` → ловится (это либо
краш-риск при jc>0, либо явная опечатка).

## DoD

- [ ] `go build ./...` без тегов — ок
- [ ] `go build -tags "...,with_awg" ./cmd/sing-box` — ок
- [ ] `go test -tags with_awg ./transport/wireguard/...` — зелёный (вкл. no-panic)
- [ ] `gofmt -l` — пусто
- [ ] существующие `lx-test/config/awg2_*.json` остаются валидными

## Зона касания upstream (для ребейза)

- `device_awg.go` / `device_awg_test.go` — lx-собственные файлы под `with_awg`,
  в upstream их нет → конфликтов на ребейзе не дают.
- `submodules/wireguard-go` — **не трогаем** (guard ловит раньше).
