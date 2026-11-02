# SPEC: 150 — QUIC_DETOUR_OVERSIZE_DATAGRAM

> 🌐 English version: **[SPEC.md](SPEC.md)** (основной файл — английский, эта копия вспомогательная; решение владельца 2026-11-02).

**Фича:** [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (источник и главный путь); часть про hysteria2/tuic — патч поверх апстримных файлов, строка в реестре [004-HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md).

| Поле | Значение |
|------|----------|
| Тип | B (bug) — QUIC-клиент (`masque` h3, апстримные `hysteria2`, `tuic`) умирает через несколько секунд после хендшейка, если его UDP идёт через потоковый `detour` |
| Статус | I (implemented) — 2026-11-02: хелпер, три шва, red/green юниты в дереве; финальная сборка на Mac владельца держала h3 через релей n009 в обычной и XUDP-упаковке (4 из 4 пингов за 20 с, где раньше умирал на 1–5 с); второй релей стенда 2026-11-01 (n002) в тот день сломался сам (vless TCP-пинг не проходил, QUIC через него попадал на чужой ключ); устройство репортёра и живые серверы hysteria2/tuic не прогонялись |
| Ветка | `lx` |
| Build-tag | — (`common/lxdatagram`, `protocol/masque` собираются всегда; швы в `protocol/hysteria2` и `protocol/tuic` — под апстримным `with_quic`) |
| Источник | Телеграм-отчёт zomg 2026-11-01 (LxBox 2.25.19-rc.1, ядро `v1.14.4-lx.28`): узел «WARP (MASQUE) h3» с `detour` через бесплатные релеи `[BL]` не поднимается, MASQUE h2 и AWG через те же релеи работают. Стенд на Mac владельца 2026-11-01 против релеев `igareck/vpn-configs-for-russia`, решающий замер отладочной сборкой тем же вечером |
| Связано | [146](../146-MASQUE_CLOSE_WEDGE_DETOUR_DEADLINE/SPEC.md) (вторая половина контракта detour-conn: дедлайны; её обёртка стоит над этой, порядок закреплён тестом), [021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) (сам outbound), [074](../074-MASQUE_VHTTP_AUTO/SPEC.md) (`auto` маскировал отказ откатом на h2), [120](../120-PATH_MTU_ALIGN/SPEC.md) (PMTUD на внутреннем QUIC — тот же механизм с другой стороны), [028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) (вложенные туннели и размер датаграммы), [091](../091-CONFIG_VALIDATION_TUIC_MASQUE/SPEC.md) (единственный другой шов `// lx:` в `protocol/tuic/outbound.go`) |

UDP-сокет ядра молча усекает датаграмму, которая не помещается в буфер вызывающего. UDP-«сокет»,
протянутый через потоковый `detour` (vless XUDP или обычный vless UDP поверх WebSocket, gRPC,
XHTTP, TCP, …), так не делает: пакетные читатели sing отвечают `io.ErrShortBuffer`, quic-go
считает такую ошибку чтения фатальной и закрывает транспорт. Cloudflare WARP через несколько секунд
после установления каждой h3-сессии шлёт двенадцать PMTU-зондов по 1453–1462 байта, на 1–10 байт
больше объявленных quic-go 1452, поэтому каждый MASQUE h3-туннель через такой detour умирает в этот
момент. Фикс даёт detour-conn семантику ядра: чтение в короткий буфер возвращает усечённую голову
датаграммы и выбрасывает хвост, ровно как ОС на настоящем сокете. Апстримные `hysteria2` и `tuic`
отдают quic-go свой detour-conn тем же путём чтения и получают ту же обёртку.

## 1. Проблема

### 1.1 Стенд

Картина репортёра: h3 к WARP через `detour` → selector из релеев `[BL]` — «dial quic: context
deadline exceeded» / `handshake_failure` / нет соединения; h2 и AWG через тот же detour в порядке.

На Mac, с ключами WARP из конфига лаунчера владельца и семью релеями из списка, разделились три
независимых слоя:

1. **Xray-сниффинг QUIC на части VPS-релеев.** При `sniffing.destOverride` без `routeOnly` релей
   отправляет UDP/QUIC на адрес, в который резолвится SNI, а не на запрошенный IP. Доказательство:
   SNI `www.apple.com` → удалённый алерт `0x150 200:internal error` (Akamai); `www.cloudflare.com` →
   «different public key» (обычный edge, не MASQUE-endpoint); имя без A-записи → таймаут. Отсюда
   `handshake_failure` репортёра с `telemost.yandex.ru` из пула SNI LxBox. TLS по TCP (h2) там не
   переписывается. Чинить не нам; совет — нейтральный SNI или h2.
2. **Релеи вовсе без UDP** (фронты onrender). Тоже не наше.
3. **Через каждый релей, который UDP пропускает** (VPS с REALITY в Польше, ws за Cloudflare:2083),
   и обычный vless UDP, и XUDP: h3-туннель поднимается, первый пинг может даже пройти, и через
   1–5 с `masque: tunnel died: read from tunnel: use of closed network connection`, на одном релее
   с видимой причиной: `quic: transport closed: short buffer`. Бинарь до SPEC 146 ведёт себя так же,
   то есть это не регресс lx.28.

Слой 3 — предмет этой задачи. Отладочная сборка с обёрткой, которая читала каждую detour-датаграмму в
буфер 64 КиБ, печатала её и усекала до длины вызывающего, показала через оба релея и в обеих упаковках
**ровно двенадцать датаграмм сверх размера после хендшейка и ни одной после**:

| Длина на проводе | Сколько | Сверх 1452 | Первый байт |
|---|---|---|---|
| 1462 | 3 | 10 | `0x4x`/`0x5x` (1-RTT, короткий заголовок) |
| 1457 | 3 | 5 | те же |
| 1454 | 3 | 2 | те же |
| 1453 | 3 | 1 | те же |

Убывающая лестница, три попытки на размер, затем тишина: это DPLPMTUD сервера (RFC 8899), ищущий
потолок пути выше объявленного клиентом. **С усечением h3-туннель через оба релея выжил**: 4 пинга
за 20 с, 355–536 мс после первого. Один из трёх релеев сам оборвал TCP (EOF) — это его доступность,
не этот баг.

### 1.2 Почему сокет ядра выживает, а потоковый detour нет

quic-go (пин `v0.61.0-sing-box-mod.12`) объявляет `max_udp_payload_size = protocol.MaxPacketBufferSize = 1452`
(`connection.go:338/470`) и читает каждую датаграмму в буфер 1452 байта:

- `basicConn.ReadPacket` (передан `net.PacketConn`, наш путь `masque` через
  `bufio.NewUnbindPacketConn`) → `ReadFrom(buffer.Data[:1452])`; комментарий там говорит прямо:
  пакет больше «will then end up undecryptable» — то есть ожидается усечённым и выброшенным;
- `basicConnectedConn.ReadFrom` (передан `net.Conn` через `quic.DialConn`/`DialEarlyConn`, путь
  `sing-quic` у `hysteria2` и `tuic`) → `conn.Read(p)` с `len(p) == 1452`.

`Transport.listen` продолжает работу на `net.Error.Temporary()` и на платформенном errno «message
too long» (`EMSGSIZE`, `WSAEMSGSIZE` — Windows сообщает об усечённой датаграмме ошибкой, quic-go её
явно игнорирует); **любая другая ошибка чтения закрывает транспорт**. `io.ErrShortBuffer` — ни то,
ни другое.

На настоящем сокете ядро копирует первые `len(p)` байт и выбрасывает хвост (Linux, darwin, Android);
усечённый пакет не расшифровывается и отбрасывается, сервер считает зонд потерянным и остаётся на
1452. Это контракт, под который написан quic-go: датаграмма сверх размера выбрасывается, никогда не
фатальна.

Через потоковый detour датаграмма приходит целиком, со своим префиксом длины, и читатели sing
отказываются отдавать её часть:

- `vless.PacketConn.Read` (`sing-vmess/vless/client.go:215`): `if len(b) < int(length) { return 0, io.ErrShortBuffer }`;
- `vless.XUDPConn.ReadFrom` → `buf.With(p)` → `ReadPacket` → `buffer.ReadFullFrom(c.Conn, length)` →
  `io.ErrShortBuffer` из `common/buf`.

Ошибка не временная, quic-go закрывает транспорт, следующее чтение возвращает «use of closed network
connection», `masque` сносит туннель. С `vhttp: auto` (SPEC 074) узел тихо приземляется на h2 и
пользователь видит лишь «h3 через этот релей не работает»; с `vhttp: h3` узел мёртв.

Дедлайн-обёртка SPEC 146 не при чём: `deadline.Reader.pipeRead(len(p))` выделяет буфер длины
вызывающего и пробрасывает ошибку как есть. Старый бинарь без неё умирает так же.

Может ли сервер превышать `max_udp_payload_size` партнёра — дело Cloudflare (RFC 9000 §18.2
говорит лишь, что такие датаграммы «are not likely to be processed by the receiver»); сокет ядра
снимает вопрос, и мы обязаны снять его так же. Поднять константу quic-go значило бы лишь сдвинуть
порог: зонды всегда идут выше объявленного, в этом их смысл.

### 1.3 Кого задевает

- **`masque` h3 через любой detour, несущий UDP в потоке**: vless/vmess/trojan поверх WebSocket,
  gRPC, XHTTP, голого TCP, в обычной и XUDP-упаковке. Это случай репортёра и любая схема «WARP за
  релеем»; `vhttp: auto` прячет его под постоянным h2.
- **Апстримные `hysteria2` и `tuic` через те же detour**: то же чтение в 1452 байта, те же
  читатели, тот же фатальный путь. Любая серверная датаграмма больше 1452 (PMTUD-зонд, сервер,
  отдающий 1452 полезных байта плюс заголовок обфускации) убивает сессию. Выведено из кода, на живом
  сервере не замерено: сервера hysteria2/tuic за релеем под рукой не было. Conn порт-хоппинга
  `hysteria2` (`hysteria.HopConn`) уже читает в большой буфер и копирует, то есть усекает —
  единственный путь внутри `sing-quic`, который уже ведёт себя как сокет.
- **Не задеты**: прямой путь (`dialer.udpConn` над `*net.UDPConn`, усечение ядром); detour,
  заканчивающиеся датаграммным сокетом (netstack endpoint'а, UDP-прокси), отдают датаграммы как
  датаграммы — на этом стенде не замерялось, вне задачи.

## 2. Фикс

Новая логика в новом пакете, по одному шву на протокол. Код sing и quic-go не копируется и не
правится (пины не тронуты; апстриму ничего не предлагается).

### 2.1 `common/lxdatagram` — семантика ядра для conn с датаграммами в потоке

`TruncateReads(conn net.Conn, logger logger.Logger) net.Conn`:

- **Сокет ядра проходит нетронутым.** Если `N.UnwrapReader(conn)` реализует `syscall.Conn`, conn —
  настоящий сокет (`dialer.udpConn` прямого пути сам это заявляет и разворачивается до
  `*net.UDPConn`); он возвращается как есть, без копии и без лишнего слоя. Проверка та же, которой
  `sing-quic` решает, держит ли он сокет (`withSyscallConn`). Потоковый detour останавливает
  разворачивание на слое кадрирования (`XUDPConn`, `vless.PacketConn`: читатели незаменяемые) и
  `syscall.Conn` не является.
- **Иначе** `Read(p)` читает следующую датаграмму в собственный буфер обёртки на 65535 байт (один на
  conn, под мьютексом) и копирует в `p` `min(n, len(p))` байт. Датаграмма длиннее `p` возвращает
  `len(p), nil`; хвост выбрасывается и никогда не переносится в следующий `Read` — границы датаграмм
  сохраняются, как на сокете. В `p` от 65535 байт читается напрямую.
- Первое усечение на conn логируется один раз на debug с обеими длинами; это штатное поведение
  PMTUD, не предупреждение.
- Обёртка прозрачна для стеков вокруг: `Upstream()` возвращает обёрнутый conn, так что
  `deadline.NeedAdditionalReadDeadline` (SPEC 146) по-прежнему видит через неё ответ `XUDPConn`;
  `ReaderReplaceable()` — false (обёртка обязана остаться в пути чтения); всё остальное (`Write`,
  `Close`, дедлайны, адреса) — встроенного conn.

`Dialer(dialer N.Dialer, logger logger.Logger) N.Dialer` оборачивает результат `DialContext` для
`udp` и возвращает всё остальное (`tcp`, `ListenPacket`) без изменений. Это шов для кода, который
получает dialer и диалит внутри библиотеки (`sing-quic`).

### 2.2 `masque`

`connectH3WithBudget` (`protocol/masque/outbound.go`, `// lx: SPEC 150`): conn от
`o.dialer.DialContext` сначала проходит через `TruncateReads`, проверка дедлайна SPEC 146 идёт по
результату, так что цепочка к quic-go — `UnbindPacketConn → deadline.Conn → усекающий conn →
detour-conn`. Усекающий слой читает собственным буфером полного размера, поэтому сохраняет
датаграмму целой по любую сторону дедлайн-обёртки (`deadline.Reader` читает в буфер длины
вызывающего и пробросил бы ошибку короткого буфера как есть); он стоит ближе всех к detour-conn,
чтобы дедлайн-обёртка читала обычный conn. Оба порядка закреплены тестом (§3) вместе с голой
дедлайн-обёрткой, воспроизводящей полевой отказ.

### 2.3 `hysteria2`, `tuic`

По одной строке (`// lx: SPEC 150`): `Dialer`, передаваемый в `hysteria2.NewClient` /
`tuic.NewClient`, — `lxdatagram.Dialer(outboundDialer, logger)`. Всё, что два клиента делают с
dialer'ом, остаётся как было: `sing-quic` диалит внутри `dialFunc` / `offerNew`, ставит сверху
Salamander/Gecko/HopConn (hysteria2) и отдаёт результат в `qtls.Dial`/`CreateTransport`; обёртка
сидит под обфускацией, там, где приходит сырая датаграмма. Клиенты dialer по типу не проверяют
(сверено в пине), а собственное использование `outboundDialer` в `hysteria2` для резолва realm
(`ResolveDialer.QueryOptions`) остаётся на необёрнутом dialer'е. На прямом пути обёртка — no-op
(§2.1), горячий путь обоих протоколов не меняется.

### 2.4 Вне скоупа

- Класс SPEC 146 (`Close` ждёт dial через detour-conn без дедлайнов) в `hysteria2`/`tuic`: хелпер
  намеренно не добавляет дедлайн-обёртку; это отдельное решение с ценой в горутину на датаграмму.
- Семантика `vless.PacketConn`/`XUDPConn` в sing: не меняется и не копируется (обёртка в точке
  инъекции, правило владельца 2026-09-14).
- Inbound'ы / серверная сторона: не тронуты.
- Xray-сниффинг релеев и фронты без UDP (§1.1, слои 1–2): совет репортёру — h2 или `vhttp: auto`,
  нейтральный SNI, `162.159.198.1`/`.2` в `server`.

## 3. Проверка

- `common/lxdatagram/truncate_test.go`:
  - датаграмма сверх размера → `n == len(p)`, `err == nil`, следующий `Read` отдаёт **следующую**
    датаграмму, не хвост; датаграммы ровно по размеру и короче не тронуты;
  - `*net.UDPConn` (`net.ListenUDP`) и `dialer.udpConn` проходят без обёртки;
  - `Dialer` оборачивает `udp` и не трогает `tcp`/`ListenPacket`;
  - композиция с обёрткой SPEC 146 усекает в любом порядке (`deadline.NewConn` над
    `TruncateReads` и наоборот); голая дедлайн-обёртка сама воспроизводит полевой
    `io.ErrShortBuffer`;
  - `NeedAdditionalReadDeadline` виден сквозь обёртку.
- `protocol/masque/oversize_datagram_lx_test.go` — red/green на продакшен-пути
  `connectH3WithBudget`: фальшивый detour-conn, кадрирующий датаграммы как `vless.PacketConn`
  (префикс длины, `io.ErrShortBuffer` при буфере короче кадра), отдаёт кадр 1462 байта первой
  «серверной» датаграммой. До фикса dial завершается сразу с `short buffer` в цепочке ошибки; после
  кадр выбрасывается и dial завершается по дедлайну контекста (сервер не отвечает) —
  `errors.Is(err, context.DeadlineExceeded)`.
- `go test -race` с тегами lx-ci для `common/lxdatagram`, `protocol/masque/...`,
  `protocol/hysteria2`, `protocol/tuic`; `go vet`, gofmt; `go build` с `LX_TAGS`.
  `common/lxdatagram`, `protocol/hysteria2`, `protocol/tuic` добавлены в список тестов lx-ci (пакет
  без строки там не бегает нигде).
- Живьём, Mac, домашний выход: финальная сборка против двух релеев §1.1, пропускающих UDP; h3-пинг
  через detour должен пережить пятую секунду, на которой умирал раньше.
- Устройство (LxBox): узел репортёра «WARP (MASQUE) h3» с `detour` через релей `[BL]`, пропускающий
  UDP. За репортёром.

## 4. Файлы

- `common/lxdatagram/truncate.go` — `TruncateReads`, `Dialer` (новый)
- `common/lxdatagram/truncate_test.go` (новый)
- `protocol/masque/outbound.go` — `connectH3WithBudget` (`// lx: SPEC 150`, одна строка перед блоком SPEC 146)
- `protocol/masque/oversize_datagram_lx_test.go` (новый)
- `protocol/hysteria2/outbound.go` — `Dialer:` в `hysteria2.NewClient` (`// lx: SPEC 150`)
- `protocol/tuic/outbound.go` — `Dialer:` в `tuic.NewClient` (`// lx: SPEC 150`)
- `.github/workflows/lx-ci.yml` — список тестов
