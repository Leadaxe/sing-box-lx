# UPHOLD — 041-WG_HANDSHAKE_GIVEUP_REBIND

| Field | Value |
|---|---|
| Judge | Fable 5 (fresh judge, uphold pass), 2026-08-02 |
| Diff | file: /private/tmp/claude-501/-Users-macbook-projects-sing-box-lx/63e102f5-1808-4157-b76a-da9c411add00/scratchpad/041v2-combined.diff (commit 768398e12 + submodule f007282..1255464) |
| Touches | P5, P1 |
| Promises judged | 6, P6 |

## Кандидаты-предательства

1. **P5 — нудж будит idle-спящие узлы** (прямо названная мутация). Сценарий: устройство просыпается, LxBox шлёт `RebindStaleEndpoints()`, спящий по SPEC 020 узел — а его сессия после долгого сна стухшая *по определению* — проходит стале-предикат, ребайндится и просыпается; энергомодель SPEC 020 разрушена.
   fate: ОПРОВЕРГНУТО — evidence: два независимых гейта. Протокольный: `RebindStale()` выходит на `!w.started.Load()` под `resumeMu` (protocol/wireguard/endpoint_rebind_lx.go:317), а idle-suspend ставит именно `started.Store(false)` (endpoint.go:300-302, внутри `SuspendIfIdle`); девайсный: `RebindIfSessionStale` выходит на `!device.isUp()` (device/lx_giveup_rebind.go:658). Тесты `TestRebindStale_asleepNotWoken` (protocol) и `TestNudgeDownDeviceNoop` (device) — PASS: `ok github.com/sagernet/sing-box/protocol/wireguard 27.614s`, `--- PASS: TestNudgeDownDeviceNoop (0.01s)`.
2. **P5 — рефакторинг v1→общий `selfHealRebind` регрессит give-up-ветку**. Сценарий: `handleHandshakeGiveUp` переписан из device.go в lx_giveup_rebind.go; ошибка в переносе (дебаунс, freshPort, пин порта) — и страховочный ~90 с триггер, единственный полевой-проверенный, ломается.
   fate: ОПРОВЕРГНУТО — evidence: все v1-тесты на новой базе зелёные: `--- PASS: TestHandshakeGiveUpSelfHeal (0.05s)`, `--- PASS: TestGiveUpRebindDebounce (0.45s)`, `--- PASS: TestGiveUpRebindPinnedPortPreserved (0.23s)`, `--- PASS: TestGiveUpRebindFreshPort (0.12s)`, `--- PASS: TestGiveUpRebindDisabled (0.32s)`; give-up-ветка timers.go по-прежнему зовёт `handleHandshakeGiveUp` (device/timers.go:105).
3. **P5 — досрочный триггер срабатывает на живой сессии с транзиентными потерями**. Сценарий: мобильная сеть теряет 3 инициации подряд при живом keypair; досрочный rebind меняет ephemeral-порт под здоровым туннелем — самолечение само становится источником флапа.
   fate: ОПРОВЕРГНУТО — evidence: предикат требует «нет keypair ИЛИ handshake старше RejectAfterTime (180 с)» (device/lx_giveup_rebind.go:642-647, поле `lastHandshakeNano` обновляется в timers.go:199); `--- PASS: TestEarlyRebindFreshSessionNoop (0.35s)` — свежая сессия за порогом попыток не ребайндится.
4. **P5/P4 — горутина нуджа гоняется с Close/suspend**. Сценарий: `RebindStaleEndpoints` запускает обход в горутине; box закрывается параллельно → use-after-free/паника, либо удержание `resumeMu` растягивает остановку (удар по P4).
   fate: ОПРОВЕРГНУТО — evidence: `w.closing` проверяется до и после захвата `resumeMu`, критическая секция — только флаги и nil-safe passthrough (BindUpdate уходит в отдельную горутину девайса, гейтится `isClosed`/`isUp`); прогнал сам: `go test -race ./device/ -run 'NudgeRaces' → ok github.com/sagernet/wireguard-go/device 59.844s` (TestNudgeRacesClose + TestNudgeRacesSuspend, по 25 итераций).
