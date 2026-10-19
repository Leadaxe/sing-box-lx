# SPEC: 136 — REFERENCE_MANAGER_START_MUX_LOCK

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (условие снятия)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — апстримный `ReferenceManager` (`fd9a076d2` → `07130365d` → `813ddfa98`, в форке с lx.12) блокирует `Box.Start` на чужом мьютексе |
| Статус | I (implemented) — 2026-10-19: `// lx:` блок в `route/reference.go`, юнит-тест красный до правки и зелёный после (в том числе `-race`), сборка под `LX_TAGS` проходит; на устройстве репортёра не прогонялось |
| Ветка | `lx` |
| Build-tag | — (ядро `route`) |
| Источник | [issue #49](https://github.com/Leadaxe/sing-box-lx/issues/49): Android (libbox), 55 outbound'ов + 6 endpoint'ов, 13 групп, два `vless` + ws + TLS + `multiplex`; дампы горутин раз в секунду на всём окне `Starting` |
| Связано | [019](../019-URLTEST_MODE_STICKY/SPEC.md) · [109](../109-UPSTREAM_SYNC_1_14_2_PLUS_15/SPEC.md) (CONSTRAINT о `References()` в [UPSTREAM_SYNC](../../FEATURES/005-UPSTREAM_SYNC/FEATURE.md)), [072](../072-WG_DETOUR_LIFECYCLE_FREEZE/SPEC.md) (та же семья: один узел держит в заложниках жизненный цикл процесса) |

`Box.Start` стоит 10–16 с на стадии `Started`: старт ядра ждёт, пока
закончится (или отвалится по таймауту) один произвольный URL-тест через
mux-outbound.

## 1. Причина

Две апстримные детали складываются в один замок:

1. `route/reference.go`, `ReferenceManager.Start` (стадия `Started`) **синхронно**
   вызывает `m.update()`. Тот обходит все outbound'ы и каждому, на кого никто
   не ссылается, даёт `SetKeepIdleConnections(false)`. У `vless`/`vmess`/`trojan`
   с `multiplex` это уходит в sing-mux `Client.SetKeepIdleConnections` →
   `Client.CloseIdleConnections` → `c.access.Lock()`.
2. sing-mux `client.go`, `Client.offer` берёт `c.access` на **весь** `offerNew()`,
   то есть на полный сетевой dial (TCP + TLS + WebSocket upgrade, таймаут
   `TCPTimeout`). URL-тест групп стартует на `PostStart`, раньше `Started`, и
   первая же проба через mux-outbound уводит `access` в сеть.

Итог: `Started` ждёт handshake удалённого узла. На Android туннель всё это время
висит в `Starting`; остальные URL-тесты того же узла стоят в очереди за тем же
замком (`offer`). Стек дампа — в issue #49, старт в одном и том же месте
(`route/reference.go:94` → `sing-mux/client.go:322`) на каждом снимке.

Файл `route/reference.go` до этой задачи был чисто апстримным, без lx-маркеров.

## 2. Фикс

`route/reference.go`, `ReferenceManager.Start`, блок `// lx: SPEC 136`: первый
`update()` снят со стартового пути и выполняется в той же горутине, что и
`loop()`, перед ним:

```go
go func() {
	m.update()
	m.loop()
}()
```

Что от этого не меняется:

- **`update()` остаётся однопоточным.** `loop()` — единственный другой
  вызывающий; теперь оба вызова идут последовательно в одной горутине, карты
  `keepIdle` и `unreferencedTransports` по-прежнему принадлежат ей одной.
- **Сигналы не теряются.** `subscriber` создаётся и подписывается на хуки
  (`urltest.HistoryStorage`, `clashmode.Manager`) до запуска горутины; буфер
  подписки — 1, `Emit` неблокирующий, так что событие, прилетевшее во время
  первого `update()`, дождётся `loop()`.
- **Публичный `ReferenceManager.CloseIdleConnections()`** (ручное закрытие
  простаивающих соединений, `Box.CloseIdleConnections` ← libbox
  `CloseConnections`) состояние менеджера **не читает**: он обходит менеджеры
  outbound'ов и DNS-транспортов напрямую. Синхронизация не нужна.
- Закрытие: `scope.Add(m.subscriber.Close)` закрывает `done`, `loop()` выходит.
  Если на момент закрытия первый `update()` ещё стоит на чужом замке, горутина
  доживёт до его отпускания и выйдет сама — раньше в этом месте стоял бы весь
  `Box.Start`.

Что меняется: простаивающие соединения неиспользуемых outbound'ов закрываются
на мгновение позже возврата `Box.Start`, а не до него. Это оптимизация, у неё
нет потребителей, которым нужна гарантия «к концу `Start` уже закрыто».

Корень — в sing-mux (`offer` под замком на время dial'а) — **не трогаем**:
пятый форк-сабмодуль ради этого не заводим, апстриму не предлагаем (бан).
Побочный эффект корня — очередь параллельных стримов за одним медленным
dial'ом в рантайме — этой задачей не лечится.

## 3. Условие снятия

Апстрим sing-box перестанет вызывать `update()` на стартовом пути
`ReferenceManager.Start` (например, унесёт его в `loop()` сам) **или** sing-mux
перестанет держать `Client.access` поверх `offerNew()` (резервирование слота под
замком, dial без него). Любого из двух достаточно. При мерже: конфликт в
`ReferenceManager.Start` = сверить с §2; бамп sing-mux = посмотреть `offer`.

## 4. Проверка

- `route/reference_start_lx_test.go`, `TestReferenceManagerStart_doesNotWaitForKeepIdle`:
  фейковый outbound, чей `SetKeepIdleConnections` блокируется на канале, в
  контексте со стаб-менеджерами; `Start(Started)` обязан вернуться за секунду,
  пока вызов ещё заблокирован, и сам вызов обязан прийти с `keep=false`.
  До правки — красный (`Start(Started) blocked …`), после — зелёный, `-race`,
  `-count=3`.
- `go test ./route/...` под `LX_TAGS` — зелёный; `make -f Makefile.lx lx-build` —
  проходит.
- Живая: на конфиге репортёра (issue #49) `Starting → Started` без
  многосекундной паузы; в дампе на старте нет `ReferenceManager.Start` под
  `sync.Mutex.Lock` в `sing-mux.(*Client).CloseIdleConnections`. За репортёром.

## 5. Файлы

- `route/reference.go` — `Start`: первый `update()` в горутине перед `loop()` (`// lx: SPEC 136`)
- `route/reference_start_lx_test.go` — регресс-тест (новый)
