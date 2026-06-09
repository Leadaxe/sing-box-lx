package v2ray

// lx: client transport registry.
//
// Upstream selects the client transport with a hardcoded switch in
// NewClientTransport (transport.go). sing-box-lx replaces that switch with this
// registry so downstream transports (e.g. XHTTP) register themselves via init()
// behind a build tag, keeping the recurring diff to upstream files near zero.
// The built-in transports below reproduce upstream's switch behavior exactly,
// including the QUIC TLS-required check. See SPECS/002.

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2rayhttp"
	"github.com/sagernet/sing-box/transport/v2rayhttpupgrade"
	"github.com/sagernet/sing-box/transport/v2raywebsocket"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// ClientTransportConstructor builds a client transport from the full
// V2RayTransportOptions (each transport reads its own sub-options field).
type ClientTransportConstructor func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error)

var clientTransportRegistry = make(map[string]ClientTransportConstructor)

// RegisterClient registers a client transport constructor for the given type.
// Built-in types are registered below; downstream types (xhttp) register from
// their own package under a build tag.
func RegisterClient(transportType string, constructor ClientTransportConstructor) {
	clientTransportRegistry[transportType] = constructor
}

// lookupClientTransport returns the constructor for a transport type, if any.
func lookupClientTransport(transportType string) (ClientTransportConstructor, bool) {
	constructor, loaded := clientTransportRegistry[transportType]
	return constructor, loaded
}

func init() {
	RegisterClient(C.V2RayTransportTypeHTTP, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		return v2rayhttp.NewClient(ctx, dialer, serverAddr, options.HTTPOptions, tlsConfig)
	})
	RegisterClient(C.V2RayTransportTypeGRPC, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		return NewGRPCClient(ctx, dialer, serverAddr, options.GRPCOptions, tlsConfig)
	})
	RegisterClient(C.V2RayTransportTypeWebsocket, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		return v2raywebsocket.NewClient(ctx, dialer, serverAddr, options.WebsocketOptions, tlsConfig)
	})
	RegisterClient(C.V2RayTransportTypeQUIC, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		if tlsConfig == nil {
			return nil, C.ErrTLSRequired
		}
		return NewQUICClient(ctx, dialer, serverAddr, options.QUICOptions, tlsConfig)
	})
	RegisterClient(C.V2RayTransportTypeHTTPUpgrade, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		return v2rayhttpupgrade.NewClient(ctx, dialer, serverAddr, options.HTTPUpgradeOptions, tlsConfig)
	})
}
