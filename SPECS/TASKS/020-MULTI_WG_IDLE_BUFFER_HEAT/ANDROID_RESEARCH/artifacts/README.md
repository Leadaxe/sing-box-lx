# Artifacts — Android idle-suspend run

Raw evidence for [`../RESULTS.md`](../RESULTS.md). Credential-free: the full core log
(which also carried the user's DNS lookups and per-app connection lines) was filtered
down to just the `lx idle:` transitions; goroutine/heap dumps contain only stack
addresses and Go package paths (no IPs, no tokens, no serials).

| File | What | How to read |
|---|---|---|
| `device-core-suspend.jsonl` | the 8 `lx idle: suspend` lines from the device, one JSON object per line | timestamps + message |
| `desktop-rc18-lxidle.log` | the same 8 suspends from the preliminary desktop rc.18 run (cross-check) | plain text |
| `device-goroutine-before.txt` | `pprof goroutine?debug=1` with all 9 endpoints `Up` | `RoutineReceiveIncoming` count = 18 |
| `device-goroutine-after.txt` | same, after 8 suspended | `RoutineReceiveIncoming` count = 2 |
| `device-heap-before.pb` | `pprof heap?gc=1`, all up | `go tool pprof -top -inuse_space device-heap-before.pb` |
| `device-heap-after.pb` | same, 8 suspended | `go tool pprof -top -inuse_space device-heap-after.pb` |
| `heap-before-top.txt` | text render of the before profile | `PopulatePools.func3 = 223.93 MB` |
| `heap-after-top.txt` | text render of the after profile | `PopulatePools.func3 = 89.89 MB` |

Reproduce the delta:

```
go tool pprof -top -inuse_space device-heap-before.pb | grep PopulatePools   # 223.93MB
go tool pprof -top -inuse_space device-heap-after.pb  | grep PopulatePools   #  89.89MB
```

Count recv-workers:

```
grep -c RoutineReceiveIncoming device-goroutine-before.txt   # stacks; sum the leading counts → 18
```
(the `debug=1` format groups goroutines by stack with a leading count, so sum the
`N @ …` headers whose stack contains `RoutineReceiveIncoming`, not the raw line count.)
