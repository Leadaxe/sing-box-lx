# SPEC: 155 — UPSTREAM_MIGRATION_1_15

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — перевод базы форка с линии 1.14 (`upstream/stable`) на линию 1.15 (`upstream/testing`) |
| Статус | I (implemented) — 2026-11-05: `lx-1.15` содержит `upstream/testing` (alpha.13 + 2), сборка и тесты зелёные, живой прогон на NanoPi, пререлиз `v1.15.0-lx.1-rc.1`; в `lx` не вливается до беты апстрима (§5); остаются хвосты §5a и прогон на Android |
| Ветка | `lx-1.15` от `lx` = `ec4b85566` (v1.14.4-lx.31), worktree `../sing-box-lx-1.15` с собственными копиями четырёх форк-сабмодулей (без симлинков) |
| Срез апстрима | `upstream/testing` = `9f1cd1ef7` (v1.15.0-alpha.13 + 2, 2026-11-03). Ветка перезаливается force-push'ем: хеши ниже верны для этого среза |
| Связано | [107](../107-MASQUE_UPSTREAM_1_15_COMPARISON/SPEC.md), [110](../110-MASQUE_ON_UPSTREAM_TRANSPORT/SPEC.md) (MASQUE), [048](../048-GVISOR_HANDSHAKE_NIL_CRASH/SPEC.md) (патч gvisor), [064](../064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md) (снимается), [117](../117-UPSTREAM_SYNC_SCOPED_LIFECYCLE/SPEC.md) (scope уже у нас), память: миграция 1.14 шла в ветке `lx-1.14` |

Решение владельца 2026-11-05: «сделаем ветку 1.15 и начнём в ней готовиться». Апстрим на alpha.13,
беты нет; работа идёт в отдельной ветке и в `lx` не вливается до решения о релизной линии.

## 1. Что приносит 1.15

`upstream/stable..upstream/testing` = 44 коммита, 366 файлов, +20,8k/−6,0k. По весу для нас:

| Коммит | Суть | Что задевает у нас |
|---|---|---|
| `2a32d0a5d` Add go TUN stack, `3c5862c6b` Remove dependency on gVisor | Собственный TCP/IP-стек в `sing-tun` (`tun.Go`); gVisor убран из WG, Tailscale, OpenVPN, OpenConnect; тег `with_gvisor` исчез; опция TUN `stack` устарела (удаление в 1.17) | `LX_TAGS`, `lx-ci.yml`, `build_libbox`, форк-сабмодуль `gvisor` (патч 048), ~30 файлов `protocol/tailscale` с constraint `with_gvisor`, 28 lx-файлов в `transport/wireguard` поверх переписанного стека, `masque`-outbound на gVisor-стеке |
| `3ddbc0450` Add MASQUE support, `c14e9c15f` Rewrite HTTP proxy protocol | Endpoint'ы `masque-client`/`masque-server`, общий `transport/http`, CONNECT-UDP | Общие пакеты `protocol/masque`, `transport/masque` с нашим outbound; по символам пакетов столкновений нет (проверено по объявлениям верхнего уровня) — собирается рядом. Перевод на их транспорт — [110](../110-MASQUE_ON_UPSTREAM_TRANSPORT/SPEC.md) |
| `6231b40d7` Fix outbound group resolution and interruption for nested groups | `Selector.NewConnection`/`NewPacketConnection` удалены, `AttachConnection`, `flow_tracker` (позже заменён `adapter/host.go` в `1a55e9ed2`) | Хотфикс 064 и `SingPacketConn` снимаются; наша дельта `protocol/group` (019/054/020/073) ложится на переписанный `RealTag`/`Selected(network)` |
| `298e7af3e` Add tailcat support | Tailcat inbound/outbound, DERP `verify_client_*` | Пакет `protocol/tailscale` — наши 8 lx-файлов (status, toggle, control_https, headroom) |
| `b7eb47b30`, `7350365e7`, `32479aa1b`, `acc4693f7` auto-redirect на Android, bridge | — | Напрямую не задевает; меняет `route/`, `protocol/tun` |
| `bb2f9f454` Rework forward NAT, `a9077d3f7` backpressure, `1a55e9ed2` host flows, `9f1cd1ef7` close UDP on address change | Инфраструктура endpoint'ов: `UpstreamPort`, `WaitWriteInboundBuffers`, `adapter/host.go` | `protocol/wireguard/endpoint.go`, наши 020/097/106/112 |
| `da47aa948` certificate SHA-256 pinning, `b52cbde10` dns_server_address, `4ddc0dafe` Prefixable, `1ad040e34` schema validation | Опции и схема | `option/tls.go`, `option/wireguard.go` (конфликт), `sing-box schema` (SPEC 030 lx-типы) |
| `20fd7be5c` anytls в свою библиотеку, `d541b8cb8` | Зависимость `sing-anytls` | `protocol/anytls/outbound.go` (lx-маркер) |

