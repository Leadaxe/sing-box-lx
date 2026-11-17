# SPEC: 157 — DNS_MULTIPLEXER_WRITE_LOCK

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (условие снятия)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный `queryMultiplexer` DNS-транспорта `tcp` пишет запросы в общее соединение из многих горутин без синхронизации; через прокси-outbound'ы с «ранним» соединением поток DNS ломается |
| Статус | I (implemented) — 2026-11-17: `// lx:` блок в `dns/transport/multiplexer.go`, юниты красные до правки и зелёные после; на роутере A/B не прогонялся |
| Ветка | `lx-spec157-dns-multiplexer-write-lock` → `lx` |
| Build-tag | — (ядро `dns/transport`) |
| Источник | [issue #77](https://github.com/Leadaxe/sing-box-lx/issues/77): роутер OpenWrt (тот же, что в #53), `v1.14.3-lx.23`; DNS-сервер `tcp` 8.8.4.4 с `detour` на `urltest`-группу из четырёх узлов (vless + reality с vision, vless + reality поверх HTTP-транспорта, два shadowsocks + shadowtls с h2mux); за 1,5 ч — 210 строк `router: process DNS packet: dns: short read` пачками до 82 в секунду |
| Связано | [046](../046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md) (тот же `ConnPool`/`ExchangeAsync`), [077](../077-XHTTP_DIAL_CTX_CONTRACT/SPEC.md) (DNS-транспорт с `detour` через прокси-узел) |

DNS-сервер `tcp` с `detour` через прокси теряет запросы пачками: в логе — серии
`dns: short read` в одну секунду, у пользователя — сайт открывается с первого раза
секунд за пять, пока ОС или браузер не повторят DNS-запрос.

## 1. Причина

Две детали, апстримные обе, складываются в порчу потока:

1. `dns/transport/multiplexer.go`, `queryMultiplexer.exchangeAsync`: запрос пишется в
   общее соединение (`options.write` → `WriteMessage` → один `conn.Write`) без замка.
   Пишут сразу многие горутины: `ConnPool.AcquireShared` отдаёт только что
   открытое соединение всем ожидающим одновременно, а у нас `dispatch` к тому же
   запускает `exchangeAsync` в отдельной горутине. Чтение при этом
   сериализовано — его ведёт одна `recvLoop`.
2. Прокси-outbound'ы отдают «ранние» соединения: заголовок протокола уходит вместе
   с первым payload, а признак «заголовок уже записан» — обычный `bool` без
   синхронизации:
   - `sing-vmess/vless.(*Conn).Write` — `if !c.requestWritten { WriteRequest(...) }`
     (`protocol/vless/outbound.go` дайлит через `DialEarlyConn`);
   - `sing-mux.(*clientConn).Write` — то же с заголовком `StreamRequest`
     (все outbound'ы с `multiplex`);
   - `shadowsocks` без mux дайлит через `method.DialEarlyConn`.

   Голый TCP параллельные `Write` переживает (`net.TCPConn` держит свой замок),
   поэтому апстримные тесты мультиплексора на `net.Dial` бага не видят.

Итог: если на свежем соединении сразу несколько запросов, больше одного видят
`requestWritten == false` и пишут свой заголовок. Сервер прокси снимает первый,
а второй пересылает резолверу как данные — TCP-DNS-кадрирование сломано.
Соединение умирает, `recvLoop` роняет все ожидающие на нём запросы разом
(`Invalidate` → `completeConnDone`), повторы снова гонятся на следующем свежем
соединении. Отсюда пачки в логе.

Что именно видно в логе. `EOF`, таймауты и отмены уровнем error не пишутся
(`E.IsClosedOrCanceled` в `route/dns.go`), видна только та часть, где из
сломанного потока прочитан префикс длины меньше 10 байт, — `mDNS.ErrShortRead`
из `ReadMessage` (`dns/transport/tcp.go`). Какие именно байты присылает публичный
резолвер на испорченный поток, локально не воспроизведено: связь гонки с `short
read` показана механизмом и полевой картиной, A/B на роутере — впереди.

`vless` с `flow: xtls-rprx-vision` не задет: `VisionConn.Write` держит свой
`writeAccess`, и первый `Write` нижнего `vless.Conn` идёт под ним.

## 2. Фикс

`dns/transport/multiplexer.go`, два места с маркером `// lx: SPEC 157`:

- в `multiplexConn` — поле `writeAccess sync.Mutex`;
- в `exchangeAsync` — `options.write` под этим замком.

```go
conn.writeAccess.Lock()
writeErr := m.options.write(conn, message, queryId)
conn.writeAccess.Unlock()
```

Что от этого не меняется:

- **Чтение.** `recvLoop` по-прежнему одна на соединение и замок не берёт.
- **Пайплайнинг.** Запросы всё так же уходят в одно соединение без ожидания
  ответов; сериализуется только запись, а она — один `conn.Write` на запрос.
- **`exchangeSingle` и проба reuse** пишут из одной горутины в своё соединение —
  замок им не нужен, их код не тронут.
- **Ошибка записи** обрабатывается как раньше (`take` → `Invalidate` → повтор на
  новом соединении для переиспользованного).

Цена: запись, упёршаяся в медленный прокси, задерживает следующие записи в то же
соединение. Раньше они так же стояли бы внутри `conn.Write` нижнего соединения
(у h2mux — в пайпе стрима), только вперемешку.

Корень на стороне библиотек (`requestWritten` без синхронизации в sing-vmess и
sing-mux) **не трогаем**: контракт `net.Conn` они нарушают, но форк-сабмодули ради
этого не заводим, а чинить у потребителя, который пишет конкурентно, дешевле.

## 3. Условие снятия

Апстрим sing-box сериализует запись в `queryMultiplexer` сам (замок, очередь
записи, одна пишущая горутина) **или** мультиплексор перестанет писать в одно
соединение из нескольких горутин. При мерже: конфликт в `multiplexConn` или
`exchangeAsync` = сверить с §2; страж — тесты §4, они упадут, если запись снова
станет конкурентной.

## 4. Проверка

- `dns/transport/multiplexer_write_lock_lx_test.go` — настоящие VLESS-клиент и
  -сервер из `sing-vmess` и h2mux-клиент и -сервер из `sing-mux` в процессе, за ними
  TCP-DNS-сервер с пайплайнингом на 127.0.0.1; 20 раундов по новому
  `queryMultiplexer` с боевыми `WriteMessage`/`ReadMessage`, в каждом 16
  параллельных запросов:
  - `TestLxMultiplexerConcurrentWritesOnVLESSConn`: до правки 238–267 из 320
    запросов падают, сервер видит 16–18 испорченных потоков; после — 0 из 320,
    `-count=20`;
  - `TestLxMultiplexerConcurrentWritesOnMuxConn`: до правки 300 из 320, 20
    испорченных потоков; после — 0 из 320, `-count=20`.
- `go vet` и `go test ./dns/...` без тегов и под `LX_TAGS` — зелёные, кроме
  `TestSurvivalSuccessRestoresService` (`dns/transport/group`): он нестабилен и
  без правки (9 падений из 40 прогонов на `lx` без этой задачи), мультиплексор не
  использует.
- `go build ./...`, сборка `cmd/sing-box` под `LX_TAGS`, `sing-box check` по
  `lx-test/config/*.json` — проходят.
- `-race` не гонялся: на хосте сборки нет cgo.
- Живая: роутер из #77, A/B стокового ядра и ядра с правкой по числу `short read`
  в логе и по задержке некешируемого DNS-запроса. Не прогонялась.

## 5. Файлы

- `dns/transport/multiplexer.go` — `writeAccess` в `multiplexConn`, запись под ним в `exchangeAsync` (`// lx: SPEC 157`)
- `dns/transport/multiplexer_write_lock_lx_test.go` — регресс-тесты (новый)
