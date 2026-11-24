# IMPLEMENTATION REPORT: 138 — URLTEST_RR_SKIP_DEAD_SLOT

**Фича:** [007-URLTEST_BALANCE](../../FEATURES/007-URLTEST_BALANCE/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `protocol/group/urltest_balance_lx.go` | поле `alive func(tag string) bool` у `balancer`; `pick` берёт снимок `b.slots` под `access` и дальше работает без замка; общий `liveSlot(k)` — `k % живых`-й живой слот (живых нет или набор сменился между проходами — выбор не меняется); с закреплением живой свой слот остаётся, мёртвый — `liveSlot(hash / pool)`; без закрепления — `liveSlot(counter)`, при всех живых ровно `counter % pool`; `(*URLTest).slotAlive` — узел резолвится и есть history под `RealTag`, нерезолвимый тег — мёртв (фолбэк `Pool()` на сырой тег не нужен: такой тег `resolve` и так отдаёт в fallback) |
| `protocol/group/urltest.go` | `URLTest.Start`: `s.balancer.alive = s.slotAlive` в lx-блоке рядом с `onChange` (`// lx: SPEC 138`) |
| `protocol/group/urltest_rr_skip_lx_test.go` | `TestRRSkip_counterRotatesOverLiveSlots`, `_counterAllLiveUnchanged`, `_stickyDeadSlotKeysSpread`, `_stickyRecovery`, `_allDeadKeepsLegacyPick`, `_emptyPoolFallback`, `_redirectedWrongNetworkFallsBack`, `_deadMemberKeptButSkipped` (httptest-фикстура SPEC 019, реальная `HistoryStorage`) |

Апстримный код: одна строка внутри существующего lx-блока `URLTest.Start`. Build-tag не нужен —
поведение меняется только в `mode: round_robin`.

## Проверка

go1.26.8 windows/amd64:

- `gofmt -l` по трём файлам — пусто; `go vet ./protocol/group/` и `go vet -tags "<LX_TAGS>" ./protocol/group/` — чисто.
- `go test -count=3 -ldflags=-checklinkname=0 ./protocol/group/` — ok (75 тестов: 67 прежних + 8 новых);
  с `-tags "<LX_TAGS>"` — ok. `-race` не прогонялся: на хосте нет cgo.
- До правки `pick` (поле и `slotAlive` уже есть, обхода нет) падают 5 из 8 новых:
  `counterRotatesOverLiveSlots` (`got map[a:1000 b:1000 c:1000 d:1000]`),
  `stickyDeadSlotKeysSpread` (`key host0.example of dead slot 1 went to b`),
  `stickyRecovery` (`dead b: its keys must spread over a and c, got map[b:330]`),
  `redirectedWrongNetworkFallsBack` (`pick 0: a, want fallback`),
  `deadMemberKeptButSkipped` (`dead b must be skipped, got map[a:5 b:5]`).
  Три оставшихся — регрессионные (здоровый пул, все мертвы, пустой пул) и зелёные до и после.
- Против промежуточного варианта «первый живой по кругу» (по ревью заменён) падают
  `stickyDeadSlotKeysSpread` (`got map[d:502] of 502` — два мёртвых подряд сваливались на
  один живой) и `stickyRecovery` (`got map[c:330]`).
- `go build ./...` без тегов — ok; `CGO_ENABLED=0 go build -trimpath -tags "<LX_TAGS>" -ldflags "… -checklinkname=0 …" ./cmd/sing-box`
  (эквивалент `lx-build`) — ok; `sing-box check` на всех `lx-test/config/*.json` и на конфиге
  `round_robin` из отчёта (узлы `direct`) — ok.

- Локальный A/B (Windows, два локальных SOCKS-сервера вместо двух физических, пул 4 из 4,
  `sticky_hash: ["none"]`, 40 запросов на фазу), v1.14.2-lx.11 против v1.14.2-lx.11 + патч:
  сервер B лёг — сразу 20/40 отказов у обоих; через 8 с — 20/40 против 0/40; сервер B
  вернулся — 0/40 у обоих, узлы B снова в ротации.

## На устройстве

A/B 2026-10-22 на роутере владельца отчёта. Прогон на бэкпорте, а не на `lx`.

**Стенд.** OpenWrt 25.12.5, aarch64_cortex-a53 (ASUS RT-AX59U, MT7981). Боевой конфиг без
изменений: `auto` — `round_robin`, пул 3 из 4, `pool_tolerance: 700`, `sticky_hash: ["none"]`,
`interval: 3667ms`; 4 узла на двух физических серверах (сервер 1: vless + reality с
HTTP-транспортом без mux и ss + shadowtls с h2mux); в конфиге `passive_check: true` (lx.11).

**Бинари.** Стоковый `1.14.2-lx.11` (rev `3d3d3db`) против `1.14.2-lx.11-spec138`
(rev `ef287e6` = lx.11 + этот патч, только код и тесты). Оба собраны `lx-release.yml`
(`workflow_dispatch`, `dry_run=true`) в форке, артефакт `dist-linux-arm64` (musl, CGO, тот же
набор из 18 тегов, что у стокового). Тестовый бинарь подменялся `mount --bind` из tmpfs с
рестартом службы, во флеш ничего не писалось.

**Простой.** Правило nft во временной таблице `inet`, hook output, priority −10:
`ip daddr <сервер 1> drop` на 45 с, затем `reject with tcp reset` на 30 с — оба узла
сервера 1 умирают разом.

**Нагрузка.** 4 новых HTTPS-соединения в секунду через tun
(`curl --max-time 3 --interface singtun0 https://www.google.com/generate_204`), отказы / всего
за окно 5 с. По одному прогону на ячейку, живой домашний канал с фоновыми отказами ~1 %.

| окно | drop, сток | drop, +SPEC 138 | reject, сток | reject, +SPEC 138 |
|---|---|---|---|---|
| 0–4 с | 13/20 | 6/20 | 6/20 | 7/20 |
| 5–9 с | 13/20 | 6/20 | 6/20 | 5/20 |
| 10–14 с | 13/20 | 5/20 | 7/20 | 8/20 |
| 15–19 с | 6/20 | 3/20 | 6/20 | 3/20 |
| 20–24 с | 6/20 | 0/20 | 5/20 | 0/16 |
| 25–29 с | 7/20 | 0/20 | 7/20 | 0/20 |
| 30–34 с | 6/20 | 0/20 | — | — |
| 35–39 с | 5/20 | 0/20 | — | — |
| 40–44 с | 6/20 | 0/20 | — | — |

- **Сток:** ~1/3 новых соединений падает весь простой — совпадает с таблицей SPEC §1 для пула
  3 из 4. Первые 15 с прогона drop выше (13/20); прогон один, не разбиралось.
- **С патчем:** 0 отказов с ~20 с простоя до конца, в обоих режимах; первые ~15–20 с без
  изменений — окно обнаружения (SPEC §2.3).
- **RST не ускорил реакцию:** с `reject` она те же ~15–20 с, что с `drop`, вопреки ожиданию
  SPEC §2.3. Не разбиралось; возможные причины — проба h2mux-узла идёт по существующей сессии,
  тайминг прогонов.
- **Отказ тихий.** Лог стокового ядра за 45 с чёрной дыры: ни одной ошибки дайла по висящим
  новым соединениям, только 20× `connection download closed: http2: client connection lost`
  (существующие h2mux-сессии умирают на ~25–45 с).

## Открыто

- ~~Прогон на `lx` (не на бэкпорте) и статус D — решение владельца.~~ Закрыто 2026-10-23: PR #54 принят и влит в `lx` (`a212f936d`), `go vet` + тесты `protocol/group` под `LX_TAGS` и `-race` на tip `lx` зелёные.
- Почему RST не сокращает окно обнаружения на устройстве.

## §3. Самовосстановление по отказу соединения

Ветка `lx-spec138-rr-self-heal`, не влита. Контракт — [SPEC.md §3](SPEC.md#3-самовосстановление-по-отказу-соединения),
план и разбор решений — [PLAN.md](PLAN.md#3-самовосстановление-по-отказу-соединения).

### Что сделано по этапам

- **Этап 1 — отказ открытия.** Соединение, которое не открылось через узел пула, один раз
  повторяется через следующий живой слот; группа фоном пробует отказавший узел и соседа без записи
  в history. Узел молчит, сосед отвечает — history узла стирается, слот получает замену под
  `checking`; молчат оба — пробуется третий; молчат все — заморозка 15 с. Заморозка также по
  пробуждению и смене сети. Список соединений и демон показывают узел, выбранный для каждого
  соединения (ячейка в `InboundContext`, повторное «новое соединение» демона).
- **Этап 2 — первый байт, сдвиг, карантин.** Обёртка первого байта на соединениях группы;
  закрытие без байта после записи клиента — отказ. Отказ на живом узле сдвигает sticky-ключ,
  пройденный круг даёт пометку «проблема в адресе». Счётчики слотов и карантин по сравнению с
  медианой пула.
- **Этап 3 — UDP.** Ошибка открытия UDP — повтор и проверка, как в TCP; поток без ответа — повод
  для проверки; UDP не сдвигает ключи и не идёт в счётчики.
- **Этап 4 — переделка по стенду и ревью.** Учёт незавершённых соединений по узлам и повод
  «висит дольше 5 с» при выборе слота; отказ ждёт вердикта по своему узлу (первый байт соединения
  или ответ пробы, начатых после отказа), сокращение «history моложе 30 с — жив» для вердиктов
  убрано, контроль пробуется всегда; вердикт «мёртв» отменяет открытия в полёте и закрывает ждущие
  соединения; обёртка уходит с пути записи после первой записи клиента; замер задержки через группу
  выведен из самовосстановления. Что отвергнуто и почему — SPEC §3.15.
- **Этап 5 — наблюдаемость.** Поля `GetPool` (карантин и счётчики по слоту, сдвинутые и
  помеченные ключи и заморозка по группе), libbox `GetPoolState`, `sing-box api group pool`.
  Повторное «новое соединение» несёт приращение трафика тика.

Попутно, в той же ветке:

- **SPEC 054.** Отказ в подключении и сброс при открытии через прокси-узел в `least_test` и
  `failover` — «путь мёртв» (штраф и запасной дайл), у `direct` и endpoint — ответ сайта, как
  раньше ([SPEC 054](../054-URLTEST_PENALTY_FAILOVER/SPEC.md), [HISTORY](../054-URLTEST_PENALTY_FAILOVER/HISTORY.md)).
  Отметка конца тикового прогона SPEC 125 переведена на монотонные часы.
- **Рукопожатия прокси-узлов** socks, httpupgrade и shadowtls v3 ограничены 15 с и контекстом
  вызывающего — расширение хотфикса [SPEC 161](../161-HTTP_CONNECT_OUTAGE_LEAK/SPEC.md). Без этого
  зависший сервер узла держал открытие бесконечно, и самовосстановление не получало ни отказа, ни
  его времени.

### Коммиты

```
1e65d0bfa lx(daemon): SPEC 138 — the repeated NEW event carries the tick's traffic delta
3a9644edd lx(urltest): SPEC 138 — PoolSlot carries the self-heal state
4f152445a lx(urltest): SPEC 138 — GetPool reports quarantine, slot counters, shifted and marked keys and the freeze; sing-box api group pool
48a4a12c9 lx(handshake): hook the handshake bound into the socks, shadowtls and httpupgrade outbounds
8ec170786 lx(handshake): bound the socks, httpupgrade and shadowtls v3 outbound handshakes by 15s and the caller's context
76d0c841b ci(lx): run the trafficcontrol and urltest package tests
c8d291e28 lx(urltest): SPEC 054 — a refused or reset open through a proxy node is a dead path; SPEC 125 tick mark on the monotonic clock
6048daeee lx(urltest): SPEC 138 — hook the connection tracking, the client-finished mark and the delay-measurement tag into upstream files
d93e70439 lx(urltest): SPEC 138 — track unfinished connections per node, settle failures by the node's own verdict, keep the upload path off the first-byte wrapper
de50e24d4 lx(urltest): SPEC 138 — hook the UDP retry and watch into ListenPacket
0bc512d07 lx(urltest): SPEC 138 — round_robin self-heal covers UDP; a stalled connection only triggers a rate-limited node check
c82372e4d lx(urltest): SPEC 138 — hook the first-byte watch and the key shift into urltest.go
a0ab31414 lx(urltest): SPEC 138 — first byte as the success signal, sticky key shift and slot quarantine in round_robin
faf57cca4 lx(urltest): SPEC 138 — hook the self-heal and the per-connection pick into upstream files
86cf40bc3 lx(urltest): SPEC 138 — round_robin retries a failed dial through the next slot, probes the pair and replaces a dead node
beac708c0 docs(lx): SPEC 138 — round_robin self-heal on a failed connection (#76)
```

Код каждого этапа и вставки в апстримные файлы — отдельными коммитами.

### Апстримные файлы

По `git diff lx..HEAD --numstat`, только файлы без суффикса `_lx`:

| Файл | Строк | Задача |
|------|-------|--------|
| `protocol/group/urltest.go` | +36 −13 | SPEC 138 (и комментарий отметки SPEC 125) |
| `adapter/inbound.go` | +3 | SPEC 138 |
| `route/route.go` | +2 | SPEC 138 |
| `route/conn.go` | +1 | SPEC 138 |
| `common/trafficcontrol/tracker.go` | +5 −3 | SPEC 138 |
| `common/urltest/urltest.go` | +1 | SPEC 138 |
| `daemon/started_service.go` | +11 | SPEC 138 |
| `daemon/started_service.proto` | +17 | SPEC 138 |
| `daemon/started_service.pb.go` | +86 −11 | генерированный из `.proto` |
| `protocol/socks/outbound.go` | +10 | SPEC 161 (рукопожатие socks) |
| `transport/v2rayhttpupgrade/client.go` | +5 | SPEC 161 (обмен httpupgrade) |
| `protocol/shadowtls/outbound.go` | +1 | SPEC 161 (рукопожатие shadowtls v3) |
| `.github/workflows/lx-ci.yml` | +3 | `common/trafficcontrol` и `common/urltest` в прогоне под `-race` |

Остальное — lx-файлы (`*_lx.go`, `common/lxhandshake/`) и тесты.

### Как проверялось

- **Юнит-тесты.** 111 тестов `TestRR*` в `protocol/group` по критериям SPEC §4.2, плюс тесты
  `common/trafficcontrol`, `common/dialer`, `route`, `daemon`, `common/urltest`. Фикстуры —
  `newPoolTestGroup` SPEC 019 с узлами, которые отказывают в открытии, отвечают или молчат на
  пробу, «открываются» без сети и закрывают соединение без байта, висят на дозвоне.
- **`-race`** — обязательно: замена против тика, ячейка узла против чтения трекера, учёт
  незавершённых соединений против вердикта. lx-ci гоняет `protocol/group`, `common/trafficcontrol`,
  `common/urltest`, `daemon` под `-race`.
- **Мутационные прогоны.** Участок кода портится (например, снимается срок ожидания отказа), и
  тесты пакета должны на нём упасть.
- **Независимые ревью** кода этапов; найденное закреплено тестами
  `urltest_rr_heal_review_lx_test.go`, `urltest_rr_shift_review_lx_test.go`,
  `urltest_rr_stand_lx_test.go` (сценарии стенда в юнитах).
- **Прогон на дереве ветки** (go1.26.10 darwin/arm64, 2026-11-25): `go test -count=1
  -ldflags=-checklinkname=0` пакетов `protocol/group`, `common/trafficcontrol`,
  `common/lxhandshake`, `protocol/socks`, `transport/v2rayhttpupgrade`, `protocol/shadowtls`,
  `route` — ok; `-race` для `protocol/group`, `common/trafficcontrol`, `daemon` — ok;
  `protocol/group` с `LX_TAGS` — ok.

### Стенд на NanoPi

**Схема.** NanoPi (linux/arm64), всё на loopback. Пять серверов sing-box: `socks` (n1, n3, n5) и
`vless` (n2, n4), выход `direct`. Назначение — `uhttpd` на портах 19050–19069 и 19090–19099.
Клиент — sing-box с входом `mixed` и группой `round_robin` из пяти узлов: `pool: 3`,
`pool_tolerance: 0`, `interval: 10m`, проба — URL того же `uhttpd`. Варианты sticky: `["none"]`,
по умолчанию и `["dest_port"]`. Запросы — `curl` через вход клиента. Отказы: `kill -9` сервера
(узел мёртв), `kill -STOP` (узел принимает и молчит), правило `reject` по портам назначения на
сервере (сайт недоступен через узел). Сравнение — с `v1.14.4-lx.36` и между сборками ветки.

**Этап 1:**

- Один узел пула убит: 30 из 30 запросов прошли, замена в слоте через 0,13 с после отказа.
  `lx.36` на том же сценарии — 10 отказов из 30.
- Два узла пула убиты разом — оба заменены в ту же секунду.
- Убиты все пять («вся сеть») — ни один узел не вычеркнут.
- 100 параллельных запросов через убитый узел — одна проверка, три пробы.

**Этап 2:**

- Узел убит через 1 с после плановой пробы: замена через 0,84 с. Сборка этапа 2, где history
  моложе 30 с считалась ответом без пробы, — 32 с (отсюда отказ от сокращения, SPEC §3.15).
- Порт закрыт на одном узле — ключ сдвинулся на соседний слот.
- Порт закрыт на всех узлах пула — ключ прошёл круг и получил пометку «проблема в адресе».
- Socks-узел с закрытыми портами под потоком запросов по разным портам ушёл в карантин.

**Цена обёртки первого байта, первая версия** (запись через обёртку всё соединение). Пул из трёх
узлов одного типа, 512 МиБ загрузки и отдачи через `curl`, пять чередующихся прогонов; процессор
клиента — по `/proc/<pid>/stat`. Медианы, бинарь без обёртки → бинарь этапа 2:

| Узлы | Направление | Скорость, МБ/с | Процессор, с/ГБ |
|------|-------------|----------------|-----------------|
| socks | отдача | 133 → 109 (−18 %) | 4,02 → 7,11 (+77 %) |
| vless | отдача | 135 → 108 (−20 %) | 3,99 → 7,38 (+85 %) |
| socks | загрузка | 126 → 140 | 6,11 → 5,33 |
| vless | загрузка | 148 → 145 | 4,08 → 4,21 |

Загрузка в пределах разброса прогонов (у socks 92–147 МБ/с без обёртки). 2000 коротких запросов
подряд через socks: 65,1 → 65,9 с, процессор клиента 16,4 → 17,3 с. Обёртка переделана: после
первой записи клиента запись идёт мимо неё (этап 4).

**Повторный стенд** после этапа 4 (замер обёртки и сценарии с зависшим узлом, `kill -STOP`):
результат повторного стенда: <ожидается>.

### Открыто

- Полевая проверка по issue #76 на настоящем роутере с подпиской: пул, один узел убит вручную,
  `interval` 10 мин — со sticky и без.
- LxBox: переход на `GetPoolState`, показ карантина и сдвигов, обновление цепочки соединения в
  профайлере по повторному «новому соединению».
- Лаунчер: новые поля пула; старая ошибка в `core/backend_daemon_traffic.go` — событие обновления
  для соединения без записи у клиента не применяется.
- Тесты рукопожатий SPEC 161 (`common/lxhandshake`, `protocol/socks`,
  `transport/v2rayhttpupgrade`, `protocol/shadowtls`) в lx-ci не включены.
