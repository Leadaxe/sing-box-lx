# SPEC 062 — конфиг masque к стандарту sing-box (вложенный `tls`, `transport`), с алиасами

**Фича:** [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — схема конфига + миграция |
| Статус | D (draft) — скоуп утверждён владельцем 2026-08-12, реализации нет |
| Зона | схема `option/masque.go` и её разрешение; поведение туннеля не трогаем |
| Build-tag | — (в ядре) |
| Смежные | [SPEC 021](../021-MASQUE_CONNECT_IP_OUTBOUND/SPEC.md) — сам outbound; [SPEC 060](../060-TLS_FRAGMENT_AUTO_ON_DETOUR/SPEC.md) — авто-фрагментация |

## 0. TL;DR

Masque — единственный outbound со своим диалектом конфига: плоские `sni` /
`skip_cert_verify` / `fragment*` вместо общего блока `tls: {…}`, и `network` в
значении «транспорт h3/h2» вместо стандартного «список tcp/udp».

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

### 1.2 Транспорт

| поле | сейчас | делаем |
|---|---|---|
| `network` (`"h3"`/`"h2"`, `string`) | транспорт | **не трогаем тип**; deprecated в пользу `transport` |
| `transport` (`"h3"`/`"h2"`) | — | **новое**, предпочтительное |
| `network_list` (`["tcp","udp"]`) | L4-список | **не трогаем** |

**Тип `network` НЕ меняется** (решение владельца 2026-08-12). Переезд L4-списка из
`network_list` в `network` и смена его типа на `NetworkList` — **следующий шаг, после
`lx.30`**, когда алиасы снимут. Так конфиги не падают на парсинге и не нужен хрупкий
декод двух форм одного поля.

⚠️ Формулировка предупреждения должна показывать, что `network` не исчезает, а меняет
владельца — иначе читается как «функциональность убрали»:

```
masque: `network` is deprecated, use `transport` instead
(`network` will later mean the tcp/udp list, as in other outbounds)
```

### 1.3 Предупреждения на неприменимое

- **`tls.alpn`** — warning + игнор. ALPN выводится из транспорта (`h3` → `["h3"]`,
  `h2` → `["h2"]`); заданный вручную ломает согласование транспорта с endpoint'ом.
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
    Description:       "legacy masque options (network, sni, skip_cert_verify, fragment*)",
    DeprecatedVersion: "1.14.0-lx.26",
    ScheduledVersion:  "1.14.0-lx.30",
    EnvName:           "MASQUE_LEGACY_FIELDS",
}
```

`ScheduledVersion` — версия, в которой алиасы перестают работать; она же момент, когда
`network` можно переназначить на L4-список (§1.2).

⚠️ **Окно короткое.** Темп релизов форка — 8 стабильных тегов за две недели
(`lx.16`…`lx.24`, 2026-07-27…08-11), то есть lx.26 → lx.30 это порядка недели-полутора.
Для сравнения, upstream на такую же депрекацию даёт две минорные версии. Владелец
подтвердил срок: LxBox — наш продукт, генератор конфигов и миграция подписок делаются
синхронно. **Условие выполнимости: LxBox переключается на новые имена до `lx.30`.**

## 4. Решения (owner, 2026-08-12)

1. **Имя нового поля транспорта — `transport`.** Освобождает `network` под стандартный
   смысл; альтернатива «не трогать вовсе» отклонена — тогда `network`/`network_list`
   остаются вывернутыми навсегда.
2. **`ScheduledVersion` = `1.14.0-lx.30`.**
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
| `option/masque.go` | +`OutboundTLSOptionsContainer`, +`Transport`, пометки Deprecated на legacy |
| `protocol/masque/outbound.go` | разрешение старое/новое, warning'и, проброс `tls` в `buildH2TLSClient` |
| `experimental/deprecated/constants.go` | +`Note` |

Upstream-файлов — три, все по содержимому уже наши (masque целиком lx-owned).
`transport/masque/*` не трогаем.

## 7. Риски

1. **Короткое окно депрекации** (§3) — главный. Смягчение: LxBox обновляется синхронно.
2. **Шум в логах у всех пользователей** до обновления LxBox: каждый masque-узел даст
   deprecation-warning. Приемлемо (warning, не ошибка), но заметно — стоит согласовать
   момент включения с релизом LxBox.
3. **Подписки со старыми именами** — работают до `lx.30`, дальше требуют перегенерации
   на стороне LxBox.
4. **badjson и вложенный блок** — «поле отсутствует» vs «пустой объект» проверять живым
   `box.New`, а не чтением структуры (прецедент: badjson схлопывает пустые значения).

## 8. Тест-план

См. [TEST_PLAN.md](TEST_PLAN.md).
