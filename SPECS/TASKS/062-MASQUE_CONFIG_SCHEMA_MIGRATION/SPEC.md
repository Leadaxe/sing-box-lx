# SPEC 062 — конфиг masque к стандарту sing-box (вложенный `tls`, `vhttp`), с алиасами

**Фича:** [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — схема конфига + миграция |
| Статус | C (complete) — сделано 2026-08-12; обе схемы проверены на живом WARP, старые конфиги работают с одним предупреждением |
| Зона | схема `option/masque.go` и её разрешение; поведение туннеля не трогаем |
| Build-tag | — (в ядре) |
| Смежные | [SPEC 021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) — сам outbound; [SPEC 060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) — авто-фрагментация |

## 0. TL;DR

Masque — единственный outbound со своим диалектом конфига: плоские `sni` /
`skip_cert_verify` / `fragment*` вместо общего блока `tls: {…}`, и `network` в
значении «версия HTTP h3/h2» вместо стандартного «список tcp/udp».

Приводим к общей конвенции **аддитивно**: новые имена работают, старые продолжают
работать через алиасы с deprecation-предупреждением. Ничего не ломается сразу;
снятие алиасов — `v1.14.0-lx.30`.

Почему это не косметика: чужой диалект стоит пользователю каждый раз, когда он
переносит настройки между протоколами, и нам — каждый раз, когда общий TLS-слой
получает новую возможность, а masque её не видит (так уже вышло с
`record_fragment`, см. SPEC 060).

---

## 1. Что меняется

### 1.1 Вложенный TLS-блок

Добавляется `OutboundTLSOptionsContainer` — тот же контейнер, что у vless/trojan/vmess
(`option/tls.go:138`, поле `TLS *OutboundTLSOptions \`json:"tls,omitempty"\``).

| было (плоское) | стало | старое |
|---|---|---|
| `sni` | `tls.server_name` | алиас, deprecated |
| `skip_cert_verify` | `tls.insecure` | алиас, deprecated |
| `fragment` | `tls.fragment` | алиас, deprecated |
| `fragment_fallback_delay` | `tls.fragment_fallback_delay` | алиас, deprecated |
| `record_fragment` | `tls.record_fragment` | алиас, deprecated |

Обработчик уже готов: h2-путь ходит через `tls.NewClientWithOptions`
([SPEC 021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) Ф4), то есть весь
`OutboundTLSOptions` уже разбирается штатно — сейчас мы лишь конструируем его
вручную из трёх полей вместо того, чтобы принять от пользователя.

### 1.2 Версия HTTP (h3/h2)

| поле | сейчас | делаем |
|---|---|---|
| `network` (`"h3"`/`"h2"`, `string`) | версия HTTP | **не трогаем тип**; deprecated в пользу `vhttp` |
| `vhttp` (`"h3"`/`"h2"`) | — | **новое**, предпочтительное |
| `network_list` (`["tcp","udp"]`) | L4-список | **не трогаем** |

**Тип `network` НЕ меняется** (решение владельца 2026-08-12). Переезд L4-списка из
`network_list` в `network` и смена его типа на `NetworkList` — **следующий шаг, после
`lx.30`**, когда алиасы снимут. Так конфиги не падают на парсинге и не нужен хрупкий
декод двух форм одного поля.

⚠️ Формулировка предупреждения должна показывать, что `network` не исчезает, а меняет
владельца — иначе читается как «функциональность убрали»:

```
masque: `network` is deprecated, use `vhttp` instead
(`network` will later mean the tcp/udp list, as in other outbounds)
```

### 1.3 Предупреждения на неприменимое

- **`tls.alpn`** — warning + игнор. ALPN выводится из версии HTTP (`h3` → `["h3"]`,
  `h2` → `["h2"]`); заданный вручную ломает согласование с endpoint'ом.
- **`tls.ech`, `tls.kernel_tx`, `tls.kernel_rx`, `tls.reality`** — warning + игнор,
  для masque неприменимы.
- **`fragment` / `record_fragment` на h3** — warning: там нет TLS-over-TCP, резать
  нечего (см. [SPEC 060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md)).

## 2. Правила разрешения старое/новое

1. задано новое → берём новое;
2. задано только старое → берём старое + `deprecated.Report`;
3. заданы оба и **противоречат** → fail-fast с внятной ошибкой (молча выбирать одно
   из двух нельзя — пользователь не узнает, какое поле проигнорировано);
4. заданы оба и совпадают → берём новое, молча.

## 3. Депрекация

Штатный механизм upstream — `deprecated.Note` + `deprecated.Report(ctx, …)`
(`experimental/deprecated/constants.go`).

```go
var OptionMASQUELegacyFields = Note{
    Name:              "masque-legacy-fields",
    Description:       "legacy masque options (…), replaced by `vhttp` and the standard `tls` block (planned for removal in v1.14.0-lx.30)",
    DeprecatedVersion: "1.14.0-lx.26",
    // ScheduledVersion намеренно пуст — см. ниже.
    EnvName:           "MASQUE_LEGACY_FIELDS",
}
```

### ⚠️ `ScheduledVersion` не заполняем: он ломает lx-нумерацию

Обнаружено при реализации, **на живом конфиге** — юнит-тесты этого не ловят (в них
`C.Version = "unknown"`, и проверка выключается).

`Note.Impending()` считает срочность по **минорной** версии:
`Parse(ScheduledVersion).Minor - Parse(C.Version).Minor <= 1`. Для пары
`1.14.0-lx.26` → `1.14.0-lx.30` разница минорных версий равна **0**, то есть «снятие
вот-вот». В этом режиме `stderrManager` превращает предупреждение в **фатальную ошибку**
с требованием выставить `ENABLE_DEPRECATED_MASQUE_LEGACY_FIELDS` — то есть ломает ровно те
конфиги, ради совместимости которых депрекация и затевалась.

Апстримовая схема исходит из того, что снятие приходится на следующий minor; lx-линия
нумерует релизы **внутри** одного minor, поэтому механизм неприменим как есть. Любая
lx-цель (`1.14.0-lx.NN`, да и `1.15.0`) даёт тот же эффект — «безопасны» только `1.16.0+`,
что уже неправда по смыслу.

Решение: `ScheduledVersion` оставить пустым, целевую версию назвать в `Description`.
Дополнительно поправлен `MessageWithLink()` — при пустом поле он выдавал оборванное
«will be removed in sing-box .», теперь говорит «in a future version» (одна ветка,
поведение остальных Note не меняется).

Момент, когда `network` можно переназначить на L4-список (§1.2), определяется этой спекой,
а не полем в коде.

⚠️ **Окно короткое.** Темп релизов форка — 8 стабильных тегов за две недели
(`lx.16`…`lx.24`, 2026-07-27…08-11), то есть lx.26 → lx.30 это порядка недели-полутора.
Для сравнения, upstream на такую же депрекацию даёт две минорные версии. Владелец
подтвердил срок: LxBox — наш продукт, генератор конфигов и миграция подписок делаются
синхронно. **Условие выполнимости: LxBox переключается на новые имена до `lx.30`.**

## 4. Решения (owner, 2026-08-12)

1. **Имя нового поля — `vhttp`** (значения `"h3"`/`"h2"`). Освобождает `network` под
   стандартный смысл. `transport` **отвергнут**: у vless/trojan/vmess это ключ
   V2Ray-транспорта и он **объект** (`{"type":"ws"}`), а не строка — переиспользовать
   его для h3/h2 значило бы воспроизвести ровно ту путаницу, которую снимает эта задача.
   `mode`/`version` заняты в форке в другом смысле (группы urltest; `HTTPClientOptions`).
2. **Срок снятия — `v1.14.0-lx.30`** (в коде поле `ScheduledVersion` пустое, см. §3).
3. **`tls.utls` для masque пока НЕ даём.** Это единственная реальная маскировка
   ClientHello (в отличие от `cipher_suites`, который на TLS 1.3 почти ничего не решает),
   но на h3 uTLS работает иначе, чем на h2, и требует отдельной проверки. Отдельная
   задача, если понадобится.

## 5. Что НЕ в scope

- поведение туннеля, pinning, регистрация/enroll в WARP (вне ядра — SPEC 021 §4);
- переезд L4-списка `network_list` → `network` и смена типа (после `lx.30`);
- `tls.utls` (§4.3);
- изменение дефолтного SNI — уже сделано в SPEC 021.

## 6. Тронутые файлы

| файл | что |
|---|---|
| `option/masque.go` | +`OutboundTLSOptionsContainer`, +`VHTTP`, пометки Deprecated на legacy |
| `protocol/masque/legacy_options_lx.go` | **новый** — `resolveLegacyOptions`, `warnUnsupportedTLSOptions` |
| `protocol/masque/outbound.go` | вызов разрешения, `disable_sni`, чтение `options.TLS`, проброс блока в `buildH2TLSClient` |
| `experimental/deprecated/constants.go` | +`Note` (+ в реестр `Options`) и правка `MessageWithLink()` при пустом `ScheduledVersion` |

Тесты (lx-owned): `protocol/masque/legacy_options_lx_test.go`.

Из upstream-файлов затронут только `experimental/deprecated/constants.go` — одна ветка в
`MessageWithLink()`; остальное по содержимому наше (masque целиком lx-owned).
`transport/masque/*` не трогали.

## 10. Результаты (2026-08-12)

Живой WARP, обе схемы на одном конфиге:

| узел | схема | результат |
|---|---|---|
| h3 | `network` + `sni` (legacy) | ✅ `warp=on` |
| h3 | `vhttp` + `tls.server_name` | ✅ `warp=on` |

- deprecation-строк на конфиг с legacy-полями — **ровно одна** (не по одной на поле);
- конфликт `network: h3` + `vhttp: h2` → fail-fast с именами обоих полей;
- `tls.alpn` → warning «ignored — ALPN follows `vhttp`», узел работает;
- `sing-box check` принимает обе схемы.

h2 в этом прогоне падал (`x509: algorithm unimplemented`) — **не регрессия**: собрана
контрольная сборка из `v1.14.0-lx.25-rc.3`, она падает идентично. Причина внешняя: на
подставленный SNI Cloudflare отдаёт настоящий RSA-сертификат этого сайта, а профиль
пиннит ECDSA-ключ эндпоинта (то же наблюдалось в SPEC 021).

Статика: `make -f Makefile.lx lx-build`, `go vet`, `gofmt -l` — чисто;
`go test -race` по `protocol/masque`, `transport/masque/...`, `option`, `common/tls` — зелено.

## 7. Риски

1. **Короткое окно депрекации** (§3) — главный. Смягчение: LxBox обновляется синхронно.
2. **Шум в логах у всех пользователей** до обновления LxBox: каждый masque-узел даст
   deprecation-warning. Приемлемо (warning, не ошибка), но заметно — стоит согласовать
   момент включения с релизом LxBox.
3. **Подписки со старыми именами** — работают до `lx.30`, дальше требуют перегенерации
   на стороне LxBox.
4. **badjson и вложенный блок** — «поле отсутствует» vs «пустой объект» проверять живым
   `box.New`, а не чтением структуры (прецедент: badjson схлопывает пустые значения).

## 8. План реализации

Порядок шагов важен: схема → разрешение → предупреждения → проводка. После каждого шага
дерево должно собираться и проходить тесты.

### Шаг 1. Схема (`option/masque.go`)

```go
type MASQUEOutboundOptions struct {
    DialerOptions
    ServerOptions
    OutboundTLSOptionsContainer   // ← новое: даёт `tls: {…}`

    Profile   string `json:"profile,omitempty"`
    VHTTP string `json:"vhttp,omitempty"` // ← новое: "h3" | "h2"

    // Deprecated: use `vhttp`. Removed in v1.14.0-lx.30.
    Network string `json:"network,omitempty"`
    // Deprecated: use `tls.server_name`. Removed in v1.14.0-lx.30.
    SNI string `json:"sni,omitempty"`
    // Deprecated: use `tls.insecure`. Removed in v1.14.0-lx.30.
    SkipCertVerify bool `json:"skip_cert_verify,omitempty"`
    // Deprecated: use `tls.fragment`. Removed in v1.14.0-lx.30.
    Fragment bool `json:"fragment,omitempty"`
    // …то же для FragmentFallbackDelay, RecordFragment
    …
}
```

Комментарий `// Deprecated:` — не косметика: его показывают IDE и `staticcheck`, поэтому
формулировка обязана называть замену и версию снятия.

Конфликта имён нет: плоские `fragment`/`record_fragment` и одноимённые поля внутри `tls`
живут на разных уровнях JSON (проверено по составу `OutboundTLSOptions`).

### Шаг 2. Разрешение (`protocol/masque/outbound.go`, начало `NewOutbound`)

Отдельная функция, не размазывать по телу:

```go
// resolveLegacyOptions folds the deprecated flat fields into the standard
// shapes, reporting each one it had to use. lx: SPEC 062.
func resolveLegacyOptions(ctx context.Context, options *option.MASQUEOutboundOptions) error
```

Для каждой пары действует §2. Конкретно:

| новое | старое | конфликт = fail-fast, если |
|---|---|---|
| `Transport` | `Network` | оба заданы и различаются |
| `TLS.ServerName` | `SNI` | оба заданы и различаются |
| `TLS.Insecure` | `SkipCertVerify` | оба заданы и различаются |
| `TLS.Fragment` | `Fragment` | оба заданы и различаются |
| `TLS.FragmentFallbackDelay` | `FragmentFallbackDelay` | оба заданы и различаются |
| `TLS.RecordFragment` | `RecordFragment` | оба заданы и различаются |

⚠️ **Ловушка с `bool`.** У `SkipCertVerify`/`Fragment`/`RecordFragment` «не задано» и
«задано `false`» неразличимы (прецедент: [SPEC 060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) §3.3).
Поэтому для них:
- конфликтом считается только `старое == true && новое == false`;
- `старое == true` → включаем и репортим;
- `старое == false` → молчим, даже если поле явно написано в конфиге (отличить нельзя).

Так «явный `false`» не будет ошибочно объявлен конфликтом, а `true` не потеряется.

### Шаг 3. Предупреждения (§1.3)

Проверять **после** разрешения, по итоговому `options.TLS`:

- `TLS.ALPN != nil` → warning, поле игнорируется (ALPN задаёт транспорт);
- `TLS.ECH`/`TLS.Reality`/`TLS.KernelTx`/`TLS.KernelRx` заданы → warning, игнор;
- транспорт `h3` и (`TLS.Fragment` или `TLS.RecordFragment`) → warning: на QUIC резать
  нечего.

Это `logger.Warn`, не ошибки: конфиг остаётся валидным, поведение предсказуемым.

### Шаг 4. Проводка

- `sni := …` в `NewOutbound` читает `options.TLS.ServerName` (после разрешения);
- `PrepareTLSConfig(..., insecure)` получает `options.TLS.Insecure`;
- `buildH2TLSClient` перестаёт конструировать `OutboundTLSOptions` вручную и принимает
  пользовательский `*options.TLS`, доклеивая поверх только masque-специфику (pinning,
  клиентский сертификат, ALPN по транспорту) — как сейчас, но поверх пользовательских
  значений, а не поверх пустышки;
- `network` в теле кода переименовать в `vhttp` для читаемости (внутренняя
  переменная, на конфиг не влияет).

### Шаг 5. `deprecated.Note`

`experimental/deprecated/constants.go` — по образцу §3. Вызов —
`deprecated.Report(ctx, deprecated.OptionMASQUELegacyFields)` из `resolveLegacyOptions`;
менеджер берётся из контекста и при его отсутствии это тихий no-op
(`experimental/deprecated/manager.go:13`), так что в тестах вызов безопасен.

Репортить **один раз на outbound**, а не на каждое legacy-поле: иначе узел с четырьмя
старыми полями даст четыре одинаковых строки.

### Шаг 6. Побочная возможность (бесплатно)

Вложенный блок приносит `tls.disable_sni` — штатный способ сказать «слать ClientHello
без SNI». Сейчас такого способа нет вовсе: пустой `sni` подменяется дефолтом профиля.
Отдельно реализовывать не надо, но **проверить в тест-плане** (см. TEST_PLAN §новая схема),
поскольку на части путей endpoint отвечает только без SNI (SPEC 021 §Молчащий endpoint).

## 9. Тест-план

См. [TEST_PLAN.md](TEST_PLAN.md).
