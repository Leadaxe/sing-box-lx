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
  slot by slot ([§3](lx-config.md#3-urltest-node-selection-modes)). Per slot it also returns
  `Quarantined`, `Connections` and `ConfirmedFailures`.
- **`GetPoolState(groupTag)`** — returns the same slots plus the group-wide `ShiftedKeys`,
  `MarkedKeys`, `Frozen` and `FrozenSeconds`
  ([self-heal](lx-urltest.md#pool-self-heal)).
- **`GetDNSGroups()`** — returns the live state of every DNS `group` server
  ([§5](lx-config.md#5-dns-server-group)): per member `clean` / `liveErrors` / `lastErrorAgeMs` /
  `liveWins` / `current`.
- **`GetRunningConfig()`** — returns the canonical JSON options the running box was built
  from, post-override. The result is an object with a `Content()` accessor: a bare `string`
  return would crash gomobile on android/arm64.
- **`SubscribeDNSQueries(includeAnswers, handler)`** — subscribes to a structured live
  DNS-query stream. Per query it delivers the `domain`, `qtype`, `rcode`, the CNAME chain and
  answers (when `includeAnswers`), process attribution, and `dnsServer` / `dnsServerType` /
  `outbound`. An `rcode` of `-1` means a lookup failure, a first-class state. An empty
  `outbound` means direct/system, also a valid state, not a bug.
- **`GetChains()`** — returns the state of every `chain` outbound
  ([§10](lx-config.md#10-chain-outbound--a-virtual-multi-hop-path-of-groups-and-nodes)): per position the
  resolved node and, for positions ≥ 1, the link instance (`starting|active|idle`, live
  connections, effective MTU and why, what `strip` removed, whether `rewrite` applied, last
  error). It also returns dial, error and link counters.
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
