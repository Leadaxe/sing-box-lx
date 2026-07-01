package masque

// HTTP/2 CONNECT-IP path. Adapted from mihomo's transport/masque/client_h2.go
// (itself from Diniboy1123/connect-ip-go).
//
// WARP CONNECT-IP over h2 is an *extended* CONNECT (a CONNECT carrying the
// :protocol pseudo-header). Neither stdlib net/http nor golang.org/x/net/http2's
// high-level ClientConn will drive it against WARP: the latter refuses with
// "extended connect not supported by peer" because WARP never advertises
// SETTINGS_ENABLE_CONNECT_PROTOCOL (the same RFC-non-compliance it shows on h3).
//
// So we drive the HTTP/2 connection manually with x/net/http2's public Framer +
// hpack (both already dependencies): client preface, our SETTINGS, one HEADERS
// frame for the extended CONNECT, then DATA frames carrying capsule DATAGRAM
// frames (type 0). This skips the peer-settings gate entirely. No http fork.
//
// Unlike HTTP/3, HTTP/2 has no native datagrams: proxied IP packets are carried
// as HTTP capsule DATAGRAM frames inside the CONNECT stream's DATA. TTL/checksum
// handling mirrors the HTTP/3 path.

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"sync"

	"github.com/sagernet/quic-go/quicvarint"
	E "github.com/sagernet/sing/common/exceptions"

	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const h2DatagramCapsuleType uint64 = 0

const (
	ipv4HeaderLen = 20
	ipv6HeaderLen = 40
)

const (
	h2StreamID             = 1
	h2InitialWindowSize    = 8 << 20 // 8 MiB receive window (real flow-control backpressure)
	h2SettingEnablePush    = 0x2
	h2SettingInitialWindow = 0x4
	// maxCapsulePayload bounds a single peer-declared capsule DATAGRAM payload.
	// payloadLen is a peer-controlled varint (up to 2^62-1); without a ceiling
	// `int(payloadLen)` can wrap negative on 32-bit and make([]byte, n) can OOM.
	// A proxied IP packet never exceeds jumbo-frame size; cap generously.
	// lx: SPEC 021 A4/A5/B5.
	maxCapsulePayload = 65535
)

// ConnectTunnelH2 establishes a CONNECT-IP tunnel over an already-handshaked
// HTTP/2 TLS connection (ALPN "h2"). It takes ownership of tlsConn.
func ConnectTunnelH2(ctx context.Context, profile Profile, tlsConn net.Conn, connectURI string) (io.Closer, IpConn, error) {
	u, err := url.Parse(connectURI)
	if err != nil {
		_ = tlsConn.Close()
		return nil, nil, E.Cause(err, "parse connect uri")
	}

	conn := &h2RawConn{
		tlsConn: tlsConn,
		framer:  xhttp2.NewFramer(tlsConn, tlsConn),
		recvCh:  make(chan []byte, 8),
		errCh:   make(chan error, 1),
		closed:  make(chan struct{}),
	}
	conn.hpackBuf = new(bytes.Buffer)
	conn.hpackEnc = hpack.NewEncoder(conn.hpackBuf)

	// Client connection preface + our SETTINGS.
	if _, err = io.WriteString(tlsConn, xhttp2.ClientPreface); err != nil {
		_ = tlsConn.Close()
		return nil, nil, E.Cause(err, "write h2 preface")
	}
	if err = conn.framer.WriteSettings(
		xhttp2.Setting{ID: h2SettingEnablePush, Val: 0},
		xhttp2.Setting{ID: h2SettingInitialWindow, Val: h2InitialWindowSize},
	); err != nil {
		_ = tlsConn.Close()
		return nil, nil, E.Cause(err, "write h2 settings")
	}
	// Grow the connection-level receive window too.
	if err = conn.framer.WriteWindowUpdate(0, h2InitialWindowSize); err != nil {
		_ = tlsConn.Close()
		return nil, nil, E.Cause(err, "write h2 window update")
	}

	// Extended CONNECT HEADERS.
	status, err := conn.sendConnect(profile, u)
	if err != nil {
		_ = tlsConn.Close()
		return nil, nil, err
	}
	if status < 200 || status > 299 {
		_ = tlsConn.Close()
		return nil, nil, E.New("connect-ip: server responded with ", status)
	}

	go conn.readLoop()

	return conn, &h2IpConn{str: conn, closeChan: make(chan struct{})}, nil
}

// h2RawConn drives a single extended-CONNECT stream over a raw HTTP/2 conn.
type h2RawConn struct {
	tlsConn net.Conn
	framer  *xhttp2.Framer

	hpackBuf *bytes.Buffer
	hpackEnc *hpack.Encoder

	writeMu sync.Mutex

	recvCh chan []byte // reassembled DATA payload chunks
	errCh  chan error
	closed chan struct{}

	closeOnce sync.Once
}

