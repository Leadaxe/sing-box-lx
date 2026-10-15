# PLAN: 123 — UTLS_RANDOMIZED_CONSISTENT

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Файлы

| Файл | Что меняется |
|------|--------------|
| `common/tls/utls_randomized_lx.go` | `lxRandomPool` (chrome/firefox/safari), `lxRandomFingerprints()` — перевыбор `randomFingerprint` и сборка `randomizedFingerprint` на апстримном seed, `lxRandomizedFingerprint(seed)` — `HelloRandomizedALPN`, `DefaultWeights` + TLS 1.3, без P-256 первым шаром, `CurveIDs_Append_X25519 = 1`, `KeyShare_Append_RandomGroups = 1` |
| `common/tls/utls_client.go` | `lxRandomFingerprints()` последней строкой `init` (`// lx: SPEC 123`) |
| `common/tls/utls_randomized_lx_test.go` | 500 семян (гибрид в группах и шарах по разу, перед X25519; ALPN по умолчанию и из `NextProtos`), детерминизм seed, пул `random` |
| `common/tls/reality_client_lx_test.go` | `random`, `randomized` в списках `DefaultKeepsHybrid` и `ClassicalStripsHybrid` |
| FEATURE 017, `docs-lx/protocols-transports{,.ru}.md` §5.2, `SPECS/README.md`, `docs-lx/lx-changelog.md` | строки `random`/`randomized` |

## Почему строка в конце `init`, а не замена трёх строк

Апстримные строки `init` остаются как есть — синк не конфликтует с ними; lx-строка перезаписывает
обе переменные после апстримного выбора. Цена — апстримный `rand.Intn` для пула и сборка весов
выполняются впустую один раз при старте.

## Проверка

- `gofmt`, `go vet -tags with_utls ./common/tls/`, `go test -tags with_utls -ldflags=-checklinkname=0 ./common/tls/`.
- Откат весов и пула к апстримным — стражи падают.
