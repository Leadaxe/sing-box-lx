# FEATURE 015 — CHAIN — a virtual multi-hop path of groups and nodes / виртуальная цепочка хопов из групп и узлов

| Field / Поле | Value / Значение |
|--------------|------------------|
| Type / Тип | Product feature / Продуктовая фича |
| Build tag | `with_lx_chain` (chain state in the UI — behind `with_lx_command`, see [OBSERVABILITY](../006-OBSERVABILITY/FEATURE.md)) / (состояние цепочки в UI — за `with_lx_command`) |
| State / Состояние | ✅ Implemented (I): unit tests + acceptance stand on live shadowsocks hops; no field run of WireGuard links yet / Реализовано (I): юниты + приёмочный стенд на живых shadowsocks-хопах; полевой проверки WG-звеньев не было — [073](../../TASKS/073-CHAIN_OUTBOUND/SPEC.md) |

> **Language.** This document carries both languages in one file: the two blocks below hold the
> same content, not a word-by-word translation. The tables and code blocks are shared and live
> in the English block to avoid drift between two copies of the same field list.
>
> **Язык.** Документ билингвальный: два блока ниже несут одно и то же содержание, а не пословный
> перевод. Таблицы и блоки кода общие и живут в английском блоке — чтобы два экземпляра одного
> списка полей не разъезжались.

---

<details open>
<summary><h2>🇬🇧 English</h2></summary>

## Purpose

A multi-hop route `client → hop 1 → hop 2 → … → destination` can only be assembled by hand today:
the exit node is given a `detour` onto the entry node, and the relation is baked into the nodes
themselves. A node needed in two roles (on its own and as a link) has to be duplicated in the
config; a group (`selector`, `urltest`) cannot be a link at all — a `detour` onto a group
redirects one node, but gives no way to choose *what that node goes through*.

The feature adds an outbound of type `chain`: an ordered list of positions, each of which is a
node **or a group**. The chain is assembled at runtime from what the groups picked right now; node
configs are not duplicated; switching a group at any position changes the path without a restart.
From the outside a chain is an ordinary outbound — it can go into a route, into a `selector`, into
a `urltest`.

## Controlled parameters

```json
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "idle_timeout": "5m",
  "strip_evasion": true,
  "strip": { "multiplex.padding": false, "tls.utls": true },
  "rewrite": { "wireguard": { "mtu": 1200 } }
}
```

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `outbounds` | list of tags, ≥ 2 | required | Chain positions **in packet order**: `[0]` is the first hop from the client (it touches the real network), the last one is the node whose address the destination sees. Any position may be a node, an endpoint or a group of any nesting |
| `idle_timeout` | duration | `5m` | How long a runtime link instance may sit unused before it is removed; `0` — keep until stop |
| `strip_evasion` | bool | `true` | Strip one-sided DPI tricks (catalog below) from links at positions ≥ 1 |
| `strip` | map key → bool | `{}` | Patch over the catalog: `false` — keep, `true` — strip additionally. An unknown key is a start error |
| `rewrite` | map node type → JSON patch | `{}` | JSON merge-patch over the config of a node of that type; applies to links (positions ≥ 1) |

The position order is the one thing to remember: **not** "who goes through whom", as with
`detour`, but "in what order the packet travels".

### `strip` catalog

| Key | By default | What is removed | Why it is one-sided |
|-----|------------|-----------------|---------------------|
| `tls.fragment` | stripped | packet-level ClientHello fragmentation (`fragment`, `fragment_fallback_delay`) | the server never sees it; inside a tunnel it only adds latency and failure points. **`record_fragment` is not touched** — under `detour` it is switched on automatically as a path fix (see [HOTFIXES / 060](../004-HOTFIXES/FEATURE.md)) and is not subject to stripping |
| `multiplex.padding` | stripped | multiplexer padding | requested by the client; a server that supports it accepts traffic without it just the same |
| `xhttp.padding` | stripped | XHTTP padding — reduced to the minimum of the range, obfs mode off | the server accepts any length |
| `tls.utls` | **not** stripped | ClientHello fingerprint imitation | not a source of failures; can be stripped via `strip`, except on nodes with `reality` (where it is mandatory — start error) |

