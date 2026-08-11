# SPEC 060 — авто-фрагментация ClientHello у TLS-outbound'ов под `detour`

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — связка «TLS-outbound + `detour`» молча не поднимается на части нижних плеч |
| Статус | C (complete) — сделано 2026-08-12; матрица 38/38 на живых узлах (связность + 5 МБ), см. [TEST_PLAN.md](TEST_PLAN.md) §RESULTS |
| Зона | общий TLS-слой (`common/tls`) + признак «диалим через detour» |
| Build-tag | — (встроено в ядро) |

## 0. TL;DR

Если outbound с TLS-over-TCP (VLESS, trojan, vmess, anytls, masque h2, …) ходит через
`detour`, его **ClientHello может молча теряться**: нижнее плечо пересылает наш хендшейк
от своего имени, а PMTU на участке «detour-сервер → цель» ниже размера ClientHello.
ICMP `Fragmentation Needed` до клиента не доходит ⇒ пакет исчезает, наружу это выглядит
как `tls handshake: EOF` через 12–17 с.

Лечится фрагментацией первой TLS-записи (`record_fragment`) — механизм в ядре **уже есть**.
Задача — включать его **автоматически при `detour`**, чтобы пользователь не обязан был
знать про дыру и помнить, где ставить флаг.

---

## 1. Причина (доказано замерами)

Порог по размеру ClientHello, живые VLESS-узлы, цель — WARP endpoint `162.159.199.118:8443`:

| ClientHello | FI / SE узлы | GB / FR узлы |
|---|---|---|
| 1488 B | OK | OK |
| 1502 B и выше | **висит → EOF** | OK |

- Порог ≈1490 B — свойство **пути за чужим сервером**, не протокола и не SNI.
  Длина `sni` влияет лишь потому, что двигает размер ClientHello через порог.
- Прямой путь (без detour) с тем же ClientHello работает: наш собственный аплинк держит 1500.
- Воспроизводится **голым `curl` / Go-клиентом без sing-box** ⇒ причина вне ядра;
  ядро лишь не даёт средства обойти.
- Бьёт не только по masque: связка `VLESS detour VLESS` через тот же узел падает так же
  (`EOF` на хендшейке верхнего REALITY).

### Почему это не лечится MTU-настройками

| ограничитель | почему не достаёт |
|---|---|
| `mtu` у masque/WG-outbound | режет IP-пакеты **внутри** уже поднятого туннеля; ClientHello идёт ДО туннеля |
| `tun.mtu` | ограничивает вход из TUN; ClientHello рождается в outbound после роутинга и через TUN не проходит |
| route-action `tls_fragment` | применяется в `route/conn.go` к соединениям через маршрутизатор; внутренний dial outbound→detour туда не попадает |
| снять DF / разрешить фрагментацию | DF ставит **чужой** detour-сервер; для TCP IP-фрагментация вредна (правильный механизм — MSS) |

Единственный рычаг на нашей стороне — фрагментация первой TLS-записи.

## 2. Что фрагментировать (и что НЕ надо)

**Только хендшейк.** После него дыра не мешает: TCP-поток режется ядром по MSS, и данные
любого размера проходят (замер: 64 КБ прикладных данных через сломанное плечо — мгновенно).
Поэтому поведение `tf.NewConn` — резать только первую запись с SNI — ровно то, что нужно;
постоянного налога на трафик нет.

Замер эффекта через сломанное плечо:

| режим | результат |
|---|---|
| без фрагментации | ❌ FAIL, 12 с |
| `fragment` (packet-split) | ✅ OK, 0.6 с |
| `record_fragment` | ✅ **OK, 0.1 с** |

⇒ дефолт — `record_fragment` (быстрее: не ждёт ACK между кусками, что через detour всё равно
недоступно — `writeAndWaitAck` требует `*net.TCPConn`, а conn нижнего плеча им не является).

## 3. Решение

**Включать `record_fragment` по умолчанию, когда outbound диалит через `detour`.**

- Явное значение в конфиге **всегда сильнее** авто (и `true`, и `false`).
- Применяется единообразно ко всем TLS-outbound'ам через общий слой — не точечно
  в каждом протоколе, иначе вернётся та же проблема «надо помнить, где включать».
- `h3`/QUIC-транспорты не затрагиваются: там нет TLS-over-TCP, а quic-go держит
  `InitialPacketSize` ниже порога и делает PLPMTUD (замер: masque h3 через detour — 4/4 OK).

### Как реализовано

Одна точка на весь форк — `NewClientWithOptions` в `common/tls/client.go`, до диспетчеризации
по движкам, поэтому STD, uTLS и REALITY получают одинаковый дефолт:

```go
func applyDetourFragmentDefault(options *ClientOptions) {
    if !options.DialedThroughDetour { return }
    if options.Options.Fragment || options.Options.RecordFragment { return }
    options.Options.RecordFragment = true
}
```

Признак detour вычисляется единственной функцией `tls.DialedThroughDetour(DialerOptions)` —
протоколы не проверяют `Detour != ""` сами. Каждый TLS-outbound передаёт результат в
`ClientOptions.DialedThroughDetour`.

**Решения по вопросам, которые были открыты в драфте:**

1. **Признак** — `option.DialerOptions.Detour != ""`, вычисляется в `common/tls`, передаётся
   явным полем `ClientOptions`. По протоколам не размазано (одна строка на outbound).
2. **`default_outbound`** — НЕ покрыт, и это не пробел: флаг `dialer.Options.DefaultOutbound`
   выставляется **единственным** местом в ядре — `common/httpclient` (внутренний HTTP-клиент
   для rule-set'ов/подписок, см. `box.go`). У прокси-outbound'ов (vless/trojan/masque/…)
   этого пути нет: их «через чужой сервер» всегда приходит из конфига как `detour`.
   Протаскивать флаг из диалера ради внутреннего httpclient не потребовалось.
   Вложенные цепочки покрыты автоматически: у каждого звена свой `detour`.
3. **`*bool` не понадобился.** `OutboundTLSOptions` — upstream-структура; смена типа поля
   расширила бы зону ребейза. Вместо этого «пользователь сделал выбор» = любой из
   `fragment`/`record_fragment` уже `true`.
   ⚠️ **Известный компромисс:** явный `"record_fragment": false` под detour неотличим от
   «не задано», поэтому авто всё равно включится. Отключить фрагментацию под detour можно
   только выбрав другой режим (`"fragment": true`). Практического способа «detour без
   всякой фрагментации» сейчас нет — если понадобится, потребуется `*bool` и переоценка
   стоимости ребейза.
4. **`fragment: true` не апгрейдится.** Если пользователь выбрал packet-split, авто не
   добавляет сверху record-split — иначе молча меняется выбранный режим.
5. **DPI-побочка** — принята владельцем: фрагментированный ClientHello сам по себе
   сигнатура, но цена (0.1 с на хендшейк, только под detour) признана приемлемой против
   «связка не работает вообще».

## 3a. Тронутые файлы (зона ребейза)

| Файл | Что |
|---|---|
| `common/tls/client.go` | `ClientOptions.DialedThroughDetour`, `DialedThroughDetour()`, `applyDetourFragmentDefault()`, вызов в `NewClientWithOptions`, `NewDialerFromClientOptions()` |
| `protocol/vless/outbound.go` | +1 поле в `ClientOptions` |
| `protocol/trojan/outbound.go` | +1 поле |
| `protocol/vmess/outbound.go` | `NewClient` → `NewClientWithOptions` (+ поле) |
| `protocol/anytls/outbound.go` | `NewClient` → `NewClientWithOptions` (+ поле) |
| `protocol/shadowtls/outbound.go` | `NewClient` → `NewClientWithOptions` (+ поле) |
| `protocol/http/outbound.go` | `NewDialerFromOptions` → `NewDialerFromClientOptions` (+ поле) |
| `protocol/masque/outbound.go` | +1 поле (h2-путь, поверх SPEC 021 Ф4) |

Итого 8 upstream-файлов, из них 7 — по одной строке-полю. Существующие сигнатуры
`NewClient`/`NewDialerFromOptions` сохранены (новый вариант добавлен рядом), поэтому чужие
вызовы не тронуты.

Тесты (lx-owned): `common/tls/detour_fragment_lx_test.go`.

## 4. Что НЕ в scope

- **masque h2 вообще не имеет полей `fragment`/`record_fragment`** — это чинит
  [SPEC 021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) Ф4 (перевод на общий `common/tls`).
  SPEC 060 меняет только **дефолт**, и рассчитывает, что поля уже есть.
- Изменение поведения без `detour` — прямой путь не трогаем.
- QUIC/h3-транспорты (см. §3).
- Починка самой PMTU-дыры — она в чужой сети.

## 5. Условие снятия (обязательно для HOTFIXES)

Снять, если апстрим сам начнёт включать фрагментацию под detour либо появится штатный
PMTU-aware путь для вложенных диалов. Пока этого нет — держим: без фикса связка
«TLS-outbound + detour» на части узлов не поднимается вообще.

## 6. Тест-план

См. [TEST_PLAN.md](TEST_PLAN.md).
