# SPEC: 027 — UTLS_OVER_QUIC

| Поле | Значение |
|------|----------|
| Тип | Q (question/исследование) |
| Статус | C (complete) |

**Аудит без реализации.** Фиксирует, ПОЧЕМУ uTLS-фингерпринт ClientHello
(`tls.utls.fingerprint`, `fp=chrome`) недостижим поверх QUIC-протоколов
(`hysteria2`, `tuic`) на текущих зависимостях форка. Реализация НЕ входит в
скоуп этой спеки — это причинно-следственный разбор блокера и обоснование того,
почему настоящий фикс отложен.

**Симптом (пользовательский).** Outbound `hysteria2` или `tuic` с
`tls.utls.enabled: true` успешно проходит `NewOutbound` при старте — конфиг
валиден, нода в списке «живая» — но не устанавливает **ни одного** соединения:
QUIC-хендшейк падает с `unsupported usage for uTLS`. Не fatal для всего конфига,
поэтому валидатором не ловится. Прилетает массово из публичных подписок под
Xray, где `fp=chrome` вешают на все ноды без разбора, включая hy2/tuic.

**Версии зависимостей на момент аудита** (`go list -m`):

| Модуль | Версия |
|--------|--------|
| `github.com/metacubex/utls` | `v1.8.4` |
| `github.com/sagernet/quic-go` | `v0.59.0-sing-box-mod.4` |
| `github.com/sagernet/sing-quic` | `v0.6.4-0.20260709034545-e23afe1172dc` |

Все строки-якоря ниже проверены построчно по этим версиям (module cache) и по
исходникам форка. Метод: ручной `grep`+`read` + adversarial-верификация
многоагентным workflow (10 агентов, 0 ошибок). Вердикты совпали.

---

## 1. Механизм отказа: путь выбора TLS для QUIC

**Выбор клиентского TLS-конфига** — `common/tls/client.go:103-108`
(`NewClientWithOptions`): при `options.UTLS.Enabled` создаётся
`UTLSClientConfig` (`newUTLSClient`). Это происходит **успешно** — uTLS отдаёт
валидный конфиг-объект, поэтому `NewOutbound` не падает.

**Точки построения TLS в QUIC-протоколах** (одно вхождение каждое, номера точны):

- `protocol/hysteria2/outbound.go:57` — `tls.NewClient(...)` → сохраняется в
  `hysteria2.ClientOptions.TLSConfig` (`:141`).
- `protocol/tuic/outbound.go:47` — `tls.NewClient(...)` → в
  `tuic.ClientOptions.TLSConfig` (`:72`).

Обе точки только **хранят** конфиг — ошибки здесь нет. Падение — позже, на dial.

**QUIC-путь в `sing-quic`** — `qtls/quic.go` (пакет объявлен как `package qtls`,
`quic.go:1`), функции `Dial`/`DialEarly`/`CreateTransport`. Структура каждой:

```go
if quicTLSConfig, isQUICConfig := config.(Config); isQUICConfig {
    // нативный QUIC-путь — вызывает config.Dial / .CreateTransport
    return quicTLSConfig.Dial(...)
}
tlsConfig, err := config.STDConfig()   // фолбэк
if err != nil { return nil, err }
quic.Dial(ctx, conn, addr, tlsConfig, quicConfig)
```

Приведение записано `config.(Config)` на `sing-quic/quic.go:92` (`Dial`), `:106`
(`DialEarly`), `:121` (`CreateTransport`). `Config` — интерфейс из
`sing-quic/quic.go:26` с методами `Dial` / `DialEarly` / `CreateTransport`.

