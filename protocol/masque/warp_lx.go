package masque

// lx: SPEC 110 — the `profile` and `address` keys of the masque-client
// endpoint: everything Cloudflare WARP needs on top of RFC 9484, applied to the
// shared HTTP client and the tunnel client before they are built.

import (
	"net/http"
	"time"

	"github.com/sagernet/sing-box/option"
	transportHTTP "github.com/sagernet/sing-box/transport/http"
	"github.com/sagernet/sing-box/transport/masque"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// warpProtocol is the :protocol token WARP expects instead of connect-ip.
	warpProtocol = "cf-connect-ip"
	// warpHTTP3ConnectBudget bounds the wait for the CONNECT response on HTTP/3
	// before falling back to HTTP/2: WARP ignores QUIC from some source
	// addresses after a completed handshake, and the official client's HTTP/2
	// path works from the same address. Measured 2026-08 (SPEC 074): a working
	// path answers well under a second; 3 s leaves room for a slow hop.
	warpHTTP3ConnectBudget = 3 * time.Second
	// warpLegacyDatagramSetting is SETTINGS_H3_DATAGRAM_00, which the official
	// WARP client still advertises.
	warpLegacyDatagramSetting = 0x276
)

// applyLXProfile validates the fork's keys and configures the clients for the
// chosen dialect. Returns the HTTP client options and tunnel client options to
// use.
func applyLXProfile(options option.MASQUEClientEndpointOptions, httpOptions transportHTTP.ClientOptions, clientOptions masque.ClientOptions) (transportHTTP.ClientOptions, masque.ClientOptions, error) {
	clientOptions.LX.Address = options.Address
	switch options.Profile {
	case "", option.MASQUEProfileStandard:
	case option.MASQUEProfileCloudflare:
		clientOptions.LX.Protocol = warpProtocol
		httpOptions.TunnelLX = transportHTTP.TunnelLXOptions{
			PlainConnectHTTP2: true,
			PlainConnectHeaders: http.Header{
				"Cf-Connect-Proto": []string{warpProtocol},
				"Pq-Enabled":       []string{"false"},
			},
			HTTP3ConnectBudget:      warpHTTP3ConnectBudget,
			HTTP3AdditionalSettings: map[uint64]uint64{warpLegacyDatagramSetting: 1},
		}
	default:
		return httpOptions, clientOptions, E.New("unknown masque profile: ", options.Profile)
	}
	return httpOptions, clientOptions, nil
}
