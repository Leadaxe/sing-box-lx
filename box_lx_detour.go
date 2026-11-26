package box

import (
	"reflect"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

// lx: SPEC 165 Д1–Д4 — what `detour` does not apply.
//
// With a detour upstream builds the hop's dialer and never the node's own
// (common/dialer/dialer.go NewWithOptions: Detour != "" → NewDetour, no
// NewDefault), and only NewDefault (common/dialer/default.go) reads the
// socket fields. They do nothing under detour and say nothing. The pre-pass
// does not change that; it takes them out of the effective config with a
// `detour` change, so GetOutboundConfig shows what runs.

// ignoredUnderDetour are the dial fields upstream does not apply under
// detour (Д1). domain_resolver and domain_strategy do apply (Д2).
var ignoredUnderDetour = []string{
	"bind_interface", "inet4_bind_address", "inet6_bind_address", "bind_address_no_port",
	"protect_path", "routing_mark", "reuse_addr", "netns", "connect_timeout",
	"tcp_fast_open", "tcp_multi_path", "disable_tcp_keep_alive", "tcp_keep_alive",
	"tcp_keep_alive_interval", "udp_fragment", "network_strategy", "network_type",
	"fallback_network_type", "fallback_delay",
}

// readByProtocol are the fields a protocol handles itself under detour too
// (Д3): they stay as written.
var readByProtocol = map[string]map[string]bool{
	C.TypeAnyTLS:      {"tcp_fast_open": true},
	C.TypeOpenConnect: {"disable_tcp_keep_alive": true, "tcp_keep_alive": true, "tcp_keep_alive_interval": true},
}

const ignoredUnderDetourReason = "not applied under detour (common/dialer NewWithOptions builds the detour dialer only)"

// showIgnoredUnderDetour applies Д1 to every node with a detour. A node it
// changes gets a fresh copy of its options struct.
func showIgnoredUnderDetour(options *option.Options, changes map[string][]option.LXConfigChange) error {
	outbounds := append([]option.Outbound(nil), options.Outbounds...)
	for i := range outbounds {
		changed, nodeChanges, err := withoutIgnoredDialFields(outbounds[i].Type, outbounds[i].Options)
		if err != nil {
			return E.Cause(err, "outbound ", outbounds[i].Tag)
		}
		if changed != nil {
			outbounds[i].Options = changed
			changes[outbounds[i].Tag] = append(changes[outbounds[i].Tag], nodeChanges...)
		}
	}
	endpoints := append([]option.Endpoint(nil), options.Endpoints...)
	for i := range endpoints {
		changed, nodeChanges, err := withoutIgnoredDialFields(endpoints[i].Type, endpoints[i].Options)
		if err != nil {
			return E.Cause(err, "endpoint ", endpoints[i].Tag)
		}
		if changed != nil {
			endpoints[i].Options = changed
			changes[endpoints[i].Tag] = append(changes[endpoints[i].Tag], nodeChanges...)
		}
	}
	options.Outbounds = outbounds
	options.Endpoints = endpoints
	return nil
}

// withoutIgnoredDialFields returns a copy of options without the fields of
// Д1 and their changes, or nil when there is nothing to take out.
func withoutIgnoredDialFields(typeName string, options any) (any, []option.LXConfigChange, error) {
	wrapper, ok := options.(option.DialerOptionsWrapper)
	if !ok {
		return nil, nil, nil
	}
	dialerOptions := wrapper.TakeDialerOptions()
	if dialerOptions.Detour == "" {
		return nil, nil, nil
	}
	content, err := json.Marshal(dialerOptions.AbstractDialerOptions)
	if err != nil {
		return nil, nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil, nil, err
	}
	var nodeChanges []option.LXConfigChange
	for _, field := range ignoredUnderDetour {
		value, has := fields[field]
		if !has || readByProtocol[typeName][field] {
			continue
		}
		delete(fields, field)
		nodeChanges = append(nodeChanges, option.LXConfigChange{
			Field: field, From: value,
			By: option.LXConfigChangeBy{Type: option.LXChangeByDetour, Reason: ignoredUnderDetourReason},
		})
	}
	if len(nodeChanges) == 0 {
		return nil, nil, nil
	}
	content, err = json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	var kept option.AbstractDialerOptions
	if err := json.Unmarshal(content, &kept); err != nil {
		return nil, nil, err
	}
	// A fresh struct of the same type: the caller's options stay as written.
	value := reflect.ValueOf(options)
	copied := reflect.New(value.Elem().Type())
	copied.Elem().Set(value.Elem())
	dialerOptions.AbstractDialerOptions = kept
	copiedOptions := copied.Interface()
	copiedOptions.(option.DialerOptionsWrapper).ReplaceDialerOptions(dialerOptions)
	return copiedOptions, nodeChanges, nil
}
