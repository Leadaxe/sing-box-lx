# TASKS: 135 — LXD_HOOKS

**Feature:** [014-LXD_DAEMON](../../FEATURES/014-LXD_DAEMON/FEATURE.md) · plan — [PLAN.md](PLAN.md)

- [x] 1. SPEC approved by the owner 2026-10-17; English primary, Russian copy
- [x] 2. `hooks` key + validation; `Options.Hooks`; runner: directory, stubs, "not configured", gate, outcomes, `state.json`, `recover`
- [x] 3. Unix execution: process group, output to file, `flock`, timeout → `SIGKILL`; Windows stub
- [x] 4. Events at `bootstrap`/`Apply`/`Rollback`/`Start`/`Stop`/`shutdown`; `hooks` in `/admin/status`; `InterfaceName()` accessor and name collection
- [x] 5. Tests of §3 (config, stubs, events, failures, timeout, fatal, rollback, state.json, output, gate, flock, accessor); `go vet` for three OSes
- [x] 6. OpenWrt `term_timeout 30` + README paragraph; lxd-daemon EN/RU (§3, Hooks, §10); changelog; FEATURE wording check
- [x] 7. IMPLEMENTATION_REPORT; SPEC status → I (both files)
- [ ] 8. Manual owner checks (§3): Mac launcher DNS round trip, `kill -9` → `post_stop(recover)`, RouteRich `update` without overlap
