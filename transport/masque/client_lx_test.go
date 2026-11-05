package masque

import (
	std_bufio "bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	transportHTTP "github.com/sagernet/sing-box/transport/http"
	"github.com/sagernet/sing-tun/gtcpip/header"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

// testClientHandler records what the tunnel client hands to the device.
type testClientHandler struct {
	access         sync.Mutex
	configurations []Configuration
	packets        chan []byte
}

func newTestClientHandler() *testClientHandler {
	return &testClientHandler{packets: make(chan []byte, 16)}
}

func (h *testClientHandler) UpdateConfiguration(configuration Configuration) error {
	h.access.Lock()
	defer h.access.Unlock()
	h.configurations = append(h.configurations, configuration)
	return nil
}

func (h *testClientHandler) WriteInboundBuffers(packetBuffers []*buf.Buffer) error {
	return h.WaitWriteInboundBuffers(packetBuffers)
}

func (h *testClientHandler) WaitWriteInboundBuffers(packetBuffers []*buf.Buffer) error {
	for _, buffer := range packetBuffers {
		h.packets <- bytes.Clone(buffer.Bytes())
		buffer.Release()
	}
	return nil
}

func (h *testClientHandler) FrontHeadroom() int {
	return PacketHeadroom
}

func (h *testClientHandler) recorded() []Configuration {
	h.access.Lock()
	defer h.access.Unlock()
	return append([]Configuration(nil), h.configurations...)
}

// testStream is a stream transport: reads come from input, writes are kept.
type testStream struct {
	input  io.Reader
	access sync.Mutex
	output bytes.Buffer
}

func (s *testStream) Read(p []byte) (int, error) {
	return s.input.Read(p)
}

func (s *testStream) Write(p []byte) (int, error) {
	s.access.Lock()
	defer s.access.Unlock()
	return s.output.Write(p)
}

func (s *testStream) Close() error { return nil }

func (s *testStream) written() []byte {
	s.access.Lock()
	defer s.access.Unlock()
	return bytes.Clone(s.output.Bytes())
}

// testSessionHandler records packets the session reads off the stream.
type testSessionHandler struct {
	packets [][]byte
}

func (h *testSessionHandler) handleAddressAssign(addresses []AssignedAddress) error  { return nil }
func (h *testSessionHandler) handleAddressRequest(addresses []AssignedAddress) error { return nil }
func (h *testSessionHandler) handleRouteAdvertisement(routes []AddressRange) error   { return nil }
func (h *testSessionHandler) handlePacketTooBig(buffer *buf.Buffer, mtu int)         { buffer.Release() }

func (h *testSessionHandler) handlePacket(buffer *buf.Buffer) {
	h.packets = append(h.packets, bytes.Clone(buffer.Bytes()))
	buffer.Release()
}

func newTestClient(lx ClientLXOptions, handler ClientHandler) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		ctx:          ctx,
		cancel:       cancel,
		logger:       logger.NOP(),
		handler:      handler,
		stateUpdated: make(chan struct{}),
		lx:           lx,
	}
}

func newTestClientSession(client *Client, stream io.ReadWriteCloser) *clientSession {
	current := &clientSession{client: client}
	current.session = newSession(client.ctx, stream, current, client.handler.FrontHeadroom)
	return current
}

// testIPv4Packet builds a minimal valid IPv4/UDP packet.
func testIPv4Packet(source, destination netip.Addr) []byte {
	packet := make([]byte, header.IPv4MinimumSize+header.UDPMinimumSize)
	ipHeader := header.IPv4(packet)
	ipHeader.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(packet)),
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     source,
		DstAddr:     destination,
	})
	ipHeader.SetChecksum(^ipHeader.CalculateChecksum())
	return packet
}

func appendCapsule(b []byte, capsuleType uint64, payload []byte) []byte {
	b = appendVarint(b, capsuleType)
	b = appendVarint(b, uint64(len(payload)))
	return append(b, payload...)
}

func appendVarint(b []byte, value uint64) []byte {
	encoded := make([]byte, transportHTTP.VarintLen(value))
	transportHTTP.PutVarint(encoded, value)
	return append(b, encoded...)
}

// readCapsules splits a byte stream into capsules.
func readCapsules(t *testing.T, b []byte) (types []uint64, payloads [][]byte) {
	t.Helper()
	for len(b) > 0 {
		capsuleType, typeLength, valid := transportHTTP.DecodeVarint(b)
		require.True(t, valid)
		b = b[typeLength:]
		length, lengthLength, valid := transportHTTP.DecodeVarint(b)
		require.True(t, valid)
		b = b[lengthLength:]
		require.GreaterOrEqual(t, uint64(len(b)), length)
		types = append(types, capsuleType)
		payloads = append(payloads, b[:length])
		b = b[length:]
	}
	return
}

