package trusttunnel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"golang.org/x/net/http2"
)

const (
	testUsername = "user"
	testPassword = "secret"
	testHoldHost = "hold.test"
)

// testTLSConfig adapts a crypto/tls config to the sing TLS interfaces.
type testTLSConfig struct {
	config *tls.Config
}

func (c *testTLSConfig) ServerName() string                { return c.config.ServerName }
func (c *testTLSConfig) SetServerName(serverName string)   { c.config.ServerName = serverName }
func (c *testTLSConfig) NextProtos() []string              { return c.config.NextProtos }
func (c *testTLSConfig) SetNextProtos(nextProto []string)  { c.config.NextProtos = nextProto }
func (c *testTLSConfig) HandshakeTimeout() time.Duration   { return 0 }
func (c *testTLSConfig) SetHandshakeTimeout(time.Duration) {}
func (c *testTLSConfig) STDConfig() (*tls.Config, error)   { return c.config, nil }
func (c *testTLSConfig) Clone() aTLS.Config                { return &testTLSConfig{c.config.Clone()} }
func (c *testTLSConfig) Start() error                      { return nil }
func (c *testTLSConfig) Close() error                      { return nil }
func (c *testTLSConfig) Client(conn net.Conn) (aTLS.Conn, error) {
	return tls.Client(conn, c.config), nil
}

func (c *testTLSConfig) Server(conn net.Conn) (aTLS.Conn, error) {
	return tls.Server(conn, c.config), nil
}

func newTestTLS(t *testing.T) (server *testTLSConfig, client *testTLSConfig) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	server = &testTLSConfig{&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}}
	client = &testTLSConfig{&tls.Config{
		ServerName: "localhost",
		RootCAs:    pool,
	}}
	return
}

// testDialer dials directly; with blackhole set, every connection it opened
// silently drops traffic in both directions (a NAT that forgot the mapping).
type testDialer struct {
	blackhole atomic.Bool
	dials     atomic.Int32
}

func (d *testDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := new(net.Dialer).DialContext(ctx, network, destination.String())
	if err != nil {
		return nil, err
	}
	d.dials.Add(1)
	return &blackholeConn{Conn: conn, blackhole: &d.blackhole}, nil
}

func (d *testDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp", "")
}

type blackholeConn struct {
	net.Conn
	blackhole *atomic.Bool
}

func (c *blackholeConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.blackhole.Load() {
			return n, err
		}
	}
}

func (c *blackholeConn) Write(p []byte) (int, error) {
	if c.blackhole.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

// testHandler forwards TCP to the real destination (502 on a failed dial), keeps
// connections to hold.test open without dialing, and echoes UDP back.
type testHandler struct {
	access  sync.Mutex
	tcp     []Metadata
	udp     []Metadata
	holding atomic.Int32
}

func (h *testHandler) NewConnection(ctx context.Context, conn *ServerConn, metadata Metadata) {
	h.access.Lock()
	h.tcp = append(h.tcp, metadata)
	h.access.Unlock()
	go func() {
		if metadata.Destination.Fqdn == testHoldHost {
			h.holding.Add(1)
			defer h.holding.Add(-1)
			io.Copy(io.Discard, conn)
			conn.Close()
			return
		}
		remote, err := net.DialTimeout("tcp", metadata.Destination.String(), 5*time.Second)
		if err != nil {
			conn.Fail(err)
			return
		}
		go func() {
			io.Copy(remote, conn)
			remote.Close()
		}()
		io.Copy(conn, remote)
		conn.Close()
	}()
}

func (h *testHandler) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata Metadata) {
	h.access.Lock()
	h.udp = append(h.udp, metadata)
	h.access.Unlock()
	go func() {
		defer conn.Close()
		for {
			buffer := buf.NewSize(MaxPayloadSize)
			destination, err := conn.ReadPacket(buffer)
			if err != nil {
				buffer.Release()
				return
			}
			err = conn.WritePacket(buffer, destination)
			if err != nil {
				return
			}
		}
	}()
}