5. **P1 — новая патч-поверхность (нудж) без условия снятия**. Сценарий: v2 добавил третий механизм (libbox wake-API), а строка реестра 041 несёт условие только для give-up — нудж становится долгом без срока.
   fate: ОПРОВЕРГНУТО — evidence: строка реестра 041 (FEATURE.md:86) несёт оба условия: «Триггеры give-up/досрочный: апстрим введёт своё пересоздание bind… (следить за give-up веткой `device/timers.go` при бампах submodule). Нудж: апстрим даст собственный wake-API».
6. **P5 — паника `peers[0]` в `selfHealRebind` при пустом списке**. Сценарий: нудж зовёт shared-действие без пиров → index out of range в горутине → крах ядра.
   fate: ОПРОВЕРГНУТО — evidence: нудж выходит на `len(stale) == 0` до вызова (lx_giveup_rebind.go:669-671), giveup/early всегда передают ровно одного peer; все Nudge-тесты PASS.

## Леджер

### P1. У каждого держащегося фикса есть условие снятия.

**Рассуждение:** Свидетель — по строке реестра при бампе submodule видно, что проверить и когда снять. Диф расширил патч-поверхность 041 (v2: досрочный триггер + нудж через libbox), значит строка реестра обязана покрыть новые поверхности. В копии брифа и в живом FEATURE.md строка 041 обновлена: колонка «Где патч» перечисляет все четыре слоя (submodule `device/`, transport-маркер, `protocol/wireguard/`, `experimental/libbox/`), колонка «Условие снятия» несёт раздельные условия для give-up/досрочного (с указанием, за какой веткой следить при бампах) и для нуджа (апстримный wake-API). Мутация «новая запись “держим” без условия снятия» не произошла. Маркеров `PROMISE F004-P1` в коде нет вообще (grep по репо и сабмодулю пуст) — для процессной фичи locus живёт в самом реестре, но отсутствие маркеров стоит зафиксировать как долг разметки всей фичи 004.

locus:    SPECS/FEATURES/004-HOTFIXES/FEATURE.md:86 (строка реестра 041)
killer:   расширить патч на libbox/нудж, оставив в условии снятия только give-up — новая поверхность осталась бы долгом без срока; в дифе условие расширено вместе с патчем
evidence: FEATURE.md:86: «Триггеры give-up/досрочный: апстрим введёт своё пересоздание bind по провалу цикла рукопожатий (следить за give-up веткой `device/timers.go` при бампах submodule). Нудж: апстрим даст собственный wake-API | держим»
grade:    static
link:     строка реестра называет и что проверять при бампе, и отдельное условие для каждой из трёх поверхностей v2 — ровно то, что обещание требует от записи «держим»
verdict:  ДЕРЖИТСЯ

### P2. Вложенные туннели через `detour` несут трафик.

**Рассуждение:** Свидетели — AWG-over-AWG качает, явный `udp_fragment: false` возвращает DF. Диф добавляет только новые lx-файлы и 11 строк passthrough в transport/wireguard/endpoint.go; loci фикса 028 — `protocol/masque/outbound.go` и нижнее плечо в `protocol/wireguard/endpoint.go` — дифом не задеты. Косвенного влияния нет: rebind переоткрывает bind через существующий `BindUpdate`, диалер и его `UDPFragmentDefault` не трогаются. Маркер `PROMISE F004-P2` отсутствует (маркеров нет во всей фиче); locus подтверждён по SPEC-комментариям.

locus:    protocol/wireguard/endpoint.go:96,153 (`options.UDPFragmentDefault = true`, `// lx: SPEC 028`), protocol/masque/outbound.go:181
killer:   убрать `UDPFragmentDefault=true` с нижнего плеча — DF вернулся бы по умолчанию; дифа в этих файлах нет
evidence: `git diff f7a7d1a2d..768398e12 -- submodules/sing-tun box.go route/ protocol/masque/ protocol/wireguard/endpoint.go | wc -l` → `0`; grep подтверждает `endpoint.go:96: options.UDPFragmentDefault = true`, `outbound.go:181: options.UDPFragmentDefault = true`
grade:    static
link:     нулевой диф по обоим loci плюс живое присутствие фикса в коде доказывают, что обещание не задето
verdict:  НЕ ЗАТРОНУТО

