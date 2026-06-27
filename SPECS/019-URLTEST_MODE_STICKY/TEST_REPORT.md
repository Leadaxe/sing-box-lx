# TEST_REPORT — 019 URLTEST_MODE_STICKY

**Дата:** 2026-06-28 · **Статус:** Протестировано (юнит + живой прогон) · **База:** `v1.14.0-alpha.35` (ветка `lx-1.14`)

## Что тестировали

SPEC 019: `mode` у urltest-группы (`least_test` | `round_robin` | `least_connection`)
плюс per-flow `sticky` (`jumphash` | `ttlmap`). Код:
[urltest.go](../../protocol/group/urltest.go),
[urltest_balance_lx.go](../../protocol/group/urltest_balance_lx.go),
[group.go](../../option/group.go).

## Стенд

- Бинарь собран из ветки `lx-1.14`, теги
  `with_gvisor,with_quic,with_utls,with_wireguard,with_clash_api` — **отдельный**
  экземпляр, рядом с launcher-ядром, не вместо него (launcher не трогали).
- 5 реальных vless-нод (из живого конфига; 5 регионов ЕС), urltest-группа поверх них.
- Три независимых экземпляра на своих портах (mixed-in + Clash API), по одному
  на режим. Liveness наблюдали через Clash API `/proxies`, выбор ноды на каждое
  соединение — по debug-логу outbound dial.
- На момент прогонов live было 2–3 ноды из 5 (часть отвечала `context deadline
  exceeded` на health-check) — что само по себе проверяет работу с неполным
  live-набором.

## Юнит-тесты

`go test -race` по `./protocol/group/` — **15/15 PASS**:

```
TestBalancerLeastTestIsNil            TestRoundRobinDistribution
TestBalancerLeastConnectionRejected   TestRoundRobinFallbackWhenNoneLive
TestBalancerUnknownModeRejected       TestRoundRobinSingleLive
TestJumpConsistentHashBounds          TestJumpConsistentHashStable
TestStickyJumphashSameKeySameNode     TestStickyEmptyKeyFixedNode
TestStickyTTLMapStickAndExpire        TestStickyTTLMapDeadNodeRepick
TestStickyTTLMapCap                   TestStickyKeyComponents
TestStickyValidation
```

## Живые прогоны

| # | Сценарий | Конфиг | Ожидание | Результат |
|---|----------|--------|----------|-----------|
| 1 | **round_robin без sticky**, 2 live-ноды | `mode: round_robin` | чередование по live-набору | строгое чередование A↔B, **10/10** соединений раскиданы поровну ✓ |
| 2 | **Мёртвые ноды исключены** | (тот же) | DEAD-узлы не выбираются | 3 неживые ноды ни разу не выбраны; ротация только по live ✓ |
| 3 | **sticky ttlmap** | `sticky.mode: ttlmap`, `hash: [source_ip, dest_ip, dest_port]` | один ключ → одна нода | все соединения с одинаковым ключом залипли на одну ноду ✓ |
| 4 | **sticky jumphash** | `sticky.mode: jumphash`, `hash: [domain]` | домен→нода детерминированно и стабильно; спред ~1/n | каждый из 8 доменов стабильно на свою ноду (повтор → та же нода), спред 4/4 по двум нодам ✓ |

### Negative (валидация при старте)

Все отвергаются на этапе построения outbound с внятным сообщением:

| Вход | Сообщение |
|------|-----------|
| `mode: least_connection` | `urltest mode least_connection is not implemented yet (SPEC 019 phase 2)` |
| `mode: <неизвестный>` | `unknown urltest mode: …` |
| `sticky.hash: [<неизвестный компонент>]` | `unknown urltest sticky hash component: …` |
| `sticky.mode: <неизвестный>` | `unknown urltest sticky mode: …` |

## Нюанс конфигурации (не баг — свойство дизайна)

Компонент `dest_ip` в sticky-ключе берётся из `destination.Addr`, который **пуст,
пока цель не зарезолвлена** (см. `stickyComponent` в
[urltest_balance_lx.go](../../protocol/group/urltest_balance_lx.go) — ветка
`URLTestStickyDestIP` отдаёт `""` при невалидном адресе). При доменном входе
(socks5h / sniff) на момент построения ключа адрес ещё доменный, поэтому `dest_ip`
вырождается в `""`.

Практическое следствие: если для доменного трафика собрать ключ только из
`source_ip|dest_ip|dest_port` при одном источнике, все компоненты совпадут →
единый ключ → весь трафик залипнет на одну ноду (формально корректная работа
sticky, но не та грануляция, что ожидалась). Для доменного трафика в `hash`
следует класть `domain`. На `round_robin` без sticky это не влияет.

## Вывод

Фича работает по спецификации на реальных серверах: round_robin распределяет
строго по live-набору и исключает мёртвые ноды; sticky (обе стратегии) залипает
детерминированно; невалидная конфигурация отвергается при старте; `least_connection`
честно зарезервирован под phase 2. Гонок не выявлено (`-race` чист на юнитах и при
живых прогонах). Изменений в рабочем дереве по итогам тестирования нет.
