# SPEC 031 — Сверка паритета AWG и параметр AdvancedSecurity

**Фича:** [AWG2](../../FEATURES/AWG2/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | Q (question/исследование) → F (feature) для §5 |
| Статус | N (new) |
| Подмодуль | `submodules/wireguard-go` (HEAD `7d15f33`, база sagernet + LX-прививка AWG) |
| Связанные | [[SPECS/TASKS/003-AWG2_CLIENT_ENDPOINT]] · [[SPECS/TASKS/005-AWG2_RANGED_MAGIC_HEADERS]] · [[SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION]] · [[SPECS/TASKS/009-WIRESOCK_MASQUERADE_PROFILES]] · [[SPECS/TASKS/026-AWG_MAGIC_VS_RESERVED_CLEAR]] |

> **Папка переименована** из `031-AWG2_TIMED_JUNK_J_ITIME` (номер `031` — прежний
> якорь). Исходное содержание — фича «timed junk J1–J3/Itime» — **отозвано**: таких
> параметров в AmneziaWG не существует, см. §3 и [HISTORY.md](HISTORY.md).

## 1. Вопрос

«Мы все фишки AWG реализовали или не все?» — требовался ответ, проверяемый по
первоисточнику, а не по памяти.

## 2. Ответ: паритет по обфускации полный — 16 из 16

У AmneziaWG **нет нормативной спецификации** (нет RFC, нет whitepaper — в отличие
от WireGuard). Протокол задан реализациями, поэтому «истина» = пересечение
официальных реализаций. Сверка по трём независимым источникам:

| Источник | Роль | Набор параметров обфускации |
|---|---|---|
| `amneziawg-tools` `src/config.c` | что пользователь пишет в `.conf` (ближайшее к спеке конфига) | `Jc Jmin Jmax` · `S1–S4` · `H1–H4` · `I1–I5` · `AdvancedSecurity` |
| `amneziawg-linux-kernel-module` `src/uapi/wireguard.h` | netlink-контракт | `WGDEVICE_A_{JC,JMIN,JMAX,S1..S4,H1..H4,I1..I5}` · `WGPEER_A_ADVANCED_SECURITY` |
| **наш форк** (3 слоя: `option/` → `device_awg.go` → `device/uapi.go`) | — | `jc jmin jmax` · `s1–s4` · `h1–h4` · `i1–i5` |

**Вывод: все 16 параметров обфускации AWG у нас реализованы**, сквозной цепочкой
конфиг → UAPI → устройство. CPS-мини-язык `I1–I5` совпадает 1:1 — те же 8 тегов
(`b, d, ds, dz, r, rc, rd, t`), сверено с `obfBuilders` апстрима.

Единственное расхождение в наборе — `AdvancedSecurity` (§5).

Расхождение в интроспекции (не влияет на работу): наш `device/uapi.go` **get**-путь
не отдаёт `i1..i5` (только set принимает); апстрим отдаёт.

## 3. Чего в AWG НЕТ (отозванные гипотезы)

Фиксируется, чтобы не переоткрывать.

**`J1`, `J2`, `J3`, `Itime` — не существуют.** Ни в `amneziawg-go`, ни в
`amneziawg-tools`, ни в ядерном модуле. Исходная версия этой спеки описывала их
как «поздний слой advanced security» — это была ошибка памяти, не подтверждённая
кодом.

**«Advanced»-ключи из ветки `master` `amneziawg-go`** —
`header_protection_key`, `content_padding_addition`, `rekey_after_time`,
`rekey_timeout`, `reject_after_time`, `keepalive_timeout`,
`max_handshake_attempts` — **не часть AWG**:

- `amneziawg-tools` их не отправляет (проверено `config.c`/`set.c`/`setconf.c` —
  ни одного упоминания) ⇒ пользователь не может их задать, в `.conf` их нет;
- ядерный модуль их не знает (нет netlink-атрибутов).

Это неreleased разработка Go-ветки. Реальный AWG-сервер их не потребует — иначе он
был бы несовместим с официальным клиентом и с ядерным модулем. **Методический
вывод: `master` одной реализации ≠ протокол.** Сверять — по `amneziawg-tools`
(пользовательский набор) с кросс-проверкой ядерным модулем.

Часть этих ключей (`rekey_after_time`, `keepalive_timeout`,
`max_handshake_attempts`) — вообще локальные тайминги WireGuard, вынесенные в
настройку: они не согласуются с пиром и на совместимость не влияют.

## 4. Практический ответ про совместимость

К **любому серверу на официальном AmneziaWG** (Go-реализация или ядерный модуль,
выпущенные версии) наш клиент подключается: полный набор обфускации на месте.

Оговорка, не связанная с набором: всё это работает под тегом сборки `with_awg`;
без него UAPI подмодуля отвергнет `jc/i1/…` как неизвестные ключи
(`transport/wireguard/device_awg.go:34`).

## 5. Зазор: `AdvancedSecurity` (единственный подтверждённый)

Есть в обеих официальных реализациях, у нас отсутствует.

### 5.1 Что это по факту (по коду, не по названию)

- **Тип:** boolean. В tools — `parse_bool(&ctx->last_peer->awg, ...)` +
  флаг `WGPEER_HAS_AWG` (`config.c:587`), CLI-форма `advanced-security`;
  в модуле — `WGPEER_A_ADVANCED_SECURITY` типа `NLA_FLAG` (`netlink.c:75`).
- **Device-level роль — гейт валидации.** `wg_device_handle_post_config`
  (`device.c:562`): `if (!wg->advanced_security) return 0;` — вся проверка
  junk-параметров (`jc>=0`, `jmax<MESSAGE_MAX_SIZE`, `jmax>=jmin`, `S1`-переполнение)
  выполняется **только** при включённом флаге. Это ровно та валидация, что у нас
  реализована в [[SPECS/TASKS/008-AWG_JUNK_PARAM_VALIDATION]] — но у нас безусловная.
- **Peer-level роль — состояние согласования, не тумблер.** `noise.c:601-633`:
  `advanced_security = wg->advanced_security && …`, далее `peer->advanced_security = …`
  — значение **устанавливается по ходу хендшейка** и уходит в
  `wg_genl_mcast_peer_unknown(...)`. То есть per-peer это результат согласования, а
  не поле, которое пользователь задаёт ради изменения обфускации.

### 5.2 Почему это, скорее всего, не блокер совместимости

`advanced_security` в модуле **не гейтит отправку junk на send-пути** — в `send.c`
и `receive.c` он не встречается вообще (проверено grep'ом). Он управляет
валидацией конфига и учётом состояния пира. У нас обфускация применяется, когда
параметры заданы, а валидация безусловна — поведение на проводе то же.

**Это гипотеза уровня «по коду выглядит так», а не проверенный факт.** Не
проверено: поведение сервера, которому клиент **не** сообщил `AdvancedSecurity`
(игнорирует / трактует как «обфускации нет»), и есть ли у Go-реализации свой
эквивалент этого флага в выпущенных версиях.

### 5.3 Что сделать (если подтвердится необходимость)

1. Схема: `AdvancedSecurity *bool json:"advanced_security,omitempty"` в
   `option/wireguard_awg.go` (указатель — чтобы отличать «не задано» от `false`;
   ср. [[badjson-empty-slice-collapses-to-nil]] — со slice-семантикой уже обжигались).
2. Проброс: строка в `transport/wireguard/device_awg.go` по образцу существующих.
3. Подмодуль: `case "advanced_security"` в `device/uapi.go` + поле на device/peer;
   решить, гейтить ли им нашу валидацию (для паритета с модулем) — по умолчанию
   **не гейтить**, безусловная валидация строже и уже отлажена.

**Объём:** ~50–100 LOC (схема + проброс + UAPI + тесты). Логики на send-пути нет,
поэтому риск низкий — в отличие от отозванной таймерной фичи.

## 6. Критерии приёмки

Для исследовательской части (§2–§4) — выполнено: ответ получен и обоснован
ссылками на код трёх реализаций.

Для §5, если переводится в F:

1. Конфиг с `"advanced_security": true|false` парсится; отсутствие поля =
   поведение как сейчас (backward-compat).
2. UAPI подмодуля принимает ключ без `IpcErrorInvalid`.
3. Существующие AWG-конфиги ведут себя идентично — junk/CPS уходят как раньше.
4. `gofmt`/`lx-check` чистые.

## 7. Открытые вопросы

- **Нужен ли `AdvancedSecurity` практически?** Требуется живая проверка против
  сервера на официальном AWG: отличается ли поведение, когда клиент флаг не
  передаёт. Без этого фича — паритет ради паритета (ср. [[SPECS/TASKS/024-RUNTIME_LOOP_GUARD]],
  отложенную до воспроизводимого кейса).
- **Есть ли в выпущенных версиях `amneziawg-go` аналог флага?** В UAPI ветки
  `master` его нет; сверить по тегам/релизам, а не по `master`.
- **Закрывать ли расхождение get-пути** (`i1..i5` не отдаются нашим UAPI) — влияет
  только на интроспекцию; отдельная мелкая задача.

## 8. Источники

Все проверены загрузкой исходников (не по документации и не по памяти):

- `amnezia-vpn/amneziawg-tools` — `src/config.c` (`key_match` — набор `.conf`),
  `src/set.c`, `src/setconf.c`
- `amnezia-vpn/amneziawg-linux-kernel-module` — `src/uapi/wireguard.h`,
  `src/netlink.c`, `src/device.c:562`, `src/noise.c:601-633`, `src/send.c`, `src/receive.c`
- `amnezia-vpn/amneziawg-go` — `device/uapi.go` (ветка `master`; см. оговорку §3)
- наш форк — `option/wireguard_awg.go`, `transport/wireguard/device_awg.go`,
  `submodules/wireguard-go/device/{uapi.go,obf.go,obf_*.go,device.go,send.go}`
