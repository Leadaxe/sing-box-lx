# SPEC 059 — XHTTP XMUX: переиспользование HTTP-соединений

**Фича:** [XHTTP](../../FEATURES/002-XHTTP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — клиентский транспорт (CONSTITUTION §3.6) |
| Статус | D (draft) — дизайн; код не начат |
| Ветка | `lx` |
| Связанные | [[SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT]] (базовый транспорт, `PARAM_MAP.md` — построчная сверка полей) · [[SPECS/TASKS/050-URLTEST_CANCELLABLE]] (дедлайны XHTTP-conn, `Close()` рвёт пайп) |

## 1. Проблема

Сегодня каждый XHTTP-dial открывает **новое HTTP-соединение** к серверу.
Один `*http.Transport` на клиента переиспользует keep-alive соединения по
своей внутренней политике, но XHTTP этим не управляет и серверу об этом
не сообщает. Отсюда две группы последствий.

**Совместимость (главная цель).** Xray-сервер видит клиента без XMUX как
трафик с иным сетевым почерком, чем эталонный Xray-клиент: другое число
TCP/TLS-соединений на сессию, другое распределение запросов по ним, нет
принудительной ротации. Конфиги, приходящие к пользователям из подписок,
содержат секцию `xmux` — сейчас мы её **молча игнорируем**: поля нет в
`V2RayXHTTPOptions`, `badjson` его отбрасывает. Пользователь получает
транспорт, ведущий себя не так, как задумал владелец сервера.

**Оптимизация (вторая цель).** Без переиспользования каждый новый поток
платит полный TCP+TLS(+REALITY) хендшейк. На мобильной сети это десятки
–сотни мс на открытие и лишний расход батареи; при активном сёрфинге
(много коротких соединений) накладные расходы доминируют над полезным
трафиком.

Историческая справка: issue #4 был закрыт как «XMUX not planned» —
решение принималось в рамке «нет воспроизводимого детекта, значит не
нужно». Настоящая задача открыта с **другой** целью: не противодействие
детекту, а совместимость с Xray-серверами и экономия хендшейков.

## 2. Референс

Эталон — **shtorm-7/sing-box-extended**, тег `v1.13.18-extended-2.6.4`:

- [`transport/v2rayxhttp/mux.go`](https://github.com/shtorm-7/sing-box-extended/blob/v1.13.18-extended-2.6.4/transport/v2rayxhttp/mux.go)
  — `XmuxClient` / `XmuxManager` целиком (146 строк);
- [`transport/v2rayxhttp/client.go`](https://github.com/shtorm-7/sing-box-extended/blob/v1.13.18-extended-2.6.4/transport/v2rayxhttp/client.go)
  — точки вызова (`getHTTPClient`, `AddOpenUsage(±1)`);
- [`option/v2ray_transport.go`](https://github.com/shtorm-7/sing-box-extended/blob/v1.13.18-extended-2.6.4/option/v2ray_transport.go)
  — `V2RayXHTTPXmuxOptions`, дефолты (стр. 312–322), `GetNormalized*` (стр. 503–521).

Первоисточник семантики — Xray-core,
[`transport/internet/splithttp/config.go`](https://github.com/XTLS/Xray-core/blob/main/transport/internet/splithttp/config.go)
(`XmuxConfig`: `maxConcurrency`, `maxConnections`, `cMaxReuseTimes`,
`hMaxRequestTimes`, `hMaxReusableSecs` — все `RangeConfig`).

Локальный слепок референса на момент написания спеки лежит рядом:
[`REFERENCE_mux.go.txt`](REFERENCE_mux.go.txt) (расширение `.txt`, чтобы
файл не попадал в сборку). При расхождении первоисточник — ссылки выше.

## 3. Контракт

Новое поле в `V2RayXHTTPOptions` ([option/v2ray_xhttp.go](../../../option/v2ray_xhttp.go)):

```go
// Xmux configures HTTP connection reuse. Nil selects Xray-compatible
// defaults (see below); an explicit object overrides them.
Xmux *V2RayXHTTPXmuxOptions `json:"xmux,omitempty"`

type V2RayXHTTPXmuxOptions struct {
    MaxConcurrency   string `json:"max_concurrency,omitempty"`    // "min-max"
    MaxConnections   string `json:"max_connections,omitempty"`    // "min-max"
    CMaxReuseTimes   string `json:"c_max_reuse_times,omitempty"`  // "min-max"
    HMaxRequestTimes string `json:"h_max_request_times,omitempty"`// "min-max"
    HMaxReusableSecs string `json:"h_max_reusable_secs,omitempty"`// "min-max"
}
```

**Форма диапазонов — строка `"min-max"`**, как у всех прочих range-полей
нашего XHTTP (`x_padding_bytes`, `sc_*`); см.
[[SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT]] и память
`xhttp-range-fields-string-form`. Референс использует
`badoption.Range[int]` (форма `[min,max]`), Xray — `RangeConfig`;
обе принимают и одиночное число. **Решение:** принимаем строковую форму
как основную и дополнительно — JSON-массив `[min,max]` и голое число,
чтобы конфиги из подписок (написанные под extended/Xray) не падали.
Это расширение приёма, а не расхождение семантики.

### Семантика полей

| Поле | Значение | Дефолт (когда `xmux` отсутствует) |
|------|----------|-----------------------------------|
| `max_concurrency` | сколько одновременно открытых потоков допускает **одно** HTTP-соединение | `1-1` |
| `max_connections` | сколько соединений держать в пуле; при недоборе всегда открывается новое | `0` (не ограничено) |
| `c_max_reuse_times` | сколько раз соединение может быть **выдано** под новый поток | `0` (без лимита) |
| `h_max_request_times` | сколько HTTP-запросов проходит через соединение до вывода из пула | `600-900` |
| `h_max_reusable_secs` | срок жизни соединения в пуле, секунды | `1800-3000` |

Дефолты **применяются и при отсутствии секции** `xmux` — это поведение
референса (`option/v2ray_transport.go:312-319`) и оно же даёт совместимость
«из коробки»: клиент без явного конфига ведёт себя как Xray-клиент.

⚠️ Каждый диапазон **разыгрывается один раз** (`rand()`): для менеджера —
при создании (`max_concurrency`, `max_connections`), для соединения — в
момент его появления (`c_max_reuse_times`, `h_max_request_times`,
`h_max_reusable_secs`). Не перевыбирается на каждый запрос.

### Валидация

`max_connections` и `max_concurrency` **взаимоисключающие**: если оба
заданы с ненулевым верхом — ошибка конфигурации (текст референса:
`max_connections cannot be specified together with max_concurrency`).
Проверка — в `normalizeMeta`, вместе с остальными XHTTP-полями.

## 4. Механизм

Модель референса переносится один в один; отличия — только в типах
(`intRange` вместо `badoption.Range[int]`) и в том, что у нас клиент
client-only (см. §6).

**`xmuxClient`** — одно HTTP-соединение плюс его счётчики:
`openUsage` (сколько потоков сейчас на нём живёт), `leftUsage` (сколько
раз ещё можно выдать), `leftRequests` (атомарный остаток HTTP-запросов),
`unreusableAt` (дедлайн). Закрытие отложенное: `Close()` при
`openUsage > 0` только помечает `closed`, реальный `XmuxConn.Close()`
происходит, когда последний поток отпустит соединение (`AddOpenUsage(-1)`).
Это критично — иначе ротация рвёт живые потоки пользователя.

**`xmuxManager`** — пул под мьютексом. `getXmuxClient()`:

1. **вытеснение** — из пула убираются соединения, у которых:
   закрыт транспорт (`IsClosed()`), исчерпан `leftUsage`, исчерпан
   `leftRequests`, или наступил `unreusableAt`. Вытесненные закрываются
   (отложенно, см. выше);
2. пул пуст → новое соединение;
3. `max_connections > 0` и в пуле меньше — новое соединение;
4. иначе среди пула отбираются те, у кого `openUsage < max_concurrency`
   (при `max_concurrency == 0` — все);
5. если таких нет → новое соединение;
6. иначе — **криптослучайный** выбор одного из кандидатов
   (`crypto/rand`, как в референсе — не `math/rand`), декремент
   `leftUsage`.

**Точки интеграции в нашем клиенте**
([transport/v2rayxhttp/client.go](../../../transport/v2rayxhttp/client.go)):
`Client` получает `xmuxManager`; в `DialContext` соединение берётся из
менеджера, счётчик поднимается `AddOpenUsage(+1)`, а в `Close()` conn'а —
опускается `AddOpenUsage(-1)`. Декремент обязан быть **идемпотентным**
(`sync.Once` на conn), иначе двойной `Close()` — а он у нас возможен, см.
[[SPECS/TASKS/050-URLTEST_CANCELLABLE]] — уведёт счётчик в минус и
соединение никогда не закроется.

`leftRequests` декрементируется на **каждый HTTP-запрос**, а не на
каждый поток: в packet-up режиме один поток порождает много POST'ов
(по одному на `Write`). Это тонкий момент — счётчик обязан жить в слое,
через который проходят все запросы (`newRequest`/`RoundTrip`), иначе
`h_max_request_times` не соблюдается и сервер увидит соединение, живущее
дольше договорённого.

## 5. Взаимодействие с существующим кодом

**Дедлайны и отмена ([[SPECS/TASKS/050-URLTEST_CANCELLABLE]]).** Сейчас
`Close()` XHTTP-conn'а рвёт пайп и освобождает dial. С XMUX добавляется
второй смысл: отпустить соединение в пул. Порядок обязан быть таким:
сначала завершить свой поток, потом `AddOpenUsage(-1)`. Тест на двойной
`Close()` — обязателен.

**Асинхронный download (`ead099674`).** `dialPacketUp`/`dialStreamUp`
теперь отдают conn до получения response-заголовков; download-`RoundTrip`
живёт в горутине. Счётчик `openUsage` обязан подниматься **до** запуска
горутины и опускаться только когда закрылись обе половины — иначе
соединение вернётся в пул, пока по нему ещё читают.

**REALITY.** При `mode: auto` + REALITY выбирается stream-one
(см. `reality_detect.go`). XMUX ортогонален выбору режима, но
переиспользование REALITY-соединения — самый ценный случай по
оптимизации (хендшейк дороже обычного TLS).

## 6. Границы

- **Client-only.** Форк клиентский (память `fork-is-client-focused`),
  серверная часть XMUX не реализуется. Поля, влияющие только на сервер,
  не вводятся.
- **`download`-секция референса** (отдельный транспорт/detour для
  скачивания со своим `xmux`) **вне объёма**: у нас нет `Download`-опций
  в `V2RayXHTTPOptions`. Второй менеджер (`xmuxManager2`) не нужен.
  Если `download` появится — расширять отдельной задачей.
- **`h_keep_alive_period`** — в текущих Xray-core и extended отсутствует
  (проверено: в `XmuxConfig` пять полей). Не вводим.

## 7. Критерии приёмки

1. Конфиг с секцией `xmux` (в любой из трёх форм записи диапазона)
   принимается без ошибок; конфиг без неё получает Xray-дефолты
   (`max_concurrency 1-1`, `h_max_request_times 600-900`,
   `h_max_reusable_secs 1800-3000`).
2. `max_connections` вместе с `max_concurrency` → ошибка конфигурации.
3. Юнит-тесты на `xmuxManager`: вытеснение по каждому из четырёх
   критериев; открытие нового при пустом пуле / недоборе
   `max_connections` / заполненном `max_concurrency`; декремент
   `leftUsage`; отложенное закрытие при `openUsage > 0`.
4. Тест на идемпотентность: двойной `Close()` conn'а уменьшает
   `openUsage` ровно на 1.
5. Тест на `h_max_request_times` в packet-up: N POST'ов по одному потоку
   выводят соединение из пула.
6. `debug`-лог: открытие нового соединения и каждое вытеснение с
   указанием причины (`closed` / `reuse` / `requests` / `expired`) —
   это замена метрикам, см. §8.2.
7. `go test ./transport/v2rayxhttp/...` зелёный; `gofmt` чист;
   сборка под lx-тегами проходит.
8. **Полевая проверка на живом Xray-сервере** — обязательна до перевода
   статуса в C: сессия поднимается, переживает ротацию по
   `h_max_request_times`, не рвёт активные потоки при вытеснении.

## 8. Открытые вопросы

1. **Дефолты при отсутствии `xmux`.** Референс включает XMUX всегда.
   Для нас это меняет поведение существующих конфигов (сейчас пул
   не ограничен ничем, кроме `http.Transport`). Принять поведение
   референса (совместимость) — или включать XMUX только при явной
   секции (консервативность)? Спека написана под **первый** вариант,
   как более совместимый; решение за владельцем.
2. ~~**Метрики в observability.**~~ **Решено 2026-08-11: не делать в этой
   задаче.** Состояние пула детерминировано конфигом (в отличие от urltest
   и DNS, где наблюдаемость появилась из-за зависимости от сети), а
   реальный вопрос отладки — «переиспользовалось ли соединение» — читается
   по времени открытия потоков, а не по числу соединений в пуле. Цена шва
   (proto → регенерация `pb.go` через `lx-proto` → поддержка в LxBox и
   лаунчере) несоразмерна. **Вместо этого:** `debug`-лог в самом
   транспорте — строка при открытии нового соединения и при вытеснении с
   причиной (`closed` / `reuse` / `requests` / `expired`). Этого хватает
   для полевой проверки §7. Метрики — отдельной задачей в
   [OBSERVABILITY](../../FEATURES/006-OBSERVABILITY/FEATURE.md), если
   полевой прогон покажет, что нужен взгляд снаружи ядра.