**Ветка `isQUICConfig` — мёртвая для всех клиентских TLS-конфигов форка.**
Базовый интерфейс `aTLS.Config` (`sing/common/tls/config.go`) НЕ содержит методов
`Dial`/`DialEarly`/`CreateTransport`, поэтому его реализаторы структурно не могут
удовлетворить `qtls.Config`. Поиск метода-ресивера `CreateTransport` по всему
module cache — пусто. Ни один конфиг в `common/tls/` не реализует `qtls.Config`:
ни `STDClientConfig`, ни `UTLSClientConfig`, ни `RealityClientConfig`. Значит QUIC
у всех идёт через фолбэк `config.STDConfig()`:

| Конфиг | `STDConfig()` | Итог поверх QUIC |
|--------|---------------|------------------|
| `STDClientConfig` | валидный `*crypto/tls.Config` | **работает** |
| `UTLSClientConfig` | `unsupported usage for uTLS` (`utls_client.go:82`) | **падает на dial** |
| `RealityClientConfig` | `unsupported usage for reality` (`reality_client.go:127`) | падает на dial |

То есть **plain-TLS поверх QUIC работает, а uTLS — нет.** `STDConfig()` честно
возвращает ошибку, а не заглушку: весь смысл uTLS — подменить ClientHello своим
(browser-parrot) вместо стандартного, а `http3.Transport` / `quic.DialEarly`
требуют именно `*crypto/tls.Config`, который такую подмену выразить не может.
Отдать туда plain-`*tls.Config` = молча потерять фингерпринт (нода коннектится
под дефолтным Go-ClientHello) — для anti-DPI это **хуже** явного отказа.

Reality в этом ядре страдает идентично (`STDConfig()` тоже ошибка) — reality
поверх QUIC в форке тоже не работает; escape-hatch `config.(qtls.Config)` — код
без единого имплементора.

---

## 2. Почему форка `sing-quic` / протоколов недостаточно: `quic-go` прибит к `crypto/tls`

Даже если бы `UTLSClientConfig` реализовал `qtls.Config`, каждый его метод обязан
вернуть `*quic.Conn`, а единственный минтёр — `quic.Dial`/`DialEarly`, которые
внутри жёстко конструируют стандартный TLS:

- `sagernet/quic-go` `internal/handshake/crypto_setup.go:30` —
  `conn *tls.QUICConn` (конкретный тип `crypto/tls`, не интерфейс).
- `crypto_setup.go:95` — `cs.conn = tls.QUICClient(&tls.QUICConfig{...})`,
  единственный клиентский конструктор (серверный `tls.QUICServer` — `:130`).
- Публичный API quic-go (`Dial`, `DialEarly`) принимает только
  `*crypto/tls.Config`; ни `quic.Config`, ни `quic.Transport` не имеют поля/хука
  для подмены ClientHello. Grep по модулю на `utls|ClientHelloSpec|fingerprint` —
  ноль совпадений. Хук внедрения отсутствует.

**Вывод уровня 2:** реализация `qtls.Config` в `common/tls` — необходима, но НЕ
достаточна. Настоящая маскировка требует **форка `sagernet/quic-go`**:
абстрагировать `conn` за интерфейсом, чтобы туда можно было подставить
QUIC-стейт-машину uTLS (`UQUICConn`). В форке уже есть паттерн такого carry —
`replace github.com/sagernet/wireguard-go => ./submodules/wireguard-go`
(`go.mod`, дисциплина AmneziaWG).

---

## 3. Настоящий блокер глубже: `metacubex/utls@v1.8.4` не умеет QUIC-фингерпринт

Форк одного `quic-go` дал бы **бракованный** фикс. Стена — на стороне самого uTLS.

**Уровень 3a — любой браузерный fingerprint идёт через preset-путь, где
transport-parameters НЕ пишутся.**

- `metacubex/utls` `u_conn.go:109` — бинарная развилка
  `if uconn.ClientHelloID == HelloGolang`. Всё, кроме `HelloGolang`, падает в
  `else` → `applyPresetByID(uconn.ClientHelloID)` (`u_conn.go:129`) → `ApplyPreset`
  → `makeClientHelloForApplyPreset`. Т.е. **любой именованный fingerprint
  (`chrome`, `firefox`, …) — это preset-путь.** Единственный путь без preset —
  `HelloGolang`, то есть стандартный Go-хелло (фичи нет).
