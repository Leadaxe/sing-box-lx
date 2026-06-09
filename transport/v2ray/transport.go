package v2ray

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2rayhttp"
	"github.com/sagernet/sing-box/transport/v2rayhttpupgrade"
	"github.com/sagernet/sing-box/transport/v2raywebsocket"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type (
	ServerConstructor[O any] func(ctx context.Context, logger logger.ContextLogger, options O, tlsConfig tls.ServerConfig, handler adapter.V2RayServerTransportHandler) (adapter.V2RayServerTransport, error)
	ClientConstructor[O any] func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options O, tlsConfig tls.Config) (adapter.V2RayClientTransport, error)
)

func NewServerTransport(ctx context.Context, logger logger.ContextLogger, options option.V2RayTransportOptions, tlsConfig tls.ServerConfig, handler adapter.V2RayServerTransportHandler) (adapter.V2RayServerTransport, error) {
	if options.Type == "" {
		return nil, nil
	}
	switch options.Type {
	case C.V2RayTransportTypeHTTP:
		return v2rayhttp.NewServer(ctx, logger, options.HTTPOptions, tlsConfig, handler)
	case C.V2RayTransportTypeWebsocket:
		return v2raywebsocket.NewServer(ctx, logger, options.WebsocketOptions, tlsConfig, handler)
	case C.V2RayTransportTypeQUIC:
		if tlsConfig == nil {
			return nil, C.ErrTLSRequired
		}
		return NewQUICServer(ctx, logger, options.QUICOptions, tlsConfig, handler)
	case C.V2RayTransportTypeGRPC:
		return NewGRPCServer(ctx, logger, options.GRPCOptions, tlsConfig, handler)
	case C.V2RayTransportTypeHTTPUpgrade:
		return v2rayhttpupgrade.NewServer(ctx, logger, options.HTTPUpgradeOptions, tlsConfig, handler)
	default:
		return nil, E.New("unknown transport type: " + options.Type)
	}
}

func NewClientTransport(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
	if options.Type == "" {
		return nil, nil
	}
	// lx:begin xhttp — switch replaced by the client transport registry (registry.go);
	// built-in types are registered there, downstream types (xhttp) register via init().
	constructor, loaded := lookupClientTransport(options.Type)
	if !loaded {
		return nil, E.New("unknown transport type: " + options.Type)
	}
	return constructor(ctx, dialer, serverAddr, options, tlsConfig)
	// lx:end xhttp
}
