# 010 — WG-endpoint без `detour` режет download на Android (GRO split-brain)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — расследование |
| Статус | **C (closed)** — корень подтверждён на железе (probe v2.1: `rxoffload=true`+`dispatch=single`), фикс верифицирован (download 0.44→20.7 Mbps, вровень с контрольной нодой), вмержен в `lx` (submodule `fb8d8d8`). Кандидат №2 не понадобился. |
| Зона | ядро `sing-box-lx` + submodule `wireguard-go` (`conn/`) |

---

## Симптом

WireGuard-**endpoint** на Android без `detour`: download почти мёртв при живом
upload. Тот же конфиг с `"detour": "direct"` на endpoint'е — download нормальный.
Асимметрия (download убит, upload жив) указывает на дефект **только на приёме (RX)**.

---

## Развилка путей — что определяет всё

`transport/wireguard/endpoint.go:200-215`:

```go
wgListener, isWgListener := common.Cast[dialer.WireGuardListener](e.options.Dialer)
if isWgListener {
    bind = conn.NewStdNetBind(wgListener.WireGuardControl())   // ← NO-DETOUR
} else {
    bind = NewClientBind(..., e.options.Dialer, ...)           // ← DETOUR
}
```

- `WireGuardListener` (`common/dialer/wireguard.go`) реализует **только**
  `DefaultDialer` (`common/dialer/default.go:374`).
- `common.Cast` рекурсивна по `Upstream()` (`sing/common/upstream.go:13`).
- **No-detour:** dialer оборачивает `DefaultDialer` → каст успешен → **`StdNetBind`**
  (путь wireguard-go **с** UDP-offload GSO/GRO).
- **Detour:** dialer = `DetourDialer`; его `Upstream()` (`detour.go:82`) возвращает
  резолвнутый `direct *Outbound`, который **не** реализует `WireGuardControl()`/
  `Upstream()` → каст проваливается → **`ClientBind`** (через `direct`, **без**
  offload, пакет-в-пакет).

`detour: direct` **физически меняет реализацию UDP-bind**, а не «добавляет ума тому
же сокету». Единственная функциональная разница на приёме между `StdNetBind` и
`ClientBind` — **GSO/GRO offload**.

---

## Корень — build-tag / runtime-GOOS split-brain в `StdNetBind` на Android

На Android `runtime.GOOS == "android"`, **не** `"linux"`. Отсюда расщепление:

1. **GRO потенциально включается (`rxOffload` зависит от ядра).** Файлы
   `conn/*_linux.go` (`controlfns_linux.go`, `features_linux.go`) компилируются под
   android — имя `*_linux.go` относит их к linux-семейству, **куда входит и
   android**. В `controlfns_linux.go` android-guard'ы (`runtime.GOOS != "android"`,
   строки 40, 47) уже есть, но **только вокруг `IP_PKTINFO`/`IPV6_PKTINFO`** —
   `setsockopt(IPPROTO_UDP, UDP_GRO, 1)` (`controlfns_linux.go:64`) выполняется
   **без** android-guard'а. Затем `supportsUDPOffload` (`features_linux.go:32-36`)
   читает `UDP_GRO` назад: `rxOffload = (opt == 1)`. Взведётся ли `rxOffload` —
   **рантайм-вопрос к ядру android**, а не гарантия кода: если ядро отдаёт
   `opt == 1`, `rxOffload` ставится `true` и ядро начинает коалесить входящие UDP в
   GRO-«суперпакет». Уже существующие PKTINFO-guard'ы в этом же файле — прямое
   свидетельство, что хрупкость android здесь точечно правилась; на `UDP_GRO` этот
   guard пока не распространён.

2. **GRO не разбирается.** Весь приёмный диспетчер в `conn/bind_std.go` гейтится на
   `runtime.GOOS == "linux"` (строки 212, 221, 272, 328, 463) → на android **ложно**.
   `receiveIP` идёт в else-ветку (`bind_std.go:289`) — одиночный `ReadMsgUDP`,
   `numMsgs=1`; `splitCoalescedMessages` **никогда не вызывается**. Настоящий парсер
   `getGSOSize` (`control_linux.go`) имеет тег `//go:build linux && !android` → на
   android берётся stub из `control_default.go`.

