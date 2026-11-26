# FEATURE 015 — CHAIN — a multi-hop path of groups and nodes / цепочка хопов из групп и узлов

| Field / Поле | Value / Значение |
|--------------|------------------|
| Type / Тип | Product feature / Продуктовая фича |
| Build tag | `with_lx_chain` (chain state and the config handles — behind `with_lx_command`, see [OBSERVABILITY](../006-OBSERVABILITY/FEATURE.md)) / (состояние цепочки и ручки конфига — за `with_lx_command`) |
| State / Состояние | 🔄 Model rewritten by [165](../../TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md): code on a branch, unit tests and the live stands `lx-test/chain` (shadowsocks hops, WireGuard on a position) green; not released yet / Модель переписана задачей [165](../../TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md): код в ветке, юниты и живые стенды `lx-test/chain` (shadowsocks-хопы, WireGuard на позиции) зелёные; в релиз ещё не вошла. Before / До: [073](../../TASKS/073-CHAIN_OUTBOUND/SPEC.md) (link instances, see its HISTORY), [075](../../TASKS/075-CHAIN_POSITION_TOGGLE/SPEC.md) |

> **Language.** This document carries both languages in one file: the two blocks below hold the
> same content, not a word-by-word translation. The tables and code blocks are shared and live
> in the English block.
>
> **Язык.** Документ билингвальный: два блока ниже несут одно и то же содержание, а не пословный
> перевод. Таблицы и блоки кода общие и живут в английском блоке.

---

<details open>
<summary><h2>🇬🇧 English</h2></summary>

## Purpose

A multi-hop route `client → hop 1 → hop 2 → … → destination` by hand means giving every node a
`detour` onto the one below, and a group (`selector`, `urltest`) cannot be "the hop below" for a
whole set of nodes at once. An outbound of type `chain` is an ordered list of positions, each a
node, an endpoint **or a group**; switching a group on any position changes the path without a
restart. From the outside a chain is an ordinary outbound.

## Controlled parameters

```json
{
  "type": "chain",
  "tag": "virtualisation",
  "outbounds": ["selector-in", "selector-mid", "selector-exit"],
  "rewrite": { "wireguard": { "mtu": 1200 } },
  "interrupt_exist_connections": false
}
```

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `outbounds` | list of tags, ≥ 2, all distinct | required | Positions **in packet order**: `[0]` is the entry (it touches the real network), the last one is the node whose address the destination sees |
| `rewrite` | map node type → JSON merge-patch | `{}` | Applied to every node a position ≥ 1 takes over; it cannot touch `detour` |
| `interrupt_exist_connections` | bool | `false` | On a position toggle, also cut the user connections through the chain (selector semantics); the sockets the nodes keep through the hops are always cut |

`idle_timeout`, `strip_evasion` and `strip` of the old link model fail the load and say what
replaced them. What the core does to a node under `detour` — the MTU alignment and the strip
filters — lives in the root block `lx.detour` ([SPEC 165 §2.1](../../TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md)):

```json
"lx": { "detour": {
  "mtu_align": "clamp",
  "strip": { "filters": { "tls": { "fragment": true, "spoof": true, "kernel": true, "engine": true } } },
  "except_outbounds": ["foreign-node"]
} }
```

| `lx.detour` | `mtu_align` | `strip` |
|-------------|-------------|---------|
| absent / `true` | `clamp` | the whole catalog |
| `false` | `off` | none — upstream behaviour |
| object | as written, absent → `clamp` | as written, absent → the catalog; an object of `filters` replaces the catalog whole |

## How it works

```
config load (lx pre-pass, before any node is created)
   chain C = [P0, P1, …, Pn−1]
   ├─ hops C#0 … C#n−1 appended as outbounds of type chain-hop
   ├─ every node reached on Pi, i ≥ 1 (group members of any depth, not direct/block)
   │    gets detour = C#(i−1); its own detour is overridden; rewrite by type applied
   ├─ conflicts fail the load (node taken over twice, taken over and on an entry,
   │    nested chain above position 0, node without detour support,
   │    WireGuard with listen_port; a ring is upstream's circular dependency)
   └─ then path-MTU alignment (lx.detour.mtu_align), then the sniff guard graph
runtime
   C ──► C#(n−1) ──► Pn−1 (node: dials itself, its detour is C#(n−2);
                          group: picks by its own logic, the pick goes its detour)
        C#i: position disabled → C#(i−1); P0 disabled → the kernel's direct dialer;
             direct on Pi (i ≥ 1) → C#(i−1); block → refused
```

