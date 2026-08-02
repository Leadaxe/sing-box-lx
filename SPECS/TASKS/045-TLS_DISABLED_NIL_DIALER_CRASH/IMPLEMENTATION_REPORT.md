# IMPLEMENTATION_REPORT: 045 — TLS_DISABLED_NIL_DIALER_CRASH

## Изменённые файлы

| Зона | Файл | Что |
|---|---|---|
| `// lx:` upstream | `protocol/trojan/outbound.go` | Гейт `if outbound.tlsConfig != nil` вокруг создания `tlsDialer` (маркер `tls-disabled-dialer`) |
| `// lx:` upstream | `protocol/vless/outbound.go` | То же |
| new files | `protocol/trojan/outbound_tls_disabled_lx_test.go` | Red/green регрессия: disabled → оба поля nil; enabled → оба не nil |
| new files | `protocol/vless/outbound_tls_disabled_lx_test.go` | То же |

Deps / wiring / build-теги — не тронуты. Фикс безусловный: без него сборка
без тегов тоже падает, «поведение upstream» здесь = задуманное, а не фактическое.

## DoD

- `go build ./...` (без тегов) — ок.
- Сборка с тегами: `go build -tags <LX_TAGS минус badlinkname,with_naive_outbound>`
  (go1.25-хост) — ок.
- `go vet ./protocol/trojan ./protocol/vless` — ок; `gofmt -l` пусто.
- `go test ./protocol/trojan ./protocol/vless` — зелёные; red-фаза воспроизведена
  (stash фикса → оба `TestNewOutboundTLSDisabledNoDialer` падают).
- `./sing-box check` принимает конфиг с plain-trojan (`tls.enabled:false`,
  порт из жалобы) и plain-vless нодами.

## Зона конфликтов на следующем мерже

`protocol/trojan/outbound.go`, `protocol/vless/outbound.go` — по одному
`// lx:`-блоку в `NewOutbound`. Условие снятия — в реестре
[HOTFIXES](../../FEATURES/004-HOTFIXES/FEATURE.md): апстрим сам добавит
nil-гейт (как в vmess) или переделает контракт `NewClientWithOptions`.

## Вне scope

- Field-подтверждение на устройстве жалобы (нужен AAR ≥ lx.19-rc.3 в LxBox).
- Issue апстриму не отправлялся (политика фичи 004 — пассивные условия снятия).
- App-side: LxBox эмитит `"tls": {"enabled": false}` в конфиг — легально,
  ядро обязано это переваривать; билдер не меняем.
