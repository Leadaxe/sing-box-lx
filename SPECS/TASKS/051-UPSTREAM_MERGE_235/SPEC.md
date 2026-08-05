# SPEC: 051 — UPSTREAM_MERGE_235

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — плановая синхронизация с апстримом |
| Статус | O (open) — мерж выполнен (`f56680e0d`), дрейфа нет; блокер tailscale/wireguard-go снят (`577fe8789`), AAR собирается. Остаток: device-прогон (R4) |
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

## Результат мержа (`f56680e0d`)

Все 65 конфликтов разрешены; `merge-base == tip upstream/testing` — дрейфа
больше нет.

### Главный урок: опаснее конфликтов оказалось АВТОслияние

Три поломки git сделал **без конфликта**, молча склеив обе стороны. Именно они,
а не 65 явных конфликтов, едва не уехали в релиз:

| Файл | Что сломалось | Симптом |
|---|---|---|
| `adapter/outbound.go` | потерялся импорт `time`, нужный нашему `IdleSuspendable` | не компилируется |
| `dns/client_log.go` | `logRefreshedResponse` задвоился (наша версия с `transport` + апстримовая) | `redeclared in this block` |
| `transport/wireguard/endpoint.go` | после нашего nil-guard остался апстримовый `return e.tunDevice.Close()` | nil-паника в `TestPortAddressesSurviveTeardown` (SPEC 020 level 3) |

Вывод для следующего мержа: список конфликтных файлов — **не** список зон риска.
Проверять надо сборкой и тестами под всеми тегами, а не только глазами по
конфликтам.

### Вторая ловушка: «нет lx-маркера» ≠ «файл не наш»

27 файлов были отнесены к «безопасно брать апстримовые» по отсутствию маркеров.
Критерий сработал, но проверять его надо **по авторству коммитов**
(`git log --author` от merge-base), а не по маркерам: `adapter/outbound.go`
маркеров не имел, зато нёс наш интерфейс — и был сломан именно там.

### Адаптация к новым API апстрима

- **quic-go v0.61** убрал `http3.ParseCapsule` в пользу stateful
  `CapsuleParser`: `transport/masque/connectip` (SPEC 021) и его тест переведены
  на `NewCapsuleParser().Next()`. `CapsuleReader` реализует `quicvarint.Reader`,
  поэтому наши `parse*Capsule` не тронуты.
- **`allowedIPs.LookupFromPacket`** — апстрим зовёт метод, которого нет в базе
  нашего AWG2-форка `wireguard-go`. С nil-хуком (`peerByIPPacketFunc`) и nil-пакетом
  функция сводится к lookup по dst-адресу, что и делает наш `Lookup(ip []byte)`.
  Помечено `lx:begin awg-lookup`; **пересмотреть при перепрививке сабмодуля**.
  ⚠️ Fast-forward форка на `sagernet/dev` **невозможен**: там ноль наших
  lx-коммитов, обновление уничтожило бы всю AWG2-обфускацию и SPEC 041. Нужен
  re-graft по существующей схеме (см. [[wg-1.14-migration-is-submodule-rebase]]).

### Проверки R1–R3

- **R1** ✅ все 94 файла с lx-маркерами сохранили число блоков; 55 lx-only файлов
  на месте; патчи HOTFIXES 028/029/030/039/045/046/047/050 найдены поимённо.
- **R2** ✅ `*.pb.go` регенерированы через `make -f Makefile.lx lx-proto`, не
  слиты руками; все 8 наших RPC присутствуют. ⚠️ `lx-proto` прогоняет
  `gofumpt -w .`, который пачкает три сабмодуля — откатить перед коммитом.
- **R3** ✅ `go build ./...`, `go test ./...` без тегов, `lx-check` на полном
  наборе тегов, тесты 050 + WireGuard под lx-тегами — зелёные. Сабмодули не
  сдвинуты.
- **R4** ⏳ device-прогон — остаток до релизного тега.

## ✅ Блокер снят (`577fe8789`): tailscale 1.102 требовал API, которого не было в базе AWG2-форка

CI после мержа красный (`lx-ci` run 30998205737, шаги lint и build-check):

```
tailscale@v1.102.1/wgengine/wgcfg/device.go:28: undefined: device.PeerLookupFunc
tailscale@v1.102.1/wgengine/wgcfg/device.go:29: undefined: device.NewPeerConfig
```

**Цепочка** (воспроизведена локально, не по догадке):
`experimental/libbox/native_shell_session.go` → `protocol/tailscale/tailssh` →
`tailscale/wgengine/wgcfg` → `wireguard-go/device.PeerLookupFunc`.

