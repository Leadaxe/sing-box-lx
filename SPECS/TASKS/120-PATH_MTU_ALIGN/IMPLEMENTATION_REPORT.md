# IMPLEMENTATION REPORT: 120 — PATH_MTU_ALIGN

**Фича:** [015-CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Зона | Файлы | Содержимое |
|------|-------|------------|
| Новый пакет | `common/lxmtu/{table,mode,graph,map}.go`, `lxmtu_test.go` | Таблицы §2.1–2.2 (`IPUDP`, `TunnelOverhead` 60/80 и 79/99, `QUICClamp`, `MASQUEOuterInitialPacketSize`); режимы `off`/`fill`/`clamp` + `except` (`Policy.Apply`, никогда не повышает); `Resolver` по типизированным опциям — ёмкость с мемоизацией и защитой от цикла (туннели → эффективный `mtu`, tailscale → `system_interface_mtu` или 1280, группы → min, транзитивный `detour`, tuic native / chain → ∞ + предупреждение), `Align()` правит опции на месте; `AlignMap` — та же логика для map-формы chain |
| lx-файлы | `option/lx.go`, `box_lx.go` | `lx.mtu_align` строка-или-объект, валидация режима, дефолт `clamp`, `LXResolved.MTUAlign`; пре-пасс `alignPathMTU` после `ResolveLX` — Info на изменённое поле, Warn на предупреждение, неизвестный тег в `except` = ошибка старта, `off` — тишина |
| chain | `protocol/chain/{mtu,transform,clone,chain}.go` | Собственные константы и `overheadOf` заменены на `lxmtu`; политика из контекста; QUIC-звенья на позициях ≥ 1 получают `initial_packet_size` + PMTUD off; `mtu_reason` с префиксом режима, `initial_packet_size=…` в `describe()` и `MTUReason` звена |
| masque | `protocol/masque/outbound.go`, `outer_packet_size_lx.go`, `transport/masque/connectip/connectip.go` | `InitialPacketSize = clamp(mtu + 51, 1200, 1452)` (формула в `lxmtu`); ICMP Packet Too Big называет `бюджет − context-id`, не ниже 1280 |
| Тесты | `common/lxmtu/lxmtu_test.go`, `option/lx_test.go`, `box_lx_mtu_test.go`, `protocol/chain/chain_test.go`, `protocol/masque/outer_packet_size_lx_test.go`, `transport/masque/connectip/icmp_mtu_lx_test.go` | §6.1 (формулы, группы, транзитивность, цикл, режимы, `except`, QUIC-минимум, предупреждения), §6.2 (конфиг #37 → 1232/PMTUD off с точной строкой лога; IPv4 → 1252; явный 1300 в clamp/fill; selector/urltest; WG endpoint под masque), `TestChainMTU` + hysteria2-звено над WG, §6.4 |

Апстримных файлов ноль; `go.mod` не менялся. Доки: `lx-config.*` §0/§4/§10/§13, `protocols-transports.*` (строка `mtu` masque), `lx-changelog.md` (секция `v1.14.3-lx.14`, имя рабочее), FEATURE 015 (правило MTU → общий механизм), FEATURE 009 (границы: IPv6-фрагменты WARP, `mtu + 51`).

## Отклонения от SPEC

- `Policy.Apply` принимает признак `explicit`: туннель без `mtu` несёт дефолт типа (1408/1280) как `Configured`; `fill` понижает не влезающий дефолт (это не явное значение), но не записывает дефолт, который влезает. Строка лога для такого случая: `wg mtu 1408 → 1200 (default) (…)`.
- Legacy outbound `wireguard` в этом дереве — заглушка (`StubOptions`), выравнивается только endpoint-форма.
- Masque-накладные в chain сменились с оценки 90 на 79/99 по семейству; ожидания `TestChainMTU` обновлены.

## Проверка

- `gofmt -l` чисто; `go build ./...` без тегов — ok; `go vet` затронутых пакетов — ok.
- `go test -ldflags=-checklinkname=0` (go1.26.8): `./common/lxmtu/... ./option/...` — ok; `-tags with_lx_chain,with_quic,with_gvisor,with_wireguard,with_utls ./protocol/chain/...` и корень (`LX|MTU|Align`) — ok; `-tags with_quic,with_gvisor,with_utls ./protocol/masque/... ./transport/masque/...` — ok; `./lx-test/chain/...` — ok.
- `make -f Makefile.lx lx-build` (полный набор тегов) — ok; `sing-box check` конфига #37 — ok.

### Стенд GL (Амстердам, чистый канал), 2026-10-09

Конфиг по форме #37: masque WARP h3 (`mtu` 1280, IPv4-сервер) + hysteria2 `82.38.64.229:8443` (единственный живой hy2 из наших подписок; hy2 с IPv6 в наших источниках нет) через `detour`.

| Проверка | До (1242, без выравнивания) | После |
|---|---|---|
| Лог старта | — | `lx: mtu_align: hy2-via-warp initial_packet_size → 1252 (detour warp[masque] mtu 1280 − 28 ipv4; pmtud off)` |
| hy2 через WARP, `generate_204` | 204 | 204 |
| hy2 через WARP, 10 МБ down / up | 7.8 МБ/с | 8.7–10.5 МБ/с down, 11.0 МБ/с up |
| Первый пакет 1280 байт сразу после подъёма туннеля (§1.3) | **0/5** (контроль 1178 байт: 4/4) | **5/6** (единственный провал — сбой подъёма туннеля, см. ниже) |
| QUIC Initial по IPv6 через masque 1280 | 1232 OK, 1252/1280 TIMEOUT | без изменений (свойство туннеля; выравнивание даёт QUIC-узлу 1232) |

Сквозной прогон hy2 по IPv6 через masque не сделан — нет IPv6-сервера hy2; полевая проверка остаётся за репортёром #37.

## Наблюдения

- **Вне SPEC 120.** На GL ~25 % попыток поднять h3-туннель к WARP падают сразу с `dial connect-ip: read response: http3: parsing frame failed: PROTOCOL_VIOLATION (remote)`; повтор через секунды проходит. От `InitialPacketSize` не зависит (A/B по 12 подъёмов: 3 сбоя при 1242 и 3 при 1331). В `vhttp: auto` такой одиночный сбой уводит узел на h2 и запоминает h2 до отказа h2 (`connect()` в `protocol/masque/outbound.go`). Кандидат в отдельную задачу.
- `sing-box check` выравнивание выполняет (ошибка на неизвестный тег в `except` ловится), но Info-строки не печатает.

## Открыто

- Проверка репортёром #37 на IPv6-сервере hy2.
- Вопрос №8 SPEC 110 закрыт для канала GL (1331 проходит там же, где 1242); мобильная сеть не замерялась.
