# PLAN: 120 — PATH_MTU_ALIGN

**Фича:** [015-CHAIN](../../FEATURES/015-CHAIN/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Этапы и файлы

### Этап 1 — общий пакет `common/lxmtu` (новый, без build-tag)

| Файл | Что |
|------|-----|
| `common/lxmtu/table.go` | Константы и таблицы §2.1–2.2 SPEC: `IPUDP(fam)`, `TunnelOverhead(type, fam)`, `IsTunnelType`, `TunnelDefaultMTU`, QUIC-кламп |
| `common/lxmtu/mode.go` | `Mode` (`off`/`fill`/`clamp`), `Policy{Mode, Except}`, разбор строки/объекта (`option.LXMTUAlign`), решение `apply(configured, limit)` → `Decision` |
| `common/lxmtu/graph.go` | Обход по опциям: `Resolver` над `[]option.Outbound` + `[]option.Endpoint` (мапа tag → узел), `Capacity(tag)` с мемоизацией и защитой от цикла, `Demand(node)` по типу, `Align()` → `[]Decision` с правкой типизированных опций на месте |
| `common/lxmtu/decision.go` | `Decision{Tag, Field, Configured, Effective, Reason, Warning}`, форматирование строк лога §4 |
| `common/lxmtu/*_test.go` | Критерии §6.1 |

Типизированный доступ к полям по типу узла — через type switch по `*option.XxxOptions`
(hysteria2/tuic/hysteria → `QUICOptions`; wireguard endpoint / legacy; masque; openvpn-client;
openconnect; tailscale; selector/urltest/chain). Для `chain` (работает с `map[string]any` после
strip/rewrite) — второй вход `AlignMap(typeName, m, capacityBelow, fam)` с той же таблицей.

### Этап 2 — masque

| Файл | Что |
|------|-----|
| `protocol/masque/outbound.go` | `InitialPacketSize: 1242` → `lxmtu.MASQUEOuterInitialPacketSize(mtu)` (= `mtu + 51`, кламп [1200, 1452]) |
| `protocol/masque/initial_packet_size_lx_test.go` | 1280 → 1331, 1420 → 1452, 1100 → 1200 (кламп) |
| `transport/masque/connectip/connectip.go` | ICMP Packet Too Big: `max(minMTU, MaxDatagramPayloadSize − len(contextIDZero))` вместо константы |
| `transport/masque/connectip/icmp_mtu_lx_test.go` | бюджет 1310 → ICMP 1309; бюджет 1205 → ICMP 1280 |

### Этап 3 — `detour`: пре-пасс и ключ

| Файл | Что |
|------|-----|
| `option/lx.go` | `LXOptions.MTUAlign *LXMTUAlign` (`json:"mtu_align"`), `LXMTUAlign` с `UnmarshalJSON` строка-или-объект (`mode`, `except`), валидация режима; `LXResolved.MTUAlign LXMTUAlignResolved{Mode, Except}`; дефолт `clamp` |
| `option/lx_test.go` | парсинг обеих форм, неизвестный режим — ошибка, пустой блок = `clamp` |
| `box_lx.go` | в `applyLXOptions` после `ResolveLX`: `lxmtu.NewResolver(options, policy).Align()` → Info/Warn по решениям; неизвестный тег в `except` — ошибка |
| `box_lx_test.go` | критерии §6.2 на `option.Options` (без поднятия box): конфиг #37 → 1232/PMTUD off; IPv4 → 1252; явный 1300 в clamp/fill; selector под detour; транзитивный detour; endpoint WG под masque |

### Этап 4 — `chain`

| Файл | Что |
|------|-----|
| `protocol/chain/mtu.go` | константы/`overheadOf`/`isTunnelType`/`tunnelDefaultMTU`/`mtuFromMap` → вызовы `lxmtu`; `applyMTU` → `lxmtu.AlignMap` с политикой из контекста (`LXResolved.MTUAlign`); для QUIC-звеньев — `initial_packet_size` + `disable_path_mtu_discovery` в `m` |
| `protocol/chain/transform.go` | `cloneInfo`: `mtuReason` с префиксом режима; QUIC-поля в `describe()` |
| `protocol/chain/chain_test.go` | `TestChainMTU` остаётся зелёным (masque 90 → 79/99 правится в ожиданиях, если задето); новый кейс hysteria2-звено над WG |

### Этап 5 — доки

| Файл | Что |
|------|-----|
| `docs-lx/lx-config.ru.md`, `docs-lx/lx-config.md` | §13: ключ `lx.mtu_align` (строка/объект, режимы, `except`), пример в §0; §4 masque: `InitialPacketSize` = `mtu + 51`; §10 chain: ссылка на общий механизм |
| `docs-lx/protocols-transports.ru.md`, `.md` | masque: таблица выводимых значений под туннелем (hy2 1232/1252); hysteria2/tuic: примечание про `detour` на туннель |
| `SPECS/FEATURES/015-CHAIN/FEATURE.md` | «Правила и гарантии»: MTU-контракт → общий механизм, ссылка на 120; строка 120 в «Задачах фичи» |
| `SPECS/FEATURES/009-MASQUE_WARP/FEATURE.md` | «Границы»: WARP теряет внутренние IPv6-фрагменты; `InitialPacketSize` внешнего QUIC; строка 120 |
| `SPECS/README.md` | строка 120 в Roadmap |
| `docs-lx/lx-changelog.md` | секция следующего тега |

## Что не меняется

- Апстрим-файлы `protocol/hysteria2/outbound.go`, `protocol/tuic/outbound.go`,
  `protocol/hysteria/outbound.go`: читают `options.InitialPacketSize` /
  `options.DisablePathMTUDiscovery` как раньше — пре-пасс правит опции до них.
- `common/dialer`: детур-диалер не трогается.
- sing-quic / quic-go: без форка и патчей.
- Поведение `chain` при дефолтном режиме для WG-звеньев.

## Зона касания upstream

`// lx:` блоков в апстрим-файлах **не добавляется**: `box_lx.go`, `option/lx.go`,
`protocol/masque/*`, `transport/masque/*`, `protocol/chain/*`, `common/lxmtu/*` — lx-файлы.
`go.mod` не меняется.

## Проверка

- `gofmt -l` на тронутых файлах.
- `go vet` + `go test` с `-ldflags=-checklinkname=0`, тулчейн из `go.version`:
  `./common/lxmtu/...`, `./option/...`, `./protocol/chain/...` (`-tags with_lx_chain,with_quic,with_gvisor,with_wireguard`),
  `./protocol/masque/...`, `./transport/masque/...` (`-tags with_quic,with_gvisor`), корень (`box_lx_test.go`).
- `make -f Makefile.lx lx-build`; `sing-box check` конфига #37 (с заглушками ключей) и конфигов
  с тремя формами `lx.mtu_align`.
- `lx-test/chain` стенд.
- GL (Амстердам): §6.5–6.6 SPEC — тестовое ядро в `/tmp`, без TUN, под `timeout`, уборка после.