## Rules and guarantees

- **One instance, one path.** A node has one instance; a route straight to a taken-over node goes
  the chain's path. A group on a position checks the very instances that carry its traffic: a
  node dead on the chain's path leaves the pool even if it is alive on its own `detour`
  (LxBox#233).
- **Position 0 is not rewritten**: its nodes go their own way.
- **Groups are not rewritten**, only their leaves; all selection logic stays with the group.
- **`direct` on a position ≥ 1** — a position or a group pick — passes the dial to the hop below:
  no traffic leaves past the positions below.
- **Every change is recorded.** `GetOutboundConfig(tag)` returns `original` (as written),
  `effective` (after the pre-pass) and `changes` (`chain`, `chain_rewrite`, `lx.detour`, `mtu_align`, in that
  order); applied to `original` they give `effective`. `GetEffectiveConfig` returns the whole
  effective config, hops included.
- **MTU.** The alignment of [SPEC 120](../../TASKS/120-PATH_MTU_ALIGN/SPEC.md) /
  [144](../../TASKS/144-MTU_ALIGN_STREAM_BOUNDARY/SPEC.md) applies to taken-over nodes like to any
  `detour`; a hop carries `min(position, hop below)`, so a node above fits whether the position
  is on, off or picks `direct`.
- **Strip filters** act on any node with `detour`, per handshake, only when its conn is not a
  kernel socket: `tls.fragment` (the split sleeps 500 ms per piece inside a tunnel;
  `record_fragment` stays), `tls.spoof`, `tls.kernel` (kTLS), `tls.engine` (`apple` → `go`).
  The decision of `lx.sniff_guard` is stronger than the filter.
- **Runtime toggle** of any position (`SetChainPositionEnabled`), persisted in the cache file.
- **Hop tags** `<chain>#<i>` are a public contract; UIs hide the `chain-hop` type.

## Boundaries

- A chain has no rings: a ring is a plain detour cycle after the rewrite, and upstream's
  start-order check fails the load (`circular outbound dependency`, hop tags included);
  `[A, Dir{A, X}]` is one. The core itself checks only rewrite conflicts — a node or a group in
  two chains or on two positions >= 1 of one chain. Clients build chains without either.
- A taken-over node cannot carry a fixed local port: the load fails (ALGORITHM К5). Socket
  options upstream ignores under any `detour` (`bind_interface`, `routing_mark`,
  `tcp_fast_open`, …) do nothing here too; `GetOutboundConfig` shows them as `lx.detour` changes
  (Д1).
- UDP capability of the hop below is not checked statically: a dial error names the positions.
- A nested chain is allowed only on position 0.

</details>

<details open>
<summary><h2>🇷🇺 Русский</h2></summary>

## Назначение

Многохоповый маршрут `клиент → хоп 1 → хоп 2 → … → цель` вручную — это `detour` каждому узлу на
узел ниже, и группа (`selector`, `urltest`) не может быть «хопом ниже» сразу для набора узлов.
Outbound типа `chain` — упорядоченный список позиций, каждая из которых узел, endpoint **или
группа**; переключение группы на любой позиции меняет путь без перезапуска. Снаружи цепочка —
обычный outbound.

## Контролируемые параметры

Поля, пример и таблица `lx.detour` — в английском блоке.

- `outbounds` — позиции **в порядке пакета**: `[0]` — вход, касается реальной сети, последний —
  узел, чей адрес видит цель.
- `rewrite` — JSON merge-patch по типу узла для всех узлов, которые перехватывают позиции ≥ 1;
  `detour` им менять нельзя.
- `interrupt_exist_connections` — при переключении позиции рвать и пользовательские соединения
  через цепочку; сокеты, которые узлы держат через хопы, рвутся всегда.

`idle_timeout`, `strip_evasion` и `strip` прежней модели звеньев — ошибки загрузки с указанием,
что их заменило. Что ядро делает с узлом под `detour` — выравнивание MTU и фильтры `strip` —
живёт в корневом блоке `lx.detour`: без ключа и `true` — `clamp` и весь каталог, `false` —
поведение апстрима, объект — что написано.

## Как работает

При загрузке конфига, до создания узлов, цепочка добавляет хопы `<цепочка>#<i>` (тип
`chain-hop`) и каждому узлу, до которого доходит позиция `i ≥ 1` (члены групп любой вложенности,
кроме `direct` и `block`), ставит `detour` на хоп позиции ниже; собственный `detour` узла
перекрывается, `rewrite` применяется по типу. Конфликты — ошибки загрузки. Затем идут выравнивание
MTU и граф носителей `sniff_guard`. Во время работы цепочка дозванивается через верхний хоп; хоп
идёт в свою позицию, узел позиции — своим `detour` в хоп ниже. Выключенная позиция пропускается,
выключенный вход — прямой dialer ядра, `direct` на позиции ≥ 1 — хоп ниже, `block` — отказ.

## Правила и гарантии

- **Один экземпляр, один путь.** Прямой маршрут на перехваченный узел идёт путём цепочки.
  Группа на позиции проверяет те экземпляры, что несут трафик: узел, мёртвый путём цепочки,
  уходит из пула, даже если жив своим `detour` (LxBox#233).
- **Позиция 0 не перезаписывается**, **группы не перезаписываются** — меняются только листья.
- **`direct` на позиции ≥ 1** — позицией или выбором группы — передаёт дозвон хопу ниже: мимо
  нижних позиций трафик не уходит.
- **Каждое изменение записано.** `GetOutboundConfig(tag)` отдаёт `original`, `effective` и
  `changes` (`chain`, `chain_rewrite`, `lx.detour`, `mtu_align`); применённые к `original`, они дают
  `effective`. `GetEffectiveConfig` — весь итоговый конфиг вместе с хопами.
- **MTU** — общее выравнивание SPEC 120/144; ёмкость хопа — `min(позиция, хоп ниже)`, чтобы узел
  над ним работал при включённой, выключенной позиции и при выбранном `direct`.
- **Фильтры `strip`** — на любом узле с `detour`, на каждом рукопожатии, только без настоящего
  сокета ядра: разрез ClientHello, `tls.spoof`, kTLS, движок `apple` → `go`. Решение
  `sniff_guard` сильнее фильтра.
- **Выключение позиции на лету** (`SetChainPositionEnabled`), с сохранением в cache-file.
- **Теги хопов** `<цепочка>#<i>` — публичный контракт; UI скрывает тип `chain-hop`.

## Границы

- Колец в цепочке нет: после перезаписи кольцо — обычный цикл `detour`, и апстримная
  проверка порядка запуска валит загрузку (`circular outbound dependency`, с тегами хопов);
  `[A, Dir{A, X}]` — кольцо. Ядро само проверяет только конфликт перезаписи — узел или группа
  в двух цепочках или на двух позициях ≥ 1 одной цепочки. Клиенты собирают цепочки без того
  и другого.
- У перехваченного узла не может быть фиксированного локального порта: загрузка падает
  (ALGORITHM К5). Опции сокета, которые апстрим под любым `detour` игнорирует
  (`bind_interface`, `routing_mark`, `tcp_fast_open`, …), не действуют и здесь;
  `GetOutboundConfig` показывает их записями `lx.detour` (Д1).
- UDP-способность хопа ниже статически не проверяется: ошибка дозвона называет позиции.
- Вложенная цепочка — только на позиции 0.

</details>

---

## Tasks / Задачи фичи

| Task / Задача | What it delivers / Что даёт |
|---------------|-----------------------------|
| [073](../../TASKS/073-CHAIN_OUTBOUND/SPEC.md) | First model: runtime link instances per position; replaced by 165, see its HISTORY / Первая модель: рантайм-звенья по позициям; заменена задачей 165, см. её HISTORY |
| [075](../../TASKS/075-CHAIN_POSITION_TOGGLE/SPEC.md) | Runtime enable/disable of any position, cache-file persistence, selector-style interrupt / Вкл/выкл позиции на лету, сохранение в cache-file, разрыв по селекторной модели |
| [120](../../TASKS/120-PATH_MTU_ALIGN/SPEC.md) | Path-MTU alignment under `detour` (now `lx.detour.mtu_align`) / Выравнивание MTU под `detour` (теперь `lx.detour.mtu_align`) |
| [144](../../TASKS/144-MTU_ALIGN_STREAM_BOUNDARY/SPEC.md) | Only IP tunnels and groups carry a capacity upwards; IPv6 floor 1280; AWG `s4` / Ёмкость наверх несут только IP-туннели и группы; пол IPv6 1280; `s4` AWG |
| [165](../../TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md) | Chain as a rewrite of `detour`, `lx.detour` (mtu_align + strip filters), `GetEffectiveConfig` / `GetOutboundConfig` / Цепочка как перезапись `detour`, `lx.detour`, ручки итогового конфига |