func (h *testHandler) udpMetadata() []Metadata {
	h.access.Lock()
	defer h.access.Unlock()
	return append([]Metadata(nil), h.udp...)
}

type testServer struct {
	service *Service
	handler *testHandler
	address M.Socksaddr
	client  *testTLSConfig
}

func startTestServer(t *testing.T, protocol string) *testServer {
	t.Helper()
	handler := &testHandler{}
	service, err := NewService(ServiceOptions{
		Handler: handler,
		Users:   []auth.User{{Username: testUsername, Password: testPassword}},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, clientTLS := newTestTLS(t)
	var address M.Socksaddr
	if protocol == ProtocolHTTP3 {
		packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { packetConn.Close() })
		address = M.SocksaddrFromNet(packetConn.LocalAddr())
		go service.ServeQUIC(packetConn, serverTLS)
	} else {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address = M.SocksaddrFromNet(listener.Addr())
		go service.ServeTCP(listener, serverTLS)
	}
	t.Cleanup(func() { service.Close() })
	return &testServer{service: service, handler: handler, address: address, client: clientTLS}
}

func (s *testServer) newClient(t *testing.T, protocol string, password string, dialer N.Dialer, modify func(*ClientOptions)) *Client {
	t.Helper()
	options := ClientOptions{
		Dialer:    dialer,
		Server:    s.address,
		TLSConfig: s.client.Clone(),
		Protocol:  protocol,
		Username:  testUsername,
		Password:  password,
		AppName:   "sing-box-test",
	}
	if modify != nil {
		modify(&options)
	}
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func startEchoServer(t *testing.T) M.Socksaddr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(conn, conn)
				conn.Close()
			}()
		}
	}()
	return M.SocksaddrFromNet(listener.Addr())
}

func closedPort(t *testing.T) M.Socksaddr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := M.SocksaddrFromNet(listener.Addr())
	listener.Close()
	return address
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func echoRoundTrip(t *testing.T, conn net.Conn, size int) {
	t.Helper()
	payload := make([]byte, size)
	rand.Read(payload)
	go conn.Write(payload)
	received := make([]byte, size)
	_, err := io.ReadFull(conn, received)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, received) {
		t.Fatal("echo mismatch")
	}
}

func runEndToEnd(t *testing.T, protocol string) {
	t.Run("TCP", func(t *testing.T) { testTCP(t, protocol) })
	t.Run("UDP", func(t *testing.T) { testUDP(t, protocol) })
	t.Run("Check", func(t *testing.T) { testCheck(t, protocol) })
	t.Run("WrongPassword", func(t *testing.T) { testWrongPassword(t, protocol) })
	t.Run("DeferredFailure", func(t *testing.T) { testDeferredFailure(t, protocol) })
	t.Run("ReservedAuthorities", func(t *testing.T) { testReservedAuthorities(t, protocol) })
	t.Run("StreamLimit", func(t *testing.T) { testStreamLimit(t, protocol) })
	t.Run("StaleSession", func(t *testing.T) { testStaleSession(t, protocol) })
	t.Run("IdleTeardown", func(t *testing.T) { testIdleTeardown(t, protocol) })
	t.Run("HealthCheck", func(t *testing.T) { testHealthCheck(t, protocol) })
}

func TestHTTP2(t *testing.T) {
	runEndToEnd(t, ProtocolHTTP2)
	t.Run("NonConnect", testNonConnect)
	t.Run("HealthCheckFailure", testHealthCheckFailure)
}

