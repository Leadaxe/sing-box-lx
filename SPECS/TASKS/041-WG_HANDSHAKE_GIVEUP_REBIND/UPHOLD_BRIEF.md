# UPHOLD_BRIEF — SPECS/TASKS/041-WG_HANDSHAKE_GIVEUP_REBIND

<!-- Machine-assembled (uphold-brief.sh). Never edited by hand:
     an edited brief is a contaminated judge. -->

| Field | Value |
|---|---|
| Task | SPECS/TASKS/041-WG_HANDSHAKE_GIVEUP_REBIND |
| Diff | file: /private/tmp/claude-501/-Users-macbook-projects-sing-box-lx/7baf7b9a-68eb-42e5-b017-401b6dbb650f/scratchpad/041-combined.diff |

## Input 1: the feature spec (verbatim)

# FEATURE 004 — HOTFIXES — что мы чиним за апстримом

| Поле | Значение |
|------|----------|
| Тип | Процессная фича (постоянная работа форка) |
| Build-tag | — (фиксы встроены в ядро) |
| Состояние | Живой реестр |

Форк не пишется с нуля — он дорабатывает продукт SagerNet. Часть работы
поэтому не «добавить своё», а **починить чужое**: баги апстрима, которые
бьют по нашим пользователям, чинятся у нас, потому что ждать апстрим дороже.

Отсюда обязанность, которой нет у обычной фичи: каждый хотфикс — это патч
поверх чужой базы, и у него должен быть **срок годности**. Пока апстрим
не починил то же самое, мы держим фикс и платим за него на каждом мерже;
как только починил — фикс надо снять, иначе он начнёт конфликтовать
или тихо расходиться с новой логикой.

Поэтому каждая запись несёт **условие снятия**. Реестр того, что уже снято
и за чем следить, — [UPSTREAM_SYNC](../005-UPSTREAM_SYNC/FEATURE.md).

## Promises

<!-- backfill 2026-07-31 · DRAFT — pending author review -->

- **P1. У каждого держащегося фикса есть условие снятия.** Запись реестра без
  условия снятия — долг без срока: на каждом мерже апстрима мы платим за патч,
  не зная, когда перестать. Держатель ожидания — сопровождающий форка.
  Свидетель: тот случай, когда при бампе submodule или мерже апстрима по
  строке реестра видно, что именно проверить и когда фикс снять (010 снят
  ровно так). Мутация: новая запись «держим» без условия снятия.
- **P2. Вложенные туннели через `detour` несут трафик.** Инкапсуляция
  раздувает внешнюю датаграмму; без фикса она молча дропалась из-за DF.
  Свидетель: тот случай, когда AWG-over-AWG через `detour` поднимается и
  качает данные; тот случай, когда явный `"udp_fragment": false` возвращает
  DF. Мутация: нижнее плечо туннельного outbound снова форсит DF по умолчанию.
- **P3. Опечатка в `detour` видна на старте.** Нода с несуществующим
  провайдером падает при запуске с внятной ошибкой, а не живёт «мёртвой,
  в логах пусто». Свидетель: тот случай, когда конфиг с опечаткой в теге
  отвергается на старте. Мутация: возврат ленивого резолва с кэшированием
  промаха.
- **P4. Остановка туннеля завершается быстро.** `box.Close()` при десятках
  WG/AWG-нод — доли секунды, ничего не остаётся недозакрытым. Свидетель:
  тот случай, когда профиль с ~20-30 пропингованными нодами останавливается
  без 10-секундного зависания. Мутация: снятие quiesce-этапа или гейта
  in-flight wake.
- **P5. WG/AWG-узел с умершим путём чинит себя сам.** Если состояние потока
  на пути умерло (сон устройства → протухший NAT/DPI 5-tuple), узел после
  провала цикла рукопожатий сам пересоздаёт сокет и восстанавливается без
  реконнекта пользователя; в здоровом/спящем состоянии механизм ничего не
  стоит; пиновый `listen_port` не меняется (самолечение тогда ограничено).
  Свидетель: тот случай, когда узел с мёртвым первым сокетом восстанавливается
  после give-up сам; тот случай, когда второй give-up в окне не даёт второго
  пересоздания. Мутация: rebind без смены порта при непинованном
  `listen_port`, или фоновый опрос вместо события give-up.

