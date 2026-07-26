package option

import "github.com/sagernet/sing/common/json/badoption"

type SelectorOutboundOptions struct {
	Outbounds                 []string `json:"outbounds" reference:"outbound"`
	Default                   string   `json:"default,omitempty" reference:"outbound"`
	InterruptExistConnections bool     `json:"interrupt_exist_connections,omitempty"`
}

type URLTestOutboundOptions struct {
	Outbounds                 []string           `json:"outbounds" reference:"outbound"`
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	Tolerance                 uint16             `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
	// lx: SPEC 019 v2 — load-balancing.
	Mode     string                  `json:"mode,omitempty"` // least_test (default) | round_robin
	Balancer *URLTestBalancerOptions `json:"balancer,omitempty"`
	// PassiveCheck (lx: SPEC 019): treat a recent successful TCP dial through a
	// node as proof of liveness (the SYN/SYN-ACK round-trip traversed the whole
	// chain) and skip URL-probing it while the proof is fresh (< interval).
	// least_test: the periodic re-test cycle is skipped entirely while the
	// currently selected node is passively confirmed. round_robin
	// (pool_tolerance == 0): passively confirmed slots are treated as live
	// without a probe. Saves probe traffic and radio wakeups on active groups;
	// the cost is staler delay numbers in the UI. Default false (upstream
	// probing behaviour).
	PassiveCheck bool `json:"passive_check,omitempty"`
}

// URLTestBalancerOptions configures round_robin: a fixed-size pool of live nodes, lazily
// health-checked, with optional per-flow stickiness. lx: SPEC 019 v2. Required only for
// round_robin (defaults {pool:3, pool_tolerance:0} apply when omitted); set with any other
// mode is an error.
type URLTestBalancerOptions struct {
	Pool          int    `json:"pool,omitempty"`           // rotation pool size, default 3, < 1 is an error
	PoolTolerance uint16 `json:"pool_tolerance,omitempty"` // ms; 0 = first-live-fill, > 0 = top-N-by-delay with eviction threshold
	// StickyHash key components: process|domain|source_ip|dest_ip|dest_port.
	// Omitted → default ["process","domain"]. To DISABLE stickiness use ["none"] — a bare [] is
	// NOT honoured because the config decoder (badjson.UnmarshallExcludedContext) re-marshals
	// the struct and collapses an empty array to nil, indistinguishable from "omitted".
	StickyHash []string `json:"sticky_hash,omitempty"`
}
