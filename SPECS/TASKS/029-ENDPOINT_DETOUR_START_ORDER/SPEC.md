# SPEC: 029 — ENDPOINT_DETOUR_START_ORDER

**Фича:** [HOTFIXES](../../FEATURES/HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — WG/AWG-endpoint с `detour` навсегда мёртв, если провайдер detour объявлен в конфиге ПОЗЖE потребителя |
| Статус | C — первопричина устранена апстримом (`f39ab0e9`, lx.15); держим только fail-fast резолв на старте, red/green e2e-стенд зелёный |
| Владелец | Момент резолва detour у `wireguard`-endpoint (`protocol/wireguard/endpoint.go`). Механизм упорядочивания старта (`startOutbounds` топосорт) — upstream, не трогаем |

## 0. TL;DR

WG/AWG-endpoint с `detour: X` резолвил свой detour **слишком рано — прямо в
конструкторе `NewEndpoint`**, в фазе `Create`, до того как построен весь граф
узлов. Endpoints создаются строго в порядке массива `endpoints[]` и попадают в
реестр только в **конце** своего `Create`. Поэтому если провайдер detour (`X`)
объявлен в конфиге **позже** потребителя, в момент резолва его ещё нет в
реестре → `DetourDialer` через `sync.Once` кэширует ошибку
`outbound detour not found: X` **навсегда** → туннель не отправляет ни байта,
в логе вечно `connect to server: outbound detour not found: X`.

Смена порядка в конфиге (провайдер раньше потребителя) «чинила» симптом
случайно. Ядро **умеет** упорядочивать старт по зависимостям (топосорт
`startOutbounds`: узел не стартует, пока не стартовали все его `Dependencies()`,
а `detour` объявлен как зависимость) — но резолв **утекал из-под** этого
барьера в фазу `Create`, где порядка нет.

**Состояние сейчас:** преждевременный резолв убран **апстримом** (`f39ab0e9`,
lx.15) — конструкторный каст удалён, проба egress-anchor переехала в транспортный
слой в фазу Start. Баг закрыт не нашим кодом.

Мы держим одну правку: **явный резолв detour в `Start(StartStateStart)`** — за
топосорт-барьером, где провайдер гарантированно уже стартовал. Она не чинит баг,
а страхует от тихого отказа: `not found` там = честная опечатка в теге →
fail-fast на старте, **без ретраев**, вместо немого вечного `sync.Once`-кэша.
История двухчастного фикса и его растворения в мерже — [HISTORY.md](HISTORY.md).

## 1. Симптом (устройство)

Цепочка `awg2-home` (AWG) с `detour: wg-parnas` (плоский WG), где `awg2-home`
объявлен в `endpoints[]` **раньше** `wg-parnas`:

```
ERROR endpoint/wireguard[awg2-home]: connect to server: outbound detour not found: wg-parnas
```
— повторяется бесконечно (наблюдалось 216 раз подряд), `awg2-home` мёртв
(delay = −1). При этом `wg-parnas` сам по себе жив (delay идёт). Смена порядка
(`wg-parnas` раньше `awg2-home`) → `awg2-home` встаёт (delay ≈ 200мс),
`outbound detour not found` исчезает (0 раз). Диагностировано по telemetry
debug-API :9269 + core-log.

## 2. Первопричина (доказана stack-trace repro)

Инструментированный двух-endpoint repro поймал стек в точке отказа:

```
DetourDialer.init  detour=wg-parnas loaded=false
  common/dialer/detour.go   (*DetourDialer).Dialer   ← sync.Once fires init
  common/dialer/detour.go   (*DetourDialer).Upstream
  sing/common.Cast[...]                               ← Cast walks Upstream()
  protocol/wireguard/endpoint.go  NewEndpoint         ← common.Cast[dialer.UDPListener](outboundDialer)
  adapter/endpoint/manager.go     Manager.Create
  box.go                          box.New  (Create loop)
```

Триггер — `common.Cast[dialer.UDPListener](outboundDialer)` в `NewEndpoint`
(проба SPEC 020 egress-anchor). `common.Cast` рекурсивно идёт по `Upstream()`;
`DetourDialer.Upstream()` вызывает `d.Dialer()` → `initOnce.Do(init)` →
`Outbound("wg-parnas")` **немедленно, в конструкторе**.

Тайминг из repro (home-first, сломанный порядок): `Create ENTER awg2-home` →
`init detour=wg-parnas loaded=false` → `Create EXIT awg2-home` →
`Create ENTER wg-parnas` (~1мс позже). Провайдер создаётся **после** резолва.
`sync.Once` замораживает `initErr` на весь жизненный цикл процесса; каждый
`receive()`/`Send()` (`client_bind.go`) вечно читает мёртвый кэш.

**Почему «дерево ещё не прочитано, но порядок помог»:** инвариант
«все `Create()` до любого `Start`» верен, но **нерелевантен** — резолв случается
не в `Start`, а в середине `Create`-цикла. До топосорт-барьера, где порядок
гарантирован.

## 3. Почему это специфично для endpoint

| Потребитель detour | Защищён от порядка? | Почему |
|---|---|---|
| DNS-транспорты (udp/quic/tls/https/tcp) | Да | Явно зовут `InitializeDetour` на своей фазе Start (после топосорта) |
| Обычные outbounds (vless/trojan/ss) | Да | Резолвят detour лениво, при первом коннекте от роутера — граф давно собран |
| **WG/AWG-endpoint** | **Нет (был)** | Единственный, кто (а) инициирует трафик сам и (б) резолвил detour в **конструкторе** через egress-anchor-каст |

## 4. Фикс (актуальное состояние)

Первопричина — преждевременный резолв в фазе Create — **устранена апстримом**
(`f39ab0e9` «Fix wg detour», приехал с мержем `4cc0aec9` в lx.15): конструкторный
каст egress-anchor удалён из [protocol/wireguard/endpoint.go](../../../protocol/wireguard/endpoint.go)
целиком, проба живёт в транспортном слое в фазе Start, то есть уже за барьером.
Наша симметричная правка (гейт `if options.Detour == ""`) при этом мерже
растворилась и в дереве отсутствует — держать её нечего. Разбор — в
[HISTORY.md](HISTORY.md).

Держим **одну** правку в [protocol/wireguard/endpoint.go](../../../protocol/wireguard/endpoint.go):

**Ранний резолв за барьером.** В `Endpoint.Start`, ветка
`case adapter.StartStateStart`, после `w.endpoint.Start(false)`:
`return dialer.InitializeDetour(w.outboundDialer)`. Обоснование:
- WG-endpoint объявляет `detour` как `Dependencies()`
  (`adapter/endpoint/adapter.go`), endpoints вливаются в топосорт
  `startOutbounds` (`adapter/outbound/manager.go` — `append(outbounds,
  endpoints...)`), который **не стартует потребителя, пока не стартовал
  провайдер**. → на этой стадии `wg-parnas` гарантированно в реестре и запущен.
- `StartStateStart`, не `StartStateStarted`: device и его receive-воркер
  поднимаются уже в Start/PostStart, `Started` был бы поздно. `StartStateStart`
  — самая ранняя безопасная точка, где топосорт уже отработал.
- `InitializeDetour` для no-detour endpoint'а — дешёвый no-op (возвращает nil
  для не-`DetourDialer`).
