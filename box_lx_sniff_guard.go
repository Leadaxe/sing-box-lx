package box

// lx: SPEC 060 — wiring of lx.sniff_guard: the load-time checks that need the
// build registries or the rest of the config, the node and carrier facts the
// guard needs from the typed options, and the late load of persisted labels.

import (
	"context"
	"encoding/pem"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/lxsniffguard"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	"golang.org/x/crypto/cryptobyte"
)

// newSniffGuard checks lx.sniff_guard against the build and the config and
// returns the guard of this box, or nil when the block is off. raw is the
// block as written (before canonicalization drops disabled sections).
func newSniffGuard(ctx context.Context, options *option.Options, raw *option.LXSniffGuardOptions, policy option.LXSniffGuardResolved, logger log.Logger) (*lxsniffguard.Guard, error) {
	if !policy.Enabled() {
		return nil, nil
	}
	if err := checkSniffGuardTypes(ctx, raw); err != nil {
		return nil, err
	}
	cacheFile := options.Experimental != nil && options.Experimental.CacheFile != nil && options.Experimental.CacheFile.Enabled
	if policy.TLS.Enabled() && policy.TLS.Persist && !cacheFile {
		return nil, E.New("lx.sniff_guard.tls.persist: requires experimental.cache_file to be enabled")
	}
	if policy.QUIC.Enabled() && policy.QUIC.Persist && !cacheFile {
		return nil, E.New("lx.sniff_guard.quic.persist: requires experimental.cache_file to be enabled")
	}
	nodes := make(map[string]lxsniffguard.NodeInfo)
	carriers := make(map[string]lxsniffguard.CarrierInfo)
	add := func(outboundType, tag string, rawOptions any) {
		carriers[tag] = sniffGuardCarrierInfo(outboundType, tag, rawOptions)
		info, covered := sniffGuardNodeInfo(outboundType, rawOptions)
		if !covered || !policy.CoversType(outboundType) || policy.SNIMismatchOnly && !info.SNIMismatch {
			return
		}
		nodes[tag] = info
	}
	for _, outbound := range options.Outbounds {
		add(outbound.Type, outbound.Tag, outbound.Options)
	}
	for _, endpoint := range options.Endpoints {
		add(endpoint.Type, endpoint.Tag, endpoint.Options)
	}
	// The cache file opens at a start stage, after box.New; persisted labels
	// are loaded then (loadSniffGuardCache).
	return lxsniffguard.New(lxsniffguard.Config{
		Policy:   policy,
		Nodes:    nodes,
		Carriers: carriers,
		Logger:   logger,
	}), nil
}

// loadSniffGuardCache hands the opened cache file to the guard. Called once
// the cache file has passed its initialize stage, before any outbound starts.
func loadSniffGuardCache(ctx context.Context) {
	guard := lxsniffguard.FromContext(ctx)
	if guard == nil {
		return
	}
	if cache, isCache := service.FromContext[adapter.CacheFile](ctx).(lxsniffguard.Cache); isCache {
		guard.LoadCache(cache)
	}
}

