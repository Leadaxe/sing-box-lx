# TASKS: 115 — TAILSCALE_PEER_PATH_STATUS

- [x] 1. Контракт с лаунчером: поля, enum, унарный вызов, без тика — согласован и утверждён владельцем 2026-10-07
- [x] 2. proto: `GetTailscaleStatus`, `TailscaleStatusRequest`, `TailscalePeer` 18–22, `TailscaleEndpointStatus.health`, enum `TailscalePeerPath`; регенерация
- [x] 3. `adapter`: константы пути, `TailscaleStatusProvider`, поля в `lx:`-блоках
- [x] 4. `protocol/tailscale`: общий сборщик, `TailscaleStatus()`, `fillTailscalePeerPath`, `Health`
- [x] 5. daemon: обработчик + заглушка + маппинг enum; поля в конвертере потока
- [x] 6. libbox: `GetTailscaleStatus`, поля `TailscalePeer`, `Health()`
- [x] 7. CLI `sing-box api tailscale peers`
- [x] 8. Тесты: `protocol/tailscale`, `daemon`, `experimental/libbox` с `-race` и `LX_TAGS`; `go build ./...` без тегов
- [x] 9. SPEC, PLAN, TASKS, CONSUMERS, отчёт; FEATURE 006; Roadmap; `lxd-grpc-api(.ru).md`; changelog
- [ ] 10. Живой прогон на tailnet через лаунчер после rc.2 → D
- [ ] 11. Следующая спека: `Connection.tailscalePeerID` через `PeerForIP` (после ресинка daemonpb у лаунчера)
