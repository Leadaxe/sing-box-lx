# SPEC: 052 — NETSTACK_CONNECT_DEADLINE

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug-class) — хрупкость апстрима (netstack-дайлы без предохранителя), чиним у себя |
| Статус | C (complete) — фикс в дереве, юнит + стендовая верификация пройдены |
| Ветка | `lx` |
| Base | `afedb23aa` (v1.14.0-lx.20) |
| Связанные | Полевой дамп 2026-08-06 (CPH2411): 16 дайлов припаркованы в `DialTCPWithBind` на одном WARP-эндпоинте; исследование природы — [RESEARCH.md](RESEARCH.md) |

**Touches:** реестр HOTFIXES (новая строка). Промисы других фич не затронуты; SPEC 020/041/050 — совместимость разобрана ниже.

## Why

**TCP-дайлы через gVisor-netstack — единственный класс путей дайла в ядре без
таймаута.** Их четыре: WG/AWG-эндпоинт (его stackDevice делят и per-conn-дайлы
MASQUE), openvpn, openconnect и tailscale — первые через локальный
`DialTCPWithBind`, остальные три через сабмодульный `gonet.DialTCPWithBind`,
класс идентичен. `C.TCPConnectTimeout` (5s) живёт ровно в одном месте —
в системном `net.Dialer`; netstack-пути его обходят структурно, и ни один слой
выше (route, группы, detour, tun-инбаунд) не ставит дедлайн на ctx
пользовательского дайла. Единственная граница — SYN-бэкофф gVisor:
6 ретрансмитов, 1+2+4+8+16+32+64 = **~127 секунд** до `ErrTimeout`
(стенд: `2m7.065s` с точностью до секунды; ручка `TCPSynRetriesOption` в нашем
пине gVisor мертва — хранится, не читается).

Итог для пользователя: любая тихая чёрная дыра (Wi-Fi зарезал UDP к WARP,
мёртвый узел, заснувшее радио) превращается в «всё молча висит» — каждый дайл
стоит ~127с без ошибки, группе не на что реагировать, приложения по своим
таймаутам ретраят в тот же мёртвый узел. Домены хуже: `DialSerial` по адресам —
до N×127с. Это унаследованный апстрим-дизайн (файл байт-в-байт), не lx-регрессия;
QUIC-аутбаунды спасены случайно (у quic-go свой 5s-дефолт).

## Design

Один одноразовый connect-дедлайн **C.TCPTimeout (15s)** в leaf-шве —
`DialTCPWithBind` ([transport/wireguard/device_stack_gonet.go](../../../transport/wireguard/device_stack_gonet.go)):

```go
ctx, cancel := connectContextLx(ctx)   // lx: SPEC 052
defer cancel()
```

Логика и обоснование — в lx-файле
[transport/wireguard/connect_deadline_lx.go](../../../transport/wireguard/connect_deadline_lx.go);
в апстримном файле — вставка в 2 строки (громкий конфликт при мерже вместо
тихого склеивания, урок SPEC 051).

Три родственных netstack-пути (openvpn, openconnect, tailscale) зовут
сабмодульный `gonet.DialTCPWithBind` — им поставлен тот же одноразовый шов
inline (`connectCtx, cancel := context.WithTimeout(ctx, C.TCPTimeout)` +
`defer cancel()`, маркер `// lx: SPEC 052`): на каждом сайте ctx используется
только самим вызовом дайла, `gonet.TCPConn` ctx не удерживает (проверено по
пину gVisor: поля `{deadlineTimer, wq, ep, readMu, read}`, контекста нет) —
S1-безопасность идентична основному шву.

Решения, каждое — ответ на верифицированную ловушку (реестр S1–S6 в
[RESEARCH.md](RESEARCH.md)):

- **15s = бюджет проб, не 5s** (S5, S3). Health-check группы дайлит с
  `C.TCPTimeout`; пользовательский дедлайн ниже пробного открывает вилку:
  медленный-но-живой узел проходит пробы, а все пользовательские дайлы через
  него падают — перманентный outage там, где сегодня «медленно, но работает».
  Общая константа держит бюджеты в связке. Замеренный легитимный потолок —
  cold L3 (teardown→rebuild+handshake+SYN) **4.8s при RTT≈0**, в поле с одним
  потерянным initiation (+5s retry) — 8–10s; двойные туннели (vless-over-WARP)
  туда же. 5s резал бы свои же пробуждения, 15s накрывает с запасом.
