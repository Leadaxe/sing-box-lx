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
	paddingRange intRange
	// meta holds the normalized placement/key/method selection (session, seq,
	// uplink-data, X-Padding obfs). Computed once in NewClient.
	meta metaConfig
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

	paddingRange, err := parseRangeOr(options.XPaddingBytes, "x_padding_bytes", intRange{100, 1000})
	if err != nil {
		return nil, err
	}

	meta, err := normalizeMeta(metaOptions{
		SessionPlacement:     options.SessionPlacement,
		SessionKey:           options.SessionKey,
		SeqPlacement:         options.SeqPlacement,
		SeqKey:               options.SeqKey,
		UplinkDataPlacement:  options.UplinkDataPlacement,
		UplinkDataKey:        options.UplinkDataKey,
		UplinkChunkSize:      options.UplinkChunkSize,
		UplinkHTTPMethod:     options.UplinkHTTPMethod,
		XPaddingObfsMode:     options.XPaddingObfsMode,
		XPaddingKey:          options.XPaddingKey,
		XPaddingHeader:       options.XPaddingHeader,
		XPaddingPlacement:    options.XPaddingPlacement,
		XPaddingMethod:       options.XPaddingMethod,
		ScMaxEachPostBytes:   options.ScMaxEachPostBytes,
		ScMinPostsIntervalMs: options.ScMinPostsIntervalMs,
	}, mode)
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

	// Keep the configured path verbatim (only guarantee a leading slash). A
	// trailing slash is load-bearing: reverse proxies (e.g. nginx `location
	// /upload/ {}`) 301-redirect a bare "/upload" to "/upload/", and our download
	// RoundTrip does not follow redirects, so the 301 surfaces as a dial error.
	// The one place the slash must go is stream-one's bare path (empty sessionId),
	// where the Xray server keys the bidirectional branch on an exact bare path —
	// that trim happens locally in applyMeta, not globally here (lx: SPEC 002).
	path := options.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

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
		paddingRange:   paddingRange,
		meta:           meta,
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

// baseURL builds a fresh request URL targeting the normalized base path. The
// placement engine (applyMeta) appends session/seq path segments and query params
// as configured; applyXPadding attaches the padding. The base path is set via
// sHTTP.URLSetPath so percent-encoding matches the rest of sing-box.
func (c *Client) baseURL() (*url.URL, error) {
	u := &url.URL{
		Scheme: c.scheme,
		Host:   c.serverAddr.String(),
	}
	if err := sHTTP.URLSetPath(u, c.path); err != nil {
		return nil, E.Cause(err, "parse path")
	}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u, nil
}

// newRequest constructs an XHTTP request: it builds the base URL, lets the
// placement engine position the sessionID and (packet-up) seqStr, then attaches
// X-Padding. An empty sessionID emits no session metadata (stream-one targets the
// bare path with no sessionId, which is how the server routes the bidirectional
// branch). An empty seqStr emits no seq (stream modes).
func (c *Client) newRequest(ctx context.Context, method, sessionID, seqStr string, body interface{ Read([]byte) (int, error) }) (*http.Request, error) {
	u, err := c.baseURL()
	if err != nil {
		return nil, err
	}
	basePath := u.Path
	request := &http.Request{
		Method: method,
		URL:    u,
		Header: c.headers.Clone(),
		Host:   c.host,
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	c.applyMeta(request, basePath, sessionID, seqStr)
	c.applyXPadding(request)
	if body != nil {
		request.Body = readCloser{body}
	}
	return request.WithContext(ctx), nil
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
},
) {
	buffer := buf.Get(buf.BufferSize)
	for {
		if _, err := body.Read(buffer); err != nil {
			break
		}
	}
	buf.Put(buffer)
	_ = body.Close()
}
