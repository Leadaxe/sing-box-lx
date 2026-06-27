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
	// lx: SPEC 019 — load-balancing mode + per-flow stickiness.
	Mode   string                `json:"mode,omitempty"` // least_test (default) | round_robin | least_connection
	Sticky *URLTestStickyOptions `json:"sticky,omitempty"`
}

// URLTestStickyOptions binds one flow to one node when Mode is round_robin/least_connection.
// lx: SPEC 019.
type URLTestStickyOptions struct {
	Mode    string             `json:"mode,omitempty"`    // jumphash (default) | ttlmap
	Timeout badoption.Duration `json:"timeout,omitempty"` // ttlmap entry TTL, default 10m (ignored for jumphash)
	Cap     int                `json:"cap,omitempty"`     // ttlmap max entries, default 2000 (ignored for jumphash)
	Hash    []string           `json:"hash,omitempty"`    // key components: process|domain|source_ip|dest_ip|dest_port
}
