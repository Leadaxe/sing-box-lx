package lxmtu

import (
	"sort"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// Resolver walks the detour graph of a config (outbounds + endpoints, one tag
// namespace) and aligns every node that sits on top of an IP tunnel (SPEC 120
// §2.1–2.2, §3 "detour"). It mutates the typed option structs in place, before
// the nodes are created, so the upstream constructors read aligned values.
type Resolver struct {
	policy Policy
	order  []string
	nodes  map[string]*node

	capacities map[string]capacity
	visiting   map[string]bool
	aligning   map[string]alignState
	effective  map[string]uint32
	decisions  map[string][]Decision
}

type node struct {
	tag      string
	typeName string
	options  any
}

type capacity struct {
	value   int
	reason  string
	warning string
}

type alignState uint8

const (
	alignPending alignState = iota
	alignInProgress
	alignDone
)

// NewResolver indexes the nodes of a config. Unknown detour / group member
// tags are treated as unlimited: the start-up dependency check reports them.
func NewResolver(outbounds []option.Outbound, endpoints []option.Endpoint, policy Policy) *Resolver {
	r := &Resolver{
		policy:     policy,
		nodes:      make(map[string]*node, len(outbounds)+len(endpoints)),
		capacities: make(map[string]capacity),
		visiting:   make(map[string]bool),
		aligning:   make(map[string]alignState),
		effective:  make(map[string]uint32),
		decisions:  make(map[string][]Decision),
	}
	for i := range outbounds {
		r.index(outbounds[i].Tag, outbounds[i].Type, outbounds[i].Options)
	}
	for i := range endpoints {
		r.index(endpoints[i].Tag, endpoints[i].Type, endpoints[i].Options)
	}
	return r
}

func (r *Resolver) index(tag, typeName string, options any) {
	if _, duplicate := r.nodes[tag]; duplicate {
		return
	}
	r.nodes[tag] = &node{tag: tag, typeName: typeName, options: options}
	r.order = append(r.order, tag)
}

// Validate checks the policy against the config: every excepted tag must
// name a node.
func (r *Resolver) Validate() error {
	var unknown []string
	for tag := range r.policy.Except {
		if _, found := r.nodes[tag]; !found {
			unknown = append(unknown, tag)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return E.New("lx.mtu_align.except: unknown outbound/endpoint tag: ", strings.Join(unknown, ", "))
}

// Align aligns every node and returns the decisions in config order. In
// ModeOff nothing is touched and nothing is returned.
func (r *Resolver) Align() ([]Decision, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.policy.Mode == ModeOff {
		return nil, nil
	}
	for _, tag := range r.order {
		r.alignNode(tag)
	}
	var decisions []Decision
	for _, tag := range r.order {
		decisions = append(decisions, r.decisions[tag]...)
	}
	return decisions, nil
}

// Capacity is how many bytes of IP packet the node carries for a node above
// it (SPEC 120 §2.1): a tunnel's effective mtu, a group's minimum, a node
// with detour its detour's capacity, anything else Unlimited. reason names
// the limiting tunnel ("warp[masque] mtu 1280", "… via sel"); warning is set
// when the capacity is Unlimited for a reason worth logging (tuic in native
// relay mode, a chain as target).
func (r *Resolver) Capacity(tag string) (int, string, string) {
	if memo, done := r.capacities[tag]; done {
		return memo.value, memo.reason, memo.warning
	}
	if r.visiting[tag] {
		// A detour cycle; the dependency check at start rejects it.
		return Unlimited, "", ""
	}
	r.visiting[tag] = true
	result := r.capacityOf(tag)
	delete(r.visiting, tag)
	r.capacities[tag] = result
	return result.value, result.reason, result.warning
}

func (r *Resolver) capacityOf(tag string) capacity {
	n := r.nodes[tag]
	if n == nil {
		return capacity{value: Unlimited}
	}
	switch options := n.options.(type) {
	case *option.WireGuardEndpointOptions, *option.MASQUEOutboundOptions,
		*option.OpenVPNClientEndpointOptions, *option.OpenConnectEndpointOptions:
		return r.tunnelCapacity(n)
	case *option.TailscaleEndpointOptions:
		mtu := options.SystemInterfaceMTU
		if mtu == 0 {
			mtu = DefaultTailscaleMTU
		}
		return capacity{value: int(mtu), reason: describeTunnel(n, mtu)}
	case *option.SelectorOutboundOptions:
		return r.groupCapacity(n, options.Outbounds)
	case *option.URLTestOutboundOptions:
		return r.groupCapacity(n, options.Outbounds)
	case *option.ChainOutboundOptions:
		return capacity{value: Unlimited, warning: "chain " + tag + " as a detour target aligns its own links; nodes above it are not aligned"}
	case *option.TUICOutboundOptions:
		if options.UDPRelayMode != "quic" {
			return capacity{value: Unlimited, warning: "tuic " + tag + " in native udp_relay_mode below a tunnel may drop oversize datagrams; use quic"}
		}
	}
	return r.detourCapacity(n)
}

// tunnelCapacity is the tunnel's effective mtu, i.e. after its own alignment
// (recursively down the detour graph). 0 (openvpn/openconnect without mtu)
// is Unlimited: we do not know the value the tunnel will negotiate.
func (r *Resolver) tunnelCapacity(n *node) capacity {
	mtu := r.alignNode(n.tag)
	if mtu == 0 {
		return capacity{value: Unlimited}
	}
	return capacity{value: int(mtu), reason: describeTunnel(n, mtu)}
}

func describeTunnel(n *node, mtu uint32) string {
	return n.tag + "[" + n.typeName + "] mtu " + strconv.Itoa(int(mtu))
}

// groupCapacity is the minimum over all members (SPEC 120 §2.5): the only
// value that works in every state of the group without recreating the node
// above. The warning of an unlimited member is reported only when the group
// as a whole is unlimited, as the chain does.
func (r *Resolver) groupCapacity(n *node, members []string) capacity {
	result := capacity{value: Unlimited}
	for _, member := range members {
		value, reason, warning := r.Capacity(member)
		if value < result.value {
			result.value = value
			result.reason = reason + " via " + n.tag
		}
		if value == Unlimited && warning != "" && result.warning == "" {
			result.warning = warning
		}
	}
	if result.value != Unlimited {
		result.warning = ""
	}
	return result
}

// detourCapacity: a node without an IP layer of its own passes its detour's
// capacity through unchanged.
func (r *Resolver) detourCapacity(n *node) capacity {
	detour := detourOf(n.options)
	if detour == "" {
		return capacity{value: Unlimited}
	}
	value, reason, warning := r.Capacity(detour)
	if value == Unlimited {
		return capacity{value: Unlimited, warning: warning}
	}
	return capacity{value: value, reason: reason + " via " + n.tag}
}

func detourOf(options any) string {
	wrapper, ok := options.(option.DialerOptionsWrapper)
	if !ok {
		return ""
	}
	return wrapper.TakeDialerOptions().Detour
}

// alignNode aligns one node (memoised) and returns its effective mtu when it
// is a tunnel, 0 otherwise. Re-entry through a detour cycle returns the
// configured value and records nothing.
func (r *Resolver) alignNode(tag string) uint32 {
	switch r.aligning[tag] {
	case alignDone:
		return r.effective[tag]
	case alignInProgress:
		return r.configuredMTU(tag)
	}
	r.aligning[tag] = alignInProgress
	r.effective[tag] = r.alignOf(tag)
	r.aligning[tag] = alignDone
	return r.effective[tag]
}

func (r *Resolver) configuredMTU(tag string) uint32 {
	n := r.nodes[tag]
	if n == nil {
		return 0
	}
	mtu, _, _ := tunnelFields(n)
	return mtu
}

// tunnelFields reads the mtu (default applied), the server used for the
// address family and whether the mtu was explicit.
func tunnelFields(n *node) (mtu uint32, server string, explicit bool) {
	switch options := n.options.(type) {
	case *option.WireGuardEndpointOptions:
		mtu = options.MTU
		if len(options.Peers) > 0 {
			server = options.Peers[0].Address
		}
	case *option.MASQUEOutboundOptions:
		mtu = options.MTU
		server = options.Server
	case *option.OpenVPNClientEndpointOptions:
		mtu = options.MTU
		server = options.Server
	case *option.OpenConnectEndpointOptions:
		mtu = options.MTU
		server = options.Server
	default:
		return 0, "", false
	}
	explicit = mtu != 0
	if !explicit {
		mtu = TunnelDefaultMTU(n.typeName)
	}
	return mtu, server, explicit
}

func setTunnelMTU(n *node, mtu uint32) {
	switch options := n.options.(type) {
	case *option.WireGuardEndpointOptions:
		options.MTU = mtu
	case *option.MASQUEOutboundOptions:
		options.MTU = mtu
	}
}

// quicFields returns the QUIC options of a hysteria2/tuic/hysteria node.
func quicFields(n *node) (quic *option.QUICOptions, server string, ok bool) {
	switch options := n.options.(type) {
	case *option.Hysteria2OutboundOptions:
		return &options.QUICOptions, options.Server, true
	case *option.TUICOutboundOptions:
		return &options.QUICOptions, options.Server, true
	case *option.HysteriaOutboundOptions:
		return &options.QUICOptions, options.Server, true
	}
	return nil, "", false
}

func (r *Resolver) record(tag string, decision Decision) {
	r.decisions[tag] = append(r.decisions[tag], decision)
}

// alignOf computes and applies the decisions of one node; returns the
// effective mtu for tunnels.
func (r *Resolver) alignOf(tag string) uint32 {
	n := r.nodes[tag]
	if n == nil {
		return 0
	}
	if quic, server, isQUIC := quicFields(n); isQUIC {
		r.alignQUIC(n, quic, server)
		return 0
	}
	mtu, server, explicit := tunnelFields(n)
	if !IsTunnelType(n.typeName) {
		return 0
	}
	detour := detourOf(n.options)
	if detour == "" {
		return mtu
	}
	value, reason, warning := r.Capacity(detour)
	if value == Unlimited {
		if warning != "" {
			r.record(tag, Decision{Tag: tag, Field: "mtu", Configured: mtu, Effective: mtu, Explicit: explicit, Warning: warning})
		}
		return mtu
	}
	overhead, known := TunnelOverhead(n.typeName, server)
	if !known {
		r.record(tag, Decision{
			Tag: tag, Field: "mtu", Configured: mtu, Effective: mtu, Explicit: explicit,
			Reason:  "kept (overhead of " + n.typeName + " unknown)",
			Warning: tag + "[" + n.typeName + "] mtu not aligned: overhead of " + n.typeName + " is unknown (detour " + reason + ")",
		})
		return mtu
	}
	limit := value - overhead
	if limit <= 0 {
		r.record(tag, Decision{
			Tag: tag, Field: "mtu", Configured: mtu, Effective: mtu, Explicit: explicit,
			Warning: tag + " mtu not aligned: capacity below too small (detour " + reason + " − " + strconv.Itoa(overhead) + " " + Family(server) + ")",
		})
		return mtu
	}
	decision := r.policy.Apply(tag, "mtu", mtu, explicit, uint32(limit),
		"detour "+reason+" − "+strconv.Itoa(overhead)+" "+Family(server))
	if decision.Changed {
		setTunnelMTU(n, decision.Effective)
	}
	r.record(tag, decision)
	return decision.Effective
}

// alignQUIC derives initial_packet_size = capacity − ip/udp, clamped to the
// QUIC range, and turns PMTUD off with it (SPEC 120 §2.2, §2.4). An explicit
// value is left alone in fill, lowered in clamp; PMTUD is only touched
// together with the value.
func (r *Resolver) alignQUIC(n *node, quic *option.QUICOptions, server string) {
	detour := detourOf(n.options)
	if detour == "" {
		return
	}
	value, reason, warning := r.Capacity(detour)
	if value == Unlimited {
		if warning != "" {
			r.record(n.tag, Decision{Tag: n.tag, Field: "initial_packet_size", Configured: uint32(quic.InitialPacketSize), Effective: uint32(quic.InitialPacketSize), Explicit: quic.InitialPacketSize != 0, Warning: warning})
		}
		return
	}
	overhead := IPUDP(server)
	limit := value - overhead
	size, raised := QUICClamp(limit)
	configured := uint32(quic.InitialPacketSize)
	decision := r.policy.Apply(n.tag, "initial_packet_size", configured, configured != 0, uint32(size),
		"detour "+reason+" − "+strconv.Itoa(overhead)+" "+Family(server))
	if decision.Changed {
		quic.InitialPacketSize = int(decision.Effective)
		quic.DisablePathMTUDiscovery = true
		decision.Reason += "; pmtud off"
	}
	if raised && (decision.Changed || decision.Warning != "") {
		decision.Warning = joinWarning(decision.Warning, n.tag+" initial_packet_size "+strconv.Itoa(size)+" will not fit: path limit "+strconv.Itoa(limit)+" is below the QUIC minimum "+strconv.Itoa(QUICMinPacketSize))
	}
	r.record(n.tag, decision)
}

func joinWarning(existing, added string) string {
	if existing == "" {
		return added
	}
	return existing + "; " + added
}
