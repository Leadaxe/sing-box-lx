# PLAN: 115 — TAILSCALE_PEER_PATH_STATUS

1. **Источник** — `ipnlocal.LocalBackend.Status()` (форк tailscale v1.102.1-sing-box-1.14-mod.5): `PeerStatus.CurAddr` / `PeerRelay` / `Relay` / `LastWrite` / `LastHandshake`, `Status.Health`. Форк не трогается (go.mod-зависимость).
2. **Контракт** согласован с сессией лаунчера «Пути пиров и соединений в Tailscale» и утверждён владельцем: поля 18–22 в `TailscalePeer`, `health = 16`, enum, унарный `GetTailscaleStatus(endpointTag)`; тика в потоке нет.
3. **Слои**: `adapter` (константы, интерфейс, поля в `lx:`-блоках) → `protocol/tailscale` (общий сборщик, вердикт) → `daemon` (RPC + заглушка + маппинг enum) → libbox (клиент, поля, `Health()`) → CLI (`api tailscale peers`).
4. **Тесты**: вердикт над `ipnstate.PeerStatus`; daemon с фейковым провайдером (менеджер endpoint'ов в контексте инстанса, как `resolveTailscaleEndpoint` ждёт); libbox с фейковым gRPC-клиентом.
5. **Живой прогон** — на tailnet владельца через лаунчер после rc.2 (ядро само тайлнет не поднимает).
6. **Доки**: SPEC, CONSUMERS, FEATURE 006, Roadmap, `lxd-grpc-api(.ru).md`, changelog.
