// Package v2rayxhttp implements the client side of the Xray "XHTTP"
// (a.k.a. "splithttp") v2ray transport for sing-box-lx. It is a lean-native
// implementation written on sing-box/sing primitives and the in-tree
// v2rayhttp HTTP/2 conn helpers, rather than vendoring Xray internals.
// See SPECS/002-XHTTP_CLIENT_TRANSPORT.
//
// Wire protocol (mirrors Xray-core transport/internet/splithttp):
//
//	A random per-dial session id is generated. Requests target
//	"<path>/<sessionId>" (and, for upload packets, "<path>/<sessionId>/<seq>").
//	Every request carries a random-length X-Padding header in the
//	configured x_padding_bytes range to blur the on-wire size signature.
//
//	stream-one : a single POST whose request body carries client->server
//	             bytes and whose response body carries server->client bytes
//	             (one fully bidirectional HTTP/2 stream). Closest to
//	             httpupgrade; this is the mode "auto" falls back to here.
//	stream-up  : a single streamed POST for the upload direction plus a
//	             separate GET whose response body is the download direction.
//	packet-up  : a GET download stream plus sequential POST upload packets,
//	             each "<path>/<sessionId>/<seq>" carrying one write.
package v2rayxhttp

import (
	"context"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sHTTP "github.com/sagernet/sing/protocol/http"

	"golang.org/x/net/http2"
)

const (
	modeAuto      = "auto"
	modePacketUp  = "packet-up"
	modeStreamUp  = "stream-up"
	modeStreamOne = "stream-one"
)

var _ adapter.V2RayClientTransport = (*Client)(nil)

type Client struct {
	ctx          context.Context
	dialer       N.Dialer
	serverAddr   M.Socksaddr
	transport    http.RoundTripper
	scheme       string
	host         string
	path         string
	mode         string
	headers      http.Header
	paddingMin   int
	paddingMax   int
	noGRPCHeader bool
	// realityEnabled records whether the TLS config is a Reality client config.
	// It drives mode=auto resolution (Reality → stream-one, like Xray).
	realityEnabled bool
}

// NewClient builds an XHTTP client transport. The tlsConfig (possibly Reality)
// is consumed exactly like the other v2ray transports: when present it drives
// an HTTP/2 dialer over the TLS dialer; when absent a plaintext HTTP/2 (h2c)
// transport is used.
func NewClient(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayXHTTPOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
	mode := options.Mode
	if mode == "" {
		mode = modeAuto
	}
	switch mode {
	case modeAuto, modePacketUp, modeStreamUp, modeStreamOne:
	default:
		return nil, E.New("v2ray-xhttp: unknown mode: ", mode)
	}

	paddingMin, paddingMax, err := parsePaddingRange(options.XPaddingBytes)
	if err != nil {
		return nil, err
	}

	var (
		transport http.RoundTripper
		scheme    string
	)
	if tlsConfig == nil {
		scheme = "http"
		// Plaintext h2c: speak HTTP/2 over a cleartext TCP conn so the same
		// streaming request/response body machinery works without TLS.
		transport = &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
				return dialer.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
			},
		}
	} else {
		scheme = "https"
		if len(tlsConfig.NextProtos()) == 0 {
			tlsConfig.SetNextProtos([]string{http2.NextProtoTLS})
		}
		tlsDialer := tls.NewDialer(dialer, tlsConfig)
		transport = &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.STDConfig) (net.Conn, error) {
				return tlsDialer.DialTLSContext(ctx, M.ParseSocksaddr(addr))
			},
		}
	}

	var host string
	if options.Host != "" {
		host = options.Host
	} else if tlsConfig != nil && tlsConfig.ServerName() != "" {
		host = tlsConfig.ServerName()
	} else {
		host = serverAddr.String()
	}

	path := options.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")

	headers := make(http.Header)
	for key, value := range options.Headers {
		headers[key] = value
	}

	return &Client{
		ctx:            ctx,
		dialer:         dialer,
		serverAddr:     serverAddr,
		transport:      transport,
		scheme:         scheme,
		host:           host,
		path:           path,
		mode:           mode,
		headers:        headers,
		paddingMin:     paddingMin,
		paddingMax:     paddingMax,
		noGRPCHeader:   options.NoGRPCHeader,
		realityEnabled: tlsConfigIsReality(tlsConfig),
	}, nil
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	sessionID := newSessionID()
	switch c.mode {
	case modeAuto:
		// Match Xray's auto resolution (transport/internet/splithttp/dialer.go):
		// Reality → stream-one; otherwise → packet-up (the most broadly compatible
		// mode, live-validated against Xray 3x-ui). Xray also picks stream-up when
		// downloadSettings is present, but we don't support asymmetric transport.
		if c.realityEnabled {
			return c.dialStreamOne(ctx, sessionID)
		}
		return c.dialPacketUp(ctx, sessionID)
	case modePacketUp:
		return c.dialPacketUp(ctx, sessionID)
	case modeStreamUp:
		return c.dialStreamUp(ctx, sessionID)
	case modeStreamOne:
		return c.dialStreamOne(ctx, sessionID)
	default:
		return nil, E.New("v2ray-xhttp: unknown mode: ", c.mode)
	}
}

