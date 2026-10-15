# SPEC: 130 — WIN7_REGISTRY_NOTIFY_THREAD_AGNOSTIC

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (условие снятия)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — дефект апстрима sing-tun `bca4bf0`, пришёл с синком v1.14.3 (lx.14) |
| Статус | I (implemented) — патч в форке `submodules/sing-tun`, кросс-сборка windows/386 и amd64; проверка на Win7 — за репортёром [issue #38](https://github.com/Leadaxe/sing-box-lx/issues/38) |

Сборка `windows-7` с `v1.14.3-lx.14` и `lx.15` не стартует на Windows 7:

```
FATAL start service: initialize network: watch registry key
SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces: The parameter is incorrect.
```

## 1. Причина

Апстрим sing-tun `bca4bf0` («Detect DNS server changes in default interface
monitor») добавил в `monitor_windows.go` наблюдатель за ключами
`Tcpip{,6}\Parameters\Interfaces` через `RegNotifyChangeKeyValue` с фильтром
`REG_NOTIFY_THREAD_AGNOSTIC`. Флаг появился в Windows 8; Windows 7 отвечает на
него `ERROR_INVALID_PARAMETER`. Ошибка регистрации фатальна для
`NetworkUpdateMonitor.Start` → ядро не запускается вообще.

Апстрим держит этот код и в `dev` на 2026-10-12. Win7 — штатная цель нашего
релиза (`lx-release.yml`, нога `windows-7` на пропатченном Go).

## 2. Фикс

`submodules/sing-tun/monitor_windows.go`, блоки `// lx: SPEC 130`:

1. Регистрация пробует фильтр с `REG_NOTIFY_THREAD_AGNOSTIC`; на
   `ERROR_INVALID_PARAMETER` снимает флаг и повторяет. Решение запоминается
   в наблюдателе — дальше перерегистрация идёт уже без флага.
2. Без флага уведомление отменяется, когда умирает поток-регистратор. Поэтому
   все регистрации (первичные и перерегистрация после события) выполняются в
   горутине цикла, прибитой `runtime.LockOSThread()` на всё время жизни
   наблюдателя; ошибка первичной регистрации возвращается вызывающему через
   канал, как и раньше — `watch registry key <path>: …`.

На Windows 8+ флаг принимается с первой попытки — поведение апстрима не
меняется (кроме того, что регистрация происходит на выделенном потоке).

## 3. Условие снятия

Апстрим sing-tun перестанет требовать `REG_NOTIFY_THREAD_AGNOSTIC` (фоллбэк
или отказ от флага) либо откажется от поддержки Windows 7 в сборках sing-box
вместе с нами. При мерже сабмодуля: конфликт в `monitor_windows.go` = сверить
регистрацию с этим разделом.

## 4. Проверка

- `GOOS=windows GOARCH=386 go vet .` и `GOOS=windows GOARCH=amd64 go build ./...`
  в сабмодуле — зелёные.
- Живая: сборка `windows-7` следующего тега на машине репортёра стартует,
  в логе нет `watch registry key`.
