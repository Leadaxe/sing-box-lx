# SPEC 032 — VLESS Encryption (`mlkem768x25519plus`), клиент

**Фича:** [VLESS_ENCRYPTION](../../FEATURES/012-VLESS_ENCRYPTION/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) |
| Статус | C (complete) — реализовано; остаток: полевая проверка |
| Ветка | `lx` |
| Build-tag | без нового (ядро VLESS собирается всегда) |
| Связанные | [[SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT]] · [[SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION]] · [[SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES]] |

## 1. Проблема

Конфиг с полем `encryption` VLESS-аутбаунда не работает:

```json
{ "type": "vless", "uuid": "…", "flow": "xtls-rprx-vision",
  "encryption": "mlkem768x25519plus.native.0rtt.<base64>",
  "tls": { "enabled": true, "reality": { "enabled": true, "public_key": "…" } } }
```

Зазор — на трёх слоях сразу:

| Слой | Файл | Что есть | Чего нет |
|------|------|----------|----------|
| Схема конфига | `option/vless.go` | `uuid`, `flow`, `network`, tls, `multiplex`, `transport`, `packet_encoding` | поля `encryption` — декодер строгий, конфиг отвергается до старта |
| Клиент VLESS | `protocol/vless/outbound.go:89` | `vless.NewClient(uuid, flow, logger)` | параметра под слой шифрования |
| Протокол | `sing-vmess` (внешний, `go.mod:58`) | `Request` = `version(0)+uuid+addons+cmd+addr` | самого слоя: ни handshake, ни AEAD-обёртки |

