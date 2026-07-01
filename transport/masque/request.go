package masque

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/transport/masque/connectip"
)

// capsuleProtocolHeaderValue is the structured-field boolean true ("?1") sent in
// the Capsule-Protocol request header (RFC 9297).
const capsuleProtocolHeaderValue = "?1"

// buildConnectIPRequest constructs the Extended CONNECT request for a profile.
func buildConnectIPRequest(profile Profile, u *url.URL) *http.Request {
	header := http.Header{
		http3.CapsuleProtocolHeader: []string{capsuleProtocolHeaderValue},
		// The official WARP client sends an empty User-Agent.
		"User-Agent": []string{""},
	}
	return &http.Request{
		Method: http.MethodConnect,
		Proto:  profile.RequestProtocol,
		Host:   u.Host,
		Header: header,
		URL:    u,
	}
}

// advertiseDefaultRoute tells the peer we accept all IPv4/IPv6 traffic, so the
// endpoint routes everything through the tunnel.
func advertiseDefaultRoute(ctx context.Context, ipConn IpConn) error {
	adv, ok := ipConn.(interface {
		AdvertiseRoute(context.Context, []connectip.IPRoute) error
	})
	if !ok {
		// h2 shim doesn't do capsule route advertisement.
		return nil
	}
	return adv.AdvertiseRoute(ctx, []connectip.IPRoute{
		{
			IPProtocol: 0,
			StartIP:    netip.AddrFrom4([4]byte{}),
			EndIP:      netip.AddrFrom4([4]byte{255, 255, 255, 255}),
		},
		{
			IPProtocol: 0,
			StartIP:    netip.AddrFrom16([16]byte{}),
			EndIP: netip.AddrFrom16([16]byte{
				255, 255, 255, 255, 255, 255, 255, 255,
				255, 255, 255, 255, 255, 255, 255, 255,
			}),
		},
	})
}
