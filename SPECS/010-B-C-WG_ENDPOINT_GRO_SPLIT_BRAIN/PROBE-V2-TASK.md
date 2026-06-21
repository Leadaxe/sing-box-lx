# 010 — Задание команде ядра: probe v2 (вывод не в stderr, а в core-log)

**Кому:** команда `sing-box-lx` (правка submodule `wireguard-go` + `transport/wireguard/endpoint.go`).
**От кого:** прогон probe v1 на реальном устройстве (LxBox, 2026-06-21).
**Зачем:** probe v1 на тест-телефоне **немой** — не из-за бага в логике, а из-за
канала вывода. Нужна v2 с выводом в канал, который на Android реально виден.

---

## ⚠️ ОБНОВЛЕНИЕ 2026-06-21 (после прогона v2): нужна v2.1 — `Errorf`, не `Verbosef`

Probe **v2** (`endpoint.go:260-264`, `logger.Verbosef("LX-GRO-PROBE …")`) прогнан на
том же CPH2411/Android 15 — и **снова немой**. Причина установлена на железе и
финальна:

**На Android уровень DEBUG не доходит до platform-forwarding вообще.** Факты прогона:
- `endpoint.go:228-229` `Verbosef` → `e.options.Logger.**Debug**(strings.ToLower(...))`.
- Поднял `log.level` конфига до `debug` (`PUT /config`, подтверждено
  `{"level":"debug"}`, ядро переподняло endpoint) → в LxBox core-log
  (`GET /logs?source=core`, 500 записей) **ноль записей уровня `debug`** — только
  `info`/`error`. Т.е. весь DEBUG-класс отсекается в libbox-тракте до наблюдателя,
  несмотря на то что `log/observable.go:140-141` выглядит как безусловный
  `platformWriter.WriteMessage` (в реальной .aar-сборке debug всё равно не проходит).
- Контроль: `endpoint/wireguard[…]: outbound connection` (это **INFO**) — доходит
  пачками; `error`-записи — доходят. Значит канал core-log жив, не проходит именно
  **уровень**.
- Дополнительно: формат прогоняется через `strings.ToLower` (`endpoint.go:229`) →
  строка пришла бы как `lx-gro-probe …` (нижний регистр) — учесть при поиске, но это
  вторично: при DEBUG её всё равно нет.

**Что сделать (v2.1) — минимальная правка `endpoint.go`:** поднять уровень
probe-строки с `Verbosef` (Debug) на **`Errorf`** (Error) — Error на Android
**подтверждённо доходит** до core-log. Т.е. строки 262-263:

```go
// было:
logger.Verbosef("LX-GRO-PROBE: GOOS=%s txOffload4=%v rxOffload4=%v txOffload6=%v rxOffload6=%v dispatch=%s",
    goos, tx4, rx4, tx6, rx6, dispatch)
// стало (v2.1):
logger.Errorf("LX-GRO-PROBE: GOOS=%s txOffload4=%v rxOffload4=%v txOffload6=%v rxOffload6=%v dispatch=%s",
    goos, tx4, rx4, tx6, rx6, dispatch)
```

`Errorf` → `e.options.Logger.Error` (`endpoint.go:231-232`). NB: там тоже
`strings.ToLower` — итоговая строка будет `lx-gro-probe: goos=…`; снимать по
`q=gro-probe` (lowercase). Это диагностический probe, временный — Error-уровень для
него приемлем (на проде строки нет).

**Что НЕ нужно (проверено впустую на стороне LxBox):**
- Поднимать `log.level` конфига — DEBUG всё равно не проходит на Android.
- Bypass trace/DEBUG-фильтра в `BoxService.writeDebugMessage` (LxBox Kotlin) — строка
  до него не доходит, отсекается раньше уровнем.

Канал (core-log) и место лога (после `IpcSet`, каст `*conn.StdNetBind`) из v2 —
**правильные, не трогать**. Меняется ровно одно: `Verbosef` → `Errorf`.

---

## Что произошло на устройстве (факты прогона)

Probe v1 (`gro-probe.patch`, ветка submodule `gro-probe-010` @ `21423f6`) собран,
вложен в LxBox, установлен и прогнан:

