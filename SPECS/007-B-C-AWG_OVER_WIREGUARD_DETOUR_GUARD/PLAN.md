# PLAN: 007 — AWG_OVER_WIREGUARD_DETOUR_GUARD

## Архитектура решения

Guard живёт в **ленивом резолве detour** (`common/dialer/detour.go`
`DetourDialer.init()`), рядом с upstream-проверкой `empty direct outbound`. Это
даёт **вариант B** тем же механизмом, что и empty-direct: ошибка кэшируется в
`initErr` (через `sync.Once`), `DialContext`/`ListenPacket` возвращают её вместо
соединения. Ядро стартует без ошибок, прочие узлы работают, AWG-нода фейлит
каждый dial. Не fatal на старте.

### Откуда «источник AWG»
`DetourDialer` сам по себе не знает тип владельца. Прокидываем флаг:
`dialer.Options.IsAmneziaWG` → `NewDetour(..., ownerIsAmneziaWG)`.
`protocol/wireguard.NewEndpoint` выставляет его из `options.AmneziaWGOptions.IsSet()`.

### Как «цель WireGuard»
Резолвленная detour-цель приводится к `adapter.Outbound`; триггер —
`Type() == C.TypeWireGuard` (покрывает и плоский WG, и AWG — тип один). Группы
(`adapter.OutboundGroup`) раскрываются рекурсивно через `All()` +
`OutboundManager.Outbound(tag)`, с set'ом посещённых против циклов.

## Изменённые файлы

| Файл | Зона | Что |
|------|------|-----|
| `common/dialer/detour.go` | `// lx:` upstream | поле `ownerIsAmneziaWG`, guard в `init()`, рекурсивный `detourTargetIsWireGuard`, новый параметр `NewDetour` |
| `common/dialer/dialer.go` | `// lx:` upstream | поле `Options.IsAmneziaWG`, проброс в `NewDetour` |
| `protocol/wireguard/endpoint.go` | `// lx:` upstream (AWG-шов) | `IsAmneziaWG: options.AmneziaWGOptions.IsSet()` в `dialer.Options` |
| `common/dialer/awg_detour_guard_test.go` | **новый lx-файл** | unit-тест матрицы + init-пути |

## Логика guard (как реализовано)

```go
// detour.go init(), после empty-direct:
if d.ownerIsAmneziaWG && detourTargetIsWireGuard(mgr, dialer, map[string]bool{}) {
    d.initErr = E.New("amneziawg endpoint cannot detour through a wireguard-based "+
        "endpoint (detour: ", d.detour, "): AmneziaWG inside a WireGuard tunnel "+
        "hangs the kernel on Android; use a non-wireguard detour (e.g. vless)")
    return
}

func detourTargetIsWireGuard(mgr, ob, visited) bool {
    if o, ok := ob.(adapter.Outbound); ok && o.Type() == C.TypeWireGuard { return true }
    if g, ok := ob.(adapter.OutboundGroup); ok {
        for _, tag := range g.All() {
            if visited[tag] { continue }
            visited[tag] = true
            if m, loaded := mgr.Outbound(tag); loaded && detourTargetIsWireGuard(mgr, m, visited) { return true }
        }
    }
    return false
}
```

## DoD (IMPLEMENTATION_PROMPT §2) — факт

- [x] `go build ./...` без тегов — ок.
- [x] `go build -tags "...,with_awg" ./cmd/sing-box` — ок.
- [x] `go test ./common/dialer/...` — зелёный (8 подтестов).
- [x] `gofmt -l` изменённых файлов — пусто.
- [x] `go vet ./common/dialer/... ./protocol/wireguard/...` — чисто.

## Зона касания upstream (для ребейза)

- `common/dialer/detour.go`, `common/dialer/dialer.go`,
  `protocol/wireguard/endpoint.go` — upstream-файлы, правки только в `// lx:`
  блоках. Конфликт на ребейзе — лишь если upstream перепишет `DetourDialer.init`
  / сигнатуру `NewDetour` / `dialer.Options` / конструктор endpoint.
- `awg_detour_guard_test.go` — lx-собственный, конфликтов не даёт.
- Сигнатура `NewDetour` расширена 4-м параметром — единственный внешний вызов в
  `dialer.go` обновлён; других вызовов в дереве нет.
