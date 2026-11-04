# SPEC: 153 — AWG_MASQUERADE_OVER_DETOUR

**Фича:** [003-AWG](../../FEATURES/003-AWG/FEATURE.md) (маскировочная пачка перед хендшейком); затронута [009-MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (ключ `lx.quic_sniff_decoy` задачи 151 расширен на AWG).

| Поле | Значение |
|------|----------|
| Тип | B (bug) + F (feature) — AWG-endpoint с маскировкой через `detour` на сниффящий релей не поднимается; новый ключ `lx.awg_masquerade` |
| Статус | C (closed) — 2026-11-04: хук форка, ключ, фильтр пачки, зонд пути, юниты форка и lx-слоя (`c7f24d3f8`); loopback-стенд 2026-11-05 (Xray `v26.10.31`, `destOverride` с `quic`, без `routeOnly`): все восемь строк матрицы как в §4.3 — контроль `all`+`off` 0/3 с подменой назначения в логе Xray, дефолты 3/3 без пачки, `all`+`detour` 3/3 с байтом, прямой путь 3/3 с байтом и без, чистый WG через релей 3/3 (§6); issue #64 закрыт |
| Ветка | `lx`; форк `submodules/wireguard-go` ветка `lx-awg2-v007` |
| Build-tag | — (`option`, `common/lxdatagram`, `transport/wireguard` собираются всегда; `with_awg` нужен, чтобы AWG-поля вообще прошли загрузку) |
| Источник | [Issue #64](https://github.com/Leadaxe/sing-box-lx/issues/64) (владелец, 2026-11-04) по двум независимым полевым отчётам из Telegram LxBox 2026-11-03 |
| Связано | [151](../151-QUIC_SNIFF_DECOY/SPEC.md) (§5.2 — тот же случай, тогда оставленный конфигу; байт-обманка), [009](../009-WIRESOCK_MASQUERADE_PROFILES/SPEC.md) (per-handshake приманка `ip=quic`, `SetDecoyPacketsFunc`), [007](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) (AWG через WG-detour), [150](../150-QUIC_DETOUR_OVERSIZE_DATAGRAM/SPEC.md) (проверка «сокет ядра» на conn как надиалено) |

## 0. TL;DR

AmneziaWG-endpoint с маскировкой (`ip=quic` или статичный `i1`) через `detour` на VLESS/Xray
не поднимается, тот же endpoint напрямую и чистый WireGuard через тот же `detour` работают.
Приманка — настоящий QUIC Initial с SNI из `id`, и она первая датаграмма UDP-ассоциации;
QUIC-сниффер Xray её разбирает и при `destOverride: ["quic"]` без `routeOnly` переписывает
назначение всей ассоциации на этот SNI — хендшейк AWG за ней уходит на чужой домен.

Два изменения, оба в lx-слое:

1. `lx.awg_masquerade`: `direct` (дефолт) / `all`. Через `detour` маскировочная пачка
   (per-handshake приманка, `i1`…`i5`, junk `Jc`) не шлётся вовсе; протокольные поля, которые
   сервер проверяет (`S1`–`S4`, `H1`–`H4`, защита заголовка AWG 3.x, паддинг, хвосты), не тронуты.
2. `lx.quic_sniff_decoy` (SPEC 151) достаёт до AmneziaWG: когда пачка отправляется и начинается с
   QUIC-образного пакета, а режим выбирает этот путь, перед ней идёт та же однобайтовая датаграмма.

Они независимы: с дефолтами (`direct` + `off`) случай с `detour` закрыт пунктом 1; кто оставляет
пачку через `detour` (`all`), прикрывается пунктом 2; кто сидит за домашним роутером со сниффящим
прокси на прямом пути, включает 2 в `direct`/`all`, как уже может для MASQUE.

## 1. Полевые отчёты (Telegram LxBox, 2026-11-03)

- Тестер A: публичные VLESS из нескольких списков как `detour`, WARP AWG и затем собственный
  WireGuard-сервер с AWG-маскировкой как endpoint. Чистый WG через `detour` подключается; AWG через
  тот же `detour` — нет, на всех версиях клиента (включая сборку месячной давности), всех серверах
  и endpoint'ах. Один раз утром поднялся на одном сервере и больше никогда.
- Тестер B: свой платный VLESS, WARP AWG с обфускацией как endpoint: «AWG с обфускацией через
  цепочку тоже не работает», напрямую — «везде».

Матрица июньского issue #2 (AWG с `detour` на WG/AWG вешал ядро): тогда **AWG → VLESS работал**.
Профили WARP AWG несли только junk (`Jc/Jmin/Jmax`), без `i1`; случайный junk как QUIC не
разбирается. Приманка `ip=quic` появилась позже (SPEC 009, per-handshake генерация в lx.13), и
генераторы WARP теперь выдают `i1`. Это сходится с «в июне работало, сейчас мертво на любой версии
клиента»: триггер — содержимое первой датаграммы, а не регресс клиента.

## 2. Механизм

Порядок пачки хендшейка в форке, `submodules/wireguard-go/device/send.go`,
`SendHandshakeInitiation`:

1. per-handshake приманки из `SetDecoyPacketsFunc` (`ip=quic|dns|stun|sip`, SPEC 009);
2. статичные `i1`…`i5` (CPS-блобы);
3. `Jc` случайных junk-датаграмм;
4. сама инициация (`[S1 padding][initiation][trailer]`).

Каждая запись — отдельная датаграмма. С `ip=quic` первая датаграмма, которую вообще видит
detour-conn, — QUIC Initial с ClientHello и SNI из `id`.

Xray сниффит первый payload UDP-сессии (`app/dispatcher`), пробует QUIC-сниффер независимо от
порта назначения, и при `quic` в `destOverride` без `routeOnly` подменяет назначение на SNI.
Дальнейшие датаграммы той же ассоциации идут туда же: хендшейк WireGuard уходит на `<id>:<порт>`
и до сервера не доходит. Если первая датаграмма — инициация, сниффер не узнаёт её, подмены нет,
всё работает. Это объясняет каждую строку отчётов, включая «странный» сервер, где поднялось
(`routeOnly: true` или нет `quic` в `destOverride`). Порт не важен: QUIC-сниффер работает на любом
UDP-порту, поэтому собственный сервер тестера A на не-443 падает так же.

Разбор на loopback-стенде и лог Xray с подменённым назначением — SPEC 151 §5.2.

## 3. Почему через `detour` пачка снимается, а не только прикрывается байтом

Маскировка нужна на первой миле. Через `detour` первая миля видит внешний протокол (VLESS, WG,
Hysteria…), а не внутреннюю пачку, так что там она ничего не защищает и стоит:

- трафик: Initial ~1200 байт, до пяти `i`-пакетов и до `Jc` junk-датаграмм до `Jmax` байт на
  каждый хендшейк, повторно на каждом ретрае и rekey; внутри WG-`detour` — под урезанным MTU;
- диагностика: дамп на сервере или лог прокси показывает приманки и резолв подставных SNI вместо
  чистого хендшейка WireGuard;
- надёжность: однобайтовая обманка полагается на то, что прокси смотрит только первую датаграмму;
  снятие пачки не полагается ни на что.

Протокольные поля остаются, потому что их проверяет сервер: паддинг `S1`–`S4`, типы `H1`–`H4`,
`header_protection_key` AWG 3.x, `content_padding_addition`, `random_trailers`, тайминги.
Снимается только то, что сервер игнорирует: приманки, `i`-пакеты, junk.

Байт перед пачкой сохраняет место: `awg_masquerade=all` через `detour` (пользователь хочет пачку
внутри туннеля по своим причинам) и прямой путь за домашним роутером со сниффером — ровно случай
`direct`/`all` SPEC 151 для MASQUE. Умный DPI классифицирует по самому Initial и на лишнюю
датаграмму не реагирует; тупой сниффер спотыкается. Свидетельств, что лишний байт сам по себе
сигнатура, нет, поэтому ручка предлагается, не навязывается.

## 4. Реализация

### 4.1 Форк `submodules/wireguard-go` — один хук

`device.HandshakeBurstFunc func(burst [][]byte) [][]byte`, `Device.SetHandshakeBurstFunc`
(`device.go`, рядом с `SetDecoyPacketsFunc`). `SendHandshakeInitiation` (`send.go`) собирает
пачку как голые payload'ы — приманки, обфусцированные `i1`…`i5`, junk без headroom — в этом
порядке, отдаёт фильтру, и только то, что он вернул, получает `MessageEncapsulatingTransportSize`
байт headroom и встаёт перед инициацией. `nil` — инициация одна. Без фильтра пачка идёт как
собрана. Вся политика — в lx-слое; форк не знает ни о путях, ни о ключах.

Юниты (`device/lx_handshake_burst_test.go`): без фильтра — приманка, `i1`, два junk, инициация;
фильтр видит четыре голых payload'а в этом порядке и его префикс уходит первым; `nil` — первая
датаграмма A это инициация.

### 4.2 `transport/wireguard` — фильтр и зонд пути

- `handshake_burst_lx.go` — `newHandshakeBurstFilter(masquerade, sniffDecoy, pathIsKernel, logger)`:
  пустая пачка → `nil`; ошибка зонда → пачка как есть (Send за ней сообщит отказ);
  `masquerade != all` и путь не сокет ядра → `nil` (один Debug на устройство);
  `lxdatagram.SniffDecoyAppliesTo(sniffDecoy, kernel)` и `burst[0]` QUIC-образен (long header
  0x80 + fixed bit 0x40, RFC 9000 §17.2) → байт впереди. Возвращает `nil`, когда ни то, ни другое
  не может изменить пачку (`all` + `off`), фильтр тогда не ставится.
- `client_bind.go` — `wireConn.kernel` записывается при `connect()` (`lxdatagram.IsKernelSocket`
  для connect-пути, `IsKernelPacketConn` для `ListenPacket`); `ClientBind.PathIsKernel()`
  диалит при нужде — хендшейк, который спрашивает, всё равно диалил бы на первом Send — и
  возвращает ошибку дайла как есть.
- `endpoint.go` `Start` (`// lx: SPEC 153`): `pathIsKernel` = всегда `true` для `StdNetBind`
  (UDP-listener без `detour`), `clientBind.PathIsKernel` для `ClientBind`; фильтр ставится,
  когда у endpoint'а есть AWG-поля. Решение по пути — то же, что у `SniffDecoyApplies` для
  QUIC-дайлов (SPEC 151 §3.1): сокет ядра на conn как надиалено, до обёрток; `detour` на `direct`
  даёт сокет ядра и считается прямым путём.
- `common/lxdatagram/decoy.go` — `SniffDecoyAppliesTo(mode, kernel)` (общий корень
  `SniffDecoyApplies`), `SniffDecoy()`, `IsKernelSocket`, `IsKernelPacketConn` (распаковка
  заменяемых ридеров по образцу `UnwrapReader`), `AWGMasqueradeFromContext`.

### 4.3 Таблица взаимодействия

| Путь | `awg_masquerade` | `quic_sniff_decoy` | Первые датаграммы |
|---|---|---|---|
| direct | direct / all | off / detour | приманка, i1…, junk, init |
| direct | direct / all | direct / all | байт, приманка, i1…, junk, init |
| detour | direct | любой | init |
| detour | all | off / direct | приманка, i1…, junk, init (поведение до задачи) |
| detour | all | detour / all | байт, приманка, i1…, junk, init |

Юниты (`transport/wireguard/handshake_burst_lx_test.go`): по строке таблицы; фильтр ставится
только когда может подействовать; байт только перед QUIC-образной пачкой (`dns`-приманка и junk
без байта; сам байт и long header без fixed bit не QUIC); ошибка зонда оставляет пачку;
`ClientBind.PathIsKernel` — системный дайлер (connect и `ListenPacket`) → сокет ядра,
`net.Pipe`-дайлер → detour.

## 5. Конфиг

Ключ в корневом блоке `lx` (SPEC 098), рядом с `quic_sniff_decoy`, глобальный для всех AWG-endpoint'ов:

| Значение | Пачка отправляется |
|---|---|
| `"direct"` / `false` / нет ключа (**дефолт**) | только на сокете ядра (без `detour` или `detour` на `direct`); через прокси/туннель — одна инициация |
| `"all"` / `true` | на любом пути (поведение до lx.31) |

```json
"lx": { "awg_masquerade": "direct" }
```

Разбор как у `quic_sniff_decoy`: строка-enum, bool принимается для симметрии и пишется обратно
режимом; `direct` канонизируется в отсутствие ключа; неизвестное значение — ошибка загрузки.
`lx.quic_sniff_decoy` нового ключа не получает: его режимы применяются к AWG-пачке как есть.
Юниты — `option/lx_test.go` `TestLXAWGMasquerade`. Документация пользователя —
`docs-lx/lx-config.md` / `.ru.md` §13.

## 6. Проверка

Сделано 2026-11-04: юниты форка и lx-слоя (§4), `go vet`, сборка под `LX_TAGS` и без `with_awg`.

Loopback-стенд 2026-11-05 на Mac владельца — стенд SPEC 151 §5.2 (генератор, ключи, Xray-релей `v26.10.31` с `destOverride` `http`/`tls`/`quic`, `routeOnly: false`, `finalRules` allow 127/8), сборка рабочего дерева `c7f24d3f8`: sing-box WireGuard-сервер на `127.0.0.1:51153`, клиентский endpoint `jc 4/40/70`, `ip: quic`, `id: nonexistent.invalid`, `ib: chrome`, через vless-релей (XUDP, в одной строке — без упаковки) или напрямую; три HTTP-запроса через туннель к мишени в LAN. Счётчики из trace-лога клиента и лога Xray (`failed to open connection to udp:nonexistent.invalid:51153` = подмена назначения).

| Путь | `awg_masquerade` | `quic_sniff_decoy` | Результат | Пачка снята | Байт | Подмена в Xray |
|---|---|---|---|---|---|---|
| detour | all | off (контроль, поведение до задачи) | 0/3 | — | — | 8 |
| detour | direct (дефолт) | off (дефолт) | 3/3 | да | — | 0 |
| detour, без упаковки | direct | off | 3/3 | да | — | 0 |
| detour | all | detour | 3/3 | — | да | 0 |
| detour | all | direct | 0/3 | — | — | 8 |
| прямой | direct | direct | 3/3 | — | да | 0 |
| прямой | direct | off | 3/3 | — | — | 0 |
| detour, чистый WG | — | — | 3/3 | — | — | 0 |

Каждая строка — как в таблице §4.3. Устройство не прогонялось; шаги из issue #64 для полевого разбора:

1. Свой VLESS (Xray) с `sniffing: { enabled: true, destOverride: ["quic"] }` без `routeOnly`.
2. AWG-endpoint с `ip=quic`, `ib=chrome`, `Jc=4`, штатные `H1`–`H4`, `S1=S2=0`, пир — свой
   WireGuard-сервер; `detour` = этот VLESS.
3. До изменения: хендшейк не проходит; `tcpdump` на WireGuard-сервере ничего с хоста VLESS;
   хост VLESS резолвит SNI приманки.
4. После, дефолты: хендшейк с первой попытки; дамп на сервере — только инициация и транспорт с
   хоста VLESS, ничего из пачки.
5. `awg_masquerade=all`, `quic_sniff_decoy=off`: падает как раньше (контроль).
   `awg_masquerade=all`, `quic_sniff_decoy=detour`: поднимается; дамп на входе VLESS — байт,
   Initial, junk, инициация.
6. Прямой путь, `quic_sniff_decoy=direct`: байт перед Initial, хендшейк проходит; прямой путь с
   `off` — побайтово сегодняшняя пачка.
7. Чистый WG через тот же `detour` — без изменений во всех режимах.

## 7. Файлы

- `submodules/wireguard-go/device/device.go` — `HandshakeBurstFunc`, `SetHandshakeBurstFunc`
- `submodules/wireguard-go/device/send.go` — пачка как payload'ы → фильтр → headroom
- `submodules/wireguard-go/device/lx_handshake_burst_test.go` (новый)
- `option/lx.go` — `LXAWGMasquerade`, `LXOptions.AWGMasquerade`, `LXResolved.AWGMasquerade`,
  `AWGMasqueradeOrDirect`; `option/lx_test.go`
- `common/lxdatagram/decoy.go` — `SniffDecoyAppliesTo`, `SniffDecoy`, `IsKernelSocket`,
  `IsKernelPacketConn`, `AWGMasqueradeFromContext`
- `transport/wireguard/handshake_burst_lx.go`, `handshake_burst_lx_test.go` (новые)
- `transport/wireguard/client_bind.go` — `wireConn.kernel`, `ClientBind.PathIsKernel`
- `transport/wireguard/endpoint.go` — `pathIsKernel`, установка фильтра (`// lx: SPEC 153`)
- `docs-lx/lx-config.md`, `docs-lx/lx-config.ru.md` — §13 `lx.awg_masquerade`, дополнение
  `lx.quic_sniff_decoy`
- `SPECS/TASKS/151-QUIC_SNIFF_DECOY/SPEC.md`, `SPEC.ru.md` — §5.2 ссылается сюда
- `SPECS/FEATURES/003-AWG/FEATURE.md` — раздел «Маскировочная пачка и `detour`», строка задачи
