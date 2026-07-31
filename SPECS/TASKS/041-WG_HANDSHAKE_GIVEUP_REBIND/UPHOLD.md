# UPHOLD — 041-WG_HANDSHAKE_GIVEUP_REBIND

| Field | Value |
|---|---|
| Judge | fresh uphold judge (consumer's representative), 2026-07-31 |
| Diff | file: /private/tmp/claude-501/-Users-macbook-projects-sing-box-lx/7baf7b9a-68eb-42e5-b017-401b6dbb650f/scratchpad/041-combined.diff (sing-box-lx `9645d5476` + submodule `d892107..f007282`) |
| Touches | P5, P1 |
| Promises judged | 5, P5 |

## Кандидаты-предательства

1. **P5 — дебаунс съедает ПЕРВОЕ самолечение.** Механизм: `handleHandshakeGiveUp` сравнивает `now - last < RekeyAttemptTime`, а поле `last atomic.Int64` при создании девайса равно нулю. Если бы `last` инициализировался временем старта (или сравнение было бы «прошло ли 90 с с момента старта»), то узел, у которого 5-tuple умер в первые 90 секунд жизни девайса, молча не вылечился бы вовсе — ровно тот сценарий, ради которого фича существует. Конкретный отказ: пользователь будит устройство, профиль стартует, первый же цикл give-up гасится дебаунсом, узел навсегда в ERR.
   fate: ОПРОВЕРГНУТО — evidence: `last` действительно стартует с нуля, и `now-0` — огромное число, поэтому первый rebind проходит. Проверено прямым прогоном на дереве фикса: `TestJudgeFirstGiveUpNotDebounced` — `expected last=0 at construction` не сработал, и далее `first give-up healed: opens=2` (`--- PASS: TestJudgeFirstGiveUpNotDebounced (0.04s)`). Первое give-up-событие лечится немедленно.

2. **P5 — самолечение одноразовое (латч).** Механизм: `last` монотонно растёт при каждом rebind, и если бы CAS/сравнение было построено как «один rebind на девайс», то узел, переживший второй сон устройства через час, уже не восстановился бы — обещание «чинит себя сам» держалось бы ровно один раз за сессию, а пользователю снова понадобился бы реконнект.
   fate: ОПРОВЕРГНУТО — evidence: дебаунс — скользящее окно `RekeyAttemptTime` (90 с), а не латч. Прогон `TestJudgeSecondWindowRebinds` (состарил `last` на 91 с, как это сделали бы настенные часы) дал `second window healed: opens=3` (`--- PASS`). Второй сон лечится так же, как первый.

3. **P5 — rebind, гоняясь с idle-suspend (SPEC 020) / Close, оставляет девайс «поднятым, но с закрытым сокетом».** Механизм самый опасный: `handleHandshakeGiveUp` уносит тяжёлую часть в `go func()` и зовёт `device.BindUpdate()` **в обход** `device.state.Lock()`, который держат все остальные вызывающие (`upLocked` — device.go:217, и `changeState` — device.go:184). А `BindUpdate` сначала безусловно закрывает сокет (`closeBindLocked`), и только потом проверяет `if !device.isUp() { return nil }`. Конкретный отказ: `Resume()`/`Up()` завершился, сокет открыт, состояние up — и тут просыпается отложенная горутина, которая закрывает сокет и, увидев неудачно прочитанное состояние, не открывает новый; узел остаётся живым по состоянию, но глухим — самолечение превращается в порчу.
   fate: ОПРОВЕРГНУТО — evidence: проверено обоими порядками на дереве фикса. (а) rebind после `Down()`+`Up()`: `TestJudgeRebindLeavesSocketClosed` → `opens before=2 after=3 ports=[0 1 1] net.port=1 bind!=nil=true isUp=true` (`--- PASS`) — при состоянии up `BindUpdate` переоткрывает сокет и сохраняет пинованный порт. (б) rebind, попавший в окно suspend: `TestJudgeRebindRacesSuspend` → `after suspend-race: opens=2 ports=[0 1] net.port=1 bind=true isUp=true` (`--- PASS`) — последующий `Up()` восстанавливает сокет, порт не потерян. Гонка вырождается в no-op ровно так, как обещано.

4. **P5 — rebind на закрытом девайсе паникует или течёт.** Механизм: `isClosed()` проверяется до входа в горутину, а `Close()` может случиться внутри окна; `closeBindLocked` ждёт `netc.stopping.Wait()`, что даёт потенциальный дедлок или use-after-close.
   fate: ОПРОВЕРГНУТО — evidence: 25 гонок «give-up против Close» под `-race`: `TestJudgeRebindRacesClose` → `no panic / no deadlock across 25 close races` (`--- PASS: TestJudgeRebindRacesClose (1.00s)`), гонок детектор не нашёл.

5. **P5 — «красный» тест на самом деле не красный** (тест написан так, что проходит и на базе, то есть ничего не доказывает).
   fate: ОПРОВЕРГНУТО — evidence: материализовал базовое дерево `d892107` в скретчпаде и положил туда тот же файл теста: на базе `TestHandshakeGiveUpSelfHeal` даёт `timed out waiting for packet on peer tun` / `--- FAIL: TestHandshakeGiveUpSelfHeal (20.02s)`, на фиксе — `--- PASS: TestHandshakeGiveUpSelfHeal (0.02s)`. Red/green подлинный.

6. **P4 — новая горутина ломает быструю остановку.** Механизм: rebind-горутина держит `device.net` и ждёт `stopping.Wait()`; если она стартовала перед `box.Close()`, каждый `Endpoint.Close()` мог бы снова блокироваться, воскрешая 10-секундное зависание, которое чинила запись 030.
   fate: ОПРОВЕРГНУТО — evidence: диффом не затронуты ни `box.go`, ни `route/reachability_common_lx.go` — `git show 9645d5476 -- box.go route/reachability_common_lx.go | wc -l` → `0`; quiesce-этап на месте (`box.go:639: s.router.QuiesceForShutdown()`). Плюс гонка с Close по кандидату 4 не зависает.

## Леджер

### P1. У каждого держащегося фикса есть условие снятия.

**Рассуждение:** свидетель — «при бампе submodule или мерже апстрима по строке реестра видно, что именно проверить и когда фикс снять». Задача добавляет в реестр новую запись 041 со статусом «держим», то есть ровно тот случай, против которого направлена мутация («новая запись “держим” без условия снятия»). Проверяю саму добавленную строку. Маркеров `PROMISE F004-P1` в репозитории нет (`grep -rn "PROMISE F0"` по всему дереву не дал ни одного попадания) — конвенция маркеров в этом проекте не применяется, поэтому locus установлен напрямую по строке реестра. Условие снятия у 041 не просто присутствует, но и операционно: названа конкретная ветка кода для наблюдения при бампах submodule — это сильнее пассивной формулировки «апстрим однажды сам» у 028/030.

```
locus:    SPECS/FEATURES/004-HOTFIXES/FEATURE.md:88 (строка реестра 041, колонка «Условие снятия»)
killer:   Добавить запись со статусом «держим» и прочерком/пустотой в колонке «Условие снятия». В диффе этого нет: добавленная строка несёт заполненное условие вместе с указателем, за чем следить.
evidence: `git show 9645d5476 -- SPECS/FEATURES/004-HOTFIXES/FEATURE.md | grep "^+"` → строка реестра 041 с условием «Апстрим введёт своё пересоздание bind по провалу цикла рукопожатий (следить за give-up веткой `device/timers.go` при бампах submodule)»
grade:    static
link:     Единственная новая запись реестра, созданная этой задачей, несёт непустое условие снятия с явным местом наблюдения — то самое, что обещает P1, и прямая противоположность мутации.
verdict:  ДЕРЖИТСЯ
```

### P2. Вложенные туннели через `detour` несут трафик.

**Рассуждение:** свидетели — AWG-over-AWG через `detour` качает данные; явный `"udp_fragment": false` возвращает DF. Мутация — нижнее плечо туннельного outbound снова форсит DF по умолчанию. Обещание живёт в установке `UDPFragmentDefault=true`. Диффом эти файлы не затронуты вовсе: изменены только `submodules/wireguard-go` (указатель + `device/`) и пять строк в `transport/wireguard/endpoint.go`. Косвенного влияния тоже нет: `UDPFragmentDefault` — опция диалера, определяющая флаг DF при открытии нижнего UDP-сокета, а rebind переоткрывает bind через тот же `device.net.bind.Open` с тем же самым диалером, то есть свежий сокет наследует ту же политику фрагментации, что и исходный. Порт меняется, флаг DF — нет.

```
locus:    protocol/wireguard/endpoint.go:96 и protocol/wireguard/endpoint.go:153 (`UDPFragmentDefault: true, // lx: SPEC 028`), protocol/masque/outbound.go:181
killer:   Убрать `UDPFragmentDefault = true` (или заменить на false) в endpoint/masque. Этих файлов дифф не касается.
evidence: `git show 9645d5476 --stat -- protocol/masque/outbound.go protocol/wireguard/endpoint.go` → пусто. Установки на месте: `grep -n "UDPFragmentDefault" protocol/wireguard/endpoint.go protocol/masque/outbound.go` → `protocol/masque/outbound.go:181: options.UDPFragmentDefault = true`, `protocol/wireguard/endpoint.go:96: options.UDPFragmentDefault = true`, `protocol/wireguard/endpoint.go:153: UDPFragmentDefault: true, // lx: SPEC 028`.
grade:    static
link:     Дифф доказуемо не касается файлов, несущих обещание, а установки DF по-прежнему присутствуют в коде — мутация не внесена ни прямо, ни через общий путь rebind.
verdict:  НЕ ЗАТРОНУТО
```

### P3. Опечатка в `detour` видна на старте.

**Рассуждение:** свидетель — конфиг с опечаткой в теге отвергается на старте; мутация — возврат ленивого резолва с кэшированием промаха. Обещание держит ранний резолв detour в `protocol/wireguard/endpoint.go`. Дифф этот файл не трогает (изменён однофамилец `transport/wireguard/endpoint.go` — другой пакет). Проверил и косвенное влияние: единственная правка в `transport/wireguard/endpoint.go` — вызов `wgDevice.SetGiveUpRebind` в `Start()` **после** `device.NewDevice`, то есть уже после фазы резолва (резолв идёт выше, в блоке `resolve` на строках ~270-276). Ни порядка инициализации, ни fail-fast-поведения старта она не меняет: это чистый сеттер двух атомиков без возврата ошибки.

```
locus:    protocol/wireguard/endpoint.go (ранний резолв detour с fail-fast на старте; запись 029 реестра — FEATURE.md:83)
killer:   Вернуть ленивый резолв провайдера при первом дайле с кэшированием промаха. В диффе нет: файл не изменён, а добавленная в `transport/wireguard/endpoint.go` строка стоит после резолва и не влияет на его момент.
evidence: `git show 9645d5476 --stat -- protocol/wireguard/endpoint.go` → пусто. Единственная кодовая вставка в соседнем файле: `transport/wireguard/endpoint.go:329: wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)`, стоящая сразу за `wgDevice := device.NewDevice(...)`, тогда как резолв эндпоинтов расположен выше по функции (строки ~264-276).
grade:    static
link:     Файл-носитель обещания не изменён, а единственная правка в соседнем файле выполняется позже фазы резолва и не может вернуть ленивость — поведение старта при опечатке неизменно.
verdict:  НЕ ЗАТРОНУТО
```

### P4. Остановка туннеля завершается быстро.

**Рассуждение:** свидетель — профиль с ~20-30 пропингованными нодами останавливается без 10-секундного зависания; мутация — снятие quiesce-этапа или гейта in-flight wake. Файлы-носители (`box.go`, `route/reachability_common_lx.go`) диффом не затронуты. Но здесь untouched недостаточно проверить статически: задача вводит **новую горутину**, которая берёт `device.net` и внутри `BindUpdate`→`closeBindLocked` ждёт `netc.stopping.Wait()`. Это ровно тот класс блокировок, из-за которого 030 и возникла, поэтому проверил косвенное влияние прогоном. Гонка «give-up против Close» 25 раз под детектором гонок не дала ни зависания, ни паники. Плюс сам механизм устроен так, что на закрытом девайсе он выходит раньше горутины (`if device.isClosed() { return }`), а на опущенном `BindUpdate` не переоткрывает сокет.

```
locus:    box.go:639 (`s.router.QuiesceForShutdown()`) и route/reachability_common_lx.go (гейт in-flight wake)
killer:   Убрать вызов `QuiesceForShutdown` из `box.Close` или снять гейт in-flight wake. Ни того, ни другого файла дифф не касается; quiesce-вызов на месте.
evidence: `git show 9645d5476 -- box.go route/reachability_common_lx.go | wc -l` → `0`; `grep -rn "quiesce\|Quiesce" box.go` → `box.go:639: s.router.QuiesceForShutdown()`. Косвенное влияние новой горутины проверено: `go test ./device/ -run TestJudgeRebindRacesClose -race` → `no panic / no deadlock across 25 close races`, `--- PASS: TestJudgeRebindRacesClose (1.00s)`.
grade:    synthetic
link:     Носители обещания не изменены и quiesce-этап на месте, а единственный правдоподобный канал косвенной порчи — новая горутина, блокирующая закрытие — опровергнут прогоном гонок с Close без зависаний.
verdict:  НЕ ЗАТРОНУТО
```

### P5. WG/AWG-узел с умершим путём чинит себя сам.

**Рассуждение:** обещание разложено на четыре проверяемые части, и задача его создаёт, так что каждую проверяю отдельно.
(1) *Восстановление после give-up без реконнекта.* Первый свидетель — «узел с мёртвым первым сокетом восстанавливается после give-up сам». Он воплощён буквально: тест моделирует мёртвый 5-tuple биндом, чьё первое поколение сокета молча глотает отправки. Red/green подлинный — я материализовал базовое дерево `d892107` в скретчпаде и прогнал на нём тот же файл теста: база падает по таймауту, фикс проходит.
(2) *Нулевая цена в здоровом/спящем состоянии.* Ни таймеров, ни горутин в покое: триггер — существующая ветка give-up в `expiredRetransmitHandshake`, а вся работа заводится только при её срабатывании; фоновых опросов (второй половины мутации) в диффе нет. У спящего узла путь недостижим, потому что все таймеры гейтятся `timersActive()`, где `peer.device.isUp()` (device/timers.go:79), а SPEC 020 усыпляет именно через `device.Down()`.
(3) *Пинованный `listen_port` не меняется.* Ядро передаёт `freshPort` как `e.options.ListenPort == 0`, и в пинованном режиме порт сохраняется — это ровно защита от мутации «rebind без смены порта при непинованном listen_port», взятая с обеих сторон: при непинованном порт запрашивается нулевой (свежий эфемерный), при пинованном — сохраняется.
(4) *Второй give-up в окне не даёт второго пересоздания* — второй свидетель, покрыт дебаунсом, причём я убедился, что дебаунс не вырождается ни в проглатывание первого лечения, ни в одноразовый латч (кандидаты 1 и 2).
Отмечу для спеки: живого подтверждения на устройстве (реальный сон телефона, реальный NAT/DPI) в этом проходе нет — весь grade синтетический, ровно как «особенности сопровождения» и предупреждают про 028/029/030. Механизм доказан, полевые условия — нет; но обещание сформулировано через свидетелей, которые все воспроизведены. Маркер `PROMISE F004-P5` отсутствует, хотя места-носители помечены содержательными комментариями `lx: SPEC 041` — конвенция маркеров в проекте не используется, locus установлен по ним.

```
locus:    submodules/wireguard-go device/device.go:603 (`handleHandshakeGiveUp`), точка вызова device/timers.go:106 (`peer.device.handleHandshakeGiveUp(peer)`), включение transport/wireguard/endpoint.go:329 (`wgDevice.SetGiveUpRebind(true, e.options.ListenPort == 0)`)
killer:   Заменить триггер фоновым опросом вместо события give-up, либо звать `SetGiveUpRebind(true, true)` безусловно (свежий порт вопреки пинованному `listen_port`), либо убрать дебаунс. Ни одного из трёх в диффе нет: триггер — единственная строка в существующей ветке give-up, `freshPort` вычисляется из `ListenPort == 0`, дебаунс на CAS присутствует.
evidence: Red/green: на базе `d892107` (материализована в скретчпаде + тот же файл теста) `go test ./device/ -run TestHandshakeGiveUpSelfHeal` → `lx_giveup_selfheal_test.go:171: timed out waiting for packet on peer tun` / `--- FAIL: TestHandshakeGiveUpSelfHeal (20.02s)`; на фиксе `f007282` под `-race` → `--- PASS: TestHandshakeGiveUpSelfHeal (0.02s)`. Остальные свидетели, тот же прогон: `--- PASS: TestGiveUpRebindFreshPort`, `--- PASS: TestGiveUpRebindPinnedPortPreserved`, `--- PASS: TestGiveUpRebindDebounce`, `--- PASS: TestGiveUpRebindDisabled`, итог `ok github.com/sagernet/wireguard-go/device 2.826s`. Полный пакет без регрессий: `go test ./device/ -count=1` → `ok github.com/sagernet/wireguard-go/device 1.542s`. Гейт спящего состояния: `device/timers.go:79: return peer.isRunning.Load() && peer.device != nil && peer.device.isUp()`.
grade:    synthetic
link:     Оба названных фичей свидетеля воспроизведены прогоном (мёртвый первый сокет лечится сам — и только на фиксе; второй give-up в окне второго пересоздания не даёт), пинованный порт сохраняется, а триггером служит существующее событие give-up, а не фоновый опрос — то есть обе половины мутации отсутствуют.
verdict:  ДЕРЖИТСЯ
```

## Completion call

Обещаний всего: 5. Держится с уликой: 2. Предано: 0. Не затронуто (с уликой): 3. Отложено: 0.
