# sing-box-lx — observability: CommandClient extensions

> RU: [lx-observability.ru.md](lx-observability.ru.md) · Feature: [OBSERVABILITY](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md)

These are client-API additions, not config: methods on libbox's `CommandClient` (the native
gRPC management channel). All of them are gated behind `with_lx_command` and called by LxBox.
They add nothing to a sing-box config file: the methods appear in a build with the tag, and
the client calls them. Without the tag the methods are absent and the daemon serves only the
upstream command set. Details are in the
[OBSERVABILITY feature](../SPECS/FEATURES/006-OBSERVABILITY/FEATURE.md).

The methods the fork adds to `CommandClient`:

- **`URLTestOutbound(tag, link, timeout)`** — synchronously measures the latency of a single
  outbound or endpoint on demand, outside a group's periodic test.
- **`GetRules()`** — returns a snapshot of the routing rule table: route rules and DNS rules.
- **`GetGroups()`** — returns a snapshot of the outbound groups; the group stream pushes the
  same data.
- **`GetOutbounds()`** — returns the flat outbound and endpoint list. It is needed alongside
  `GetGroups` because standalone outbounds are not in any group. A WG/AWG endpoint's item also
  carries `EndpointState` (`never_built` / `building` / `up` / `asleep` / `torn_down` / `down`
  / `disabled`) and `IdleSinceSeconds`, the time since its last dial. Both are empty or 0 for
  other outbounds ([lx-energy.md §11](lx-energy.md#11-lazy-build-and-the-build-budget-keep-devices-from-being-built-for-nothing)).
  The `disabled` state is set by `SetEndpointEnabled` (below;
  [lx-energy.md §12](lx-energy.md#12-a-manual-switch-turns-an-endpoint-off-without-a-config-change)).
- **`GetPool(groupTag)`** — returns a `urltest` group's current round_robin rotation pool,
  slot by slot ([lx-urltest.md](lx-urltest.md)). Per slot it also returns
  `Quarantined`, `Connections` and `ConfirmedFailures`.
- **`GetPoolState(groupTag)`** — returns the same slots plus the group-wide `ShiftedKeys`,
  `MarkedKeys`, `Frozen` and `FrozenSeconds`
  ([self-heal](lx-urltest.md#pool-self-heal)).
- **`GetDNSGroups()`** — returns the live state of every DNS `group` server
  ([lx-dns-group.md](lx-dns-group.md)): per member `clean` / `liveErrors` / `lastErrorAgeMs` /
  `liveWins` / `current`.
- **`GetRunningConfig()`** — returns the canonical JSON options the running box was built
  from, post-override. The result is an object with a `Content()` accessor: a bare `string`
  return would crash gomobile on android/arm64.
- **`GetEffectiveConfig()`** — returns the config after the lx pre-pass, in the same canonical
  form as `GetRunningConfig`: chain hops (type `chain-hop`) added, `detour` of the nodes on chain
  positions rewritten, MTU aligned, `lx.detour` written out expanded
  ([SPEC 165](../SPECS/TASKS/165-CHAIN_DETOUR_REWRITE/SPEC.md)). Same `Content()` object.
- **`GetOutboundConfig(tag)`** — returns one outbound, endpoint, group or chain hop as a JSON
  object `{tag, original, effective, changes}`: `original` as in `GetRunningConfig`, `effective`
  as in `GetEffectiveConfig`. Each change is `{field, from, to, by}`: `field` is a dotted path
  (`tls.server_name`), `from` is absent when the field was absent, `to` is absent when the field
  was removed, and `by` names the cause — `type` (`chain`, `chain_rewrite`, `lx.detour`, `mtu_align`) with
  `chain`, `position`, `node_type`, `mode`, `below`, `overhead`, `reason` as they apply. Changes go
  in the order they were made (`chain` → `chain_rewrite` → `mtu_align`); applied to `original`
  they give `effective`. A hop has only `effective`. An unknown tag is an error (`NotFound` over
  gRPC).

  ```json
  {
    "tag": "NL-FREE#114",
    "original":  { "type": "wireguard", "tag": "NL-FREE#114", "detour": "o-ru" },
    "effective": { "type": "wireguard", "tag": "NL-FREE#114", "detour": "c-proton#0" },
    "changes": [
      { "field": "detour", "from": "o-ru", "to": "c-proton#0",
        "by": { "type": "chain", "chain": "c-proton", "position": 1 } }
    ]
  }
  ```
- **`SubscribeDNSQueries(includeAnswers, handler)`** — subscribes to a structured live
  DNS-query stream. Per query it delivers the `domain`, `qtype`, `rcode`, the CNAME chain and
  answers (when `includeAnswers`), process attribution, and `dnsServer` / `dnsServerType` /
  `outbound`. An `rcode` of `-1` means a lookup failure, a first-class state. An empty
  `outbound` means direct/system, also a valid state, not a bug.
- **`GetChainStatus(tag)`** — returns the state of one `chain` outbound
  ([lx-chain.md](lx-chain.md)): per position the tag, whether it is a group, the node it resolves
  to now, `transparent` (a `direct` on position ≥ 1, the dial goes to the hop below), `disabled`
  and its dial errors; plus the chain's dial and error counters.
- **`SetChainPositionEnabled(chain, position, enabled)`** — switches one chain position off or on
  at runtime; returns only an error.
- **`SetEndpointEnabled(tag, enabled)`** — switches a WG/AWG endpoint off or on at runtime
  ([lx-energy.md §12](lx-energy.md#12-a-manual-switch-turns-an-endpoint-off-without-a-config-change)). Returns an
  `EndpointToggleResult` object whose `State` is the endpoint state after the call.

The connection stream is extended too: a tracked `Connection` carries a separate
**`detourList`** field, the transport-detour tail of the final outbound. It is exposed
separately from the proxy `chain`, because the `chain` omits the detour.

The methods appear in a build with the tag:

```sh
make -f Makefile.lx lx-build   # includes with_lx_command (and with_xhttp/with_awg)
```

From the command line, `sing-box api group pool` shows a group's pool over the same channel:

```sh
sing-box api group pool auto     # slots, state, connections and failures of the round_robin pool of group auto
```
