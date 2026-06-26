package dns

import (
	"github.com/sagernet/sing-box/option"
)

type TransportAdapter struct {
	transportType string
	transportTag  string
	dependencies  []string
	// lx: SPEC 018 — the outbound (detour) tag this DNS server is statically bound to, from
	// its DialerOptions.Detour. A DNS rule picks the server; the channel the server uses is
	// fixed here at config time. Empty = default outbound. The server-side stream resolves a
	// selector tag to the live node via Now(); this stores only the raw tag (zero hot-path cost).
	outboundTag string
}

func NewTransportAdapter(transportType string, transportTag string, dependencies []string) TransportAdapter {
	return TransportAdapter{
		transportType: transportType,
		transportTag:  transportTag,
		dependencies:  dependencies,
	}
}

func NewTransportAdapterWithLocalOptions(transportType string, transportTag string, localOptions option.LocalDNSServerOptions) TransportAdapter {
	var dependencies []string
	if localOptions.DomainResolver != nil && localOptions.DomainResolver.Server != "" {
		dependencies = append(dependencies, localOptions.DomainResolver.Server)
	}
	return TransportAdapter{
		transportType: transportType,
		transportTag:  transportTag,
		dependencies:  dependencies,
		outboundTag:   localOptions.Detour, // lx: SPEC 018
	}
}

func NewTransportAdapterWithRemoteOptions(transportType string, transportTag string, remoteOptions option.RemoteDNSServerOptions) TransportAdapter {
	var dependencies []string
	if remoteOptions.DomainResolver != nil && remoteOptions.DomainResolver.Server != "" {
		dependencies = append(dependencies, remoteOptions.DomainResolver.Server)
	}
	return TransportAdapter{
		transportType: transportType,
		transportTag:  transportTag,
		dependencies:  dependencies,
		outboundTag:   remoteOptions.Detour, // lx: SPEC 018
	}
}

func (a *TransportAdapter) Type() string {
	return a.transportType
}

func (a *TransportAdapter) Tag() string {
	return a.transportTag
}

func (a *TransportAdapter) Dependencies() []string {
	return a.dependencies
}

// OutboundTag is the detour tag this DNS server is bound to (lx: SPEC 018); "" = default
// outbound. May be a selector tag — the caller resolves Now() if it needs the live node.
func (a *TransportAdapter) OutboundTag() string {
	return a.outboundTag
}