- В preset-пути инъекция QUIC transport-parameters **закомментирована самими
  авторами utls**: `u_handshake_client.go:324` — заголовок
  `// [UTLS] We don't need this, since it is not ready yet`;
  `u_handshake_client.go:333` — `// \thello.quicTransportParameters = p`
  (присваивание под `//`).
- Единственное **активное** присваивание `hello.quicTransportParameters = p` —
  `handshake_client.go:210`, внутри `makeClientHello` (`u_conn.go:116`,
  Golang-путь; а также `u_conn.go:569` — ECH-inner в `MarshalClientHello`).
  Preset-путь его не достигает.
- Ни один именованный preset не несёт `QUICTransportParametersExtension`: в
  `u_parrots.go`/`u_common.go` — **ноль** инстансов (тип и конструктор живут
  только в `u_tls_extensions.go`, применение в `u_quic.go:171` закомментировано).

**Уровень 3b — даже ручной preset-параметр несёт хардкодный SCID.** Машинерия
preset transport-params (`u_quic_transport_parameters.go`) строит параметры из
фиксированных значений (тестовый вектор:
`InitialSourceConnectionID([]byte{0x53,0xf0,0xb2})`). quic-go генерит свой SCID
per-connection. RFC 9000 §7.3 требует, чтобы `initial_source_connection_id` в
TLS-параметрах совпадал с SCID из Initial-пакета — рассинхрон = fatal
`TRANSPORT_PARAMETER_ERROR`. Скормить quic-go-шный runtime-SCID в preset-хелло
негде.

**Обязательность по RFC.** RFC 9001 §8.2: «Endpoints **MUST** send the
`quic_transport_parameters` extension» — ClientHello явно назван носителем;
получатель без расширения ОБЯЗАН закрыть соединение (`missing_extension`).

**Итог:** на `metacubex/utls@v1.8.4` нет пути, дающего одновременно
(а) браузерный fingerprint и (б) валидные per-connection QUIC transport
parameters. Либо одно, либо другое. Браузерно-фингерпринтованный QUIC-хелло из
такого форка пришёл бы на сервер без обязательного расширения → отвергнут. Тот же
мёртвый исход, что и сейчас, только с форком и лишним control-plane сверху.

Именно поэтому экосистема (`refraction-networking/uquic`) форкает **и** utls,
**и** пакетный слой QUIC — переписывает preset-builder под инъекцию живых
transport-params и Initial-пакетный слой.

---

## 4. Прецедент: Xray-core эту задачу тоже не решает

Проверено по исходникам `XTLS/Xray-core` (web + raw sources):

- **TUIC** в Xray-core **отсутствует** — это протокол sing-box. `fp=chrome` на
  tuic-ноде в «xray-подписке» — параметр, который сам Xray никогда не исполняет.
