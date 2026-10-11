# PLAN: 122 — UTLS_RELEASE_HANDSHAKE_STATE

**Фича:** [REALITY](../../FEATURES/017-REALITY/FEATURE.md) · контракт — [SPEC.md](SPEC.md)

План под вариант B (§4 SPEC, решение владельца 2026-10-11) и однократной подмене ALPN в первом `HandshakeContext` lx-типа (§3.1).

## Файлы

| Файл | Что меняется |
|------|--------------|
| `common/tls/utls_release_lx.go` (новый, `with_utls`) | `lxUTLSConn` — встраивает `utlsALPNWrapper` с пустым `nextProtocols`; поля `nextProtos`, `alpnApplied`, `access sync.Mutex`. `HandshakeContext` под `access`: при `!alpnApplied && len(nextProtos) > 0` — `BuildHandshakeState` → подмена `ALPNExtension.AlpnProtocols` → `BuildHandshakeState` (ошибка — вернуть, флаг не ставить), `alpnApplied = true`; затем `UConn.HandshakeContext(ctx)`, при `nil` — `releaseHandshakeState`. `releaseHandshakeState(uConn *utls.UConn, config *utls.Config)`: no-op при `config.Renegotiation != RenegotiateNever` (R1a); иначе обнуляет `HandshakeState.Hello`, `.ServerHello`, `.State13`, `Extensions`; затем `if r, ok := any(uConn).(interface{ ReleaseHandshakeState() }); ok { r.ReleaseHandshakeState() }` — метод форка, если он есть. Комментарий: что отпускается, почему безопасно (§1 SPEC), кто не должен читать состояние после рукопожатия |
| `common/tls/utls_client.go` | `UTLSClientConfig.Client`: `// lx: SPEC 122` — `return &lxUTLSConn{utlsALPNWrapper: utlsALPNWrapper{utlsConnWrapper{utls.UClient(conn, c.config.Clone(), c.id)}, nil}, nextProtos: c.config.NextProtos}, nil` вместо `&utlsALPNWrapper{…}`. `utlsALPNWrapper.HandshakeContext` не меняется |
| `common/tls/reality_client.go` | `ClientHandshake`: одна строка `releaseHandshakeState(uConn)` перед `return &realityClientConnWrapper{uConn}` (`// lx: SPEC 122`) |
| `common/tls/utls_release_lx_test.go` (новый, `with_utls`) | Критерий 1 SPEC: uTLS+ALPN на `net.Pipe` против `crypto/tls`-сервера в горутине; REALITY через `utls.RealityServer` с `DialContext` на внутренний TLS-сервер; проверка полей, данные, второй `HandshakeContext`; ALPN на проводе для `NextProtos` = `["h2"]`, `["http/1.1"]` и пустого — как до задачи; неуспешное рукопожатие состояние не трогает |
| `submodules/utls/u_conn_release_lx.go` (новый, форк) | `func (uconn *UConn) ReleaseHandshakeState()`: `clientHelloSpec = nil`, `echCtx = nil` (и, по решению владельца, `HandshakeState = PubClientHandshakeState{}`); no-op при `config.Renegotiation != RenegotiateNever`; тест в `u_conn_release_lx_test.go` |
| `go.mod` | абзац `lx:begin utls-firefox148`: «seven commits» → восемь, строка про новый файл |
| `docs-lx/lx-release-runbook.{ru.,}md` §1.1 | ожидаемое число строк `git log` форка: 7 → 8 |
| `SPECS/FEATURES/017-REALITY/FEATURE.md` | «Особенности сопровождения»: восемь коммитов; строка 122 в таблице задач |
| `SPECS/README.md`, `docs-lx/lx-changelog.md` | строка 122 в Roadmap; пункт в секции текущего релиза |

## Порядок коммитов (IMPLEMENTATION_PROMPT §3.2)

1. форк: новый файл + тест → push форка **до** суперпроекта;
2. ядро: `utls_release_lx.go` + тест (новые файлы);
3. ядро: `// lx:` строки в `utls_client.go` (`Client`) и `reality_client.go` (`ClientHandshake`) — один коммит;
4. гитлинк + `go.mod` + раннбук + FEATURE/Roadmap/changelog.

## Что не меняется

- `prepareClientHello`, `AuthKey`, `key_share`, фрагментация, ALPN — провод прежний; стражи 083/086/088/089/118 без правок.
- Путь ошибки рукопожатия (REALITY fallback, `realityClientFallback`) — состояние остаётся.
- `std_client.go`, ECH-обёртка, kTLS — не трогаются (ECH и kTLS получают освобождение через обёрнутый uTLS-клиент).

## Проверка

- `gofmt -l` по lx-файлам; `go vet`; `go test -ldflags=-checklinkname=0 -tags with_utls ./common/tls/`.
- Форк: `go test -run Release .` и полный `go test .`.
- Повтор замера §2 SPEC прототипом из scratchpad (`bench/sb`, переменные `LXB_ADDR`, `LXB_REALITY_BIN`) на собранном дереве — без `releasePodkop`, так как освобождение теперь внутри ядра: ожидание ≤ 10,5 КиБ на REALITY `chrome`.
- Стенд Xray ≥ v26.9.8 — критерий 5 SPEC.
