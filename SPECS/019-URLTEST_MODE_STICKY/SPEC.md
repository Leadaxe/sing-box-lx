# SPEC 019 — URLTest: режимы балансировки `mode` + липкость `sticky`

**Тип:** lx-фича (расширение группы `urltest` — балансировка нагрузки)
**Статус:** Реализовано + протестировано (юнит + живой прогон на 5 vless-нодах) —
см. [TEST_REPORT.md](TEST_REPORT.md). Релиз `v1.14.0-lx.1-rc.11`.
**Приоритет:** Medium (распределение трафика по нескольким узлам, а не выбор одного «лучшего»)
**Файлы ядра:** `option/group.go`, `protocol/group/urltest.go`, `constant/proxy.go`
**Связано:** upstream issue [#4110](https://github.com/SagerNet/sing-box/issues/4110)
(тот же концепт `mode` на `urltest`); SPEC 014/015 (Clash→CommandClient — `Now()` для UI)

## Задача

Upstream sing-box умеет только **выбирать один узел**: `selector` (вручную) и
`urltest` (лучший по latency). Нет балансировки — раздачи трафика по нескольким
узлам. Issue #4110 в апстриме открыт и просит ровно это, но мейнтейнер
исторически отклонял load-balancing (#227/#698/#3023 — `not planned`). Для форка
это уместная фича.

Добавляем на существующий тип `urltest` два поля:

- **`mode`** — стратегия выбора узла на каждое соединение;
- **`sticky`** — опциональная привязка «один поток → один узел» (объект).

Форма «`mode` на `urltest`» (а не новый outbound `loadbalance`) выбрана
сознательно: переиспользует готовую health-инфраструктуру urltest (тикер, история
задержек, `url`/`interval`/`tolerance`), совпадает по конфигу с #4110, и при
пустом `mode` поведение урлтеста не меняется (полная обратная совместимость).

## Конфиг

```json
{
  "type": "urltest",
  "tag": "balanced",
  "outbounds": ["a", "b", "c"],
  "url": "https://www.gstatic.com/generate_204",
  "interval": "3m",
  "tolerance": 50,
  "mode": "round_robin",
  "sticky": {
    "mode": "jumphash",
    "timeout": "10m",
    "cap": 2000,
    "hash": ["process", "domain"]
  }
}
```

### Поле `mode` (string, дефолт `least_test`)

| Значение | Поведение |
|---|---|
| `least_test` (или пусто) | **дефолт.** Текущее поведение urltest: выбрать лучший по `delay + tolerance`. Полная обратная совместимость. `sticky` игнорируется. |
| `round_robin` | ротация по кругу **среди живых** узлов. |
| `least_connection` | узел с минимумом активных соединений **среди живых**. **Фаза 2** (см. ниже). |

### Поле `sticky` (объект, дефолт — отсутствует = липкости нет)

| Поле | Тип | Дефолт | Смысл |
|---|---|---|---|
| `mode` | string | `jumphash` | механизм привязки: `jumphash` (без состояния) или `ttlmap` (таблица с TTL) |
| `timeout` | duration | `10m` | TTL записи в `ttlmap`. Для `jumphash` игнорируется (состояния нет) |
| `cap` | int | `2000` | макс. размер таблицы `ttlmap`; превышение → выселение старейших (LRU). Для `jumphash` игнорируется |
| `hash` | []string | `[]` | компоненты ключа: `process`, `domain`, `source_ip`, `dest_ip`, `dest_port`. Пустой `[]` = липкости нет |

`sticky` применяется только к `round_robin` и `least_connection`. При `least_test`
игнорируется (с варном в лог, если `sticky.hash` непуст).

## Семантика

### Отбор кандидатов (общий для всех режимов)

- Кандидаты = только узлы с **живым результатом теста** (`history.LoadURLTestHistory`
  непуст и поддерживают `network`). Непротестированные/мёртвые исключены.
- Отсчёт начинается с **лучшего** (наименьший delay) — детерминированная точка
  старта ротации.
- Все мертвы / нет истории → fallback на первый узел из `outbounds`,
  поддерживающий `network` (как делает текущий `Select` на :315-322).

### Ключ липкости

- Ключ = конкатенация компонентов `sticky.hash[]` **в порядке массива**,
  с разделителем (например `\x00`).
- Источник компонентов (в момент `DialContext`/`ListenPacket`):
  | Компонент | Источник | Может быть пуст |
  |---|---|---|
  | `process` | `adapter.ContextFrom(ctx).ProcessInfo` → первый `AndroidPackageNames`, иначе `ProcessPath` | да (нет атрибуции) |
  | `domain` | `destination.Fqdn` (хост, если не IP) | да (трафик по IP) |
  | `source_ip` | `adapter.ContextFrom(ctx).Source.Addr().String()` | да (нет metadata) |
  | `dest_ip` | `destination.Addr().String()` | редко |
  | `dest_port` | `destination.Port` | почти никогда |
- **Пустой компонент → `""` в ключ.**
- **Все компоненты пусты → ключ `""`.** `""` — стабильный ключ: `jumphash("")`
  даёт один фиксированный узел всегда. Ротации для безключевых соединений **нет**
  (иначе рвутся сессии). Это не особая ветка — просто следствие правила
  «пустые → `""`».

### Механизм `jumphash` (дефолт, без состояния)

- `node = JumpConsistentHash(hash64(key), len(живые))` → индекс в **списке живых
  узлов** (отсортированном детерминированно, например по тегу).
- Состояния нет: `timeout`/`cap` неактуальны.
- Цена бесстатусности: при изменении числа живых узлов ~1/n ключей пересчитаются
  на другой узел (jump-hash минимизирует переезд — только ~1/n, не полная
  перетасовка как у `% n`).

### Механизм `ttlmap` (с состоянием)

Таблица `key → {node tag, lastTime}` под `sync.Mutex`:

- ключ есть и узел **жив** → берём записанный узел, `lastTime = now`;
- ключа нет / записанный узел **умер** (выпал из живых) → выбираем среди живых
  по правилу режима (`round_robin`/`least_connection`), записываем/перезаписываем.
  **Это единственный случай переезда залипшего ключа — и он обязателен.**
- **Чистка:** ленивая (при проходе выселяем `now - lastTime > timeout`) **+
  лёгкий фоновый тикер** (редкий полный свип); тикер гасится в `Close()`.
- **Cap:** при `len(table) > cap` выселяем самые старые по `lastTime` (LRU).

### Гранулярность

- Выбор узла — **один раз на соединение** (`DialContext` для TCP, `ListenPacket`
  для UDP). UDP/QUIC-сессия целиком остаётся на одном узле — никакой
  ребалансировки по датаграмме.

## Корень интеграции (точки в коде)

`urltest` сейчас хранит выбор как **одно поле** `selectedOutboundTCP/UDP`
(`protocol/group/urltest.go:200-201`) и применяет его в `DialContext` (:118-142) /
`ListenPacket` (:144-160) **без учёта `destination`**. Пересчёт — только тикером
в `performUpdateCheck` (:410-427).

Для `round_robin`/`least_connection`/sticky этого недостаточно: выбор обязан
происходить **на каждое соединение** с учётом ключа. Поэтому:

- В `DialContext`/`ListenPacket` добавляется ветка по `mode`:
  - `least_test` → существующий путь (`selectedOutboundTCP/UDP` + `Select`);
  - иначе → новый `selectByMode(network, key)` — отбирает живых, применяет
    ротацию/least-conn/sticky, возвращает узел на это соединение.
- Health-тикер/история/`urlTest`/`performUpdateCheck` — **не трогаются** (они
  по-прежнему поддерживают историю задержек, которой пользуется отбор живых).
- `Now()` (`:110-130`) для не-`least_test` режимов вернёт последний фактически
  выбранный тег (`lastSelected`, индикатор для Clash-API/UI; «текущего» в строгом
  смысле нет).
- **`Now()` cold-start (least_test, rc.12).** До первого URL-теста история пуста,
  поэтому `selectedOutboundTCP/UDP` ещё `nil` — но трафик уже идёт через
  fallback-узел, который вернёт `Select()` (первый годный = `outbounds[0]` при
  пустой истории, `exists=false`). Раньше `Now()` в этот момент возвращал `""`
  (UI зиял пустотой, хотя соединения шли). Теперь при `nil`-выборе `Now()`
  доопрашивает `Select(TCP)`/`Select(UDP)` и показывает **тот же** узел, что
  выберет следующий `DialContext` — это не «предполагаемый», а тот же источник
  истины (та же функция, что зовёт `DialContext` при пустом `selectedOutbound*`).
  Микрозазор остаётся только в редкой гонке «результат теста прилетел ровно между
  `Now()` и дайлом» на первых секундах. Заметь: когда процесс **не выгружался** из
  памяти (быстрый рестарт конфига на Android), история и `selectedOutbound*`
  переживают рестарт (in-memory `HistoryStorage`, без диска), поэтому `Now()`
  показывает прошлый выбор сразу, без прогрева — это и есть наблюдаемая «липкость
  выбора после перезапуска», by design апстрима.

### Источник метаданных

`adapter.ContextFrom(ctx)` (`adapter/inbound.go:176`) → `*InboundContext`
(может быть `nil` — тогда `process`/`source_ip` пусты, ключ всё равно собирается).
`ProcessInfo` (`*ConnectionOwner`, `adapter/platform.go:73`) заполняется
`route.searchProcessInfo` **до** выбора outbound, но только при включённом
process-searcher и локальном источнике — иначе `nil` (компонент → `""`, это ОК).
Образец чтения — `dns/client_log.go:86` `processInfoFromContext`.

## Опции (`option/group.go`)

```go
type URLTestOutboundOptions struct {
    Outbounds                 []string           `json:"outbounds"`
    URL                       string             `json:"url,omitempty"`
    Interval                  badoption.Duration `json:"interval,omitempty"`
    Tolerance                 uint16             `json:"tolerance,omitempty"`
    IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
    InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
    // lx: SPEC 019
    Mode   string                  `json:"mode,omitempty"`   // least_test|round_robin|least_connection
    Sticky *URLTestStickyOptions   `json:"sticky,omitempty"`
}

// lx: SPEC 019
type URLTestStickyOptions struct {
    Mode    string             `json:"mode,omitempty"`    // jumphash|ttlmap; дефолт jumphash
    Timeout badoption.Duration `json:"timeout,omitempty"` // дефолт 10m (только ttlmap)
    Cap     int                `json:"cap,omitempty"`     // дефолт 2000 (только ttlmap)
    Hash    []string           `json:"hash,omitempty"`    // process|domain|source_ip|dest_ip|dest_port
}
```

## Валидация (fail-fast при старте)

- `mode` ∈ {`""`, `least_test`, `round_robin`, `least_connection`} — иначе ошибка.
- `sticky.mode` ∈ {`""`, `jumphash`, `ttlmap`} — иначе ошибка.
- каждый элемент `sticky.hash` ∈ {`process`, `domain`, `source_ip`, `dest_ip`,
  `dest_port`} — иначе ошибка.
- `sticky.cap < 0` — ошибка; `0` → дефолт 2000.
- `sticky` непуст при `mode == least_test` → варн в лог (не ошибка), игнорируем.

## Фазы

- **Фаза 1 (этот SPEC, сейчас):** `mode` = `least_test` + `round_robin`; `sticky`
  с `jumphash` и `ttlmap`. Хирургическая правка точки выбора в
  `DialContext`/`ListenPacket` + ключ + две реализации привязки. `least_connection`
  валидируется, но возвращает ошибку «не реализован» (или временно маппится на
  `round_robin` — решить при реализации).
- **Фаза 2 (отдельно):** `least_connection`. Требует per-node счётчика активных
  соединений с **декрементом на закрытии** (counting-обёртка вокруг conn через
  `interruptGroup`/`onClose`, риск утечки счётчика) — другой класс сложности и
  рисков, поэтому отдельным заходом со своим тест-планом.

## Проверка

- **Распределение:** N дайлов по K живым узлам в `round_robin` (без sticky) →
  каждый узел получил ≈ N/K (равномерно в пределах ±1).
- **Пропуск мёртвого:** узел без истории не выбирается; после его «оживления» —
  снова в ротации.
- **Fallback:** все мертвы → `outbounds[0]`, не паника.
- **Sticky `jumphash`:** один и тот же ключ при стабильном наборе живых → всегда
  один узел; ключ `""` (все компоненты пусты) → фиксированный узел, не ротация.
- **Sticky `ttlmap`:** ключ держится за узлом, пока узел жив и `< timeout`;
  протухание после `timeout`; перезапись при смерти узла; выселение при `cap`.
- **Дефолт `least_test`:** конфиг без `mode`/`sticky` ведёт себя бит-в-бит как
  старый urltest (выбор лучшего, тикер, история).
- `go build ./...`, `-tags with_lx_command`, `go test -race ./protocol/group/`,
  `gofmt` чисто.

## Что НЕ в scope

- Веса узлов (`weighted_round_robin`) — отложено; sticky уже даёт управление
  распределением через зерно ключа.
- `least_connection` — Фаза 2.
- Новый outbound-тип `loadbalance` — намеренно не делаем (расширяем `urltest`).
- Изменение Clash-API/proto — `mode`/`sticky` чисто конфиг-сторона ядра;
  `Now()`/`All()` остаются как есть.