Остальное (process search, cache file, mmap rule-sets, systemd, Xcode, docs) — без нашей дельты.

Старые 1.15-коммиты из прежних разборов (`Close idle connections`, `power report`, `Start(stage, scope)`,
`Fix UDP fragmentation`, `Fix interrupt group`) уже у нас через stable (109, 117, 140, 154).

## 2. Зависимости и сабмодули

Пины `testing` отличаются от наших на минуты — это те же снапшоты, снятые для другой ветки:

| Модуль | Наш пин (stable) | `testing` | Разница |
|---|---|---|---|
| `wireguard-go` | `e4d85ef2ed38` | `fdf0ad046586` | +2 коммита (`device: Add blocking per-peer packet injection`, `Apply pre-shared keys to lazily created peers`); нет в форке `lx-awg2-v007` (17 наших поверх merge-base) → merge в форк |
| `sing-tun` | `620991cbdec9` | `7ac0256fa09d` | +8 коммитов (routed port, forward NAT, blocking inbound write, flow judge без блокировки); нет в форке (13 наших) → merge в форк |
| `gvisor` | `20260727.0-sing-box-mod.1` | тот же, только `// indirect` | решение §5.1 |
| `utls` | v1.8.7 | v1.8.7 | без изменений |
| `quic-go` | mod.12 | mod.12 | без изменений |
| `tailscale` | `79c1732442a9` | `670967df7dbf` | не сабмодуль; в форке sagernet переписан netstack под `tun.Go` |
| `sing` | `5dc452e537bf` | `03a6cce64bf7` | рядом |
| `go.mod` `go` | 1.26.0 | **1.25.5** | у `testing` ниже, чем у `stable` — брать нашу; тулчейн 1.26.10 совпадает |

Теги `testing` по умолчанию: `with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,with_ocm,with_cloudflared,with_usbip,with_openvpn,with_openconnect,badlinkname,tfogo_checklinkname0` — без `with_gvisor`, с новыми `with_acme`, `with_ccm`, `with_ocm`, `with_cloudflared`.

## 3. Пробный мерж (2026-11-05, `--no-commit`, откачен)

348 файлов, +20,1k/−4,7k. **18 конфликтов**: 15 по содержимому, 3 modify/delete.

| Файл | Хунков / строк | Ожидаемое разрешение |
|---|---|---|
| `transport/wireguard/endpoint.go` | 8 / 221 | Самый дорогой: наши 020/041/072/097/106/112 поверх `tun.Go`-стека и `UpstreamPort`/host flows. Руками, с чтением обеих сторон |
| `experimental/cachefile/cache.go` | 3 / 74 | Наш `// lx` (147 перехват cache.db) против буферизованной записи апстрима |
| `cmd/internal/build_libbox/main.go` | 1 / 67 | Наш набор тегов против удаления `with_gvisor` и новых тегов |
| `route/reference.go` | 1 / 34 | Наш `poolReferences` (109) против `OnDemand`/host |
| `option/wireguard.go` | 2 / 32 | Наши AWG-поля против `Prefixable`/валидации |
| `dns/transport/fakeip/store.go` | 1 / 31 | lx-маркер против буферизации |
| `protocol/tailscale/endpoint.go` | 3 / 31 | Наши status/toggle против tailcat и снятия gVisor |
| `experimental/deprecated/constants.go` | 1 / 27 | Наши deprecated-записи рядом с `stack` |
| `protocol/wireguard/endpoint.go` | 3 / 23 | 020/106 против `UpstreamPort`, scope |
| `protocol/http/outbound.go` | 1 / 20 | lx-маркер против переписанного HTTP-прокси |
| `experimental/cachefile/fakeip.go` | 1 / 19 | как `store.go` |
| `option/tailscale.go` | 1 / 8 | наши ключи против tailcat |
| `service/oomkiller/service.go` | 1 / 6 | |
| `transport/v2raygrpclite/server.go` | 1 / 5 | |
| `go.sum` | — | сторона апстрима + `go mod tidy`, `replace` на месте |
| `transport/wireguard/device_stack_gonet.go` | modify/delete | удалён апстримом; наша правка — один маркер; удалить, перенести смысл в новый стек |
| `transport/openvpn/device_stack.go`, `transport/openconnect/device_stack.go` | modify/delete | то же |

