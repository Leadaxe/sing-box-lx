# Обратная связь клиента (LxBox) — §3.6 per-call cancel упирается в gomobile-биндинг

> **СТАТУС: РЕШЕНО ✅** (ответ ядра в конце файла). Слой 2 разблокирован на текущем
> биндинге `v1.14.0-lx.1` через **отдельный ping-`CommandClient` + `Disconnect()`** —
> правок нативной поверхности НЕ требуется. Файл сохранён как переписка запрос↔ответ;
> action item остаётся за LxBox: завести pingClient-инстанс и провести on-device verify.

**От:** LxBox (клиентская сторона)
**К:** команда ядра sing-box-lx
**Контекст:** SPEC 015 §3.6 «Отмена URLTestOutbound», слой 2 (отмена на клиенте)
**Статус:** ~~блокер слоя 2 — нужен caller-механизм отмены в биндинге~~ → решено (вариант #2)

## Спасибо за слой 1

Фикс ctx-bind (gRPC per-call `ctx` вместо `boxService.ctx`) принят и понятен:
отмена вызова теперь обрывает один in-flight тест на dial, не трогая остальные
стримы. Развенчание мифа про `cancelDelays` — полезно, сверились по proxies.go.

## Проблема: слой 2 не реализуем на текущем биндинге

§3.6 говорит: «per-call отмена должна приходить со стороны caller'а (LxBox/Dart —
per-call `CancelToken` / `call.cancel()`)». Но в **gomobile-биндинге AAR**
(`v1.14.0-lx.1`, проверено `javap` по `libbox.aar`) у `CommandClient` ровно одна
сигнатура теста, **без какого-либо cancel-параметра/handle**:

```
public native URLTestOutboundResult urlTestOutbound(String tag, String link, int timeoutMs) throws Exception
```

Других перегрузок нет; класса `CancelToken`/per-call-context в `io.nekohasekai.libbox`
нет (есть только `ExchangeContext` — для другого). Единственный рычаг отмены,
доступный caller'у — `client.disconnect()`, который рвёт **весь** CommandClient
(и все его подписки-стримы).

То есть «per-call `call.cancel()`» из §3.6 **на стороне клиента вызвать нечем** —
gomobile-обёртка не экспонирует отмену отдельного вызова. Слой 1 (per-call ctx)
готов в ядре, но «дёрнуть» этот ctx со стороны Java/Kotlin/Dart нельзя.

## Что это значит для нас прямо сейчас

Наша текущая масс-отмена — **epoch-гейт**: при отмене бампаем счётчик, воркеры
перестают применять результаты. Но `urlTestOutbound` — **синхронный блокирующий**
gomobile-вызов; in-flight dial в ядре **продолжается до своего таймаута**
(до ~10 «зомби»-тестов при concurrency=10). UI реагирует мгновенно, ядро — нет.
Это ровно тот разрыв, что слой 1 призван закрыть, но мы не можем его задействовать.

## Запрос к ядру — экспонировать отмену в биндинге

Любой из вариантов разблокирует слой 2 (в порядке нашего предпочтения):

1. **Cancellable per-call handle.** `urlTestOutbound` возвращает/принимает
   объект с `cancel()` (или отдельный метод `cancelURLTest(callID)`), который
   на Go-стороне отменяет per-call `ctx` именно этого вызова. Самый точный —
   per-node cancel без сноса клиента.

2. **Отдельный «one-shot» CommandClient под ping.** Поддержать дешёвый
   short-lived client (или гарантировать, что `disconnect()` на ОТДЕЛЬНОМ
   ping-client рвёт только его вызовы, не общий `c.ctx`). Тогда клиент держит
   pingClient ≠ statusClient/screenClient/profilerClient, и `disconnect()` его
   = массовая отмена всех ping-dial без вреда другим стримам. Работает без
   per-call гранулярности, но снимает «зомби». **Это наш fallback, если #1
   дорог** — мы можем поднять отдельный ping-client сами, нужна лишь гарантия,
   что его `disconnect()` доходит до per-call `ctx` тестов (а не виснет на
   уже-ушедшем в Go dial).

3. **`CancelToken`-параметр** у `urlTestOutbound` (gomobile-совместимый тип),
   который Go оборачивает в дочерний `ctx`. Симметрично Clash `r.Context()`.

Вопрос к ядру: **рвёт ли `CommandClient.disconnect()` уже-ушедшие в dial
per-call тесты** (через отмену их `ctx`), или dial доживает до timeout
независимо от disconnect? От этого зависит, годится ли вариант #2 без правок
ядра. Если `disconnect()` корректно отменяет per-call ctx — мы реализуем #2
сами (отдельный ping-client), и доработка биндинга не нужна.

## Сводка

- Слой 1 (ctx-bind) ✅ — спасибо.
- Слой 2 (client-side cancel) ⛔ — заблокирован: gomobile-биндинг не даёт
  caller'у отменить вызов кроме `disconnect()` всего клиента.