| Параметр | Значение |
|---|---|
| Устройство | OnePlus **CPH2411**, **Android 15** (SDK 35), arm64-v8a, ColorOS |
| Сборка | release-APK с probe-`.aar` (`libbox-gro-probe-010.aar`, sha `4006811a…`), vc 2714 |
| Профиль | WARP-endpoint **без `detour`** (`🔥⛈️ WARP (AWG 1.5)`, peer `162.159.192.6:854`, single-peer → `isConnect=true`) |
| Что подтверждено | **`StdNetBind.Open` вызывался** — endpoint поднялся, был handshake и трафик (`down_total` доходил до 5.8 МБ; core-log: `endpoint/wireguard[🔥⛈️ WARP (AWG 1.5)]: outbound connection to …`) |
| Что НЕ получилось | **probe-строка `LX-GRO-PROBE` не появилась нигде** — ни в logcat, ни в `stderr.log`, ни в core/app-логах |

Маркер в бинаре есть (проверено: `strings libbox.so | grep LX-GRO-PROBE` = 2 в
arm64). То есть код probe в сборке, путь исполнялся — **молчит именно вывод**.

---

## Корень немоты: Go-stderr на этом Android уходит в `/dev/null`

Probe v1 пишет через `fmt.Fprintf(os.Stderr, …)`. На стороне LxBox stderr ловится
через `Libbox.redirectStderr(File(filesDir, "stderr.log"))` (best-effort,
`BoxApplication.initializeLibbox`). На **CPH2411/ColorOS/Android 15** этот редирект
по факту **не наполняет файл**:

- `stderr.log` **отсутствует/пуст** — Debug API `GET /files/local?name=stderr.log`
  стабильно отдаёт `not_found` (читает `getApplicationDocumentsDirectory()/stderr.log`
  = тот же `filesDir`, куда пишет redirectStderr — путь верный, файла просто нет).
- В logcat probe тоже нет (Android по умолчанию направляет stdout/stderr приложения
  в `/dev/null`; `setprop log.redirect-stdio true` — **запрещён** non-root adb на
  ColorOS, `Failed to set property … See dmesg`).
- `run-as` — **запрещён** (release-APK не debuggable → sandbox недоступен).
- Warn `redirectStderr failed` в logcat **нет** → редирект не падает явно, но и не
  работает (dup2 на закрытый fd 2 — типовое поведение Android-приложения; молча
  no-op).

Вывод: **канал stderr на реальном целевом устройстве для probe непригоден.**
Инструкция из `PROBE.md` («`adb logcat | grep LX-GRO-PROBE`») исходила из допущения
«stderr → logcat», которое на этом OEM неверно.

---

## Что РАБОТАЕТ как канал: sing-box core-log (PlatformInterface)

Единственный канал из ядра, который на устройстве **подтверждённо доходит** до
наблюдателя — штатный лог sing-box. Он виден через LxBox Debug API
`GET /logs?source=core` (живые записи `endpoint/wireguard[…]: outbound connection`,
`router: …` и т.п. снимались в реальном времени).

В `wireguard-go` этот канал уже подключён рядом с bind'ом
(`transport/wireguard/endpoint.go`):

```go
// endpoint.go ~227
logger := &device.Logger{
    Verbosef: func(format string, args ...any) {
        e.options.Logger.Debug(fmt.Sprintf(strings.ToLower(format), args...))
    },
    Errorf:   func(format string, args ...any) {
        e.options.Logger.Error(fmt.Sprintf(strings.ToLower(format), args...))
    },
}
wgDevice := device.NewDevice(e.options.Context, deviceInput, bind, logger, e.options.Workers)
```

`device.Logger.Verbosef/Errorf` → `options.Logger.Debug/Error` → **core-log → виден
в `/logs?source=core`.** Это целевой канал для probe v2.

---

## Задание: probe v2

Цель та же, что v1 (см. `SPEC.md` шаг 0): **снять `txOffload`/`rxOffload`/`dispatch`
для WG-endpoint-сокета на android**. Меняется только транспорт вывода.

**Требование:** probe-строка должна уходить в `device.Logger` (→ core-log), НЕ в
`os.Stderr`.

Сложность: `StdNetBind` создаётся в `endpoint.go` (развилка ~200-215) **раньше**, чем
`device.Logger` (~227), и логгер в bind не передаётся — поэтому v1 и взял `os.Stderr`
(в `Open` логгера нет под рукой). Варианты, на выбор команды ядра (любой допустим):