func testTCP(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	echo := startEchoServer(t)
	dialer := &testDialer{}
	client := server.newClient(t, protocol, testPassword, dialer, nil)
	ctx := testContext(t)
	for i := 0; i < 3; i++ {
		conn, err := client.DialContext(ctx, echo)
		if err != nil {
			t.Fatal(err)
		}
		if conn.RemoteAddr() == nil || conn.LocalAddr() == nil {
			t.Fatal("nil address")
		}
		echoRoundTrip(t, conn, 256*1024)
		conn.Close()
	}
	if dials := dialer.dials.Load(); dials != 1 {
		t.Fatalf("%d sessions opened, want 1", dials)
	}
	server.handler.access.Lock()
	metadata := server.handler.tcp[0]
	server.handler.access.Unlock()
	if metadata.User != testUsername || metadata.Destination != echo || metadata.Network != N.NetworkTCP || !metadata.Source.Addr.IsLoopback() {
		t.Fatalf("metadata %+v", metadata)
	}
}

func testUDP(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	client := server.newClient(t, protocol, testPassword, &testDialer{}, nil)
	ctx := testContext(t)
	packetConn, err := client.ListenPacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	destinations := []M.Socksaddr{M.ParseSocksaddr("127.0.0.1:5353"), M.ParseSocksaddr("[2001:db8::53]:5353")}
	for round := 0; round < 2; round++ {
		for i, destination := range destinations {
			payload := bytes.Repeat([]byte{byte(i + 1)}, 1000*(i+1))
			_, err = packetConn.WriteTo(payload, destination.UDPAddr())
			if err != nil {
				t.Fatal(err)
			}
			received := make([]byte, 65535)
			n, source, err := packetConn.ReadFrom(received)
			if err != nil {
				t.Fatal(err)
			}
			if M.SocksaddrFromNet(source) != destination || !bytes.Equal(received[:n], payload) {
				t.Fatalf("reply from %s, %d bytes", source, n)
			}
		}
	}
	// A large datagram goes through the buffer API.
	buffer := buf.NewSize(packetConn.FrontHeadroom() + MaxPayloadSize)
	buffer.Advance(packetConn.FrontHeadroom())
	buffer.Extend(MaxPayloadSize)
	err = packetConn.WritePacket(buffer, destinations[0])
	if err != nil {
		t.Fatal(err)
	}
	reply := buf.NewSize(MaxPayloadSize)
	defer reply.Release()
	source, err := packetConn.ReadPacket(reply)
	if err != nil || source != destinations[0] || reply.Len() != MaxPayloadSize {
		t.Fatalf("large datagram: %v %s %d", err, source, reply.Len())
	}
	if err = packetConn.WritePacket(buf.NewSize(64), M.ParseSocksaddrHostPort("example.com", 53)); err == nil {
		t.Fatal("domain destination accepted")
	}
	flows := server.handler.udpMetadata()
	if len(flows) != 2 {
		t.Fatalf("%d flows, want 2", len(flows))
	}
	for _, flow := range flows {
		if flow.User != testUsername || flow.Network != N.NetworkUDP || !flow.Source.Addr.IsLoopback() {
			t.Fatalf("flow metadata %+v", flow)
		}
	}
}