**Следствие для пользователя:** такие конфиги не поднимаются ни у нас, ни в любом
другом клиенте на sing-box (Karing в том числе) — это не баг разбора в приложении.
Совместимы сегодня Xray-core (референс) и mihomo. Апстрим не реализовал: запрос
[SagerNet/sing-box#4179](https://github.com/SagerNet/sing-box/issues/4179) удалён,
публичного обязательства нет. Обход на сервере — отдельный inbound с
`"decryption": "none"`.

**Не путать с REALITY.** Слой живёт **внутри** VLESS, под транспортом и независимо
от TLS. `encryption` и REALITY комбинируются, но это разные механизмы; PQ-фичи
самого REALITY вынесены в §7 и в scope не входят.

Цель — принимать `encryption`-конфиги наравне с Xray/mihomo.

## 2. Архитектура (как будет устроено)

### 2.1 Формат строки и парсер — новый пакет `common/vlessenc/`

```
mlkem768x25519plus.<appearance>.<0rtt|1rtt>[.<padding>][.<delay>].<ключ>[.<ключ>…]
```

| Сегмент | Значения | Смысл |
|---------|----------|-------|
| handshake | `mlkem768x25519plus` (единственный) | должен совпасть с сервером |
| appearance | `native` \| `xorpub` \| `random` | вид на проводе: AEAD-заголовки как TLSv1.3 / XOR по публичным ключам / полностью случайный (XOR-ятся только 5-байтные заголовки) |
| rtt | `0rtt` \| `1rtt` | переиспользование тикета vs всегда полный handshake |
| padding, delay | `100-111-1111.75-0-111.50-0-3333` | блоки `вероятность-от-до`; **только 1-RTT** |
| ключ | base64 | публичный параметр сервера (`xray x25519` или `xray mlkem768`) |

`ParseClientSpec(string) (*ClientSpec, error)`; пусто или `none` → слой выключен.
Ошибка парса = ошибка конфига на `check`/старте с указанием сегмента — fail-fast по
образцу [[SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION]], а не падение в рантайме.

Формат брать из **текущей** доки Project X: после мержа он менялся (padding
переведён в `probability-from-to` в v25.8.31), текст PR уже расходится с реальностью.

### 2.2 Handshake — обёртка `net.Conn` между транспортом и `vless.Client`

Пять сообщений; `unitedKey` комбинирует `pfsKey` (forward-secret, ML-KEM-768 +
X25519) и `nfsKey` (из конфигурационного пароля):

1. **Client Hello** — длина под `nfsKey`, PFS-публичный ключ, паддинг
2. **Server Hello** — PFS-ключ под `nfsKey`, тикет под `unitedKey`
3. **Ticket Hello** — длина/тикет под `nfsKey`, внутренний VLESS под `unitedKey`
4. **Server Random** — 16 случайных байт + внутренний VLESS под `unitedKey`
5. **0-RTT** — шаги 1–2 пропускаются по кэшированному тикету

Точка врезки — `protocol/vless/outbound.go`: обёртка ставится **над** транспортом/TLS
и **под** `vless.Client`, который остаётся нетронутым. Кэш тикетов — на процесс,
ключ (сервер, пароль), TTL из Server Hello; персистентности нет.

**Криптопримитивы — stdlib:** `crypto/mlkem` (Go 1.24) + `crypto/ecdh`. Наш `go.mod`
— `go 1.24.7`, внешних зависимостей не требуется. С Vision слой не пере-шифрует уже
зашифрованный TLSv1.3-payload.

### 2.3 Схема конфига

`option.VLESSOutboundOptions`: одно поле `Encryption string json:"encryption,omitempty"`.
Отсутствие → текущее поведение байт-в-байт.

## 3. Зона касания upstream

- `option/vless.go`, `protocol/vless/outbound.go` — **2 файла, +1 поле и врезка
  обёртки**; остальное в изолированном `common/vlessenc/` (lx-owned). Так апстримный
  мерж этой фичи даст конфликт в двух строках, а не в криптокоде.
- `sing-vmess` **не трогаем** — слой снаружи от него.
- Inbound (`decryption`) — вне scope: форк client-focused (issue
  [#5](https://github.com/Leadaxe/sing-box-lx/issues/5) закрыт not planned).

## 4. Критерии приёмки

1. `check` принимает `encryption: "mlkem768x25519plus.native.0rtt.<key>"`; битые строки
   дают ошибку с указанием сегмента.
2. Юниты парсера на строках из доки Project X и mihomo-вики, включая негативные.
3. **Живой Xray-сервер** с `decryption: "mlkem768x25519plus.native.600s.<key>"`:
   handshake + DNS + HTTPS + download (главный критерий, стендовый).
4. Оба режима: `1rtt` с паддингом и `0rtt` с реконнектом по тикету.
5. Все три appearance: `native`, `xorpub`, `random`.
6. Комбинация с REALITY + Vision + XHTTP ([[SPECS/TASKS/002-XHTTP_CLIENT_TRANSPORT]]) несёт трафик.
7. Регресс: конфиги без `encryption` не изменили поведения.
8. `gofmt -l` чист по lx-файлам, `lx-check` зелёный.

## 5. Оценка объёма

| Слой | Файлы | Характер | LOC |
|------|-------|----------|-----|
| Схема конфига | `option/vless.go` | +1 поле | ~5 |
| Парсер spec-строки | `common/vlessenc/spec.go` | сегменты + валидация, механически по доке | ~120–180 |
| **Handshake** | `common/vlessenc/client.go` | **новый механизм** — 5 сообщений, HKDF, AEAD, паддинг, точный wire-формат | ~300–450 |
| Кэш тикетов | `common/vlessenc/ticket.go` | map + TTL | ~40–60 |
| Врезка | `protocol/vless/outbound.go` | обёртка conn | ~20–30 |
| Тесты | `common/vlessenc/*_test.go` | парс + вектора handshake | ~150–250 |

**Итого ~650–1000 LOC.** Риск сосредоточен в §2.2: ошибка в порядке HKDF или в
паддинге даёт «молча не коннектится» без диагностики. Парсер и конфиг — дёшево и
предсказуемо. Отсюда порядок работ: парсер+конфиг одним подходом, handshake со
стендом — отдельным, основным.

## 6. Открытые вопросы — закрыты

Все три сняты при реализации (2026-08-01):

- **Эталон.** Отлаживать вслепую не пришлось: слой уже реализован в форке
  `starifly/sing-box` (`protocol/vless/encryption`, 948 LOC на клиент+сервер) —
  том самом ядре NekoBox+, которое поднимает эти ноды в поле. Клиентская часть
  портирована оттуда, а не написана с нуля, поэтому риск §5 («ошибка в порядке
  HKDF даёт молча не коннектится») практически снят.
- **Лицензия.** Вопрос отпал вместе со сменой источника: `starifly/sing-box` —
  тот же GPL-3.0 и та же upstream-база, что у нас. Заимствование внутри одной
  лицензии; происхождение отмечено в заголовках портированных файлов.
- **Приоритет.** Кейс воспроизводимый и не гипотетический: 8 нод рабочей
  подписки владельца требуют этот слой. Симптом — WS-апгрейд проходит, дальше
  сервер рвёт связь; в логе пусто. Диагноз подтверждён дампом конфигурации
  NekoBox+ (`sager_net.db`), где у этих же серверов стоит
  `mlkem768x25519plus.native.0rtt.<ML-KEM-768 key>`.

## 7. Не в scope

- Серверная сторона (`decryption`).
- Другие handshake-методы — иных пока не существует.
- Персистентный кэш тикетов между запусками.
- **PQ-фичи самого REALITY** — отдельная зона, не нужна для этой задачи:
  - `mldsa65Verify` в схеме нет (`option/tls.go:243` — три поля). Но сервер с
    `mldsa65Seed` наш клиент **не отвергает**: по дизайну Xray клиент без
    `mldsa65Verify` соединяется, лишь пропуская доп-проверку. Реализация упирается в
    отсутствие `crypto/mldsa` в stdlib → внешняя криптозависимость, решение владельца.
  - `X25519MLKEM768` вырезается из ClientHello (`common/tls/reality_client.go:148-158`)
    **намеренно**: auth-key REALITY выводится жёстко из классического X25519
    `KeyShareKeys.Ecdhe`, снятие фильтра без ветвления по `ServerHello.ServerShare.Group`
    ломает аутентификацию. Цена — расхождение с настоящим Chrome (fingerprint-риск).

  Заводить отдельную спеку — только если появится реальный кейс.

## 8. Источники

- PR [XTLS/Xray-core#5067](https://github.com/XTLS/Xray-core/pull/5067) — механизм,
  wire-формат, `proxy/vless/encryption/{client,server}.go`; merged 2025-08-28
- [Project X — VLESS outbound](https://xtls.github.io/en/config/outbounds/vless.html) —
  дока поля `encryption`, `xray vlessenc`/`x25519`/`mlkem768`
- [mihomo — VLESS](https://wiki.metacubex.one/en/config/proxies/vless/) — второй
  независимый клиент, подтверждает формат
- Xray-core [v25.8.31](https://newreleases.io/project/github/XTLS/Xray-core/release/v25.8.31) —
  padding переведён в `probability-from-to`
- PR [XTLS/Xray-core#4915](https://github.com/XTLS/Xray-core/pull/4915) и
  [дока REALITY](https://xtls.github.io/en/config/transports/reality.html) — §7, ML-DSA-65