func (c *h2RawConn) sendConnect(profile Profile, u *url.URL) (int, error) {
	c.hpackBuf.Reset()
	writeField := func(name, value string) {
		_ = c.hpackEnc.WriteField(hpack.HeaderField{Name: name, Value: value})
	}
	// WARP h2 CONNECT-IP is a *plain* CONNECT (only :method + :authority), with
	// the IP tunnel keyed off the cf-connect-proto header — NOT an extended
	// CONNECT with :protocol/:scheme/:path (that triggers PROTOCOL_ERROR).
	writeField(":method", "CONNECT")
	writeField(":authority", authorityFromURL(u))
	writeField("capsule-protocol", "?1")
	writeField("user-agent", "")
	if profile.H2ConnectProto != "" {
		writeField("cf-connect-proto", profile.H2ConnectProto)
		writeField("pq-enabled", "false")
	}

	c.writeMu.Lock()
	err := c.framer.WriteHeaders(xhttp2.HeadersFrameParam{
		StreamID:      h2StreamID,
		BlockFragment: c.hpackBuf.Bytes(),
		EndStream:     false,
		EndHeaders:    true,
	})
	c.writeMu.Unlock()
	if err != nil {
		return 0, E.Cause(err, "write CONNECT headers")
	}

	// Read frames until the response HEADERS for our stream arrives.
	hpackDec := hpack.NewDecoder(4096, nil)
	for {
		frame, err := c.framer.ReadFrame()
		if err != nil {
			return 0, E.Cause(err, "read response")
		}
		switch f := frame.(type) {
		case *xhttp2.SettingsFrame:
			if !f.IsAck() {
				c.writeMu.Lock()
				_ = c.framer.WriteSettingsAck()
				c.writeMu.Unlock()
			}
		case *xhttp2.HeadersFrame:
			if f.StreamID != h2StreamID {
				continue
			}
			fields, err := hpackDec.DecodeFull(f.HeaderBlockFragment())
			if err != nil {
				return 0, E.Cause(err, "decode response headers")
			}
			status := 0
			for _, hf := range fields {
				if hf.Name == ":status" {
					status = parseStatus(hf.Value)
				}
			}
			return status, nil
		case *xhttp2.GoAwayFrame:
			return 0, E.New("connect-ip: server sent GOAWAY: ", f.ErrCode.String())
		case *xhttp2.RSTStreamFrame:
			if f.StreamID == h2StreamID {
				return 0, E.New("connect-ip: stream reset: ", f.ErrCode.String())
			}
		}
	}
}

func (c *h2RawConn) readLoop() {
	defer close(c.recvCh)
	var window int64 = h2InitialWindowSize
	for {
		frame, err := c.framer.ReadFrame()
		if err != nil {
			c.setErr(err)
			return
		}
		switch f := frame.(type) {
		case *xhttp2.DataFrame:
			if f.StreamID != h2StreamID {
				continue
			}
			data := f.Data()
			if len(data) > 0 {
				buf := make([]byte, len(data))
				copy(buf, data)
				select {
				case c.recvCh <- buf:
				case <-c.closed:
					return
				}
				// Replenish flow-control windows.
				window -= int64(len(data))
				if window < h2InitialWindowSize/2 {
					incr := uint32(h2InitialWindowSize - window)
					c.writeMu.Lock()
					_ = c.framer.WriteWindowUpdate(0, incr)
					_ = c.framer.WriteWindowUpdate(h2StreamID, incr)
					c.writeMu.Unlock()
					window = h2InitialWindowSize
				}
			}
			if f.StreamEnded() {
				c.setErr(io.EOF)
				return
			}
		case *xhttp2.SettingsFrame:
			if !f.IsAck() {
				c.writeMu.Lock()
				_ = c.framer.WriteSettingsAck()
				c.writeMu.Unlock()
			}
		case *xhttp2.PingFrame:
			if !f.IsAck() {
				c.writeMu.Lock()
				_ = c.framer.WritePing(true, f.Data)
				c.writeMu.Unlock()
			}
		case *xhttp2.GoAwayFrame:
			c.setErr(E.New("connect-ip: GOAWAY ", f.ErrCode.String()))
			return
		case *xhttp2.RSTStreamFrame:
			if f.StreamID == h2StreamID {
				c.setErr(E.New("connect-ip: RST_STREAM ", f.ErrCode.String()))
				return
			}
		}
	}
}

// writeData sends a capsule payload as one DATA frame on the CONNECT stream.
func (c *h2RawConn) writeData(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.framer.WriteData(h2StreamID, false, b)
}

func (c *h2RawConn) setErr(err error) {
	select {
	case c.errCh <- err:
	default:
	}
}

func (c *h2RawConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.writeMu.Lock()
		_ = c.framer.WriteRSTStream(h2StreamID, xhttp2.ErrCodeCancel)
		c.writeMu.Unlock()
		_ = c.tlsConn.Close()
	})
	return nil
}