// checkSniffGuardTypes fails the start on a type that no outbound or endpoint
// registry of this build knows: a typo would otherwise match nothing.
func checkSniffGuardTypes(ctx context.Context, raw *option.LXSniffGuardOptions) error {
	if raw == nil {
		return nil
	}
	known := func(outboundType string) bool {
		if registry := service.FromContext[adapter.OutboundRegistry](ctx); registry != nil {
			if _, loaded := registry.CreateOptions(outboundType); loaded {
				return true
			}
		}
		if registry := service.FromContext[adapter.EndpointRegistry](ctx); registry != nil {
			if _, loaded := registry.CreateOptions(outboundType); loaded {
				return true
			}
		}
		return false
	}
	checkMap := func(key string, detourTypes option.LXSniffGuardDetourTypes) error {
		for carrierType := range detourTypes {
			if carrierType == option.LXSniffGuardDirect || carrierType == option.LXSniffGuardWildcard {
				continue
			}
			if !known(carrierType) {
				return E.New(key, ": unknown outbound type ", strconv.Quote(carrierType))
			}
		}
		return nil
	}
	if raw.Nodes != nil {
		for _, outboundType := range raw.Nodes.OutboundType {
			if !known(outboundType) {
				return E.New("lx.sniff_guard.nodes.outbound_type: unknown outbound type ", strconv.Quote(outboundType))
			}
		}
	}
	if err := checkMap("lx.sniff_guard.detour_type", raw.DetourType); err != nil {
		return err
	}
	if raw.TLS != nil {
		if err := checkMap("lx.sniff_guard.tls.detour_type", raw.TLS.DetourType); err != nil {
			return err
		}
	}
	if raw.QUIC != nil {
		if err := checkMap("lx.sniff_guard.quic.detour_type", raw.QUIC.DetourType); err != nil {
			return err
		}
	}
	return nil
}

// sniffGuardNodeInfo returns the facts of a node, and covered=false for a
// node the guard cannot protect (no TLS or QUIC handshake of its own, a
// system TLS engine, naive, any other type).
func sniffGuardNodeInfo(outboundType string, rawOptions any) (info lxsniffguard.NodeInfo, covered bool) {
	info.Type = outboundType
	switch options := rawOptions.(type) {
	case *option.VLESSOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, true)
	case *option.VMessOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, true)
	case *option.TrojanOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, true)
	case *option.HTTPOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, true)
	case *option.AnyTLSOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, false)
	case *option.ShadowTLSOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, false)
	case *option.TrustTunnelOutboundOptions:
		return sniffGuardTLSNode(info, options.Server, options.TLS, false)
	case *option.TUICOutboundOptions:
		info.SNIMismatch = sniffGuardTLSMismatch(options.Server, options.TLS)
		return info, true
	case *option.Hysteria2OutboundOptions:
		// Salamander hides the Initial: nothing for a sniffer to read.
		salamander := options.Obfs != nil && options.Obfs.Type == C.Hysteria2ObfsTypeSalamander
		info.SNIMismatch = !salamander && sniffGuardTLSMismatch(options.Server, options.TLS)
		return info, true
	case *option.MASQUEOutboundOptions:
		if options.Profile == "" || options.Profile == "cloudflare" {
			// WARP: the SNI is a cover name by design.
			info.SNIMismatch = true
			return info, true
		}
		tlsOptions := options.TLS
		if (tlsOptions == nil || tlsOptions.ServerName == "") && options.SNI != "" {
			// The deprecated flat `sni` alias.
			merged := option.OutboundTLSOptions{ServerName: options.SNI}
			if tlsOptions != nil {
				merged = *tlsOptions
				merged.ServerName = options.SNI
			}
			tlsOptions = &merged
		}
		info.SNIMismatch = sniffGuardTLSMismatch(options.Server, tlsOptions)
		return info, true
	case *option.WireGuardEndpointOptions:
		// Only a QUIC-shaped masquerade of AmneziaWG looks like a QUIC
		// Initial: ip=quic, or a static i1, which may be a QUIC capture (the
		// burst filter checks the shape at run time). Its SNI is a cover
		// name by design.
		if options.Ip != "quic" && options.I1 == "" {
			return info, false
		}
		info.SNIMismatch = true
		return info, true
	}
	return info, false
}

// sniffGuardTLSNode is a node whose handshake is TLS over TCP. optional means
// TLS is a choice of the node (vless, vmess, trojan, http) rather than built
// into the protocol.
func sniffGuardTLSNode(info lxsniffguard.NodeInfo, server string, tlsOptions *option.OutboundTLSOptions, optional bool) (lxsniffguard.NodeInfo, bool) {
	if optional && (tlsOptions == nil || !tlsOptions.Enabled) {
		return info, false
	}
	if tlsOptions != nil && (tlsOptions.Engine == "apple" || tlsOptions.Engine == "windows") {
		// The system engines build the ClientHello out of reach.
		return info, false
	}
	info.SNIMismatch = sniffGuardTLSMismatch(server, tlsOptions)
	return info, true
}

