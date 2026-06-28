package option

import "github.com/sagernet/sing/common/json/badoption"

type SelectorOutboundOptions struct {
	Outbounds                 []string `json:"outbounds"`
	Default                   string   `json:"default,omitempty"`
	InterruptExistConnections bool     `json:"interrupt_exist_connections,omitempty"`
}

type URLTestOutboundOptions struct {
	Outbounds                 []string           `json:"outbounds"`
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	Tolerance                 uint16             `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
	// lx: SPEC 019 v2 — load-balancing.
	Mode     string                  `json:"mode,omitempty"` // least_test (default) | round_robin
	Balancer *URLTestBalancerOptions `json:"balancer,omitempty"`
}

// URLTestBalancerOptions configures round_robin: a fixed-size pool of live nodes, lazily
// health-checked, with optional per-flow stickiness. lx: SPEC 019 v2. Required only for
// round_robin (defaults {pool:3, pool_tolerance:0} apply when omitted); set with any other
// mode is an error.
type URLTestBalancerOptions struct {
	Pool          int    `json:"pool,omitempty"`           // rotation pool size, default 3, < 1 is an error
	PoolTolerance uint16 `json:"pool_tolerance,omitempty"` // ms; 0 = first-live-fill, > 0 = top-N-by-delay with eviction threshold
	// StickyHash key components: process|domain|source_ip|dest_ip|dest_port.
	// nil (field omitted) → default ["process","domain"]; [] (explicit) → stickiness off.
	StickyHash []string `json:"sticky_hash,omitempty"`
}
