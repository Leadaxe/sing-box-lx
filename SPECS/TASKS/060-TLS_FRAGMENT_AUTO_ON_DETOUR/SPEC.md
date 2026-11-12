# SPEC 060 — авто-фрагментация ClientHello у TLS-outbound'ов под `detour`

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — связка «TLS-outbound + `detour`» молча не поднимается на части релеев |
| Статус | C (complete) — сделано 2026-08-12; матрица 38/38 на живых узлах, см. [TEST_PLAN.md](TEST_PLAN.md) §RESULTS. Причина переписана 2026-11-12: сниффинг ClientHello на релее, а не PMTU — см. [HISTORY.md](HISTORY.md) |
| Зона | общий TLS-слой (`common/tls`) + признак «диалим через detour» |
| Build-tag | — (встроено в ядро) |

## 0. TL;DR

Если outbound с TLS-over-TCP (VLESS, trojan, vmess, anytls, masque h2, …) ходит через
`detour`, релей может **прочитать SNI из нашего ClientHello и увести соединение по этому имени**
(Xray `sniffing.destOverride` без `routeOnly`). Цель, у которой SNI — маскировка (WARP с чужим
именем, REALITY), получает соединение не туда: хендшейк висит и падает `tls handshake: EOF`
через 12–17 с либо приходит чужой ключ.

Разрез ClientHello на несколько TLS-записей (`record_fragment`) снифферу не по зубам — он
сдаётся, назначение остаётся нашим. Задача — включать это **автоматически при `detour`**, чтобы
пользователь не обязан был знать про сниффинг на чужом релее.

Это TLS-сторона того же класса, что [SPEC 151](../151-QUIC_SNIFF_DECOY/SPEC.md) для QUIC
(байт-приманка перед Initial): обе — анти-сниффинг на релее.

---

## 1. Причина

### 1.1 Механизм (установлен на стенде [SPEC 150 §3.1](../150-QUIC_DETOUR_OVERSIZE_DATAGRAM/SPEC.md))

Xray-инбаунд с `sniffing.destOverride` и `routeOnly: false` читает SNI из ClientHello, заменяет
им назначение и резолвит заново. Резолвящееся имя уводит соединение на хост этого имени и его
ключ; нерезолвящееся — сброс. На собственном Xray владельца (2026-11-02):

| masque h2 через сниффящий Xray | результат |
|---|---|
| целый ClientHello | чужой ключ |
| `record_fragment: true` | туннель поднят, 3/3 |

По коду Xray (`app/dispatcher/default.go`, `common/protocol/tls/sniff.go`): у цикла сниффинга
бюджет 200 мс; на неполный ClientHello TLS-сниффер отвечает `ErrNoClue`, это считается
попыткой, после двух цикл сдаётся. ClientHello, разрезанный на TLS-записи, не разбирается
никогда — назначение остаётся нашим.

### 1.2 Факты замеров 060 (2026-08-12) под этим механизмом

Цель — WARP `162.159.199.118:8443`, `network: h2`, `sni: www.google.com`, плечи — живые VLESS.

| факт | как объясняется |
|---|---|
| через FI/SE висит → EOF 12–17 с, через GB/FR проходит | сниффинг — настройка конкретного релея, не протокола (какие релеи сниффили, тогда не проверялось) |
| `sni: www.google.com` — резолвящееся имя | назначение уходит на `www.google.com:8443`, там никто не отвечает |
| без detour тот же ClientHello проходит | на прямом пути релея нет |
| `VLESS(DE-2) detour VLESS(FI)` — EOF | SNI REALITY — реальный сайт; уведённое соединение REALITY не проходит |
| `record_fragment` ✅ 0.1 с, `fragment` ✅ 0.6 с | оба режут ClientHello на неполные куски для сниффера |
| прикладные записи до 64 КБ после хендшейка — мгновенно | сниффер смотрит только начало потока |
| masque h3 через FI — 4/4 | согласуется, если у FI в `destOverride` не было `quic` (не проверено); h3 через релей со сниффингом `quic` ловится — SPEC 150/151 |

**Не объяснено:** порог по размеру — ClientHello ~1488 B проходил через «сломанное» плечо,
~1502–1511 B нет (варьировалось длиной `sni`/паддингом). Если тот замер шёл с резолвящимся
именем, сниффинг его не объясняет. Перепроверка на тех же узлах невозможна (не наши серверы);
открытый факт.

### 1.3 Почему не лечится настройками пути

| ручка | почему не достаёт |
|---|---|
| `mtu` у masque/WG-outbound | режет пакеты **внутри** поднятого туннеля; ClientHello идёт до него |
| `tun.mtu` | ограничивает вход из TUN; ClientHello рождается в outbound после роутинга |
| route-action `tls_fragment` | применяется в `route/conn.go` к соединениям через маршрутизатор; внутренний дайл outbound→detour туда не попадает |