// sniffGuardTLSMismatch applies the SNI table of FEATURE 019 to a TLS block.
func sniffGuardTLSMismatch(server string, tlsOptions *option.OutboundTLSOptions) bool {
	if tlsOptions == nil {
		return false
	}
	if tlsOptions.Reality != nil && tlsOptions.Reality.Enabled {
		return true
	}
	if tlsOptions.DisableSNI {
		return false
	}
	if tlsOptions.ECH != nil && tlsOptions.ECH.Enabled {
		// The outer ClientHello carries the public name of the ECH config.
		// Only an inline config is read here; with config_path or a config
		// fetched over DNS the plain rule below stands in.
		if publicName := sniffGuardECHPublicName(tlsOptions.ECH.Config); publicName != "" {
			return lxsniffguard.SNIMismatch(server, publicName)
		}
	}
	return lxsniffguard.SNIMismatch(server, tlsOptions.ServerName)
}

// sniffGuardECHPublicName returns the public name of the first config of an
// inline ECH config list (PEM "ECH CONFIGS"), or "".
func sniffGuardECHPublicName(config []string) string {
	if len(config) == 0 {
		return ""
	}
	block, _ := pem.Decode([]byte(strings.Join(config, "\n")))
	if block == nil || block.Type != "ECH CONFIGS" {
		return ""
	}
	list := cryptobyte.String(block.Bytes)
	var configs cryptobyte.String
	if !list.ReadUint16LengthPrefixed(&configs) {
		return ""
	}
	for !configs.Empty() {
		var (
			version  uint16
			contents cryptobyte.String
		)
		if !configs.ReadUint16(&version) || !configs.ReadUint16LengthPrefixed(&contents) {
			return ""
		}
		var (
			configID     uint8
			kemID        uint16
			publicKey    cryptobyte.String
			cipherSuites cryptobyte.String
			maxNameLen   uint8
			publicName   cryptobyte.String
		)
		if contents.ReadUint8(&configID) && contents.ReadUint16(&kemID) &&
			contents.ReadUint16LengthPrefixed(&publicKey) && contents.ReadUint16LengthPrefixed(&cipherSuites) &&
			contents.ReadUint8(&maxNameLen) && contents.ReadUint8LengthPrefixed(&publicName) && len(publicName) > 0 {
			return string(publicName)
		}
	}
	return ""
}

// sniffGuardCarrierInfo returns the facts of an outbound or endpoint as the
// carrier of other dials.
func sniffGuardCarrierInfo(outboundType, tag string, rawOptions any) lxsniffguard.CarrierInfo {
	info := lxsniffguard.CarrierInfo{Type: outboundType, Fingerprint: outboundType + "|" + tag, Encrypted: true}
	if server, isServer := rawOptions.(option.ServerOptionsWrapper); isServer {
		serverOptions := server.TakeServerOptions()
		info.Fingerprint = outboundType + "|" + serverOptions.Server + "|" + strconv.Itoa(int(serverOptions.ServerPort))
	} else if wireguard, isWireGuard := rawOptions.(*option.WireGuardEndpointOptions); isWireGuard && len(wireguard.Peers) > 0 {
		peer := wireguard.Peers[0]
		info.Fingerprint = outboundType + "|" + peer.Address + "|" + strconv.Itoa(int(peer.Port))
	}
	switch outboundType {
	case C.TypeSOCKS:
		info.Encrypted = false
	case C.TypeHTTP:
		options, _ := rawOptions.(*option.HTTPOutboundOptions)
		info.Encrypted = options != nil && options.TLS != nil && options.TLS.Enabled
	}
	return info
}