func TestClientTunnelProtocol(t *testing.T) {
	t.Parallel()
	require.Equal(t, "connect-ip", newTestClient(ClientLXOptions{}, nil).tunnelProtocol())
	require.Equal(t, "cf-connect-ip", newTestClient(ClientLXOptions{Protocol: "cf-connect-ip"}, nil).tunnelProtocol())
}

func TestRequestOrApplyAddressStatic(t *testing.T) {
	t.Parallel()
	static := []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32"), netip.MustParsePrefix("2606:4700:110:8a36::1/128")}
	handler := newTestClientHandler()
	client := newTestClient(ClientLXOptions{Address: static}, handler)
	stream := &testStream{input: bytes.NewReader(nil)}
	current := newTestClientSession(client, stream)
	require.NoError(t, current.requestOrApplyAddress())
	require.Empty(t, stream.written())
	require.Equal(t, []Configuration{{Address: static}}, handler.recorded())
	current.access.Lock()
	require.True(t, current.ready)
	require.Equal(t, static, current.configuration.Address)
	current.access.Unlock()
}

func TestRequestOrApplyAddressRequest(t *testing.T) {
	t.Parallel()
	handler := newTestClientHandler()
	client := newTestClient(ClientLXOptions{}, handler)
	stream := &testStream{input: bytes.NewReader(nil)}
	current := newTestClientSession(client, stream)
	require.NoError(t, current.requestOrApplyAddress())
	types, payloads := readCapsules(t, stream.written())
	require.Equal(t, []uint64{capsuleTypeAddressRequest}, types)
	addresses, err := parseAddresses(payloads[0])
	require.NoError(t, err)
	require.Equal(t, []AssignedAddress{
		{RequestID: 1, Prefix: netip.MustParsePrefix("0.0.0.0/32")},
		{RequestID: 2, Prefix: netip.MustParsePrefix("::/128")},
	}, addresses)
	require.Empty(t, handler.recorded())
	current.access.Lock()
	require.False(t, current.ready)
	current.access.Unlock()
}

func TestIgnoreAddressAssign(t *testing.T) {
	t.Parallel()
	static := []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")}
	assign := []AssignedAddress{{RequestID: 1, Prefix: netip.MustParsePrefix("10.0.0.7/32")}}

	t.Run("static", func(t *testing.T) {
		t.Parallel()
		handler := newTestClientHandler()
		client := newTestClient(ClientLXOptions{Address: static}, handler)
		require.True(t, client.ignoreAddressAssign(assign))
		current := newTestClientSession(client, &testStream{input: bytes.NewReader(nil)})
		require.NoError(t, current.requestOrApplyAddress())
		require.NoError(t, current.handleAddressAssign(assign))
		require.Equal(t, []Configuration{{Address: static}}, handler.recorded())
		current.access.Lock()
		require.Equal(t, static, current.configuration.Address)
		current.access.Unlock()
	})
	t.Run("assigned", func(t *testing.T) {
		t.Parallel()
		handler := newTestClientHandler()
		client := newTestClient(ClientLXOptions{}, handler)
		require.False(t, client.ignoreAddressAssign(assign))
		current := newTestClientSession(client, &testStream{input: bytes.NewReader(nil)})
		require.NoError(t, current.handleAddressAssign(assign))
		require.Equal(t, []Configuration{{Address: []netip.Prefix{netip.MustParsePrefix("10.0.0.7/32")}}}, handler.recorded())
	})
}

func TestRawCapsulesRead(t *testing.T) {
	t.Parallel()
	packet := testIPv4Packet(netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("172.16.0.2"))
	require.Equal(t, byte(0x45), packet[0])
	for _, testCase := range []struct {
		name        string
		rawCapsules bool
		payload     []byte
		expected    [][]byte
	}{
		{"raw bare packet", true, packet, [][]byte{packet}},
		{"standard bare packet is dropped", false, packet, nil},
		{"standard context id", false, append([]byte{0}, packet...), [][]byte{packet}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := appendCapsule(nil, transportHTTP.CapsuleTypeDatagram, testCase.payload)
			handler := &testSessionHandler{}
			current := newSession(context.Background(), &testStream{input: bytes.NewReader(input)}, handler, func() int { return PacketHeadroom })
			current.rawCapsules = testCase.rawCapsules
			require.ErrorIs(t, current.loopCapsule(), io.EOF)
			require.Equal(t, testCase.expected, handler.packets)
		})
	}
}