**Почему до мержа собиралось:** старый пин `tailscale v1.92.4` этих символов не
требовал — проверено грепом по обеим версиям модуля. Мерж поднял его до
`v1.102.1-sing-box-1.14-mod.1`.

**Почему нельзя обойти тегами:** `protocol/tailscale/*` и `tailssh` гейтятся по
`with_gvisor` (апстримовый дизайн, был и до мержа), а `with_gvisor` мы включаем
всегда. `with_tailscale` тут не помогает — `include/tailscale.go` под ним, но
`native_shell_session.go` тянет `tailssh` напрямую, минуя реестр.

**Почему нельзя обойти адаптацией, как `LookupFromPacket`:** тот вызов был в
НАШЕМ файле (`transport/wireguard/endpoint.go`) — одна строка под
`lx:begin awg-lookup`. Здесь символы требует чужой модуль в своём коде; наш
`replace` на форк-сабмодуль перекрывает апстримовый wireguard-go для всей сборки.

**Что нужно:** перенести на базу форка апстримовый коммит wireguard-go
`f69b247 device: further add, revise API for on-demand configuration of peers`
(5 файлов, ~220 строк: `allowedips.go`, `device.go`, `peer.go`, `receive.go`,
`send.go`). Пробный `cherry-pick -n` дал конфликты в трёх файлах
(`allowedips.go`, `device.go`, `peer.go`) — это горячий путь WireGuard, поверх
которого лежит наша AWG2-обфускация, поэтому разрешать его без AWG-стенда
нельзя. Пробный прогон откачен, сабмодуль чист.

⚠️ Fast-forward форка на `sagernet/dev` **невозможен**: там ноль наших
lx-коммитов, обновление снесло бы всю AWG2-обфускацию и SPEC 041.

**Статус мержа:** ядро (`go build ./...`, `go test ./...`, `lx-check`) собиралось
и проходило — блокер бил только по пути `libbox` → `tailssh`, то есть по
AAR-сборке.

### Как закрыт

В форк-сабмодуль (`Leadaxe/wireguard-go-awg2-lx`, ветка `lx-awg2-v005`,
`1255464` → `ce20e73`) перенесены три апстримовых коммита:

| Коммит | Что даёт |
|---|---|
| `e924a91` | базовый API on-demand-конфигурации пиров |
| `f69b247` | `PeerLookupFunc`, `NewPeerConfig`, `SetPeerLookupFunc`, `AllowedIPs.LookupFromPacket` |
| `7c3a736` | `PeerSessionState`, `SetSessionStateFunc` |

Полный re-graft **не понадобился** — хватило трёх cherry-pick'ов.

**Конфликты во всех трёх — одной природы:** невзятый нами коммит `70b09a6`
переименовал `AllowedIPs.mutex` → `mu` и `IPv4`/`IPv6` → `ipv4`/`ipv6`. Взята их
логика целиком (`insertLocked`, `lookupLocked`, `setPeerPrefixes`,
`peerByIPPacketFunc`), имена полей оставлены **наши**: тянуть переименование
через всю AWG-дельту дорого и без выигрыша.

**Единственное содержательное место — `device/timers.go`**, ветка окончательного
провала цикла рукопожатий: там живёт наш SPEC 041 (rebind по give-up).
Сохранены оба вызова, апстримовый `noteSessionHandshakeStopped()` идёт **перед**
нашим `handleHandshakeGiveUp()` — потребитель должен увидеть «handshake stopped»
до пересоздания сокета, а не после.

**Семантика для нас нейтральна:** `lookupFunc` ставится только через
`SetPeerLookupFunc`, который зовёт исключительно tailscale-движок (у нас не
активен). При nil-хуке `LookupPeer` и `LookupFromPacket` ведут себя ровно как
раньше.

Здесь же снят временный shim `lx:begin awg-lookup` в
`transport/wireguard/endpoint.go` — вызов вернулся к апстримовой форме, дельта
форка уменьшилась.

**Проверено:** AWG-обфускация цела (8 тестов, включая reserved-vs-magic SPEC 026
и transport-padding SPEC 025), `go test ./device/` в сабмодуле зелёный; в ядре —
`libbox` собирается, оба шага `vet` из CI проходят, `go test ./...` без тегов и
тесты 050/WireGuard/route под полным lx-набором зелёные. Сабмодуль запушен **до**
суперпроекта (иначе гитлинк уехал бы на несуществующий коммит).