### P3. Опечатка в `detour` видна на старте.

**Рассуждение:** Свидетель — конфиг с опечаткой в теге отвергается на старте. Locus фикса 029 (ранний резолв detour в `Start` вместо ленивого) живёт в `protocol/wireguard/endpoint.go` — файле, которого диф не касается (новый `endpoint_rebind_lx.go` — отдельный файл того же пакета, в старт-путь не вмешивается). Мутация «возврат ленивого резолва с кэшированием промаха» не произошла. Маркер `PROMISE F004-P3` отсутствует; locus подтверждён по комментарию `lx: SPEC 029`.

locus:    protocol/wireguard/endpoint.go:200-206 («lx: SPEC 029 — resolve the detour now, not lazily at first dial»)
killer:   убрать ранний резолв из Start — промах снова кэшировался бы навсегда c симптомом «нода мёртвая, в логах пусто»; диф файла не трогает
evidence: `git diff f7a7d1a2d..768398e12 -- … protocol/wireguard/endpoint.go | wc -l` → `0`; grep: `endpoint.go:200: // lx: SPEC 029 — resolve the detour now, not lazily at first dial`
grade:    static
link:     нулевой диф по файлу-локусу и живой ранний резолв в коде — обещание не задето
verdict:  НЕ ЗАТРОНУТО

### P4. Остановка туннеля завершается быстро.

**Рассуждение:** Свидетель — профиль с ~20-30 нодами останавливается без 10-секундного зависания; мутация — снятие quiesce-этапа или гейта in-flight wake. Loci (box.go, route/reachability_common_lx.go) дифом не задеты (нулевой диф). Косвенное влияние проверено отдельно, потому что нудж вводит новую горутину, берущую `resumeMu` — тот самый мьютекс, на котором в исходном баге висел Close: критическая секция `RebindStale` — проверки флагов и nil-safe passthrough, тяжёлый `BindUpdate` уходит в горутину девайса и гейтится `isClosed`/`isUp`, так что Close ждёт микросекунды, не device-rebuild. Гонка нудж/Close прогнана под -race — зелёная.

locus:    box.go + route/reachability_common_lx.go (quiesce-этап SPEC 030)
killer:   заблокировать Close на `resumeMu`, пока нудж держит его через BindUpdate — вернуло бы суммирующиеся секунды; в дифе BindUpdate вынесен из-под `resumeMu` в горутину девайса
evidence: `git diff f7a7d1a2d..768398e12 -- … box.go route/ … | wc -l` → `0`; `go test -race ./device/ -run 'NudgeRaces'` → `ok github.com/sagernet/wireguard-go/device 59.844s`
grade:    synthetic
link:     нулевой диф по loci плюс race-зелёная гонка нуджа с Close показывают, что новый код не удлиняет остановку
verdict:  НЕ ЗАТРОНУТО

### P5. WG/AWG-узел с умершим путём чинит себя сам — и быстро.

