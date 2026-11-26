# HISTORY: 161 — HTTP_CONNECT_OUTAGE_LEAK

Как пришли к текущему состоянию. Текущее состояние — в [SPEC.md](SPEC.md).

## 1. HTTP CONNECT (PR #73)

Хотфикс начался с HTTP outbound: прокси, принявший TCP и CONNECT и замолчавший, держал дозвон и
его ресурсы без предела (issue #74). Сторож ставится после нижележащего дозвона, 15 с, сокет
закрывается по истечении; `detour` нового дедлайна не получает. Вошёл в `v1.14.4-lx.36`.

## 2. SOCKS, httpupgrade, ShadowTLS v3 (2026-11-25, ветка `lx-spec138-rr-self-heal`)

### Повод

На стенде SPEC 138 (самовосстановление `round_robin`) socks-сервер узла под `kill -STOP` держал
открытие соединения через группу без предела: рукопожатие SOCKS ждало ответа на контексте без
дедлайна, а клиент мог не сдаться. Для группы это и висящее соединение, и потерянный сигнал: ни
ошибки открытия, ни её времени. Разбор показал тот же класс у обмена `httpupgrade` (запрос и ответ
без дедлайна и без контекста) и у TLS-рукопожатия ShadowTLS v3 на стандартном TLS (контекст
вызывающего без таймаута).

### Что выбрали

Общий пакет `common/lxhandshake` с двумя формами — по контексту (`Context` + `Dialer`) для
клиентов, которые слушают контекст, и по соединению (`Guard`) для обменов без контекста — вместо
отдельной копии сторожа HTTP CONNECT на каждый протокол. Сторож HTTP CONNECT на общий пакет не
переводился.
ShadowTLS v3 получил тот же таймаут, что `tls.ClientHandshake` даёт остальным путям.

### Что не тронули

`tor`, VLESS `encryption`, `trusttunnel`, `xhttp` — у них ожидание уже ограничено своим контрактом
или идёт не к серверу узла; причины — в SPEC.

## 3. Синк upstream/stable 2026-11-26 (SPEC 166)

Апстрим в тот же день ограничил рукопожатия сам: `8a1ddcebd` — SOCKS (`client.SetHandshakeTimeout`) и
HTTP (`HandshakeTimeout`), `c7f675a68` — ShadowTLS v3 на стандартном TLS, `27c872d81` — httpupgrade и
websocket. В SOCKS два механизма вместе ломали тип ошибки: наш контекст отменялся раньше апстримного
таймаута, клиент `sing` возвращал `ctx.Err()`, и вместо таймаута выходил `context.Canceled` — для
SPEC 054 это «клиент ушёл», узел без штрафа. Шов SOCKS снят на мерже, `handshake_timeout_lx_test.go`
переведён на `client.SetHandshakeTimeout` и целиком проходит на апстримном механизме. HTTP, httpupgrade и
ShadowTLS дают таймаут и с двумя механизмами; владелец в тот же день решил снять и их. Удалены
`protocol/http/handshake_lx.go`, `common/lxhandshake/`, `protocol/shadowtls/handshake_lx.go`, швы в
`protocol/http/outbound.go`, `transport/v2rayhttpupgrade/client.go`, `protocol/shadowtls/outbound.go` —
файлы равны апстримным. Тест HTTP укорачивает приватный `handshakeTimeout` клиента `sing` (апстрим
ставит константу); тест httpupgrade ждёт честные 15 с — там предел встроен в `DialContext`. Issue
об этих таймаутах у апстрима не было, в коммитах ссылок на нас нет.