**Не-обещания:** снятые и закрытые записи (010 — снят апстримом; 012 —
не воспроизводится) гарантий не несут. Отправка issue апстриму не обещана —
условия снятия пассивные, проверяются нами на мержах.

**Открытый вопрос (backfill):** запись 040 (self-heal acceptLoop system-стека)
делается параллельной задачей — её promise добавить при закрытии 040, из её
собственных формулировок.

## Реестр

| Задача | Симптом | Где патч | Условие снятия | Статус |
|---|---|---|---|---|
| [010](../../TASKS/010-WG_ENDPOINT_GRO_SPLIT_BRAIN/SPEC.md) | WG-endpoint без `detour` режет download на Android (0.44 Mbps) | submodule `conn/` | ✅ **выполнено** — upstream `24ea133` | **СНЯТ** |
| [028](../../TASKS/028-NESTED_TUNNEL_UDP_FRAGMENT/SPEC.md) | Вложенные туннели через `detour` не ходят | `protocol/masque/outbound.go`, endpoint | Апстрим сам выставит `UDPFragmentDefault` для туннельных outbound'ов | держим |
| [029](../../TASKS/029-ENDPOINT_DETOUR_START_ORDER/SPEC.md) | Endpoint с `detour` мёртв, если провайдер объявлен позже | `protocol/wireguard/endpoint.go` | ✅ причина устранена — upstream `f39ab0e9` (lx.15). Остаток: апстрим сам введёт ранний резолв detour с fail-fast | **частично снят** — держим только fail-fast |
| [030](../../TASKS/030-FAST_BOX_SHUTDOWN/SPEC.md) | `box.Close()` виснет 10 с+ при ~30 узлах | `box.go`, `route/reachability_common_lx.go` | Апстрим введёт свой quiesce-этап при остановке | держим |
| [012](../../TASKS/012-TCP_DOWNLINK_STALL_ZOMBIE_CONNS/SPEC.md) | ↑517 ↓0, приложения «висят» | — (кода нет) | — | закрыт: не воспроизводится |
| [039](../../TASKS/039-REPORT_ARCHIVE_ROTATION/SPEC.md) | Архивы отчётов растут без предела — 427 МБ / 575 папок за 19 дней | `experimental/libbox/report.go` + вызов в OOM- и crash-путях | Апстрим введёт свою ротацию архивов отчётов | держим |
| [040](../../TASKS/040-SINGTUN_ACCEPTLOOP_SELFHEAL/SPEC.md) | acceptLoop system-стека молча умирает от чужого close fd → весь новый TCP получает мгновенный RST до рестарта VPN (LxBox §047: «браузер мёртв, QUIC жив») | форк `submodules/sing-tun` (`stack_system.go`: warn с errno + relisten + счётчик) | Апстрим сделает acceptLoop устойчивым (или заменит механику system-стека) | держим |
| [041](../../TASKS/041-WG_HANDSHAKE_GIVEUP_REBIND/SPEC.md) | WG/AWG-узлы после сна устройства навсегда в ERR (мёртвый 5-tuple), лечит только реконнект | форк `submodules/wireguard-go` (`device/`: rebind по give-up) + 1 строка `transport/wireguard/endpoint.go` | Апстрим введёт своё пересоздание bind по провалу цикла рукопожатий (следить за give-up веткой `device/timers.go` при бампах submodule) | держим |

## Разбор записей

**010 — GRO split-brain.** UDP_GRO включался, а приёмный путь был
linux-only, из-за чего Android получал склеенные пакеты и не разбирал их.
Наш фикс — гейт за `!android`. При миграции на wireguard-go v0.0.3 апстрим
сделал то же самое (`24ea133 «conn: harmonize GOOS checks…»`), наш патч
удалён. Но остался **след**: GRO на Android теперь полностью рабочий и
требует большого `MaxSegmentSize` как топлива — см. запись CONSTRAINT
в [UPSTREAM_SYNC](../005-UPSTREAM_SYNC/FEATURE.md).

