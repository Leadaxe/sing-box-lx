# SPEC: 048 — GVISOR_HANDSHAKE_NIL_CRASH

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — гонка в gvisor, корень доказан крашдампом с устройства |
| Статус | S (spec) — не реализовано |

TCP-соединение, не дошедшее до established, роняет **весь процесс** nil-pointer
паникой: `handleConnecting` вызывает `ep.h.processSegments()` на endpoint'е,
у которого `h` уже занулён закрывающей стороной. Гейт метода проверяет только
состояние endpoint'а, а состояние в этот момент ещё `connecting`.

Фикс — nil-guard на `ep.h` в `handleConnecting`, по образцу уже стоящей там
проверки состояния. Патч в gvisor, то есть третий форк-сабмодуль.

Build-tag: нет (фикс безусловный). Scope: **client-only**.

---

## 1. Проблема

Жалоба (Telegram, 2026-08-03): падение ядра. Крашбандл с устройства,
ядро `1.14.0-lx.19-rc.3`, `go1.26.5, android/arm64`:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x6e9a22279c]

goroutine 287 [running]:
tcp.(*handshake).processSegments(0x0)          ← receiver == nil
    gvisor/pkg/tcpip/transport/tcp/connect.go:534
tcp.handleConnecting(0x6d7422c708)             ← endpoint НЕ nil
    gvisor/pkg/tcpip/transport/tcp/dispatcher.go:152
tcp.(*processor).start(0x6e8db4dbd8, 0x0?)
    gvisor/pkg/tcpip/transport/tcp/dispatcher.go:314
