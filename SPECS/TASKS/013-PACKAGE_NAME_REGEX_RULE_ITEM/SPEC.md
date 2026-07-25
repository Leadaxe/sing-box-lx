# SPEC: 013 — PACKAGE_NAME_REGEX_RULE_ITEM

**Фича:** [UPSTREAM_SYNC](../../FEATURES/UPSTREAM_SYNC/FEATURE.md)

| Поле | Значение |
|------|----------|
| Тип | F (feature) — бэкпорт апстрим-фичи |
| Статус | C (complete) |

Добавить rule-item **`package_name_regex`** (route / DNS / headless) — матчинг имени Android-пакета по регулярному выражению. Точечный бэкпорт апстрим-фичи 1.14 на стабильную базу 1.13.13 **без** полной миграции на 1.14.

Scope: **все платформы** (фича активна там, где заполняется `ProcessInfo.AndroidPackageNames`, т.е. Android). Build-tag: нет — встроена в ядро роутинга.

> **Обновление (2026-07-02, база 1.14.0-alpha.35, аудит SPEC 022 #19):** после миграции базы на 1.14 сам impl (`route/rule/rule_item_package_name_regex.go` + поле `option.RawDefaultRule.PackageNameRegex`) стал **нативным upstream** — бэкпорт-дельта по коду больше не нужна и растворилась в базе. НО LX-тест `route/rule/rule_item_package_name_regex_test.go` **сохраняется намеренно**: upstream своего теста для этого item не поставляет (проверено на базе и на `upstream/testing`), так что это единственное покрытие фичи. Тест изолирован в своём `_test.go`, тестирует стабильный публичный API (`NewPackageNameRegexItem`/`Match`) и ребейз-конфликтов не несёт.

---

## 1. Проблема / контекст

Запрос (2026-06-23): нужен `package_name_regex` в проекте. У апстрима поле существует **только с sing-box 1.14.0** (commit [`941ce58b`](https://github.com/SagerNet/sing-box/commit/941ce58b) «Add `package_name_regex` route, DNS and headless rule item»), в ветке 1.13.x его нет. На нашей базе уже есть `package_name` (точное совпадение, map-lookup) — но не regex-вариант.

Полная миграция 1.13.13→1.14 оценена отдельным feasibility-разбором как ~1,5–2 дня работы с главным риском в ребейзе AmneziaWG-подмодуля `wireguard-go` (база 506b763 → v0.0.3, ветки diverged 52/51, ручная переинсерция §010 android-GRO fix). Сама же фича `package_name_regex` — изолированный add в `route/rule`, **не трогает** ни один awg/xhttp/selector/build-tag файл и **не гейтится** новым build-тегом. Поэтому выбран точечный бэкпорт, а полная миграция отложена до выхода **v1.14.0 stable** (её штатно подхватит существующий `lx-rebase.yml`, который по дизайну исключает alpha/beta/rc).

Апстрим-коммит `941ce58b` дополнительно содержит хунк про `C.RuleSetVersion5` в `option/rule_set.go` — это часть отдельного rule-set v5 (1.14), **не относится** к фиче и **не переносится** (на нашей базе `RuleSetVersionCurrent = RuleSetVersion4`).

---

## 2. Цель

Правило роутинга / DNS-правило / headless-правило (rule-set) с полем `package_name_regex: ["^com\\.termux.*", ...]` матчит соединение, если хотя бы одно из имён пакетов в `metadata.ProcessInfo.AndroidPackageNames` удовлетворяет хотя бы одному из выражений. Семантика и сообщения об ошибках — идентичны апстрим-1.14.

---

## 3. Требования

### 3.1 Новый rule-item
- Файл `route/rule/rule_item_package_name_regex.go` — дословно апстрим-версия из `941ce58b`: `PackageNameRegexItem` с `[]*regexp.Regexp`, конструктор `NewPackageNameRegexItem([]string) (*PackageNameRegexItem, error)` (компиляция через `regexp.Compile`, ошибка `parse expression <i>`), `Match` по `AndroidPackageNames`, человекочитаемый `String()` (усечение до 3 выражений в описании).

### 3.2 Option-поля
- `PackageNameRegex badoption.Listable[string]` с тегом `json:"package_name_regex,omitempty"` — сразу после `PackageName` в трёх структурах: `option.RawDefaultRule` ([option/rule.go](../../../option/rule.go)), `option.RawDefaultDNSRule` ([option/rule_dns.go](../../../option/rule_dns.go)), `option.DefaultHeadlessRule` ([option/rule_set.go](../../../option/rule_set.go)). Выравнивание struct-тегов — под существующий столбец каждого файла (gofmt-чисто).

### 3.3 Регистрация item в правилах
- В `NewDefaultRule` ([route/rule/rule_default.go](../../../route/rule/rule_default.go)), `NewDefaultDNSRule` ([route/rule/rule_dns.go](../../../route/rule/rule_dns.go)), `NewDefaultHeadlessRule` ([route/rule/rule_headless.go](../../../route/rule/rule_headless.go)) — блок `if len(options.PackageNameRegex) > 0 { ... }` сразу после `PackageName`-блока, с проброской ошибки `E.Cause(err, "package_name_regex")`. Все три конструктора уже возвращают `error` на нашей базе — сигнатуры не меняются.

### 3.4 Cond-функции
- `isProcessRule` / `isProcessDNSRule` ([route/rule_conds.go](../../../route/rule_conds.go)) и `isProcessHeadlessRule` ([route/rule/rule_set.go](../../../route/rule/rule_set.go)) — добавить `|| len(rule.PackageNameRegex) > 0`, чтобы правило с одним лишь `package_name_regex` корректно классифицировалось как process-rule.

### 3.5 Что НЕ трогаем
- Хунк `RuleSetVersion5` из апстрим-коммита (см. §1) — **не переносим**.
- Документацию апстрима (`docs/`) бэкпортить не обязательно; референс — официальная страница route/rule.

---

## 4. Критерии приёмки

- `go build ./...` без тегов и сборка с lx-тегами (`with_gvisor with_quic with_wireguard with_utls with_clash_api with_xhttp with_awg`) — ок. ✅
- `go vet ./route/... ./option/...` и `gofmt -l` по затронутым файлам — чисто. ✅
- Юнит-тест `route/rule/rule_item_package_name_regex_test.go` зелёный: матч префикса/якоря `$`, матч одного из нескольких пакетов, no-match, nil `ProcessInfo` (без паники), ошибка на невалидном выражении. ✅
- Ребейз-зона: фича — это новый файл + точечные правки в 6 файлах роутинга/опций; коллизий с awg/xhttp/selector/CI-кластерами нет (подтверждено feasibility-разбором).

---

## 5. Вне скоупа

- Полная миграция на 1.14 (отложена до v1.14.0 stable; отдельный feasibility-отчёт).
- Прочие 1.14-матчеры (`source_mac_address`, `source_hostname`, `preferred_by`, DNS response-matching), DNS-экшены `evaluate`/`respond`, rule-set v5, `tls_spoof`.
- Заполнение `AndroidPackageNames` на не-Android платформах (определяется upstream-логикой process-resolver).

---

## 6. Ссылки

- [Upstream commit 941ce58b](https://github.com/SagerNet/sing-box/commit/941ce58b) — исходная апстрим-фича (route/DNS/headless + docs + rule-set v5 хунк).
- [Документация route/rule#package_name_regex](https://sing-box.sagernet.org/configuration/route/rule/#package_name_regex) — «Match android package name using regular expression», since 1.14.0.
- Feasibility-разбор миграции 1.13.13→1.14 (этой сессии) — обоснование точечного бэкпорта вместо полного перехода.