Автослияние прошло ещё в 24 файлах с нашими маркерами (`common/dialer/default.go`, `route/route.go`,
`route/network.go`, `daemon/*`, `protocol/tun/inbound.go`, `transport/wireguard/port.go` …) — проверять
сборкой и сверкой «+/−»-строк по методу 109 §3 п. 3, конфликтов там не будет.

Сборка после разрешения конфликтов **не пройдёт** без работ §4: `with_gvisor`-файлы, стек `masque`,
`SingPacketConn`.

## 4. Работы (порядок)

1. **Сабмодули первыми** (раннбук §1): merge `fdf0ad046586` в `wireguard-go` (`lx-awg2-v007`), merge
   `7ac0256fa09d` в `sing-tun` (`lx`), сверить дельту форков (AWG, 040), запушить до ядра.
2. **Мерж** `upstream/testing` в `lx-1.15`, 18 конфликтов по §3. `go.mod`: `go 1.26.0` наш.
3. **gVisor**: убрать `with_gvisor` из `LX_TAGS`, `lx-ci.yml`, `build_libbox`; снять constraint
   `with_gvisor` с наших файлов в `protocol/tailscale` и `lx-test`; `include/*_stub.go`. Судьба
   сабмодуля — §5.1.
4. **WireGuard-стек**: перенести 28 lx-файлов `transport/wireguard` на `tun.Go` — что из 020/041/072/097
   (lazy build, suspend, quiesce, connect deadline, peer session) опирается на gVisor-типы, выяснится
   сборкой. SPEC 020 замеры памяти повторить (были на gVisor).
5. **Группы**: снять 064 и `SingPacketConn` (тест 084 остаётся как страж), переложить 019/054/020/073
   на `Selected(network)`/`RealTag(detour, network)`.
6. **MASQUE**: наш outbound (`protocol/masque/outbound.go`, `transport/masque/{masque,client_h2,profile,request}.go`,
   `connectip/`) удаляется; WARP-слой по [110](../110-MASQUE_ON_UPSTREAM_TRANSPORT/SPEC.md) поверх апстримного
   `masque-client`, начиная с этапов Э0/Э1 (решение 5.4).
7. **Теги LxBox/лаунчера**: пин `kCoreBuildTagsPin` без `with_gvisor` — задача в их репозиториях.
8. Сборка без тегов, с `LX_TAGS`, тесты затронутых пакетов, стенды `lx-test`, `lx-check`; кросс под
   android/linux/windows; dry run `lx-release.yml`.
9. Пока апстрим в альфе — пререлизы `-alpha.N`/`-rc.N` с ветки `lx-1.15`; промоут в `lx` — решение §5.3.

## 5. Решения владельца (2026-11-05)

| # | Вопрос | Решение |
|---|---|---|
| 5.1 | Форк-сабмодуль `gvisor` (патч 048) | **Снимаем вместе с `with_gvisor`** — идём за апстримом. Дефект 048 (`handleConnecting` на занулённом `h`) проверить в `tun.Go` **по коду**, без стенда; результат — строкой в 048 |
| 5.2 | Релизная линия | **Только пререлизы с `lx-1.15`; в `lx` не вливаем.** `lx` остаётся на stable 1.14.x |
| 5.3 | Момент промоута | **Не раньше беты 1.15** |
| 5.4 | MASQUE | **Сразу на апстримном `masque-client` с нашим тонким слоем — [110](../110-MASQUE_ON_UPSTREAM_TRANSPORT/SPEC.md)**, свой outbound на новый стек не переносим. Перед швами перепроверить, не появилось ли у апстрима своё под WARP (на `9f1cd1ef7` — нет: токен константой, `ADDRESS_REQUEST` безусловный, профилей нет) |
| 5.5 | Новые теги апстрима `with_acme`, `with_ccm`, `with_ocm`, `with_cloudflared`, `with_usbip` | **Брать в `LX_TAGS`** |
| 5.6 | `go.mod` `go` директива | **Свежая (наша 1.26.0)**, не 1.25.5 из `testing` |
| 5.7 | Порядок | Сначала отказ от gVisor (§4.3), потом остальное — иначе сборка не проходит |

## 5a. Ход работ