3. **Данные рушатся.** Склеенный GRO-блоб (несколько WG-транспорт-пакетов в одном
   recv, до ~64 КБ) трактуется как **один** пакет: `device/receive.go` читает только
   первый заголовок, хвост ломает AEAD → пакет дропается → **download деградирует**.

`ClientBind` (detour-путь) не вызывает `controlFns`/`supportsUDPOffload`, читает по
одной датаграмме (`BatchSize()=1`) — offload-машинерии нет вообще, поэтому путь
иммунен. Это и объясняет «`detour: direct` лечит».

**Детерминирована только мёртвая RX-ветка разбора** (split-путь на android никогда
не зовёт `splitCoalescedMessages`). А вот **активация** offload — нет: `rxOffload`
взводится, лишь если ядро android вернуло `UDP_GRO == 1` (см. п.1), и даже при
взведённом `rxOffload` баг **проявляется**, только когда ядро коалесит в моменте —
нужен плотный входящий поток (download). На редком трафике ядро отдаёт по пакету —
склейки нет — работает. Поэтому «баг в коде» = неразбираемый GRO **при условии**, что
GRO вообще включился; первое детерминировано, второе — рантайм-зависимо и требует
repro (или прямого замера `rxOffload`, см. шаг 0).

---

## Опровергнутые гипотезы (по коду — не повторять)

| # | Гипотеза | Почему отвергнута |
|---|----------|-------------------|
| 1 | MTU / фрагментация | Симптом асимметричный (RX-only); при MTU резало бы симметрично. |
| 2 | У no-detour нет network-strategy / умного выбора интерфейса | Endpoint и direct зовут **один** конструктор `dialer.NewWithOptions → NewDefault`; `networkStrategy` гейтится только на `AutoDetectInterface`/`platformInterface`/`!disableDefaultBind` — одинаково для обоих. На Android оба привязаны к интерфейсу через `ProtectFunc == AutoDetectInterfaceFunc` (`route/network.go:340,368`). Разница не в выборе интерфейса. |
| 3 | Флаг `DirectOutbound` влияет на dialer | `DirectOutbound` — write-only поле; в `NewDefault` не передаётся и нигде не читается. |
| 4 | «Голый `ListenPacket` без стратегии» (`client_bind.go:89`) — корень | Эта ветка — multi-peer / без явного endpoint. Single-peer + валидный endpoint даёт `isConnect=true` (`endpoint.go:208`) → `DialContext`, не `ListenPacket`. А на no-detour `ClientBind` вообще не используется. |

---

## Открытый второй кандидат (если фикс GRO не лечит полностью)

**Тихий хэндовер / смена IP без смены интерфейса.** Re-bind сокета на смене сети идёт
через `onPauseUpdated` → `device.Up()` → `BindUpdate()`. Но событие `NetworkWake`
эмитится только из `notifyInterfaceUpdate`, а мобильный монитор
(`experimental/libbox/monitor.go:95-98`) дедуплицирует по **Name+Index, игнорируя
Addresses**. Смена source-IP на том же интерфейсе → событие подавляется → сокет не
переоткрывается. Не путь `StdNetBind`-vs-`ClientBind`, но самостоятельный сетевой
кандидат — держать открытым.

> Замечание против самой GRO-версии, которое надо снять repro: если баг чисто в
> GOOS-логике, он должен бить download и на стабильной сети, не только на сотовой.
> Если на стабильной сети download жив — либо GRO коалесит по-разному на разных
> интерфейсах, либо GRO не единственная причина (тогда вес смещается к кандидату №2).

---

## План проверки (эксперимент, не релиз)

Один дискриминирующий замер, изолирующий именно RX:

0. **(Бесплатно, без сборки ядра) Сначала снять факт `rxOffload`.** Залогировать
   фактический возврат `supportsUDPOffload(conn)` → `(txOffload, rxOffload)` на
   целевом Android. Это дискриминирует всю GRO-гипотезу до любого патча:
   - `rxOffload == false` → GRO на этом ядре не активируется, корень №1 **мёртв** без
     repro; вес немедленно уходит на кандидат №2 (тихий хэндовер).
   - `rxOffload == true` → GRO взведён, переходим к шагу 1 (изолировать именно RX).

1. Запатчить `conn/controlfns_linux.go`: пропускать `setsockopt(UDP_GRO)` при
   `runtime.GOOS == "android"` (TX/GSO **не трогать**, чтобы `txOffload` оставался
   `true` и эксперимент проверял только RX). Тогда `rxOffload` читается `false` и
   приём идёт обычным путём.
2. Собрать ядро и воспроизвести no-detour WG-endpoint на **любом** Android
   (эмулятор/устройство — баг детерминирован в коде, оператор не нужен), снять
   download под плотным потоком.
3. Желательно — пакетная проверка: реально ли `recvmsg` отдаёт >MTU датаграммы на
   этом сокете (подтверждает, что GRO коалесит).

Исходы:
- download починился при живом upload → корень = GRO-на-android **подтверждён**, и
  минимальный фикс найден тем же шагом;
- не починился → переходим к кандидату №2 (тихий хэндовер).

---

## Решение

Пока **нет** — это таска-расследование. Код в ядро/submodule — только после repro.

**Кандидат на фикс (когда подтверждён):** гейтить `UDP_GRO`-setsockopt
(`controlfns_linux.go`) и/или чтение `rxOffload` (`features_linux.go`) за `!android`,
чтобы `StdNetBind` на android не объявлял offload, который не умеет разбирать. Это
откатывает android на «без offload» — поведение, идентичное рабочему detour-пути.

> NB: фикс «добавить RX self-disable в linux-ветку» был бы **no-op** — эта ветка на
> android мёртвый код. Корень = split-brain GOOS, а не отсутствие fallback.

---

## Acceptance (для будущего фикса)

- [ ] No-detour WG-endpoint на Android даёт download, сопоставимый с `detour: direct`.
- [ ] Фикс не ломает offload/производительность на «настоящем» Linux (не-android) —
      гейт за `!android`, а не глобальное отключение.
- [ ] Регресс: plain WG **и** AmneziaWG endpoint; single-peer (`isConnect`) и
      multi-peer (`ListenPacket`) пути.
- [ ] Юнит/интеграционный тест на coalesced-receive (сейчас отсутствует — поэтому
      дефект и проскочил).

---

## Источники (проверено по живому коду)

- `transport/wireguard/endpoint.go:200-215` — развилка StdNetBind vs ClientBind.
- `common/dialer/wireguard.go`, `default.go:374` — `WireGuardListener` / `WireGuardControl`.
- `common/dialer/detour.go:82` — `DetourDialer.Upstream()` → direct.
- `sing/common/upstream.go:13` — `common.Cast` рекурсия по `Upstream()`.
- `transport/wireguard/client_bind.go:53-99` — `isConnect` Dial vs Listen, кэш `c.conn`.
- `conn/controlfns_linux.go:40,47` — PKTINFO под `runtime.GOOS != "android"` guard;
  `controlfns_linux.go:64` — `setsockopt(UDP_GRO,1)` **без** android-guard'а.
- `conn/features_linux.go:32-36` — `supportsUDPOffload` читает `UDP_GRO` назад,
  `rxOffload = (opt == 1)` (рантайм-зависимо от ядра).
- `conn/bind_std.go:212,221,272,328,463` — диспетчер offload под `runtime.GOOS=="linux"`.
- `conn/control_linux.go` (`linux && !android`) vs `control_default.go` (stub на android).
- `device/receive.go` — coalesced-блоб трактуется как один пакет.
- `route/network.go:340,368` — `ProtectFunc == AutoDetectInterfaceFunc` на android.
- `experimental/libbox/monitor.go:95-98` — моб. монитор дедупит по Name+Index.