func TestRawCapsulesWrite(t *testing.T) {
	t.Parallel()
	packet := testIPv4Packet(netip.MustParseAddr("172.16.0.2"), netip.MustParseAddr("1.1.1.1"))
	for _, testCase := range []struct {
		name        string
		rawCapsules bool
		expected    []byte
	}{
		{"raw", true, packet},
		{"standard", false, append([]byte{0}, packet...)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stream := &testStream{input: bytes.NewReader(nil)}
			current := newSession(context.Background(), stream, &testSessionHandler{}, func() int { return PacketHeadroom })
			current.rawCapsules = testCase.rawCapsules
			buffer := buf.NewSize(PacketHeadroom + len(packet))
			buffer.Resize(PacketHeadroom, 0)
			_, _ = buffer.Write(packet)
			require.NoError(t, current.writePackets([]*buf.Buffer{buffer}))
			types, payloads := readCapsules(t, stream.written())
			require.Equal(t, []uint64{transportHTTP.CapsuleTypeDatagram}, types)
			require.Equal(t, testCase.expected, payloads[0])
		})
	}
}

// upgradeDialer serves one HTTP/1.1 Upgrade over a net.Pipe and hands the
// server end of the tunnel to the test.
type upgradeDialer struct {
	requests chan *http.Request
	tunnels  chan net.Conn
}

func (d *upgradeDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	clientConn, serverConn := net.Pipe()
	go func() {
		request, err := http.ReadRequest(std_bufio.NewReader(serverConn))
		if err != nil {
			serverConn.Close()
			return
		}
		d.requests <- request
		_, err = serverConn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + request.Header.Get("Upgrade") + "\r\n\r\n"))
		if err != nil {
			serverConn.Close()
			return
		}
		d.tunnels <- serverConn
	}()
	return clientConn, nil
}

func (d *upgradeDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

// TestClientWARPSessionOverHTTP1 drives a whole client session the way WARP
// is spoken on a stream transport: cf-connect-ip token, static addresses (no
// ADDRESS_REQUEST), bare packets in DATAGRAM capsules both ways.
func TestClientWARPSessionOverHTTP1(t *testing.T) {
	t.Parallel()
	dialer := &upgradeDialer{requests: make(chan *http.Request, 1), tunnels: make(chan net.Conn, 1)}
	httpClient, err := transportHTTP.NewClient(transportHTTP.ClientOptions{
		Dialer:  dialer,
		Server:  M.ParseSocksaddr("127.0.0.1:443"),
		Version: 1,
	})
	require.NoError(t, err)
	static := []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")}
	handler := newTestClientHandler()
	client, err := NewClient(ClientOptions{
		Context:    context.Background(),
		Logger:     logger.NOP(),
		HTTPClient: httpClient,
		Path:       "/",
		Handler:    handler,
		LX: ClientLXOptions{
			Protocol:          "cf-connect-ip",
			Address:           static,
			RawStreamCapsules: true,
		},
	})
	require.NoError(t, err)
	client.Start()
	defer client.Close()

	request := <-dialer.requests
	require.Equal(t, "cf-connect-ip", request.Header.Get("Upgrade"))
	tunnel := <-dialer.tunnels
	defer tunnel.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.WaitReady(ctx))
	require.Equal(t, []Configuration{{Address: static}}, handler.recorded())

	// Server to client: a bare packet in a DATAGRAM capsule reaches the device.
	inbound := testIPv4Packet(netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("172.16.0.2"))
	go func() {
		_, _ = tunnel.Write(appendCapsule(nil, transportHTTP.CapsuleTypeDatagram, inbound))
	}()
	select {
	case packet := <-handler.packets:
		require.Equal(t, inbound, packet)
	case <-ctx.Done():
		t.Fatal("inbound packet not delivered")
	}

	// Client to server: the first bytes on the wire are a DATAGRAM capsule
	// with the bare packet, not an ADDRESS_REQUEST.
	outbound := testIPv4Packet(netip.MustParseAddr("172.16.0.2"), netip.MustParseAddr("1.1.1.1"))
	buffer := buf.NewSize(PacketHeadroom + len(outbound))
	buffer.Resize(PacketHeadroom, 0)
	_, _ = buffer.Write(outbound)
	go func() {
		_ = client.WritePacketBuffers([]*buf.Buffer{buffer}, false)
	}()
	expected := appendCapsule(nil, transportHTTP.CapsuleTypeDatagram, outbound)
	received := make([]byte, len(expected))
	require.NoError(t, tunnel.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadFull(tunnel, received)
	require.NoError(t, err)
	require.Equal(t, expected, received)
}
