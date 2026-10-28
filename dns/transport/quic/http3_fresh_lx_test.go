//go:build with_lx_command

package quic

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

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/lxtrace"
	"github.com/sagernet/sing-box/dns/transport"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// lx: SPEC 139 — DoH3 under a connection test with fresh=true: every fresh
// query runs on a one-off QUIC connection closed after it, and the
// production client keeps its connection.

type doh3TestServer struct {
	address string
	client  *stdtls.Config
	opened  atomic.Int32
	closed  atomic.Int32
}

func newDoH3TestServer(t *testing.T) *doh3TestServer {
	// httptest only lends its certificate and a client config trusting it.
	certSource := httptest.NewTLSServer(http.NotFoundHandler())
	certificates := certSource.TLS.Certificates
	client := certSource.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	certSource.Close()
	client.ServerName = "example.com" // in the httptest certificate
	client.NextProtos = []string{http3.NextProtoH3}

	s := &doh3TestServer{client: client}
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	s.address = packetConn.LocalAddr().String()
	server := &http3.Server{
		TLSConfig: http3.ConfigureTLSConfig(&stdtls.Config{Certificates: certificates}),
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
			writer.Header().Set("Content-Type", transport.MimeType)
			writer.Write(packed)
		}),
		ConnContext: func(ctx context.Context, conn *quic.Conn) context.Context {
			s.opened.Add(1)
			go func() {
				<-conn.Context().Done()
				s.closed.Add(1)
			}()
			return ctx
		},
	}
	go server.Serve(packetConn)
	t.Cleanup(func() {
		server.Close()
		packetConn.Close()
	})
	return s
}

func newDoH3TestTransport(t *testing.T, s *doh3TestServer) *HTTP3Transport {
	serverAddr := M.SocksaddrFromNetIP(netip.MustParseAddrPort(s.address))
	h3 := &HTTP3Transport{
		dialer:      N.SystemDialer,
		destination: &url.URL{Scheme: "https", Host: s.address, Path: "/dns-query"},
		headers:     http.Header{},
		serverAddr:  serverAddr,
		tlsConfig:   s.client,
	}
	h3.transport = h3.newTransport()
	t.Cleanup(func() { h3.transport.Close() })
	return h3
}

func doh3TestQuery() *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)
	return message
}

func doh3AttrValues(stage lxtrace.StageSnapshot, key string) []string {
	var values []string
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			values = append(values, attr.Value)
		}
	}
	return values
}

func TestHTTP3FreshOneOffClient_LX(t *testing.T) {
	server := newDoH3TestServer(t)
	h3 := newDoH3TestTransport(t, server)
	production := h3.transport

	_, err := h3.Exchange(context.Background(), doh3TestQuery())
	require.NoError(t, err)
	require.EqualValues(t, 1, server.opened.Load())

	for i := 0; i < 2; i++ {
		trace := lxtrace.New(lxtrace.Options{Fresh: true, Probe: true})
		ctx, dnsStage := lxtrace.Begin(lxtrace.With(context.Background(), trace), "dns", "remote")
		response, err := h3.Exchange(ctx, doh3TestQuery())
		require.NoError(t, err)
		require.Len(t, response.Answer, 1)
		dnsStage.End(nil)
		summary := trace.Finish(nil)
		require.Equal(t, []string{"new"}, doh3AttrValues(summary.Stages[0], "connection"))
		require.Equal(t, []string{"detached"}, doh3AttrValues(summary.Stages[0], "pool"))
	}
	require.EqualValues(t, 3, server.opened.Load(), "each fresh query opens a connection of its own")
	require.Eventually(t, func() bool { return server.closed.Load() == 2 }, 5*time.Second, 10*time.Millisecond,
		"the one-off connections are closed after their exchange")

	_, err = h3.Exchange(context.Background(), doh3TestQuery())
	require.NoError(t, err)
	require.EqualValues(t, 3, server.opened.Load(), "production reuses its own connection")
	require.EqualValues(t, 2, server.closed.Load(), "the production connection stays open")
	require.Same(t, production, h3.transport)
}

func TestHTTP3FreshNoTrace_LX(t *testing.T) {
	var h3 HTTP3Transport
	current := new(http3.Transport)
	allocs := testing.AllocsPerRun(100, func() {
		got, release := h3.freshTransportLx(context.Background(), current)
		release()
		if got != current {
			t.Fatal("production client replaced without a trace")
		}
	})
	require.Zero(t, allocs)
	trace := lxtrace.New(lxtrace.Options{Probe: true})
	got, release := h3.freshTransportLx(lxtrace.With(context.Background(), trace), current)
	release()
	require.Same(t, current, got, "a test without fresh uses the production client")
}