- `not found` здесь = провайдера нет в графе вообще (опечатка) → **fail-fast**,
  ошибка всплывает из `Start`, а не тонет в вечном `sync.Once`-кэше. **Никаких
  ретраев** — резолв за барьером всегда однозначен.

**Зачем держим, если баг ушёл.** Это не исправление бага, а защита от **тихого
отказа**. При опечатке в теге `detour` (или удалённом провайдере) ленивый резолв
закэшировал бы промах навсегда: эндпоинт стартует «успешно», в логах тишина,
трафик не идёт никогда — ровно тот симптом, с отладки которого началась задача.
Ранний резолв превращает его во внятную ошибку старта.

**Условие снятия:** апстрим введёт такой же ранний резолв detour с fail-fast сам.

**Долговечность через SPEC 020:** `DetourDialer` создаётся один раз в
`NewEndpoint`, `sync.Once` переживает suspend/resume/rebuild. `resumeOnDial` не
трогает `outboundDialer`. Одноразовый `InitializeDetour` держится вечно.

**`sync.Once` не трогаем** — за барьером первый резолв всегда корректен;
кэшировать правильный ответ навсегда — верно.

## 5. Стенд (red/green)

[test/wireguard_detour_order_lx_test.go](../../../test/wireguard_detour_order_lx_test.go)
(`with_gvisor && with_wireguard`, без AWG — баг протокол-независимый). Два
in-process box'а на loopback: клиент объявляет потребителя `wg-inner`
(`detour: wg-outer`) **раньше** провайдера `wg-outer` в `endpoints[]` — ровно
сломанный порядок. Сервер: outer-WG hairpin → inner-WG → эхо. TCP+UDP
ping-pong + large-data через двойной туннель.

- **RED** (без фикса): FAIL, ~92с таймаут — трафик не идёт, `connect to server:
  outbound detour not found: wg-outer`.
- **GREEN** (с фиксом): PASS ~2.8с — туннель встаёт независимо от порядка.

Регрессии: SPEC 020 idle-suspend юниты PASS; SPEC 028 AWG-over-AWG стенд PASS
(фикс не сломал nested-туннели).

## 6. Field-план

Debug-API :9269. Повторить сломанный порядок на устройстве: `awg2-home`
(`detour: wg-parnas`) объявить **раньше** `wg-parnas`. До фикса — вечный
`outbound detour not found: wg-parnas`, нода мёртва. После — встаёт независимо
от порядка. Проверить также AWG-over-AWG (`WARP AWG 1.5` через `awg2-home`).

## 7. Инварианты

- No-detour endpoint: поведение не изменено — ранний резолв для него дешёвый
  no-op, а egress-anchor теперь выводится в транспортном слое (апстрим).
- Прочие протоколы (DNS-транспорты, обычные outbounds): не затронуты.
- Механизм упорядочивания старта (топосорт) не изменён и никогда не был сломан —
  резолв лишь обходил его, утекая в фазу Create.
- `not found` для реально несуществующего detour — по-прежнему ошибка
  (теперь fail-fast в Start вместо немого рантайм-кэша).
- SPEC 020 suspend/resume: резолв долговечен, не пере-резолвится.

## 8. Ссылки

- [SPEC 028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) — вложенные туннели (тот же класс конфигов; DF-фикс не пересекается)
- [SPEC 020](../020-MULTI_WG_IDLE_BUFFER_HEAT/SPEC.md) — idle-suspend (egress-pool + suspend/resume, durability фикса)
- [SPEC 007](../007-AWG_OVER_WIREGUARD_DETOUR_GUARD/SPEC.md) — снятый guard AWG-over-WG (без него вложение не стартовало вовсе)
