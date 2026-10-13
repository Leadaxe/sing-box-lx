# SPEC: 131 — MASQUE_DROP_LEGACY_NETWORK

**Фича:** [MASQUE_WARP](../../FEATURES/009-MASQUE_WARP/FEATURE.md) (конфиг к общему стандарту, продолжение [062](../062-MASQUE_CONFIG_SCHEMA_MIGRATION/SPEC.md))

| Поле | Значение |
|------|----------|
| Тип | R (refactor) — снятие legacy-чтения, обещанного SPEC 062 «Removed in v1.14.0-lx.30» |
| Статус | N (new) — поставлена 2026-10-13 из сессии лаунчера по решению владельца |

## 1. Решение владельца (13.10.2026)

Контекст: аудит LxBox §591 п.6 предлагал «старые ссылки с `network=` у masque читать для
совместимости, при h2→h3 предупреждать». Владелец отказал дословно: **«отказываемся от
старых ссылок вовсе, никакой обратной совместимости; старый параметр `network` полностью
освобождаем под `udp|tcp` как в sing-box; новое не делаем, но старую логику снимаем
полностью»**. На уточнение «касается ли это ядра» — **«да, снимаем и в ядре»**.

Клиенты уже сделали свою часть (контракт LX Shared Contract 1.1.118, D-129, лаунчер
`6dc299d4`, LxBox §642): `network` у masque не читается, не мигрируется, не эмитится;
в теле — обычный неизвестный ключ, который общий санитайзер снимает до ядра. Ядро —
последнее место, где `network` ещё означает версию HTTP.

## 2. Что есть сейчас

- `option/masque_lx.go`: поле `Network string json:"network"` с пометкой
  «Deprecated: use `vhttp`. Removed in v1.14.0-lx.30» и комментарием «`network` is expected
  to take its usual meaning later». Срок lx.30 давно прошёл (сейчас 1.14.3-lx.17), поле живо.
- `protocol/masque/legacy_options_lx.go:resolveLegacyOptions`: `network` сворачивается
  в `VHTTP`; расхождение `network` ≠ `vhttp` — hard error всего конфига; использование
  репортится `deprecated.OptionMASQUELegacyFields` (`experimental/deprecated/constants.go`).
- `docs-lx/lx-config.md` ~572 и `lx-config.ru.md` ~578: врезка «old shape still works and
  reports a deprecation» + таблица миграции.
- Тесты: `protocol/masque/legacy_options_lx_test.go`.

## 3. Требования

1. **Снять чтение `network` как версии HTTP.** Поле `Network` из `MASQUEOutboundOptions`
   убрать; ветку `network` в `resolveLegacyOptions` убрать; тесты на неё — снять или
   перевернуть («`network` в теле masque не влияет на `vhttp`»).
2. **Семантику tcp/udp для `network` НЕ вводить** (решение владельца: «новое не делаем»).
   Ключ после снятия должен вести себя как любой неизвестный ключ тела masque — как
   ядро сегодня обрабатывает неизвестное поле outbound (ошибка строгого JSON-декодера
   `json: unknown field "network"` либо игнор — оставить ровно то поведение, что у
   прочих неизвестных ключей, не делать исключений). Переименование `network_list` →
   `network` — отдельная будущая задача, здесь не делается.
3. **Остальные legacy-поля блока** (`sni`, `skip_cert_verify`, `fragment`,
   `fragment_fallback_delay`, `record_fragment`) — тот же срок lx.30 истёк. Решение
   владельца этой задачи касается `network`; снимать ли вместе с ним остальной блок
   (и `OptionMASQUELegacyFields` целиком) — спросить владельца одной строкой перед
   реализацией. Если «да» — снять всё одной волной и удалить `legacy_options_lx.go`
   вместе с константой deprecated; если «нет» — константу и `Description` переписать
   без слова `network`.
4. **Доки:** `docs-lx/lx-config.md`/`.ru.md` — врезку переписать: `network` у masque не
   читается, старые конфиги с `network` не поддерживаются (вместо «still works and reports
   a deprecation»); из таблицы миграции строку `network → vhttp` снять или пометить
   «removed». `docs-lx/lx-changelog.md` — пункт в раздел следующего релиза;
   `FEATURES/009-MASQUE_WARP/FEATURE.md` — абзац про `network` (строка ~53) обновить.
5. **Совместимость клиентов:** лаунчер и LxBox до ядра `network` не пропускают
   (общий `unknown_key`), поэтому поведение ядра на теле с `network` клиентами не
   наблюдается; ломать нечего. Ручной `config.json` пользователя с `network:` у masque
   после релиза перестанет читаться как версия HTTP — это и есть решение владельца.

## 4. Критерии приёмки

- `grep -rn 'Network' option/masque_lx.go protocol/masque/` не находит поля версии HTTP
  (`NetworkList`, `autoNetwork`, `effectiveNetwork` — не в счёт).
- Конфиг masque с `vhttp: h2` без `network` работает как прежде; конфиг с `network: h2`
  и без `vhttp` получает дефолт профиля, а не h2.
- `make -f Makefile.lx lx-build`, `go test ./protocol/masque/... ./option/...` зелёные;
  `lx-ci.yml` зелёный.
- Релиз-нота и changelog называют снятие явно.

## 5. Связанное

- Контракт клиентов: `singbox-launcher/contract/README.md` 1.1.118, `SPECS/103-F-O-LX_SHARED_CONTRACT/DECISIONS.md` D-129, `contract/registry/protocols/masque.json` (`body.skipped.network`).
- SPEC 062 (миграция схемы masque), SPEC 060 (fragment на h3).