func parseStatus(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func authorityFromURL(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	host := u.Hostname()
	if host == "" {
		return u.Host
	}
	return host + ":443"
}

// h2IpConn adapts the raw h2 CONNECT stream to IpConn, framing/parsing capsule
// DATAGRAM frames over the DATA stream.
type h2IpConn struct {
	str *h2RawConn

	readBuf  []byte // leftover bytes spanning DATA frames
	writeBuf []byte // reusable scratch for the outgoing capsule frame (tx pump only)

	mu        sync.Mutex
	closeChan chan struct{}
	closeErr  error
}

func (c *h2IpConn) ReadPacket() (b []byte, err error) {
start:
	data, err := c.receiveDatagram()
	if err != nil {
		defer func() { _ = c.Close() }()
		select {
		case <-c.closeChan:
			return nil, c.closeErr
		default:
			return nil, err
		}
	}
	if err := validateProxiedPacket(data); err != nil {
		goto start
	}
	return data, nil
}

// receiveDatagram reads one capsule DATAGRAM payload, reassembling across DATA
// frame boundaries.
func (c *h2IpConn) receiveDatagram() ([]byte, error) {
	for {
		// Try to parse a full capsule from the buffer.
		if len(c.readBuf) > 0 {
			r := bytes.NewReader(c.readBuf)
			vr := quicvarint.NewReader(r)
			capsuleType, err1 := quicvarint.Read(vr)
			payloadLen, err2 := quicvarint.Read(vr)
			if err1 == nil && err2 == nil {
				if payloadLen > maxCapsulePayload {
					// Peer declared an implausibly large capsule — treat as a
					// protocol violation rather than allocating/wrapping.
					return nil, E.New("connect-ip: capsule payload too large: ", payloadLen)
				}
				headerLen := len(c.readBuf) - r.Len()
				total := headerLen + int(payloadLen)
				if len(c.readBuf) >= total {
					payload := make([]byte, payloadLen)
					copy(payload, c.readBuf[headerLen:total])
					c.readBuf = c.readBuf[total:]
					if capsuleType != h2DatagramCapsuleType {
						continue
					}
					return payload, nil
				}
			}
		}
		// Need more bytes.
		chunk, ok := <-c.str.recvCh
		if !ok {
			select {
			case err := <-c.str.errCh:
				return nil, err
			default:
				return nil, io.EOF
			}
		}
		c.readBuf = append(c.readBuf, chunk...)
	}
}

func (c *h2IpConn) WritePacket(b []byte) (icmp []byte, err error) {
	data, err := composeH2Datagram(b)
	if err != nil || data == nil {
		return nil, nil
	}
	// Reuse writeBuf: WritePacket is only called from the single tx pump, and
	// writeData writes the frame to the wire before returning, so the scratch is
	// free to reuse next call. lx: SPEC 021 B2.
	need := quicvarint.Len(h2DatagramCapsuleType) + quicvarint.Len(uint64(len(data))) + len(data)
	if cap(c.writeBuf) < need {
		c.writeBuf = make([]byte, 0, need)
	}
	frame := c.writeBuf[:0]
	frame = quicvarint.Append(frame, h2DatagramCapsuleType)
	frame = quicvarint.Append(frame, uint64(len(data)))
	frame = append(frame, data...)
	c.writeBuf = frame
	if err := c.str.writeData(frame); err != nil {
		select {
		case <-c.closeChan:
			return nil, c.closeErr
		default:
			return nil, err
		}
	}
	return nil, nil
}

func (c *h2IpConn) Close() error {
	c.mu.Lock()
	if c.closeErr == nil {
		c.closeErr = io.ErrClosedPipe
		close(c.closeChan)
	}
	c.mu.Unlock()
	return c.str.Close()
}

func validateProxiedPacket(data []byte) error {
	if len(data) == 0 {
		return E.New("connect-ip: empty packet")
	}
	switch v := ipVersion(data); v {
	default:
		return E.New("connect-ip: unknown IP version: ", v)
	case 4:
		if len(data) < ipv4HeaderLen {
			return E.New("connect-ip: malformed datagram: too short")
		}
	case 6:
		if len(data) < ipv6HeaderLen {
			return E.New("connect-ip: malformed datagram: too short")
		}
	}
	return nil
}

func composeH2Datagram(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	switch v := ipVersion(b); v {
	default:
		return nil, E.New("connect-ip: unknown IP version: ", v)
	case 4:
		if len(b) < ipv4HeaderLen {
			return nil, E.New("connect-ip: IPv4 packet too short")
		}
		ttl := b[8]
		if ttl <= 1 {
			return nil, E.New("connect-ip: datagram TTL too small")
		}
		b[8]--
		binary.BigEndian.PutUint16(b[10:12], ipv4Checksum(b[:ipv4HeaderLen]))
	case 6:
		if len(b) < ipv6HeaderLen {
			return nil, E.New("connect-ip: IPv6 packet too short")
		}
		hopLimit := b[7]
		if hopLimit <= 1 {
			return nil, E.New("connect-ip: datagram Hop Limit too small")
		}
		b[7]--
	}
	return b, nil
}

func ipVersion(b []byte) uint8 { return b[0] >> 4 }

func ipv4Checksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i < ipv4HeaderLen; i += 2 {
		if i == 10 {
			continue
		}
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for (sum >> 16) > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
