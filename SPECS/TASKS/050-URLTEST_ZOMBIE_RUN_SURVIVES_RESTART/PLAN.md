# PLAN: 050 — URLTEST_ZOMBIE_RUN_SURVIVES_RESTART

## Порядок фиксов задан зависимостью, а не удобством

`batch.Wait()` — это чистый `wg.Wait()`
(`sing@v0.8.12.../common/batch/batch.go:76`): он **не слушает отмену ctx**.
Отмена контекста, который `batch.New` возвращает, доходит только до тех задач,
которые сами её проверяют.

Отсюда вывод, определяющий весь план: **уровень 3 в одиночку проблему не решает.**
Даже отменяя ctx группы в `Close()`, мы не разблокируем `Wait()`, пока сами
задачи не научатся завершаться по отмене. А задача завершится по отмене, только
если её разбудит уровень 1 или 2. Поэтому 1 и 2 — обязательные, 3 — то, что
делает результат наблюдаемым и закрывает диагностический критерий.

## Уровень 1 — дедлайны на XHTTP-conn (корень)

`transport/v2rayxhttp/conn.go`. Сейчас `streamConn` и `splitConn` возвращают
`os.ErrInvalid` на все три `Set*Deadline`. Заменяем на рабочую реализацию.

Механика: `io.Pipe` не умеет дедлайны, но `PipeWriter.CloseWithError(err)`
мгновенно разблокирует висящий `Write` этой ошибкой. Значит дедлайн = таймер,
который по истечении вызывает `CloseWithError(os.ErrDeadlineExceeded)`.

```go
type deadlinePipe struct {
	writer *io.PipeWriter
	access sync.Mutex
	timer  *time.Timer
}

// setWriteDeadline (пере)взводит таймер: истёкший дедлайн рвёт висящий Write
// ошибкой os.ErrDeadlineExceeded, нулевое время снимает дедлайн.
func (d *deadlinePipe) setWriteDeadline(t time.Time) error {
	d.access.Lock()
	defer d.access.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if t.IsZero() {
		return nil
	}
	delay := time.Until(t)
	if delay <= 0 {
		d.writer.CloseWithError(os.ErrDeadlineExceeded)
		return nil
	}
	d.timer = time.AfterFunc(delay, func() {
		d.writer.CloseWithError(os.ErrDeadlineExceeded)
	})
	return nil
}
```

Read-сторона: у `streamConn` чтение упирается в `<-c.created` и затем в
`reader.Read`; у `splitConn` — сразу в `reader.Read`. Оба — тела HTTP-ответа,
их прерывает `reader.Close()`. `SetReadDeadline` реализуем тем же таймером,
закрывающим reader (для `streamConn` — с учётом того, что reader может быть
ещё не привязан: тогда закрываем после `created`, либо взводим флаг, который
`Read` проверит).

`SetDeadline` = оба. Таймеры гасятся в `Close`, чтобы не течь.

**Почему не «просто убрать `NeedAdditionalReadDeadline`»:** этот метод сообщает
верхним слоям, что conn дедлайнов не поддерживает и нужна внешняя обёртка. После
R1 его следует пересмотреть, но это отдельное решение — на этапе реализации
проверить, кто его читает, и не менять поведение вслепую.

`packetConn` (packet-up) уже отменяем — он держит `c.ctx` и проверяет его в
`sendPacket`; там правок не требуется, только сверить, что `Set*Deadline` не
мешают (сейчас у него дедлайнов нет — добавить по тому же образцу для
единообразия, если это не потянет лишнего).

## Уровень 2 — отмена ctx на диале рвёт хендшейк

Две точки, обе наши.

**2a. `transport/v2rayxhttp/conn.go` — сторож ctx в `dialStreamOne`/`dialStreamUp`.**
Conn отдаётся наверх до того, как `RoundTrip` реально поднял поток. Пока conn не
стал рабочим, отмена ctx диала должна закрывать pipe:

```go
// lx: 050 — conn отдан до готовности RoundTrip; пока поток не поднялся,
// отмена диал-контекста обязана освобождать висящий Write (иначе хендшейк
// поверх этого conn не прерывается ничем).
go func() {
	select {
	case <-ctx.Done():
		conn.closeWithError(ctx.Err())
	case <-conn.created:
	}
}()
```

Сторож обязан завершаться по `conn.created` — иначе он переживёт диал и будет
рвать уже рабочее пользовательское соединение при отмене диал-контекста
(это и есть требование R4).

