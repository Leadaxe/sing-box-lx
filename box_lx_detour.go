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
// does not change that: the options go to upstream as written. Only the
// effective view leaves them out, with an `lx.detour` change, so
// GetOutboundConfig shows what runs. After an upstream merge, check this list
// against common/dialer (lx-release-runbook §3).

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
	C.TypeImpair:      {"connect_timeout": true}, // protocol/impair applies it itself
	C.TypeOpenConnect: {"disable_tcp_keep_alive": true, "tcp_keep_alive": true, "tcp_keep_alive_interval": true},
}

const ignoredUnderDetourReason = "not applied under detour (common/dialer NewWithOptions builds the detour dialer only)"

// recordIgnoredUnderDetour writes the Д1 changes of every node with a
// detour. The options are not touched: what upstream gets is the config as
// it is, the behaviour is upstream's.
func recordIgnoredUnderDetour(options *option.Options, changes map[string][]option.LXConfigChange) error {
	for _, outbound := range options.Outbounds {
		_, nodeChanges, err := withoutIgnoredDialFields(outbound.Type, outbound.Options)
		if err != nil {
			return E.Cause(err, "outbound ", outbound.Tag)
		}
		changes[outbound.Tag] = append(changes[outbound.Tag], nodeChanges...)
	}
	for _, endpoint := range options.Endpoints {
		_, nodeChanges, err := withoutIgnoredDialFields(endpoint.Type, endpoint.Options)
		if err != nil {
			return E.Cause(err, "endpoint ", endpoint.Tag)
		}
		changes[endpoint.Tag] = append(changes[endpoint.Tag], nodeChanges...)
	}
	return nil
}

// withoutIgnoredUnderDetour is the effective view of options (Н2): a copy in
// which the nodes with a detour lack the fields of Д1. options is not
// written through.
func withoutIgnoredUnderDetour(options option.Options) (option.Options, error) {
	outbounds := append([]option.Outbound(nil), options.Outbounds...)
	for i := range outbounds {
		changed, _, err := withoutIgnoredDialFields(outbounds[i].Type, outbounds[i].Options)
		if err != nil {
			return options, E.Cause(err, "outbound ", outbounds[i].Tag)
		}
		if changed != nil {
			outbounds[i].Options = changed
		}
	}
	endpoints := append([]option.Endpoint(nil), options.Endpoints...)
	for i := range endpoints {
		changed, _, err := withoutIgnoredDialFields(endpoints[i].Type, endpoints[i].Options)
		if err != nil {
			return options, E.Cause(err, "endpoint ", endpoints[i].Tag)
		}
		if changed != nil {
			endpoints[i].Options = changed
		}
	}
	options.Outbounds = outbounds
	options.Endpoints = endpoints
	return options, nil
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