1. **Логировать на стороне `endpoint.go` после `bind.Open`/после `NewDevice`.**
   `StdNetBind` уже хранит `ipv4TxOffload/ipv4RxOffload/ipv6TxOffload/ipv6RxOffload`
   как поля (см. v1-патч — они проставляются в `Open`). Добавить геттер на bind
   (напр. `OffloadState() (tx4, rx4, tx6, rx6 bool)`) и сразу после поднятия device
   вызвать `logger.Verbosef("LX-GRO-PROBE: GOOS=%s tx=%v rx=%v dispatch=%s", …)`.
   Чисто, не тащит logger внутрь conn-пакета.

2. **Пробросить лёгкий лог-хук в `StdNetBind`.** Поле-функция
   `OnProbe func(string)` на bind, выставляемое из `endpoint.go` до `Open`; внутри
   `Open` дёргать его вместо `fmt.Fprintf(os.Stderr, …)`. Хук замыкается на
   `e.options.Logger.Debug`.

Формат строки оставить как в v1 (по строке на v4/v6 сокет), маркер `LX-GRO-PROBE`,
поля `GOOS / txOffload / rxOffload / dispatch`. `dispatch` — `single` на android,
`split` на linux (логика `groProbeDispatch()` из v1 переносится без изменений).

**Уровень:** через `Verbosef` (→ `options.Logger.Debug`). На устройстве снимем
`GET /logs?source=core` — debug-уровень в core-log проходит (проверено: INFO/router
видны; LxBox core-log не режет debug на этом канале при включённом core_logs).

> NB на стороне LxBox: чтобы debug-строки точно дошли, на устройстве включим
> `core_logs_enabled=true` (App Settings → Diagnostics) перед прогоном — это наша
> зона, отдельного действия от ядра не требует.

**Не трогать:** саму offload-логику (`controlfns_linux.go` / `features_linux.go` /
`bind_std.go` dispatch). Это по-прежнему **только замер**, не фикс. Фикс (гейт
`UDP_GRO` за `!android`) — отдельным шагом после того, как v2 даст
`rxOffload`-факт.

---

## Сборка и передача (как с v1)

```bash
# в submodule wireguard-go: ветка gro-probe-010, новый commit поверх 21423f6
# в main repo: ветка lx-gro-probe-010 бампит pin submodule
gh workflow run lx-build.yml -f target=android-aar -f branch=lx-gro-probe-010
gh run download <run-id> -D dist/lx-gro-probe-010
```

Отдать обновлённый `libbox-gro-probe-010.aar` (+ обновить sha в `PROBE.md`). LxBox-сторона
готова: APK-обвязка, профиль WARP-без-detour, прогон-плейбук уже отлажены — повторный
прогон = вложить новый `.aar`, пересобрать release-APK, поднять WARP-endpoint, снять
`GET /logs?source=core | grep LX-GRO-PROBE`.

---

## Как читать результат (без изменений к SPEC.md)

| core-log строка | Вывод | Дальше |
|---|---|---|
| `rxOffload=true` (+ `dispatch=single`) | GRO взведён, не разбирается — **корень №1 подтверждён** | фикс: гейт `UDP_GRO` за `!android` |
| `rxOffload=false` | GRO не активируется — **корень №1 мёртв** | кандидат №2 (тихий хэндовер, `monitor.go`) |

---

## Статус LxBox-стороны (готово, переиспользуемо)

- Probe-`.aar` кладётся в `app/android/app/libs/libbox.aar` (бэкап реального ядра
  `v1.13.13-lx.12` в `/tmp/libbox-real-lx12.aar.bak`). **Восстановить перед релизом.**
- Сборка: `flutter build apk --release --split-per-abi --target-platform android-arm64`
  **напрямую** (НЕ через `build-local-apk.sh` — его `fetch-libbox.sh` затрёт probe).
  Подпись `CN=BoxVPN` → встаёт `adb install -r` поверх без uninstall; vc 2714 > 2702.
- Прогон через Debug API (token, порт — в LxBox `project_dev_endpoints`):
  `POST /action/start-vpn` → `POST /action/switch-node?tag=<WARP urlenc>` →
  трафик → `GET /logs?source=core&q=LX-GRO-PROBE`.