**2b. `protocol/vless/lx_encryption.go` — дедлайн вокруг `Handshake`.**
`ClientInstance.Handshake` не принимает ctx (провод SPEC 032 не трогаем), но
теперь conn под ним умеет дедлайны — значит хендшейк можно ограничить снаружи:

```go
func (h *vlessDialer) wrapEncryption(ctx context.Context, conn net.Conn) (net.Conn, error) {
	if h.encryption == nil {
		return conn, nil
	}
	// lx: 050 — Handshake работает с голым net.Conn и внутри пишет в conn без
	// проверки ctx; ограничиваем его дедлайном conn'а и снимаем дедлайн после.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	...
}
```

Сигнатура `wrapEncryption` получает `ctx` — вызов в
`protocol/vless/outbound.go:190` правится на одну строку (файл upstream-owned,
касание минимальное, как и задумано разделением SPEC 032).

## Уровень 3 — `Close()` группы отменяет прогон

`protocol/group/urltest.go`.

1. `URLTestGroup` получает собственный child-ctx и его `cancel`:
   в `NewURLTestGroup` — `ctx, cancel := context.WithCancel(ctx)`, поля
   `ctx`/`cancel`.
2. `Close()` вызывает `g.cancel()` (под тем же `g.access`, где уже гасится
   ticker). Отмена доходит до задач `testNodes` через `testCtx`, а те — до
   висящего `Write` через уровни 1–2.
3. `testNodes`: batch создаётся на `ctx`-аргументе, а `testCtx` — от `g.ctx`.
   Рассогласование убирается: `testCtx` строится от того же ctx, что и batch
   (`context.WithTimeout(ctx, C.TCPTimeout)` — ctx, возвращённый `batch.New`).
   Тогда и первая ошибка в batch, и отмена группы гасят остальные задачи.
4. Проверить `PostStart`: `go g.CheckOutbounds(false)` остаётся, но теперь
   отменяем — принудительная регистрация горутины в WaitGroup не нужна, если
   критерий приёмки №3 выполняется отменой. Если на стенде окажется, что не
   выполняется, — добавить `sync.WaitGroup` и ждать её в `Close` с потолком.

## Изменяемые файлы

| Файл | Зона | Что |
|---|---|---|
| `transport/v2rayxhttp/conn.go` | наш (фича 002) | Дедлайны `streamConn`/`splitConn`, сторож ctx в `dialStreamOne`/`dialStreamUp` |
| `protocol/vless/lx_encryption.go` | наш (фича 012) | `wrapEncryption(ctx, conn)` + дедлайн на хендшейк |
| `protocol/vless/outbound.go` | upstream | Одна строка вызова `wrapEncryption` |
| `protocol/group/urltest.go` | upstream (фича 007) | child-ctx + `cancel` в `Close`, согласование `testCtx` с batch-ctx |
| `transport/v2rayxhttp/*_test.go` | наш | Стенд-репро: сервер принимает TCP и не читает тело |
| `protocol/group/urltest_*_test.go` | наш | Регрессия: после `Close()` горутины прогона уходят |

Зона касания upstream — минимальная: одна строка в `outbound.go` и локальные
правки жизненного цикла в `urltest.go`. Оба места помечаются `// lx: 050`.

## Стенд

Узлы пользователя не нужны и недоступны. Репро строится локально:

- `net.Listener`, который принимает TCP-соединение и **не читает** из него
  ничего (для TLS-варианта — принимает и молчит после ClientHello);
- узел `vless + xhttp (stream-one) + encryption` на этот listener;
- red: `urltest.URLTest` не возвращается, горутина живёт после `box.Close()`;
- green: возврат ошибкой в пределах таймаута, `runtime.NumGoroutine` возвращается
  к базовому уровню.

Тот же стенд закрывает критерий №2 (отмена ctx в фазе хендшейка) и №5 (память).

## Риски

- **Ложные разрывы живых соединений** (R4). Основной риск — сторож ctx в 2a,
  переживший диал. Митигация: выход сторожа по `conn.created`, плюс тест на
  живом локальном XHTTP-сервере, что отмена диал-контекста после успешного
  диала не рвёт поток.
- **`NeedAdditionalReadDeadline`.** Менять его значение без разбора потребителей
  нельзя — вынесено в отдельный шаг с проверкой вызовов.
- **Гонка таймера и `Close`.** Таймеры дедлайнов обязаны гаситься в `Close` под
  тем же мьютексом; проверяется `-race`.
