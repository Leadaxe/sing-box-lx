# SPEC: 109 — UPSTREAM_SYNC_1_14_2_PLUS_15

**Фича:** [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | R (sync) — мерж `upstream/stable` (v1.14.2 + 15 коммитов, без нового тега) в `lx` по раннбуку |
| Статус | I (implemented) — 2026-09-28: мерж `741d5ffa1` в дереве, дрейф 0; сборка без тегов, с `with_lx_command` и с полным `LX_TAGS`+`with_lx_idle_suspend`, тесты затронутых пакетов, стенды `lx-test`, `lx-check` (9 конфигов); не выпущено, на устройстве не прогонялось |
| Ветка | `lx` |
| База | до: `4a2eb44ee` (v1.14.2-lx.7, merge-base `af6e64c3b` = v1.14.2); после: `upstream/stable` = `a781ae655` = v1.14.2 + 15 |
| Связано | [102](../102-UPSTREAM_SYNC_1_14_2/SPEC.md) (предыдущий синк), [084](../084-INTERRUPT_GROUP_ABBA_DEADLOCK/SPEC.md) (снят), [028](../028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) (семантика флага изменилась), [019](../019-URLTEST_MODE_STICKY/SPEC.md) (пул `round_robin`), [064](../064-SELECTOR_INTERRUPT_DEAD_ON_INBOUND/SPEC.md) |

Решение владельца 2026-09-28, после среза lx.7: «делай синк 15 коммитов stable и всех вложенных
пакетов». Синк откладывался в lx.5, lx.6 и lx.7.

## 1. Что принёс апстрим

| Коммит | Суть | Наши файлы |
|---|---|---|
| `a8fbf4b07`, `7086db1ad` | Управление простаивающими соединениями. Новая служба `route.ReferenceManager` обходит правила, DNS-правила, endpoint'ы, inbound'ы, службы и выбранные узлы групп; outbound'ам и DNS-транспортам, на которые никто не ссылается, ставит `SetKeepIdleConnections(false)`, DNS-транспорты без этого интерфейса сбрасывает. Интерфейсы `adapter.IdleConnectionKeeper`, `adapter.Referrer`, `adapter.V2RayMultiplexClientTransport`; `Box.CloseIdleConnections()` | `box.go`, `adapter/outbound/manager.go`, `daemon/instance.go`, `protocol/group/*`, `protocol/{vless,vmess,trojan,tuic,anytls,tailscale}`, `dns/transport_adapter.go` |
| `07cc8fd1b`, `a781ae655` | libbox: `Pause` на Android и iOS закрывает простаивающие соединения; `Wake` снимает паузу только на Android; на iOS пауза снимается по состоянию экрана через новый `WakeNow`. Вне Android/iOS `Pause` ничего не делает. Новые методы `WakeNow`, `RecordScreenState`, `RecordLockState` | `experimental/libbox/command_server.go` |
| `9f081758e` | `UDPFragmentDefault = true` теперь ставит сокет-опцию явно (`IP_PMTUDISC_DONT`, на darwin `IP_DONTFRAG = 0`); раньше означал «не ставить ничего». hysteria, hysteria2 и tuic флаг больше не выставляют. `sing` 5f9aad7def20 | `common/dialer/default.go`, `common/listener/listener_udp.go` — дельты нет |
| `0ed951aa0` | `interrupt.Group`: нижележащий `Close` вынесен из-под мьютекса | `common/interrupt/*` — тот же фикс, что наш 084 |
| `cbab5e92d` | GSO на TUN включается по `PreMatchFlow`, а не по списку сетей endpoint'а | `protocol/tun/inbound.go` — дельты нет |
| `4537a1ac0` | Системный TUN openvpn/openconnect не останавливает чтение на ошибке записи | дельты нет |
| `5bc47e648` | Локальный DNS учитывает глобальные серверы systemd-resolved | дельты нет |
| `7a9b35fb6`, `ad7d13845`, `8c525aa56`, `53165d3c2`, `ae303df43` | Power-отчёты, метаданные платформы, дамп heap и горутин, лог сетевого пути | `daemon/started_service.go`, `experimental/libbox/{log,report}.go`, `transport/wireguard/endpoint.go` |
| `a23d5f9ef` | Документация | — |

Модули: `sing` → 5f9aad7def20, `sing-mux` → v0.3.9-0.20260927144857, `sing-quic` → 8601a428f4db,
`sing-snell` → bc5a12ac736f.

## 2. Сабмодули

Пины `upstream/stable` для всех четырёх форков не менялись: `wireguard-go` v0.0.7, `sing-tun`
ddaa4ca25e3b, `gvisor` 20260727.0-sing-box-mod.1, `utls` v1.8.7. Наши ветки пины содержат, дельта
`sing-tun` — два файла SPEC 040, `utls` — три коммита поверх тега. Работы в сабмодулях нет.
Новые коммиты в `sagernet/dev` форков `wireguard-go` и `sing-tun` относятся к линии 1.15 и не
берутся.

## 3. Ритуал

1. **Ядро**: `git merge upstream/stable` → 4 конфликта.

   | Файл | Разрешение |
   |---|---|
   | `common/interrupt/group.go` | форма апстрима; вместе с `conn.go` файл побайтно равен апстримному. `NewSingPacketConn` и `SingPacketConn` (SPEC 064) вынесены в `sing_packet_conn_lx.go` |
   | `dns/transport_adapter.go` | оба поля: `references` апстрима и наш `outboundTag` (SPEC 018) |
   | `protocol/group/urltest.go` | обе стороны: `References` и `NotifyUpdated` апстрима; наши `Pool`, `Mode`, выбор с учётом штрафов (SPEC 054), сброс достижимости (SPEC 020) |
   | `go.sum` | сторона апстрима + `go mod tidy` (ушли 8 строк модулей под `replace`) |

2. **Сверка `go list -m`**: четыре модуля резолвятся в `./submodules/<name>`.
3. **Сверка автослияния**: для 19 файлов, которые правил апстрим и в которых есть наша дельта,
   набор строк нашей дельты до мержа и после совпал (сравнение `git diff` к базе и к `stable` по
   отсортированным «+/−» строкам). Отличие одно — `urltest.go`, правки из п. 1 и §4.
   Счётчик `lx`-маркеров по файлам сошёлся.
4. **Сборка и тесты**: `go build ./...`; `go build -tags with_lx_command ./...`; сборка с полным
   `LX_TAGS`+`with_lx_idle_suspend`; `go test` без тегов (`.`, `route`, `common/{interrupt,dialer,httpclient}`,
   `adapter`, `dns`, `option`, `protocol/{group,wireguard}`, `transport/wireguard`, `daemon`) и с
   полными тегами (те же плюс `protocol/{masque,chain,vless}`, `transport/{masque,v2rayxhttp}`,
   `experimental/libbox`, `common/tls`, `lxd`); стенды `lx-test/{chain,initerr,startclose,zombie}`;
   `make -f Makefile.lx lx-check`; `gofmt -l` пуст.
5. **§2a**: Go 1.26.8, cronet, `upstream.version` = 1.14.2 совпадают со stable; `.github` и
   скрипты тулчейна апстрим не менял. `require` равен stable. Dry run не требуется.

## 4. Адаптация нашей дельты

| Что | Почему | Где |
|---|---|---|
| Группа `round_robin` отдаёт в `References()` все занятые слоты пула | Апстримный `References()` называет только выбранный узел. В `round_robin` диалится каждый слот, и остальные узлы пула считались бы неиспользуемыми: их простаивающие соединения закрывались бы после каждого использования | `poolReferences` в `urltest_balance_lx.go`, одна строка в `urltest.go` (`bd0b66934`) |
| Хотфикс 084 снят | Апстрим вынес `Close` из-под мьютекса тем же способом. Наш остаток — `SingPacketConn.Close` в lx-файле | `common/interrupt/` |
| `TestLazyStateTransitions` читает часы сна на тик позже | Упал один раз при прогоне под нагрузкой: `SleepSince` — прошедшее время и в момент усыпления бывает нулём. К мержу отношения не имеет | `1286271a7` |

## 5. За чем следить

- **SPEC 028 на Linux и Android начал действовать.** До этого мержа `UDPFragmentDefault = true` у
  `wireguard`-endpoint и `masque`-outbound на этих ОС не снимал DF: опция не ставилась, а ядро по
  умолчанию (`IP_PMTUDISC_WANT`) выставляет DF само. Теперь фрагментация разрешена явно. Наш юнит
  проверяет только отсутствие `PMTUDISC_DO` и разницы не видел.
- **`masque` по h3 остался с разрешённой фрагментацией**, тогда как апстрим у своих QUIC-протоколов
  (hysteria, hysteria2, tuic) флаг убрал. Оставлено по смыслу SPEC 028 (вложенные туннели).
  Проверить на устройстве: туннель WARP по h3 на мобильной сети и WG/AWG поверх `masque`.
- **XHTTP с `xmux` вне нового учёта.** Транспорт не реализует `IdleConnectionKeeper` и
  `V2RayMultiplexClientTransport`: пул `xmux` живёт по своим правилам, как до мержа. Закрытие
  простаивающих соединений на паузе устройства его не касается.
- **`chain`** отдаёт хопы через `Dependencies()` — обходится `ReferenceManager` как обычный
  outbound. Звенья — рантайм-экземпляры вне менеджера, до них учёт не доходит.
- **Группа DNS** (013) отдаёт участников через `Dependencies()` транспорта — участники считаются
  используемыми, пока используется группа.
- **Аварийный режим SPEC 054** диалит узел мимо кеша выбранного; такой узел в `References()` не
  попадает, его простаивающие соединения закроются после использования. Поведение корректное,
  цена — повторный хендшейк.
- **libbox `Pause`/`Wake`**: на Android семантика прежняя плюс закрытие простаивающих соединений.
  iOS-клиент должен звать `WakeNow` по включению экрана — для LxBox на Android не требуется.
- **`common/interrupt/group.go`, `conn.go`** теперь без нашей дельты: при следующем синке брать
  сторону апстрима.

## 6. Критерии приёмки

| # | Критерий | Результат |
|---|---|---|
| 1 | Дрейф от `upstream/stable` = 0 (merge-base == tip `a781ae655`) | ✅ |
| 2 | `require` в `go.mod` равен stable; четыре `replace` на форк-сабмодули на месте | ✅ |
| 3 | Сборка и тесты по §3 п. 4 | ✅ |
| 4 | Набор строк нашей дельты в автослитых файлах не изменился | ✅ |
| 5 | Пул `round_robin` в `References()` | ✅ `TestPoolReferences` |
| 6 | Прогон на устройстве: смена сети, пауза и пробуждение, вложенные туннели, `masque` h3 | не проводился |
| 7 | Выпуск | не выпущено |
