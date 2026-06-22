# 012 — PROBE: инструментовка download-копирования (`LX_CONN_TRACE`)

Цель зонда — развести **изнутри ядра** место застревания download для зомби-соединения,
которое синхронный pcap локализовал, но не смог разложить (см. [SPEC.md](SPEC.md),
раздел «Оговорки о строгости»). Pcap показал: 777B пришли в ядро на `wlan0`, до
приложения на `tun0` не дошли. Зонд отвечает на вопрос **где именно** на пути
`remoteConn → conn`.

Артефакт целиком: [instrumentation.patch](instrumentation.patch).

---

## Что зондируем

`route/conn.go` → `connectionCopy(ctx, source, destination, direction, …)`:

- download-горутина запускается строкой `go m.connectionCopy(ctx, remoteConn, conn, true, …)`
  → `source = remoteConn` (upstream, уже **расшифрованный** reality/vless),
  `destination = conn` (gVisor tun-сокет приложения), `direction = true`.
- тело: `bufio.CopyWithIncreateBuffer(destination, source, …)` (sing v0.8.10) —
  голый Read→Write без read-deadline.

Зонд считает **раздельно**:
- `read`  — байты, отданные `source.Read` (для download: plaintext, который proxy успел расшифровать и выдать);
- `write` — байты, принятые `destination.Write` (для download: что реально ушло в tun-сокет приложения).

---

## Как читать вывод

Лог на уровне **INFO** (виден при любом боевом log.level). Две формы:

```
lx-trace download tick#<k>: read=<N> write=<M>                       ← периодически, ПОКА copy жив
lx-trace download final:    read=<N> write=<M> err=<...> timeout=<bool>   ← один раз, при возврате copy
```

`tick#k` печатается раз в период (см. ниже) — это снимок ВО ВРЕМЯ зависания, ради
висящего зомби. `final` — итог при закрытии/отвисании. Обе строки читаются по одной
таблице. `upload`-направление логируется симметрично.

Разбор для зомби-соединения (download, ↓0 на tun0):

| Снимок | Где застряло | Трактовка |
|---|---|---|
| `read=0 write=0` | **выше** copy | proxy (reality/vless) не отдал НИ ОДНОГО расшифрованного байта, хотя pcap показал зашифрованные 777B → дефект расшифровки/фрейминга, НЕ в `connectionCopy` |
| `read>0 write=0` | **запись в tun** | байты из upstream прочитаны, но не записаны/не флашатся в gVisor tun-сокет → подтверждает гипотезу SPEC «застряло в `remoteConn→conn`» |
| `read>0 write>0` | copy шёл | смотреть на `err`/`timeout` — копирование двигалось, причина в завершении/лаге, не в самом stuck |

Ключ к развилке pcap: обёртка стоит на **расшифрованном** `remoteConn`, поэтому
`read` — это plaintext. `read=0` при наличии зашифрованного трафика в pcap снимает
с `connectionCopy` подозрение и переводит расследование выше по стеку (proxy-слой).

---

## Механика (почему обёртка перехватывает, а не обходится)

Обёртка `lxTraceConn` (`route/conn_trace_lx.go`) встраивает `net.Conn`, считает байты
в `Read`/`Write`, и **намеренно НЕ реализует** `Upstream()` / `ReaderReplaceable()` /
`WriterReplaceable()` / `SyscallConn()`. Это критично — иначе её обойдут:

1. **`N.UnwrapCountReader`** (sing `common/network/counter.go`) разворачивает обёртку
   только если она реализует `ReaderWithUpstream` **и** `ReaderReplaceable()==true`.
   Без них разворот останавливается на `lxTraceConn` → её `Read` в цепочке.
2. **`copyDirect`** (sing `common/bufio/copy_direct.go`) включает zero-copy `splice`
   только при `SyscallAvailableForRead/Write` (нужен `syscall.Conn`). Go-проброс
   встроенного `net.Conn` НЕ даёт обёртке `SyscallConn()` → `copyDirect` возвращает
   `handed=false` → копирование идёт буферным путём через `Read`/`Write` обёртки.

