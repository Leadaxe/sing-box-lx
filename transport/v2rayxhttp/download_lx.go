package v2rayxhttp

import (
	"context"

	D "github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
)

// configureDownload builds a distinct pool even when the two endpoints happen
// to share an address: credentials, HTTP version and lifetime remain independent.
func (c *Client) configureDownload(ctx context.Context, inherited N.Dialer, settings *option.V2RayXHTTPDownloadOptions) error {
	if settings == nil {
		return nil
	}
	if c.mode == modeStreamOne {
		return E.New("v2ray-xhttp: download_settings is incompatible with stream-one mode")
	}
	if settings.Server == "" || settings.ServerPort == 0 {
		return E.New("v2ray-xhttp: download_settings requires server and server_port")
	}
	if settings.XHTTP.DownloadSettings != nil {
		return E.New("v2ray-xhttp: nested download_settings is not supported")
	}
	address := settings.ServerOptions.Build()
	dialer := inherited
	if settings.Dialer != nil {
		var err error
		dialer, err = D.New(ctx, *settings.Dialer, address.IsFqdn())
		if err != nil {
			return E.Cause(err, "create XHTTP download dialer")
		}
	}
	// A dialer built for an IP uplink has no resolver wrapper. Preserve its
	// physical-network/protect options, but resolve a domain download endpoint
	// through the core default bootstrap path instead of passing it unresolved.
	if settings.Dialer == nil && address.IsFqdn() {
		if _, plainDirect := inherited.(*D.DefaultDialer); plainDirect {
			queryOptions, err := D.NewDNSQueryOptions(ctx, nil, false)
			if err != nil {
				return E.Cause(err, "create XHTTP download resolver")
			}
			dialer = D.NewResolveDialer(ctx, inherited, true, "", queryOptions, 0)
		}
	}
	var tlsConfig tls.Config
	if settings.TLS != nil {
		var err error
		tlsConfig, err = tls.NewClient(ctx, log.NewNOPFactory().Logger(), settings.Server, *settings.TLS)
		if err != nil {
			return E.Cause(err, "create XHTTP download TLS")
		}
	}
	transport, err := NewClient(ctx, dialer, address, settings.XHTTP, tlsConfig)
	if err != nil {
		return E.Cause(err, "create XHTTP download transport")
	}
	c.download = transport.(*Client)
	return nil
}

func (c *Client) downloadClient() *Client {
	if c.download != nil {
		return c.download
	}
	return c
}

// readSide keeps body-read health accounting on the download pool, while upload
// requests and their failures continue to be charged to the upload pool.
func (r *xmuxRelease) readSide() *xmuxRelease {
	if r != nil && r.peer != nil {
		return r.peer
	}
	return r
}