The catalog deliberately **excludes** parameters that are contracts with the server: `flow`,
`obfs`, `shadowtls`, `plugin`, `udp_over_tcp`, `ech`, transport paths and hosts. The chain never
touches those.

### Build

Tag `with_lx_chain`; without it `type: chain` is rejected when the config is read, and the rest of
the core behaves exactly like upstream. Exposing chain state to the client is part of
[OBSERVABILITY](../006-OBSERVABILITY/FEATURE.md), behind its `with_lx_command` tag.

## Inputs

- The chain config and the configs of the nodes/groups named by the position tags (the chain reads
  them without modifying the originals).
- The current pick of every group at a position — whatever that group would choose for an ordinary
  connection, with all of its logic (manual selection, health check, sticky, penalties, fallback).
- Connections from the router (TCP/UDP) — as for any outbound.

## Outputs

- On the wire: a single nested connection — the transport of position 0 carries the transport of
  position 1, which carries position 2, and so on to the destination. Each next hop is the payload
  of the previous one.
- Observability: the resolved path of a connection (which node at which position) in the tail of
  `detourList`; chain state per position — the picked node, the link state (none / starting /
  active / suspended / idle), live connections, effective MTU and what was stripped or rewritten;
  counters for dials, per-position errors, link creations and removals.
- Dial errors name the position and what they went through:
  `chain[tag] #2 (warp-exit) via #1 (node-m): …`.

## Data flow

```
config ──► START
   │  validation: ≥2 positions, tags exist, link types at positions ≥1 are
   │  allowed (node with dial fields | direct | block | a group of those);
   │  strip/rewrite apply to EVERY reachable node (dry run);
   │  a cycle "group contains the chain" — rejected
   │  warm-up: links of deterministic positions (nodes, selectors — not urltest)
   ▼
connection ──► POSITION n−1 (exit)
   │  node ───────► its LINK (runtime instance "node through position n−2")
   │  group ──────► the group picks by its own logic ──► LINK of the picked node
   │  direct ─────► transparent: straight to position n−2 (the hop is "off")
   │  block ──────► connection rejected
   ▼  ... the link dials ITS OWN server through position i−1 ...
POSITION 0 (entry) ──► the original as is: its dial fields, its network, no link
   ▼
the destination sees the exit node's address

LINK = (position, node): created on first pick (or by warm-up), config = original
   − strip + rewrite, then MTU lowered by the overhead of IP tunnels below it;
   same tag as the original (so the group's history/penalties/sticky keep working);
   lives while it has connections OR was picked within idle_timeout; a sleeping
   WireGuard link of a chain obeys the ENERGY rules
GROUPS ARE NEVER COPIED; links exist only for nodes at positions ≥ 1
```

## Rules and guarantees

- **Order = packet order.** `outbounds[0]` is the entry, the last one is the exit.
- **Position 0 is never modified.** Its dial fields (`bind_interface`, `routing_mark`, …) apply;
  neither `strip`, nor `rewrite`, nor the MTU adjustment touch it.
- **Groups are not copied.** All selection logic stays with the original group: manual selection,
  health check, sticky, penalties and fallback, `interrupt_exist_connections`. Switching a group
  with that flag also cuts the chain's connections that went through it, including the tunnels of
  links above — they come back up through the new pick.