**Рассуждение:** Задача владеет обещанием; диф — это его v2. Прогоняю свидетелей. (1) «узел с мёртвым первым сокетом восстанавливается после give-up сам» — v1-механизм пережил рефакторинг: `TestHandshakeGiveUpSelfHeal` PASS, give-up-ветка timers.go зовёт `handleHandshakeGiveUp` → общий `selfHealRebind`. (2) «второй give-up в окне не даёт второго пересоздания» — `TestGiveUpRebindDebounce` PASS, плюс v2-усиление: окно теперь общее на все три триггера (`TestSharedDebounceAcrossTriggers` PASS — give-up внутри окна после нуджа подавлен, следующая серия лечится снова). (3) «после пробуждения пинг зелёный в первые секунды, а не на второй минуте» — ровно полевой остаток, который v2 закрывает: досрочный триггер (`TestEarlyRebindSelfHeal` PASS, red/green против v1-базы по замыслу теста — сам RED-прогон на f007282 я не повторял, но тест намеренно не использует пост-фиксного API) и нудж (`TestNudgeRebindsStaleSession` PASS — трафик после нуджа ходит end-to-end без спроса). Все три мутации проверены: rebind без смены порта — `TestGiveUpRebindFreshPort`/`TestNudgePinnedPortPreserved` PASS, `SetGiveUpRebind(true, e.options.ListenPort == 0)` (transport/wireguard/endpoint.go:329); фоновых таймеров/опроса нет — триггеры 1-2 живут в существующем retry-цикле, триггер 3 оплачен вызывающим (grep по lx_giveup_rebind.go: ни `time.Ticker`, ни `time.AfterFunc`); нудж не будит спящих — кандидат 1, опровергнут двумя гейтами и двумя тестами. Нулевая цена в здоровом состоянии: `TestNudgeHealthySessionNoop` PASS, `TestEarlyRebindFreshSessionNoop` PASS. `enabled` по умолчанию true (device.go:329). Оговорка: улика синтетическая — сама фича честно фиксирует «041 закрыта на синтетике и в релизный тег ещё не входит», полевой прогон на стенде жалобы — заявленный остаток задачи, не дефект дифа. Маркеров `PROMISE F004-P5` нет; loci подтверждены по `lx: SPEC 041`-комментариям.

locus:    submodules/wireguard-go/device/lx_giveup_rebind.go:1-143 (три триггера + предикат + общий дебаунс); device/timers.go:105,113 (вызовы); protocol/wireguard/endpoint_rebind_lx.go:311-321 (гейты сна); transport/wireguard/endpoint.go:406-412 (passthrough); experimental/libbox/command_server_rebind_lx.go:269-285 (нудж)
killer:   ослабить стале-предикат (убрать проверку RejectAfterTime) или снять гейт `!started` в RebindStale — досрочный rebind бил бы по живым сессиям, а нудж будил бы спящих; оба гейта в дифе на месте и покрыты тестами
evidence: `go test ./device/ -run 'EarlyRebind|Nudge|SharedDebounce|GiveUp|StaleRebind'` → 15/15 PASS, `ok …/device 26.989s`; `go test -race ./device/ -run 'NudgeRaces'` → ok; `go test ./protocol/wireguard/ -run 'RebindStale'` → `ok github.com/sagernet/sing-box/protocol/wireguard 27.614s`
grade:    synthetic
link:     каждый свидетель обещания и каждая названная мутация закрыты проходящим тестом на реальной паре устройств harness'а, включая гонки под -race
verdict:  ДЕРЖИТСЯ

### P6. Смерть acceptLoop system-стека не фатальна.

**Рассуждение:** Locus — форк `submodules/sing-tun` (`stack_system.go`). Диф бампит только гитлинк `submodules/wireguard-go` (f007282→1255464); гитлинк sing-tun не фигурирует ни в списке файлов коммита, ни в дифе — встречного отката фикса 040 нет. Косвенных путей от wireguard-rebind к acceptLoop system-стека не существует (разные стеки, разные сабмодули). Мутация «тихий выход из acceptLoop без relisten» не произошла. Маркер `PROMISE F004-P6` отсутствует (общий долг разметки фичи).

locus:    форк submodules/sing-tun, stack_system.go (warn с errno + relisten + счётчик)
killer:   встречный бамп гитлинка sing-tun на upstream-версию — фикс молча откатился бы; в дифе гитлинк sing-tun не тронут
evidence: `git show --stat 768398e12` — в списке файлов из сабмодулей только `submodules/wireguard-go | 2 +-`; `git diff f7a7d1a2d..768398e12 -- submodules/sing-tun … | wc -l` → `0`
grade:    static
link:     диф физически не достигает репозитория-носителя фикса, значит девайс-верифицированное поведение 040 не могло измениться
verdict:  НЕ ЗАТРОНУТО

## Completion call

Обещаний всего: 6. Держится с уликой: 2. Предано: 0. Не затронуто (с уликой): 4. Отложено: 0.