**028 — DF во вложенных туннелях.** Нижний UDP-сокет открывается через
`common/dialer`, который по умолчанию ставит DF. При вложении внешняя
датаграмма штатно великовата (инкапсуляция +32 WG, +`s4` AWG на каждый пакет) —
с DF она молча дропается вместо фрагментации. Direct-узлы через тот же detour
работали, потому что их датаграммы мелкие. Фикс: endpoint и masque ставят
`UDPFragmentDefault=true` (opt-out, как у direct/hysteria2/tuic); явный
`"udp_fragment": false` возвращает DF.

**029 — порядок старта.** Ядро упорядочивает старт по зависимостям, и `detour`
в этих зависимостях участвует: провайдер гарантированно стартует раньше
потребителя независимо от порядка в конфиге. Ломалось не упорядочивание —
резолв **обходил** его, утекая в фазу создания, которая идёт до старта. Провайдер,
объявленный в конфиге позже, на тот момент ещё не был зарегистрирован, промах
кэшировался навсегда, и эндпоинт молча не пропускал ни байта; перестановка строк
в конфиге «чинила» его случайно.

Причина устранена **апстримом** (`f39ab0e9`, приехал с мержем в lx.15): преждевременный
резолв из фазы создания убран, проба перенесена в старт. Наша половина фикса,
делавшая то же самое, при мерже растворилась — держать её больше незачем.

Держим только вторую половину: **ранний резолв detour на старте вместо ленивого
при первом дайле**. Это не исправление бага, а защита от тихого отказа — при
опечатке в теге `detour` (или удалённом провайдере) конфиг падает на старте
с внятной ошибкой, тогда как ленивый резолв закэшировал бы промах навсегда
и дал бы ровно тот же симптом «нода мёртвая, в логах пусто», с отладки которого
началась вся задача. Условие снятия: апстрим введёт такой же ранний резолв
с fail-fast сам.

**030 — медленная остановка.** `box.Close` рвал endpoints, пока idle/urltest-тик
ещё слал wake-пинги; каждый `Endpoint.Close()` блокировался на `resumeMu`,
дожидаясь полного device-rebuild с хендшейком (~0.5–5 с), и это суммировалось
последовательно. Фикс из четырёх шагов; drain намеренно **не трогаем** —
иначе use-after-free в gVisor netstack.

**012 — зонтик, а не баг.** Симптом наблюдался на разных узлах, включая WG,
то есть за ним стояло несколько причин. WG-долю закрыл 010. Для не-WG
(VLESS/reality) отдельного фикса нет — симптом не воспроизводится, и закрывать
его выдуманным объяснением мы не стали. Артефакт: зонд `LX_CONN_TRACE`
(в бою не прогонялся; сам зонд глушил `ReadWaiter` и маскировал баг —
перед живым прогоном сделать прозрачным).

**039 — архивы отчётов без ротации.** Апстрим пишет каждый OOM/crash-отчёт в новую
папку и не удаляет старые никогда: удаление подразумевается на стороне клиента, а тот
чистит только выгруженное. Отчёт тяжёлый (два pprof-профиля + копия конфига, ~750 КБ),
поэтому регулярный сбой превращается в сотни МБ — на устройстве нашлось 575 папок и
427 МБ, накопленных за 19 дней, пик 94 отчёта в сутки. Фикс — подрезка архива перед
записью нового отчёта, по количеству и по объёму, общая для обоих путей. Тонкость:
порядок удаления только по mtime — суффиксы коллизий (`-1..-1000`) ломают
лексикографический порядок имён, и сортировка по имени удаляла бы не те папки.
Накопленное фикс не подчищает сам: ротация срабатывает лишь при следующем отчёте.

**041 — мёртвый 5-tuple после сна.** За время сна per-flow-состояние на пути
(NAT-маппинг и/или классификация DPI) умирает, а `wireguard-go` после провала
цикла рукопожатий (90 с ретраев, ветка give-up) навсегда продолжает ретраить
в тот же сокет — тот же исходящий порт, тот же мёртвый 5-tuple. Ручной
реконнект «лечил» ровно сменой ephemeral-порта. Фикс — пассивное
самовосстановление: событие give-up (существующий таймерный путь, срабатывает
только под спросом трафика) один раз переоткрывает bind — со свежим портом,
если `listen_port` не пинован, — и сразу повторяет рукопожатие; masquerade-
приманка `i1` уходит с первой же инициацией нового 5-tuple. В здоровом,
спящем и закрытом состоянии цена нулевая: ни таймеров, ни горутин; на
down-девайсе rebind вырождается в no-op (совместимость со сном SPEC 020 —
из state machine девайса).

