//go:build with_lx_command

package transport

import (
	"context"
	stdtls "crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/dns"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// lx: SPEC 139 — DoH under a connection test with fresh=true: every fresh
// query runs on a one-off connection closed after it, the production client
// keeps its connection, and a fresh query's timeout does not reset it.

type dohTestServer struct {
	server *httptest.Server
	opened atomic.Int32
	closed atomic.Int32
	hang   atomic.Bool
	unhang chan struct{}
}

func newDoHTestServer(t *testing.T) *dohTestServer {
	s := &dohTestServer{unhang: make(chan struct{})}
	s.server = httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if s.hang.Load() {
			select {
			case <-s.unhang:
			case <-request.Context().Done():
			}
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var query mDNS.Msg
		if query.Unpack(body) != nil || len(query.Question) == 0 {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		response := new(mDNS.Msg)
		response.SetReply(&query)
		response.Answer = append(response.Answer, &mDNS.A{
			Hdr: mDNS.RR_Header{Name: query.Question[0].Name, Rrtype: mDNS.TypeA, Class: mDNS.ClassINET, Ttl: 60},
			A:   net.IPv4(192, 0, 2, 1),
		})
		packed, err := response.Pack()
		if err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", MimeType)
		writer.Write(packed)
	}))
	s.server.EnableHTTP2 = true
	s.server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			s.opened.Add(1)
		case http.StateClosed, http.StateHijacked:
			s.closed.Add(1)
		}
	}
	s.server.StartTLS()
	t.Cleanup(func() {
		select {
		case <-s.unhang:
		default:
			close(s.unhang)
		}
		s.server.Close()
	})
	return s
}

// dohTestDialer dials the test server over TLS with h2, as the TLS dialer of
// a real DoH server does.
type dohTestDialer struct {
	address string
	config  *stdtls.Config
}

func (d *dohTestDialer) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, d.address)
	if err != nil {
		return nil, err
	}
	tlsConn := stdtls.Client(conn, d.config)
	err = tlsConn.HandshakeContext(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (d *dohTestDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

var _ N.Dialer = (*dohTestDialer)(nil)

func newDoHTestTransport(t *testing.T, s *dohTestServer) *HTTPSTransport {
	serverURL, err := url.Parse(s.server.URL)
	require.NoError(t, err)
	config := s.server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	config.NextProtos = []string{http2.NextProtoTLS}
	config.ServerName = "example.com" // in the httptest certificate
	destination := &url.URL{Scheme: "https", Host: serverURL.Host, Path: "/dns-query"}
	serverAddr := M.SocksaddrFromNetIP(netip.MustParseAddrPort(serverURL.Host))
	transport := NewHTTPSRaw(dns.TransportAdapter{}, nil, &dohTestDialer{address: serverURL.Host, config: config}, destination, http.Header{}, serverAddr, nil)
	t.Cleanup(transport.Reset)
	return transport
}

func dohTestQuery() *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)
	return message
}

func freshDNS() (context.Context, *lxtrace.Trace, *lxtrace.Stage) {
	return tracedDNS(lxtrace.Options{Fresh: true, Probe: true})
}

func TestHTTPSFreshOneOffClient_LX(t *testing.T) {
	server := newDoHTestServer(t)
	transport := newDoHTestTransport(t, server)
	production := transport.transport

	_, err := transport.Exchange(context.Background(), dohTestQuery())
	require.NoError(t, err)
	require.EqualValues(t, 1, server.opened.Load())

	for i := 0; i < 2; i++ {
		ctx, trace, dnsStage := freshDNS()
		response, err := transport.Exchange(ctx, dohTestQuery())
		require.NoError(t, err)
		require.Len(t, response.Answer, 1)
		dnsStage.End(nil)
		summary := trace.Finish(nil)
		require.Equal(t, []string{"new"}, attrValues(summary.Stages[0], "connection"))
		require.Equal(t, []string{"detached"}, attrValues(summary.Stages[0], "pool"))
	}
	require.EqualValues(t, 3, server.opened.Load(), "each fresh query opens a connection of its own")
	require.Eventually(t, func() bool { return server.closed.Load() == 2 }, 5*time.Second, 10*time.Millisecond,
		"the one-off connections are closed after their exchange")

	_, err = transport.Exchange(context.Background(), dohTestQuery())
	require.NoError(t, err)
	require.EqualValues(t, 3, server.opened.Load(), "production reuses its own connection")
	require.EqualValues(t, 2, server.closed.Load(), "the production connection stays open")
	require.Same(t, production, transport.transport)
}

func TestHTTPSFreshTimeoutKeepsProduction_LX(t *testing.T) {
	server := newDoHTestServer(t)
	transport := newDoHTestTransport(t, server)
	_, err := transport.Exchange(context.Background(), dohTestQuery())
	require.NoError(t, err)
	production := transport.transport

	server.hang.Store(true)
	ctx, _, _ := freshDNS()
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = transport.Exchange(ctx, dohTestQuery())
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Same(t, production, transport.transport, "a fresh timeout does not reset the production client")
	require.True(t, transport.transportResetAt.IsZero())

	// The same timeout outside a test does reset it: the guard is what kept it.
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, err = transport.Exchange(ctx, dohTestQuery())
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotSame(t, production, transport.transport)
}

func TestHTTPSFreshNoTrace_LX(t *testing.T) {
	var transport HTTPSTransport
	current := new(HTTPSTransportWrapper)
	allocs := testing.AllocsPerRun(100, func() {
		got, release := transport.freshTransportLx(context.Background(), current)
		release()
		if got != current {
			t.Fatal("production client replaced without a trace")
		}
	})
	require.Zero(t, allocs)
	ctx, _, _ := tracedDNS(lxtrace.Options{Probe: true})
	got, release := transport.freshTransportLx(ctx, current)
	release()
	require.Same(t, current, got, "a test without fresh uses the production client")
}
