//go:build with_xhttp

package v2rayxhttp

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2ray"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// init registers the XHTTP client transport constructor with the v2ray client
// transport registry. It runs only when built with -tags with_xhttp, so a
// build without the tag leaves "xhttp" unregistered and the runtime rejects it
// with the standard "unknown transport type: xhttp" error.
func init() {
	v2ray.RegisterClient(C.V2RayTransportTypeXHTTP, func(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayTransportOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
		return NewClient(ctx, dialer, serverAddr, options.XHTTPOptions, tlsConfig)
	})
}