- **A link = the original minus the DPI garb, plus `rewrite`, with an adjusted MTU.** The order of
  transformations is fixed: `strip` → `rewrite` → MTU. `mtu` in a node's config means "as a
  standalone node"; the chain only **lowers** it by the exact overhead of the IP tunnels below the
  link (WireGuard inside an IP tunnel −60/−80 by the server's address family, MASQUE ≈ −90); over
  stream and datagram proxies the MTU is left alone. Under a group the worst case over all its
  members is taken — so a link is not rebuilt when a group below switches.
- **Transparent `direct`.** At a position ≥ 1 `direct` means "no hop here": the path shortens by
  that position. `direct` as a member of a selector is a runtime off-switch for the position.
  `block` is terminal at any position.
- **Lazy creation, deterministic warm-up.** Links are created on the first pick of a node; at start
  only the links of positions whose pick is known are raised (nodes, selectors of any nesting
  without a urltest on the path). A warm-up failure is a start failure.
- **Removal by idleness, not by switching.** A link is removed only when it has zero live
  connections **and** was not picked for longer than `idle_timeout`; a live stream through the old
  pick keeps its link. A sleeping tunnel of a link is an ENERGY state, not a removal.
- **Fail-fast at start:** an unknown `strip` key; a `rewrite` that does not decode into the config
  of at least one reachable node; `tls.utls: true` on a node with `reality`; a disallowed type at a
  position ≥ 1; a chain tag colliding with user tags; a cycle through groups.
- **Internal tags.** The chain reserves tags of the form `<tag>#<i>`; they are addressable for
  latency probing per path prefix, but are not part of the outbound lists.

## Boundaries

- **A group at a position ≥ 1 ranks its nodes by direct measurements**, not "through the hops
  below". Per-layer path latency comes from probing the internal tags `<tag>#0`, `#1`, …. Passive
  feedback and penalties from the chain's dials are attributed to the node's tag — shared between
  its direct and its chained role.
- **UDP capability of the hop below is not checked statically**: a tunnel at position i above a
  node without UDP at position i−1 produces a dial error naming both positions, not a start error.
- **Sticky urltest at a position ≥ 1** is keyed by the address of the server of the node above it,
  not by the final destination.
- **A nested chain** is allowed only at position 0.
- **DPI between hops** (`["domestic-relay", "foreign-node"]`) needs
  `"strip": {"tls.fragment": false}` — the default assumes DPI between the client and the entry.
- **A tunnel over a datagram proxy** (hysteria2, tuic, ss-UDP) keeps the configured `mtu` — the
  physical path is unknown, exactly as with a plain `detour`; over `tuic` in `native` mode a
  warning is emitted.
- **The first dial to a new pick** pays for creating the link synchronously (for WireGuard — stack
  start plus handshake).
- Server side, per-position overrides outside `rewrite`-by-type, and limits on the number of links
  are deliberately not done.

</details>

<details open>
<summary><h2>🇷🇺 Русский</h2></summary>

## Назначение

Многохоповый маршрут `клиент → хоп 1 → хоп 2 → … → цель` сегодня собирается только вручную:
узлу-выходу прописывается `detour` на узел-вход, и связь зашивается в сами узлы. Узел, нужный
в двух ролях (сам по себе и как звено), приходится дублировать в конфиге; группа (`selector`,
`urltest`) звеном быть не может вовсе — `detour` на группу переадресует один узел, но не даёт
выбирать, *через что* этот узел идёт.

Фича добавляет outbound типа `chain`: упорядоченный список позиций, каждая из которых — узел
**или группа**. Цепочка собирается в рантайме из того, что группы выбрали прямо сейчас; конфиг
узлов не дублируется; переключение группы на любой позиции меняет путь без перезапуска. Снаружи
цепочка — обычный outbound: её можно положить в маршрут, в `selector`, в `urltest`.

## Контролируемые параметры

Поля и их значения — в таблице английского блока (`outbounds`, `idle_timeout`, `strip_evasion`,
`strip`, `rewrite`), пример конфига — там же.

- `outbounds` — позиции **в порядке пакета**: `[0]` — первый хоп от клиента (касается реальной
  сети), последний — узел, чей адрес видит цель. Любая позиция — узел, endpoint или группа любой
  вложенности.
- `idle_timeout` — сколько рантайм-звено может простаивать до удаления; `0` — жить до остановки.
- `strip_evasion` — снимать у звеньев на позициях ≥ 1 односторонние DPI-приёмы (каталог ниже).
- `strip` — патч к каталогу: `false` — не снимать, `true` — снимать дополнительно; неизвестный
  ключ — ошибка старта.
- `rewrite` — JSON merge-patch поверх конфига узла данного типа; действует на звеньях (позиции ≥ 1).

Порядок позиций — единственное, что нужно запомнить: **не** «кто через кого», как у `detour`,
а «в каком порядке идёт пакет».

### Каталог `strip`

- `tls.fragment` — снимается по умолчанию: пакетная фрагментация ClientHello (`fragment`,
  `fragment_fallback_delay`). Сервер её не видит; внутри туннеля она даёт лишь задержки и точки
  отказа. **`record_fragment` не трогается** — под `detour` он включается автоматически как защита
  пути (см. [HOTFIXES / 060](../004-HOTFIXES/FEATURE.md)) и снятию не подлежит.
- `multiplex.padding` — снимается: паддинг запрашивается клиентом, сервер одинаково принимает
  трафик и без него.
- `xhttp.padding` — снимается: диапазон сводится к минимуму, обфус-режим выключается; сервер
  допускает любую длину.
- `tls.utls` — **не** снимается по умолчанию: имитация отпечатка ClientHello не источник отказов.
  Снять можно через `strip`, кроме узлов с `reality` (там utls обязателен — ошибка старта).

В каталог намеренно **не входят** параметры-контракты с сервером: `flow`, `obfs`, `shadowtls`,
`plugin`, `udp_over_tcp`, `ech`, пути и хосты транспортов. Их цепочка не трогает никогда.

### Сборка

Тег `with_lx_chain`; без него `type: chain` отвергается на чтении конфига, остальное ядро ведёт
себя в точности как upstream. Выдача состояния цепочки клиенту — часть
[OBSERVABILITY](../006-OBSERVABILITY/FEATURE.md), за её тегом `with_lx_command`.

## Входы

- Конфиг цепочки и конфиги узлов/групп по тегам позиций (цепочка читает их, не меняя оригиналов).
- Текущий выбор каждой группы на позиции — то, что группа выбрала бы для обычного соединения, со
  всей её логикой (ручной выбор, health-check, sticky, штрафы, fallback).
- Соединения из маршрутизатора (TCP/UDP) — как у любого outbound.

## Выходы

- Провод: одно вложенное соединение — транспорт позиции 0 несёт транспорт позиции 1, тот —
  позиции 2, и так до цели. Каждый следующий хоп — полезная нагрузка предыдущего.
- Наблюдаемость: разрешённый путь соединения (какой узел на какой позиции) в хвосте `detourList`;
  состояние цепочки по позициям — выбранный узел, состояние звена (нет / поднимается / активно /
  уснуло / простаивает), живые соединения, эффективный MTU и что снято/переписано; счётчики
  дозвонов, ошибок по позициям, созданий и удалений звеньев.
- Ошибки дозвона — с указанием позиции и пары «через что»:
  `chain[tag] #2 (warp-exit) via #1 (node-m): …`.

## Data flow

Схема — в английском блоке выше (диаграмма общая). Ключевое:

- **Звено** = пара (позиция, узел): создаётся при первом выборе или прогревом; конфиг = оригинал
  − `strip` + `rewrite`, затем MTU понижен на накладные IP-туннелей под ним; тег совпадает с
  оригиналом (поэтому история/штрафы/sticky группы продолжают работать); живёт, пока есть
  соединения ИЛИ его выбирали в пределах `idle_timeout`; уснувшее WG-звено подчиняется правилам
  ENERGY.
- **Группы не копируются никогда**; звенья существуют только у узлов на позициях ≥ 1.
- Позиция 0 — оригинал как есть: его dial-поля, его сеть, без звена.

## Правила и гарантии

- **Порядок = порядок пакета.** `outbounds[0]` — вход, последний — выход.
- **Позиция 0 не изменяется.** Её dial-поля (`bind_interface`, `routing_mark`, …) действуют; ни
  `strip`, ни `rewrite`, ни подгонка MTU её не касаются.
- **Группы не копируются.** Вся логика выбора — оригинальной группы: ручной выбор, health-check,
  sticky, штрафы и fallback, `interrupt_exist_connections`. Переключение группы с этим флагом рвёт
  и соединения цепочки, прошедшие через неё, включая туннели звеньев выше — они переподнимаются
  через новый выбор.
- **Звено = оригинал минус DPI-обвес плюс `rewrite`, с подогнанным MTU.** Порядок преобразований
  фиксирован: `strip` → `rewrite` → MTU. `mtu` в конфиге узла трактуется как «MTU как
  самостоятельного»; цепочка только **понижает** его на точные накладные IP-туннелей под звеном
  (WireGuard внутри IP-туннеля −60/−80 по семейству адреса сервера, MASQUE ≈ −90); над потоковыми
  и датаграммными прокси MTU не меняется. Под группой берётся худший случай по всем её участникам —
  звено не пересоздаётся при переключении группы ниже.
- **Прозрачный `direct`.** На позиции ≥ 1 `direct` — «хопа нет»: путь укорачивается на эту
  позицию. `direct` участником селектора = выключатель позиции на лету. `block` — терминален на
  любой позиции.
- **Ленивое создание, детерминированный прогрев.** Звенья создаются при первом выборе узла; на
  старте поднимаются только звенья позиций, чей выбор известен (узлы, селекторы любой вложенности
  без urltest на пути). Ошибка прогрева = ошибка старта.
- **Удаление по простою, не по переключению.** Звено удаляется только при нуле живых соединений
  **и** отсутствии выбора дольше `idle_timeout`; живой поток через старый выбор держит звено.
  Уснувший туннель звена — состояние ENERGY, не удаление.
- **Fail-fast на старте:** неизвестный ключ `strip`; `rewrite`, который не декодируется в конфиг
  хотя бы одного достижимого узла; `tls.utls: true` при узле с `reality`; недопустимый тип на
  позиции ≥ 1; коллизия тега цепочки с пользовательскими тегами; цикл через группы.
- **Внутренние теги.** Цепочка резервирует теги вида `<tag>#<i>`; они адресуемы для проверки
  задержки по префиксу пути, но не входят в списки outbound'ов.

## Границы

- **Группа на позиции ≥ 1 ранжирует по прямым замерам** своих узлов, не «через нижние хопы».
  Задержку пути по слоям даёт проверка внутренних тегов `<tag>#0`, `#1`, …. Пассивный фидбек и
  штрафы от дозвонов цепочки приписываются тегу узла — общему для прямой и цепочечной роли.
- **UDP-способность нижнего хопа не проверяется статически**: туннель на позиции i над узлом без
  UDP на позиции i−1 даёт ошибку дозвона с указанием позиций, не ошибку старта.
- **Sticky urltest на позиции ≥ 1** ключуется адресом сервера узла над ней, не конечной целью.
- **Вложенная цепочка** допустима только на позиции 0.
- **DPI между хопами** (`["domestic-relay", "foreign-node"]`) требует
  `"strip": {"tls.fragment": false}` — дефолт рассчитан на DPI между клиентом и входом.
- **Туннель над датаграммным прокси** (hysteria2, tuic, ss-UDP) сохраняет `mtu` конфига —
  физический путь неизвестен, как и при обычном `detour`; над `tuic` в `native`-режиме выдаётся
  предупреждение.
- **Первый дозвон к новому выбору** платит создание звена синхронно (для WireGuard — старт стека
  и хендшейк).
- Серверная сторона, per-позиционные переопределения вне `rewrite`-по-типу, лимиты числа звеньев —
  намеренно не делаем.

</details>

---

## Tasks / Задачи фичи

| Task / Задача | What it delivers / Что даёт |
|---------------|-----------------------------|
| [073](../../TASKS/073-CHAIN_OUTBOUND/SPEC.md) | Outbound `chain`: group positions, runtime links, transparent `direct`, warm-up/idleness, MTU, `strip`/`rewrite`, observability / Outbound `chain`: позиции-группы, рантайм-звенья, прозрачный `direct`, прогрев/простой, MTU, `strip`/`rewrite`, наблюдаемость |