func (c *Client) Close() error {
	if transport, ok := c.transport.(*http2.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

// requestURL builds the full URL for the given path suffix elements appended to
// the configured base path, e.g. "<path>/<sessionId>" or
// "<path>/<sessionId>/<seq>".
func (c *Client) requestURL(elem ...string) (*url.URL, error) {
	u := &url.URL{
		Scheme: c.scheme,
		Host:   c.serverAddr.String(),
	}
	// stream-one targets the bare normalized path with no trailing segment (and
	// no sessionId): Xray's splithttp server routes a request to the bidirectional
	// stream-one handler only when sessionId is empty, so the URL must be exactly
	// "<path>" — not "<path>/" (a trailing slash would be parsed as an empty
	// sessionId segment by some server-side path matchers). strings.Join of an
	// empty slice yields "", which would otherwise produce "<path>/".
	if len(elem) == 0 {
		if err := sHTTP.URLSetPath(u, c.path); err != nil {
			return nil, E.Cause(err, "parse path")
		}
		if !strings.HasPrefix(u.Path, "/") {
			u.Path = "/" + u.Path
		}
		return u, nil
	}
	fullPath := c.path + "/" + strings.Join(elem, "/")
	if err := sHTTP.URLSetPath(u, fullPath); err != nil {
		return nil, E.Cause(err, "parse path")
	}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u, nil
}

// newRequest constructs an XHTTP request with the shared headers, host and a
// fresh random X-Padding value applied.
func (c *Client) newRequest(ctx context.Context, method string, u *url.URL, body interface{ Read([]byte) (int, error) }) *http.Request {
	request := &http.Request{
		Method: method,
		URL:    u,
		Header: c.headers.Clone(),
		Host:   c.host,
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	// Padding placement matches Xray's default (XPaddingObfsMode off): the value is
	// carried as a query param "x_padding=<value>" inside the Referer header
	// (PlacementQueryInHeader, key "x_padding"). The server validates the x_padding
	// length against its xPaddingBytes range (default 100-1000) and replies 400 if it
	// is missing or out of range — so it must live in the Referer, not a standalone
	// X-Padding header. Verified live against an Xray (3x-ui) XHTTP server.
	referer := &url.URL{Scheme: c.scheme, Host: c.host, Path: u.Path}
	rq := referer.Query()
	rq.Set("x_padding", c.padding())
	referer.RawQuery = rq.Encode()
	request.Header.Set("Referer", referer.String())
	if body != nil {
		request.Body = readCloser{body}
	}
	return request.WithContext(ctx)
}

func (c *Client) padding() string {
	n := c.paddingMin
	if c.paddingMax > c.paddingMin {
		n += rand.Intn(c.paddingMax - c.paddingMin + 1)
	}
	if n <= 0 {
		return ""
	}
	return strings.Repeat("0", n)
}

// newSessionID returns a random session id formatted as a dashed UUID string
// (8-4-4-4-12), matching Xray's sessionId = uuid.New().String() (verified against
// XTLS/Xray-core transport/internet/splithttp dialer.go). The server treats it as
// an opaque grouping key; the dashed format keeps it interchangeable with Xray.
func newSessionID() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	const hexdigits = "0123456789abcdef"
	var h [32]byte
	for i, v := range b {
		h[i*2] = hexdigits[v>>4]
		h[i*2+1] = hexdigits[v&0x0f]
	}
	return string(h[0:8]) + "-" + string(h[8:12]) + "-" + string(h[12:16]) + "-" + string(h[16:20]) + "-" + string(h[20:32])
}

func parsePaddingRange(raw string) (int, int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 100, 1000, nil
	}
	if !strings.Contains(raw, "-") {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0, 0, E.Cause(err, "parse x_padding_bytes")
		}
		return v, v, nil
	}
	parts := strings.SplitN(raw, "-", 2)
	minV, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, E.Cause(err, "parse x_padding_bytes min")
	}
	maxV, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, E.Cause(err, "parse x_padding_bytes max")
	}
	if maxV < minV {
		minV, maxV = maxV, minV
	}
	return minV, maxV, nil
}

// readCloser adapts a plain reader to io.ReadCloser for use as a request body
// without pulling in an extra import.
type readCloser struct {
	r interface{ Read([]byte) (int, error) }
}

func (r readCloser) Read(p []byte) (int, error) { return r.r.Read(p) }
func (r readCloser) Close() error               { return nil }

// drainAndClose fully discards then closes an HTTP response body.
func drainAndClose(body interface {
	Read([]byte) (int, error)
	Close() error
}) {
	buffer := buf.Get(buf.BufferSize)
	for {
		if _, err := body.Read(buffer); err != nil {
			break
		}
	}
	buf.Put(buffer)
	_ = body.Close()
}
