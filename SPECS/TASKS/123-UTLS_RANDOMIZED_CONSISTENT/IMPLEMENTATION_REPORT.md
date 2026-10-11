# IMPLEMENTATION REPORT: 123 — UTLS_RANDOMIZED_CONSISTENT

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `common/tls/utls_randomized_lx.go` | `lxRandomPool` = `HelloChrome_Auto`, `HelloFirefox_Auto`, `HelloSafari_Auto`; `lxRandomFingerprints()`; `lxRandomizedFingerprint(seed)` — `HelloRandomizedALPN` с весами R1 |
| `common/tls/utls_client.go` | одна строка в конце `init`: `lxRandomFingerprints() // lx: SPEC 123` |
| `common/tls/utls_randomized_lx_test.go` | `TestLxRandomizedAlwaysHybridAndALPN` (500 семян), `TestLxRandomizedSeedDeterministic`, `TestLxRandomPoolCarriesHybrid` |
| `common/tls/reality_client_lx_test.go` | `random`/`randomized` в `TestLxRealityKeyShareDefaultKeepsHybrid` и `…ClassicalStripsHybrid` — `classical` вырезает гибрид и у них, `AuthKey` из X25519 на месте |

Форк utls не тронут. Апстримные строки `init` не тронуты.

## Проверка

- `gofmt`, `go vet -tags with_utls ./common/tls/`, `go test -tags with_utls -ldflags=-checklinkname=0 -run TestLx ./common/tls/` (go1.26.8) — ok.
- С откатом к `HelloRandomized` без весов 123 и пулом с `edge`/`ios`: `TestLxRandomizedAlwaysHybridAndALPN` падает на первом же семени («hybrid share»), `TestLxRandomPoolCarriesHybrid` — «Edge-85 has no hybrid share».

## Открыто

- Полная сборка `lx-build` и `sing-box check`.
- Стенд Xray v26.9.9 (критерий 3).