- 2026-11-05: §4.1 выполнен. Форки: `wireguard-go` ветка `lx-1.15` = `c1d6138` (merge `fdf0ad046586`; конфликт `device/send.go` — AWG-раскладка `outboundLayout` перенесена внутрь апстримного `newInputElement`, `InputPacket` у апстрима удалён, четыре lx-теста переведены на хелпер над `InputPackets`); `sing-tun` ветка `lx-1.15` = `02095c3` (merge `7ac0256fa09d`, 32 коммита; конфликт `stack_system.go` — поля 040 и апстримные `dispatchStage`/`processAccess` вместе). Тесты device/conn/tun и selfheal зелёные; `TestGoKernel*` в sing-tun падают «operation not permitted» и на чистом пине — нужен TUN, не регресс. Обе ветки запушены в форк-remote'ы; гитлинки в `lx-1.15` обновлены.

- 2026-11-05 (ночь): §4.2–4.6 выполнены первым проходом, merge-коммит в `lx-1.15` (сообщение коммита —
  полный перечень). Сборка без тегов, с `LX_TAGS` (+`with_lx_idle_suspend`), кросс linux/amd64,
  linux/arm64, android/arm64; `lx-check` на 9 конфигах; тесты всех пакетов и стенды `lx-test`
  (chain, conntest, initerr, startclose, zombie) зелёные. Снято: 071 (свой штамп очерёдности
  pause-событий — апстрим применяет событие в горутине `go updateNetworkPause()` сам), 048 (gVisor
  ушёл; проверка `tun.Go` по коду — в SPEC 048). Хотфикс 064 этой миграцией не снимался: его точка
  вызова в `selector.go` ушла ещё с синком v1.14.4 (группы разворачивает роутер), а обёртка
  `SingPacketConn` осталась неиспользуемым кодом с тестами — убрать отдельно, в `lx` тоже. Открытые хвосты первого прохода:
  - `Endpoint.ActiveTCPFlows()` возвращает 0 — у `tun.Go` нет счётчика established-потоков;
    idle-тик SPEC 020 работает по дельте байтов. Вариант: lx-файл в форке sing-tun со счётчиком
    по движкам.
  - `on_demand` (апстрим: `SetKeepIdleConnections` → `SetIdle`) и `lx.wg.idle_suspend` — два
    механизма усыпления на одном устройстве; совместное включение не валидируется.
  - `lx.masque.idle_timeout` парсится и ни на что не влияет; доки masque описывают удалённый
    outbound — переписать под SPEC 110.
  - В `TestDevicePauseLeavesSocketOpen` первый bind порта в загруженном прогоне превышал 2 с —
    окно ожидания расширено до 10 с; причина не исследована.
  - AAR: `with_usbip` по-прежнему не включён (решение 5.5 касалось `LX_TAGS`).

- 2026-11-05 (утро): MASQUE переведён на апстримный `masque-client` с WARP-слоем
  ([110 §12](../110-MASQUE_ON_UPSTREAM_TRANSPORT/SPEC.md)); старый тип `masque` отдаёт
  поясняющую ошибку. Живой прогон на NanoPi (arm64, OpenWrt): WARP MASQUE h3/h2, WG и AWG через
  `detour` на `masque-client`, hysteria2 и vmess+ws+tls из публичной подписки — трафик идёт.
  Прогон нашёл дефект порта: ленивый WG-endpoint не получал `Initialize` и падал на первом dial —
  исправлено, тест проходит все стадии старта. Контрольные замеры на ядре 1.14 на том же хосте:
  прямой WG к WARP (UDP 2408) не проходит на обоих ядрах — сеть стенда; 93 vless-узла публичного
  списка мертвы на обоих ядрах. Не проверено вживую: Tailscale, OpenVPN, OpenConnect (нет стенда),
  idle-suspend (в сценарии узел был достижим и не усыплялся), TUN-inbound, Android.

- 2026-11-05: пререлиз **`v1.15.0-lx.1-rc.1`** срезан с `lx-1.15` (`52e257f44`) после зелёного dry run
  `lx-release.yml` (13 сборок, включая AAR и Win7). Нумерация `lx.N` у линии 1.15 своя, чтобы не
  занимать номера линии 1.14 — решение исполнителя, владелец может переименовать. Полигон для
  приёмки: NanoPi, `/root/lx15-test` (`README.txt`, `./run.sh <сценарий>`).

## 6. Критерии приёмки

1. `lx-1.15` содержит `upstream/testing` целиком (merge-base == tip), форк-сабмодули на пинах `testing`
   плюс наша дельта.
2. Сборка без тегов и с `LX_TAGS` (без `with_gvisor`), тесты затронутых пакетов, стенды, `lx-check`.
3. Дельта каждого автослитого lx-файла совпадает до/после (метод 109).
4. Прогон на устройстве: WG/AWG, idle-suspend, masque, Tailscale — на новом стеке.
5. Пререлиз с ветки собран `lx-release.yml`.
