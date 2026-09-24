# SPEC: 101 — WG_HANDSHAKE_GSO_RETRY_LOG_NOISE

**Фича:** [AWG](../../FEATURES/003-AWG/FEATURE.md) (форк-сабмодуль `submodules/wireguard-go`, общий для WG и AWG)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — шум лога: успешная переотправка хендшейка без UDP GSO печатается как ERROR |
| Статус | I (implemented) — 2026-09-24: сабмодуль d0568ce (`device/lx_gso_unwrap.go`, три места в `device/send.go`, страж `lx_gso_handshake_log_test.go` red на d3a0e26 / green), гитлинк в суперпроекте; выпуск в v1.14.1-lx.13; полевое подтверждение по LxBox #95 впереди |
| Ветка | `lx`; сабмодуль `submodules/wireguard-go`, ветка `lx-awg2-v007` (база d3a0e26) |
| Связано | [094](../094-XHTTP_LOCAL_CLOSE_NOT_FAILURE/SPEC.md) (тот же класс: локальное штатное событие не есть ошибка), [041](../041-WG_HANDSHAKE_SELFHEAL/SPEC.md) (хендшейк-пути форка), LxBox #95 |

## 1. Симптом

Лог LxBox на Proton AWG (ядро lx.10):

```
ERROR … failed to send handshake initiation: disabled UDP GSO on 0.0.0.0:36771, …
```

Пользователь читает строку как причину обрыва и присылает её вместо лога после неё.
Между lx.4 и lx.10 код GSO не менялся; строка не доказывает потерю пакета.

## 2. Разбор

`conn/bind_std.go` (`send` с offload): если ядро отвергло GSO (`errShouldDisableUDPGSO`),
bind выключает offload для этого семейства адресов, **переотправляет тот же батч без GSO**
и возвращает `ErrUDPGSODisabled{onLaddr, RetryErr: err}`, где `RetryErr` — ошибка
**повторной** отправки, то есть `nil`, когда переотправка прошла. Обёртка нужна
data-пути как сигнал «offload выключен», не как ошибка доставки.

Три вызывающих:

| Путь | Строка | Разворот | Итог при успешной переотправке |
|---|---|---|---|
| data, `RoutineSequentialSender` | `device/send.go:1076` | `errors.As` → `Verbosef`, дальше только `RetryErr` | Verbose |
| `SendHandshakeInitiation` | `device/send.go:282` | нет | **ERROR** с полным текстом обёртки |
| `SendHandshakeResponse` | `device/send.go:342` | нет | **ERROR** с полным текстом обёртки |

Возврат обоих хендшейк-методов вызывающие игнорируют (`timers.go`, `receive.go`,
`send.go`, `lx_giveup_rebind.go`): дефект не влияет на состояние, только на лог.

## 3. Решение

Один разворот на оба хендшейк-пути, зеркально data-пути: `errors.As(err, &errGSO)` →
`Verbosef` с текстом обёртки, `err = errGSO.RetryErr`; `Errorf` — только при ненулевом
остатке. Логика bind и провод не меняются. Вынести общий разворот в lx-хелпер
(`device/lx_gso_unwrap.go`), чтобы три места не расходились.

Страж-тест на поддельном `conn.Bind`, чей `Send` возвращает `ErrUDPGSODisabled{RetryErr: nil}`
и `{RetryErr: io.ErrClosedPipe}`: хендшейк-инициация и ответ пишут Verbose в первом
случае и ERROR во втором (перехват логгера устройства, образец — тесты `lx_*_test.go`
в `device/`).

## 4. Границы

- Только сабмодуль и бамп гитлинка в суперпроекте; ядро не трогается.
- Апстрим wireguard-go (WireGuard/wireguard-go) несёт тот же дефект; контрибуция
  невозможна (бан), фикс остаётся форк-локальным и переживает re-graft как lx-коммит.

## 5. Критерии приёмки

1. Тест red на d3a0e26 (ERROR при `RetryErr == nil`), green после правки.
2. `go test ./device/ ./conn/` сабмодуля; сборка ядра с `LX_TAGS`; стенды `lx-test`.
3. Гитлинк сабмодуля обновлён, сабмодуль запушен раньше суперпроекта.
4. Выпущено в v1.14.1-lx.13; changelog и релиз-ноты называют LxBox #95.
