# SPEC: 051 — UPSTREAM_MERGE_235

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — плановая синхронизация с апстримом |
| Статус | N (new) — разведка проведена, мерж не начат |
| Ветка | `lx` |
| Base | `43c3d89f1` (v1.14.0-lx.20-rc.4) |
| Страховка | ветка `backup-pre-merge-235` на `d1c2f3e21` |

## Why

На момент среза `v1.14.0-lx.20-rc.4` дрейф от `upstream/testing` — **235 коммитов**
(merge-base `c9e81856e` против tip `d1e283be4`). Правило форка требует мержить
апстрим перед релизным тегом; здесь это сознательно отложено, потому что пробный
прогон показал масштаб, несовместимый с «правкой перед тегом».

Отложено — не значит отменено: чем дольше копится дрейф, тем дороже каждый
следующий мерж, и тем выше риск, что наш патч тихо разойдётся с новой логикой
апстрима.

## Фактура разведки (пробный `git merge --no-commit`, откачен)

**65 конфликтных файлов.** Категории, по убыванию риска:

| Категория | Файлы | Чем опасно |
|---|---|---|
| Инфраструктура сборки | `go.mod`, `go.sum`, `.gitmodules`, подмодули `clients/android`, `clients/apple`, `clients/desktop` | Ошибка = не собирается ничего |
| Генерённые protobuf | `daemon/*.pb.go` (4), `experimental/boxdd/*.pb.go` (2), `daemon/started_service.proto` | Правятся **не руками**, а `make -f Makefile.lx lx-proto`; ручное слияние молча разойдётся с контрактом SPEC 014/015 |
| Наши SPEC-зоны | `route/dns.go` (046), `route/router.go`, `route/route.go`, `box.go` (030), `experimental/libbox/*` (017/018/037/039), `transport/wireguard/*` + `protocol/wireguard/endpoint.go` (041), `protocol/group/urltest.go` (019/020/050), `option/*` | Здесь живут патчи с условиями снятия — каждый сверять индивидуально по реестру HOTFIXES |
| Прочее | `protocol/tailscale/*`, `protocol/hysteria2/*`, `dns/*`, `service/api/server.go`, доки, схема | Обычное слияние |

**Темы дрейфа** (по префиксам сообщений): platform 14, tailscale 13, dns 10,
boxdd 7, release 6, daemon 4, wg/wireguard 3, windivert 2.

**Зона 050 чиста.** Проверено через merge-base (не через `git diff HEAD upstream` —
он показывает нашу дельту наоборот, см. [[upstream-drift-check-use-merge-base]]):

- `transport/v2rayxhttp/conn.go` — 0 коммитов апстрима;
- `protocol/vless/outbound.go`, `protocol/vless/lx_encryption.go` — 0;
- `protocol/group/urltest.go` — 4 коммита, но все про другое (`InterfaceUpdated`,
  переименование `NewConnectionEx`→`NewConnection`) и все уже влиты к нам.
  `URLTestGroup.Close`, `testNodes`, `batch`, `g.ctx` апстрим не трогал.

Отдельно сверены два коммита с названиями, задевающими нашу тему:
`eaa738d08 Fix inconsistent URLTest results` (5 строк в `adapter/outbound.go`) и
`90bc53e64 daemon: Improve URLTest` (правит `daemon`) — оба про другое,
апстримный `Close()` по-прежнему не отменяет идущий прогон.

## Требования

**R1. Ни один наш патч не теряется молча.** Для каждой записи реестра
[HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) после мержа проверено: патч
на месте либо снят осознанно (апстрим починил то же самое) с отметкой в реестре.

**R2. Protobuf регенерирован, а не слит руками.** `make -f Makefile.lx lx-proto`;
диффы вне SPEC-014/015-контракта отсутствуют.

**R3. Сборка на полном наборе тегов.** `make -f Makefile.lx lx-check` зелёный,
`go test ./...` без тегов зелёный, AAR собирается.

**R4. Device-прогон.** После мержа — проверка на устройстве: туннель
поднимается, DNS ходит, URL-тест меряет, WG/AWG-узлы живы. Мерж такого объёма
без живой проверки не считается завершённым.

## Границы

- Не тянуть в этот мерж новые фичи форка — только синхронизация.
- Подмодули (`gvisor`, `sing-tun`, `wireguard-go`) — отдельная сверка: встречный
  бамп на мерже уводит `replace` и **молча снимает** наш патч (см. запись 048 в
  реестре HOTFIXES).
- Порядок: мерж → сборка → gofmt/lx-check → device → и только потом тег.
