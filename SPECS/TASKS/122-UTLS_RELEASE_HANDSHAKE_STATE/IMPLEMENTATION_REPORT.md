# IMPLEMENTATION REPORT: 122 — UTLS_RELEASE_HANDSHAKE_STATE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

## Что сделано

| Файл | Содержимое |
|------|-----------|
| `submodules/utls/u_conn_release_lx.go` (форк, восьмой коммит `ad69b19`) | `(*UConn).ReleaseHandshakeState()`: no-op до `isHandshakeComplete` и при `vers != TLS13 && Renegotiation != Never`; иначе `clientHelloSpec`, `echCtx`, `ech` = nil |
| `submodules/utls/u_conn_release_lx_test.go` | Chrome/Firefox/Safari против `crypto/tls`: до рукопожатия не освобождает, после — да, повторный `Handshake` и `Write` работают; TLS 1.2 с ренегоциацией пресета — состояние остаётся, TLS 1.3 — освобождается |
| `common/tls/utls_release_lx.go` | `lxUTLSConn` (встраивает `utlsALPNWrapper` с пустым списком; `nextProtos`, `alpnApplied`, `access sync.Mutex`), `HandshakeContext` под мьютексом: однократная подмена ALPN (`lxApplyALPN` — логика апстримной обёртки), рукопожатие, при успехе `releaseHandshakeState`. `releaseHandshakeState`: только TLS 1.3 — `Hello`, `ServerHello`, `State13`, `Extensions` = nil, затем метод форка через проверку интерфейса |
| `common/tls/utls_client.go` | `Client` → `newLXUTLSConn(…)` (одна `// lx:`-строка) |
| `common/tls/reality_client.go` | `releaseHandshakeState(uConn)` после проверки `verified` (одна `// lx:`-строка) |
| `common/tls/utls_release_lx_test.go` | ALPN на проводе (`h2`, `http/1.1`, пусто → список пресета, `randomized`), освобождение, повторный `HandshakeContext`, данные; два конкурентных `HandshakeContext`; неудачное рукопожатие — состояние цело, флаг стоит, повтор — ошибка; TLS 1.2 — состояние остаётся; REALITY end-to-end через `NewRealityServer` (dest — локальный TLS-сервер): освобождение и эхо; неверный ключ — ошибка без паники |
| `go.mod` (комментарий `lx:begin utls-firefox148`), `docs-lx/lx-release-runbook{,.ru}.md` §1.1, FEATURE 017 | восемь коммитов форка |

Апстримный `utlsALPNWrapper.HandshakeContext` не тронут.

## Отступление от спеки

Страж ренегоциации заменён условием «только TLS 1.3» (R1a): браузерные пресеты сами ставят
`Config.Renegotiation = RenegotiateOnceAsClient` (`u_tls_extensions.go:1700-1706`), и страж
«`Renegotiation != Never`» выключил бы освобождение для `chrome`/`firefox`/`safari`. Найдено при
реализации, подтверждено финальным ревью.

## Проверка

- Форк: `go vet .`, `go test -race -run TestReleaseHandshakeState .`, полный `go test .` — ok.
- Ядро: `gofmt`, `go vet -tags with_utls ./common/tls/`; тесты 122 под `-race` ×5; полный `go test -tags with_utls ./common/tls/` (стражи 083/086/088/089/118/123) — ok.
- Замер (временный тест, в репо нет): 200 живых uTLS-соединений через loopback, ALPN `h2`, сервер в том же процессе (абсолют включает его), разница апстримная обёртка → 122:

  | Пресет | Апстрим | 122 | Разница |
  |---|---|---|---|
  | Chrome 133 | 42 102 Б | 16 605 Б | −25,5 КБ |
  | Firefox 148 | 36 085 Б | 18 415 Б | −17,7 КБ |

- Ревью кода (Fable, три прохода: дизайн, редакция ALPN, реализация) — «можно сдавать»; правки (REALITY-кейс ошибки, комментарий, фиксация R1a) внесены.

## Открыто

- `make -f Makefile.lx lx-build` полным набором тегов.
- Стенд Xray ≥ v26.9.8 (критерий 5).