- **Leaf select — единственный безопасный шов** (S1). Дедлайн привязан к
  connect-фазе через `defer cancel()` и физически не может пережить её в
  возвращённом conn. Ctx-дедлайн любым слоем выше переживает установку и
  убивает долгоживущие стримы — класс SPEC 050 (XHTTP привязывает stream-one
  request к dial-ctx).
- **Per-address бюджет — бесплатно.** Дедлайн на каждый connect ⇒ в
  `DialSerial`-цепочке каждый адрес получает свой бюджет; мёртвый первый AAAA
  не съедает бюджет живого A-фолбэка.
- **UDP не трогаем.** Netstack-«дайл» UDP локален (сети не ждёт) — висеть
  нечему; мёртвость видна только протоколу выше (QUIC/DNS несут свои таймеры);
  без connect-фазы любой дедлайн стал бы дедлайном сессии и резал здоровые
  долгоживущие потоки.
- **Ранний родительский дедлайн побеждает** (`context.WithTimeout` не
  продлевает). Истечение отдаёт `context.DeadlineExceeded` — отличим от
  `context.Canceled` отмены вызывающим, задел под классификацию ошибок.

### Что сознательно НЕ делаем здесь

Реакция группы на ошибки (переселект с мёртвого узла) — **отдельная работа**:
там ловушки S2 (неклассифицированная ошибка → пинг-понг + Interrupt всех
соединений) и S6 (реакция пробингом → wake-storm спящих эндпоинтов, ломает
SPEC 020), плюс перекрытие с авто-спасением LxBox §308. Директива владельца
для неё: временные дельты на demand-пути, не тикающие таймеры. Этот SPEC лишь
гарантирует, что ошибка появляется за 15с — реакции есть на чём работать.

## Верификация

- **Юнит** ([connect_deadline_lx_test.go](../../../transport/wireguard/connect_deadline_lx_test.go)):
  бюджет ≈ `C.TCPTimeout` и невозможность продления раннего родителя;
  blackhole-connect на channel-эндпоинте gVisor (SYN в никуда) рубится ctx за
  ~0.3с с `context.DeadlineExceeded` — не ждёт 127с бэкоффа.
- **Стенд** (desktop-бинарь + локальный WG-пир, метод в RESEARCH.md):
  blackhole-дайл (peer → TEST-NET-1) — было `2m7.065s`, стало **`15.050s`**;
  контроль после фикса без регрессий: warm-дайлы 0.007–0.05s, wake из
  idle-suspend работает (`lx idle: wake by=dial`, дайл 0.017s). До-фиксовые
  базовые длительности (warm 0.19–0.55s, cold L2/L3) — в RESEARCH.md §E2.
- **Уточнение бюджета** (адверсариальная проверка): SPEC 020 cold-wake
  (rebuild/Resume в `resumeOnDial`) выполняется ДО leaf-дайла и в 15s-бюджет
  НЕ входит — бюджет покрывает только handshake+SYN, т.е. запас ещё
  консервативнее заявленного. Пробы urltest: внешний probe-дедлайн (тот же
  `C.TCPTimeout`, но взведён раньше) всегда срабатывает первым или вровень —
  двойной отмены нет, поведение проб не изменено.

## Файлы

- [transport/wireguard/connect_deadline_lx.go](../../../transport/wireguard/connect_deadline_lx.go) — логика + обоснование (lx-owned)
- [transport/wireguard/device_stack_gonet.go](../../../transport/wireguard/device_stack_gonet.go) — 2-строчный шов (апстримный файл)
- [transport/wireguard/connect_deadline_lx_test.go](../../../transport/wireguard/connect_deadline_lx_test.go) — регрессионные тесты
- [transport/openvpn/device_stack.go](../../../transport/openvpn/device_stack.go), [transport/openconnect/device_stack.go](../../../transport/openconnect/device_stack.go), [protocol/tailscale/endpoint.go](../../../protocol/tailscale/endpoint.go) — тот же шов inline (апстримные файлы, маркер `// lx: SPEC 052`)