Итог: под трейсом счётчики **гарантированно** видят весь поток. Цена — на время
диагностики теряется zero-copy splice (копирование принудительно буферное). Поэтому
зонд под env-гейтом и НЕ для постоянной работы.

---

## Гейт и оверхед

`LX_CONN_TRACE` (`route/conn_trace_lx.go`, `sync.OnceValue` → `time.Duration` = период тика):
- `""`, `"0"`, `"false"` → выключено: обёртки в copy-цепочку **не ставятся вовсе**,
  тикер не запускается, путь копирования и поведение закрытия не меняются, оверхед нулевой;
- `"1"`, `"true"` → включено с дефолтным периодом тика **5s** (`lxConnTraceDefaultTick`);
- длительность (`"3s"`, `"500ms"`, `"10s"`) → включено с этим периодом тика;
- нераспознанное непустое значение → включено с дефолтным периодом (fail-open).

Тик запускается отдельной горутиной на каждое copy-направление; останавливается
через `defer close(stop)` при возврате `connectionCopy`. Счётчики атомарны
(проверено `go test -race`).

---

## Как собрать и прогнать

1. Применить артефакт к чистому релизному дереву (`route/conn.go` + новый файл):
   ```
   git apply SPECS/012-TCP_DOWNLINK_STALL_ZOMBIE_CONNS/instrumentation.patch
   # либо вручную: diff для conn.go + создать route/conn_trace_lx.go из хвоста патча
   ```
   (на момент написания зонд уже лежит в рабочем дереве: `M route/conn.go`,
   `?? route/conn_trace_lx.go`.)
2. `gofmt -l route/conn.go route/conn_trace_lx.go` → должно быть пусто (lx-правило).
   `go build ./...` и `go vet ./route/` → проходят.
3. Собрать lx-ядро для устройства (обычный build-флоу LxBox/gomobile).
4. На CPH2411 выставить env ядра `LX_CONN_TRACE=1`, прогнать репро WhatsApp/Telegram.
5. Снять core-лог через `/logs?source=core` (на CPH2411 stderr→/dev/null), искать
   строки `lx-trace download:` для зомби-соединения.

---

## Оговорки

1. Зонд меняет тайминги (буферный путь вместо splice) — он диагностический, не для
   постоянной работы. Вне гейта код нетронут.
2. `lxTraceConn` (как `net.Conn`-обёртка) не пробрасывает `N.WriteCloser.CloseWrite()`
   → под трейсом half-close деградирует в полный `Close()`. На диагностику не влияет;
   ещё одна причина не держать `LX_CONN_TRACE` включённым в проде.
3. **Момент логирования — две формы, и почему обе нужны.** Лог `final` стоит в хвосте
   `connectionCopy`, ПОСЛЕ возврата `CopyWithIncreateBuffer`. А для висящего зомби
   copy НЕ возвращается — он заблокирован на `Read` (в этом и суть бага). Поэтому:
   - `final` для висящего зомби **не выйдет**, пока соединение не отвиснет (наблюдали
     ↓0→↓2820) или его не разорвут (ручной close / `DELETE /connections` / переоткрытие).
   - чтобы видеть stuck В МОМЕНТ зависания, добавлен **периодический `tick#k`** (раз в
     период `LX_CONN_TRACE`, дефолт 5s): он печатает текущие `read`/`write`, пока copy
     жив. Для висящего зомби увидим серию `tick#1 read=… write=0`, `tick#2 …` — это и
     есть прямое доказательство застревания во времени, без необходимости рвать связь.
   - на развилку `read=0` vs `read>0,write=0` обе формы отвечают одинаково; `tick`
     просто показывает её раньше и без ручного вмешательства.
