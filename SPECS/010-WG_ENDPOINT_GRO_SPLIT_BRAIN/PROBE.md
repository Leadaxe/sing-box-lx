# 010 — Probe-ядро для LxBox: замер `rxOffload` на Android

Тестовое (НЕ релизное) ядро с временным диагностическим логом. Цель — один факт:
**взводит ли ядро Android `rxOffload` для WG-endpoint-сокета**. От этого зависит,
какой из двух кандидатов чинить (см. `SPEC.md`, шаг 0).

---

## Что в ядре (probe v2.1)

> **Эволюция канала:** v1 писал в `os.Stderr` → на CPH2411/ColorOS он уходит в
> `/dev/null` (нем). v2 перешёл на `device.Logger` → core-log, но через `Verbosef`
> (DEBUG-уровень) — а **DEBUG отсекается внутри libbox-тракта на Android** (проверено
> эмпирически: при `log.level=debug` в core-log ноль debug-записей, INFO/Error идут).
> **v2.1 пишет через `Errorf` (Error-уровень), который на Android до core-log
> доходит.** Полный разбор — в `PROBE-V2-TASK.md`.

- `submodules/wireguard-go/conn/bind_std.go`: `StdNetBind.Open` больше **не** печатает
  в stderr; вместо этого геттер `OffloadState()` — отдаёт `ipv4/ipv6 Tx/RxOffload` +
  `dispatch`. Offload-логика не тронута.
- `transport/wireguard/endpoint.go`: сразу после `IpcSet` (device поднят, `bind.Open`
  отработал) логирует строку через `logger.Errorf` (→ `options.Logger.Error` →
  core-log). Только no-detour путь (`*conn.StdNetBind`); detour-путь (`ClientBind`)
  offload не имеет и строку не печатает.

Формат строки (одна; обёртка `Errorf` прогоняет формат через `strings.ToLower`,
поэтому строка приходит в **нижнем регистре**, маркер — `lx-gro-probe`):

```
lx-gro-probe: goos=android txoffload4=<bool> rxoffload4=<bool> txoffload6=<bool> rxoffload6=<bool> dispatch=<split|single>
```

- `rxoffload4`/`rxoffload6` — главное поле: включился ли GRO на приёме.
- `dispatch` — какую RX-ветку берёт диспетчер: `single` = одиночный `ReadMsgUDP` без
  разбора GRO (баг), `split` = разбор коалесированных сообщений.
- На Android ожидаем `goos=android` и `dispatch=single` всегда. Вопрос только в
  `rxoffload*`.

---

## Готовый artefact (собран CI)

Probe-`.aar` собран через on-demand CI (`gh workflow run lx-build.yml
-f target=android-aar -f branch=lx-gro-probe-010`) и лежит в локальном дереве:

| Файл | SDK | sha256 (v2.1) |
|------|-----|--------|
| `dist/lx-gro-probe-010/libbox-gro-probe-010.aar` | 23 (main) | `9f609cac40782065bcc0d3c9ebde6f6ad969246a177b010c4bb13cff61cd2ad1` |
| `dist/lx-gro-probe-010/libbox-legacy-gro-probe-010.aar` | 21 (legacy) | `432d3e0a6f8200214b3c99a486ee2d32872df18836b9cd93efa657763c9befcb` |

Собрано из `lx-gro-probe-010` @ `6177c6b2` (v2.1, `Errorf`) — подтверждено по логу
checkout CI. Маркер `LX-GRO-PROBE` вкомпилирован в `libbox.so`.
Подложить `.aar` в `app/libs/` приложения, собрать debug-APK — и переходить к прогону.

## Откуда берётся probe

- **submodule** `wireguard-go`: ветка `gro-probe-010`, commit `21423f6` (probe-лог в
  `conn/bind_std.go`). Базовый pin lx — `27290b6`.
- **main repo**: ветка `lx-gro-probe-010` бампит pin submodule на probe-commit. `lx`
  не тронута (на ней — только CI-файл `lx-build.yml`).
- Голый diff probe рядом: `gro-probe.patch`.

Пересобрать в любой момент:
```bash
gh workflow run lx-build.yml -f target=android-aar -f branch=lx-gro-probe-010
gh run download <run-id> -D dist/lx-gro-probe-010
```

## Как прогнать (команда LxBox)

1. Подложить готовый `.aar` (см. выше) в сборку приложения.
2. Включить core-log: **App Settings → Diagnostics → `core_logs_enabled=true`**.
   (Уровень `log.level` менять НЕ нужно — probe идёт через Error, а Error в core-log
   на Android проходит независимо от `log.level`.)
3. Поднять профиль с **WG-endpoint без `detour`** (тот самый конфиг, где download
   мёртв).
4. Запустить туннель, дать пройти трафику, снять core-log через Debug API
   (строка в **нижнем регистре** — ищем по `gro-probe`):

   ```bash
   GET /logs?source=core&q=gro-probe
   ```

5. Скопировать строку `lx-gro-probe: …` и вернуть её нам.

> Замечание: строка пишется один раз при поднятии device (старт туннеля), после
> `IpcSet`. Если в core-log пусто — либо путь пошёл в `ClientBind` (проверьте, что
> endpoint **без** `detour`), либо не включён `core_logs_enabled`.

---

## Как читать результат

| core-log строка | Вывод | Дальше |
|---|---|---|
| `rxoffload4/6=true` (+ `dispatch=single`) | GRO взведён, но не разбирается — **корень №1 подтверждён** | чиним submodule (гейт `UDP_GRO` за `!android`) |
| `rxoffload4/6=false` | GRO на этом ядре не активируется — **корень №1 мёртв** | переключаемся на кандидат №2 (тихий хэндовер, `monitor.go`) |

Бонус для физического repro (по желанию): прогнать на **стабильном Wi-Fi** и на
**сотовой** — если download мёртв только на сотовой/при переключениях, это в пользу
кандидата №2 независимо от `rxOffload`.

---

## После замера

Probe — временный. Удалить из `bind_std.go`: две `fmt.Fprintf(... "LX-GRO-PROBE" ...)`
строки, функцию `groProbeDispatch()`, импорт `os` (если он добавлялся только под
probe). Это submodule-правка — не должна попасть в релизное ядро.