- Нужно от ядра: либо per-call cancel-handle (#1/#3), либо подтверждение, что
  `disconnect()` отдельного ping-client отменяет per-call ctx тестов (#2 — тогда
  делаем сами).

Референс клиента: `app/lib/controllers/home_controller/ping_orchestration.dart`
(`runMassUrltest`, epoch-гейт), `BoxCommandClient.kt:265` (`urlTestOutbound`
синхронный), `cc_channel.dart:177` (`ccUrlTestOutbound` через MethodChannel).

---

# Ответ ядра — вариант #2 работает БЕЗ правок биндинга

**От:** команда ядра sing-box-lx
**К:** LxBox
**Дата проверки:** против `experimental/libbox/command_client.go` + `common/urltest/urltest.go` (HEAD ветки lx-1.14)
**Итог:** ваш блокер снимается вариантом #2 — отдельный ping-`CommandClient` + его `Disconnect()`. Правка биндинга (#1/#3) НЕ нужна для устранения «зомби». Разбор ниже.

## Прямой ответ на главный вопрос

> «Рвёт ли `CommandClient.disconnect()` уже-ушедшие в dial per-call тесты?»

**Да.** Цепочка проверена по коду:

1. `Disconnect()` ([command_client.go:295](../../experimental/libbox/command_client.go)) делает ДВЕ вещи: `c.cancel()` (отменяет общий `c.ctx`) **и** `c.grpcConn.Close()`.
2. После слоя 1 серверный хэндлер привязал тест к gRPC **per-call** `ctx` (`testCtx := ctx`, [started_service_command_lx.go](../../daemon/started_service_command_lx.go)). Обрыв клиентского вызова/транспорта → gRPC-Go рантайм отменяет серверный stream-ctx этого вызова (стандартный `grpc.NewServer`, никакой обёртки, отвязывающей ctx, нет — [daemon/server.go:16](../../daemon/server.go), [command_server.go:164](../../experimental/libbox/command_server.go)).
3. Этот ctx течёт в `urltest.URLTest(testCtx, …)` → в **оба** ctx-aware этапа: `detour.DialContext(ctx, …)` (TCP/proxy connect+handshake) и `client.Do(req.WithContext(ctx))` (HTTP HEAD) — [urltest.go:99,127](../../common/urltest/urltest.go). Отмена ctx обрывает уже-ушедший dial, не дожидаясь `C.TCPTimeout`.

Итог: dial **НЕ** доживает до timeout независимо от disconnect — он падает по отмене ctx. «Зомби» закрываются.

## Но: рвите ОТДЕЛЬНЫЙ ping-client, не общий

Нюанс, который надо учесть. `Disconnect()` через `c.cancel()` отменяет **общий** `c.ctx` ([command_client.go:32-33](../../experimental/libbox/command_client.go)) — один на ВСЕ вызовы этого инстанса. Если дёрнуть `Disconnect()` на вашем основном client'е, оборвутся и Connections/Groups/Status-стримы. Поэтому:

**Держите ОТДЕЛЬНЫЙ `CommandClient`-инстанс под масс-пинг.** Каждый `NewCommandClient` ([command_client.go](../../experimental/libbox/command_client.go)) поднимает СВОЙ `c.ctx`/`c.cancel` и СВОЙ `grpcConn` (`Connect()`/`ConnectWithFD` — [:239](../../experimental/libbox/command_client.go), [:266](../../experimental/libbox/command_client.go)). Значит:

- `pingClient ≠ statusClient/screenClient/profilerClient`;
- `pingClient.disconnect()` отменяет только per-call ctx ping-тестов, рвёт только ping-conn;
- остальные стримы целы.

Это **ровно ваш fallback #2**, и он реализуем на текущем `v1.14.0-lx.1`-биндинге как есть: `urlTestOutbound` уже экспонирован, `disconnect()` уже экспонирован. Новой нативной поверхности не требуется.

## Почему epoch-гейт сам по себе не закрывал зомби (и почему теперь закроет)

Ваш epoch-гейт гасит **применение** результатов на стороне UI, но `urlTestOutbound` — синхронный блокирующий gomobile-вызов, и до слоя 1 серверный тест был привязан к `boxService.ctx` (жил, пока жив сервис) — отменить его было нечем, кроме сноса всего client'а. Теперь, при отдельном ping-client: epoch-бамп (мгновенный UI) **+** `pingClient.disconnect()` (рвёт серверные dial'ы) = и UI, и ядро реагируют. Воркер-пул после disconnect просто получит ошибки на оставшихся вызовах — их и так гасит epoch-гейт.

Практически: при «отмене» зовите `pingClient.disconnect()` и поднимайте свежий `pingClient` под следующий прогон (либо реконнект того же). Стоимость — один short-lived conn на прогон масс-пинга, дёшево.

## Про варианты #1 / #3 (per-call handle / CancelToken)

Не отвергаем, но считаем **избыточными** для вашей задачи:

- #1/#3 дают гранулярность «отменить ОДИН узел из батча, оставив остальные». Для масс-отмены («отменить весь прогон») это не нужно — disconnect ping-client'а гасит весь батч разом.
- Цена #1/#3 — новый stateful слой в gomobile-поверхности (handle-реестр / `CancelToken`-тип, биндинг, версионирование AAR). Это та самая «новая подсистема», которую SPEC 015 §3.6 просит не плодить.
- Если позже появится UX «перепинговать только этот узел с отменой» — вернёмся к #1. Пока YAGNI.

## Что нужно от вас для подтверждения

Проверьте на устройстве сценарий: запустить масс-пинг (concurrency=10) на медленных/недоступных узлах → нажать «отмена» → убедиться, что (а) серверные dial'ы рвутся в пределах ~момента, не висят до `C.TCPTimeout`; (б) Connections/Groups-стримы основного client'а не мигают/не пересоздаются. Если (а) не подтвердится — пришлите лог, копнём транспортный слой конкретного outbound (теоретически отдельный outbound мог бы игнорировать ctx в своём dial — но `urltest.URLTest` зовёт его правильно).

## Сводка ответа

- Вопрос #2 (disconnect рвёт per-call ctx тестов) — **ДА**, проверено по коду. Реализуйте #2 сами.
- Условие: **отдельный** ping-`CommandClient`-инстанс (свой `c.ctx`/`c.cancel`/conn — уже так устроено), чтобы disconnect не задел другие стримы.
- Правки биндинга (#1/#3) — не требуются; держим #1 в запасе под будущий per-node UX.
- Слой 2 разблокирован на текущем биндинге. ✅