На стороне релея лечится `"routeOnly": true` в `sniffing` инбаунда — но релей чужой.

## 2. Что фрагментировать

**Только хендшейк.** Сниффер смотрит начало потока; `tf.NewConn` режет только первую запись с
SNI — постоянного налога на трафик нет.

| режим через сниффящее плечо | результат |
|---|---|
| без фрагментации | ❌ FAIL, 12 с |
| `fragment` (TCP-сплит) | ✅ OK, 0.6 с |
| `record_fragment` | ✅ **OK, 0.1 с** |

⇒ дефолт — `record_fragment`: разрез на записи сохраняется в байтах потока и доходит до релея
как есть. `fragment` через detour не может ждать ACK (`writeAndWaitAck` требует
`*net.TCPConn`), держится на паузе `fragment_fallback_delay`, а сегментацию к цели делает сокет
релея.

## 3. Решение

**Включать `record_fragment` по умолчанию, когда outbound диалит через `detour`.**

- Явное значение в конфиге **всегда сильнее** авто: `fragment: true` или `record_fragment` с
  любым значением. `record_fragment` — `*bool` с
  [SPEC 156](../156-RECORD_FRAGMENT_EXPLICIT_FALSE/SPEC.md): явный `false` выключает дефолт
  (сеть, где DPI дропает разрезанный ClientHello, LxBox#215).
- Применяется единообразно ко всем TLS-outbound'ам через общий слой.
- `h3`/QUIC не затрагивается: там нет TLS-over-TCP. Сниффинг QUIC — отдельный механизм,
  [SPEC 151](../151-QUIC_SNIFF_DECOY/SPEC.md).

### Как реализовано

Одна точка на весь форк — `NewClientWithOptions` в `common/tls/client.go`, до диспетчеризации
по движкам, поэтому STD, uTLS и REALITY получают одинаковый дефолт:

```go
func applyDetourFragmentDefault(options *ClientOptions) {
    if !options.DialedThroughDetour { return }
    if options.Options.Fragment || options.Options.RecordFragment != nil { return }
    recordFragment := true // SPEC 156: the field is *bool
    options.Options.RecordFragment = &recordFragment
}
```

REALITY: дефолт доходит до его ClientHello с [SPEC 088](../088-REALITY_FRAGMENT_BYPASS/SPEC.md)
(до неё REALITY строил `utls.UClient` на голом соединении мимо `tlsfragment`, и матрица 060
REALITY-узлы как фрагментированные не покрывала).

Признак detour — единственная функция `tls.DialedThroughDetour(DialerOptions)`:
`Detour != ""` в конфиге. Каждый TLS-outbound передаёт результат в
`ClientOptions.DialedThroughDetour`. Куда detour ведёт в рантайме (группа с выбранным
`direct`), признак не видит, хотя на прямом пути релея нет и разрез там не нужен —
открытый вопрос.

**Решения:**

1. **Признак** — из конфига, вычисляется в `common/tls`, передаётся явным полем
   `ClientOptions`.
2. **`default_outbound`** не покрыт и не нужен: `dialer.Options.DefaultOutbound` выставляет
   только `common/httpclient` (внутренний клиент rule-set'ов/подписок). Вложенные цепочки
   покрыты: у каждого звена свой `detour`.
3. **«Пользователь сделал выбор»** = `fragment: true` либо `record_fragment` задан явно с любым
   значением.
4. **`fragment: true` не апгрейдится** — иначе молча меняется выбранный режим.
5. **DPI-побочка** — принята владельцем: разрезанный ClientHello сам по себе сигнатура. Цена
   видна в LxBox#215; отказ — явный `"record_fragment": false`.

## 3a. Тронутые файлы (зона мержа)

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

Тесты (lx-owned): `common/tls/detour_fragment_lx_test.go`.

## 4. Что НЕ в scope

- Изменение поведения без `detour`.
- QUIC/h3 — [SPEC 151](../151-QUIC_SNIFF_DECOY/SPEC.md).
- Настройка чужого релея (`routeOnly`).

## 5. Условие снятия (обязательно для HOTFIXES)

Снять, если апстрим сам начнёт резать ClientHello под detour, либо если сниффинг с подменой
назначения уйдёт из распространённых релеев (Xray сменит дефолт `routeOnly`).

## 6. Тест-план

См. [TEST_PLAN.md](TEST_PLAN.md) — записи прогона 2026-08-12 используют язык PMTU-гипотезы
(«сломанное плечо», «PMTU-дыра»); факты в нём верны, толкование — в §1.2.