```

Receiver у `processSegments` нулевой, аргумент `handleConnecting` — нет. То есть
`ep != nil`, но `ep.h == nil`; паника — на разыменовании `h.ep.segmentQueue`
(`connect.go:534`).

### 1.1 Корень: окно между `ep.h = nil` и захватом мьютекса в `Close`

`listenContext.performHandshake`, ветка неудачного рукопожатия
(`accept.go:317-323`):

```go
ep.mu.Lock()
if !ep.EndpointState().connected() {
    ep.stack.Stats().TCP.FailedConnectionAttempts.Increment()
    ep.stats.FailedConnectionAttempts.Increment()
    ep.h = nil        // ← handshake занулён
    ep.mu.Unlock()    // ← мьютекс отпущен
    ep.Close()        // ← Close берёт LockUser заново
```

Между `ep.mu.Unlock()` и захватом мьютекса внутри `Close()` endpoint остаётся в
`StateSynSent`/`StateSynRecv`, то есть `EndpointState().connecting() == true`.
В это окно приходит очередной сегмент по тому же соединению, dispatcher будит
`handleConnecting` — тот проходит свой гейт (`dispatcher.go:150`):

```go
if !ep.EndpointState().connecting() {
    ep.mu.Unlock()
    return
}
if err := ep.h.processSegments(); err != nil {   // ← h уже nil
```

Гейт проверяет **состояние**, но не проверяет, что `h` ещё жив. Оба условия
раньше совпадали; после того как зануление `h` было вынесено перед `Close()`,
совпадать перестали.

### 1.2 Обе стороны гонки видны в одном дампе

Горутина 103038 держит **тот же** endpoint `0x6d7422c708`, что и паникующая:

```
goroutine 103038 [sync.Mutex.Lock]:
tcp.(*Endpoint).LockUser(0x6d7422c708)         endpoint.go:689
tcp.(*Endpoint).Close(0x6d7422c708)            endpoint.go:1054
tcp.(*listenContext).performHandshake(...)     accept.go:323
tcp.(*ForwarderRequest).CreateEndpoint(...)    forwarder.go:161
sing-tun.(*gLazyConn).HandshakeContext(...)    stack_gvisor_lazy.go:57
sing-tun.(*gLazyConn).SetReadDeadline(...)     stack_gvisor_lazy.go:137
sing-box/common/sniff.PeekStream(...)          common/sniff/sniff.go:48
route.(*Router).actionSniff(...)               route/route.go:731
route.(*Router).matchRule(...)                 route/route.go:662
protocol/tun.(*Inbound).NewConnectionEx(...)   protocol/tun/inbound.go:564
created by sing-tun.(*TCPForwarder).Forward    stack_gvisor_tcp.go:95
```

Адрес endpoint'а совпадает с аргументом `handleConnecting` в паникующей
горутине 287 — то есть закрывающая сторона стоит ровно на `accept.go:323`
(`ep.Close()`, строка сразу за `ep.h = nil`), пока dispatcher обрабатывает
сегмент того же соединения. Гонка не реконструирована, а снята целиком.

### 1.3 Почему стреляет именно у нас

Баг апстримный и от нашей дельты не зависит, но наш путь расширяет окно:

- **Lazy-режим sing-tun.** `CreateEndpoint` вызывается не в момент SYN, а
  отложенно — из `gLazyConn.SetReadDeadline` (`stack_gvisor_lazy.go:137`),
  то есть уже из роутинг-пайплайна. `performHandshake` блокируется на
  `<-notifyCh` в горутине сниффера, а не в стековой.
- **Сниффинг.** `actionSniff` → `PeekStream` дёргает рукопожатие ради
  дедлайна чтения; путь до `Close()` длиннее на весь стек роутера.
- **Условие срабатывания** — TCP, не доходящий до established (сервер молчит,
  RST, таймаут) при продолжающихся ретрансмитах SYN. Дальше — тайминг.

Обстановка в момент краша по остальному дампу: 336 горутин, 156 в
`chan receive`, активные `LocalDNSTransport.Exchange` и `URLTestGroup.loopCheck`
— шли проверки/переподключения, то есть неудачных дозвонов было много.

### 1.4 Зона поражения

`ep.h` разыменовывается в `dispatcher.go` пять раз:

| Строка | Контекст | Под гейтом состояния |
|---|---|---|
| 105 | `ep.h.listenEP` (`deliverAccepted`) | вызывается из `handleConnecting` после `processSegments` |
| 129 | `ep.h.listenEP.waiterQueue.Notify` | то же |
| 152 | `ep.h.processSegments()` | **краш здесь** |
| 155 | `lEP := ep.h.listenEP` | ветка ошибки того же вызова |
| 165 | `ep.h.listenEP != nil` | после успешного `processSegments` |

Все пять — внутри одного `handleConnecting`, поэтому единственный ранний guard
закрывает их разом; отдельные проверки на каждую строку не нужны.

## 2. Доказательство

- Крашдамп с устройства (`1.14.0-lx.19-rc.3`): `processSegments` с нулевым
  receiver'ом при ненулевом endpoint'е.
- В том же дампе горутина 103038 стоит на `accept.go:323` с тем же адресом
  endpoint'а — обе стороны гонки одновременно.
- Источник зануления единственный: `grep -n "\.h = " pkg/tcpip/transport/tcp/*.go`
  → `accept.go:321`, `accept.go:346`, `connect.go:175`. Первое — ветка провала
  (без синхронного закрытия окна), второе — успех (после него состояние уже
  `connected`, гейт `connecting()` не пускает), третье — создание.
- Гейт `handleConnecting` (`dispatcher.go:150`) проверяет только состояние;
  проверки `ep.h != nil` в файле нет.

## 3. Требования

- Сегмент, пришедший на endpoint с уже занулённым `h`, — тихий no-op под
  разблокированным мьютексом, не паника.
- Guard стоит **до** первого разыменования `ep.h` в `handleConnecting` и
  закрывает все пять точек из таблицы 1.4.
- Поведение при живом `h` не меняется: рукопожатие обрабатывается как сейчас.
- Патч оформлен как форк-сабмодуль gvisor по схеме `wireguard-go`/`sing-tun`
  (§3.4 конституции): `replace` в `go.mod`, дельта — один файл, встречный
  upstream-бамп зависимости на мерже не принимается вслепую.

## 4. Критерии приёмки

- Юнит в форке gvisor: `handleConnecting` на endpoint'е с `h == nil` и
  состоянием `StateSynRecv` не паникует (red/green — на текущем коде падает).
- Гоночный тест под `-race`: forwarder + поток TCP-коннектов на молчащий
  адрес, параллельно закрытие — чисто.
- Стенд: нода-«чёрная дыра» (принимает SYN, не отвечает) + поток коротких
  TCP-коннектов через TUN со включённым сниффингом; до фикса — паника,
  после — соединения штатно отваливаются по таймауту. Схема стенда — как в
  [046](../046-DNS_HIJACK_PACKET_LOOP_STALL/SPEC.md).
- Field: подтверждение от заявителя на сборке с фиксом.

## 5. Границы

- Причина недостижимости узла (DPI silent-drop, мёртвая нода) вне scope:
  ядро обязано переживать любой неудачный дозвон без паники.
- Lazy-режим sing-tun и порядок сниффинга не меняем — они лишь расширяют окно,
  а не создают баг. Отключение lazy окно сузит, но не закроет.
- Issue апстриму (SagerNet/gvisor или google/gvisor) не обещан — политика
  фичи 004 (пассивные условия снятия). Условие снятия: апстрим добавляет
  nil-guard в `handleConnecting` либо закрывает окно на стороне
  `performHandshake` (зануление `h` под тем же удержанием мьютекса, что и
  перевод состояния).
- Подключение gvisor как сабмодуля — новая инфраструктурная работа (сейчас
  зависимость тянется модулем напрямую); цена ребейза оценивается в PLAN.
