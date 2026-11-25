# SPEC: 157 — DNS_MULTIPLEXER_WRITE_LOCK

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (условие снятия)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — потоковый DNS через прокси пишется в одно соединение из многих горутин без синхронизации, с обеих сторон туннеля: клиентский `queryMultiplexer` DNS-транспорта `tcp` и серверный `HandleStreamDNSRequest` (`hijack-dns`, `dns`-outbound, `resolved`). Прокси-соединения VLESS и sing-mux пишут свой заголовок первым `Write` под голым `bool`, и поток DNS ломается |
| Статус | I (implemented) — 2026-11-17: клиентская половина (`dns/transport/multiplexer.go`); 2026-11-18: серверная половина (`protocol/dns/stream_write_lock_lx.go` и три вызывающих). Юниты красные до правки и зелёные после; в поле клиентская половина `short read` не убрала (§4), серверная на выходных узлах не проверялась. **Патч снят синком [166](../166-UPSTREAM_SYNC_1_14_4_PLUS_146/SPEC.md) (2026-11-26):** апстрим `3d071e6e4` сериализует запись в `queryMultiplexer` и в ответах потокового DNS (`NewDNSConnection`); `stream_write_lock_lx.go` удалён, правки ниже описывают снятый код. Стражи (`multiplexer_write_lock_lx_test.go`, `handle_write_lock_lx_test.go`, `route/dns_stream_write_lock_lx_test.go`, `service/resolved/stream_write_lock_lx_test.go`) остаются и проходят на апстримном коде |
| Ветка | `lx-spec157-dns-multiplexer-write-lock` → `lx` |
| Build-tag | — (ядро: `dns/transport`, `protocol/dns`, `route`, `service/resolved`) |
| Источник | [issue #77](https://github.com/Leadaxe/sing-box-lx/issues/77): роутер OpenWrt (тот же, что в #53), `v1.14.3-lx.23`; DNS-сервер `tcp` 8.8.4.4 с `detour` на `urltest`-группу из четырёх узлов (vless + reality с vision, vless + reality поверх HTTP-транспорта, два shadowsocks + shadowtls с h2mux); выходные узлы — sing-box 1.14.x с `hijack-dns`; за 1,5 ч — 210 строк `router: process DNS packet: dns: short read` пачками до 82 в секунду |
| Связано | [046](../046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md) (тот же `ConnPool`/`ExchangeAsync`), [077](../077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) (DNS-транспорт с `detour` через прокси-узел) |

DNS-сервер `tcp` с `detour` через прокси теряет запросы пачками: в логе — серии
`dns: short read` в одну секунду, у пользователя — сайт открывается с первого раза
секунд за пять, пока ОС или браузер не повторят DNS-запрос.

## 1. Причина

Одна и та же гонка с двух сторон туннеля. Общее у обеих половин: прокси-соединения
дописывают заголовок протокола к первому `Write`, а признак «заголовок уже
записан» — обычный `bool` без синхронизации. Голый TCP параллельные `Write`
переживает (`net.TCPConn` держит свой замок), поэтому апстримные тесты на
`net.Dial` бага не видят.

### 1.1. Клиент: запросы

`dns/transport/multiplexer.go`, `queryMultiplexer.exchangeAsync`: запрос пишется в
общее соединение (`options.write` → `WriteMessage` → один `conn.Write`) без замка.
Пишут сразу многие горутины: `ConnPool.AcquireShared` отдаёт только что открытое
соединение всем ожидающим одновременно, а у нас `dispatch` к тому же запускает
`exchangeAsync` в отдельной горутине. Чтение сериализовано — его ведёт одна
`recvLoop`.

«Ранние» соединения клиента:

- `sing-vmess/vless.(*Conn).Write` — `if !c.requestWritten { WriteRequest(...) }`
  (`protocol/vless/outbound.go` дайлит через `DialEarlyConn`);
- `sing-mux.(*clientConn).Write` — то же с заголовком `StreamRequest`
  (все outbound'ы с `multiplex`);
- `shadowsocks` без mux дайлит через `method.DialEarlyConn`.

На свежем соединении заголовок запроса уходит дважды, сервер прокси снимает
первый, а второй отдаёт DNS-серверу как данные. Такой поток сервер закрывает;
клиент видит `EOF`, а его уровнем error не пишут (`E.IsClosedOrCanceled`), — в
логе этой половины не видно.

### 1.2. Сервер: ответы — источник `short read`

`protocol/dns/handle.go`, `HandleStreamDNSRequest`: каждый ответ пишется своей
горутиной — `go writeStreamResponse(conn, response)` — без замка. Серверные
соединения прокси дописывают к первому ответу свой заголовок:

- `sing-vmess/vless.(*serverConn).Write` — `if !c.responseWritten`, заголовок
  `00 00` (версия и длина аддонов);
- `sing-mux.(*serverConn).Write` — то же, байт статуса `00`.

`HandshakeSuccess` ни один из них не реализует, так что
`N.ReportConnHandshakeSuccess` в `hijackDNSStream` заголовок заранее не пишет.
Когда на свежее соединение одновременно готовы несколько ответов, заголовок
уходит дважды. Клиент снимает первый, а лишние `00 00` (у mux — `00` плюс
старший байт длины) читает как префикс длины 0, и `ReadMessage`
(`dns/transport/tcp.go`) возвращает `mDNS.ErrShortRead`. Ровно эта строка и
видна в логе роутера.

Почему пачками. Одно испорченное соединение роняет все ожидающие на нём запросы
разом (`recvLoop` → `Invalidate` → `completeConnDone`); запросы первой попытки
тихо перезапускаются, в лог попадают создатель соединения и повторы, упавшие на
следующем свежем соединении. 190 строк в одну секунду — это одно-два испорченных
соединения, а не 190 порч. Триггер — всплеск параллельных запросов на свежем
соединении: в поле пачка пришла сразу после пробуждения ноутбука.

`vless` с `flow: xtls-rprx-vision` не задет: `VisionConn.Write` держит свой
`writeAccess`, и первый `Write` нижнего соединения идёт под ним.

## 2. Фикс

### 2.1. Клиент

`dns/transport/multiplexer.go`, два места с маркером `// lx: SPEC 157`:

- в `multiplexConn` — поле `writeAccess sync.Mutex`;
- в `exchangeAsync` — `options.write` под этим замком.

```go
conn.writeAccess.Lock()
writeErr := m.options.write(conn, message, queryId)
conn.writeAccess.Unlock()
```

### 2.2. Сервер

`protocol/dns/stream_write_lock_lx.go` (новый файл, `handle.go` не тронут): обёртка
`serialWriteConn` (`NewSerialWriteConn`) с `Write` под своим замком. Вызывающие
оборачивают соединение один раз на поток, перед циклом
`HandleStreamDNSRequest` (`// lx: SPEC 157`):

- `route/dns.go`, `hijackDNSStream` — после `ReportConnHandshakeSuccess`;
- `protocol/dns/outbound.go`, `Outbound.NewConnection`;
- `service/resolved/service.go`, `Service.NewConnection`.

Чтение, дедлайны и закрытие идут по исходному соединению, обёртка трогает только
`Write`. Сигнатура `HandleStreamDNSRequest` не меняется.

### 2.3. Что не меняется и цена

- **Чтение.** `recvLoop` по-прежнему одна на соединение и замок не берёт; на
  сервере запросы читает один цикл.
- **Пайплайнинг.** Запросы и ответы всё так же идут в одно соединение без
  ожидания; сериализуется только запись, а она — один `conn.Write` на сообщение.
- **`exchangeSingle` и проба reuse** пишут из одной горутины в своё соединение —
  замок им не нужен, их код не тронут.
- **Ошибка записи** обрабатывается как раньше.

Цена: запись, упёршаяся в медленный прокси, задерживает следующие записи в то же
соединение. Раньше они так же стояли бы внутри `conn.Write` нижнего соединения,
только вперемешку. Под замком других замков не берут, `Close` и `Invalidate` его
не трогают — дедлока нет.

Корень на стороне библиотек (`requestWritten`/`responseWritten` без синхронизации
в sing-vmess и sing-mux) **не трогаем**: контракт `net.Conn` они нарушают, но
форк-сабмодули ради этого не заводим, а чинить у потребителя, который пишет
конкурентно, дешевле.

Серверная половина действует там, где ядро отвечает на DNS через прокси, — то
есть на выходных узлах. Роутеру из #77 она поможет, только когда узлы обновятся;
клиентская половина нужна самому роутеру.

## 3. Условие снятия

Каждую половину — отдельно:

- **клиент:** апстрим sing-box сериализует запись в `queryMultiplexer` сам (замок,
  очередь записи, одна пишущая горутина) **или** мультиплексор перестанет писать в
  одно соединение из нескольких горутин;
- **сервер:** апстрим сериализует запись ответов в `HandleStreamDNSRequest` (или
  пишет их из одной горутины) **или** sing-vmess и sing-mux сделают первый `Write`
  потокобезопасным.

При мерже: конфликт в `multiplexConn`, `exchangeAsync` или циклах вызывающих =
сверить с §2. Тесты §4 сторожат замок в `exchangeAsync`, обёртку и каждую из
трёх точек вызова: потерянная на мерже строка `NewSerialWriteConn` роняет тест
своего пакета.

## 4. Проверка

- `dns/transport/multiplexer_write_lock_lx_test.go` (клиент) — настоящие
  VLESS-клиент и -сервер из `sing-vmess` и h2mux-клиент и -сервер из `sing-mux` в
  процессе, за ними TCP-DNS-сервер с пайплайнингом на 127.0.0.1; 20 раундов по
  новому `queryMultiplexer` с боевыми `WriteMessage`/`ReadMessage`, в каждом 16
  параллельных запросов:
  - `TestLxMultiplexerConcurrentWritesOnVLESSConn`: до правки 238–267 из 320
    запросов падают, сервер видит 16–18 испорченных потоков; после — 0 из 320,
    `-count=20`;
  - `TestLxMultiplexerConcurrentWritesOnMuxConn`: до правки 300 из 320, 20
    испорченных потоков; после — 0 из 320, `-count=20`.
- `protocol/dns/handle_write_lock_lx_test.go` (сервер) — те же VLESS- и
  h2mux-сервер, за ними `HandleStreamDNSRequest` на роутере-заглушке, который
  отвечает каждому запросу своей горутиной; клиент пишет 16 запросов из одной
  горутины и читает 16 ответов, 50 свежих соединений:
  - `TestLxStreamDNSConcurrentResponsesOnVLESSConn`: до правки 44–45 из 50
    соединений падают с `dns: short read`; после — 0, `-count=20`;
  - `TestLxStreamDNSConcurrentResponsesOnMuxConn`: до правки 34–43 из 50 с
    `dns: short read`; после — 0, `-count=20`.
- Точки вызова. Серверные тесты выше идут через настоящий
  `Outbound.NewConnection` (`protocol/dns/outbound.go`): без обёртки в нём —
  43 из 50 соединений с `dns: short read`. Для двух других — по тесту в своём
  пакете на `net.Pipe` с соединением, которое ловит `Write`, начатый во время
  другого `Write`; 16 запросов подряд, ответы из 16 горутин:
  - `route/dns_stream_write_lock_lx_test.go`,
    `TestLxHijackDNSStreamSerializesResponseWrites`: без обёртки в
    `hijackDNSStream` падает, с ней зелёный под `-race`, `-count=5`;
  - `service/resolved/stream_write_lock_lx_test.go`,
    `TestLxResolvedStreamSerializesResponseWrites`: пакет только под Linux,
    локально проверена компиляция (`GOOS=linux go vet`), исполняется в lx-ci.
- `go vet` и `go test` по `./protocol/dns/ ./dns/... ./route/... ./service/resolved/`
  — зелёные. `TestSurvivalSuccessRestoresService` (`dns/transport/group`)
  нестабилен и без правки (9 падений из 40 прогонов на `lx` без этой задачи),
  мультиплексор не использует.
- `go build ./...`, сборка `cmd/sing-box` под `LX_TAGS`, `sing-box check` по
  `lx-test/config/*.json` — проходят (проверено для клиентской половины).
- `-race`: `go test -race -count=3 -run TestLx ./dns/transport/ ./protocol/dns/`
  под `LX_TAGS` — зелёный (darwin/arm64, 2026-11-23).
- Поле, клиентская половина: ядро `v1.14.3-lx.23` + §2.1 на роутере из #77 около
  14 часов (2026-11-17/18). `short read` не исчез — 190 строк в одну секунду
  сразу после пробуждения ноутбука в LAN и около 30 в следующий час. Это и
  привело к §1.2. Нагрузочный A/B с рестартами ядра до того был неинформативен:
  провалы шли за деградацией туннеля, `short read` не воспроизвёлся.
- Поле, серверная половина: выходные узлы с §2.2 — не проверялось.

## 5. Файлы

- `dns/transport/multiplexer.go` — `writeAccess` в `multiplexConn`, запись под ним в `exchangeAsync` (`// lx: SPEC 157`)
- `dns/transport/multiplexer_write_lock_lx_test.go` — регресс-тесты клиента (новый)
- `protocol/dns/stream_write_lock_lx.go` — `serialWriteConn`, `NewSerialWriteConn` (новый)
- `protocol/dns/outbound.go`, `route/dns.go`, `service/resolved/service.go` — обёртка соединения перед циклом (`// lx: SPEC 157`)
- `protocol/dns/handle_write_lock_lx_test.go` — регресс-тесты сервера (новый)
- `route/dns_stream_write_lock_lx_test.go`, `service/resolved/stream_write_lock_lx_test.go` — стражи точек вызова (новые)
- `.github/workflows/lx-ci.yml` — `./protocol/dns/` и `./service/resolved/` в шаге тестов: без строки пакет не гоняется нигде
