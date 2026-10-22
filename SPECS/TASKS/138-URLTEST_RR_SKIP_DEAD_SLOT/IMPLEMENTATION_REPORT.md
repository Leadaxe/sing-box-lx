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
