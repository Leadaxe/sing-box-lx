// Package masque implements a CONNECT-IP (RFC 9484) client for MASQUE
// outbounds, primarily targeting Cloudflare WARP. It carries whole IP packets
// over an HTTP/3 (or HTTP/2) tunnel using HTTP Datagrams and the Capsule
// Protocol.
//
// This is NOT CONNECT-UDP (RFC 9298). See SPEC 021.
//
// The CONNECT-IP wire logic is vendored under ./connectip (ported from
// connect-ip-go). This file holds the dialing that layers the profile quirks
// (Cloudflare vs standard) on top.
package masque

import (
	"context"
	"net/url"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/transport/masque/connectip"
	E "github.com/sagernet/sing/common/exceptions"
)

// IpConn is the transport-agnostic surface the outbound pumps IP packets
// through. Both the HTTP/3 connectip.Conn and the HTTP/2 shim satisfy it.
type IpConn interface {
	ReadPacket() (b []byte, err error)
	WritePacket(b []byte) (icmp []byte, err error)
	Close() error
}

// ConnectTunnelH3 establishes a CONNECT-IP tunnel over an existing HTTP/3
// (QUIC) connection and advertises a default route so all traffic is tunnelled.
// It returns the http3 transport (for lifetime management) and the IP conn.
func ConnectTunnelH3(ctx context.Context, profile Profile, quicConn *quic.Conn, connectURI string) (*http3.Transport, IpConn, error) {
	tr := &http3.Transport{
		EnableDatagrams: true,
		AdditionalSettings: map[uint64]uint64{
			// The official client still advertises the deprecated
			// SETTINGS_H3_DATAGRAM_00 (0x276); WARP expects it.
			0x276: 1,
		},
		DisableCompression: true,
	}

	hconn := tr.NewClientConn(quicConn)

	ipConn, err := dialCONNECTIP(ctx, profile, hconn, connectURI)
	if err != nil {
		_ = tr.Close()
		if err.Error() == "CRYPTO_ERROR 0x131 (remote): tls: access denied" {
			return nil, nil, E.New("masque: login failed — verify the TLS key/cert is enrolled with Cloudflare Access")
		}
		return nil, nil, E.Cause(err, "masque: dial connect-ip")
	}

	if err = advertiseDefaultRoute(ctx, ipConn); err != nil {
		_ = ipConn.Close()
		_ = tr.Close()
		return nil, nil, err
	}

	return tr, ipConn, nil
}

// dialCONNECTIP sends the Extended CONNECT request and wraps the request stream
// into a proxied IP connection.
func dialCONNECTIP(ctx context.Context, profile Profile, conn *http3.ClientConn, connectURI string) (*connectip.Conn, error) {
	u, err := url.Parse(connectURI)
	if err != nil {
		return nil, E.Cause(err, "parse connect uri")
	}

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-conn.Context().Done():
		return nil, context.Cause(conn.Context())
	case <-conn.ReceivedSettings():
	}
	settings := conn.Settings()
	if !profile.IgnoreExtendedConnect && !settings.EnableExtendedConnect {
		return nil, E.New("connect-ip: server didn't enable Extended CONNECT")
	}
	if !settings.EnableDatagrams {
		return nil, E.New("connect-ip: server didn't enable datagrams")
	}

	rstr, err := conn.OpenRequestStream(ctx)
	if err != nil {
		return nil, E.Cause(err, "open request stream")
	}

	req := buildConnectIPRequest(profile, u)
	if err = rstr.SendRequestHeader(req); err != nil {
		return nil, E.Cause(err, "send request header")
	}
	rsp, err := rstr.ReadResponse()
	if err != nil {
		return nil, E.Cause(err, "read response")
	}
	if rsp.StatusCode < 200 || rsp.StatusCode > 299 {
		return nil, E.New("connect-ip: server responded with ", rsp.StatusCode)
	}
	return connectip.NewProxiedConn(rstr), nil
}
