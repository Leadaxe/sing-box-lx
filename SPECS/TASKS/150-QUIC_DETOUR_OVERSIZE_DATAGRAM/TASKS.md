# TASKS: 150 — QUIC_DETOUR_OVERSIZE_DATAGRAM

Порядок — по [PLAN.md](PLAN.md).

- [x] 1. SPEC (EN + RU) / PLAN / TASKS
- [x] 2. `common/lxdatagram`: `TruncateReads`, `Dialer`, юниты
- [x] 3. Швы: masque (`connectH3WithBudget`), hysteria2, tuic; red/green тест masque
- [x] 4. Тесты четырёх пакетов под `-race`, сборка `LX_TAGS`, gofmt, lx-ci
- [x] 5. Живой прогон на Mac через релеи с UDP (n009 plain/XUDP 4/4 за 20 с; n002 в тот день сам сломался)
- [x] 6. Доки: FEATURE 009, FEATURE 004, индекс, Roadmap
- [x] 7. Loopback-стенд hysteria2/tuic через vless-detour с подброшенной лестницей WARP: до фикса `short buffer`, с фиксом 7/7 (SPEC §3)
- [ ] 8. Устройство репортёра (LxBox) — за репортёром