func testCheck(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	client := server.newClient(t, protocol, testPassword, &testDialer{}, nil)
	if err := client.Check(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func testWrongPassword(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	echo := startEchoServer(t)
	dialer := &testDialer{}
	client := server.newClient(t, protocol, "wrong", dialer, nil)
	ctx := testContext(t)
	_, err := client.DialContext(ctx, echo)
	var statusError *StatusError
	if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusProxyAuthRequired || !IsAuthenticationRequired(err) {
		t.Fatalf("got %v, want a 407", err)
	}
	client.access.Lock()
	session := client.session
	client.access.Unlock()
	if session != nil {
		t.Fatal("session kept after 407")
	}
	server.service.UpdateUsers([]auth.User{{Username: testUsername, Password: "wrong"}})
	conn, err := client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	echoRoundTrip(t, conn, 1024)
	conn.Close()
	if dials := dialer.dials.Load(); dials != 2 {
		t.Fatalf("%d sessions opened, want 2", dials)
	}
}

func testDeferredFailure(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	client := server.newClient(t, protocol, testPassword, &testDialer{}, nil)
	ctx := testContext(t)
	_, err := client.DialContext(ctx, closedPort(t))
	var statusError *StatusError
	if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusBadGateway || statusError.Warning != WarningConnectionFailed {
		t.Fatalf("got %v, want 502 with 300", err)
	}
	// The session survives a failed CONNECT.
	if err = client.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func testReservedAuthorities(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	client := server.newClient(t, protocol, testPassword, &testDialer{}, nil)
	ctx := testContext(t)
	for _, testCase := range []struct {
		authority  string
		statusCode int
	}{
		{"example.com", http.StatusBadRequest},
		{"example.com:0", http.StatusBadRequest},
		{AuthorityICMP, http.StatusNotFound},
	} {
		_, _, err := client.openStream(ctx, testCase.authority, UserAgent("test"), true)
		var statusError *StatusError
		if !errors.As(err, &statusError) || statusError.StatusCode != testCase.statusCode {
			t.Errorf("%s: got %v, want %d", testCase.authority, err, testCase.statusCode)
		}
	}
}

func testStreamLimit(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	dialer := &testDialer{}
	client := server.newClient(t, protocol, testPassword, dialer, nil)
	ctx := testContext(t)
	limit := serverMaxConcurrentStreams
	if protocol == ProtocolHTTP3 {
		limit = serverMaxIncomingStreams
	}
	destination := M.ParseSocksaddrHostPort(testHoldHost, 1)
	conns := make([]net.Conn, limit)
	var group sync.WaitGroup
	var failures atomic.Int32
	for i := range conns {
		group.Add(1)
		go func() {
			defer group.Done()
			conn, err := client.DialContext(ctx, destination)
			if err != nil {
				failures.Add(1)
				return
			}
			conns[i] = conn
		}()
	}
	group.Wait()
	if failures.Load() > 0 {
		t.Fatalf("%d of %d concurrent streams failed", failures.Load(), limit)
	}
	// One stream above the limit waits for a slot within its deadline instead
	// of opening a second session.
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err := client.DialContext(waitCtx, destination)
	cancel()
	if err == nil {
		t.Fatal("stream above the limit opened")
	}
	conns[0].Close()
	conn, err := client.DialContext(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	for _, conn := range conns[1:] {
		conn.Close()
	}
	if dials := dialer.dials.Load(); dials != 1 {
		t.Fatalf("%d sessions opened, want 1", dials)
	}
}

func testStaleSession(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	echo := startEchoServer(t)
	dialer := &testDialer{}
	client := server.newClient(t, protocol, testPassword, dialer, nil)
	ctx := testContext(t)
	if err := client.Check(ctx); err != nil {
		t.Fatal(err)
	}
	// The path dies while the session looks idle: the next dial spends the
	// short budget on it, rebuilds and succeeds within its own deadline.
	client.access.Lock()
	client.session.received.Store(time.Now().Add(-2 * DefaultStaleWindow).UnixNano())
	client.access.Unlock()
	dialer.blackhole.Store(true)
	newDialer := &testDialer{}
	client.dialer = newDialer
	start := time.Now()
	conn, err := client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	echoRoundTrip(t, conn, 1024)
	conn.Close()
	if newDialer.dials.Load() != 1 {
		t.Fatal("session not rebuilt")
	}
	if elapsed < staleConnectBudget || elapsed > staleConnectBudget+5*time.Second {
		t.Fatalf("stale dial took %s", elapsed)
	}
}

func testIdleTeardown(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	dialer := &testDialer{}
	client := server.newClient(t, protocol, testPassword, dialer, func(options *ClientOptions) {
		options.IdleTimeout = time.Second
	})
	ctx := testContext(t)
	conn, err := client.DialContext(ctx, startEchoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	// An open stream keeps the session, and a silent stream outlives the idle
	// window: the window is the client's, not the transport's.
	time.Sleep(2500 * time.Millisecond)
	client.access.Lock()
	session := client.session
	client.access.Unlock()
	if session == nil {
		t.Fatal("session closed with an open stream")
	}
	echoRoundTrip(t, conn, 1024)
	conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		client.access.Lock()
		session = client.session
		client.access.Unlock()
		if session == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle session not closed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = client.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if dials := dialer.dials.Load(); dials != 2 {
		t.Fatalf("%d sessions opened, want 2", dials)
	}
}

func testNonConnect(t *testing.T) {
	server := startTestServer(t, ProtocolHTTP2)
	clientTLS := server.client.config.Clone()
	clientTLS.NextProtos = []string{http2.NextProtoTLS}
	transport := &http2.Transport{TLSClientConfig: clientTLS}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, "https://"+server.address.String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || response.Header.Get("Proxy-Authenticate") != "" {
		t.Fatalf("got %d, proxy-authenticate %q", response.StatusCode, response.Header.Get("Proxy-Authenticate"))
	}

	// A client without h2 in ALPN gets no HTTP/1.1 answer.
	clientTLS = server.client.config.Clone()
	clientTLS.NextProtos = []string{"http/1.1"}
	conn, err := tls.Dial("tcp", server.address.String(), clientTLS)
	if err == nil {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))
		if err == nil {
			_, err = conn.Read(make([]byte, 1))
		}
		conn.Close()
	}
	if err == nil {
		t.Fatal("HTTP/1.1 served")
	}
}

func markSilent(client *Client, silence time.Duration) *clientSession {
	client.access.Lock()
	defer client.access.Unlock()
	past := time.Now().Add(-silence).UnixNano()
	client.session.received.Store(past)
	client.session.checked.Store(past)
	return client.session
}

func testHealthCheck(t *testing.T, protocol string) {
	server := startTestServer(t, protocol)
	echo := startEchoServer(t)
	dialer := &testDialer{}
	client := server.newClient(t, protocol, testPassword, dialer, func(options *ClientOptions) {
		options.HealthCheck = true
	})
	ctx := testContext(t)
	conn, err := client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	// Silent for less than the throttle: no check.
	session := markSilent(client, healthCheckThrottle/2)
	before := session.checked.Load()
	conn, err = client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if session.checked.Load() != before {
		t.Fatal("health check sent within the throttle")
	}
	// Silent for longer: the dial checks first, the session survives.
	markSilent(client, 2*healthCheckThrottle)
	conn, err = client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	echoRoundTrip(t, conn, 1024)
	conn.Close()
	if session.checked.Load() == before {
		t.Fatal("health check not sent")
	}
	if dials := dialer.dials.Load(); dials != 1 {
		t.Fatalf("%d sessions opened, want 1", dials)
	}
}

func testHealthCheckFailure(t *testing.T) {
	server := startTestServer(t, ProtocolHTTP2)
	echo := startEchoServer(t)
	dialer := &testDialer{}
	client := server.newClient(t, ProtocolHTTP2, testPassword, dialer, func(options *ClientOptions) {
		options.HealthCheck = true
	})
	ctx := testContext(t)
	if err := client.Check(ctx); err != nil {
		t.Fatal(err)
	}
	markSilent(client, 2*healthCheckThrottle)
	dialer.blackhole.Store(true)
	newDialer := &testDialer{}
	client.dialer = newDialer
	start := time.Now()
	conn, err := client.DialContext(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	echoRoundTrip(t, conn, 1024)
	conn.Close()
	if newDialer.dials.Load() != 1 {
		t.Fatal("session not rebuilt after a failed check")
	}
	if elapsed < healthCheckTimeout || elapsed > healthCheckTimeout+5*time.Second {
		t.Fatalf("dial took %s", elapsed)
	}
}
