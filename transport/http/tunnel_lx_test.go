package http

import (
	std_bufio "bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

// fakeHTTP3Client stands in for the QUIC client: OpenTunnel either returns
// openStream at once or, with silent set, blocks until its context is done.
type fakeHTTP3Client struct {
	silent     bool
	openStream DatagramStream
	calls      atomic.Int32
}

func (c *fakeHTTP3Client) DialContext(ctx context.Context, destination M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (c *fakeHTTP3Client) OpenTunnel(ctx context.Context, request tunnelRequest) (DatagramStream, error) {
	c.calls.Add(1)
	if c.silent {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.openStream, nil
}

func (c *fakeHTTP3Client) ResetConnection() {}

func (c *fakeHTTP3Client) Close() error { return nil }

type fakeDatagramStream struct {
	net.Conn
}

func (s *fakeDatagramStream) SendDatagram(payload []byte) error { return nil }

func (s *fakeDatagramStream) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func testTunnelRequest() tunnelRequest {
	return tunnelRequest{
		protocol:    "cf-connect-ip",
		url:         &url.URL{Path: "/"},
		destination: M.ParseSocksaddr("127.0.0.1:443"),
	}
}

func TestOpenTunnelHTTP3ConnectBudget(t *testing.T) {
	t.Parallel()
	const budget = 50 * time.Millisecond
	client := &Client{
		http3:    &fakeHTTP3Client{silent: true},
		tunnelLX: TunnelLXOptions{HTTP3ConnectBudget: budget},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	stream, err := client.openTunnelHTTP3(ctx, testTunnelRequest())
	elapsed := time.Since(start)
	require.Nil(t, stream)
	require.ErrorIs(t, err, ErrHTTP3Unavailable)
	require.GreaterOrEqual(t, elapsed, budget)
	require.Less(t, elapsed, 2*time.Second)
	require.NoError(t, ctx.Err())
}

func TestOpenTunnelHTTP3ParentCancelled(t *testing.T) {
	t.Parallel()
	client := &Client{
		http3:    &fakeHTTP3Client{silent: true},
		tunnelLX: TunnelLXOptions{HTTP3ConnectBudget: 10 * time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := client.openTunnelHTTP3(ctx, testTunnelRequest())
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrHTTP3Unavailable)
}

func TestOpenTunnelHTTP3ParentDeadlineFirst(t *testing.T) {
	t.Parallel()
	// The parent deadline runs out before the budget: that is the caller's
	// timeout, not a silent HTTP/3 endpoint.
	client := &Client{
		http3:    &fakeHTTP3Client{silent: true},
		tunnelLX: TunnelLXOptions{HTTP3ConnectBudget: 10 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.openTunnelHTTP3(ctx, testTunnelRequest())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrHTTP3Unavailable)
}

func TestOpenTunnelHTTP3PassThrough(t *testing.T) {
	t.Parallel()
	t.Run("zero budget", func(t *testing.T) {
		t.Parallel()
		client := &Client{http3: &fakeHTTP3Client{silent: true}}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := client.openTunnelHTTP3(ctx, testTunnelRequest())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotErrorIs(t, err, ErrHTTP3Unavailable)
	})
	for _, budget := range []time.Duration{0, time.Second} {
		t.Run("fast success budget "+budget.String(), func(t *testing.T) {
			t.Parallel()
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			expected := &fakeDatagramStream{Conn: local}
			client := &Client{
				http3:    &fakeHTTP3Client{openStream: expected},
				tunnelLX: TunnelLXOptions{HTTP3ConnectBudget: budget},
			}
			stream, err := client.openTunnelHTTP3(context.Background(), testTunnelRequest())
			require.NoError(t, err)
			require.Same(t, expected, stream)
		})
	}
}

// pipeDialer hands out the client end of a net.Pipe and serves an HTTP/1.1
// Upgrade on the other end, recording the request.
type pipeDialer struct {
	requests chan *http.Request
}

func (d *pipeDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	clientConn, serverConn := net.Pipe()
	go func() {
		request, err := http.ReadRequest(std_bufio.NewReader(serverConn))
		if err != nil {
			serverConn.Close()
			return
		}
		d.requests <- request
		_, _ = serverConn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + request.Header.Get("Upgrade") + "\r\n\r\n"))
	}()
	return clientConn, nil
}

func (d *pipeDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("not implemented")
}

var _ N.Dialer = (*pipeDialer)(nil)

func TestOpenTunnelSilentHTTP3FallsBack(t *testing.T) {
	t.Parallel()
	// No TLS dialer here, so the fallback lands on HTTP/1.1 rather than
	// HTTP/2; the decision under test (silent HTTP/3 -> mark broken -> next
	// version) happens before that choice.
	dialer := &pipeDialer{requests: make(chan *http.Request, 1)}
	http3 := &fakeHTTP3Client{silent: true}
	client := &Client{
		http1Dialer: dialer,
		server:      M.ParseSocksaddr("127.0.0.1:443"),
		http3:       http3,
		tunnelLX:    TunnelLXOptions{HTTP3ConnectBudget: 50 * time.Millisecond},
	}
	require.True(t, client.http3Available())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, stream, err := client.openTunnel(ctx, testTunnelRequest())
	require.NoError(t, err)
	require.Nil(t, stream)
	require.NotNil(t, conn)
	defer conn.Close()
	require.False(t, client.http3Available())
	require.EqualValues(t, 1, http3.calls.Load())
	request := <-dialer.requests
	require.Equal(t, "cf-connect-ip", request.Header.Get("Upgrade"))

	// While HTTP/3 is marked broken the next tunnel does not try it.
	conn, _, err = client.openTunnel(ctx, testTunnelRequest())
	require.NoError(t, err)
	conn.Close()
	<-dialer.requests
	require.EqualValues(t, 1, http3.calls.Load())
}

func TestOpenTunnelNoHTTP1(t *testing.T) {
	t.Parallel()
	// Same descent as above, but the profile has no HTTP/1.1 tunnel: the
	// caller gets the refusal, and no Upgrade request reaches the server.
	dialer := &pipeDialer{requests: make(chan *http.Request, 1)}
	client := &Client{
		http1Dialer: dialer,
		server:      M.ParseSocksaddr("127.0.0.1:443"),
		http3:       &fakeHTTP3Client{silent: true},
		tunnelLX:    TunnelLXOptions{HTTP3ConnectBudget: 50 * time.Millisecond, NoHTTP1: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, stream, err := client.openTunnel(ctx, testTunnelRequest())
	require.ErrorIs(t, err, ErrTunnelHTTP1Refused)
	require.Nil(t, conn)
	require.Nil(t, stream)
	select {
	case <-dialer.requests:
		t.Fatal("HTTP/1.1 request was sent")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTunnelHeadersHTTP2(t *testing.T) {
	t.Parallel()
	plainHeaders := http.Header{
		"Cf-Connect-Proto": []string{"cf-connect-ip"},
		"Pq-Enabled":       []string{"false"},
	}
	for _, testCase := range []struct {
		name     string
		options  TunnelLXOptions
		initial  http.Header
		expected http.Header
	}{
		{
			name:     "extended connect",
			initial:  http.Header{},
			expected: http.Header{":protocol": []string{"connect-ip"}},
		},
		{
			name:     "extended connect ignores plain headers",
			options:  TunnelLXOptions{PlainConnectHeaders: plainHeaders},
			initial:  http.Header{},
			expected: http.Header{":protocol": []string{"connect-ip"}},
		},
		{
			name:    "plain connect",
			options: TunnelLXOptions{PlainConnectHTTP2: true, PlainConnectHeaders: plainHeaders},
			initial: http.Header{},
			expected: http.Header{
				"Cf-Connect-Proto": []string{"cf-connect-ip"},
				"Pq-Enabled":       []string{"false"},
			},
		},
		{
			name:    "plain connect keeps user headers",
			options: TunnelLXOptions{PlainConnectHTTP2: true, PlainConnectHeaders: plainHeaders},
			initial: http.Header{"Pq-Enabled": []string{"true"}, "User-Agent": []string{"test"}},
			expected: http.Header{
				"Cf-Connect-Proto": []string{"cf-connect-ip"},
				"Pq-Enabled":       []string{"true"},
				"User-Agent":       []string{"test"},
			},
		},
		{
			name:     "plain connect without headers",
			options:  TunnelLXOptions{PlainConnectHTTP2: true},
			initial:  http.Header{},
			expected: http.Header{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := &Client{tunnelLX: testCase.options}
			header := testCase.initial.Clone()
			client.tunnelHeadersHTTP2(header, "connect-ip")
			require.Equal(t, testCase.expected, header)
		})
	}
}