- **Hysteria2** добавлен нативно (начало 2026, v26.3.27, PR #5679) — но uTLS к его
  QUIC-хендшейку **не применяется** (`transport/internet/hysteria/dialer.go`:
  плоский `crypto/tls` + `apernet/quic-go`; grep `utls|fingerprint|UClient` —
  пусто).
- uTLS в Xray (`transport/internet/tls/tls.go` `UClient`) принимает только
  `net.Conn` (TCP). XHTTP-over-h3 отдаёт в quic-go стандартный `*tls.Config`;
  ветка фингерпринта туда не заходит. Старый отдельный `quic`-транспорт выпилен в
  v24.9.7.
- Anti-censorship-история Xray для QUIC — **другая** (XHTTP-под-CDN, Hysteria2
  Salamander/finalmask), НЕ подделка браузерного ClientHello. Для настоящей
  браузерной маскировки у Xray — out-of-process Browser Dialer, и тоже TCP-only.

Никто в экосистеме не форкнул `quic-go`+`utls` ради uTLS-over-QUIC — овчинка не
стоит выделки.

---

## 5. Вывод и статус

**uTLS-фингерпринт поверх QUIC на текущих зависимостях недостижим.** Тройная
стена, каждая построчно подтверждена:

1. Ни один клиентский TLS-конфиг форка не реализует `qtls.Config` → QUIC идёт
   через `STDConfig()`, а `UTLSClientConfig.STDConfig()` — ошибка (§1).
2. Даже с реализацией `qtls.Config` `sagernet/quic-go` прибит к `crypto/tls`
   без хука внедрения ClientHello → нужен форк quic-go (§2).
3. **Настоящий блокер:** `metacubex/utls@v1.8.4` не пишет обязательный по
   RFC 9001 §8.2 `quic_transport_parameters` на preset-пути (= на любом браузерном
   fingerprint) — авторы отключили его как «not ready yet» (§3).

**Настоящий фикс** (uTLS-over-QUIC «по-честному») = форк ДВУХ чужих модулей
(`quic-go` + `utls`, либо переход на `refraction-networking/uquic`) + работа с
Initial-пакетным слоем. Материально больше, чем «подменить один конструктор»,
меняет dependency-story, и его не осилил даже Xray. **Отложено** — не в скоупе
этой спеки.

**Рекомендация по обращению с симптомом** (тоже вне реализации этой Q-спеки, как
отдельная мелкая задача): near-term fail-fast — превратить опаковую dial-time
ошибку в внятный отказ на `NewOutbound` (`hysteria2/outbound.go:57`,
`tuic/outbound.go:47`) для `utls.enabled`/`reality.enabled` поверх QUIC; на
стороне LxBox — прекратить эмиссию `fp` для hy2/tuic (каждый такой конфиг сегодня
— гарантированный fail). Молчаливый фолбэк на std-TLS — запрещён: тихо отправить
Go-хелло вместо запрошенного Chrome = регрессия безопасности для
anti-censorship-инструмента.

---

## Приложение: реестр строк-якорей

| Факт | Файл:строка | Модуль |
|------|-------------|--------|
| Развилка HelloGolang / preset | `u_conn.go:109` | metacubex/utls v1.8.4 |
| `applyPresetByID` | `u_conn.go:129` | metacubex/utls |
| Закомментированный заголовок «not ready yet» | `u_handshake_client.go:324` | metacubex/utls |
| Закомментированное присваивание TP | `u_handshake_client.go:333` | metacubex/utls |
| Единственное активное присваивание TP | `handshake_client.go:210` | metacubex/utls |
| `makeClientHello` вызовы (Golang + ECH-inner) | `u_conn.go:116`, `:569` | metacubex/utls |
| `QUICTransportParametersExtension` в пресетах | 0 в `u_parrots.go`/`u_common.go` | metacubex/utls |
| `conn *tls.QUICConn` (конкретный тип) | `crypto_setup.go:30` | sagernet/quic-go |
| `tls.QUICClient(...)` конструктор | `crypto_setup.go:95` | sagernet/quic-go |
| `reality` STDConfig ошибка | `reality_client.go:127` | sing-box-lx |
| `uTLS` STDConfig ошибка | `utls_client.go:82` | sing-box-lx |
| `qtls.Config` приведение `config.(Config)` | `quic.go:92,106,121` | sagernet/sing-quic |
| `package qtls` | `quic.go:1` | sagernet/sing-quic |
| `tls.NewClient` hysteria2 | `hysteria2/outbound.go:57` | sing-box-lx |
| `tls.NewClient` tuic | `tuic/outbound.go:47` | sing-box-lx |
| RFC — TP обязательны в ClientHello | RFC 9001 §8.2 (MUST) | — |
