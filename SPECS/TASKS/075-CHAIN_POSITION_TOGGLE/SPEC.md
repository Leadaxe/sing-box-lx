# SPEC 075 — Chain position toggle: runtime enable/disable of chain hops

**Feature:** [CHAIN](../../FEATURES/015-CHAIN/FEATURE.md)

| Field | Value |
|-------|-------|
| Type | F (feature) — runtime control surface over the `chain` outbound (SPEC 073), owner request 2026-08-25 |
| Status | I (implemented) — core + RPC + libbox + unit tests green; no field run yet |
| Branch | `lx` |
| Build-tag | `with_lx_chain` (core) · `with_lx_command` (RPC surface) |
| Related | [[SPECS/TASKS/073-CHAIN_OUTBOUND]] (the chain itself; hop-tag contract untouched) · [[SPECS/TASKS/014-CLASH_API_TO_COMMANDCLIENT_MIGRATION]] / [[SPECS/TASKS/015-COMMAND_PROTOCOL_RPC_EXTENSIONS]] (the gRPC plane the new methods live on) · [[SPECS/TASKS/037-RUNNING_CONFIG_RPC]] (serialization model reused by the clone-config snapshot) |

## 0. TL;DR

Every chain position gets an independent runtime on/off switch, controlled over
the lx gRPC plane (`SetChainPositionEnabled`). A disabled position ≥ 1 becomes a
passthrough to the previous hop (the mechanism `direct` transparency already
uses); a disabled position 0 makes hop 0 dial the real network through a plain
direct dialer. Any combination is valid — with everything disabled the chain
degenerates into `direct`. The disabled set persists in the cache-file (keyed by
position tags, like a selector's `selected`). Existing connections are
interrupted on every toggle following the selector model: a new
`interrupt_exist_connections` chain option gates whether external (user)
connections are ripped too. Enabling a position actively warms its link (clone)
when the position's choice is deterministic; a warm-up failure does NOT roll the
flag back — it is reported in the RPC response (`warmupError`) and in
`GetChains`. Diagnostics: `ChainPosition.disabled` in `GetChains` (positions are
always listed, disabled ones included), and a new `GetChainCloneConfig` RPC
returns the effective post-transform JSON of a live link (strip/rewrite/MTU/
detour applied) — snapshotted at clone creation, RunningConfig-style.

## 1. Problem

SPEC 073 shipped the chain as a static packet-order list of positions. The only
runtime path control is switching a selector inside a position; turning a hop
*off* requires either a config edit + reload or a `[node, direct]` selector
wrapper around every position. The owner wants first-class toggles: any hop —
including the entry — can be excluded from the path at runtime from the UI,
with the state surviving restarts and with full visibility of what is disabled.

## 2. Semantics

1. **Positions toggle independently.** `SetChainPositionEnabled(chain, index,
   enabled)`; index is packet order, `0` = entry.
2. **Disabled position ≥ 1** — dials go straight to hop `i−1`. The group at the
   position is not consulted; no clone is created or picked.
3. **Disabled position 0** — hop 0 dials the destination over the real network
   via a default direct dialer (interface binding / routing marks apply, like
   the `direct` outbound with empty dial options). Clones above keep their
   `detour` on the *hop tag*, so nothing is rebuilt.
4. **Any combination is valid.** All positions disabled = the chain behaves as
   `direct`. There is no "at least one enabled" invariant.
5. **Persistence** — the set of disabled position *tags* is stored in the
   cache-file per chain tag (lx bucket `chain_disabled_lx`). Tags, not indices:
   a config edit that reorders positions keeps the right hops disabled; a
   stored tag that left the chain is ignored. No cache-file service in the
   config → the toggle state is ephemeral, mirroring selector `selected`.
6. **Existing connections** — every effective toggle (both directions) calls
   `interruptGroup.Interrupt(interrupt_exist_connections)` on the chain's
   top-level connections, exactly like a selector switch. With the flag unset
   (default), external/user connections survive on the old path; the retired
   clone is picked up by the normal idle eviction once its connections drain.
   Clones are never force-closed by the toggle.
7. **Warm-up on enable** — for a position ≥ 1 whose choice is deterministic
   (leaf, or selector chain via `Now()`), the link is created immediately,
   reusing the SPEC 073 preload logic. urltest positions and
   `direct`/`block` leaves stay lazy. Warm-up failure does not roll back the
   flag: the toggle expresses user intent, node health is reported separately —
   in the RPC response (`warmupError`) and via hop `errors`/`lastError` in
   `GetChains`.
8. **Startup** — the disabled set is loaded and applied after position resolve
   and before preload; preload skips disabled positions.
9. **Observability** — `ChainStatus.Positions[i].Disabled`; `ChainPath()` skips
   disabled positions (an all-disabled chain yields an empty path); Clash API
   `chain` object inherits the field via JSON.

## 3. Config contract

```json
{
  "type": "chain",
  "tag": "chain-2",
  "outbounds": ["entry", "middle", "exit"],
  "interrupt_exist_connections": false
}
```

One new option: `interrupt_exist_connections` (default `false`), selector
semantics — internal connections are always interrupted on a toggle, external
ones only when the flag is set.

## 4. RPC contract (`daemon.StartedService`, lx block)

```proto
rpc SetChainPositionEnabled(SetChainPositionEnabledRequest) returns (SetChainPositionEnabledResponse) {}
rpc GetChainCloneConfig(GetChainCloneConfigRequest) returns (RunningConfig) {}

message SetChainPositionEnabledRequest {
  string chainTag = 1;
  int32 position = 2;   // packet order, 0 = entry
  bool enabled = 3;
}
message SetChainPositionEnabledResponse {
  string warmupError = 1;  // "" = ok or warm-up not applicable
}
message GetChainCloneConfigRequest {
  string chainTag = 1;
  int32 position = 2;
}
// ChainPosition (SPEC 073) gains:
//   bool disabled = 7;
```

Status errors are reserved for genuine call failures: `NotFound` (no such
chain / no live clone at the position), `InvalidArgument` (index out of range),
`FailedPrecondition` (service not started), `Unimplemented` (stub build). A
warm-up failure is data, not a status error — the flag HAS been applied.

`GetChainCloneConfig.content` is the effective options JSON of the live link at
the position's currently resolved leaf: `{type, tag, …options}` after
strip → rewrite → MTU → detour. Snapshotted once at clone creation
(RunningConfig model, SPEC 037): a re-marshal of the parsed struct, compare
semantically, not textually. Secrets appear as-is — this mTLS plane already
serves the full config via `GetRunningConfig`.

## 5. Implementation map

- `adapter/chain_lx.go` — `ChainController` (toggle + clone-config accessors),
  `ChainDisabledStore` (cache-file extension contract),
  `ChainPositionStatus.Disabled`.
- `option/chain_lx.go` — `interrupt_exist_connections`.
- `experimental/cachefile/chain_lx.go` — `chain_disabled_lx` bucket (JSON array
  of tags per chain tag); bucket registered in `bucketNameList`.
- `protocol/chain/chain.go` — disabled flags, interrupt group + connection
  handlers (external-connection marking, selector model), direct fallback
  dialer for hop 0, `SetPositionEnabled` (persist → interrupt → warm-up),
  `CloneConfigJSON`, load-at-start, preload/ChainPath/ChainStatus awareness.
- `protocol/chain/hop.go` — disabled short-circuits in `DialContext` /
  `ListenPacket` / `ResolveLeaf`.
- `protocol/chain/clone.go` — effective-config snapshot at creation.
- `daemon/started_service.proto` + regenerated pb — messages/RPCs above.
- `daemon/started_service_chain_lx.go` (+ `_stub.go`) — handlers.
- `experimental/libbox/command_client_chain_lx.go` — `ChainPosition.Disabled`,
  `SetChainPositionEnabled` (object result — SPEC 037 gomobile rule),
  `GetChainCloneConfig`.

## 6. Invariants

- The `<chain>#<i>` hop-tag contract (SPEC 073) is untouched. Probing a hop
  whose position is disabled measures the path *without* it — clients must
  render the disabled state alongside probe results.
- The toggle never mutates config, never rebuilds clones of other positions,
  and never force-closes a clone; resource release rides the existing eviction.
- MTU of clones above a disabled tunnel hop stays computed for the full chain —
  conservative, correct, slightly suboptimal until the clone is next rebuilt.

## 7. Testing

Unit (`protocol/chain`, fake nodes, no network): disable middle / entry / all
(direct degeneration), re-enable with warm-up, disabled position skipped by
preload and `ChainPath`, persistence round-trip through a fake
`ChainDisabledStore`, interrupt on toggle honoring the external flag,
`ChainStatus.Disabled`, `CloneConfigJSON` content. Acceptance: existing
`lx-test/chain` stand still green; field run on live WG links — pending.