## Особенности сопровождения

- **Апстрим-issue мы не заводили.** По 028/029/030 наверх ничего
  не отправлялось — значит условие снятия у них пассивное («апстрим
  однажды сам»), и проверять его нужно нам на каждом мерже. Если политику
  менять, отправка issue сделает срок снятия предсказуемым.
- **Фиксы в submodule — отдельный случай.** 010 жил в `wireguard-go`
  (текущая версия `v0.0.4-8-g7d15f33`); там снятие происходит не мержем
  апстрима, а бампом версии submodule — и заметить его сложнее.
- **Field-подтверждение — часть работы.** 028/029/030 закрыты на стенде,
  но помечены как ждущие проверки на устройстве: стенд воспроизводит
  механизм, а не условия.

## Input 2: Touches

P5 — задача создаёт это обещание (пассивный self-heal по give-up); P1 — запись 041 в реестре несёт условие снятия. Остальные promises не затронуты. Смежная [ENERGY](../../FEATURES/008-ENERGY/FEATURE.md) не затрагивается: у спящего узла таймеры остановлены и событие срабатывания недостижимо.

## Input 3: the diff

```diff
diff --git a/submodules/wireguard-go b/submodules/wireguard-go
index d892107a8..f007282d3 160000
--- a/submodules/wireguard-go
+++ b/submodules/wireguard-go
@@ -1 +1 @@
-Subproject commit d892107a83de2127d0a4785e64f141f57c397ab3
+Subproject commit f007282d31da439300f30af6074ef85f9fd8b4fa
diff --git a/transport/wireguard/endpoint.go b/transport/wireguard/endpoint.go
index a2cc33f35..5689461d6 100644
--- a/transport/wireguard/endpoint.go
+++ b/transport/wireguard/endpoint.go
@@ -322,6 +322,11 @@ func (e *Endpoint) Start(resolve bool) error {
 		},
 	}
 	wgDevice := device.NewDevice(e.options.Context, e.returnDevice, bind, logger, e.options.Workers)
+	// lx: SPEC 041 — passive self-heal: on handshake give-up the device rebinds
+	// its socket (fresh ephemeral port unless the user pinned listen_port) and
+	// re-initiates, so a dead NAT/DPI flow entry cannot hold the endpoint in
+	// ERR until a manual reconnect.
+	wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)
 	e.tunDevice.SetDevice(wgDevice)
 	var ipcConf strings.Builder
 	ipcConf.WriteString(e.ipcConf)
diff --git a/device/device.go b/device/device.go
index 047affd..da64c87 100644
--- a/device/device.go
+++ b/device/device.go
@@ -116,6 +116,23 @@ type Device struct {
 	}
 
 	ipackets [5]*obfChain
+
+	// lx: SPEC 041 — passive self-heal on handshake give-up. When a peer's
+	// handshake retry cycle exhausts (the give-up branch of
+	// expiredRetransmitHandshake), the device reopens its bind once — with a
+	// fresh ephemeral port when freshPort is set — and immediately
+	// re-initiates. Heals dead per-flow path state (an expired NAT mapping or
+	// a poisoned DPI flow entry) that otherwise pins every retry to the same
+	// dead 5-tuple until a manual reconnect. Zero cost while healthy: no
+	// timers, no goroutines — the trigger is the existing give-up event,
+	// which only fires under traffic demand after ~90s of unanswered
+	// initiations. Enabled by default; sing-box decides freshPort from
+	// whether the user pinned listen_port.
+	giveUpRebind struct {
+		enabled   atomic.Bool
+		freshPort atomic.Bool
+		last      atomic.Int64 // unix seconds of the last rebind (debounce)
+	}
 }
 
 // deviceState represents the state of a Device.
@@ -312,6 +329,7 @@ func (device *Device) SetPrivateKey(sk NoisePrivateKey) error {
 func NewDevice(ctx context.Context, tunDevice tun.Device, bind conn.Bind, logger *Logger, workers int) *Device {
 	device := new(Device)
 	device.pauseManager = service.FromContext[pause.Manager](ctx)
+	device.giveUpRebind.enabled.Store(true) // lx: SPEC 041 — self-heal on by default
 	device.state.state.Store(uint32(deviceStateDown))
 	device.closed = make(chan struct{})
 	device.log = logger
@@ -564,6 +582,55 @@ func (device *Device) BindUpdate() error {
 	return nil
 }
 
+// lx: SPEC 041 — configure the handshake give-up self-heal (see the
+// giveUpRebind field comment). freshPort must be false when the user pinned
+// an explicit listen_port: the pinned port is preserved, at the cost of the
+// rebind not changing the 5-tuple.
+func (device *Device) SetGiveUpRebind(enabled, freshPort bool) {
+	device.giveUpRebind.enabled.Store(enabled)
+	device.giveUpRebind.freshPort.Store(freshPort)
+}
+
+// lx: SPEC 041 — invoked from the give-up branch of
+// expiredRetransmitHandshake: ~90s of initiations went unanswered, so the
+// current socket's 5-tuple is proven dead. Reopen the bind (fresh ephemeral
+// port when allowed) and kick a new handshake cycle immediately. Runs the
+// heavy part in a goroutine so the timer callback never blocks on
+// BindUpdate's worker drain. Debounced to one rebind per RekeyAttemptTime
+// per device (CAS on `last` settles concurrent multi-peer give-ups). On a
+// down or closed device BindUpdate does not reopen the socket, so a rebind
+// racing idle-suspend (SPEC 020) or Close degrades to a no-op.
+func (device *Device) handleHandshakeGiveUp(peer *Peer) {
+	if !device.giveUpRebind.enabled.Load() {
+		return
+	}
+	if device.isClosed() {
+		return
+	}
+	now := time.Now().Unix()
+	last := device.giveUpRebind.last.Load()
+	if now-last < int64(RekeyAttemptTime/time.Second) {
+		return
+	}
+	if !device.giveUpRebind.last.CompareAndSwap(last, now) {
+		return
+	}
+	fresh := device.giveUpRebind.freshPort.Load()
+	go func() {
+		if fresh {
+			device.net.Lock()
+			device.net.port = 0
+			device.net.Unlock()
+		}
+		if err := device.BindUpdate(); err != nil {
+			device.log.Errorf("%v - Failed to rebind after handshake give-up: %v", peer, err)
+			return
+		}
+		device.log.Verbosef("%v - Rebound socket after handshake give-up (fresh port=%v)", peer, fresh)
+		peer.SendHandshakeInitiation(false)
+	}()
+}
+
 func (device *Device) BindClose() error {
 	device.net.Lock()
 	err := closeBindLocked(device)
diff --git a/device/lx_giveup_rebind_test.go b/device/lx_giveup_rebind_test.go
new file mode 100644
index 0000000..bbd4706
--- /dev/null
+++ b/device/lx_giveup_rebind_test.go
@@ -0,0 +1,83 @@
+/* SPDX-License-Identifier: MIT
+ *
+ * lx: SPEC 041 — unit tests for the give-up rebind mechanics on top of the
+ * self-heal harness (lx_giveup_selfheal_test.go): fresh vs pinned port,
+ * debounce, and disabled = upstream parity. These use the post-fix API
+ * (SetGiveUpRebind) and are NOT expected to compile on the pre-fix base.
+ */
+
+package device
+
+import (
+	"testing"
+	"time"
+)
+
+func waitOpens(t *testing.T, bind *gateBind, want int) {
+	t.Helper()
+	deadline := time.Now().Add(5 * time.Second)
+	for bind.openCount() < want {
+		if time.Now().After(deadline) {
+			t.Fatalf("bind reopened %d times, want %d", bind.openCount(), want)
+		}
+		time.Sleep(10 * time.Millisecond)
+	}
+}
+
+// Fresh mode (listen_port not pinned): the rebind must ask the OS for a new
+// ephemeral port — Open is called with port 0.
+func TestGiveUpRebindFreshPort(t *testing.T) {
+	pair := newGiveUpPair(t, false)
+	pair.devA.SetGiveUpRebind(true, true)
+
+	triggerGiveUp(t, pair.devA, pair.pkB)
+	waitOpens(t, pair.bindA, 2)
+
+	ports := pair.bindA.portsSnapshot()
+	if ports[1] != 0 {
+		t.Fatalf("rebind requested port %d, want 0 (fresh ephemeral)", ports[1])
+	}
+}
+
+// Pinned mode (explicit listen_port): the rebind must keep the current port.
+// chanBind.Open reports its source id (1) as the actual port, so the device
+// stores net.port=1 after the first Open and must reuse it.
+func TestGiveUpRebindPinnedPortPreserved(t *testing.T) {
+	pair := newGiveUpPair(t, false)
+	pair.devA.SetGiveUpRebind(true, false)
+
+	triggerGiveUp(t, pair.devA, pair.pkB)
+	waitOpens(t, pair.bindA, 2)
+
+	ports := pair.bindA.portsSnapshot()
+	if ports[1] != 1 {
+		t.Fatalf("rebind requested port %d, want 1 (pinned)", ports[1])
+	}
+}
+
+// A second give-up inside the debounce window must not rebind again.
+func TestGiveUpRebindDebounce(t *testing.T) {
+	pair := newGiveUpPair(t, false)
+
+	triggerGiveUp(t, pair.devA, pair.pkB)
+	waitOpens(t, pair.bindA, 2)
+
+	triggerGiveUp(t, pair.devA, pair.pkB)
+	time.Sleep(300 * time.Millisecond)
+	if got := pair.bindA.openCount(); got != 2 {
+		t.Fatalf("debounce failed: bind opened %d times, want 2", got)
+	}
+}
+
+// Disabled: the give-up branch must behave exactly like upstream — flush and
+// stop, no rebind.
+func TestGiveUpRebindDisabled(t *testing.T) {
+	pair := newGiveUpPair(t, false)
+	pair.devA.SetGiveUpRebind(false, false)
+
+	triggerGiveUp(t, pair.devA, pair.pkB)
+	time.Sleep(300 * time.Millisecond)
+	if got := pair.bindA.openCount(); got != 1 {
+		t.Fatalf("disabled mechanism still rebound: %d opens, want 1", got)
+	}
+}
diff --git a/device/lx_giveup_selfheal_test.go b/device/lx_giveup_selfheal_test.go
new file mode 100644
index 0000000..2f59471
--- /dev/null
+++ b/device/lx_giveup_selfheal_test.go
@@ -0,0 +1,172 @@
+/* SPDX-License-Identifier: MIT
+ *
+ * lx: SPEC 041 — behavioural red/green test for the handshake give-up
+ * self-heal. Field failure mode (WARP/AWG after device sleep): the per-flow
+ * path state of the socket's 5-tuple dies (expired NAT mapping / poisoned DPI
+ * flow entry), every packet sent from the old socket vanishes, and upstream
+ * wireguard-go retries into that dead socket forever — only a manual
+ * reconnect (new socket, new ephemeral port) heals the peer.
+ *
+ * The test models the dead 5-tuple with a bind whose FIRST socket generation
+ * silently swallows every send; any socket opened after a rebind delivers
+ * normally. It then drives the peer into the give-up branch of
+ * expiredRetransmitHandshake and expects traffic to flow end to end without
+ * any reconnect:
+ *
+ *   - pre-fix (base): give-up only flushes staged packets, the bind is never
+ *     reopened, every retry keeps dying in the first generation -> timeout;
+ *   - post-fix: give-up rebinds the socket and re-initiates -> tunnel comes
+ *     up and the packet arrives.
+ *
+ * This file deliberately uses NO post-fix API, so it compiles and runs RED on
+ * the pre-fix base commit. Reuses the chanBind/chanTun harness from
+ * transport_padding_test.go.
+ */
+
+package device
+
+import (
+	"context"
+	"encoding/hex"
+	"fmt"
+	"sync"
+	"testing"
+	"time"
+
+	"github.com/sagernet/wireguard-go/conn"
+)
+
+// gateBind wraps chanBind: it records every Open (the port argument the
+// device asked for) and silently swallows sends while the socket generation
+// is at most dropOpens — modelling a dead 5-tuple whose packets vanish on the
+// path without any local error.
+type gateBind struct {
+	*chanBind
+	mu        sync.Mutex
+	openPorts []uint16
+	opens     int
+	dropOpens int // swallow sends while opens <= dropOpens
+}
+
+func (b *gateBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
+	fns, actual, err := b.chanBind.Open(port)
+	b.mu.Lock()
+	b.opens++
+	b.openPorts = append(b.openPorts, port)
+	b.mu.Unlock()
+	return fns, actual, err
+}
+
+func (b *gateBind) Send(bufs [][]byte, ep conn.Endpoint, offset int) error {
+	b.mu.Lock()
+	drop := b.opens <= b.dropOpens
+	b.mu.Unlock()
+	if drop {
+		return nil // the dead 5-tuple: no local error, the packet just vanishes
+	}
+	return b.chanBind.Send(bufs, ep, offset)
+}
+
+func (b *gateBind) openCount() int {
+	b.mu.Lock()
+	defer b.mu.Unlock()
+	return b.opens
+}
+
+func (b *gateBind) portsSnapshot() []uint16 {
+	b.mu.Lock()
+	defer b.mu.Unlock()
+	return append([]uint16(nil), b.openPorts...)
+}
+
+type giveUpPair struct {
+	devA, devB *Device
+	tunA, tunB *chanTun
+	bindA      *gateBind
+	pkB        NoisePublicKey
+}
+
+// newGiveUpPair builds two Up()'d peered devices; devA sits on a gateBind
+// whose first socket generation optionally blackholes all sends.
+func newGiveUpPair(t *testing.T, dropFirstOpen bool) *giveUpPair {
+	t.Helper()
+
+	skA, err := newPrivateKey()
+	if err != nil {
+		t.Fatalf("newPrivateKey A: %v", err)
+	}
+	skB, err := newPrivateKey()
+	if err != nil {
+		t.Fatalf("newPrivateKey B: %v", err)
+	}
+	pkA := skA.publicKey()
+	pkB := skB.publicKey()
+
+	rawA, rawB := newChanBindPair()
+	bindA := &gateBind{chanBind: rawA}
+	if dropFirstOpen {
+		bindA.dropOpens = 1
+	}
+	tunA := newChanTun()
+	tunB := newChanTun()
+
+	devA := NewDevice(context.Background(), tunA, bindA, NewLogger(LogLevelError, "devA: "), 1)
+	devB := NewDevice(context.Background(), tunB, rawB, NewLogger(LogLevelError, "devB: "), 1)
+	t.Cleanup(devA.Close)
+	t.Cleanup(devB.Close)
+
+	cfgA := fmt.Sprintf(
+		"private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=127.0.0.1:2\nallowed_ip=%s/32\n",
+		hex.EncodeToString(skA[:]), hex.EncodeToString(pkB[:]), testIPB)
+	cfgB := fmt.Sprintf(
+		"private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=127.0.0.1:1\nallowed_ip=%s/32\n",
+		hex.EncodeToString(skB[:]), hex.EncodeToString(pkA[:]), testIPA)
+
+	if err := devA.IpcSet(cfgA); err != nil {
+		t.Fatalf("IpcSet A: %v", err)
+	}
+	if err := devB.IpcSet(cfgB); err != nil {
+		t.Fatalf("IpcSet B: %v", err)
+	}
+	if err := devA.Up(); err != nil {
+		t.Fatalf("Up A: %v", err)
+	}
+	if err := devB.Up(); err != nil {
+		t.Fatalf("Up B: %v", err)
+	}
+
+	return &giveUpPair{devA: devA, devB: devB, tunA: tunA, tunB: tunB, bindA: bindA, pkB: pkB}
+}
+
+// triggerGiveUp drives dev's peer into the give-up branch of
+// expiredRetransmitHandshake exactly the way 90s of unanswered retries would:
+// attempts past the limit, last initiation older than RekeyTimeout.
+func triggerGiveUp(t *testing.T, dev *Device, pk NoisePublicKey) {
+	t.Helper()
+	dev.peers.RLock()
+	peer := dev.peers.keyMap[pk]
+	dev.peers.RUnlock()
+	if peer == nil {
+		t.Fatal("peer not found")
+	}
+	peer.handshake.mutex.Lock()
+	peer.handshake.lastSentHandshake = time.Now().Add(-2 * RekeyTimeout)
+	peer.handshake.mutex.Unlock()
+	peer.timers.handshakeAttempts.Store(MaxTimerHandshakes + 1)
+	expiredRetransmitHandshake(peer)
+}
+
+// TestHandshakeGiveUpSelfHeal: dead first socket, give-up fires — the tunnel
+// must come up and deliver traffic without any reconnect.
+func TestHandshakeGiveUpSelfHeal(t *testing.T) {
+	pair := newGiveUpPair(t, true)
+
+	pkt := buildIPv4Packet(testIPA, testIPB, 8)
+	send := func() { pair.tunA.toDevice <- pkt }
+
+	// Traffic demand: stages the packet and sends the first (blackholed)
+	// initiation, exactly the state a real give-up cycle ends in.
+	send()
+	triggerGiveUp(t, pair.devA, pair.pkB)
+	awaitPacket(t, pair.tunB, pkt, send)
+}
diff --git a/device/timers.go b/device/timers.go
index 80fb7d9..2f0cc0a 100644
--- a/device/timers.go
+++ b/device/timers.go
@@ -98,6 +98,12 @@ func expiredRetransmitHandshake(peer *Peer) {
 		if peer.timersActive() && !peer.timers.zeroKeyMaterial.IsPending() {
 			peer.timers.zeroKeyMaterial.Mod(RejectAfterTime * 3)
 		}
+
+		/* lx: SPEC 041 — the exhausted cycle just proved the current socket's
+		 * 5-tuple dead (90s of initiations, zero replies). Rebind once and
+		 * re-initiate, so a stale NAT mapping / poisoned DPI flow entry cannot
+		 * pin this peer to a dead socket until a manual reconnect. */
+		peer.device.handleHandshakeGiveUp(peer)
 	} else {
 		peer.timers.handshakeAttempts.Add(1)
 		peer.device.log.Verbosef("%s - Handshake did not complete after %d seconds, retrying (try %d)", peer, int(RekeyTimeout.Seconds()), peer.timers.handshakeAttempts.Load()+1)
```

