# SPEC: 091 — CONFIG_VALIDATION_TUIC_MASQUE

**Фича:** [HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md) (§1, апстримный файл) ·
[MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (§2, форк-нативный пакет)

| Поле | Значение |
|------|----------|
| Тип | B (bug) — две дыры валидации конфига, найдены DRIFT-инвентарём контракта лаунчера (DRIFT 131 §8.3) |
| Статус | C (complete) — юниты red/green; выпущено в `v1.14.1-lx.6`, тексты ошибок сверены на бинарнике из релиза. Открытый вопрос владельца (не блокирует закрытие): смягчать ли tuic-часть до WARN |

Две мелкие заявки агента лаунчера от 2026-09-18, обе про то, что ядро молча или
невнятно реагирует на неверный конфиг. Общий принцип (как у SPEC 089 для
`reality.key_share`): опечатка отвергается **при загрузке**, текстом, который
пользователь может прочитать и исправить; тихих дефолтов «на всякий случай» нет.

Build-tag: нет. Scope: клиент (outbound'ы `tuic` и `masque`).

---

## 1. `tuic.udp_relay_mode`: мусорное значение молча становится `native`

### 1.1 Проблема

`protocol/tuic/outbound.go`, `NewOutbound`:

```go
switch options.UDPRelayMode {
case "native":
case "quic":
    tuicUDPStream = true
}
```

Ветки `default` нет: `"Native"`, `"qiuc"`, `"udp"` и любая другая опечатка тихо
означают `native`. Соседняя опция `congestion_control` при опечатке даёт ошибку
`unknown congestion control algorithm: <value>` (из `sing-quic`) — поведение
разъезжается внутри одного outbound'а. Документация апстрима перечисляет ровно два
значения, `native` и `quic`, третьего нет.

Код апстримный; `switch` без `default` там с появления tuic-outbound'а.

### 1.2 Требования

1. `default` в `switch`: любое значение, кроме `""`, `"native"`, `"quic"` →
   `E.New("unknown udp_relay_mode: ", options.UDPRelayMode, " (expected native or quic)")`.
2. `""` по-прежнему = `native` (документированный дефолт) — не трогать.
3. Проверка конфликта `udp_over_stream` + `udp_relay_mode` остаётся как есть и
   стоит **до** нового `default` (порядок не менять: конфликт двух заданных
   опций важнее опечатки в одной).
4. Маркер `// lx: SPEC 091` на правке; файл апстримный.

### 1.3 Критерии приёмки

- Юнит `protocol/tuic`: `udp_relay_mode: "qiuc"` → ошибка с текстом
  `unknown udp_relay_mode`; `"native"`, `"quic"`, `""` → конструктор проходит эту
  проверку (ошибка, если есть, — не про `udp_relay_mode`; допустимо проверять
  через `sing-box check` на минимальном конфиге, см. `lx-test/config/`).
- `sing-box check` на конфиге с опечаткой → exit ≠ 0 и этот текст.

### 1.4 Условие снятия (реестр HOTFIXES)

Апстрим добавит `default` (или валидацию в `option`) для `udp_relay_mode`. Файл
апстримный — проверять маркер на каждом мерже.

## 2. `masque`: `profile: "standard"` без `uri` — два разных фатала

### 2.1 Проблема

`protocol/masque/outbound.go`, `NewOutbound`: `resolveVHTTP` вызывается **раньше**
проверки `uri`. Пользователь, забывший `uri` на `standard`, видит разный текст в
зависимости от `vhttp`:

| `vhttp` | Текст ошибки | Про что она на самом деле |
|---|---|---|
| `h2` | `masque: vhttp h2 is not implemented for the standard profile` | про h2, не про `uri` |
| `h3` / `auto` / не задано | `masque: uri is required for the standard profile` | про `uri` |

Первый текст уводит пользователя чинить `vhttp`, хотя после этого он упрётся во
второй. Обе ошибки верные сами по себе, проблема в порядке и в том, что текст про `uri` не
говорит, **что** туда писать.

Код форк-нативный (`protocol/masque` — наш пакет, SPEC 021/062/074).

### 2.2 Требования

1. Проверка `uri` переносится **выше** `resolveVHTTP` (сразу после
   `resolveLegacyOptions`, потому что legacy-поля могут заполнять `uri`). Ошибка
   про h2 на `standard` остаётся, но теперь до неё доходят только конфиги, у
   которых `uri` есть.
2. Текст ошибки про `uri` расширяется до самодостаточного:
   `masque: uri is required for the standard profile — set it to the server's CONNECT-IP request URI, e.g. https://<host>/.well-known/masque/ip/*/*/`.
   ⚠️ Плейсхолдеров вида `{target_host}` в ядре **нет**: `uri` уходит в
   Extended CONNECT как есть (`transport/masque` ничего не подставляет), поэтому в
   примере — форма полного туннеля RFC 9484 со `*`. Первый черновик этой SPEC
   обещал подстановку — исправлено при реализации.
3. `cloudflare`-профиль (`uri` по умолчанию из профиля) не меняется.
4. Маркер `// lx: SPEC 091` на перестановке.

### 2.3 Критерии приёмки

- Юнит `protocol/masque` (рядом с `auto_vhttp_lx_test.go`): `standard` без `uri`
  при `vhttp` ∈ {`h2`, `h3`, `auto`, `""`} даёт **один и тот же** текст про `uri`;
  `standard` + `uri` + `vhttp: h2` даёт прежнюю ошибку про h2; `cloudflare` без
  `uri` проходит проверку.
- `docs-lx/lx-config.md` и `.ru.md` §4 MASQUE (плюс список ошибок валидации в `lx-protocols-transports*.md` §3.9): у `uri` на `standard` — одна фраза,
  что без него outbound не поднимется и какой шаблон ожидается (если такой фразы
  ещё нет; если есть — не дублировать).

## 3. Границы

- Никаких новых ключей и смены дефолтов.
- Провод tuic/masque не меняется.
- Сабмодули не затрагиваются.
- В лаунчере/LxBox свои гарды на оба поля остаются (UX-защита до текста ядра).