## Ledger form (machine-assembled from the language pack — reproduce EXACTLY)

Write the ledger in the language of this form. Field keys (locus, killer,
evidence, grade, link, verdict, fate, needed) are structural — never translate them.
File skeleton:

```
# UPHOLD — 041-WG_HANDSHAKE_GIVEUP_REBIND

| Field | Value |
|---|---|
| Judge | <who, date> |
| Diff | <range or file> |
| Touches | <PN list> |
| Promises judged | 5, P5 |

## Кандидаты-предательства

1. <candidate: promise, mechanism, concrete failure scenario>
   fate: <ONE of: ПРЕДАНО → PN / НЕ МОГУ ПРОВЕРИТЬ → PN / ОПРОВЕРГНУТО — evidence: why it did NOT happen (quote or command)>
2. <...>  (at least 3; every candidate ends in a fate line — an unkilled
   candidate that produced no verdict is an unfinished pass)

## Леджер

### P1. <promise wording>

**Рассуждение:** <free text: run the promise's witnesses against the diff>

locus:    <file:line — the place fulfilling the promise NOW>
killer:   <the minimal change that would betray it; why it is not in the diff>
evidence: <verbatim quote or a command with its output>
grade:    <static | synthetic | live | device | field>
link:     <one sentence: why the evidence proves the verdict>
verdict:  <ONE of: ДЕРЖИТСЯ / НЕ ЗАТРОНУТО / ПРЕДАНО / НЕ МОГУ ПРОВЕРИТЬ>
needed:   <ONLY after НЕ МОГУ ПРОВЕРИТЬ: the concrete missing check — run/device/log/testbed; this line feeds the verification queue>

### P2. <... one row per EVERY promise of the feature>

## Completion call

Обещаний всего: N. Держится с уликой: n1. Предано: n2. Не затронуто (с уликой): n3. Отложено: n4.
```
